package main

import (
	"context"
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
//   - one room setup at a time on the machine (a turn, kept in the rooms
//     folder), the after and before sides alike, so a contract's rooms
//     don't all build at once;
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
	var out sideCost
	for _, sc := range rc.Sides[side] {
		out.PeakMB = max(out.PeakMB, sc.PeakMB)
		out.SteadyMB = max(out.SteadyMB, sc.SteadyMB)
		out.Secs = max(out.Secs, sc.Secs)
		if sc.At.After(out.At) {
			out.At = sc.At
		}
	}
	return out, len(rc.Sides[side]) > 0
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
	sc, ok := loadCosts(c).cost("after")
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

// The machine's setup turn, while this process holds it: a worker keeps it
// from its room's first setup through its before app's, so its professional
// starts as soon as it can.
var (
	turnMu   sync.Mutex
	turnHeld func()
)

// paceSay, when it's set, hears each wait as it begins (room-check and
// equipment-check print them).
var paceSay func(string)

// setupTurn waits for the machine's setup turn, in the order it was asked
// for, and holds it until endSetupTurn. A turn already held is kept.
// waiting hears how many are ahead each time that changes.
//
// The line is a folder of tickets in the rooms folder's .pacing, one per
// process waiting or holding, each locked by its process for as long as it
// lives, so a ticket whose process died is passed over and cleared; the
// turn itself is a lock beside them.
func setupTurn(ctx context.Context, waiting func(ahead int)) error {
	turnMu.Lock()
	held := turnHeld != nil
	turnMu.Unlock()
	if held {
		return nil
	}
	rooms, err := roomsDir()
	if err != nil {
		return err
	}
	pacing := filepath.Join(rooms, ".pacing")
	line := filepath.Join(pacing, "line")
	if err := os.MkdirAll(line, 0o700); err != nil {
		return err
	}
	name := fmt.Sprintf("%019d-%07d", time.Now().UnixNano(), os.Getpid())
	// Locked before it's in the line, so nobody finds it there unlocked and
	// takes it for a dead one.
	tmp := filepath.Join(pacing, "ticket-"+name)
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	ticket := filepath.Join(line, name)
	if err := os.Rename(tmp, ticket); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	leave := func() {
		_ = os.Remove(ticket)
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}
	last := -1
	for {
		ahead := 0
		entries, _ := os.ReadDir(line)
		for _, e := range entries {
			if e.Name() < name && ticketAlive(filepath.Join(line, e.Name())) {
				ahead++
			}
		}
		if ahead == 0 {
			if unlock, ok := tryLockFile(filepath.Join(pacing, "turn.lock")); ok {
				turnMu.Lock()
				turnHeld = func() { unlock(); leave() }
				turnMu.Unlock()
				return nil
			}
			// Held by one not in the line ahead of this (it was asked for
			// in the same instant): it's ahead all the same.
			ahead = 1
		}
		if ahead != last && waiting != nil {
			waiting(ahead)
		}
		last = ahead
		select {
		case <-ctx.Done():
			leave()
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
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

// endSetupTurn lets the next room set up. It does nothing when this process
// holds no turn.
func endSetupTurn() {
	turnMu.Lock()
	h := turnHeld
	turnHeld = nil
	turnMu.Unlock()
	if h != nil {
		h()
	}
}

// meter follows the machine's available memory while a setup runs, to
// measure what it takes.
type meter struct {
	ok        bool
	start     int64
	mu        sync.Mutex
	low       int64
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
		}
	}()
	return m
}

// finish stops the meter and says what the setup took: the deepest fall in
// available memory since it started, and the fall now.
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
	low := min(m.low, now.Available)
	m.mu.Unlock()
	return max(0, m.start-low), max(0, m.start-now.Available), true
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

// admitByMemory waits until the machine has the memory for a side's setup:
// what's available, less the reserve, covers the most the house's rooms
// have taken to set that side up. A house never measured needs only what's
// over the reserve, and is measured as its room sets up. With no room at
// work anywhere, nothing of this tool's will free any more, so the room
// goes anyway: it can only wait for itself.
func admitByMemory(ctx context.Context, c *contract, side string, waiting func(string)) error {
	need, known := loadCosts(c).cost(side)
	told := ""
	for {
		m, ok := readMemory()
		if !ok {
			return nil
		}
		reserve := memReserve(m.Total)
		free := m.Available - reserve
		if free >= need.PeakMB<<20 || roomsAtWork() == 0 {
			return nil
		}
		why := fmt.Sprintf("waiting for memory: %s is free, and %s is kept for the machine", gb(m.Available), gb(reserve))
		if known {
			why = fmt.Sprintf("waiting for memory: this house's rooms take about %s to set up%s, and %s is free over the %s kept for the machine",
				gb(need.PeakMB<<20), map[bool]string{true: " the before app", false: ""}[side == "before"], gb(max(0, free)), gb(reserve))
		}
		// Said again only when it changes by a tenth of a gigabyte or more.
		if why != told && waiting != nil {
			waiting(why)
			told = why
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollEvery()):
		}
	}
}

// pace is one side's setup, once the machine has room for it: done
// measures it.
type pace struct {
	c    *contract
	side string
	m    *meter
	once sync.Once
}

// paceSetup waits for the machine to have room for a side's setup ("after"
// or "before"): the machine's setup turn, then the memory. Its waits are
// said in the room's status and added to what the seat waited. The setup
// starts its own time limit after this returns, so waiting never uses it up.
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
	err := setupTurn(ctx, func(ahead int) { say(fmt.Sprintf("waiting for setup (%d ahead)", ahead)) })
	if err == nil {
		err = admitByMemory(ctx, c, side, say)
	}
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
	return &pace{c: c, side: side, m: startMeter()}, nil
}

// done ends a side's measure. A setup that came up (ok) is recorded as the
// house's cost; one that failed only stops the meter.
func (p *pace) done(ok bool) {
	if p == nil {
		return
	}
	p.once.Do(func() {
		peak, steady, measured := p.m.finish()
		if ok && measured {
			recordCost(p.c, p.side, sideCost{PeakMB: peak >> 20, SteadyMB: steady >> 20,
				Secs: int(time.Since(p.m.startedAt).Seconds()), At: time.Now().UTC().Truncate(time.Second)})
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
				roomed: s.Room != "" && !s.TornDown}
			st.queued = !s.terminal() && s.Queued
			st.held = !s.terminal() && !s.Queued || st.roomed
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
	for _, st := range seats() {
		if !st.held && !st.queued {
			continue
		}
		if st.held {
			held++
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
	for {
		unlock, err := lockBooks()
		if err != nil {
			return err
		}
		ahead, held := 0, 0
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
				held++
			} else if st.queued && st.before(me) {
				ahead++
			}
		}
		if ahead == 0 && held < maxRooms() {
			s.Queued = false
			s.State, s.Reason = "setup", ""
			if w := time.Since(t0).Seconds(); w >= 1 {
				s.WaitedSecs = roundSecs(s.WaitedSecs + w)
			}
			save()
			unlock()
			return nil
		}
		why := fmt.Sprintf("waiting for a room (%d ahead)", ahead)
		if ahead == 0 {
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
