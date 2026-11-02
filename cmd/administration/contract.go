package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	"unicode/utf8"
)

// A contract is a directory in the books: contract.json, and one directory
// per professional holding their brief, settings, status, stream and report.
type contract struct {
	ID            string     `json:"id"`
	Target        string     `json:"target"`
	Project       string     `json:"project"`
	Key           string     `json:"key,omitempty"` // the project's name for the books (projectKey)
	// Its .administration when it was placed, outside the project (a
	// worktree's main checkout's), so its hooks are found after the
	// worktree is removed.
	House string `json:"house,omitempty"`
	PlacedAt      time.Time  `json:"placed_at"`
	PlacedBy      string     `json:"placed_by"`
	// Its place in the line for rooms: when it was placed, to the
	// nanosecond (pace.go).
	Seq int64 `json:"seq,omitempty"`
	Professionals []castItem `json:"professionals"`
	ClosedAt      *time.Time `json:"closed_at,omitempty"`
	Changes       *changes   `json:"changes,omitempty"`
	// PATH when the contract was placed, for the workers.
	Path      string     `json:"path,omitempty"`
	StartedAt *time.Time `json:"started_at,omitempty"`
	// Users' browsers open as windows on the person's screen, to watch.
	ShowBrowsers bool `json:"show_browsers,omitempty"`
	// The open rooms as they were at close, so a summary written again
	// later (verdicts, verify) lists those, not today's.
	OpenAtClose []openRoom `json:"open_at_close,omitempty"`
	// On changes, users also get the project as it was before them.
	Before bool `json:"before,omitempty"`
	// The project's data set the rooms start from ([data] in roster.toml);
	// empty is the setup hook's default.
	Data string `json:"data,omitempty"`
	// The one-off specialists close put on the roster, and why any it
	// couldn't keep wasn't kept.
	Kept    []string `json:"kept,omitempty"`
	NotKept []string `json:"not_kept,omitempty"`
	// A resume's "ID NAME": the professional and contract it picks up from.
	Resumes string `json:"resumes,omitempty"`
	// The build under test (provenance.go): HEAD when it was placed, a
	// hash of the uncommitted work on top when there was some, the commit
	// the person chose with --build, and the copy of the project as it was
	// placed that critics and verifiers read.
	Commit   string `json:"commit,omitempty"`
	Work     string `json:"work,omitempty"`
	Build    string `json:"build,omitempty"`
	Checkout string `json:"checkout,omitempty"`
	// The desk its rooms get, as it was placed (desk.go); its files are kept
	// in the contract's .desk.
	Desk *deskConfig `json:"desk,omitempty"`
	// The commit critics and verifiers read: --build's when this checkout
	// has it, else Commit (tested() says which).
	Tested string `json:"tested,omitempty"`
	// What place warned about the changes' base (a branch missing the
	// local default branch's commits), kept for whoever triages later.
	BaseWarnings []string `json:"base_warnings,omitempty"`
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
	// written by whoever knows what changed: one task each. With --only, it
	// is the whole of their work: the persona's own checks are off.
	Task     string `json:"task,omitempty"`
	TaskOnly bool   `json:"task_only,omitempty"`
	// What the persona is for (its description): its part of the contract
	// when it has no task of its own, so a specialist stays one.
	Focus string `json:"focus,omitempty"`
}

// Terminal states: back (finished on their own), out (shown out at the coin
// limit, report kept), closed (stopped by close), refused (the Rules turned
// the room away), failed (the room or the harness broke).
type status struct {
	State      string     `json:"state"` // queued, waiting, setup, working, back, out, closed, refused, failed
	// Waiting for a room: it holds none yet (pace.go).
	Queued bool `json:"queued,omitempty"`
	// How long it waited for the machine (a room, the setup turn, memory),
	// in seconds, and when its room was ready and it started work: its
	// coins start then, not when it was placed.
	WaitedSecs float64    `json:"waited_secs,omitempty"`
	ReadyAt    *time.Time `json:"ready_at,omitempty"`
	Reason     string     `json:"reason,omitempty"`
	// How many times it was sent back after it said it was done.
	SentBack int `json:"sent_back,omitempty"`
	// Its plan: how many steps it covered, of how many it wrote.
	PlanDone  int `json:"plan_done,omitempty"`
	PlanSteps int `json:"plan_steps,omitempty"`
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
	// When its equipment was lost mid-contract, and why: "back" then
	// means back with part of the work undone.
	EquipmentLost string `json:"equipment_lost,omitempty"`
	// Shared non-production servers its equipment's app talked to
	// ("phone: staging.example.com"): what it saw may be the server's.
	Shared []string `json:"shared,omitempty"`
	// The builds its room ran, as each said (provenance.go), and the build
	// hook's, when the house has one (build.go).
	Builds        []roomBuild `json:"builds,omitempty"`
	BuildArtifact string      `json:"build_artifact,omitempty"`
	BuildCommit   string      `json:"build_commit,omitempty"`
	// The build hook's build of the merge base, for the release check's
	// before app: one per commit, shared by every room that needs it.
	BeforeBuildArtifact string `json:"before_build_artifact,omitempty"`
	BeforeBuildCommit   string `json:"before_build_commit,omitempty"`
	// When its equipment was restarted after it stopped answering.
	EquipmentRestarted string `json:"equipment_restarted,omitempty"`
}

