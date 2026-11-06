package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Pacing (docs/design.md, Pacing): the Administration paces its rooms to the
// machine, so nobody has to pick a room count, split a contract or place one
// again. Three things do it:
//
//   - a queue for rooms: a contract placed while the rooms are taken waits,
//     and each of its users takes a room by itself, in the order the
//     contracts were placed (ADMINISTRATION_MAX_ROOMS stays a hard cap);
//   - a line for setups (kept in the rooms folder), the after and before
//     sides and the build hook's builds alike: they go in turn, as many at
//     once as the memory covers (up to ADMINISTRATION_MAX_SETUPS), and a
//     house never measured the way it sets up now goes alone once, to be;
//   - memory: each setup is measured (how far the machine's available
//     memory falls while it runs), per side, and kept with the house's
//     records; a room sets up only when what's free, less a reserve for the
//     machine, covers what the house's rooms took before.
//
// A seat's coins start when its room answers, so none of this waiting costs
// it any of its purse, and the setup's own time limit starts only when its
// turn comes.

// memory is the machine's memory, in bytes.
type memory struct {
	Total, Available int64
}

// readMemory is the machine's memory now: Linux's MemAvailable, or macOS's
// free, inactive and speculative pages from vm_stat. Both count what
// containers use, which a process tree wouldn't. ok is false where it can't
// be read; nothing is then admitted or measured by memory.
// ADMINISTRATION_MEMINFO names a file in /proc/meminfo's form to read
// instead, on any system, so the tests can say what's free.
func readMemory() (memory, bool) {
	if p := os.Getenv("ADMINISTRATION_MEMINFO"); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			return memory{}, false
		}
		return parseMeminfo(string(b))
	}
	switch runtime.GOOS {
	case "linux":
		b, err := os.ReadFile("/proc/meminfo")
		if err != nil {
			return memory{}, false
		}
		return parseMeminfo(string(b))
	case "darwin":
		t, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
		if err != nil {
			return memory{}, false
		}
		total, err := strconv.ParseInt(strings.TrimSpace(string(t)), 10, 64)
		if err != nil {
			return memory{}, false
		}
		vm, err := exec.Command("vm_stat").Output()
		if err != nil {
			return memory{}, false
		}
		return parseVMStat(string(vm), total)
	}
	return memory{}, false
}

// parseMeminfo reads MemTotal and MemAvailable from /proc/meminfo's text.
func parseMeminfo(s string) (memory, bool) {
	var m memory
	total, avail := false, false
	for _, l := range strings.Split(s, "\n") {
		k, v, ok := strings.Cut(l, ":")
		f := strings.Fields(v)
		if !ok || len(f) == 0 {
			continue
		}
		n, err := strconv.ParseInt(f[0], 10, 64)
		if err != nil {
			continue
		}
		if len(f) > 1 && strings.EqualFold(f[1], "kB") {
			n *= 1024
		}
		switch strings.TrimSpace(k) {
		case "MemTotal":
			m.Total, total = n, true
		case "MemAvailable":
			m.Available, avail = n, true
		}
	}
	return m, total && avail && m.Total > 0
}

// parseVMStat reads what's available from vm_stat's text: the free,
// inactive and speculative pages, which macOS hands out before it
// compresses or swaps.
func parseVMStat(s string, total int64) (memory, bool) {
	page := int64(4096)
	if i := strings.Index(s, "page size of "); i >= 0 {
		if f := strings.Fields(s[i+len("page size of "):]); len(f) > 0 {
			if n, err := strconv.ParseInt(f[0], 10, 64); err == nil && n > 0 {
				page = n
			}
		}
	}
	var pages int64
	found := 0
	for _, l := range strings.Split(s, "\n") {
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "Pages free", "Pages inactive", "Pages speculative":
			if n, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(v), "."), 10, 64); err == nil {
				pages += n
				found++
			}
		}
	}
	return memory{Total: total, Available: pages * page}, found > 0 && total > 0
}

// memReserve is the memory kept for the machine itself, which no room is
// admitted into: 15% of it, and at least 2 GB.
func memReserve(total int64) int64 {
	return max(total*15/100, 2<<30)
}

// gb is bytes as gigabytes, to a tenth.
func gb(n int64) string {
	return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
}

