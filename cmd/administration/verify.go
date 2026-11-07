package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
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
	f, err := parseFlags(args, map[string]bool{"model": true, "coins": true, "batch": true, "early": false})
	if err != nil {
		return err
	}
	if len(f.pos) != 1 {
		return usageError{"verify ID [--batch 12] [--coins 10m] [--model M] (most findings are checked as their professional comes back; ADMINISTRATION_VERIFY_EARLY=off leaves them to this)"}
	}
	// --early: started by a worker whose professional is back, while the
	// contract is open (Checking as they come back, below).
	early := f.has("early")
	// While the verifiers read the copy and their verdicts are written:
	// prune would remove either.
	unuse, err := useBooks()
	if err != nil {
		return err
	}
	defer unuse()
	c, dir, err := loadContract(f.pos[0])
	if err != nil {
		return err
	}
	if c.ClosedAt == nil && !early {
		return usageError{fmt.Sprintf("contract %s isn't closed yet: close it, then verify", c.ID)}
	}
	// One at a time: two would check the same findings and write over each
	// other's verdicts.
	lockf, err := os.OpenFile(filepath.Join(dir, ".verify.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lockf.Close()
	if early {
		// One waiting is enough: it checks every seat back by the time it
		// gets its turn, this one's too.
		unpend, ok := tryLock(filepath.Join(dir, ".verify.pending"), syscall.LOCK_EX)
		if !ok {
			return nil
		}
		// Said before it waits, so a close's verify that finds the lock
		// taken knows it's this one (waiting or checking), not a person's.
		mark := filepath.Join(dir, ".verify.early")
		me := strconv.Itoa(os.Getpid())
		_ = os.WriteFile(mark, []byte(me+"\n"), 0o600)
		// Only its own: a later early run may have written its own since.
		defer func() {
			if b, err := os.ReadFile(mark); err == nil && strings.TrimSpace(string(b)) == me {
				_ = os.Remove(mark)
			}
		}()
		err := syscall.Flock(int(lockf.Fd()), syscall.LOCK_EX)
		unpend()
		if err != nil {
			return err
		}
		// Another early run waiting since may have written its mark over
		// this one's: this one is checking now, and says so again.
		_ = os.WriteFile(mark, []byte(me+"\n"), 0o600)
		// Read again: the contract may have closed while this waited.
		if c, dir, err = loadContract(c.ID); err != nil {
			return err
		}
	} else if err := syscall.Flock(int(lockf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if !earlyAlive(dir) {
			return usageError{fmt.Sprintf("verify is already running on contract %s; wait for it", c.ID)}
		}
		// Findings checked as their professionals came back: wait for that
		// to finish, then check what it left.
		fmt.Printf("Waiting for the verifier checking contract %s's findings as they came back...\n", c.ID)
		if err := syscall.Flock(int(lockf.Fd()), syscall.LOCK_EX); err != nil {
			return err
		}
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
	// Once every finding is in (the contract closed), one pass over the
	// whole list first folds what's the same bug worded otherwise, so it's
	// checked once (or takes the check it has).
	if !early {
		if pre, _ := gatherFindings(c, dir); slices.ContainsFunc(pre, func(s *summed) bool { return s.verdict == "" }) {
			dedupeFindings(c, dir, model)
		}
	}
	// Unchecked findings, and checked ones with a link the matcher proposed
	// that nobody has decided: the verifier judges the link alone. Early,
	// only those one of whose finders is back (its words are final; the
	// check goes to every finder of it), and links wait for the close (the
	// matcher proposes them then).
	all, _ := gatherFindings(c, dir)
	done := map[string]bool{}
	for _, p := range c.Professionals {
		done[p.Name] = readStatus(dir, p.Name).terminal()
	}
	var todo []*summed
	for _, s := range all {
		if early {
			if s.verdict != "" || !anyBack(s.by, done) {
				continue
			}
		} else if s.verdict != "" && len(proposedLinks(s)) == 0 {
			continue
		}
		todo = append(todo, s)
	}
	// The summary is close's to write while the contract is open; once it's
	// closed, a check that lands rewrites it (asked each time: it can close
	// while this one checks).
	summarize := func() bool {
		if !early {
			return true
		}
		cc, _, err := loadContract(c.ID)
		return err == nil && cc.ClosedAt != nil
	}
	if early && len(todo) == 0 {
		return nil
	}
	if len(todo) == 0 {
		fmt.Printf("Every finding on contract %s is checked already.\n", c.ID)
		// An early check may have landed since the close wrote it.
		_, _ = writeSummary(c, dir)
		// Nothing left to check (all checked as they came back, or nothing
		// found): verified all the same, once, for the hook that waits for
		// it.
		if !early && !isFile(filepath.Join(dir, ".verified")) {
			fireVerified(c, dir, nil, 0)
		}
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
	// What's checked already, for each verifier to fold the same bug worded
	// otherwise into rather than check it again.
	master := checkedList(all)

	// The copy of the project every verifier reads, made once before any
	// starts: made by each, two at once took it from under each other, and
	// the one that lost read the project as it is now.
	code, why := projectCopy(c, dir)
	if why != "" {
		fmt.Fprintf(os.Stderr, "administration: %s\n", why)
	}
	// Whether the copy has the uncommitted work: projectCopy says why
	// whenever it doesn't.
	withWork := c.Work != "" && why == ""
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
			vs, why := verifyBatch(c, code, withWork, filepath.Join(dir, ".verify", strconv.Itoa(k+1)), b, master, model, coins)
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
			if len(vs) > 0 && summarize() {
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
	// Early, the contract is open: close writes its summary. What didn't
	// finish is said in the early log, the only place it can be: the
	// close's verify checks those again.
	if !summarize() {
		for _, f := range failed {
			fmt.Println("  " + f)
		}
		return nil
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
	if !early {
		fireVerified(c, dir, count, len(failed))
	}
	if len(failed) > 0 {
		return startError{fmt.Sprintf("%s didn't finish; their findings are still unchecked", plural(len(failed), "verifier", "verifiers"))}
	}
	return nil
}

// verifyBatch runs one walled verifier on a batch and returns its verdicts,
// or why it gave none. Its folder is kept when it fails.
func verifyBatch(c *contract, code string, withWork bool, rd string, batch []*summed, master []checked, model string, coins time.Duration) (map[*summed]verdict, string) {
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
	// The project as it was tested, not as it is now: findings true of the
	// tested commit were once checked false against a branch that had
	// moved on.
	in := map[string]any{"project": code, "target": c.Target, "findings": items}
	if len(master) > 0 {
		in["checked_list"] = master
	}
	if t := c.tested(); t != "" && code != c.Project {
		in["tested_commit"] = t
		switch {
		case withWork:
			in["tested_commit"] = t + ", with the uncommitted work as it was then"
			if n := leftOutNote(c); n != "" {
				in["tested_commit"] = t + ", with the uncommitted work as it was then, except that " + n
			}
		case c.Work != "":
			// The copy was made again without it (the work had changed):
			// a finding in the uncommitted code isn't in what it reads.
			in["tested_commit"] = t + ", without the uncommitted work the contract was placed with (it had changed before this copy was made, so code that was only in that work isn't here)"
		}
	}
	addDirs := []string{"--add-dir", code}
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
	deny = append(deny, secretDenies(contractHouse(c))...)
	deny = append(deny, gitDenies...)
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
	release := verifierSlot()
	rep, state, reason := runHeadless(rd, args, newTag(c.ID, "verifier"), coins)
	release()
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
		Room    bool     `json:"room"`
		SameAs  string   `json:"same_as"`
	}
	if i < 0 || j < i || json.Unmarshal([]byte(text[i:j+1]), &answers) != nil {
		return nil, "its answer wasn't a list of verdicts; its stream is in " + rd
	}
	out := map[*summed]verdict{}
	onList := map[string]checked{}
	for _, m := range master {
		onList[m.Ref] = m
	}
	for _, a := range answers {
		s := ids[a.ID]
		if s == nil {
			continue
		}
		// The same bug as one checked already, worded otherwise: it takes
		// that one's check, and folds into it. One sent only for its links
		// has its check; "same" says nothing of them, so it's left for the
		// next run. A ref not on the list leaves it unchecked.
		if a.Verdict == "same" && s.verdict == "" {
			if m, ok := onList[a.SameAs]; ok && !slices.Contains(s.by, m.Ref) {
				out[s] = verdict{Verdict: m.Verdict, Why: oneLine("the same as "+m.Ref+": "+a.Why, 400), By: "verifier", SameAs: m.Ref, FromFold: true}
			}
			continue
		}
		// Sent for its links, it keeps its check (below) and its links are
		// decided, whatever it said of the finding.
		v := verdict{Verdict: a.Verdict, Why: oneLine(a.Why, 400), By: "verifier", Also: a.Also, NotAlso: a.NotAlso, linkWhy: oneLine(a.LinkWhy, 300)}
		if s.verdict != "" {
			v.Verdict = s.verdict // sent for its links: its check stands
		} else if a.Verdict != "confirmed" && a.Verdict != "likely" && a.Verdict != "false" {
			continue
		}
		// Put down to the room only by a false verdict of its own.
		v.Room = a.Room && a.Verdict == "false" && s.verdict == ""
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
			byName[m[1]] = append(byName[m[1]], verdict{Finding: n, Verdict: v.Verdict, Why: v.Why, By: "verifier", Tag: v.Tag, Room: v.Room, SameAs: v.SameAs, FromFold: v.FromFold})
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

// fireVerified tells the verified hooks a verify run is done: the contract
// as the closed hook has it, with what this run decided and whether any of
// its verifiers didn't finish (their findings are still unchecked). A hook
// that reports on a run waits for this rather than closed, which comes
// before the close's checks (the last professional's findings, the pass
// for the same bug in other words). It fires once a verify at the close
// has nothing left to check, even when it checked nothing itself.
func fireVerified(c *contract, dir string, count map[string]int, failed int) {
	_ = os.WriteFile(filepath.Join(dir, ".verified"), nil, 0o600)
	p := contractPayload(c, dir)
	p["verdicts"] = map[string]int{"confirmed": count["confirmed"], "likely": count["likely"], "false": count["false"]}
	p["verifiers_failed"] = failed
	contractHooks(c, "verified", p)
}

// anyBack says whether a finder of a finding is back (or out, or otherwise
// done): its words for it are final.
func anyBack(by []string, done map[string]bool) bool {
	for _, ref := range by {
		name, _, _ := strings.Cut(strings.TrimSuffix(ref, ", failed"), " #")
		if done[name] {
			return true
		}
	}
	return false
}

// checked is one finding already checked, as a verifier sees it.
type checked struct {
	Ref     string `json:"ref"`
	Text    string `json:"text"`
	Verdict string `json:"verdict"`
}

// checkedList is the findings already checked, for the master list a
// verifier is given: each by its first finder, at most 80, the longest
// cut short. Not those found false: a real bug folded into one would take
// its false and vanish from the list.
func checkedList(all []*summed) []checked {
	var out []checked
	for _, s := range all {
		if s.verdict == "" || s.verdict == "false" || len(s.by) == 0 {
			continue
		}
		out = append(out, checked{Ref: strings.TrimSuffix(s.by[0], ", failed"), Text: oneLine(s.text, 400), Verdict: s.verdict})
		if len(out) == 80 {
			break
		}
	}
	return out
}

// startEarlyVerify checks a professional's findings as soon as it's back,
// while the contract's others still work: a verify of its own, in the
// background, that outlives the worker. One waiting for its turn is enough
// for any number back, so most of these exit at once. ADMINISTRATION_VERIFY_EARLY=off
// leaves every check to the close.
func startEarlyVerify(c *contract, dir string) {
	if os.Getenv("ADMINISTRATION_VERIFY_EARLY") == "off" {
		return
	}
	self, err := os.Executable()
	if err != nil {
		return
	}
	logf, err := os.OpenFile(filepath.Join(dir, "verify-early.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer logf.Close()
	cmd := exec.Command(self, "verify", c.ID, "--early")
	// Not the professional's tag: close clears that, and this one stays to
	// finish its checks.
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, tagVar+"=") {
			env = append(env, kv)
		}
	}
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if cmd.Start() == nil {
		_ = cmd.Process.Release()
	}
}

// dedupeFindings is the pass over a closed contract's whole list, before
// what's left is checked: one walled reader, with no tools and no code,
// given every finding (folded by words already) and asked which are the
// same bug worded otherwise, and which folds an earlier check made are
// two bugs after all. What it joins is recorded as same_as on each of the
// later one's finders, taking the earlier one's check when it has none of
// its own; what it parts loses its same_as, and a check it only took from
// the other, so it's checked on its own. A list of fewer than three, or
// from one professional, isn't worth a pass.
func dedupeFindings(c *contract, dir, model string) {
	all, _ := gatherFindings(c, dir)
	finders := map[string]bool{}
	for _, s := range all {
		for _, r := range s.by {
			name, _, _ := strings.Cut(r, " #")
			finders[name] = true
		}
	}
	if len(all) < 3 || len(finders) < 2 {
		return
	}
	type entry struct {
		ID      string   `json:"id"`
		Text    string   `json:"text"`
		By      []string `json:"found_by"`
		Verdict string   `json:"verdict,omitempty"`
		Judged  []string `json:"judged_same,omitempty"`
	}
	var list []entry
	ids := map[string]*summed{}
	for i, s := range all {
		id := fmt.Sprintf("G%d", i+1)
		ids[id] = s
		list = append(list, entry{ID: id, Text: oneLine(s.text, 800), By: s.by, Verdict: s.verdict, Judged: s.judged})
	}
	rd := filepath.Join(dir, ".verify", "dedupe")
	if os.MkdirAll(rd, 0o700) != nil {
		return
	}
	prompt, _ := fs.ReadFile(house, "house/deduper.prompt")
	if os.WriteFile(filepath.Join(rd, "deduper.md"), prompt, 0o600) != nil {
		return
	}
	settings := filepath.Join(rd, "settings.json")
	_ = writeJSON(settings, map[string]any{"permissions": map[string]any{"disableBypassPermissionsMode": "disable"},
		"disableAllHooks": true, "autoMemoryEnabled": false})
	body, _ := json.Marshal(map[string]any{"target": c.Target, "findings": list})
	args := []string{"-p", string(body),
		"--restricted", "--strict-mcp-config", "--settings", settings, "--no-session-persistence",
		"--permission-prompts", "none", "--output-format", "stream-json", "--verbose",
		"--tools", "", "--append-system-prompt-file", filepath.Join(rd, "deduper.md")}
	if model != "" {
		args = append(args, "--model", model)
	}
	fmt.Printf("Folding the same bug found in other words, across %s...\n", plural(len(all), "finding", "findings"))
	release := verifierSlot()
	rep, state, reason := runHeadless(rd, args, newTag(c.ID, "deduper"), 5*time.Minute)
	release()
	recordSpend(dir, "deduper", len(all), model, state, rep)
	if state != "done" {
		fmt.Fprintf(os.Stderr, "administration: the pass for the same bug in other words didn't finish (%s); its stream is in %s. Each is checked on its own.\n", reason, rd)
		return
	}
	text := rep.final
	i, j := strings.Index(text, "{"), strings.LastIndex(text, "}")
	var ans struct {
		Same []struct {
			IDs []string `json:"ids"`
			Why string   `json:"why"`
		} `json:"same"`
		Apart []struct {
			Ref string `json:"ref"`
			Why string `json:"why"`
		} `json:"apart"`
	}
	if i < 0 || j < i || json.Unmarshal([]byte(text[i:j+1]), &ans) != nil {
		fmt.Fprintf(os.Stderr, "administration: the pass for the same bug in other words gave no answer it could read; its stream is in %s. Each is checked on its own.\n", rd)
		return
	}
	joined, parted := 0, 0
	for _, g := range ans.Same {
		var first *summed
		for _, id := range g.IDs {
			s := ids[id]
			if s == nil || len(s.by) == 0 {
				continue
			}
			if first == nil {
				first = s
				continue
			}
			// Only into one checked and not found false, and never one found
			// false into it: a real bug would take the false, or two
			// unchecked would be checked on the first one's words alone.
			if first.verdict == "" || first.verdict == "false" || s.verdict == "false" {
				continue
			}
			to := strings.TrimSuffix(first.by[0], ", failed")
			for _, r := range s.by {
				if setSameAs(c, dir, r, to, first.verdict, oneLine("the same as "+to+": "+g.Why, 400)) {
					joined++
				}
			}
		}
	}
	for _, a := range ans.Apart {
		if clearSameAs(c, dir, a.Ref) {
			parted++
		}
	}
	if joined+parted > 0 {
		fmt.Printf("Folded %s into the same bug worded otherwise; parted %d.\n", plural(joined, "finding", "findings"), parted)
	}
	_ = os.RemoveAll(rd)
}

// editVerdict changes one finder's verdict entry ("name #N", one of the
// contract's own professionals: a model's answer names it, and it's a
// folder) under its lock. change returns false to leave it as it was; an
// entry it empties of everything is removed.
func editVerdict(c *contract, dir, ref string, change func(v *verdict, had bool) bool) bool {
	m := ownRef.FindStringSubmatch(strings.TrimSuffix(ref, ", failed"))
	if m == nil || !onContract(c, m[1]) {
		return false
	}
	n, _ := strconv.Atoi(m[2])
	unlock, err := lockFile(filepath.Join(dir, m[1], "verdicts.lock"))
	if err != nil {
		return false
	}
	defer unlock()
	var vs []verdict
	if b, err := os.ReadFile(filepath.Join(dir, m[1], "verdicts.json")); err == nil {
		_ = json.Unmarshal(b, &vs)
	}
	at := -1
	for i, v := range vs {
		if v.Finding == n {
			at = i
		}
	}
	v := verdict{Finding: n}
	if at >= 0 {
		v = vs[at]
	}
	if !change(&v, at >= 0) {
		return false
	}
	switch {
	case v.Verdict == "" && v.SameAs == "" && len(v.Also) == 0 && len(v.NotAlso) == 0:
		if at >= 0 {
			vs = append(vs[:at], vs[at+1:]...)
		}
	case at >= 0:
		vs[at] = v
	default:
		vs = append(vs, v)
	}
	sort.Slice(vs, func(i, j int) bool { return vs[i].Finding < vs[j].Finding })
	return writeJSON(filepath.Join(dir, m[1], "verdicts.json"), vs) == nil
}

// setSameAs records a finder's finding as the same bug as to's, taking to's
// check (verdict, why) when it has none of its own. A check of its own, the
// session's above all, stays.
func setSameAs(c *contract, dir, ref, to, toVerdict, why string) bool {
	tm := ownRef.FindStringSubmatch(to)
	if strings.TrimSuffix(ref, ", failed") == to || tm == nil || !onContract(c, tm[1]) {
		return false
	}
	return editVerdict(c, dir, ref, func(v *verdict, had bool) bool {
		if v.SameAs == to {
			return false
		}
		// Folded elsewhere before: the check it took from there goes.
		if v.FromFold {
			v.Verdict, v.Why, v.By, v.FromFold = "", "", "", false
		}
		v.SameAs = to
		if v.Verdict == "" && toVerdict != "" {
			v.Verdict, v.Why, v.By, v.FromFold = toVerdict, why, "verifier", true
		}
		return true
	})
}

// clearSameAs parts a finder's finding from the one it was folded into:
// its same_as goes, and the check it took from it, so it's checked on its
// own.
func clearSameAs(c *contract, dir, ref string) bool {
	return editVerdict(c, dir, ref, func(v *verdict, had bool) bool {
		// A fold the session made is the session's to part.
		if v.SameAs == "" || v.By != "verifier" && !v.FromFold {
			return false
		}
		if v.FromFold {
			v.Verdict, v.Why, v.By, v.FromFold = "", "", "", false
		}
		v.SameAs = ""
		return true
	})
}

// onContract says whether name is one of the contract's professionals.
func onContract(c *contract, name string) bool {
	for _, p := range c.Professionals {
		if p.Name == name {
			return true
		}
	}
	return false
}

// earlyAlive says whether an early verify of the contract is waiting or
// checking now: its mark names a process that's still there.
func earlyAlive(dir string) bool {
	b, err := os.ReadFile(filepath.Join(dir, ".verify.early"))
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	return err == nil && pid > 1 && syscall.Kill(pid, 0) == nil
}

// verifierSlot waits for one of the machine's places for a verifier (or
// the deduper) and holds it until release: with checks running as each
// professional comes back, several contracts' verifies run at once, and
// each is a claude beside the rooms. ADMINISTRATION_MAX_VERIFIERS places
// (4 by default), lock files in the books.
func verifierSlot() (release func()) {
	n := 4
	if v, err := strconv.Atoi(os.Getenv("ADMINISTRATION_MAX_VERIFIERS")); err == nil && v > 0 {
		n = v
	}
	books, err := booksDir()
	if err != nil {
		return func() {}
	}
	slots := filepath.Join(books, ".verifiers")
	if os.MkdirAll(slots, 0o700) != nil {
		return func() {}
	}
	for {
		for i := 1; i <= n; i++ {
			if unlock, ok := tryLock(filepath.Join(slots, fmt.Sprintf("slot-%d", i)), syscall.LOCK_EX); ok {
				return unlock
			}
		}
		time.Sleep(time.Second)
	}
}