// partial says when someone came back with most of their plan undone: back,
// but not a full part, and nothing in the exit code says so.
func (s status) partial() string {
	if s.State != "back" || s.PlanSteps < 3 || 2*s.PlanDone >= s.PlanSteps {
		return ""
	}
	return fmt.Sprintf("only %d of %d plan steps done", s.PlanDone, s.PlanSteps)
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
		// It may have recorded its outcome and exited between the read
		// above and that check: read again before calling it lost.
		var again status
		if b, err := os.ReadFile(filepath.Join(dir, name, "status.json")); err == nil && json.Unmarshal(b, &again) == nil && again.terminal() {
			return again
		}
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

// newID is a free contract id, its folder made in the books' contracts/.
func newID(books string) (string, error) {
	for range 50 {
		id, err := randomID()
		if err != nil {
			return "", err
		}
		// Made now, so two places at once can't draw the same id: only one
		// Mkdir of it works.
		if err := os.Mkdir(filepath.Join(books, "contracts", id), 0o700); err == nil {
			return id, nil
		} else if !errors.Is(err, os.ErrExist) {
			return "", err
		}
	}
	return "", errors.New("could not find a free contract id")
}

// randomID is four random letters and digits.
func randomID() (string, error) {
	const digits = "abcdefghijklmnopqrstuvwxyz0123456789"
	id := make([]byte, 4)
	for i := range id {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(digits))))
		if err != nil {
			return "", err
		}
		id[i] = digits[n.Int64()]
	}
	return string(id), nil
}

// placedBy names who placed a contract: the person (ADMINISTRATION_PLACED_BY,
// else the git author the environment sets, as on a machine shared by an
// account several people work in), else the account, with the Claude
// session when one placed it. Inside a hook git runs (it sets
// GIT_INDEX_FILE there) the author is the commit's, who may be someone
// else, so it isn't taken.
func placedBy() string {
	who := strings.TrimSpace(os.Getenv("ADMINISTRATION_PLACED_BY"))
	if who == "" && os.Getenv("GIT_INDEX_FILE") == "" {
		who = strings.TrimSpace(os.Getenv("GIT_AUTHOR_NAME"))
	}
	if who == "" {
		who = "user"
		if u := os.Getenv("USER"); u != "" {
			who = "user:" + u
		}
	}
	who = oneLine(who, 60)
	if s := os.Getenv("CLAUDE_CODE_SESSION_ID"); s != "" {
		return who + " (claude:" + s[:min(8, len(s))] + ")"
	}
	return who
}

// holderNames is the contracts holding rooms, each with who placed it:
// "ab12, placed by Sam; cd34, placed by Alex".
func holderNames(ids []string) string {
	named := make([]string, len(ids))
	for i, id := range ids {
		named[i] = id
		if c, _, err := loadContract(id); err == nil && c.PlacedBy != "" {
			who, _, _ := strings.Cut(c.PlacedBy, " (claude:")
			named[i] = fmt.Sprintf("%s, placed by %s", id, who)
		}
	}
	return strings.Join(named, "; ")
}

func maxRooms() int {
	if n, err := strconv.Atoi(os.Getenv("ADMINISTRATION_MAX_ROOMS")); err == nil && n > 0 {
		return n
	}
	return 4
}