// sideCost is what one room's setup of one side took, measured while it was
// the only setup on the machine.
type sideCost struct {
	// The most the machine's available memory fell while it set up, and how
	// far down it still was once the room answered.
	PeakMB   int64     `json:"peak_mb"`
	SteadyMB int64     `json:"steady_mb"`
	Secs     int       `json:"setup_secs"`
	At       time.Time `json:"at"`
	// How the room was set up: the house's setup hook and its build hook
	// (sideRecipe). A measure made another way isn't this setup's: a build
	// hook added since makes a setup far lighter.
	Recipe string `json:"recipe,omitempty"`
}

// sideRecipe names how a side is set up now: the setup hook and, when the
// house has one, the build hook and its env/ (buildRecipe); a build ("build")
// is the build hook's recipe alone.
func sideRecipe(c *contract, side string) string {
	hook := buildHook(c)
	if side == "build" {
		return buildRecipe(c, hook)
	}
	h := sha256.New()
	if b, err := os.ReadFile(filepath.Join(contractHouse(c), "setup")); err == nil {
		h.Write(b)
	}
	if hook != "" {
		fmt.Fprintf(h, "\x00build\x00%s", buildRecipe(c, hook))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// roomCosts is a house's measured setups, the last few of each side
// ("after", and "before" for a release check's other app), newest last.
type roomCosts struct {
	Sides map[string][]sideCost `json:"sides"`
}

// How many measures of a side are kept; the house's cost is the largest of
// them, so one setup measured while something else freed memory doesn't
// make it look cheap.
const keptCosts = 3

// costsPath is where a house's room costs are kept: beside its ledger in the
// books.
func costsPath(c *contract) (string, error) {
	books, err := booksDir()
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(projectLedger(books, contractKey(c)), ".jsonl") + ".rooms.json", nil
}

func loadCosts(c *contract) roomCosts {
	rc := roomCosts{Sides: map[string][]sideCost{}}
	p, err := costsPath(c)
	if err != nil {
		return rc
	}
	if b, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(b, &rc)
	}
	if rc.Sides == nil {
		rc.Sides = map[string][]sideCost{}
	}
	return rc
}

// cost is what a side's setup takes, as measured: the largest of the kept
// measures. ok is false for a side never measured.
func (rc roomCosts) cost(side string) (sideCost, bool) {
	return rc.costOf(side, "")
}

// costOf is cost, counting only the measures made by recipe ("" counts
// every one). ok is false when there's none.
func (rc roomCosts) costOf(side, recipe string) (sideCost, bool) {
	var out sideCost
	n := 0
	for _, sc := range rc.Sides[side] {
		if recipe != "" && sc.Recipe != recipe {
			continue
		}
		n++
		out.PeakMB = max(out.PeakMB, sc.PeakMB)
		out.SteadyMB = max(out.SteadyMB, sc.SteadyMB)
		out.Secs = max(out.Secs, sc.Secs)
		if sc.At.After(out.At) {
			out.At = sc.At
		}
	}
	return out, n > 0
}

// recordCost adds a measured setup to the house's costs.
func recordCost(c *contract, side string, sc sideCost) {
	p, err := costsPath(c)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	unlock, err := lockFile(p + ".lock")
	if err != nil {
		return
	}
	defer unlock()
	rc := loadCosts(c)
	kept := append(rc.Sides[side], sc)
	rc.Sides[side] = kept[max(0, len(kept)-keptCosts):]
	_ = writeJSON(p, rc)
}

// heavySetup says, for a house without a build hook, that its rooms' setup
// is heavy enough that building once per commit would pay: it names what was
// measured. "" when it isn't, or nothing was.
func heavySetup(c *contract) string {
	if buildHook(c) != "" {
		return ""
	}
	sc, ok := loadCosts(c).costOf("after", sideRecipe(c, "after"))
	if !ok || sc.Secs < heavySecs && sc.PeakMB < heavyMB {
		return ""
	}
	return fmt.Sprintf("this house's room setup is heavy (it took %s and %s of memory, at its largest), and every room builds the app for itself: a build hook would build each commit once, for every room and the next contract on it (README, The build hook)",
		(time.Duration(sc.Secs) * time.Second).String(), gb(sc.PeakMB<<20))
}

// What makes a setup heavy: two minutes, or a gigabyte.
const (
	heavySecs = 120
	heavyMB   = 1024
)

// The line for setups. Setups take their turns in the order they asked, and
// several may run at once: as many as the machine's memory covers (each
// one's measured cost, over the reserve, with the costs of those already
// setting up counted as taken), up to maxSetups. A setup whose house was
// never measured the way it sets up now (its setup or build hook changed)
// goes alone, so what it takes is its own; it's measured then, and the
// next ones go together.
//
// The line is a folder of tickets in the rooms folder's .pacing, one per
// process waiting or setting up, each locked by its process for as long as
// it lives, so a ticket whose process died is passed over and cleared. A
// ticket says whether its setup is under way, alone or not, and what it was
// admitted for. turn.lock beside them is held shared by each setup under
// way, and exclusive by one that goes alone.

// ticket is what a ticket file says.
type ticket struct {
	Up     bool  `json:"up,omitempty"`
	Alone  bool  `json:"alone,omitempty"`
	NeedMB int64 `json:"need_mb,omitempty"`
	// Up, and waiting for the memory for its next side (a before app):
	// two of them waiting on each other would wait for ever.
	Waiting bool `json:"waiting,omitempty"`
}

// ticketSize is a ticket file's fixed length: it's written over in place,
// in one write, so a reader never finds it empty halfway.
const ticketSize = 128

// turn is this process's place in the line, from its ticket until its
// setups are done.
type turn struct {
	name   string
	f      *os.File
	t      ticket
	unlock func() // turn.lock, once it's up
}

var (
	turnMu sync.Mutex
	held   *turn
)

// paceSay, when it's set, hears each wait as it begins (room-check and
// equipment-check print them).
var paceSay func(string)

// paceFirst puts this process's setups at the front of the line: a
// room-check or an equipment-check, which someone is sitting waiting for
// and which ends in minutes, goes before the contracts' rooms (in the
// order the checks asked, among themselves).
var paceFirst bool

// maxSetups is how many setups may run at once on the machine: half its
// processors, and at least one; ADMINISTRATION_MAX_SETUPS says otherwise.
func maxSetups() int {
	if n, err := strconv.Atoi(os.Getenv("ADMINISTRATION_MAX_SETUPS")); err == nil && n > 0 {
		return n
	}
	return max(1, runtime.NumCPU()/2)
}

func pacingDir() (string, error) {
	rooms, err := roomsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(rooms, ".pacing"), nil
}

// write puts the ticket's state in its file, over what was there.
func (t *turn) write() {
	b, _ := json.Marshal(t.t)
	if len(b) < ticketSize {
		b = append(b, []byte(strings.Repeat(" ", ticketSize-len(b)))...)
	}
	_, _ = t.f.WriteAt(b, 0)
}

// lineNow is the line as this ticket sees it: how many alive tickets are
// ahead of it and not yet up, and of the others up, how many, how much they
// were admitted for, and whether one is alone.
func lineNow(line, me string) (ahead, up int, upMB int64, alone bool, stuck int, stuckMB int64) {
	entries, _ := os.ReadDir(line)
	for _, e := range entries {
		n := e.Name()
		if n == me {
			continue
		}
		p := filepath.Join(line, n)
		if !ticketAlive(p) {
			continue
		}
		var t ticket
		if b, err := os.ReadFile(p); err == nil {
			_ = json.Unmarshal([]byte(strings.TrimSpace(string(b))), &t)
		}
		switch {
		case t.Up:
			up++
			upMB += t.NeedMB
			alone = alone || t.Alone
			if t.Waiting {
				stuck++
				stuckMB += t.NeedMB
			}
		case n < me:
			// A ticket that says nothing (one being written, or an older
			// Administration's, which holds turn.lock while it's in the
			// line) is ahead, not up.
			ahead++
		}
	}
	return
}

// setupTurn waits for this process's turn to set up a side ("after",
// "before", or "build"), in the order it was asked for, and holds it until
// endSetupTurn. A turn already held (a worker's before app, after its own)
// is kept, and waits only for the memory the side takes. waiting hears why
// it waits each time that changes.
func setupTurn(ctx context.Context, c *contract, side string, waiting func(string)) (alone bool, err error) {
	turnMu.Lock()
	t := held
	turnMu.Unlock()
	pacing, err := pacingDir()
	if err != nil {
		return false, err
	}
	line := filepath.Join(pacing, "line")
	if t == nil {
		if t, err = joinLine(pacing, line); err != nil {
			return false, err
		}
	}
	told := ""
	say := func(why string) {
		if why != told && waiting != nil {
			waiting(why)
		}
		told = why
	}
	// What the side takes, worked out now, again whenever the setups under
	// way change or it means to go alone (a measure may have landed), and
	// every few seconds (the house's hooks and its
	// env/ can change while it waits; hashing them every quarter second
	// costs a big env/ dearly).
	var need sideCost
	var measured, goAlone bool
	var recipe, beforeRecipe string
	lastUp := -1
	for tick := 0; ; tick++ {
		ahead, up, upMB, someAlone, stuck, stuckMB := lineNow(line, t.name)
		// The recipes (hashing the hooks and env/) every few seconds; the
		// costs file whenever the setups under way change, or while it
		// means to go alone, so a measure that lands is seen at once.
		if tick%20 == 0 {
			recipe = sideRecipe(c, side)
			beforeRecipe = sideRecipe(c, "before")
		}
		if tick%20 == 0 || up != lastUp || goAlone {
			lastUp = up
			need, measured = sideNeedOf(c, side, recipe)
			// A worker's turn covers its before app too: one never
			// measured goes alone with the room's own, or it never would.
			goAlone = !measured
			if side == "after" && c.Before {
				if _, ok := sideNeedOf(c, "before", beforeRecipe); !ok {
					goAlone = true
				}
			}
		}
		why := ""
		switch {
		case t.unlock != nil:
			// Already up: only the memory for this side. Every other setup
			// under way waiting for memory too frees none of it.
			// One waiting has its first side up: what it took is out of
			// what's available already, so it isn't counted again.
			why = memoryWait(need, measured, upMB-stuckMB, up-stuck, side)
			if t.t.Waiting != (why != "") {
				t.t.Waiting = why != ""
				t.write()
			}
		case ahead > 0:
			why = fmt.Sprintf("waiting for setup (%d ahead)", ahead)
		case goAlone && up > 0:
			why = fmt.Sprintf("waiting for setup (%d setting up; this house's setup goes alone once, to be measured)", up)
		case someAlone:
			why = "waiting for setup (1 setting up alone, to be measured)"
		case up >= maxSetups():
			why = fmt.Sprintf("waiting for setup (%d setting up, the most at once on this machine)", up)
		default:
			why = memoryWait(need, measured, upMB, up, side)
		}
		if why == "" && t.unlock == nil {
			how := syscall.LOCK_SH
			if goAlone {
				how = syscall.LOCK_EX
			}
			// Up before the lock is taken, so one asking now (a check, ahead
			// of every ticket) counts this one as setting up, not as nothing.
			t.t = ticket{Up: true, Alone: goAlone, NeedMB: max(need.PeakMB, 0)}
			t.write()
			if unlock, ok := tryLock(filepath.Join(pacing, "turn.lock"), how); ok {
				t.unlock = unlock
				// Asked again now this one shows up: a check's ticket is
				// ahead of everyone, so it doesn't wait for one behind it,
				// and the two can pass at the same instant over the limit.
				// The contract's backs out; the check never does. (One gone
				// alone holds the lock exclusive, which no other can share.)
				if _, up2, _, _, _, _ := lineNow(line, t.name); !paceFirst && !goAlone && up2 >= maxSetups() && up2 > up {
					unlock()
					t.unlock = nil
					t.t = ticket{}
					t.write()
					why = "waiting for setup (another started at the same moment)"
				}
			} else {
				t.t = ticket{}
				t.write()
				// Held by one not in the line (an older Administration's
				// setup, or one gone alone): it's ahead all the same.
				why = "waiting for setup (1 ahead)"
			}
		}
		if why == "" {
			t.t = ticket{Up: true, Alone: t.t.Alone, NeedMB: max(need.PeakMB, 0)}
			t.write()
			turnMu.Lock()
			held = t
			turnMu.Unlock()
			return t.t.Alone, nil
		}
		say(why)
		select {
		case <-ctx.Done():
			turnMu.Lock()
			if held != t {
				t.leave()
			}
			turnMu.Unlock()
			return false, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// joinLine puts a new ticket for this process at the end of the line.
func joinLine(pacing, line string) (*turn, error) {
	if err := os.MkdirAll(line, 0o700); err != nil {
		return nil, err
	}
	// A ticket made by a process killed before it got into the line: nobody
	// holds it, so it goes.
	// (Only one a minute old: another process makes its ticket a moment
	// before it locks it.)
	if stale, _ := filepath.Glob(filepath.Join(pacing, "ticket-*")); len(stale) > 0 {
		for _, p := range stale {
			if st, err := os.Stat(p); err == nil && time.Since(st.ModTime()) > time.Minute {
				ticketAlive(p)
			}
		}
	}
	name := fmt.Sprintf("%019d-%07d", time.Now().UnixNano(), os.Getpid())
	if paceFirst {
		// Before every time-named ticket, an older Administration's too.
		name = "0-" + name
	}
	// Locked before it's in the line, so nobody finds it there unlocked and
	// takes it for a dead one.
	tmp := filepath.Join(pacing, "ticket-"+name)
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		os.Remove(tmp)
		return nil, err
	}
	t := &turn{name: name, f: f}
	t.write()
	if err := os.Rename(tmp, filepath.Join(line, name)); err != nil {
		f.Close()
		os.Remove(tmp)
		return nil, err
	}
	return t, nil
}

// sideNeed is what a side's setup takes on this machine, as the house's
// setups made the way it's set up now were measured. measured is false for
// one never measured so, and true wherever memory can't be read (nothing is
// measured there, so nothing goes alone to be).
func sideNeed(c *contract, side string) (sideCost, bool) {
	return sideNeedOf(c, side, sideRecipe(c, side))
}

// sideNeedOf is sideNeed with the recipe already worked out.
func sideNeedOf(c *contract, side, recipe string) (sideCost, bool) {
	if _, ok := readMemory(); !ok {
		return sideCost{}, true
	}
	return loadCosts(c).costOf(side, recipe)
}

// memoryWait is why a setup waits for memory, "" when it needn't: what's
// available, less the reserve and what the setups under way were admitted
// for, covers what this side took before. With nothing set up or at work,
// nothing of this tool's will free any more, so it goes anyway: it can only
// wait for itself.
func memoryWait(need sideCost, measured bool, upMB int64, up int, side string) string {
	m, ok := readMemory()
	if !ok {
		return ""
	}
	reserve := memReserve(m.Total)
	free := m.Available - reserve - upMB<<20
	if free >= need.PeakMB<<20 || up == 0 && roomsAtWork() == 0 {
		return ""
	}
	if !measured || need.PeakMB == 0 {
		return fmt.Sprintf("waiting for memory: %s is free, and %s is kept for the machine", gb(m.Available), gb(reserve))
	}
	what := map[string]string{"before": " the before app", "build": " (its build)"}[side]
	taken := ""
	if upMB > 0 {
		taken = fmt.Sprintf(" and what the %d setting up take", up)
	}
	return fmt.Sprintf("waiting for memory: this house's rooms take about %s to set up%s, and %s is free over the %s kept for the machine%s",
		gb(need.PeakMB<<20), what, gb(max(0, free)), gb(reserve), taken)
}

// tryLock takes a lock on path (how: syscall.LOCK_EX or LOCK_SH) if it's
// free now.
func tryLock(path string, how int) (unlock func(), ok bool) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false
	}
	if syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB) != nil {
		f.Close()
		return nil, false
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, true
}

// leave takes the ticket out of the line and lets turn.lock go.
func (t *turn) leave() {
	if t.unlock != nil {
		t.unlock()
		t.unlock = nil
	}
	if pacing, err := pacingDir(); err == nil {
		_ = os.Remove(filepath.Join(pacing, "line", t.name))
	}
	_ = syscall.Flock(int(t.f.Fd()), syscall.LOCK_UN)
	t.f.Close()
}

// ticketAlive says whether a ticket's process still holds it. One nobody
// holds is a process that died in line: it's cleared.
func ticketAlive(path string) bool {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return false
	}
	defer f.Close()
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return true
	}
	_ = os.Remove(path)
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}

