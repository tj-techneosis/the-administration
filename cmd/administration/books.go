package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// A verdict is the check a finding got at close: confirmed (the code agrees),
// likely, or false. The skill records them; they're how the books tell a
// professional that earns its spend from one that doesn't.
type verdict struct {
	Finding int    `json:"finding"` // 1-based, in the report's order
	Verdict string `json:"verdict"` // confirmed, likely, false
	Why     string `json:"why,omitempty"`
	// The same bug in another contract ("ID NAME #N"), or a proposed link
	// that isn't: see links.go.
	Also    []string `json:"also,omitempty"`
	NotAlso []string `json:"not_also,omitempty"`
	// "verifier" when a walled verifier gave it (administration verify);
	// empty when the session did, whose verdict replaces a verifier's.
	By string `json:"by,omitempty"`
	// The mark the finding should have had, when its own is wrong: a "NEW"
	// that was already so before the changes is "OLD". The summary files it
	// there.
	Tag string `json:"tag,omitempty"`
	// A false finding the test room caused, not the app (a seed that
	// didn't load, a setup that half ran, the wall blocking a font).
	Room bool `json:"room,omitempty"`
	// Another finding of this contract ("name #N") a check found to be the
	// same bug, worded otherwise: the summary folds this one there
	// (verify.go, Checking as they come back).
	SameAs string `json:"same_as,omitempty"`
	// The verdict was taken from that finding with the fold, not checked
	// on its own: parted, it goes with it.
	FromFold bool `json:"from_fold,omitempty"`
	// Why a verifier decided a link the way it did; it goes with the link.
	linkWhy string
}

// fixTag is a verdict's corrected mark, upper case, or why it can't be one.
// UNMARKED says the finding belongs with no mark at all.
func fixTag(t string) (string, bool) {
	switch u := strings.ToUpper(strings.TrimSpace(t)); u {
	case "", "NEW", "CHANGE", "OLD", "UNMARKED":
		return u, true
	}
	return "", false
}

