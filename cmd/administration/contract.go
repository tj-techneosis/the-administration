package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// A contract is a directory in the books: contract.json, and one directory
// per professional holding their brief, settings, status, stream and report.
type contract struct {
	ID            string     `json:"id"`
	Target        string     `json:"target"`
	Project       string     `json:"project"`
	Key           string     `json:"key,omitempty"` // the project's name for the books (projectKey)
	PlacedAt      time.Time  `json:"placed_at"`
	PlacedBy      string     `json:"placed_by"`
	Professionals []castItem `json:"professionals"`
	ClosedAt      *time.Time `json:"closed_at,omitempty"`
	Changes       *changes   `json:"changes,omitempty"`
	// PATH when the contract was placed, for the workers.
	Path      string     `json:"path,omitempty"`
	StartedAt *time.Time `json:"started_at,omitempty"`
	// Users' browsers open as windows on the person's screen, to watch.
	ShowBrowsers bool `json:"show_browsers,omitempty"`
	// On changes, users also get the project as it was before them.
	Before bool `json:"before,omitempty"`
	// The project's data set the rooms start from ([data] in roster.toml);
	// empty is the setup hook's default.
	Data string `json:"data,omitempty"`
	// The one-off specialists close put on the roster, and why any it
	// couldn't keep wasn't kept.
	Kept    []string `json:"kept,omitempty"`
	NotKept []string `json:"not_kept,omitempty"`
}

type castItem struct {
	Name      string  `json:"name"`
	Kind      string  `json:"kind"`
	Source    string  `json:"source"`
	CoinsMin  float64 `json:"coins_min"`
	SignsInAs string  `json:"signs_in_as,omitempty"`
	Sees      string  `json:"sees,omitempty"`
	Model     string  `json:"model,omitempty"`
	Uses      []string `json:"uses,omitempty"`       // the project's equipment it works through
	NoBrowser bool     `json:"no_browser,omitempty"` // a user that works only through equipment
	Dossier   int     `json:"dossier_bytes,omitempty"`
	// This professional's own task in the contract (--brief NAME=TEXT),
	// written by whoever knows what changed: one task each.
	Task string `json:"task,omitempty"`
}