// holdsTurn says whether this process holds a turn now.
func holdsTurn() bool {
	turnMu.Lock()
	defer turnMu.Unlock()
	return held != nil
}

// endSetupTurn lets the next setups go. It does nothing when this process
// holds no turn.
func endSetupTurn() {
	turnMu.Lock()
	t := held
	held = nil
	turnMu.Unlock()
	if t != nil {
		t.leave()
	}
}

// othersSettingUp says whether a setup other than this process's is under
// way: a measure taken then is partly theirs.
func othersSettingUp() bool {
	pacing, err := pacingDir()
	if err != nil {
		return false
	}
	turnMu.Lock()
	me := ""
	if held != nil {
		me = held.name
	}
	turnMu.Unlock()
	_, up, _, _, _, _ := lineNow(filepath.Join(pacing, "line"), me)
	return up > 0
}

// meter follows the machine's available memory while a setup runs, to
// measure what it takes. shared is whether another setup ran at any time
// beside it, which makes the measure partly that one's.
type meter struct {
	ok        bool
	start     int64
	mu        sync.Mutex
	low       int64
	shared    bool
	stop      chan struct{}
	stopped   chan struct{}
	once      sync.Once
	startedAt time.Time
}

func startMeter() *meter {
	m := &meter{stop: make(chan struct{}), stopped: make(chan struct{}), startedAt: time.Now()}
	mem, ok := readMemory()
	if !ok {
		close(m.stopped)
		return m
	}
	m.ok, m.start, m.low = true, mem.Available, mem.Available
	m.shared = othersSettingUp()
	go func() {
		defer close(m.stopped)
		for {
			select {
			case <-m.stop:
				return
			case <-time.After(250 * time.Millisecond):
			}
			if now, ok := readMemory(); ok {
				m.mu.Lock()
				m.low = min(m.low, now.Available)
				m.mu.Unlock()
			}
			if othersSettingUp() {
				m.mu.Lock()
				m.shared = true
				m.mu.Unlock()
			}
		}
	}()
	return m
}

