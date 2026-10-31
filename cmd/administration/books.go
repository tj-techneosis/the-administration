package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
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
}

// cmdVerdicts records verdicts for a closed or open contract, from a JSON
// object {"<professional>": [{"finding": 1, "verdict": "confirmed", "why": "..."}]}
// on stdin or in a file.
func cmdVerdicts(args []string) error {
	if len(args) != 2 {
		return usageError{"verdicts ID FILE|-   (JSON: {\"name\": [{\"finding\": 1, \"verdict\": \"confirmed\"}]})"}
	}
	c, dir, err := loadContract(args[0])
	if err != nil {
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
	if err := json.NewDecoder(in).Decode(&all); err != nil {
		return usageError{"verdicts: " + err.Error()}
	}
	on := map[string]bool{}
	for _, p := range c.Professionals {
		on[p.Name] = true
	}
	for name, vs := range all {
		if !on[name] {
			return usageError{fmt.Sprintf("%s is not on contract %s", name, c.ID)}
		}
		n := readStatus(dir, name).Findings
		for _, v := range vs {
			if v.Verdict != "confirmed" && v.Verdict != "likely" && v.Verdict != "false" {
				return usageError{fmt.Sprintf("%s finding %d: verdict is confirmed, likely or false", name, v.Finding)}
			}
			if v.Finding < 1 || v.Finding > n {
				return usageError{fmt.Sprintf("%s has %d findings; there's no finding %d", name, n, v.Finding)}
			}
		}
		if err := writeJSON(filepath.Join(dir, name, "verdicts.json"), vs); err != nil {
			return err
		}
	}
	fmt.Printf("Verdicts recorded for %d professionals on contract %s.\n", len(all), c.ID)
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
	Dossier   int      `json:"dossier_bytes,omitempty"`
}

func lineFor(c *contract, dir string, p castItem) ledgerLine {
	s := readStatus(dir, p.Name)
	l := ledgerLine{Contract: c.ID, Target: c.Target, Placed: c.PlacedAt.Format(time.RFC3339), Name: p.Name,
		Kind: p.Kind, State: s.State, CoinsMin: p.CoinsMin, CostUSD: s.CostUSD, Turns: s.Turns,
		Findings: s.Findings, Unreached: s.Unreached, Dossier: p.Dossier}
	if s.StartedAt != nil && s.FinishedAt != nil {
		l.Minutes = s.FinishedAt.Sub(*s.StartedAt).Minutes()
	}
	var vs []verdict
	if b, err := os.ReadFile(filepath.Join(dir, p.Name, "verdicts.json")); err == nil && json.Unmarshal(b, &vs) == nil {
		l.Checked = true
		for _, v := range vs {
			switch v.Verdict {
			case "confirmed":
				l.Confirmed++
			case "likely":
				l.Likely++
			case "false":
				l.False++
			}
		}
	}
	return l
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
			var l ledgerLine
			if json.Unmarshal(sc.Bytes(), &l) == nil {
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
	var out []standing
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
		return printJSON(map[string]any{"project": project, "contracts": countContracts(lines), "professionals": st})
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
	return nil
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
		if older, err = parseAge(f.get("older")); err != nil {
			return usageError{"--older: an age like 30d or 12h"}
		}
	}
	books, err := booksDir()
	if err != nil {
		return err
	}
	files, _ := filepath.Glob(filepath.Join(books, "contracts", "*", "contract.json"))
	byProject := map[string][]*contract{}
	for _, file := range files {
		c, _, err := loadContract(filepath.Base(filepath.Dir(file)))
		if err != nil || c.ClosedAt == nil {
			continue
		}
		byProject[contractKey(c)] = append(byProject[contractKey(c)], c)
	}
	raw := []string{"stream.jsonl", "brief.md", "persona.md", "settings.json", "mcp.json", "browser.json",
		"claude.log", "worker.log", "setup.log", "teardown.log", "gate.log", "room.json", "ports", "tag", "worker.pid"}
	stripped, removed := 0, 0
	for project, cs := range byProject {
		sort.Slice(cs, func(i, j int) bool { return cs[i].PlacedAt.After(cs[j].PlacedAt) })
		for i, c := range cs {
			dir := filepath.Join(books, "contracts", c.ID)
			_, reviewed := os.Stat(filepath.Join(dir, "reviewed"))
			old := time.Since(*c.ClosedAt) > older
			if i >= keep {
				// Past the keep: its numbers go to the ledger, then all of it.
				if f.has("dry-run") {
					fmt.Printf("would remove %s (%q)\n", c.ID, c.Target)
					continue
				}
				if err := appendLedger(books, project, c, dir); err != nil {
					return err
				}
				if err := os.RemoveAll(dir); err != nil {
					return err
				}
				removed++
				continue
			}
			if reviewed == nil || old {
				for _, p := range c.Professionals {
					for _, name := range raw {
						path := filepath.Join(dir, p.Name, name)
						if _, err := os.Stat(path); err == nil {
							if f.has("dry-run") {
								fmt.Printf("would remove %s\n", path)
								continue
							}
							_ = os.Remove(path)
						}
					}
				}
				if !f.has("dry-run") {
					_ = os.RemoveAll(filepath.Join(dir, "handout"))
					stripped++
				}
			}
		}
	}
	if !f.has("dry-run") {
		fmt.Printf("Pruned: %d contracts down to their summaries, %d removed (their numbers are in the ledger).\n", stripped, removed)
	}
	return nil
}

func appendLedger(books, project string, c *contract, dir string) error {
	path := projectLedger(books, project)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, p := range c.Professionals {
		b, _ := json.Marshal(lineFor(c, dir, p))
		if _, err := f.Write(append(b, '\n')); err != nil {
			return err
		}
	}
	return nil
}

func parseAge(s string) (time.Duration, error) {
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		return time.Duration(n) * 24 * time.Hour, err
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
	f, err := os.OpenFile(filepath.Join(books, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
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
