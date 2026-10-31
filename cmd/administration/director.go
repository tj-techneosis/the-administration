package main

import (
	"bytes"
	"strconv"

	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"gopkg.in/yaml.v3"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
)

// The Director runs like a professional: a separate claude -p, --restricted,
// no shell, no MCP. Its working directory is a run folder in the books: it
// reads ./in (copies of what it needs) and the project, and may write only
// in ./out. The Administration checks everything in ./out before anything
// reaches the project, and only `director apply` puts it there.

type directorRun struct {
	ID       string    `json:"id"`
	Job      string    `json:"job"` // train, onboard, review
	Project  string    `json:"project"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished"`
	State    string    `json:"state"` // done, failed, out
	Reason   string    `json:"reason,omitempty"`
	CostUSD  *float64  `json:"cost_usd,omitempty"` // as claude reports it; shown nowhere
	Turns    int       `json:"turns,omitempty"`
	Problems []string  `json:"problems,omitempty"` // what the checks found in ./out
	Repaired bool      `json:"repaired,omitempty"` // a second round fixed what the first left wrong
	Applied  bool      `json:"applied,omitempty"`
	Lost     []string  `json:"lost,omitempty"` // onboard: what each agent can't do in the kind it joins as
}

func directorDir() (string, error) {
	b, err := booksDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(b, "director"), nil
}

func cmdDirector(args []string) (int, error) {
	if len(args) == 0 {
		return exitError, usageError{"director train WHO... | onboard | review | show RUN | apply RUN"}
	}
	switch args[0] {
	case "train", "onboard", "review", "cast":
		return directorJob(args[0], args[1:])
	case "show":
		if len(args) != 2 {
			return exitError, usageError{"director show RUN"}
		}
		return exitOK, directorShow(args[1])
	case "apply":
		return exitOK, directorApply(args[1:])
	}
	return exitError, usageError{fmt.Sprintf("unknown director job %q", args[0])}
}

var missContract = regexp.MustCompile(`(?m)^contract:\s*(\S+)\s*$`)

var runRE = regexp.MustCompile(`^d[0-9]{8}-[0-9]{6}-[a-z0-9]{4}$`)