// Terminal states: back (finished on their own), out (shown out at the coin
// limit, report kept), closed (stopped by close), refused (the Rules turned
// the room away), failed (the room or the harness broke).
type status struct {
	State      string     `json:"state"` // queued, setup, working, back, out, closed, refused, failed
	Reason     string     `json:"reason,omitempty"`
	WorkerPID  int        `json:"worker_pid,omitempty"`
	Groups     []int      `json:"groups,omitempty"` // process groups to clear: the professional's, the room's servers'

	Room       string     `json:"room,omitempty"`
	URL        string     `json:"url,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	ExitCode   *int       `json:"exit_code,omitempty"`
	Ports      []int      `json:"ports,omitempty"`
	Blocked    int        `json:"blocked,omitempty"` // browser requests the gate turned away
	Leftovers  string     `json:"leftovers,omitempty"`
	// The release check's before app: its checkout and address.
	BeforeSrc string `json:"before_src,omitempty"`
	BeforeURL string `json:"before_url,omitempty"`
	Findings   int        `json:"findings"`
	Unreached  int        `json:"unreached"`
	// Usage as claude reports it, kept for anyone who wants it and shown
	// nowhere. Nil when the professional was shown out before it could say.
	CostUSD    *float64   `json:"cost_usd,omitempty"`
	Turns      int        `json:"turns,omitempty"`
	TokensIn   int        `json:"tokens_in,omitempty"`
	TokensOut  int        `json:"tokens_out,omitempty"`
	TornDown   bool       `json:"torn_down,omitempty"`
}

func (s status) terminal() bool {
	switch s.State {
	case "back", "out", "closed", "refused", "failed":
		return true
	}
	return false
}

var idRE = regexp.MustCompile(`^[a-z0-9]{4}$`)

func contractDir(id string) (string, error) {
	if !idRE.MatchString(id) {
		return "", usageError{fmt.Sprintf("%q is not a contract id", id)}
	}
	b, err := booksDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(b, "contracts", id), nil
}

func loadContract(id string) (*contract, string, error) {
	dir, err := contractDir(id)
	if err != nil {
		return nil, "", err
	}
	b, err := os.ReadFile(filepath.Join(dir, "contract.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", fmt.Errorf("no contract %s", id)
	} else if err != nil {
		return nil, "", err
	}
	var c contract
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, "", fmt.Errorf("contract %s: %v", id, err)
	}
	return &c, dir, nil
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readStatus(dir, name string) status {
	s := status{State: "queued"}
	if b, err := os.ReadFile(filepath.Join(dir, name, "status.json")); err == nil {
		if json.Unmarshal(b, &s) != nil {
			s = status{State: "queued"}
		}
	}
	if s.WorkerPID == 0 {
		if b, err := os.ReadFile(filepath.Join(dir, name, "worker.pid")); err == nil {
			s.WorkerPID, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		}
	}
	// A worker that died without recording how is failed, not working
	// forever. Alive means the pid still carries this professional's tag: a
	// reused pid belongs to someone else.
	if !s.terminal() && s.WorkerPID > 0 && !taggedAlive(s.WorkerPID, readTag(dir, name)) {
		s.State, s.Reason = "failed", "the worker stopped without recording an outcome; see worker.log"
	}
	return s
}

// readTag is the professional's process tag, written by the spawner.
func readTag(dir, name string) string {
	b, err := os.ReadFile(filepath.Join(dir, name, "tag"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func newID(books string) (string, error) {
	const digits = "abcdefghijklmnopqrstuvwxyz0123456789"
	for range 50 {
		id := make([]byte, 4)
		for i := range id {
			n, err := rand.Int(rand.Reader, big.NewInt(int64(len(digits))))
			if err != nil {
				return "", err
			}
			id[i] = digits[n.Int64()]
		}
		if _, err := os.Stat(filepath.Join(books, "contracts", string(id))); errors.Is(err, os.ErrNotExist) {
			return string(id), nil
		}
	}
	return "", errors.New("could not find a free contract id")
}

func placedBy() string {
	if s := os.Getenv("CLAUDE_CODE_SESSION_ID"); s != "" {
		return "claude:" + s[:min(8, len(s))]
	}
	if u := os.Getenv("USER"); u != "" {
		return "user:" + u
	}
	return "user"
}

func maxRooms() int {
	if n, err := strconv.Atoi(os.Getenv("ADMINISTRATION_MAX_ROOMS")); err == nil && n > 0 {
		return n
	}
	return 4
}

// liveRooms counts browser professionals still holding a room, across every
// open contract: each room runs its own stack, and the machine has only so
// much memory.
func liveRooms() int {
	books, err := booksDir()
	if err != nil {
		return 0
	}
	dirs, _ := filepath.Glob(filepath.Join(books, "contracts", "*", "contract.json"))
	n := 0
	for _, f := range dirs {
		dir := filepath.Dir(f)
		c, _, err := loadContract(filepath.Base(dir))
		if err != nil || c.ClosedAt != nil {
			continue
		}
		for _, p := range c.Professionals {
			if p.Kind == "user" {
				s := readStatus(dir, p.Name)
				if !s.terminal() || (s.Room != "" && !s.TornDown) {
					n++
				}
			}
		}
	}
	return n
}

func cmdPlace(args []string) error {
	f, err := parseFlags(args, map[string]bool{"cast": true, "with": true, "coins": true, "model": true, "project": true, "dry-run": false,
		"changes": false, "base": true, "show-browsers": false, "no-watch": false, "no-before": false, "data": true, "brief": true})
	if err != nil {
		return err
	}
	target := strings.TrimSpace(strings.Join(f.pos, " "))
	if target == "" {
		return usageError{"place needs a target: what the contract is on"}
	}
	if strings.TrimSpace(f.get("cast")) == "" && strings.TrimSpace(f.get("with")) == "" {
		return usageError{"place needs --cast a,b (from the roster) and/or --with FILE|RUN (one-off professionals, or a Director cast run)"}
	}
	if f.has("show-browsers") {
		if runtime.GOOS == "linux" && os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return usageError{"--show-browsers needs a screen, and this shell has none (no DISPLAY or WAYLAND_DISPLAY); use administration watch instead"}
		}
	}
	var coins time.Duration
	if f.has("coins") {
		if coins, err = parseCoins(f.get("coins")); err != nil {
			return usageError{err.Error()}
		}
	}
	project, err := projectDir(f.get("project"))
	if err != nil {
		return err
	}
	v, err := loadRoster(project)
	if err != nil {
		return err
	}
	byName := map[string]*professional{}
	for _, p := range v.Professionals {
		byName[p.Name] = p
	}
	var cast []*professional
	seen := map[string]bool{}
	users := 0
	// One-off professionals for this contract only: files, or the
	// candidates a Director cast run wrote. Held to the same checks as a hire.
	with, err := withProfessionals(f.get("with"))
	if err != nil {
		return err
	}
	for _, p := range with {
		if seen[p.Name] {
			return usageError{fmt.Sprintf("two professionals called %s", p.Name)}
		}
		seen[p.Name] = true
		if p.Kind == "user" {
			users++
		}
		cast = append(cast, p)
	}
	for _, n := range strings.Split(f.get("cast"), ",") {
		n = strings.TrimSpace(n)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		p, ok := byName[n]
		if !ok {
			names := make([]string, 0, len(byName))
			for k := range byName {
				names = append(names, k)
			}
			sort.Strings(names)
			return usageError{fmt.Sprintf("nobody called %q on the roster (have: %s)", n, strings.Join(names, ", "))}
		}
		if p.Kind == "user" {
			users++
		}
		cast = append(cast, p)
	}
	if _, err := exec.LookPath(claudeBin()); err != nil {
		return usageError{fmt.Sprintf("professionals run the standalone Claude Code CLI, and %q isn't on PATH. The VS Code extension doesn't provide it: install it (code.claude.com/docs/en/setup) or set ADMINISTRATION_CLAUDE", claudeBin())}
	}
	webUsers, needBrowser := 0, false
	for _, p := range cast {
		if p.Kind == "critic" {
			// A critic runs what it reads; it has no screen to look at.
			for _, u := range p.Uses {
				eq, err := checkEquipment(project, u)
				if err != nil {
					return usageError{fmt.Sprintf("%s uses %s: %v", p.Name, u, err)}
				}
				if why := eq.notFor(p.Kind, p.Browser); why != "" {
					return usageError{fmt.Sprintf("%s uses %s: %s", p.Name, u, why)}
				}
			}
		}
		if p.Kind != "user" {
			continue
		}
		if p.Browser {
			webUsers++
			needBrowser = true
		}
		for _, u := range p.Uses {
			eq, err := checkEquipment(project, u)
			if err != nil {
				return usageError{fmt.Sprintf("%s uses %s: %v", p.Name, u, err)}
			}
			if why := eq.notFor(p.Kind, p.Browser); why != "" {
				return usageError{fmt.Sprintf("%s uses %s: %s", p.Name, u, why)}
			}
			needBrowser = needBrowser || eq.Server == "playwright"
		}
	}
	if needBrowser {
		if _, err := exec.LookPath("node"); err != nil {
			return usageError{"browser professionals need Node for the browser, and node isn't on PATH"}
		}
		if _, err := os.Stat(playwrightCLI()); err != nil {
			return usageError{fmt.Sprintf("the browser isn't installed (%s); run install.sh", playwrightCLI())}
		}
	}
	if webUsers > 0 {
		setup := filepath.Join(project, ".administration", "setup")
		if st, err := os.Stat(setup); err != nil || st.Mode()&0o111 == 0 {
			return usageError{fmt.Sprintf("browser professionals need a room, and %s has no executable .administration/setup (see the README, A House of Your Own)", project)}
		}
	}
	if users > 0 {
		// Held until the workers are started, so two contracts placed at
		// once can't both slip under the room limit.
		unlock, err := lockBooks()
		if err != nil {
			return err
		}
		defer unlock()
		if live := liveRooms(); live+users > maxRooms() {
			return refusal{fmt.Sprintf("%d rooms are in use and this contract needs %d more; the limit is %d (ADMINISTRATION_MAX_ROOMS). Close a contract first.", live, users, maxRooms())}
		}
		rooms, err := roomsDir()
		if err != nil {
			return err
		}
		// The browser keeps a socket under the room's tmp, and a socket path
		// can't be long (Playwright refused one of 86 bytes). A room's tmp is
		// the rooms folder plus "/<id>/rNN/tmp".
		if len(rooms)+len("/xxxx/r99/tmp") > maxRoomTmp {
			return usageError{fmt.Sprintf("the rooms folder's path (%s) is too long for the browser's socket; set ADMINISTRATION_ROOMS to a folder of at most %d characters", rooms, maxRoomTmp-len("/xxxx/r99/tmp"))}
		}
	}

	books, err := booksDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(books, "contracts"), 0o700); err != nil {
		return err
	}
	id, err := newID(books)
	if err != nil {
		return err
	}
	c := &contract{ID: id, Target: target, Project: project, Key: projectKey(project), PlacedAt: time.Now().UTC().Truncate(time.Second),
		PlacedBy: placedBy(), Path: os.Getenv("PATH"), ShowBrowsers: f.has("show-browsers")}
	for _, p := range cast {
		ci := castItem{Name: p.Name, Kind: p.Kind, Source: p.Source, CoinsMin: p.CoinsMin, SignsInAs: p.SignsInAs, Sees: p.Sees,
			Model: p.Model, Uses: p.Uses, NoBrowser: p.Kind == "user" && !p.Browser,
			Dossier: len(readDossier(project, p.Name))}
		if coins > 0 {
			ci.CoinsMin = coins.Minutes()
		}
		if m := f.get("model"); m != "" {
			if !modelRE.MatchString(m) {
				return usageError{"--model: a model name or alias like haiku, sonnet, opus"}
			}
			ci.Model = m
		}
		c.Professionals = append(c.Professionals, ci)
	}
	for _, kv := range f.all["brief"] {
		name, task, ok := strings.Cut(kv, "=")
		name, task = strings.TrimSpace(name), strings.TrimSpace(task)
		found := false
		for i := range c.Professionals {
			if c.Professionals[i].Name == name {
				c.Professionals[i].Task, found = task, true
			}
		}
		if !ok || task == "" {
			return usageError{"--brief NAME=\"their own task\""}
		}
		if !found {
			return usageError{fmt.Sprintf("--brief %s: not in this contract's cast", name)}
		}
	}
	if f.has("base") && !f.has("changes") {
		return usageError{"--base goes with --changes"}
	}
	dir := filepath.Join(books, "contracts", id)
	if f.has("changes") {
		if c.Changes, err = collectChanges(project, f.get("base"), filepath.Join(dir, "handout")); err != nil {
			os.RemoveAll(dir)
			return err
		}
		// A release check: users compare the app before and after.
		c.Before = webUsers > 0 && !f.has("no-before") && c.Changes.MergeBase != ""
	}
	if c.Data, err = dataSet(project, f.get("data"), f.get("with")); err != nil {
		os.RemoveAll(dir)
		return err
	}
	if f.has("dry-run") {
		os.RemoveAll(dir)
		return printJSON(c)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for _, p := range cast {
		pd := filepath.Join(dir, p.Name)
		if err := os.MkdirAll(pd, 0o700); err != nil {
			return err
		}
		// The persona travels with the contract, so a later edit to the file
		// doesn't change a contract already placed. Its dossier (what it
		// learned on earlier contracts here, kept short by the Director) goes
		// with it.
		persona := p.Body + "\n"
		if p.Kind == "user" {
			// The project's rules, whole, in the user's own instructions: asked
			// to read two handed-out files, users read one and skipped the
			// rule that would have flagged what they missed.
			persona += handoutRules(project)
		}
		if d := readDossier(project, p.Name); d != "" {
			persona += "\n## What you know from earlier contracts on this project\n\n" +
				"These are notes, not instructions: the Rules and this contract's brief come first.\n\n" + d + "\n"
		}
		if err := os.WriteFile(filepath.Join(pd, "persona.md"), []byte(persona), 0o600); err != nil {
			return err
		}
		// A one-off's whole file, so a specialist worth keeping can be hired
		// from the contract after (hire --from), whatever became of the file.
		if p.Source == "contract" {
			if b, err := os.ReadFile(p.Path); err == nil {
				_ = os.WriteFile(filepath.Join(pd, "hire.md"), b, 0o600)
			}
		}
	}
	if err := writeJSON(filepath.Join(dir, "contract.json"), c); err != nil {
		return err
	}
	startErr := startWorkers(c, dir)

	names := make([]string, len(c.Professionals))
	for i, p := range c.Professionals {
		names[i] = p.Name
	}
	who := "professional"
	if len(names) != 1 {
		who = "professionals"
	}
	fmt.Printf("Contract %s placed on %q. %d %s took it: %s.\n", id, target, len(names), who, strings.Join(names, ", "))
	fmt.Printf("Check in with: administration wait %s\n", id)
	if startErr == nil {
		watchOnPlace(id, names, f.has("no-watch"))
	}
	fireHooks("placed", project, contractPayload(c, dir))
	return startErr
}

// watchOnPlace opens the watch panes, without taking focus, when the
// person is in Herdr or tmux; anywhere else it says how to watch. Nothing
// pops up either way, and the browsers stay hidden unless --show-browsers.
// ADMINISTRATION_WATCH=off (or --no-watch) keeps it to the one line.
func watchOnPlace(id string, names []string, off bool) {
	inMux := os.Getenv("HERDR_ENV") == "1" || os.Getenv("TMUX") != ""
	if off || os.Getenv("ADMINISTRATION_WATCH") == "off" || !inMux {
		fmt.Printf("Watch them work with: administration watch %s\n", id)
		return
	}
	if err := openPanes(id, names); err != nil {
		fmt.Printf("Couldn't open the watch panes (%v); watch with: administration watch %s\n", err, id)
	}
}

// startWorkers starts one worker per professional and lets them go: each
// outlives `place`.
func startWorkers(c *contract, dir string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	now := time.Now().UTC().Truncate(time.Second)
	c.StartedAt = &now
	if err := writeJSON(filepath.Join(dir, "contract.json"), c); err != nil {
		return err
	}
	failed := 0
	for _, p := range c.Professionals {
		cmd, err := spawnWorker(self, dir, c.ID, p.Name)
		if err != nil {
			failed++
			s := status{State: "failed", Reason: "could not start the worker: " + err.Error()}
			_ = writeJSON(filepath.Join(dir, p.Name, "status.json"), s)
			continue
		}
		_ = cmd.Process.Release()
	}
	if failed > 0 {
		return startError{fmt.Sprintf("%d of %d workers could not start; see status", failed, len(c.Professionals))}
	}
	return nil
}

// spawnWorker starts `administration _work` in a session of its own, so it
// outlives the shell (or the Claude Code tool call) that placed the contract.
func spawnWorker(self, dir, id, name string) (*exec.Cmd, error) {
	logf, err := os.OpenFile(filepath.Join(dir, name, "worker.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	defer logf.Close()
	null, err := os.Open(os.DevNull)
	if err != nil {
		return nil, err
	}
	defer null.Close()
	tag := newTag(id, name)
	if err := os.WriteFile(filepath.Join(dir, name, "tag"), []byte(tag+"\n"), 0o600); err != nil {
		return nil, err
	}
	cmd := exec.Command(self, "_work", id, name)
	cmd.Env = tagEnv(os.Environ(), tag)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = null, logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	// The pid goes in a file of its own: the worker owns status.json and may
	// already have written it.
	_ = os.WriteFile(filepath.Join(dir, name, "worker.pid"), []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o600)
	return cmd, nil
}

func cmdStatus(args []string) error {
	f, err := parseFlags(args, map[string]bool{"json": false})
	if err != nil {
		return err
	}
	var ids []string
	if len(f.pos) > 0 {
		ids = f.pos
	} else {
		books, err := booksDir()
		if err != nil {
			return err
		}
		files, _ := filepath.Glob(filepath.Join(books, "contracts", "*", "contract.json"))
		for _, file := range files {
			id := filepath.Base(filepath.Dir(file))
			if c, _, err := loadContract(id); err == nil && c.ClosedAt == nil {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 && !f.has("json") {
			fmt.Println("No open contracts.")
			return nil
		}
	}
	type view struct {
		*contract
		Status map[string]status `json:"status"`
	}
	var views []view
	for _, id := range ids {
		c, dir, err := loadContract(id)
		if err != nil {
			return err
		}
		vw := view{contract: c, Status: map[string]status{}}
		for _, p := range c.Professionals {
			vw.Status[p.Name] = readStatus(dir, p.Name)
		}
		views = append(views, vw)
	}
	if f.has("json") {
		return printJSON(views)
	}
	for _, vw := range views {
		state := "open"
		if vw.ClosedAt != nil {
			state = "closed"
		}
		fmt.Printf("Contract %s (%s), on %q, placed %s\n", vw.ID, state, vw.Target, vw.PlacedAt.Local().Format("Mon 15:04"))
		for _, p := range vw.Professionals {
			s := vw.Status[p.Name]
			fmt.Printf("  %-26s %-8s %s\n", p.Name, s.State, describe(s))
		}
	}
	return nil
}

func describe(s status) string {
	var parts []string
	if s.Findings > 0 || s.terminal() {
		parts = append(parts, fmt.Sprintf("%d findings", s.Findings))
	}
	if s.Unreached > 0 {
		parts = append(parts, fmt.Sprintf("%d unreached", s.Unreached))
	}
	if s.Reason != "" {
		parts = append(parts, s.Reason)
	}
	return strings.Join(parts, "; ")
}

func cmdWait(args []string) (int, error) {
	f, err := parseFlags(args, map[string]bool{"timeout": true})
	if err != nil {
		return exitError, err
	}
	if len(f.pos) != 1 {
		return exitError, usageError{"wait ID"}
	}
	limit := 3 * time.Hour
	if f.has("timeout") {
		if limit, err = time.ParseDuration(f.get("timeout")); err != nil {
			return exitError, usageError{"--timeout: a duration like 30m"}
		}
	}
	c, dir, err := loadContract(f.pos[0])
	if err != nil {
		return exitError, err
	}
	deadline := time.Now().Add(limit)
	for {
		done := true
		for _, p := range c.Professionals {
			if !readStatus(dir, p.Name).terminal() {
				done = false
			}
		}
		if done {
			break
		}
		if time.Now().After(deadline) {
			_ = cmdStatus([]string{c.ID})
			return exitError, fmt.Errorf("still working after %s; wait again, or close %s", limit, c.ID)
		}
		time.Sleep(pollEvery())
	}
	code := exitOK
	fmt.Printf("Contract %s: everyone is back.\n", c.ID)
	for _, p := range c.Professionals {
		s := readStatus(dir, p.Name)
		fmt.Printf("  %-26s %-8s %s\n", p.Name, s.State, describe(s))
		switch s.State {
		case "failed":
			code = max(code, exitStart)
		case "refused":
			code = exitRefused
		}
	}
	fmt.Printf("Close it with: administration close %s\n", c.ID)
	return code, nil
}

func pollEvery() time.Duration {
	if d, err := time.ParseDuration(os.Getenv("ADMINISTRATION_POLL")); err == nil && d > 0 {
		return d
	}
	return 2 * time.Second
}

func cmdClose(args []string) error {
	f, err := parseFlags(args, map[string]bool{"keep": false})
	if err != nil {
		return err
	}
	if len(f.pos) != 1 {
		return usageError{"close ID"}
	}
	c, dir, err := loadContract(f.pos[0])
	if err != nil {
		return err
	}
	if c.ClosedAt != nil {
		fmt.Println(filepath.Join(dir, "summary.md"))
		return nil
	}
	// Anyone still working is asked to stop; their worker records "closed",
	// keeps what they wrote, and tears their room down. Only a worker that
	// still carries its tag is signalled.
	grace := closeGrace()
	for _, p := range c.Professionals {
		s := readStatus(dir, p.Name)
		if !s.terminal() && taggedAlive(s.WorkerPID, readTag(dir, p.Name)) {
			_ = syscall.Kill(s.WorkerPID, syscall.SIGTERM)
			if p.Kind == "user" {
				grace = max(grace, closeGrace()+teardownLimit+15*time.Second)
			}
		}
	}
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		busy := false
		for _, p := range c.Professionals {
			s := readStatus(dir, p.Name)
			if taggedAlive(s.WorkerPID, readTag(dir, p.Name)) {
				busy = true
			}
		}
		if !busy {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	for _, p := range c.Professionals {
		s := readStatus(dir, p.Name)
		tag := readTag(dir, p.Name)
		if !s.terminal() {
			s.State, s.Reason = "closed", "did not stop when asked; stopped"
		}
		// Whatever is left of this professional (a worker that hung, a claude
		// whose worker died, a browser) goes, by tag only.
		clearTagged(tag)
		if p.Kind == "user" && s.Room != "" && !s.TornDown {
			teardown(c, dir, p.Name, &s)
		}
		_ = writeJSON(filepath.Join(dir, p.Name, "status.json"), s)
	}
	c.Kept, c.NotKept = keepOneOffs(c, dir)
	summary, err := writeSummary(c, dir)
	if err != nil {
		return err
	}
	if !f.has("keep") {
		if rooms, err := roomsDir(); err == nil {
			_ = os.RemoveAll(filepath.Join(rooms, c.ID))
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	c.ClosedAt = &now
	if err := writeJSON(filepath.Join(dir, "contract.json"), c); err != nil {
		return err
	}
	fireHooks("closed", c.Project, contractPayload(c, dir))
	fmt.Println(summary)
	return nil
}

// keepOneOffs puts each one-off specialist who found something on the
// project's roster, so the next contract casts them instead of the Director
// writing them again. One already there (a rerun, or tuned since) stays as
// it is; letting one go is deleting its file.
func keepOneOffs(c *contract, dir string) (kept, notKept []string) {
	for _, p := range c.Professionals {
		if p.Source != "contract" || readStatus(dir, p.Name).Findings == 0 {
			continue
		}
		src := filepath.Join(dir, p.Name, "hire.md")
		if _, err := os.Stat(src); err != nil {
			continue
		}
		dst := filepath.Join(c.Project, ".administration", "professionals", p.Name+".md")
		if _, err := os.Stat(dst); err == nil {
			notKept = append(notKept, p.Name+": already on the roster")
			continue
		}
		hp, dst, err := hireFile(c.Project, src, false)
		if err != nil {
			notKept = append(notKept, fmt.Sprintf("%s: %v", p.Name, err))
			continue
		}
		kept = append(kept, p.Name)
		fireHooks("hired", c.Project, map[string]any{"name": hp.Name, "file": dst, "kind": hp.Kind, "knows": hp.Knows})
	}
	return kept, notKept
}

func closeGrace() time.Duration {
	if d, err := time.ParseDuration(os.Getenv("ADMINISTRATION_CLOSE_GRACE")); err == nil && d > 0 {
		return d
	}
	return 30 * time.Second
}

// withProfessionals reads --with: a comma list of professional files, or of
// Director run ids (their out/candidates). They work this contract; each
// that finds something joins the roster at close (keepOneOffs).
func withProfessionals(spec string) ([]*professional, error) {
	var out []*professional
	for _, item := range strings.Split(spec, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		var files []string
		if runRE.MatchString(item) {
			_, rd, err := loadRun(item)
			if err != nil {
				return nil, err
			}
			files, _ = filepath.Glob(filepath.Join(rd, "out", "candidates", "*.md"))
			if len(files) == 0 {
				return nil, usageError{fmt.Sprintf("Director run %s wrote no candidates", item)}
			}
		} else {
			files = []string{item}
		}
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				return nil, usageError{fmt.Sprintf("--with %s: %v", f, err)}
			}
			p, err := parseProfessional(b, f, "contract", false)
			if err != nil {
				return nil, usageError{fmt.Sprintf("--with %s: %v", f, err)}
			}
			out = append(out, p)
		}
	}
	return out, nil
}

// cmdReport prints what a contract produced, so whoever placed it can read
// it through the command alone: the books sit outside the project.
func cmdReport(args []string) error {
	if len(args) < 1 || len(args) > 2 {
		return usageError{"report ID [NAME]"}
	}
	c, dir, err := loadContract(args[0])
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "summary.md")
	if len(args) == 2 {
		found := false
		for _, p := range c.Professionals {
			found = found || p.Name == args[1]
		}
		if !found {
			return usageError{fmt.Sprintf("%s isn't on contract %s", args[1], c.ID)}
		}
		path = filepath.Join(dir, args[1], "report.md")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if len(args) == 1 {
			return fmt.Errorf("contract %s has no summary yet; it's written at close (administration close %s)", c.ID, c.ID)
		}
		return fmt.Errorf("%s has no report on contract %s yet", args[1], c.ID)
	}
	_, err = os.Stdout.Write(b)
	return err
}

var dataLine = regexp.MustCompile(`(?m)^data:\s*([a-z0-9][a-z0-9-]*)\s*$`)

// dataSet is the data the rooms start from: --data, or else the "data:"
// line of a Director cast plan placed with --with; either has to be one the
// project declares.
func dataSet(project, name, with string) (string, error) {
	if name == "" {
		for _, item := range strings.Split(with, ",") {
			if item = strings.TrimSpace(item); runRE.MatchString(item) {
				if _, rd, err := loadRun(item); err == nil {
					if b, err := os.ReadFile(filepath.Join(rd, "out", "cast.md")); err == nil {
						if m := dataLine.FindStringSubmatch(string(b)); m != nil {
							name = m[1]
						}
					}
				}
			}
		}
	}
	if name == "" {
		return "", nil
	}
	sets := loadProjectConfig(project).Data
	if _, ok := sets[name]; !ok {
		names := make([]string, 0, len(sets))
		for n := range sets {
			names = append(names, n)
		}
		sort.Strings(names)
		if len(names) == 0 {
			return "", usageError{fmt.Sprintf("--data %s: the project declares no data sets ([data.<name>] in .administration/roster.toml)", name)}
		}
		return "", usageError{fmt.Sprintf("--data %s: not one of the project's data sets (%s)", name, strings.Join(names, ", "))}
	}
	return name, nil
}

// handoutRules is the project's handout (its own rule documents, the ones
// roster.toml names), as a section of a user's persona.
func handoutRules(project string) string {
	files := loadProjectConfig(project).Handout.Files
	if len(files) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n## The project's own rules\n\nThese are the project's rules, whole: work by them. A finding that breaks one is a finding, whatever it looks like; name the rule. Where your persona above and these rules disagree (a screen size, a device, a name for something), the rules are the project's current word: follow them, and say in your report that they disagreed.\n")
	total := 0
	for _, f := range files {
		abs := filepath.Join(project, f)
		st, err := os.Lstat(abs)
		if err != nil || !st.Mode().IsRegular() || !within(abs, project) || secret(filepath.Base(f)) {
			continue
		}
		body, err := os.ReadFile(abs)
		if err != nil || total+len(body) > 400<<10 {
			continue
		}
		total += len(body)
		fmt.Fprintf(&b, "\n### %s\n\n%s\n", filepath.Base(f), strings.TrimSpace(string(body)))
	}
	return b.String()
}