// cmdVerdicts records verdicts for a closed or open contract, from a JSON
// object {"<professional>": [{"finding": 1, "verdict": "confirmed", "why": "..."}]}
// on stdin or in a file.
func cmdVerdicts(args []string) error {
	if len(args) != 2 {
		return usageError{"verdicts ID FILE|-   (JSON: {\"name\": [{\"finding\": 1, \"verdict\": \"confirmed\"}]})"}
	}
	// The contract is looked up first, so a mistyped ID is told at once,
	// and again under the books' use lock once the verdicts are read: a
	// verdicts waiting on its stdin doesn't keep a prune refused.
	if _, _, err := loadContract(args[0]); err != nil {
		return err
	}
	var in io.Reader = os.Stdin
	if args[1] != "-" {
		f, err := os.Open(args[1])
		if err != nil {
			return err
		}
		defer f.Close()
		in = f
	}
	var all map[string][]verdict
	dec := json.NewDecoder(in)
	if err := dec.Decode(&all); err == io.EOF {
		return usageError{"verdicts: there are none in it to record"}
	} else if err != nil {
		return usageError{"verdicts: " + err.Error()}
	}
	// One object: a second one pasted after it would be dropped unread.
	if dec.More() {
		return usageError{`verdicts: one JSON object, {"name": [...]}, with everyone in it`}
	}
	given := 0
	for _, vs := range all {
		given += len(vs)
	}
	if given == 0 {
		return usageError{"verdicts: there are none in it to record"}
	}
	unuse, err := useBooks()
	if err != nil {
		return err
	}
	defer unuse()
	c, dir, err := loadContract(args[0])
	if err != nil {
		return err
	}
	on := map[string]bool{}
	for _, p := range c.Professionals {
		on[p.Name] = true
	}
	// Everything is checked before anything is written, so a mistake in one
	// professional's list leaves the books as they were.
	for name, vs := range all {
		if !on[name] {
			return usageError{fmt.Sprintf("%s is not on contract %s", name, c.ID)}
		}
		n := readStatus(dir, name).Findings
		for i := range vs {
			// The session's own: whatever a copy of the books says.
			vs[i].By, vs[i].FromFold = "", false
		}
		for _, v := range vs {
			if v.Verdict != "confirmed" && v.Verdict != "likely" && v.Verdict != "false" {
				return usageError{fmt.Sprintf("%s finding %d: verdict is confirmed, likely or false", name, v.Finding)}
			}
			if _, ok := fixTag(v.Tag); !ok {
				return usageError{fmt.Sprintf("%s finding %d: tag is NEW, CHANGE, OLD or UNMARKED", name, v.Finding)}
			}
			if v.Tag != "" && c.Changes == nil {
				return usageError{fmt.Sprintf("%s finding %d: a tag corrects a mark, and marks mean something only on a contract on changes; %s isn't one", name, v.Finding, c.ID)}
			}
			if v.Finding < 1 || v.Finding > n {
				return usageError{fmt.Sprintf("%s has %d findings; there's no finding %d", name, n, v.Finding)}
			}
			// same_as folds it into another of this contract's findings, the
			// same bug worded otherwise; "-" parts it from the one it was
			// folded into.
			if v.SameAs != "" && v.SameAs != "-" {
				m := ownRef.FindStringSubmatch(v.SameAs)
				if m == nil || !on[m[1]] {
					return usageError{fmt.Sprintf("%s finding %d: same_as is another finding of this contract, as \"name #N\" (or \"-\" to part it)", name, v.Finding)}
				}
				if v.SameAs == fmt.Sprintf("%s #%d", name, v.Finding) {
					return usageError{fmt.Sprintf("%s finding %d: same_as names itself", name, v.Finding)}
				}
			}
			for _, ref := range append(append([]string{}, v.Also...), v.NotAlso...) {
				if m := otherRef.FindStringSubmatch(ref); m != nil && m[1] == c.ID {
					return usageError{fmt.Sprintf("%q: a link is to another contract's finding; findings within one contract are folded, not linked", ref)}
				}
				if _, _, err := resolveOther(ref); err != nil {
					return err
				}
			}
		}
	}
	touched := map[string]bool{}
	for name, vs := range all {
		if len(vs) == 0 {
			continue
		}
		// A later call adds to the earlier ones; a finding checked again
		// takes its new verdict.
		byN := map[int]verdict{}
		unlock, err := lockFile(filepath.Join(dir, name, "verdicts.lock"))
		if err != nil {
			return err
		}
		var old []verdict
		if b, err := os.ReadFile(filepath.Join(dir, name, "verdicts.json")); err == nil {
			_ = json.Unmarshal(b, &old)
		}
		for _, v := range append(old, vs...) {
			v.Tag, _ = fixTag(v.Tag)
			// Agreeing without repeating a corrected mark keeps it: to undo
			// one, give the mark the finding was written with (UNMARKED for
			// none). A false finding shows its own mark, but its correction
			// is kept for a later check that isn't false.
			if o, ok := byN[v.Finding]; ok && v.Tag == "" {
				v.Tag = o.Tag
			}
			// A fold a check made (same_as) stays under the session's own
			// verdict, unless it gives another, or "-" to part it.
			if o, ok := byN[v.Finding]; ok && v.SameAs == "" {
				v.SameAs = o.SameAs
			}
			if v.SameAs == "-" {
				v.SameAs = ""
			}
			byN[v.Finding] = v
		}
		merged := make([]verdict, 0, len(byN))
		for _, v := range byN {
			merged = append(merged, v)
		}
		sort.Slice(merged, func(i, j int) bool { return merged[i].Finding < merged[j].Finding })
		err = writeJSON(filepath.Join(dir, name, "verdicts.json"), merged)
		unlock()
		if err != nil {
			return err
		}
		for _, v := range vs {
			if err := decideLinks(c, dir, name, v, "session"); err != nil {
				return err
			}
			for _, ref := range append(append([]string{}, v.Also...), v.NotAlso...) {
				touched[otherRef.FindStringSubmatch(ref)[1]] = true
			}
		}
	}
	// The other side of each link shows it too.
	for id := range touched {
		if oc, odir, err := loadContract(id); err == nil {
			if _, err := os.Stat(filepath.Join(odir, "summary.md")); err == nil {
				_, _ = writeSummary(oc, odir)
			}
		}
	}
	n := 0
	for _, vs := range all {
		if len(vs) > 0 {
			n++
		}
	}
	fmt.Printf("Verdicts recorded for %s on contract %s.\n", plural(n, "professional", "professionals"), c.ID)
	// A closed contract's summary shows each check beside its finding.
	if _, err := os.Stat(filepath.Join(dir, "summary.md")); err == nil {
		if _, err := writeSummary(c, dir); err != nil {
			return err
		}
	}
	return nil
}

// A line of a project's ledger: one professional on one contract, kept after
// the contract itself is pruned.
type ledgerLine struct {
	Contract  string   `json:"contract"`
	Target    string   `json:"target"`
	Placed    string   `json:"placed"`
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	State     string   `json:"state"`
	Minutes   float64  `json:"minutes"`
	CoinsMin  float64  `json:"coins_min"`
	CostUSD   *float64 `json:"cost_usd,omitempty"` // raw usage, for anyone who wants it
	Turns     int      `json:"turns,omitempty"`
	Findings  int      `json:"findings"`
	Unreached int      `json:"unreached"`
	Confirmed int      `json:"confirmed"`
	Likely    int      `json:"likely"`
	False     int      `json:"false"`
	Checked   bool     `json:"checked"` // verdicts were recorded
	// Of those, the ones a verifier gave and nobody has reviewed since.
	ByVerifier int `json:"by_verifier,omitempty"`
	Dossier   int      `json:"dossier_bytes,omitempty"`
	// How long it waited for the machine before its coins started.
	WaitedSecs float64 `json:"waited_secs,omitempty"`
}