func directorJob(job string, args []string) (int, error) {
	f, err := parseFlags(args, map[string]bool{"project": true, "model": true, "coins": true,
		"contract": true, "agents": true, "changes": false, "base": true})
	if err != nil {
		return exitError, err
	}
	project, err := projectDir(f.get("project"))
	if err != nil {
		return exitError, err
	}
	if (job == "train" || job == "cast") && len(f.pos) == 0 {
		return exitError, usageError{"director " + job + " needs words: who is needed, or the contract's target"}
	}
	coins := 10 * time.Minute
	if f.has("coins") {
		if coins, err = parseCoins(f.get("coins")); err != nil {
			return exitError, usageError{err.Error()}
		}
	}
	dd, err := directorDir()
	if err != nil {
		return exitError, err
	}
	books, _ := booksDir()
	id, err := newID(books)
	if err != nil {
		return exitError, err
	}
	runID := "d" + time.Now().Format("20060102-150405") + "-" + id
	rd := filepath.Join(dd, runID)
	in, out := filepath.Join(rd, "in"), filepath.Join(rd, "out")
	for _, d := range []string{in, out} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return exitError, err
		}
	}
	run := &directorRun{ID: runID, Job: job, Project: project, Started: time.Now().UTC().Truncate(time.Second)}
	save := func() { _ = writeJSON(filepath.Join(rd, "run.json"), run) }
	save()

	// What every job reads.
	_ = os.WriteFile(filepath.Join(in, "project.txt"), []byte(project+"\n"), 0o600)
	v, err := loadRoster(project)
	if err != nil {
		return exitError, err
	}
	_ = writeJSON(filepath.Join(in, "roster.json"), v)
	_ = os.WriteFile(filepath.Join(in, "naming.txt"), []byte(namingSurvey(project)), 0o600)
	lines, _ := history(project)
	_ = writeJSON(filepath.Join(in, "stats.json"), standings(lines))

	var jobText strings.Builder
	fmt.Fprintf(&jobText, "# Job: %s\n\nProject: %s\n\n", job, project)
	switch job {
	case "train":
		fmt.Fprintf(&jobText, "Who is needed: %s\n", strings.Join(f.pos, " "))
		if cid := f.get("contract"); cid != "" {
			c, cdir, err := loadContract(cid)
			if err != nil {
				return exitError, err
			}
			fmt.Fprintf(&jobText, "\nThe contract that needed them: %q. Its summary is ./in/contracts/%s/summary.md.\n", c.Target, c.ID)
			copyContract(cdir, filepath.Join(in, "contracts", c.ID))
		}
	case "cast":
		fmt.Fprintf(&jobText, "The contract's target: %s\n\n", strings.Join(f.pos, " "))
		logArgs := []string{"log", "-20", "--date=short", "--format=%h %ad %s%n%b", "--stat=100", "--stat-graph-width=10"}
		if f.has("changes") && f.get("base") != "" {
			// The range under contract first, in full; then what came before.
			if inRange, err := git(project, "log", "--date=short", "--format=%h %ad %s%n%b", "--stat=100", "--stat-graph-width=10", f.get("base")+"..HEAD"); err == nil && inRange != "" {
				_ = os.WriteFile(filepath.Join(in, "range.txt"), []byte(inRange+"\n"), 0o600)
			}
		}
		if log, err := git(project, logArgs...); err == nil && log != "" {
			_ = os.WriteFile(filepath.Join(in, "recent.txt"), []byte(log+"\n"), 0o600)
		}
		projectNote(&jobText, project)
		if f.has("changes") {
			ch, err := collectChanges(project, f.get("base"), in)
			if err != nil {
				return exitError, err
			}
			fmt.Fprintf(&jobText, "The contract is on changes since %s: ./in/changes.patch (%d files).\n", ch.Base, len(ch.Files)+len(ch.Untracked))
		}
	case "onboard":
		n, err := gatherAgents(project, f.get("agents"), filepath.Join(in, "agents"))
		if err != nil {
			return exitError, err
		}
		fmt.Fprintf(&jobText, "%d existing agents are in ./in/agents/.\n", n)
		toolsNote(&jobText, filepath.Join(in, "agents"))
		projectNote(&jobText, project)
		fmt.Fprintf(&jobText, "On onboarding a missing setup hook doesn't stop a browser agent becoming a user: the session writes the hook next. Missing equipment does stop an agent that needs it: plan it in ./out/equipment.md.\n")
	case "review":
		ids := unreviewed(project)
		misses := openMisses(project)
		if len(ids) == 0 && len(misses) == 0 {
			return exitError, errors.New("nothing to review: every closed contract here has been reviewed, and no misses are open")
		}
		if len(misses) > 0 {
			for _, m := range misses {
				copyFile(filepath.Join(missedDir(project), m), filepath.Join(in, "missed", m))
				// The contract it was missed on, reviewed before or not.
				b, _ := os.ReadFile(filepath.Join(missedDir(project), m))
				if mm := missContract.FindSubmatch(b); mm != nil && !slices.Contains(ids, string(mm[1])) {
					if _, cdir, err := loadContract(string(mm[1])); err == nil {
						copyContract(cdir, filepath.Join(in, "contracts", string(mm[1])))
					}
				}
			}
			for _, p := range v.Professionals {
				if inside(project, p.Path) {
					copyFile(p.Path, filepath.Join(in, "professionals", p.Name+".md"))
				}
			}
			fmt.Fprintf(&jobText, "%d misses to tune for, in ./in/missed/: %s. The project's own professionals, as their files stand, are in ./in/professionals/.\n",
				len(misses), strings.Join(misses, ", "))
		}
		for _, cid := range ids {
			_, cdir, err := loadContract(cid)
			if err == nil {
				copyContract(cdir, filepath.Join(in, "contracts", cid))
			}
		}
		copyTree(filepath.Join(project, ".administration", "dossiers"), filepath.Join(in, "dossiers"))
		if len(ids) > 0 {
			fmt.Fprintf(&jobText, "%d contracts to review: %s\n", len(ids), strings.Join(ids, ", "))
		}
	}
	_ = os.WriteFile(filepath.Join(in, "job.md"), []byte(jobText.String()), 0o600)

	prompt, _ := fs.ReadFile(house, "house/director.prompt")
	_ = os.WriteFile(filepath.Join(rd, "director.md"), prompt, 0o600)
	settings := filepath.Join(rd, "settings.json")
	deny := []string{"Bash", "WebFetch", "WebSearch", "NotebookEdit", "Agent"}
	for _, g := range secretGlobs {
		deny = append(deny, "Read(**/"+g+")")
	}
	_ = writeJSON(settings, map[string]any{
		"permissions":       map[string]any{"deny": deny, "disableBypassPermissionsMode": "disable"},
		"disableAllHooks":   true,
		"autoMemoryEnabled": false,
	})
	claudeArgs := []string{"-p", "Do the job described in ./in/job.md. Write only into ./out/.",
		"--restricted", "--strict-mcp-config", "--settings", settings, "--no-session-persistence",
		"--permission-prompts", "none", "--output-format", "stream-json", "--verbose",
		"--tools", "Read,Grep,Glob,Write", "--allowedTools", "Edit(" + absRule(out) + "/**)",
		"--add-dir", project, "--append-system-prompt-file", filepath.Join(rd, "director.md")}
	if m := f.get("model"); m != "" {
		if !modelRE.MatchString(m) {
			return exitError, usageError{"--model: a model name or alias"}
		}
		claudeArgs = append(claudeArgs, "--model", m)
	}
	tag := newTag(id, "director")
	rep, state, reason := runHeadless(rd, claudeArgs, tag, coins)
	run.State, run.Reason, run.CostUSD, run.Turns = state, reason, rep.cost, rep.turns
	if job == "onboard" {
		run.Problems = assembleConversions(in, out)
	}
	run.Problems = append(run.Problems, checkDirectorOut(project, job, out)...)
	// One repair round: the checks' problems go back to a fresh session,
	// which fixes those files in place and touches nothing else.
	if len(run.Problems) > 0 && state == "done" {
		_ = os.WriteFile(filepath.Join(in, "problems.md"), []byte("# Problems the checks found in ./out\n\n- "+
			strings.Join(run.Problems, "\n- ")+"\n"), 0o600)
		repairArgs := append([]string{}, claudeArgs...)
		repairArgs[1] = "Your files in ./out/ have the problems listed in ./in/problems.md. Fix each in place (the file format and rules are in your instructions), delete a file only if it can't be fixed, and change nothing else."
		rep2, state2, reason2 := runHeadless(rd, repairArgs, tag, 5*time.Minute)
		if rep2.cost != nil && run.CostUSD != nil {
			total := *run.CostUSD + *rep2.cost
			run.CostUSD = &total
		}
		run.Turns += rep2.turns
		if state2 == "done" {
			run.Repaired = true
			run.Problems = nil
			if job == "onboard" {
				run.Problems = assembleConversions(in, out)
			}
			run.Problems = append(run.Problems, checkDirectorOut(project, job, out)...)
		} else {
			run.Reason = strings.TrimSpace(run.Reason + "; repair round: " + reason2)
		}
	}
	if job == "onboard" {
		// Checked here, not left to the Director: the person always hears it.
		run.Lost = onboardLosses(project, in, out)
		if len(run.Lost) > 0 {
			if f, err := os.OpenFile(filepath.Join(out, "report.md"), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644); err == nil {
				fmt.Fprintf(f, "\n## What onboarding takes away (the Administration's check)\n\n- %s\n", strings.Join(run.Lost, "\n- "))
				f.Close()
			}
		}
	}
	run.Finished = time.Now().UTC().Truncate(time.Second)
	save()
	fireHooks("director", project, map[string]any{"run": runID, "job": job, "state": state,
		"problems": run.Problems, "out": out})
	fmt.Printf("Director %s (%s): %s", runID, job, state)
	if reason != "" {
		fmt.Printf(", %s", reason)
	}
	fmt.Println()
	return exitOK, directorShow(runID)
}