// sweepChecks clears what a room-check or equipment-check left when it was
// killed outright (its own cleanup never ran): its contract, and whatever
// its room still has running. Each check records its pid; a dead one's
// folder goes.
func sweepChecks() {
	books, err := booksDir()
	if err != nil {
		return
	}
	pids, _ := filepath.Glob(filepath.Join(books, "contracts", "*", "check.pid"))
	for _, pf := range pids {
		b, _ := os.ReadFile(pf)
		alive, empty := checkAlive(b)
		if alive {
			continue
		}
		// An empty pid file may be a check still writing it: give it a minute.
		if st, err := os.Stat(pf); empty && err == nil && time.Since(st.ModTime()) < time.Minute {
			continue
		}
		// Claimed first, so two sweeps at once don't both tear it down. The
		// claim is a lock, gone with a sweep that dies part way, so the next
		// one finds check.pid still there and finishes the job.
		dir := filepath.Dir(pf)
		unlock, ok := tryLockFile(filepath.Join(dir, ".sweep.lock"))
		if !ok {
			continue
		}
		if _, err := os.Stat(pf); err != nil {
			unlock() // another sweep finished it in between
			continue
		}
		if c, _, err := loadContract(filepath.Base(dir)); err == nil {
			for _, p := range c.Professionals {
				s := readStatus(dir, p.Name)
				teardown(c, dir, p.Name, &s)
				if s.Room != "" {
					_ = os.RemoveAll(filepath.Dir(s.Room))
				}
			}
		}
		_ = os.RemoveAll(dir)
		unlock()
	}
}

func cmdPlace(args []string) error {
	return placeContract(args, "", nil)
}

