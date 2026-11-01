package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
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
	// Unchecked findings, and checked ones with a link the matcher proposed
	// that nobody has decided: the verifier judges the link alone.
	all, _ := gatherFindings(c, dir)
	var todo []*summed
	for _, s := range all {
		if s.verdict == "" || len(proposedLinks(s)) > 0 {
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
	unchecked := 0
	for _, s := range todo {
		if s.verdict == "" {
			unchecked++
		}
	}
	forLinks := ""
	if n := len(todo) - unchecked; n > 0 {
		forLinks = fmt.Sprintf(" (%d checked already, for their links)", n)
	}
	fmt.Printf("Verifying %s%s on contract %s: %s, up to four at once.\n", plural(len(todo), "finding", "findings"), forLinks, c.ID, plural(len(batches), "verifier", "verifiers"))

	var mu sync.Mutex
	// Each batch goes into the books as it finishes, so stopping verify
	// part way keeps every verifier that was done.
	got := map[*summed]verdict{}
	var failed []string
	var saveErr error
	links := 0
	touched := map[string]bool{}
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
			n, err := saveVerdicts(c, dir, vs, touched)
			links += n
			if err != nil && saveErr == nil {
				saveErr = err
			}
			// The summary keeps up with the books, so one stopped part way
			// shows what was checked.
			if len(vs) > 0 {
				_, _ = writeSummary(c, dir)
			}
			for s, v := range vs {
				got[s] = v
			}
		}(k, b)
	}
	wg.Wait()
	_ = os.Remove(filepath.Join(dir, ".verify")) // empty unless a verifier failed
	if saveErr != nil {
		return saveErr
	}
	if _, err := writeSummary(c, dir); err != nil {
		return err
	}
	// The other side of each link decided shows it too.
	for id := range touched {
		if oc, odir, err := loadContract(id); err == nil {
			if _, err := os.Stat(filepath.Join(odir, "summary.md")); err == nil {
				_, _ = writeSummary(oc, odir)
			}
		}
	}
	count := map[string]int{}
	for _, v := range got {
		count[v.Verdict]++
	}
	if unchecked > 0 {
		fmt.Printf("Checked %d of %d: %d confirmed, %d likely, %d false.\n", len(got), unchecked, count["confirmed"], count["likely"], count["false"])
	}
	if links > 0 {
		fmt.Printf("Decided %s the matcher proposed.\n", plural(links, "link", "links"))
	}
	all, _ = gatherFindings(c, dir)
	if n := undecidedLinks(all); n > 0 {
		fmt.Println(undecidedLine(n, c.ID))
	}
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
	type twin struct {
		Ref  string `json:"ref"`
		Text string `json:"text"`
	}
	type item struct {
		ID      string   `json:"id"`
		Text    string   `json:"text"`
		By      []string `json:"found_by"`
		Checked string   `json:"already_checked,omitempty"`
		Maybe   []twin   `json:"maybe_same_as,omitempty"`
	}
	ids := map[string]*summed{}
	var items []item
	for i, s := range batch {
		id := fmt.Sprintf("F%d", i+1)
		ids[id] = s
		it := item{ID: id, Text: s.text, By: s.by, Checked: s.verdict}
		for _, l := range proposedLinks(s) {
			if _, t, err := resolveOther(l.To); err == nil {
				it.Maybe = append(it.Maybe, twin{l.To, oneLine(t, 1500)})
			}
		}
		items = append(items, it)
	}
	in := map[string]any{"project": c.Project, "target": c.Target, "findings": items}
	addDirs := []string{"--add-dir", c.Project}
	if c.Changes != nil && c.Changes.Patch != "" {
		in["patch"] = c.Changes.Patch
		if c.Changes.Split != "" {
			in["patch_by_file"] = filepath.Join(c.Changes.Split, "INDEX.md")
		}
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
	recordSpend(filepath.Dir(filepath.Dir(rd)), "verifier", len(batch), model, state, rep)
	if state != "done" {
		return nil, fmt.Sprintf("%s; its stream is in %s", reason, rd)
	}
	text := rep.final
	i, j := strings.Index(text, "["), strings.LastIndex(text, "]")
	var answers []struct {
		ID      string   `json:"id"`
		Verdict string   `json:"verdict"`
		Why     string   `json:"why"`
		Tag     string   `json:"tag"`
		Also    []string `json:"also"`
		NotAlso []string `json:"not_also"`
		LinkWhy string   `json:"link_why"`
	}
	if i < 0 || j < i || json.Unmarshal([]byte(text[i:j+1]), &answers) != nil {
		return nil, "its answer wasn't a list of verdicts; its stream is in " + rd
	}
	out := map[*summed]verdict{}
	for _, a := range answers {
		s := ids[a.ID]
		if s == nil {
			continue
		}
		v := verdict{Verdict: a.Verdict, Why: oneLine(a.Why, 400), By: "verifier", Also: a.Also, NotAlso: a.NotAlso, linkWhy: oneLine(a.LinkWhy, 300)}
		if s.verdict != "" {
			v.Verdict = s.verdict // sent for its links: its check stands
		} else if a.Verdict != "confirmed" && a.Verdict != "likely" && a.Verdict != "false" {
			continue
		}
		// A mark that agrees with the finding's own (UNMARKED for none) is
		// no correction.
		if t, ok := fixTag(a.Tag); ok && t != s.tag && !(t == "UNMARKED" && s.tag == "") && c.Changes != nil {
			v.Tag = t
		}
		out[s] = v
	}
	_ = os.RemoveAll(rd)
	return out, ""
}