// runHeadless runs claude in dir with the tag, under the coins, and returns
// what its stream said.
func runHeadless(dir string, args []string, tag string, coins time.Duration) (*report, string, string) {
	stream, _ := os.OpenFile(filepath.Join(dir, "stream.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	defer stream.Close()
	errf, _ := os.OpenFile(filepath.Join(dir, "claude.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	defer errf.Close()
	cmd := exec.Command(claudeBin(), args...)
	cmd.Dir = dir
	cmd.Env = tagEnv(childEnv(""), tag+"/pro")
	cmd.Stderr = errf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, w, err := os.Pipe()
	rep := &report{}
	if err != nil {
		return rep, "failed", err.Error()
	}
	defer out.Close()
	cmd.Stdout = w
	if err := cmd.Start(); err != nil {
		w.Close()
		return rep, "failed", fmt.Sprintf("could not start %s: %v", claudeBin(), err)
	}
	w.Close()
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		tee := io.TeeReader(out, stream)
		sc := bufio.NewScanner(tee)
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		for sc.Scan() {
			rep.add(sc.Bytes())
		}
		_, _ = io.Copy(io.Discard, tee)
	}()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	ctx, stop := signalContext()
	defer stop()
	state, reason := "done", ""
	select {
	case err := <-done:
		if err != nil && rep.final == "" {
			state, reason = "failed", "claude ended without finishing; see claude.log"
		}
	case <-time.After(coins):
		state, reason = "out", "shown out at the coin limit"
		killGroup(cmd.Process.Pid, done)
	case <-ctx.Done():
		state, reason = "failed", "stopped"
		killGroup(cmd.Process.Pid, done)
	}
	clearTagged(tag + "/pro")
	select {
	case <-readDone:
	case <-time.After(15 * time.Second):
		out.Close()
		<-readDone
	}
	if rep.isError != "" {
		state, reason = "failed", rep.isError
	}
	return rep, state, reason
}

// namingSurvey lists the agent, skill and command names in and around the
// project, so the Director can follow the house pattern.
func namingSurvey(project string) string {
	var b strings.Builder
	seen := map[string]bool{}
	add := func(kind, dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		var names []string
		for _, e := range entries {
			n := strings.TrimSuffix(e.Name(), ".md")
			if strings.HasPrefix(n, ".") || seen[kind+n] {
				continue
			}
			seen[kind+n] = true
			names = append(names, n)
		}
		sort.Strings(names)
		if len(names) > 0 {
			fmt.Fprintf(&b, "%s in %s:\n", kind, dir)
			for _, n := range names {
				fmt.Fprintf(&b, "  %s\n", n)
			}
		}
	}
	for _, d := range claudeDirsAround(project) {
		add("agents", filepath.Join(d, "agents"))
		add("skills", filepath.Join(d, "skills"))
		add("commands", filepath.Join(d, "commands"))
	}
	add("professionals", filepath.Join(project, ".administration", "professionals"))
	if b.Len() == 0 {
		return "No agents, skills or commands found around this project.\n"
	}
	return b.String()
}

// claudeDirsAround is the .claude folders from the project up to home, then
// the person's own.
func claudeDirsAround(project string) []string {
	home, _ := os.UserHomeDir()
	var dirs []string
	// Up to home, or to the top for a project kept outside it (/srv/work),
	// eight levels at most.
	for d, i := project, 0; i < 8; d, i = filepath.Dir(d), i+1 {
		dirs = append(dirs, filepath.Join(d, ".claude"))
		if d == home || d == filepath.Dir(d) {
			break
		}
	}
	if home != "" {
		dirs = append(dirs, filepath.Join(home, ".claude"))
	}
	var out []string
	seen := map[string]bool{}
	for _, d := range dirs {
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

// gatherAgents copies the agents around the project (or in --agents DIR) into
// dst, with SOURCES.txt saying where each came from, relative to the project.
func gatherAgents(project, only, dst string) (int, error) {
	dirs := []string{}
	if only != "" {
		for _, d := range strings.Split(only, ",") {
			if !filepath.IsAbs(d) {
				d = filepath.Join(project, d)
			}
			dirs = append(dirs, d)
		}
	} else {
		for _, d := range claudeDirsAround(project) {
			dirs = append(dirs, filepath.Join(d, "agents"))
		}
	}
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return 0, err
	}
	var sources strings.Builder
	n := 0
	for _, d := range dirs {
		files, _ := filepath.Glob(filepath.Join(d, "*.md"))
		sort.Strings(files)
		for _, file := range files {
			name := filepath.Base(file)
			if _, err := os.Stat(filepath.Join(dst, name)); err == nil {
				continue // a nearer one of the same name wins
			}
			b, err := os.ReadFile(file)
			if err != nil {
				continue
			}
			if err := os.WriteFile(filepath.Join(dst, name), b, 0o600); err != nil {
				return n, err
			}
			r, err := filepath.Rel(project, file)
			if err != nil {
				r = file
			}
			fmt.Fprintf(&sources, "%s\t%s\n", strings.TrimSuffix(name, ".md"), r)
			n++
		}
	}
	return n, os.WriteFile(filepath.Join(dst, "SOURCES.txt"), []byte(sources.String()), 0o600)
}

// unreviewed is the closed contracts of a project the Director hasn't seen.
func unreviewed(project string) []string {
	books, err := booksDir()
	if err != nil {
		return nil
	}
	files, _ := filepath.Glob(filepath.Join(books, "contracts", "*", "contract.json"))
	var ids []string
	for _, file := range files {
		dir := filepath.Dir(file)
		c, _, err := loadContract(filepath.Base(dir))
		if err != nil || contractKey(c) != projectKey(project) || c.ClosedAt == nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "reviewed")); err == nil {
			continue
		}
		ids = append(ids, c.ID)
	}
	sort.Strings(ids)
	return ids
}

// copyContract copies what the Director reads of a contract: the summary,
// and each professional's report, status and verdicts. Not streams or briefs.
func copyContract(src, dst string) {
	_ = os.MkdirAll(dst, 0o700)
	copyFile(filepath.Join(src, "summary.md"), filepath.Join(dst, "summary.md"))
	entries, _ := os.ReadDir(src)
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "handout" {
			continue
		}
		for _, f := range []string{"report.md", "status.json", "verdicts.json"} {
			copyFile(filepath.Join(src, e.Name(), f), filepath.Join(dst, e.Name(), f))
		}
	}
}

func copyTree(src, dst string) {
	files, _ := filepath.Glob(filepath.Join(src, "*.md"))
	for _, f := range files {
		copyFile(f, filepath.Join(dst, filepath.Base(f)))
	}
}

func copyFile(src, dst string) {
	b, err := os.ReadFile(src)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(dst), 0o700)
	_ = os.WriteFile(dst, b, 0o600)
}