// finish stops the meter and says what the setup took: the deepest fall in
// available memory since it started, and the fall now. ok is false when
// memory can't be read, or another setup ran beside this one.
func (m *meter) finish() (peak, steady int64, ok bool) {
	m.once.Do(func() { close(m.stop) })
	<-m.stopped
	if !m.ok {
		return 0, 0, false
	}
	now, ok := readMemory()
	if !ok {
		return 0, 0, false
	}
	m.mu.Lock()
	low, shared := min(m.low, now.Available), m.shared
	m.mu.Unlock()
	return max(0, m.start-low), max(0, m.start-now.Available), !shared
}

// roomsAtWork counts the rooms whose professional is at work now, across
// the open contracts: what frees memory as they finish.
func roomsAtWork() int {
	n := 0
	for _, st := range seats() {
		if st.state == "working" && st.roomed {
			n++
		}
	}
	return n
}

// pace is one side's setup, once the machine has room for it: done
// measures it.
type pace struct {
	c      *contract
	side   string
	recipe string
	m      *meter
	once   sync.Once
}

// paceSetup waits for the machine to have room for a side's setup ("after"
// or "before"): its turn in the line, and the memory. Its waits are said in
// the room's status and added to what the seat waited. The setup starts its
// own time limit after this returns, so waiting never uses it up.
func paceSetup(ctx context.Context, c *contract, side string, s *status, save func()) (*pace, error) {
	t0 := time.Now()
	waited := false
	say := func(why string) {
		waited = true
		s.State, s.Reason = "waiting", why
		save()
		if paceSay != nil {
			paceSay(why)
		}
	}
	p, err := paceSide(ctx, c, side, say)
	if waited {
		s.WaitedSecs = roundSecs(s.WaitedSecs + time.Since(t0).Seconds())
	}
	if err != nil {
		return nil, err
	}
	s.State, s.Reason = "setup", "setting up"
	if side == "before" {
		s.Reason = "setting up the before app"
	}
	save()
	return p, nil
}