// proposedLinks are a finding's links the matcher proposed and nobody has
// decided.
func proposedLinks(s *summed) []link {
	var out []link
	for _, l := range s.links {
		if l.State == "proposed" && !linkGone(l) {
			out = append(out, l)
		}
	}
	return out
}

// linkGone is a link to a contract pruned from the books since: nothing can
// decide it, and nothing counts it.
func linkGone(l link) bool {
	m := otherRef.FindStringSubmatch(l.To)
	if m == nil {
		return true
	}
	dir, err := contractDir(m[1])
	if err != nil {
		return true
	}
	_, err = os.Stat(filepath.Join(dir, "contract.json"))
	return err != nil
}

// decideProposed records a verifier's answers on a finding's proposed links:
// only those it was asked about, each on the professional the link is from.
// A folded finding found by several may have the same link proposed from
// each; the one answer decides them all. It returns how many it decided.
func decideProposed(c *contract, dir string, s *summed, v verdict, touched map[string]bool) int {
	decided := map[string]string{} // other finding: also or not_also
	for _, l := range proposedLinks(s) {
		switch {
		case slices.Contains(v.Also, l.To):
			decided[l.To] = "also"
		case slices.Contains(v.NotAlso, l.To):
			decided[l.To] = "not_also"
		}
	}
	mine := map[string]bool{}
	for _, ref := range s.by {
		mine[strings.TrimSuffix(ref, ", failed")] = true
	}
	n := 0
	for _, l := range readLinks(dir) {
		how := decided[l.To]
		if l.State != "proposed" || how == "" || !mine[l.From] {
			continue
		}
		m := ownRef.FindStringSubmatch(l.From)
		if m == nil {
			continue
		}
		d := verdict{Why: v.linkWhy}
		if d.Why == "" {
			d.Why = v.Why
		}
		d.Finding, _ = strconv.Atoi(m[2])
		if how == "also" {
			d.Also = []string{l.To}
		} else {
			d.NotAlso = []string{l.To}
		}
		if decideLinks(c, dir, m[1], d, "verifier") == nil {
			n++
			touched[otherRef.FindStringSubmatch(l.To)[1]] = true
		}
	}
	return n
}

// saveVerdicts puts one batch's verdicts into each professional's verdicts,
// under every name that found it, and decides the links it answered for; a
// verdict of the session's is never replaced, nor any verdict of a finding
// that was only sent for its links (those leave vs). It returns how many
// proposed links it decided.
func saveVerdicts(c *contract, dir string, vs map[*summed]verdict, touched map[string]bool) (int, error) {
	byName := map[string][]verdict{}
	links := 0
	for s, v := range vs {
		links += decideProposed(c, dir, s, v, touched)
		if s.verdict != "" {
			delete(vs, s)
			continue
		}
		for _, ref := range s.by {
			m := ownRef.FindStringSubmatch(strings.TrimSuffix(ref, ", failed"))
			if m == nil {
				continue
			}
			n, _ := strconv.Atoi(m[2])
			byName[m[1]] = append(byName[m[1]], verdict{Finding: n, Verdict: v.Verdict, Why: v.Why, By: "verifier", Tag: v.Tag})
		}
	}
	for name, add := range byName {
		unlock, err := lockFile(filepath.Join(dir, name, "verdicts.lock"))
		if err != nil {
			return links, err
		}
		var old []verdict
		if b, err := os.ReadFile(filepath.Join(dir, name, "verdicts.json")); err == nil {
			_ = json.Unmarshal(b, &old)
		}
		have := map[int]verdict{}
		for _, v := range old {
			have[v.Finding] = v
		}
		for _, v := range add {
			if o, ok := have[v.Finding]; !ok || o.By == "verifier" {
				have[v.Finding] = v
			}
		}
		merged := make([]verdict, 0, len(have))
		for _, v := range have {
			merged = append(merged, v)
		}
		sort.Slice(merged, func(i, j int) bool { return merged[i].Finding < merged[j].Finding })
		err = writeJSON(filepath.Join(dir, name, "verdicts.json"), merged)
		unlock()
		if err != nil {
			return links, err
		}
	}
	return links, nil
}
