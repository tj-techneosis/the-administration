package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// cmdVerify checks a closed contract's findings in the house: walled
// verifiers, each given a batch of findings and the project's code to read
// (and nothing else), say whether the code bears each out. Their verdicts
// go into the books like the session's, marked as theirs, and the summary
// shows them; a verdict the session records itself replaces a verifier's.
// Findings already checked are left alone.
func cmdVerify(args []string) error {
	f, err := parseFlags(args, map[string]bool{"model": true, "coins": true, "batch": true})
	if err != nil {
		return err
	}
	if len(f.pos) != 1 {
		return usageError{"verify ID [--batch 12] [--coins 10m] [--model M]"}
	}
	c, dir, err := loadContract(f.pos[0])
	if err != nil {
		return err
	}
	if c.ClosedAt == nil {
		return usageError{fmt.Sprintf("contract %s isn't closed yet: close it, then verify", c.ID)}
	}
	// One at a time: two would check the same findings and write over each
	// other's verdicts.
	lockf, err := os.OpenFile(filepath.Join(dir, ".verify.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lockf.Close()
	if err := syscall.Flock(int(lockf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return usageError{fmt.Sprintf("verify is already running on contract %s; wait for it", c.ID)}
	}
	batch := 12
	if v := f.get("batch"); v != "" {
		if batch, err = strconv.Atoi(v); err != nil || batch < 1 || batch > 50 {
			return usageError{"--batch: a number from 1 to 50"}
		}
	}
	coins := 10 * time.Minute
	if v := f.get("coins"); v != "" {
		if coins, err = parseCoins(v); err != nil {
			return usageError{err.Error()}
		}
	}
	model := f.get("model")
	if model != "" && !modelRE.MatchString(model) {
		return usageError{"--model: a model name or alias"}
	}
	all, _ := gatherFindings(c, dir)
	var todo []*summed
	for _, s := range all {
		if s.verdict == "" {
			todo = append(todo, s)
		}
	}
	if len(todo) == 0 {
		fmt.Printf("Every finding on contract %s is checked already.\n", c.ID)
		return nil
	}
	var batches [][]*summed
	for i := 0; i < len(todo); i += batch {
		batches = append(batches, todo[i:min(i+batch, len(todo))])
	}
	fmt.Printf("Verifying %s on contract %s: %s, up to four at once.\n", plural(len(todo), "finding", "findings"), c.ID, plural(len(batches), "verifier", "verifiers"))

	var mu sync.Mutex
	got := map[*summed]verdict{}
	var failed []string
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for k, b := range batches {
		wg.Add(1)
		go func(k int, b []*summed) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			vs, why := verifyBatch(c, filepath.Join(dir, ".verify", strconv.Itoa(k+1)), b, model, coins)
			mu.Lock()
			defer mu.Unlock()
			if why != "" {
				failed = append(failed, fmt.Sprintf("verifier %d: %s", k+1, why))
			}
			for s, v := range vs {
				got[s] = v
			}
		}(k, b)
	}
	wg.Wait()
	_ = os.Remove(filepath.Join(dir, ".verify")) // empty unless a verifier failed

	// Into each professional's verdicts, under every name that found it; a
	// verdict of the session's is never replaced.
	byName := map[string][]verdict{}
	for s, v := range got {
		for _, ref := range s.by {
			m := ownRef.FindStringSubmatch(strings.TrimSuffix(ref, ", failed"))
			if m == nil {
				continue
			}
			n, _ := strconv.Atoi(m[2])
			byName[m[1]] = append(byName[m[1]], verdict{Finding: n, Verdict: v.Verdict, Why: v.Why, By: "verifier"})
		}
	}
	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		var old []verdict
		if b, err := os.ReadFile(filepath.Join(dir, name, "verdicts.json")); err == nil {
			_ = json.Unmarshal(b, &old)
		}
		have := map[int]verdict{}
		for _, v := range old {
			have[v.Finding] = v
		}
		for _, v := range byName[name] {
			if o, ok := have[v.Finding]; !ok || o.By == "verifier" {
				have[v.Finding] = v
			}
		}
		merged := make([]verdict, 0, len(have))
		for _, v := range have {
			merged = append(merged, v)
		}
		sort.Slice(merged, func(i, j int) bool { return merged[i].Finding < merged[j].Finding })
		if err := writeJSON(filepath.Join(dir, name, "verdicts.json"), merged); err != nil {
			return err
		}
	}
	if _, err := writeSummary(c, dir); err != nil {
		return err
	}
	count := map[string]int{}
	for _, v := range got {
		count[v.Verdict]++
	}
	fmt.Printf("Checked %d of %d: %d confirmed, %d likely, %d false.\n", len(got), len(todo), count["confirmed"], count["likely"], count["false"])
	for _, f := range failed {
		fmt.Println("  " + f)
	}
	fmt.Println(filepath.Join(dir, "summary.md"))
	if len(failed) > 0 {
		return startError{fmt.Sprintf("%s didn't finish; their findings are still unchecked", plural(len(failed), "verifier", "verifiers"))}
	}
	return nil
}

// verifyBatch runs one walled verifier on a batch and returns its verdicts,
// or why it gave none. Its folder is kept when it fails.
func verifyBatch(c *contract, rd string, batch []*summed, model string, coins time.Duration) (map[*summed]verdict, string) {
	if err := os.MkdirAll(rd, 0o700); err != nil {
		return nil, err.Error()
	}
	type item struct {
		ID   string   `json:"id"`
		Text string   `json:"text"`
		By   []string `json:"found_by"`
	}
	ids := map[string]*summed{}
	var items []item
	for i, s := range batch {
		id := fmt.Sprintf("F%d", i+1)
		ids[id] = s
		items = append(items, item{id, s.text, s.by})
	}
	in := map[string]any{"project": c.Project, "target": c.Target, "findings": items}
	addDirs := []string{"--add-dir", c.Project}
	if c.Changes != nil && c.Changes.Patch != "" {
		in["patch"] = c.Changes.Patch
		addDirs = append(addDirs, "--add-dir", filepath.Dir(c.Changes.Patch))
	}
	prompt, _ := fs.ReadFile(house, "house/verifier.prompt")
	if os.WriteFile(filepath.Join(rd, "verifier.md"), prompt, 0o600) != nil {
		return nil, "could not write its instructions"
	}
	// Walled as the Director is: read-only tools, no shell, no web, no
	// secrets, nothing written.
	deny := []string{"Bash", "WebFetch", "WebSearch", "NotebookEdit", "Agent", "Write", "Edit"}
	for _, g := range secretGlobs {
		deny = append(deny, "Read(**/"+g+")")
	}
	settings := filepath.Join(rd, "settings.json")
	_ = writeJSON(settings, map[string]any{
		"permissions":     map[string]any{"deny": deny, "disableBypassPermissionsMode": "disable"},
		"disableAllHooks": true, "autoMemoryEnabled": false,
	})
	body, _ := json.Marshal(in)
	args := append([]string{"-p", string(body),
		"--restricted", "--strict-mcp-config", "--settings", settings, "--no-session-persistence",
		"--permission-prompts", "none", "--output-format", "stream-json", "--verbose",
		"--tools", "Read,Grep,Glob", "--append-system-prompt-file", filepath.Join(rd, "verifier.md")}, addDirs...)
	if model != "" {
		args = append(args, "--model", model)
	}
	rep, state, reason := runHeadless(rd, args, newTag(c.ID, "verifier"), coins)
	if state != "done" {
		return nil, fmt.Sprintf("%s; its stream is in %s", reason, rd)
	}
	text := rep.final
	i, j := strings.Index(text, "["), strings.LastIndex(text, "]")
	var answers []struct {
		ID      string `json:"id"`
		Verdict string `json:"verdict"`
		Why     string `json:"why"`
	}
	if i < 0 || j < i || json.Unmarshal([]byte(text[i:j+1]), &answers) != nil {
		return nil, "its answer wasn't a list of verdicts; its stream is in " + rd
	}
	out := map[*summed]verdict{}
	for _, a := range answers {
		s := ids[a.ID]
		if s == nil || (a.Verdict != "confirmed" && a.Verdict != "likely" && a.Verdict != "false") {
			continue
		}
		out[s] = verdict{Verdict: a.Verdict, Why: oneLine(a.Why, 400), By: "verifier"}
	}
	_ = os.RemoveAll(rd)
	return out, ""
}