var patternRE = regexp.MustCompile(`(?m)^pattern:\s*(\S+)\s*$`)

// checkDirectorOut holds the Director's files to the same checks as a hire,
// before anyone sees them as ready.
func checkDirectorOut(project, job, out string) []string {
	var problems []string
	pattern := ""
	if b, err := os.ReadFile(filepath.Join(out, "naming.md")); err == nil {
		if m := patternRE.FindStringSubmatch(string(b)); m != nil && m[1] != "none" {
			pattern = m[1]
		}
	}
	var nameOK *regexp.Regexp
	if pattern != "" && strings.Contains(pattern, "{name}") {
		nameOK = regexp.MustCompile("^" + strings.ReplaceAll(regexp.QuoteMeta(pattern), `\{name\}`, `[a-z0-9-]+`) + "$")
	}
	cands, _ := filepath.Glob(filepath.Join(out, "candidates", "*.md"))
	for _, c := range cands {
		b, _ := os.ReadFile(c)
		if q := quoteColonItems(b); !bytes.Equal(q, b) {
			_ = os.WriteFile(c, q, 0o644)
			b = q
		}
		p, err := parseProfessional(b, c, "project", false)
		if err != nil {
			problems = append(problems, fmt.Sprintf("candidates/%s: %v", filepath.Base(c), err))
			continue
		}
		if p.Name+".md" != filepath.Base(c) {
			problems = append(problems, fmt.Sprintf("candidates/%s: its name is %q", filepath.Base(c), p.Name))
		}
		if nameOK != nil && !nameOK.MatchString(p.Name) {
			problems = append(problems, fmt.Sprintf("candidates/%s: doesn't follow the house pattern %s", filepath.Base(c), pattern))
		}
		for _, u := range p.Uses {
			if eq, err := checkEquipment(project, u); err != nil {
				problems = append(problems, fmt.Sprintf("candidates/%s: uses %s: %v (only the project's declared equipment)", filepath.Base(c), u, err))
			} else if why := eq.notFor(p.Kind, p.Browser); why != "" {
				problems = append(problems, fmt.Sprintf("candidates/%s: uses %s: %s", filepath.Base(c), u, why))
			}
		}
	}
	if b, err := os.ReadFile(filepath.Join(out, "roster.toml")); err == nil {
		var cfg projectConfig
		if _, err := toml.Decode(string(b), &cfg); err != nil {
			problems = append(problems, "roster.toml: "+err.Error())
		}
		for name, cr := range cfg.Critics {
			if !nameRE.MatchString(name) {
				problems = append(problems, fmt.Sprintf("roster.toml: critic %q isn't a valid name", name))
			}
			if t, err := cleanTags(cr.Knows); err != nil || len(t) == 0 {
				problems = append(problems, fmt.Sprintf("roster.toml: critic %s needs knows tags", name))
			}
			for _, u := range cr.Uses {
				if eq, err := checkEquipment(project, u); err != nil {
					problems = append(problems, fmt.Sprintf("roster.toml: critic %s uses %s: %v (only the project's declared equipment; plan new equipment in equipment.md instead)", name, u, err))
				} else if why := eq.notFor("critic", false); why != "" {
					problems = append(problems, fmt.Sprintf("roster.toml: critic %s uses %s: %s", name, u, why))
				}
			}
			if cr.File != "" {
				f := cr.File
				if !filepath.IsAbs(f) {
					f = filepath.Join(project, f)
				}
				if _, err := os.Stat(f); err != nil {
					problems = append(problems, fmt.Sprintf("roster.toml: critic %s's file %s doesn't exist", name, cr.File))
				}
			}
		}
		for _, h := range cfg.Handout.Files {
			if st, err := os.Lstat(filepath.Join(project, h)); filepath.IsAbs(h) || !within(filepath.Join(project, h), project) || err != nil || !st.Mode().IsRegular() {
				problems = append(problems, fmt.Sprintf("roster.toml: handout %q isn't a file inside the project; one outside it can't be handed out, so leave it out and name it in report.md for the person to copy in", h))
			}
		}
	}
	doss, _ := filepath.Glob(filepath.Join(out, "dossiers", "*.md"))
	for _, d := range doss {
		st, _ := os.Stat(d)
		if st != nil && st.Size() > dossierLimit {
			problems = append(problems, fmt.Sprintf("dossiers/%s: %d bytes, over the %d limit", filepath.Base(d), st.Size(), dossierLimit))
		}
		b, _ := os.ReadFile(d)
		for _, p := range dossierLint(string(b)) {
			problems = append(problems, fmt.Sprintf("dossiers/%s: %s", filepath.Base(d), p))
		}
		if !nameRE.MatchString(strings.TrimSuffix(filepath.Base(d), ".md")) {
			problems = append(problems, fmt.Sprintf("dossiers/%s: not a professional's name", filepath.Base(d)))
		}
	}
	problems = append(problems, checkEquipmentPlan(filepath.Join(out, "equipment.md"), loadProjectConfig(project).Equipment)...)
	if b, err := os.ReadFile(filepath.Join(out, "cast.md")); err == nil {
		if m := dataLine.FindStringSubmatch(string(b)); m != nil {
			if _, ok := loadProjectConfig(project).Data[m[1]]; !ok {
				problems = append(problems, fmt.Sprintf("cast.md: data: %s isn't one of the project's data sets", m[1]))
			}
		}
	}
	// Nothing else may appear in out.
	_ = filepath.WalkDir(out, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		r, _ := filepath.Rel(out, p)
		ok := r == "report.md" || r == "naming.md" || r == "proposals.md" || r == "reviewed.txt" || r == "roster.toml" || r == "cast.md" || r == "equipment.md" || r == "missed.txt" ||
			strings.HasPrefix(r, "convert/") && strings.HasSuffix(r, ".toml") && !strings.Contains(r[8:], "/") ||
			(strings.HasPrefix(r, "candidates/") || strings.HasPrefix(r, "dossiers/") || strings.HasPrefix(r, "professionals/")) && strings.HasSuffix(r, ".md") && !strings.Contains(r[strings.Index(r, "/")+1:], "/")
		if !ok {
			problems = append(problems, fmt.Sprintf("%s: not something the Director writes; ignored", r))
		}
		return nil
	})
	if job == "onboard" {
		if _, err := os.Stat(filepath.Join(out, "report.md")); err != nil {
			problems = append(problems, "report.md: missing (one line per agent)")
		}
	}
	if t, _ := filepath.Glob(filepath.Join(out, "professionals", "*.md")); len(t) > 0 && job != "review" {
		problems = append(problems, "professionals/: only a review tunes a professional")
	}
	if job == "review" {
		problems = append(problems, checkTuned(project, out)...)
		// Every miss it was given is answered, tuned for or not.
		given, _ := filepath.Glob(filepath.Join(filepath.Dir(out), "in", "missed", "*.md"))
		said, _ := os.ReadFile(filepath.Join(out, "missed.txt"))
		for _, g := range given {
			if !strings.Contains(string(said), filepath.Base(g)) {
				problems = append(problems, fmt.Sprintf("missed.txt: nothing for %s", filepath.Base(g)))
			}
		}
	}
	return problems
}