func lineFor(c *contract, dir string, p castItem) ledgerLine {
	s := readStatus(dir, p.Name)
	l := ledgerLine{Contract: c.ID, Target: c.Target, Placed: c.PlacedAt.Format(time.RFC3339), Name: p.Name,
		Kind: p.Kind, State: s.State, CoinsMin: p.CoinsMin, CostUSD: s.CostUSD, Turns: s.Turns,
		Findings: s.Findings, Unreached: s.Unreached, Dossier: p.Dossier}
	// The coins it used: from when its room was ready, so what it waited
	// for the machine isn't counted against it.
	if from := s.ReadyAt; from != nil && s.FinishedAt != nil {
		l.Minutes = s.FinishedAt.Sub(*from).Minutes()
	} else if s.StartedAt != nil && s.FinishedAt != nil {
		l.Minutes = s.FinishedAt.Sub(*s.StartedAt).Minutes()
	}
	l.WaitedSecs = s.WaitedSecs
	var vs []verdict
	if b, err := os.ReadFile(filepath.Join(dir, p.Name, "verdicts.json")); err == nil && json.Unmarshal(b, &vs) == nil {
		l.Checked = true
	}
	// A finding folded into someone else's checked one has that check:
	// verify sends the claim once, not once per finder, so a finder may
	// have no verdicts of its own at all.
	own := map[int]bool{}
	for _, v := range vs {
		own[v.Finding] = true
	}
	for n, v := range foldedChecks(c, dir)[p.Name] {
		if !own[n] {
			vs = append(vs, v)
			l.Checked = true
		}
	}
	for _, v := range vs {
		switch v.Verdict {
		case "confirmed":
			l.Confirmed++
		case "likely":
			l.Likely++
		case "false":
			l.False++
		}
		if v.By == "verifier" {
			l.ByVerifier++
		}
	}
	return l
}

// foldedChecks is, for each professional, the check each of its findings
// has through the folded finding it's part of: the summary's verdict.
// It's read once per contract per run (stats reads every professional's).
var foldedCache sync.Map

func foldedChecks(c *contract, dir string) map[string]map[int]verdict {
	if v, ok := foldedCache.Load(dir); ok {
		return v.(map[string]map[int]verdict)
	}
	out := map[string]map[int]verdict{}
	defer foldedCache.Store(dir, out)
	all, _ := gatherFindings(c, dir)
	for _, f := range all {
		if f.verdict == "" {
			continue
		}
		for _, ref := range f.by {
			m := ownRef.FindStringSubmatch(strings.TrimSuffix(ref, ", failed"))
			if m == nil {
				continue
			}
			n, _ := strconv.Atoi(m[2])
			if out[m[1]] == nil {
				out[m[1]] = map[int]verdict{}
			}
			out[m[1]][n] = verdict{Finding: n, Verdict: f.verdict, By: f.checker}
		}
	}
	return out
}

// projectLedger is where a project's pruned history lives in the books.
func projectLedger(books, project string) string {
	key := strings.NewReplacer("/", "_", " ", "_").Replace(strings.Trim(project, "/"))
	return filepath.Join(books, "ledgers", key+".jsonl")
}

// history is every professional-on-a-contract line for a project: the pruned
// ones from its ledger, the ones still in the books.
func history(project string) ([]ledgerLine, error) {
	books, err := booksDir()
	if err != nil {
		return nil, err
	}
	var lines []ledgerLine
	seen := map[string]bool{}
	if f, err := os.Open(projectLedger(books, projectKey(project))); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			// A line written twice (by a prune before appendLedger
			// looked first) counts once.
			var l ledgerLine
			if json.Unmarshal(sc.Bytes(), &l) == nil && !seen[l.Contract+"/"+l.Name] {
				lines = append(lines, l)
				seen[l.Contract+"/"+l.Name] = true
			}
		}
		f.Close()
	}
	files, _ := filepath.Glob(filepath.Join(books, "contracts", "*", "contract.json"))
	for _, file := range files {
		c, dir, err := loadContract(filepath.Base(filepath.Dir(file)))
		if err != nil || contractKey(c) != projectKey(project) {
			continue
		}
		for _, p := range c.Professionals {
			if !seen[c.ID+"/"+p.Name] {
				lines = append(lines, lineFor(c, dir, p))
			}
		}
	}
	sort.Slice(lines, func(i, j int) bool { return lines[i].Placed < lines[j].Placed })
	return lines, nil
}