// paceSide takes a side's turn and starts its measure.
func paceSide(ctx context.Context, c *contract, side string, say func(string)) (*pace, error) {
	if _, err := setupTurn(ctx, c, side, say); err != nil {
		return nil, err
	}
	// The recipe as it is now its turn has come, which is the hook that
	// runs: one edited while it waited is measured as what it is.
	return &pace{c: c, side: side, recipe: sideRecipe(c, side), m: startMeter()}, nil
}

// done ends a side's measure. A setup that came up (ok), with no other
// beside it, is recorded as the house's cost; any other only stops the
// meter.
func (p *pace) done(ok bool) {
	if p == nil {
		return
	}
	p.once.Do(func() {
		peak, steady, measured := p.m.finish()
		if ok && measured {
			recordCost(p.c, p.side, sideCost{PeakMB: peak >> 20, SteadyMB: steady >> 20,
				Secs: int(time.Since(p.m.startedAt).Seconds()), At: time.Now().UTC().Truncate(time.Second), Recipe: p.recipe})
		}
	})
}

func roundSecs(f float64) float64 {
	return float64(int64(f*10+0.5)) / 10
}

// seat is one user of an open contract, as the rooms see it.
type seat struct {
	contract string
	name     string
	order    int64 // the contract's place in line
	index    int   // the user's place in its cast
	state    string
	queued   bool // waiting for a room
	held     bool // holding one
	roomed   bool // has a room built or being built
	// The room it holds, the same for users sharing one (share.go), so a
	// shared room counts once.
	room string
}