func loadRun(runID string) (*directorRun, string, error) {
	if !runRE.MatchString(runID) {
		return nil, "", usageError{fmt.Sprintf("%q is not a Director run", runID)}
	}
	dd, err := directorDir()
	if err != nil {
		return nil, "", err
	}
	rd := filepath.Join(dd, runID)
	b, err := os.ReadFile(filepath.Join(rd, "run.json"))
	if err != nil {
		return nil, "", fmt.Errorf("no Director run %s", runID)
	}
	var run directorRun
	if err := json.Unmarshal(b, &run); err != nil {
		return nil, "", err
	}
	return &run, rd, nil
}

func directorShow(runID string) error {
	run, rd, err := loadRun(runID)
	if err != nil {
		return err
	}
	out := filepath.Join(rd, "out")
	fmt.Printf("Director run %s: %s on %s, %s\n", run.ID, run.Job, run.Project, run.State)
	_ = filepath.WalkDir(out, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			r, _ := filepath.Rel(out, p)
			fmt.Printf("  out/%s\n", r)
		}
		return nil
	})
	for _, p := range run.Problems {
		fmt.Printf("  problem: %s\n", p)
	}
	if len(run.Lost) > 0 {
		fmt.Printf("\nWhat onboarding takes away (each agent's own tools against what it joins as):\n")
		for _, l := range run.Lost {
			fmt.Printf("  - %s\n", l)
		}
	}
	// The plan is what the person decides on; print it so reading it takes
	// no permission to a folder outside the project.
	if b, err := os.ReadFile(filepath.Join(out, "cast.md")); err == nil {
		fmt.Printf("\n%s\n", strings.TrimSpace(string(b)))
	}
	// What the contract needs and nothing here reaches: a plan for the
	// session (or the person) to find or build. apply never installs it.
	if b, err := os.ReadFile(filepath.Join(out, "equipment.md")); err == nil {
		fmt.Printf("\n%s\n\n  (equipment is never applied: build or find it, add it to .administration/roster.toml, and cast again)\n", strings.TrimSpace(string(b)))
	}
	if b, err := os.ReadFile(filepath.Join(out, "missed.txt")); err == nil {
		fmt.Printf("\nMisses (out/professionals/ has each tuned professional's file, whole):\n%s\n", strings.TrimSpace(string(b)))
	}
	if run.Applied {
		fmt.Println("  applied")
	} else if len(run.Problems) == 0 && run.State == "done" {
		fmt.Printf("  apply with: administration director apply %s\n", run.ID)
	}
	fmt.Printf("  files: %s\n", out)
	return nil
}