// The numbers the Director refines the roster from.
type standing struct {
	Name         string   `json:"name"`
	Contracts    int      `json:"contracts"`
	Back         int      `json:"back"`
	Out          int      `json:"out"` // shown out at the coin limit
	Failed       int      `json:"failed"`
	Findings     int      `json:"findings"`
	Checked      int      `json:"checked_contracts"`
	Confirmed    int      `json:"confirmed"`
	Likely       int      `json:"likely"`
	False        int      `json:"false"`
	ByVerifier   int      `json:"by_verifier,omitempty"` // of the checks, a verifier's not reviewed since
	AvgTurns     float64  `json:"avg_turns"`
	AvgMinutes   float64  `json:"avg_minutes"`
	AvgCoinsUsed float64  `json:"avg_coins_used"` // minutes worked / coins, 1.0 = always out
}

func standings(lines []ledgerLine) []standing {
	by := map[string]*standing{}
	var order []string
	mins := map[string]float64{}
	used := map[string]float64{}
	turns := map[string]float64{}
	for _, l := range lines {
		s := by[l.Name]
		if s == nil {
			s = &standing{Name: l.Name}
			by[l.Name] = s
			order = append(order, l.Name)
		}
		s.Contracts++
		switch l.State {
		case "back":
			s.Back++
		case "out":
			s.Out++
		case "failed", "refused":
			s.Failed++
		}
		s.Findings += l.Findings
		if l.Checked {
			s.Checked++
		}
		s.Confirmed += l.Confirmed
		s.Likely += l.Likely
		s.False += l.False
		s.ByVerifier += l.ByVerifier
		// Time and turns are averaged over the contracts they worked: a run
		// that failed early says nothing about how long the work takes.
		if l.State == "failed" || l.State == "refused" {
			continue
		}
		turns[l.Name] += float64(l.Turns)
		mins[l.Name] += l.Minutes
		if l.CoinsMin > 0 {
			used[l.Name] += l.Minutes / l.CoinsMin
		}
	}
	out := []standing{}
	for _, n := range order {
		s := by[n]
		if worked := s.Contracts - s.Failed; worked > 0 {
			s.AvgMinutes = mins[n] / float64(worked)
			s.AvgCoinsUsed = used[n] / float64(worked)
			s.AvgTurns = turns[n] / float64(worked)
		}
		out = append(out, *s)
	}
	return out
}

func cmdStats(args []string) error {
	f, err := parseFlags(args, map[string]bool{"json": false, "project": true})
	if err != nil {
		return err
	}
	project, err := projectDir(f.get("project"))
	if err != nil {
		return err
	}
	lines, err := history(project)
	if err != nil {
		return err
	}
	st := standings(lines)
	if f.has("json") {
		key := contractKey(&contract{Project: project, Key: projectKey(project)})
		// Usage is kept for whoever wants it, as each status.json keeps a
		// professional's: the checking around them (verifiers, the matcher)
		// is here, never printed.
		checking := map[string]any{}
		for who, t := range projectSpend(key, project) {
			checking[who] = map[string]any{"cost_usd": t[0], "runs": int(t[1]), "unfinished": int(t[2])}
		}
		return printJSON(map[string]any{"project": project, "contracts": countContracts(lines), "professionals": st, "checking": checking})
	}
	if len(st) == 0 {
		fmt.Println("No contracts in the books for", project)
		return nil
	}
	fmt.Printf("The books for %s: %d contracts\n\n", project, countContracts(lines))
	fmt.Printf("  %-26s %4s %4s %4s %6s %6s %6s %6s %6s\n", "professional", "runs", "out", "fail", "finds", "held", "false", "turns", "coins")
	for _, s := range st {
		fmt.Printf("  %-26s %4d %4d %4d %6d %6d %6d %6.0f %5.0f%%\n", s.Name, s.Contracts, s.Out, s.Failed,
			s.Findings, s.Confirmed, s.False, s.AvgTurns, 100*s.AvgCoinsUsed)
	}
	verified, checks := 0, 0
	for _, s := range st {
		verified += s.ByVerifier
		checks += s.Confirmed + s.Likely + s.False
	}
	if verified > 0 {
		fmt.Printf("\n%d of the %d checks above are a verifier's that nobody has reviewed since.\n", verified, checks)
	}
	key := contractKey(&contract{Project: project, Key: projectKey(project)})
	held, wrong := matcherRecord(&contract{Project: project, Key: key})
	if held+wrong > 0 {
		fmt.Printf("\nLinks the matcher proposed between contracts: %d held, %d didn't (%.0f%% right).\n", held, wrong, 100*float64(held)/float64(held+wrong))
	}
	if n := projectUndecided(key, project); n > 0 {
		sep := "\n"
		if held+wrong > 0 {
			sep = ""
		}
		fmt.Printf("%s%s\n", sep, undecidedLine(n, ""))
	}
	return nil
}