// before says whether a is ahead of b in the line for rooms.
func (a seat) before(b seat) bool {
	if a.order != b.order {
		return a.order < b.order
	}
	if a.contract != b.contract {
		return a.contract < b.contract
	}
	return a.index < b.index
}

// contractOrder is a contract's place in the line for rooms: when it was
// placed, to the nanosecond for one placed since contracts kept it.
func contractOrder(c *contract) int64 {
	if c.Seq != 0 {
		return c.Seq
	}
	return c.PlacedAt.UnixNano()
}

// seats is every user of every open contract. A room is held by a user
// that's not waiting for one and not done, or whose room isn't torn down
// yet; a room is free once its user is back and it's torn down, before
// anyone closes the contract.
func seats() []seat {
	sweepChecks()
	books, err := booksDir()
	if err != nil {
		return nil
	}
	files, _ := filepath.Glob(filepath.Join(books, "contracts", "*", "contract.json"))
	var out []seat
	for _, f := range files {
		dir := filepath.Dir(f)
		c, _, err := loadContract(filepath.Base(dir))
		if err != nil || c.ClosedAt != nil {
			continue
		}
		for i, p := range c.Professionals {
			if p.Kind != "user" {
				continue
			}
			s := seatStatus(dir, p.Name)
			st := seat{contract: c.ID, name: p.Name, order: contractOrder(c), index: i, state: s.State,
				roomed: s.Room != "" && !s.TornDown, room: c.ID + "/" + p.Name}
			if s.SharedRoom != "" {
				st.room = s.SharedRoom
			} else if s.Room != "" {
				st.room = s.Room
			}
			st.queued = !s.terminal() && s.Queued
			st.held = !s.terminal() && !s.Queued || st.roomed
			// A room handed on to it (handon.go) is held from when its
			// papers are left, before its worker says so.
			if !s.terminal() && (isFile(filepath.Join(dir, p.Name, "handoff.json")) || isFile(filepath.Join(dir, p.Name, "handoff.taken"))) {
				st.held, st.queued = true, false
			}
			out = append(out, st)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].before(out[j]) })
	return out
}