// directorApply puts a run's checked files into the project: candidates to
// .administration/candidates/ (hiring each is still the person's call),
// dossiers to .administration/dossiers/, roster.toml merged into the project's.
// The project's previous files are kept in the run folder.
func directorApply(args []string) error {
	f, err := parseFlags(args, map[string]bool{"force": false})
	if err != nil {
		return err
	}
	if len(f.pos) != 1 {
		return usageError{"director apply RUN"}
	}
	run, rd, err := loadRun(f.pos[0])
	if err != nil {
		return err
	}
	if run.Applied {
		return fmt.Errorf("run %s is already applied", run.ID)
	}
	if run.State != "done" && !f.has("force") {
		return fmt.Errorf("run %s didn't finish (%s); --force to apply what it wrote anyway", run.ID, run.State)
	}
	if problems := checkDirectorOut(run.Project, run.Job, filepath.Join(rd, "out")); len(problems) > 0 {
		return fmt.Errorf("run %s has problems; fix them or run the Director again:\n  %s", run.ID, strings.Join(problems, "\n  "))
	}
	out := filepath.Join(rd, "out")
	cont := filepath.Join(run.Project, ".administration")
	backup := filepath.Join(rd, "before")
	moved := 0
	for _, sub := range []string{"candidates", "dossiers"} {
		files, _ := filepath.Glob(filepath.Join(out, sub, "*.md"))
		for _, src := range files {
			dst := filepath.Join(cont, sub, filepath.Base(src))
			copyFile(dst, filepath.Join(backup, sub, filepath.Base(src)))
			b, _ := os.ReadFile(src)
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(dst, b, 0o644); err != nil {
				return err
			}
			moved++
		}
	}
	if b, err := os.ReadFile(filepath.Join(out, "roster.toml")); err == nil {
		dst := filepath.Join(cont, "roster.toml")
		copyFile(dst, filepath.Join(backup, "roster.toml"))
		old, _ := os.ReadFile(dst)
		merged := strings.TrimRight(string(old), "\n")
		if merged != "" {
			merged += "\n\n"
		}
		var have map[string]any
		_, _ = toml.Decode(string(old), &have)
		add, kept := newSections(string(b), have)
		for _, k := range kept {
			fmt.Printf("  roster.toml: kept your [%s]\n", k)
		}
		merged += "# From Director run " + run.ID + "\n" + add
		var check projectConfig
		if _, err := toml.Decode(merged, &check); err != nil {
			return fmt.Errorf("roster.toml wouldn't merge cleanly (%v); the run's version is %s", err, filepath.Join(out, "roster.toml"))
		}
		if err := os.MkdirAll(cont, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, []byte(merged+"\n"), 0o644); err != nil {
			return err
		}
		moved++
	}
	if b, err := os.ReadFile(filepath.Join(out, "naming.md")); err == nil {
		if m := patternRE.FindStringSubmatch(string(b)); m != nil {
			_ = os.WriteFile(filepath.Join(cont, "naming"), []byte(m[1]+"\n"), 0o644)
		}
	}
	if run.Job == "review" {
		n, err := applyTuned(run.Project, out, backup)
		if err != nil {
			return err
		}
		moved += n
		books, _ := booksDir()
		b, _ := os.ReadFile(filepath.Join(out, "reviewed.txt"))
		for _, id := range strings.Fields(string(b)) {
			if idRE.MatchString(id) {
				_ = os.WriteFile(filepath.Join(books, "contracts", id, "reviewed"), []byte(run.ID+"\n"), 0o600)
			}
		}
	}
	run.Applied = true
	_ = writeJSON(filepath.Join(rd, "run.json"), run)
	fmt.Printf("Applied %d files from %s to %s. Candidates still need hiring (administration hire NAME); proposals are in %s.\n",
		moved, run.ID, cont, filepath.Join(out, "proposals.md"))
	return nil
}