// placeContract is place. resumes is resume's "ID NAME", never a flag a
// person could set by hand; placed hears the new contract's id as soon as
// it's in the books, so a caller can tell a contract that failed at once
// from one never placed, and let go of a lock it needed only until then.
func placeContract(args []string, resumes string, placed func(id string)) error {
	f, err := parseFlags(args, map[string]bool{"cast": true, "with": true, "coins": true, "model": true, "project": true, "dry-run": false,
		"changes": false, "base": true, "show-browsers": false, "no-watch": false, "no-before": false, "data": true, "brief": true, "only": true, "persona-from": true, "build": true, "no-queue": false})
	if err != nil {
		return err
	}
	target := strings.TrimSpace(strings.Join(f.pos, " "))
	if target == "" {
		return usageError{"place needs a target: what the contract is on"}
	}
	// From before it takes a copy of the project until its contract.json
	// names it and its workers are started, so prune can't remove the copy
	// in between, and no longer: prune isn't refused while place waits for
	// quick failures, opens the watch panes or runs the placed hooks.
	unuse, err := useBooks()
	if err != nil {
		return err
	}
	defer unuse()
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
	// A house kept on this machine is found by the project's origin remote,
	// which the repo's own config names: said, so a checkout that only
	// claims to be the project is seen running the project's setup.
	if h := houseOf(project); h.Where == "machine" && !f.has("dry-run") {
		fmt.Fprintf(os.Stderr, "administration: the house kept on this machine for %s (by the project's origin remote): %s\n", h.Origin, h.Dir)
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
	// deskUsers is the users with the desk's file tools (hasFileTools): the
	// web browser's, and those whose equipment shows a window through it.
	webUsers, deskUsers, needBrowser := 0, 0, false
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
		desk := p.Browser
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
			desk = desk || eq.Server == "playwright"
		}
		if desk {
			deskUsers++
		}
	}
	if needBrowser {
		if sandboxBlocked() {
			fmt.Fprintln(os.Stderr, "administration: the browser users will likely fail: "+sandboxHint)
		}
		if _, err := exec.LookPath("node"); err != nil {
			return usageError{"browser professionals need Node for the browser, and node isn't on PATH"}
		}
		if _, err := os.Stat(playwrightCLI()); err != nil {
			return usageError{fmt.Sprintf("the browser isn't installed (%s); run install.sh", playwrightCLI())}
		}
	}
	if bad := badPorts(project); len(bad) > 0 && users > 0 {
		portWarning(bad)
	}
	if webUsers > 0 {
		if _, _, err := houseSetup(project, false); err != nil {
			return usageError{"browser professionals need a room, and " + err.Error()}
		}
	}
	// The desk is checked now, not when each user starts after its room is
	// built; so is the handout, which every user gets.
	if deskUsers > 0 {
		if _, err := fillDesk(project, ""); err != nil {
			return usageError{err.Error()}
		}
	}
	if users > 0 {
		if _, err := handoutFiles(project); err != nil {
			return usageError{err.Error()}
		}
	}
	// The build under test, and the copy of the project as it is now that
	// critics and verifiers read: made before the books' lock, which it
	// would hold up for as long as a big project takes to copy. Shared by
	// every contract on the same commit and work. Made now when a critic
	// needs it, or when there's uncommitted work (which can't be had
	// later); otherwise the commit is enough, and verify makes it if it
	// runs.
	held := &contract{Project: project, Key: projectKey(project), House: outsideHouse(project)}
	if err := recordCommit(held, strings.TrimSpace(f.get("build"))); err != nil {
		return err
	}
	critics, rooms := false, false
	for _, p := range cast {
		critics = critics || p.Kind == "critic"
		rooms = rooms || p.Kind == "user" || len(p.Uses) > 0
	}
	if held.Build != "" && held.Tested == "" && rooms && buildHook(held) != "" {
		// The hook builds from this checkout, so every room would fail.
		return usageError{fmt.Sprintf("--build %s: this checkout doesn't have that commit, and the house's build hook builds from this checkout; fetch it first (git fetch), or place it in a checkout that has it", held.Build)}
	}
	if v := os.Getenv("ADMINISTRATION_DEVICE_PORTS"); v != "" && rooms {
		if bad := devicePortsBad(v); bad != "" {
			return usageError{bad}
		}
	}
	// What can refuse the contract cheaply is asked before the copy, which
	// can take a while on a big project: a placement refused after it would
	// pay for it each time it's tried again. The flags first, then the rooms.
	c := &contract{Target: target, Project: project, Key: projectKey(project), House: outsideHouse(project), PlacedAt: time.Now().UTC().Truncate(time.Second),
		PlacedBy: placedBy(), Path: os.Getenv("PATH"), ShowBrowsers: f.has("show-browsers"), Resumes: resumes}
	for _, p := range cast {
		ci := castItem{Name: p.Name, Kind: p.Kind, Source: p.Source, CoinsMin: p.CoinsMin, SignsInAs: p.SignsInAs, Sees: p.Sees,
			Model: p.Model, Uses: p.Uses, NoBrowser: p.Kind == "user" && !p.Browser,
			Dossier: len(readDossier(project, p.Name)), Focus: oneLine(p.Description, 300)}
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
	type taskFlag struct {
		kv   string
		only bool
	}
	var tasks []taskFlag
	for _, kv := range f.all["brief"] {
		tasks = append(tasks, taskFlag{kv, false})
	}
	for _, kv := range f.all["only"] {
		tasks = append(tasks, taskFlag{kv, true})
	}
	for _, t := range tasks {
		kv, only := t.kv, t.only
		flag := map[bool]string{false: "--brief", true: "--only"}[only]
		name, task, ok := strings.Cut(kv, "=")
		name, task = strings.TrimSpace(name), strings.TrimSpace(task)
		if !ok || name == "" || task == "" {
			return usageError{fmt.Sprintf("%s %q: want NAME=\"their own task\"", flag, kv)}
		}
		found := false
		for i := range c.Professionals {
			if c.Professionals[i].Name == name {
				if c.Professionals[i].Task != "" {
					return usageError{fmt.Sprintf("%s %s: one task each (--brief or --only, once)", flag, name)}
				}
				c.Professionals[i].Task, c.Professionals[i].TaskOnly, found = task, only, true
			}
		}
		if !found {
			return usageError{fmt.Sprintf("%s %s: not in this contract's cast", flag, name)}
		}
	}
	if f.has("base") && !f.has("changes") {
		return usageError{"--base goes with --changes"}
	}
	if buildHookUnrunnable(held) != "" && rooms {
		return usageError{buildHookUnrunnable(held)}
	}
	// More users than there are rooms never fits: split it. Fewer, but the
	// rooms are taken (or others are in line for them): it's queued, and
	// its users take rooms by themselves, in turn, as they free (pace.go).
	// --no-queue refuses it instead, as busy, for a script that would
	// rather place it again later. A dry run places nothing, so it waits
	// for nobody.
	queued := ""
	roomsTaken := func() error {
		queued = ""
		if users > maxRooms() {
			return usageError{fmt.Sprintf("this contract needs %d rooms and there are %d (ADMINISTRATION_MAX_ROOMS): place its users in two contracts, or raise the limit.", users, maxRooms())}
		}
		held, waiting, ids := roomUse()
		if held+waiting+users <= maxRooms() || f.has("dry-run") {
			return nil
		}
		inUse := fmt.Sprintf("%d of %d rooms are in use", held, maxRooms())
		if waiting > 0 {
			inUse += fmt.Sprintf(" and %s for one", plural(waiting, "user is waiting", "users are waiting"))
		}
		if f.has("no-queue") {
			wait := "once its users are back (administration wait " + ids[0] + ")"
			if len(ids) > 1 {
				wait = "once enough of their users are back (administration wait on each in turn: " + strings.Join(ids, ", ") + ")"
			}
			return busy{fmt.Sprintf("%s (contract %s) and this contract needs %d: place it again %s, or with fewer users.",
				inUse, holderNames(ids), users, wait)}
		}
		queued = fmt.Sprintf("Queued, %d ahead: %s (contract %s). Its users take rooms by themselves, in turn, as they free; nothing needs placing again.",
			len(ids), inUse, holderNames(ids))
		return nil
	}
	if users > 0 {
		if err := roomsTaken(); err != nil {
			return err
		}
	}
	if held.Build != "" && held.Tested == "" {
		// Not refused (it can be someone else's), but a typo looks the same.
		fmt.Fprintf(os.Stderr, "administration: --build %s: this checkout doesn't have that commit, so every room is held to it and critics and verifiers read HEAD (%s) instead; if it's a typo, close the contract and place it again\n", held.Build, short(held.Commit))
	}
	if sha := held.tested(); sha != "" && (critics || held.Work != "") && !f.has("dry-run") {
		root, err := copyRoot(held, sha, held.Work)
		if err != nil {
			return err
		}
		if held.Checkout, err = makeCheckout(project, sha, held.Work, root); err != nil {
			return err
		}
		// Taken now. Prune can't remove it while this holds the books' use
		// lock, and an open contract's copy is kept after.
		now := time.Now()
		_ = os.Chtimes(root, now, now)
	}

	// The books' lock is held until the workers are started, so two
	// contracts placed at once can't both slip under the room limit, and no
	// longer: the watch panes and the hooks after it can take a while.
	release := func() {}
	defer func() { release() }()
	if users > 0 {
		unlock, err := lockBooks()
		if err != nil {
			return err
		}
		release = func() { unlock(); release = func() {} }
		// Asked again under the lock: two placed at once can't both fit.
		if err := roomsTaken(); err != nil {
			return err
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
	c.ID = id
	// Under the books' lock, with the rooms asked about: the line for rooms
	// goes by this.
	c.Seq = time.Now().UnixNano()
	// newID made the contract's folder: it goes again if the contract
	// isn't written.
	written := false
	defer func() {
		if !written {
			os.RemoveAll(filepath.Join(books, "contracts", id))
		}
	}()
	c.Commit, c.Work, c.Build, c.Tested, c.Checkout = held.Commit, held.Work, held.Build, held.Tested, held.Checkout
	dir := filepath.Join(books, "contracts", id)
	if f.has("changes") {
		// A --build contract's changes are the commit's alone, as its copy
		// is; otherwise HEAD and the working tree, which must still be the
		// ones the copy was made of.
		to := ""
		if c.Build != "" {
			to = c.tested()
		}
		if c.Changes, err = collectChanges(project, f.get("base"), to, filepath.Join(dir, contractHandout)); err != nil {
			os.RemoveAll(dir)
			return err
		}
		if to == "" && c.Commit != "" {
			now := &contract{Project: project}
			if err := recordCommit(now, ""); err == nil && (now.Commit != c.Commit || now.Work != c.Work) {
				os.RemoveAll(dir)
				return fmt.Errorf("the project changed while the contract was being placed (HEAD %s, work %s; now %s, %s), so its changes and the copy critics read wouldn't agree: place it again", short(c.Commit), orNone(c.Work), short(now.Commit), orNone(now.Work))
			}
		}
		if n := c.Changes.LeftOut; n > 0 {
			fmt.Fprintf(os.Stderr, "administration: --changes: %d changed file(s) that look like secrets left out of the critics' diff\n", n)
		}
		c.BaseWarnings = baseWarnings(project, c.Changes.Base)
		for _, w := range c.BaseWarnings {
			fmt.Fprintln(os.Stderr, "administration: "+w)
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
		// resume's: the persona as it was placed on the contract it picks up
		// from, not as the file says today (unless prune has stripped it,
		// and resume says so).
		if from := f.get("persona-from"); from != "" {
			if b, err := os.ReadFile(filepath.Join(from, p.Name, "persona.md")); err == nil {
				persona = string(b)
			}
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
	// The desk, kept as it is now: a resume takes the one its contract had.
	// An empty one is kept too, so a desk the roster gets later doesn't
	// reach this contract's rooms (a contract with none recorded is one
	// placed before contracts kept it, and gets the project's).
	if deskUsers > 0 {
		cfg, kept := loadProjectConfig(project).Desk, ""
		if from := f.get("persona-from"); from != "" {
			var old contract
			if b, err := os.ReadFile(filepath.Join(from, "contract.json")); err == nil && json.Unmarshal(b, &old) == nil && old.Desk != nil {
				switch {
				case old.Desk.empty():
					cfg = *old.Desk
				case isDir(deskKept(from)):
					cfg, kept = *old.Desk, deskKept(from)
				default:
					fmt.Fprintf(os.Stderr, "administration: %s's own copy of its desk was pruned, so this takes the desk roster.toml has now\n", filepath.Base(from))
				}
			}
		}
		if !cfg.empty() {
			if err := keepDesk(project, cfg, kept, deskKept(dir)); err != nil {
				os.RemoveAll(dir)
				return usageError{err.Error()}
			}
		}
		c.Desk = &cfg
	}
	if err := writeJSON(filepath.Join(dir, "contract.json"), c); err != nil {
		return err
	}
	written = true
	if placed != nil {
		placed(id)
	}
	startErr := startWorkers(c, dir)
	release()
	unuse()

	names := make([]string, len(c.Professionals))
	for i, p := range c.Professionals {
		names[i] = p.Name
	}
	who := "professional"
	if len(names) != 1 {
		who = "professionals"
	}
	fmt.Printf("Contract %s placed on %q. %d %s took it: %s.\n", id, target, len(names), who, strings.Join(names, ", "))
	if len(c.Professionals) > 1 {
		for _, p := range c.Professionals {
			if p.Task == "" && p.Source != "contract" {
				fmt.Printf("  %s has no task of its own, so its part is its persona's: %s. Give it one with --brief %s=\"...\".\n", p.Name, strings.TrimRight(p.Focus, ". "), p.Name)
			}
		}
	}
	// Who failed at once is known before anyone is told to wait or the
	// panes open: neither, for a contract nobody is working on any more.
	working := startErr == nil
	if working {
		startErr, working = quickFailures(c, dir)
	}
	if queued != "" {
		fmt.Println(queued)
	}
	if working {
		fmt.Printf("Check in with: administration wait %s\n", id)
		watchOnPlace(id, project, dir, names, f.has("no-watch"))
	}
	fireHooks("placed", project, contractPayload(c, dir))
	return startErr
}

// quickFailures waits a moment for the rooms' setup to get going and says
// who failed at once (a setup hook that exits straight away, equipment
// that isn't there), so the person hears it from place rather than from a
// wait that ends early. Anyone still setting up after that is wait's. It
// records those it named as told, as wait does, so the wait it points to
// isn't cut short by them again; and its exit is wait's: 3 if any was
// refused, else 2. working is whether anyone is still at it.
func quickFailures(c *contract, dir string) (err error, working bool) {
	setup := false
	for _, p := range c.Professionals {
		setup = setup || p.Kind == "user" || len(p.Uses) > 0
	}
	if !setup {
		return nil, true
	}
	var failed, names []string
	refused := false
	for end := time.Now().Add(2 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		failed, names, refused, working = nil, nil, false, false
		pending := false
		for _, p := range c.Professionals {
			s := readStatus(dir, p.Name)
			switch s.State {
			case "failed", "refused":
				failed = append(failed, p.Name+": "+s.Reason)
				names = append(names, p.Name)
				refused = refused || s.State == "refused"
			case "queued", "waiting", "setup":
				pending, working = true, true
			default:
				working = working || !s.terminal()
			}
		}
		if !pending || time.Now().After(end) {
			break
		}
	}
	if len(failed) == 0 {
		return nil, working
	}
	// Added to whatever a wait already wrote there.
	told := filepath.Join(dir, ".told")
	if b, err := os.ReadFile(told); err == nil {
		names = append(strings.Fields(string(b)), names...)
	}
	_ = os.WriteFile(told, []byte(strings.Join(names, "\n")+"\n"), 0o600)
	next := fmt.Sprintf("nobody else is still working on it; see why with administration status %s, then administration close %s", c.ID, c.ID)
	if working {
		next = "the others are still working: administration wait " + c.ID
	}
	msg := fmt.Sprintf("contract %s is placed, but %d of %d failed at once (%s); %s", c.ID, len(failed), len(c.Professionals), strings.Join(failed, "; "), next)
	if refused {
		return refusal{msg}, working
	}
	return startError{msg}, working
}

// watchOnPlace opens the watch panes, without taking focus, when there's a
// watch hook or the person is in Herdr or tmux; anywhere else it says how to
// watch. Nothing pops up either way, and the browsers stay hidden unless
// --show-browsers. ADMINISTRATION_WATCH=off (or --no-watch) keeps it to the
// one line.
func watchOnPlace(id, project, dir string, names []string, off bool) {
	off = off || os.Getenv("ADMINISTRATION_WATCH") == "off"
	if off || !(os.Getenv("HERDR_ENV") == "1" || os.Getenv("TMUX") != "" || watchHook(project) != "") {
		fmt.Printf("Watch them work with: administration watch %s\n", id)
		return
	}
	if err := openPanes(id, project, dir, names); err != nil {
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
		sweepChecks()
		files, _ := filepath.Glob(filepath.Join(books, "contracts", "*", "contract.json"))
		for _, file := range files {
			id := filepath.Base(filepath.Dir(file))
			if c, _, err := loadContract(id); err == nil && c.ClosedAt == nil {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 && !f.has("json") {
			fmt.Printf("No open contracts. %s\n", roomsLine())
			return nil
		}
	}
	type view struct {
		*contract
		Status map[string]status `json:"status"`
	}
	views := []view{}
	for _, id := range ids {
		c, dir, err := loadContract(id)
		if err != nil {
			// Found by the scan and gone since (a check ending): not the
			// person's to hear about.
			if len(f.pos) == 0 {
				continue
			}
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
	if len(views) == 0 && len(f.pos) == 0 {
		fmt.Printf("No open contracts. %s\n", roomsLine())
		return nil
	}
	for _, vw := range views {
		state := "open"
		if vw.ClosedAt != nil {
			state = "closed"
		}
		by := ""
		if vw.PlacedBy != "" {
			by = " by " + vw.PlacedBy
		}
		fmt.Printf("Contract %s (%s), on %q, placed %s%s\n", vw.ID, state, vw.Target, vw.PlacedAt.Local().Format("Mon 15:04"), by)
		if t := vw.testedLine(); t != "" {
			fmt.Println("  " + t)
		}
		// A house whose every room builds the app for itself, and heavily:
		// said while its contract is open, with what was measured.
		if vw.ClosedAt == nil {
			roomed := false
			for _, p := range vw.Professionals {
				roomed = roomed || p.Kind == "user" && !p.NoBrowser
			}
			if heavy := heavySetup(vw.contract); roomed && heavy != "" {
				fmt.Println("  Worth a build hook: " + heavy + ".")
			}
		}
		for _, p := range vw.Professionals {
			s := vw.Status[p.Name]
			fmt.Printf("  %-26s %-8s %s\n", p.Name, s.State, describe(s))
			for _, b := range s.Builds {
				fmt.Printf("  %-26s %-8s build %s\n", "", "", b)
			}
		}
	}
	if len(f.pos) == 0 {
		fmt.Println(roomsLine())
	}
	return nil
}

// roomsLine says how many browser rooms are free for another contract, and
// how many users are in line for one.
func roomsLine() string {
	live, waiting, _ := roomUse()
	limit := maxRooms()
	line := fmt.Sprintf("Rooms: %d of %d in use, %d free.", live, limit, max(0, limit-live))
	if waiting > 0 {
		line += fmt.Sprintf(" %s for one.", plural(waiting, "user is waiting", "users are waiting"))
	}
	return line
}

func describe(s status) string {
	var parts []string
	if s.Findings > 0 || s.terminal() {
		parts = append(parts, fmt.Sprintf("%d findings", s.Findings))
	}
	if s.Unreached > 0 {
		parts = append(parts, fmt.Sprintf("%d unreached", s.Unreached))
	}
	if s.SentBack > 0 {
		parts = append(parts, "sent back "+plural(s.SentBack, "time", "times"))
	}
	if s.Reason != "" {
		parts = append(parts, s.Reason)
	}
	if w := waitedFor(s); w != "" && s.State != "waiting" {
		parts = append(parts, "waited "+w+" for the machine")
	}
	if s.EquipmentRestarted != "" {
		parts = append(parts, "equipment restarted at "+s.EquipmentRestarted)
	}
	if s.EquipmentLost != "" {
		parts = append(parts, "equipment lost at "+s.EquipmentLost+"; the rest of their part wasn't done")
	}
	if p := s.partial(); p != "" {
		parts = append(parts, p)
	}
	return strings.Join(parts, "; ")
}

// waitedFor is how long a seat waited for the machine (a room, the setup
// turn, memory), to the second, or "" for none worth saying.
func waitedFor(s status) string {
	if s.WaitedSecs < 1 {
		return ""
	}
	return (time.Duration(s.WaitedSecs * float64(time.Second))).Round(time.Second).String()
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
	// A line each time someone's state changes, so whoever reads this later
	// sees how it went; and a failure while others work ends the wait at
	// once, so a room that broke in a second isn't heard of an hour later.
	seen := map[string]string{}
	first := true
	// Who an earlier wait already returned early for, so waiting again
	// isn't cut short by the same failure.
	toldPath := filepath.Join(dir, ".told")
	told := map[string]bool{}
	if b, err := os.ReadFile(toldPath); err == nil {
		for _, n := range strings.Fields(string(b)) {
			told[n] = true
		}
	}
	for {
		done := true
		var broke []string
		for _, p := range c.Professionals {
			s := readStatus(dir, p.Name)
			if !s.terminal() {
				done = false
			}
			if s.State != seen[p.Name] {
				if !first || s.terminal() {
					fmt.Printf("%s  %-26s %-8s %s\n", time.Now().Format("15:04"), p.Name, s.State, describe(s))
				}
				if (s.State == "failed" || s.State == "refused") && !told[p.Name] {
					broke = append(broke, p.Name)
				}
				seen[p.Name] = s.State
			}
		}
		first = false
		if done {
			break
		}
		if len(broke) > 0 {
			code := exitStart
			for _, n := range broke {
				if readStatus(dir, n).State == "refused" {
					code = exitRefused
				}
			}
			for _, n := range broke {
				told[n] = true
			}
			var names []string
			for n := range told {
				names = append(names, n)
			}
			_ = os.WriteFile(toldPath, []byte(strings.Join(names, "\n")+"\n"), 0o600)
			fmt.Printf("Contract %s: %s didn't make it; the others are still working. Wait for them with: administration wait %s\n", c.ID, strings.Join(broke, ", "), c.ID)
			return code, nil
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
	// Until its links are matched: prune would take a contract closed
	// from under the matcher.
	unuse, err := useBooks()
	if err != nil {
		return err
	}
	defer unuse()
	_, dir, err := loadContract(f.pos[0])
	if err != nil {
		return err
	}
	// One close at a time: a second waits for the first, then finds the
	// contract closed and prints its summary, rather than stopping,
	// tearing down and summing it up all over again.
	unlock, err := lockFile(filepath.Join(dir, "close.lock"))
	if err != nil {
		return err
	}
	defer unlock()
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
	if v, err := loadRoster(c.Project); err == nil {
		c.OpenAtClose = v.Open
	}
	summary, err := writeSummary(c, dir)
	if err != nil {
		return err
	}
	if !f.has("keep") {
		if rooms, err := roomsDir(); err == nil {
			_ = os.RemoveAll(filepath.Join(rooms, c.ID))
		}
		// Each room where it really was: a contract placed before the
		// rooms moved (one folder per account) has them in the old one.
		for _, p := range c.Professionals {
			// A room is <rooms>/<contract>/rN.
			if s := readStatus(dir, p.Name); s.Room != "" && filepath.Base(filepath.Dir(s.Room)) == c.ID {
				_ = os.RemoveAll(filepath.Dir(s.Room))
			}
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	c.ClosedAt = &now
	if err := writeJSON(filepath.Join(dir, "contract.json"), c); err != nil {
		return err
	}
	fireHooks("closed", c.Project, contractPayload(c, dir))
	// Closed first, so nothing waits on the matcher; its links, if any, go
	// into the summary after.
	if matchFindings(c, dir) {
		_, _ = writeSummary(c, dir)
	}
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
		// A worktree removed before close: writing its roster would make a
		// stray folder where it was. hire --from keeps them later.
		if !isDir(c.Project) {
			notKept = append(notKept, fmt.Sprintf("%s: the project, %s, is gone; keep them with administration hire --from %s %s", p.Name, c.Project, c.ID, p.Name))
			continue
		}
		dst := filepath.Join(adminDir(c.Project), "professionals", p.Name+".md")
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
			return "", usageError{fmt.Sprintf("--data %s: the project declares no data sets ([data.<name>] in %s)", name, houseFile(project, "roster.toml"))}
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
	var pictures, left []string
	for _, f := range files {
		// Held to the handout's rule, file by file: one that fails it is
		// left out here, and place has said why.
		abs, st, err := rosterFile(project, f)
		if err != nil {
			continue
		}
		// A picture is the desk's (place holds it to its own budget), not
		// text to paste in; nor is a file that isn't text, or one that
		// would take the rules past what a persona carries.
		if handoutImage[strings.ToLower(filepath.Ext(f))] {
			pictures = append(pictures, filepath.Base(f))
			continue
		}
		if total+int(st.Size()) > handoutLimit {
			left = append(left, filepath.Base(f))
			continue
		}
		in, _, err := openRegular(abs, st)
		if err != nil {
			continue
		}
		body, err := io.ReadAll(io.LimitReader(in, handoutLimit+1))
		in.Close()
		if err != nil || total+len(body) > handoutLimit || !utf8.Valid(body) || bytes.IndexByte(body, 0) >= 0 {
			left = append(left, filepath.Base(f))
			continue
		}
		total += len(body)
		fmt.Fprintf(&b, "\n### %s\n\n%s\n", filepath.Base(f), strings.TrimSpace(string(body)))
	}
	if len(pictures) > 0 {
		fmt.Fprintf(&b, "\nThe rules' pictures are in ./handout/, to look at: %s.\n", strings.Join(pictures, ", "))
	}
	if len(left) > 0 {
		fmt.Fprintf(&b, "\nThese are in ./handout/ too, but not here, as they aren't text or there's too much: %s.\n", strings.Join(left, ", "))
	}
	return b.String()
}