// seatStatus is readStatus for the line, which every waiting worker reads
// every few seconds: a worker that's gone is told by its pid alone, not by
// looking for its tag in every process on the machine (a scan per seat,
// per worker, per poll). A pid reused by something else keeps its seat
// held, as a room not torn down does, until its contract closes.
func seatStatus(dir, name string) status {
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
	if !s.terminal() && s.WorkerPID > 0 {
		// A zombie is gone too (Linux says so cheaply; elsewhere whatever
		// it was reparented to reaps it).
		if err := syscall.Kill(s.WorkerPID, 0); errors.Is(err, syscall.ESRCH) || runtime.GOOS == "linux" && zombie(s.WorkerPID) {
			s.State = "failed"
		}
	}
	return s
}

// roomUse is how the rooms stand: how many are held, how many users wait
// for one, and the open contracts doing either, in line order.
func roomUse() (held, waiting int, ids []string) {
	seen := map[string]bool{}
	rooms := map[string]bool{}
	for _, st := range seats() {
		if !st.held && !st.queued {
			continue
		}
		if st.held {
			if !rooms[st.room] {
				held++
			}
			rooms[st.room] = true
		} else {
			waiting++
		}
		if !seen[st.contract] {
			seen[st.contract] = true
			ids = append(ids, st.contract)
		}
	}
	return held, waiting, ids
}