func signalContext() (context.Context, context.CancelFunc) {
	return signalNotifyContext()
}

func signalNotifyContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
}

// projectNote tells the Director what the project gives professionals to
// work with: a setup hook (the web app, for users), the equipment declared,
// and the data sets.
func projectNote(b *strings.Builder, project string) {
	hasEq := false
	for n := range loadProjectConfig(project).Equipment {
		if _, err := checkEquipment(project, n); err == nil {
			hasEq = true
		}
	}
	if st, err := os.Stat(filepath.Join(project, ".administration", "setup")); err == nil && st.Mode()&0o111 != 0 {
		fmt.Fprintf(b, "The project has .administration/setup, so browser professionals (users) can work its running app.\n")
	} else if hasEq {
		fmt.Fprintf(b, "The project has no .administration/setup, so no user has the web browser; users work through the equipment below (uses, browser: false).\n")
	} else {
		fmt.Fprintf(b, "The project has no .administration/setup and no equipment: no users; critics only.\n")
	}
	equipmentNote(b, project)
	if sets := loadProjectConfig(project).Data; len(sets) > 0 {
		names := make([]string, 0, len(sets))
		for n := range sets {
			names = append(names, n)
		}
		sort.Strings(names)
		fmt.Fprintf(b, "Data sets the rooms can start from (the setup hook's default otherwise):\n")
		for _, n := range names {
			fmt.Fprintf(b, "- %s: %s\n", n, strings.TrimSpace(sets[n].About))
		}
	}
}

var sectionRE = regexp.MustCompile(`^\[([A-Za-z0-9_.-]+)\]\s*$`)

// newSections is the part of a run's roster.toml whose tables the project's
// doesn't have yet; a table it has already (the same critic onboarded
// twice) stays the person's, and is named in kept.
func newSections(run string, have map[string]any) (add string, kept []string) {
	var b strings.Builder
	skip := false
	for _, line := range strings.SplitAfter(run, "\n") {
		if m := sectionRE.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			skip = hasTable(have, strings.Split(m[1], "."))
			if skip {
				kept = append(kept, m[1])
			}
		}
		if !skip {
			b.WriteString(line)
		}
	}
	return b.String(), kept
}

func hasTable(m map[string]any, path []string) bool {
	v, ok := m[path[0]]
	if !ok {
		return false
	}
	if len(path) == 1 {
		return true
	}
	sub, ok := v.(map[string]any)
	return ok && hasTable(sub, path[1:])
}

var colonItem = regexp.MustCompile(`^(\s*- )([^"'\[{|>].*: .*)$`)

// quoteColonItems quotes a frontmatter list item with ": " in it (the
// Director writes "- reads only: Read, Grep" often), which YAML would
// otherwise read as a map; the body is left alone.
func quoteColonItems(b []byte) []byte {
	lines := strings.SplitAfter(string(b), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return b
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			break
		}
		l := strings.TrimRight(lines[i], "\n")
		if m := colonItem.FindStringSubmatch(l); m != nil {
			q, _ := json.Marshal(strings.TrimSpace(m[2]))
			lines[i] = m[1] + string(q) + lines[i][len(l):]
		}
	}
	return []byte(strings.Join(lines, ""))
}

// equipmentNote tells the Director what equipment the project declares,
// each with what it reaches, so it can cast users who work through it.
func equipmentNote(b *strings.Builder, project string) {
	fmt.Fprintf(b, "For equipment you plan with server = \"playwright\", its tools come from these only: %s. A device a person looks at (a phone, a VM's desktop) is server = \"screen\" with the project's driver. Anything else (a custom action like dropping files) is a program under server = \"commands\", or a server of the project's own.\n",
		strings.Join(browserTools, ", "))
	eqs := loadProjectConfig(project).Equipment
	names := make([]string, 0, len(eqs))
	for n := range eqs {
		if _, err := checkEquipment(project, n); err == nil {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return
	}
	sort.Strings(names)
	fmt.Fprintf(b, "The project's equipment, for users who work through it (administration.uses):\n")
	for _, n := range names {
		fmt.Fprintf(b, "- %s: %s\n", n, strings.TrimSpace(eqs[n].About))
	}
}

var tomlBlock = regexp.MustCompile("(?s)```toml\n(.*?)```")

// checkEquipmentPlan reads the declarations in an equipment plan the way
// place would read them in roster.toml, so a tool that doesn't exist goes
// back to the Director for repair rather than to the person's session.
func checkEquipmentPlan(path string, have map[string]equipment) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var problems []string
	for _, m := range tomlBlock.FindAllStringSubmatch(string(b), -1) {
		var cfg projectConfig
		if _, err := toml.Decode(m[1], &cfg); err != nil {
			problems = append(problems, "equipment.md: a toml block doesn't parse: "+err.Error())
			continue
		}
		for name, eq := range cfg.Equipment {
			// A block for equipment the project has is a change to it: what
			// it leaves out stays as declared.
			if old, ok := have[name]; ok {
				if eq.About == "" {
					eq.About = old.About
				}
				if eq.Server == "" {
					eq.Server = old.Server
					if len(eq.Tools) == 0 {
						eq.Tools = old.Tools
					}
				}
				if len(eq.Commands) == 0 {
					eq.Commands = old.Commands
				}
			}
			if !nameRE.MatchString(name) {
				problems = append(problems, fmt.Sprintf("equipment.md: %q isn't a valid name", name))
			}
			if strings.TrimSpace(eq.About) == "" {
				problems = append(problems, fmt.Sprintf("equipment.md: %s has no about", name))
			}
			switch eq.Server {
			case "playwright":
				for _, t := range eq.Tools {
					if !slices.Contains(browserTools, t) {
						problems = append(problems, fmt.Sprintf("equipment.md: %s: %q isn't a browser tool (they are: %s); a custom action is a program under server = \"commands\"", name, t, strings.Join(browserTools, ", ")))
					}
				}
			case "commands":
				if len(eq.Commands) == 0 {
					problems = append(problems, fmt.Sprintf("equipment.md: %s: server = \"commands\" needs commands", name))
				}
			case "screen":
				if eq.Driver == "" {
					problems = append(problems, fmt.Sprintf("equipment.md: %s: server = \"screen\" needs a driver", name))
				}
				if len(eq.Tools) > 0 {
					problems = append(problems, fmt.Sprintf("equipment.md: %s: a screen's tools are the house's; leave tools out", name))
				}
			case "":
				problems = append(problems, fmt.Sprintf("equipment.md: %s has no server", name))
			}
		}
	}
	return problems
}