// spend is one headless run's cost, kept in the contract's spend.jsonl:
// the verifiers and the matcher, whose streams go once they've answered.
type spend struct {
	At        string   `json:"at"`
	Who       string   `json:"who"` // verifier, matcher
	Findings  int      `json:"findings,omitempty"`
	Model     string   `json:"model,omitempty"`
	State     string   `json:"state"`
	CostUSD   *float64 `json:"cost_usd,omitempty"`
	Turns     int      `json:"turns,omitempty"`
	TokensIn  int      `json:"tokens_in,omitempty"`
	TokensOut int      `json:"tokens_out,omitempty"`
}

// recordSpend adds a headless run to its contract's spend.jsonl.
func recordSpend(dir, who string, findings int, model, state string, rep *report) spend {
	sp := spend{At: time.Now().UTC().Format(time.RFC3339), Who: who, Findings: findings, Model: model, State: state}
	if rep != nil {
		sp.CostUSD, sp.Turns, sp.TokensIn, sp.TokensOut = rep.cost, rep.turns, rep.tokensIn, rep.tokensOut
	}
	if f, err := os.OpenFile(filepath.Join(dir, "spend.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		b, _ := json.Marshal(sp)
		_, _ = f.Write(append(b, '\n'))
		f.Close()
	}
	return sp
}

// projectSpend is what checking cost on a project's contracts still in the
// books: each kind of run's dollars and count.
// The three numbers are dollars, runs, and runs that didn't finish (counted
// in runs; one that never started costs nothing).
func projectSpend(key, project string) map[string][3]float64 {
	out := map[string][3]float64{}
	books, err := booksDir()
	if err != nil {
		return out
	}
	entries, _ := os.ReadDir(filepath.Join(books, "contracts"))
	for _, e := range entries {
		dir := filepath.Join(books, "contracts", e.Name())
		b, err := os.ReadFile(filepath.Join(dir, "spend.jsonl"))
		if err != nil {
			continue
		}
		var o contract
		if cb, err := os.ReadFile(filepath.Join(dir, "contract.json")); err != nil || json.Unmarshal(cb, &o) != nil || contractKey(&o) != key && o.Project != project {
			continue
		}
		for _, l := range strings.Split(string(b), "\n") {
			var sp spend
			if json.Unmarshal([]byte(l), &sp) != nil {
				continue
			}
			t := out[sp.Who]
			if sp.CostUSD != nil {
				t[0] += *sp.CostUSD
			}
			t[1]++
			if sp.State != "done" {
				t[2]++
			}
			out[sp.Who] = t
		}
	}
	return out
}

func countContracts(lines []ledgerLine) int {
	seen := map[string]bool{}
	for _, l := range lines {
		seen[l.Contract] = true
	}
	return len(seen)
}

// cmdPrune keeps the books small. A closed contract's raw material (the
// professionals' streams, briefs, personas and settings, the handout) goes
// once the Director has reviewed it, or once it's older than --older. Its
// numbers move to the project's ledger, and only the last --keep summaries
// and reports stay.
//
// It holds the books' use lock (useBooks) exclusive while it looks and
// renames, so nothing it removes is being placed, closed, verified or
// resumed meanwhile, and it's refused while one of those runs. What it
// removes it first renames into the books' .pruned folder, so nothing ever
// finds half of it: a copy, a build or a contract is there whole or not at
// all. The removal itself, which can take a while, comes after the lock is
// let go: what's set aside is nobody's. A contract's numbers are in the
// ledger before it's set aside, and appendLedger writes only the lines the
// ledger lacks, so a prune cut off or failing anywhere neither loses them
// nor counts them twice.
func cmdPrune(args []string) error {
	f, err := parseFlags(args, map[string]bool{"keep": true, "older": true, "dry-run": false})
	if err != nil {
		return err
	}
	keep := 10
	if f.has("keep") {
		if keep, err = strconv.Atoi(f.get("keep")); err != nil || keep < 0 {
			return usageError{"--keep: how many closed contracts per project keep their summaries"}
		}
	}
	older := 30 * 24 * time.Hour
	if f.has("older") {
		if older, err = parseAge(f.get("older")); err != nil || older < 0 {
			return usageError{"--older: an age like 30d or 12h"}
		}
	}
	books, err := booksDir()
	if err != nil {
		return err
	}
	// A dry run changes nothing, makes nothing and takes no lock: it can
	// look while a place or a close runs.
	dry := f.has("dry-run")
	if !dry {
		if err := os.MkdirAll(books, 0o700); err != nil {
			return err
		}
		unlock, ok := tryLockFile(useLock(books))
		if !ok {
			return busy{"a place, close, verify, verdicts or resume is running on the books; prune once it's done"}
		}
		// Last-in first-out: the lock is let go, then what this prune (or
		// one cut off before it) set aside is removed.
		defer clearPruned(books)
		defer unlock()
	}
	// What couldn't be removed is told after the rest is done, with what
	// was: one failure doesn't stop the prune or hide what it did.
	var failed []string
	fail := func(what string, err error) { failed = append(failed, what+": "+err.Error()) }
	gone := func(path string) bool {
		if dry {
			return true
		}
		_, err := aside(books, path)
		if err != nil {
			fail(path, err)
		}
		return err == nil
	}
	files, _ := filepath.Glob(filepath.Join(books, "contracts", "*", "contract.json"))
	byProject := map[string][]*contract{}
	// The shared copies of the project (copyRoot) a contract still kept
	// whole may read, and the builds an open one's rooms may install.
	needed := map[string]bool{}
	need := func(c *contract) {
		if c.tested() == "" {
			return
		}
		for _, w := range []string{c.Work, ""} {
			if root, err := copyRoot(c, c.tested(), w); err == nil {
				needed[root] = true
			}
		}
	}
	for _, file := range files {
		c, _, err := loadContract(filepath.Base(filepath.Dir(file)))
		if err == nil && c.ClosedAt == nil {
			need(c)
			if root, err := buildsDir(c); err == nil && buildCommit(c) != "" {
				needed[filepath.Join(root, buildCommit(c))] = true
			}
			// Its before app's build too, of the merge base.
			if root, err := buildsDir(c); err == nil && c.Before && c.Changes != nil && c.Changes.MergeBase != "" {
				needed[filepath.Join(root, c.Changes.MergeBase)] = true
			}
		}
		if err != nil || c.ClosedAt == nil {
			continue
		}
		byProject[contractKey(c)] = append(byProject[contractKey(c)], c)
	}
	// The files a stripped contract loses, in each professional's folder:
	// everything but the report, the status, the verdicts and the links,
	// the before app's and the equipment's as well as the room's.
	raw := []string{"stream.jsonl", "brief.md", "persona.md", "settings.json", "mcp.json", "browser.json",
		"claude.log", "worker.log", "setup.log", "teardown.log", "gate.log", "room.json", "ports", "tag", "worker.pid",
		"room-before.json", "setup-before.log", "browser-before.json", "ports-before", "argv.json", "equipment-*.log"}
	// A contract closed with --keep kept its rooms: they go with it.
	keptRooms := func(id string) string {
		if rooms, err := roomsDir(); err == nil && isDir(filepath.Join(rooms, id)) {
			return filepath.Join(rooms, id)
		}
		return ""
	}
	stripped, removed := 0, 0
	for project, cs := range byProject {
		// The last closed first: the keep is the last --keep to close.
		sort.Slice(cs, func(i, j int) bool { return cs[i].ClosedAt.After(*cs[j].ClosedAt) })
		for i, c := range cs {
			dir := filepath.Join(books, "contracts", c.ID)
			_, err := os.Stat(filepath.Join(dir, "reviewed"))
			isReviewed := err == nil
			old := time.Since(*c.ClosedAt) > older
			if !isReviewed && !old {
				// Kept whole until the Director has reviewed it, or it's
				// old, past the keep or not: its findings may not be
				// checked yet.
				need(c)
				continue
			}
			if i >= keep {
				// Past the keep: its numbers go to the ledger, then all of
				// it. The ledger first, so it has them before the contract
				// is gone; a prune cut off in between leaves the contract
				// with its lines already written, and the next one adds
				// none again.
				if dry {
					fmt.Printf("would remove %s (%q)\n", c.ID, c.Target)
					continue
				}
				if err := appendLedger(books, project, c, dir); err != nil {
					fail("the ledger for "+c.ID, err)
					continue
				}
				if _, err := aside(books, dir); err != nil {
					fail(c.ID, err)
					continue
				}
				if r := keptRooms(c.ID); r != "" {
					_ = os.RemoveAll(r)
				}
				removed++
				continue
			}
			var paths []string
			for _, p := range c.Professionals {
				for _, name := range raw {
					m, _ := filepath.Glob(filepath.Join(dir, p.Name, name))
					paths = append(paths, m...)
				}
			}
			tmps, _ := filepath.Glob(checkoutDir(dir) + "-*") // a copy cut off before it was renamed into place
			for _, p := range append([]string{
				filepath.Join(dir, contractHandout),
				filepath.Join(dir, ".verify"), // a failed verifier's or matcher's stream
				filepath.Join(dir, ".match"),
				deskKept(dir),                 // the desk's files as placed
				checkoutDir(dir),              // the copy critics and verifiers read, before copies were shared
				filepath.Join(dir, "handout"), // where it was before 0.4
			}, tmps...) {
				if _, err := os.Lstat(p); err == nil {
					paths = append(paths, p)
				}
			}
			if r := keptRooms(c.ID); r != "" {
				paths = append(paths, r)
			}
			n := 0
			for _, p := range paths {
				if dry {
					fmt.Printf("would remove %s\n", p)
					continue
				}
				if st, err := os.Lstat(p); err == nil && !st.IsDir() {
					if os.Remove(p) == nil {
						n++
					}
				} else if gone(p) {
					n++
				}
			}
			if n > 0 {
				stripped++
			}
		}
	}
	// The shared copies: one no contract kept whole is on goes. A place
	// holds the use lock from before it takes a copy until its contract
	// names it, and a worker makes one only for an open contract, which
	// needs it, so a copy left by nobody is one nobody is using. One made
	// in the last hour (or --older, if less) is left all the same. A copy
	// still being made (makeCheckout's .<name>-XXXX) is judged by the copy
	// it's being made into.
	copies := 0
	grace := min(time.Hour, older)
	made, _ := filepath.Glob(filepath.Join(books, "copies", "*", "*"))
	for _, d := range made {
		into := d
		if base := filepath.Base(d); strings.HasPrefix(base, ".") {
			if i := strings.LastIndex(base, "-"); i > 1 {
				into = filepath.Join(filepath.Dir(d), base[1:i])
			}
		}
		st, err := os.Stat(d)
		if err != nil || needed[d] || needed[into] || time.Since(st.ModTime()) < grace {
			continue
		}
		if dry {
			fmt.Printf("would remove the copy %s\n", d)
			continue
		}
		if gone(d) {
			copies++
		}
	}
	// The build hook's builds, kept per commit: one nobody has used in the
	// age goes (a contract on it again builds it again). An open
	// contract's is kept, and so is one a room or a build holds the lock
	// of (ensureBuild): a room holds it while it's installing or running
	// the build.
	// A build that failed (a log, no artifact) or was killed before it
	// could clean up (its checkout left, with the house's env/ copied in)
	// goes by its log's age the same way, checkout and all.
	builds := 0
	kept, _ := filepath.Glob(filepath.Join(books, "builds", "*", "*"))
	for _, d := range kept {
		if needed[d] {
			continue
		}
		mark := filepath.Join(d, "artifact")
		if _, err := os.Stat(mark); err != nil {
			mark = filepath.Join(d, "build.log")
		}
		st, err := os.Stat(mark)
		if err != nil {
			st, err = os.Stat(d)
		}
		if err != nil || time.Since(st.ModTime()) <= older {
			continue
		}
		if dry {
			if !lockHeld(filepath.Join(d, ".lock")) {
				fmt.Printf("would remove the build in %s\n", d)
			}
			continue
		}
		unlock, ok := tryLockFile(filepath.Join(d, ".lock"))
		if !ok {
			continue // building, or in use
		}
		// Renamed aside while it's held: a room that was waiting for the
		// lock finds the folder gone and starts a new one (lockWaiting),
		// and nothing starts a build in the folder being removed.
		if gone(d) {
			builds++
		}
		unlock()
	}
	if !dry {
		fmt.Printf("Pruned: %d contracts down to their summaries, %d removed (their numbers are in the ledger).\n", stripped, removed)
		if copies > 0 {
			fmt.Printf("Removed %s of the project no contract reads any more.\n", plural(copies, "copy", "copies"))
		}
		if builds > 0 {
			fmt.Printf("Removed %s unused for %s.\n", plural(builds, "build", "builds"), map[bool]string{true: f.get("older"), false: "30d"}[f.has("older")])
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("prune couldn't remove %s (left in the books; the next prune tries again): %s",
			plural(len(failed), "thing", "things"), strings.Join(failed, "; "))
	}
	return nil
}

// aside renames path into the books' .pruned folder, whole, and says where
// it went; clearPruned removes it from there. What's in the books is then
// all of a thing or none of it, even when a prune is cut off. A path on
// another file system (a rooms folder set apart) is removed where it is.
func aside(books, path string) (string, error) {
	trash := filepath.Join(books, ".pruned")
	if err := os.MkdirAll(trash, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(trash, strings.TrimPrefix(filepath.Base(path), ".")+"-")
	if err != nil {
		return "", err
	}
	moved := filepath.Join(tmp, filepath.Base(path))
	if err := os.Rename(path, moved); err != nil {
		_ = os.Remove(tmp)
		if errors.Is(err, syscall.EXDEV) {
			return "", os.RemoveAll(path)
		}
		return "", err
	}
	return moved, nil
}

// clearPruned removes what aside put in the books' .pruned folder.
func clearPruned(books string) {
	trash := filepath.Join(books, ".pruned")
	es, _ := os.ReadDir(trash)
	for _, e := range es {
		_ = os.RemoveAll(filepath.Join(trash, e.Name()))
	}
}

// appendLedger writes a contract's lines, one per professional, to its
// project's ledger, read from dir, and only the ones the ledger doesn't
// have yet: a prune cut off after writing some or all of them, or failing
// part way, leaves the contract in the books, and the next prune completes
// its lines without doubling any. They go in one write and are synced
// before prune removes the contract. A line a failed write left cut off is
// ended first, so it spoils only itself (history skips what isn't JSON).
func appendLedger(books, project string, c *contract, dir string) error {
	path := projectLedger(books, project)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	have := map[string]bool{}
	var last byte = '\n'
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var l ledgerLine
		if json.Unmarshal(sc.Bytes(), &l) == nil {
			have[l.Contract+"/"+l.Name] = true
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if st, err := f.Stat(); err == nil && st.Size() > 0 {
		b := make([]byte, 1)
		if _, err := f.ReadAt(b, st.Size()-1); err == nil {
			last = b[0]
		}
	}
	var out []byte
	if last != '\n' {
		out = append(out, '\n')
	}
	n := 0
	for _, p := range c.Professionals {
		if have[c.ID+"/"+p.Name] {
			continue
		}
		b, _ := json.Marshal(lineFor(c, dir, p))
		out = append(append(out, b...), '\n')
		n++
	}
	if n == 0 {
		return nil
	}
	if _, err := f.Write(out); err != nil {
		return err
	}
	return f.Sync()
}

// parseAge is --older's age: a Go duration, or a number of days (30d).
// A day count too big for a duration (about 292 years) is an error, not
// one that wraps round to a small age that would prune everything.
func parseAge(s string) (time.Duration, error) {
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil {
			return 0, err
		}
		if int64(n) > math.MaxInt64/int64(24*time.Hour) || int64(n) < math.MinInt64/int64(24*time.Hour) {
			return 0, fmt.Errorf("%s is too long an age", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

// lockBooks holds the books' lock while a contract claims rooms, so two
// contracts placed at once can't both slip under the room limit.
func lockBooks() (func(), error) {
	books, err := booksDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(books, 0o700); err != nil {
		return nil, err
	}
	return lockFile(filepath.Join(books, "lock"))
}

// useLock is the books' use lock's file.
func useLock(books string) string { return filepath.Join(books, "use.lock") }

// useWait is how long a command waits for a prune to let go of the books'
// use lock before it gives up. Prune holds it only while it looks and
// renames, so it's rarely more than a moment.
var useWait = 2 * time.Minute

// useBooks holds the books' use lock, shared, until the returned func is
// called (once; calling it again does nothing). Every command that reads
// or writes a closed contract's folder (close, verify, verdicts, resume)
// or takes a shared copy before its contract names it (place) holds it
// while it needs to, and prune takes it exclusive, so prune never strips
// or removes anything under one of them. While a prune holds it, the
// command says so and waits, up to useWait, then gives up as busy (exit 4).
func useBooks() (func(), error) {
	books, err := booksDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(books, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(useLock(books), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(useWait)
	for told := false; ; {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			f.Close()
			return nil, err
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, busy{fmt.Sprintf("a prune has held the books for over %s; try again once it's done", useWait)}
		}
		if !told {
			fmt.Fprintf(os.Stderr, "The books are being pruned; waiting for it (up to %s).\n", useWait)
			told = true
		}
		time.Sleep(100 * time.Millisecond)
	}
	var once sync.Once
	return func() { once.Do(func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }) }, nil
}

// lockHeld says whether someone holds the lock at path, without making
// the file when there's none (prune --dry-run looks and changes nothing).
func lockHeld(path string) bool {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return false
	}
	defer f.Close()
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return true
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}

// tryLockFile is lockFile without the wait: ok is false when someone else
// holds it.
func tryLockFile(path string) (unlock func(), ok bool) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false
	}
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		f.Close()
		return nil, false
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, true
}

// lockFile holds an exclusive lock on path until the returned func is
// called: for a file that's read, changed and written back, so two commands
// at once (verdicts and verify, the matcher and a verifier) can't lose each
// other's change.
func lockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

func contractKey(c *contract) string {
	if c.Key != "" {
		return c.Key
	}
	return c.Project
}