// awaitRoom holds a user's worker until a room is free for it: fewer than
// the room limit are held, and no user waiting for one is ahead of it in
// line. It's asked under the books' lock, so two workers can't both take
// the last room.
func awaitRoom(ctx context.Context, c *contract, name string, s *status, save func()) error {
	t0 := time.Now()
	var ci *castItem
	for i := range c.Professionals {
		if c.Professionals[i].Name == name {
			ci = &c.Professionals[i]
		}
	}
	books, err := booksDir()
	if err != nil {
		return err
	}
	cdir := filepath.Join(books, "contracts", c.ID)
	for {
		unlock, err := lockBooks()
		if err != nil {
			return err
		}
		ahead, held := 0, 0
		rooms := map[string]bool{}
		var me seat
		all := seats()
		for _, st := range all {
			if st.contract == c.ID && st.name == name {
				me = st
			}
		}
		me.contract, me.name, me.order = c.ID, name, contractOrder(c)
		for i, p := range c.Professionals {
			if p.Name == name {
				me.index = i
			}
		}
		for _, st := range all {
			if st.contract == c.ID && st.name == name {
				continue
			}
			if st.held {
				if !rooms[st.room] {
					held++
				}
				rooms[st.room] = true
			} else if st.queued && st.before(me) {
				ahead++
			}
		}
		// A user of a contract whose rooms are shared waits for a place in
		// one of its contract's (share.go) rather than set up its own.
		why := ""
		if ahead == 0 && held < maxRooms() {
			why = shareWait(c, cdir, name, ci)
		}
		if ahead == 0 && held < maxRooms() && why == "" {
			s.Queued = false
			s.State, s.Reason = "setup", ""
			if w := time.Since(t0).Seconds(); w >= 1 {
				s.WaitedSecs = roundSecs(s.WaitedSecs + w)
			}
			save()
			unlock()
			return nil
		}
		switch {
		case why != "":
		case ahead > 0:
			why = fmt.Sprintf("waiting for a room (%d ahead)", ahead)
		default:
			why = fmt.Sprintf("waiting for a room (all %d in use)", maxRooms())
		}
		if s.State != "waiting" || s.Reason != why || !s.Queued {
			s.State, s.Reason, s.Queued = "waiting", why, true
			save()
		}
		unlock()
		select {
		case <-ctx.Done():
			s.WaitedSecs = roundSecs(s.WaitedSecs + time.Since(t0).Seconds())
			return ctx.Err()
		case <-time.After(pollEvery()):
		}
	}
}

// runCleanup runs what a room's setup declared, as soon as it started, for
// taking down what it started outside its process group (a container
// stack): the lines of its cleanup file ($ADMINISTRATION_CLEANUP), each a
// shell command, the last first, each on its own so one that fails doesn't
// stop the rest. It runs at every teardown, whether or not the setup got as
// far as its manifest, and once: the file goes after.
func runCleanup(c *contract, file string, env []string, logf *os.File) {
	b, err := os.ReadFile(file)
	if err != nil {
		return
	}
	var cmds []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			cmds = append(cmds, l)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), teardownLimit)
	defer cancel()
	for i := len(cmds) - 1; i >= 0 && ctx.Err() == nil; i-- {
		cmd := exec.Command("/bin/sh", "-c", cmds[i])
		cmd.Dir = hookDir(c)
		cmd.Env = env
		if logf != nil {
			fmt.Fprintf(logf, "administration: cleanup: %s\n", cmds[i])
			cmd.Stdout, cmd.Stderr = logf, logf
		}
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if cmd.Start() == nil {
			if err := waitGroup(ctx, cmd); err != nil && logf != nil {
				fmt.Fprintf(logf, "administration: cleanup failed: %v\n", err)
			}
		}
	}
	_ = os.Remove(file)
}

// cleanupFile is where a side's setup declares its cleanup, beside the
// manifest it writes: cleanup for the room's app, cleanup-before for the
// before app's.
func cleanupFile(mf string) string {
	if filepath.Base(mf) == "room-before.json" {
		return filepath.Join(filepath.Dir(mf), "cleanup-before")
	}
	return filepath.Join(filepath.Dir(mf), "cleanup")
}