// conversion is the Director's decision for one existing agent that joins
// as a user: everything the Administration needs,
// and a note on what of the original doesn't apply here. The agent's own
// body is carried over word for word: a person's refined persona is the
// point of onboarding it, and a rewrite loses what made it work (one
// owner persona, rewritten to half its length, missed every regression the
// original had found).
type conversion struct {
	Description string   `toml:"description"`
	Kind        string   `toml:"kind"`
	Coins       string   `toml:"coins"`
	Knows       []string `toml:"knows"`
	Constraints []string `toml:"constraints"`
	SignsInAs   string   `toml:"signs_in_as"`
	Sees        string   `toml:"sees"`
	Uses        []string `toml:"uses"`
	Browser     *bool    `toml:"browser"`
	Model       string   `toml:"model"`
	Notes       string   `toml:"notes"`
	Handout     []string `toml:"handout"`
}

// assembleConversions turns ./out/convert/<agent>.toml into candidates:
// the Administration's frontmatter, the Director's note, then the agent's
// own body from ./in/agents/. Handout files they name go into the run's
// roster.toml. Problems are reported by checkDirectorOut on the result.
func assembleConversions(in, out string) []string {
	files, _ := filepath.Glob(filepath.Join(out, "convert", "*.toml"))
	var problems []string
	handout := map[string]bool{}
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".toml")
		var cv conversion
		if _, err := toml.DecodeFile(f, &cv); err != nil {
			problems = append(problems, fmt.Sprintf("convert/%s.toml: %v", name, err))
			continue
		}
		src, err := os.ReadFile(filepath.Join(in, "agents", name+".md"))
		if err != nil {
			problems = append(problems, fmt.Sprintf("convert/%s.toml: no agent called %s in ./in/agents/", name, name))
			continue
		}
		_, body, err := splitFrontmatter(src)
		if err != nil {
			body = string(src)
		}
		fm := map[string]any{"name": name, "description": cv.Description}
		if cv.Model != "" {
			fm["model"] = cv.Model
		}
		adm := map[string]any{"kind": cv.Kind, "coins": cv.Coins, "knows": cv.Knows}
		for k, v := range map[string]any{"constraints": cv.Constraints, "uses": cv.Uses} {
			if l, _ := v.([]string); len(l) > 0 {
				adm[k] = l
			}
		}
		if cv.SignsInAs != "" {
			adm["signs_in_as"] = cv.SignsInAs
		}
		if cv.Sees != "" {
			adm["sees"] = cv.Sees
		}
		if cv.Browser != nil {
			adm["browser"] = *cv.Browser
		}
		fm["administration"] = adm
		y, err := yaml.Marshal(fm)
		if err != nil {
			problems = append(problems, fmt.Sprintf("convert/%s.toml: %v", name, err))
			continue
		}
		var b strings.Builder
		b.WriteString("---\n" + string(y) + "---\n")
		if n := strings.TrimSpace(cv.Notes); n != "" {
			b.WriteString("## In the Administration\n\n" + n + "\n\n## Your persona, as written\n\n")
		}
		body = strings.TrimLeft(body, "\n")
		if !strings.HasSuffix(body, "\n") {
			body += "\n"
		}
		b.WriteString(body)
		dst := filepath.Join(out, "candidates", name+".md")
		_ = os.MkdirAll(filepath.Dir(dst), 0o755)
		if err := os.WriteFile(dst, []byte(b.String()), 0o644); err != nil {
			problems = append(problems, err.Error())
		}
		for _, h := range cv.Handout {
			handout[h] = true
		}
	}
	if len(handout) > 0 {
		names := make([]string, 0, len(handout))
		for h := range handout {
			names = append(names, h)
		}
		sort.Strings(names)
		rt := filepath.Join(out, "roster.toml")
		old, _ := os.ReadFile(rt)
		if !strings.Contains(string(old), "[handout]") {
			q := make([]string, len(names))
			for i, n := range names {
				q[i] = strconv.Quote(n)
			}
			add := "\n[handout]\nfiles = [" + strings.Join(q, ", ") + "]\n"
			_ = os.WriteFile(rt, append(old, []byte(add)...), 0o644)
		}
	}
	return problems
}
