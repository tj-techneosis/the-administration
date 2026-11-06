package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestMemoryIsReadFromMeminfoAndVMStat(t *testing.T) {
	m, ok := parseMeminfo("MemTotal:       16384000 kB\nMemFree:          100 kB\nMemAvailable:    8192000 kB\n")
	if !ok || m.Total != 16384000*1024 || m.Available != 8192000*1024 {
		t.Errorf("meminfo: %+v %v", m, ok)
	}
	if _, ok := parseMeminfo("MemTotal: 100 kB\n"); ok {
		t.Error("meminfo without MemAvailable was taken")
	}
	vm := `Mach Virtual Memory Statistics: (page size of 16384 bytes)
Pages free:                               10000.
Pages active:                            500000.
Pages inactive:                           20000.
Pages speculative:                         5000.
Pages wired down:                        100000.
`
	m, ok = parseVMStat(vm, 8<<30)
	if !ok || m.Available != 35000*16384 || m.Total != 8<<30 {
		t.Errorf("vm_stat: %+v %v", m, ok)
	}
}

func TestTheReserveIsAShareOfMemoryAndAtLeastTwoGB(t *testing.T) {
	if r := memReserve(8 << 30); r != 2<<30 {
		t.Errorf("an 8 GB machine keeps %d", r)
	}
	if r := memReserve(64 << 30); r != 64<<30*15/100 {
		t.Errorf("a 64 GB machine keeps %d", r)
	}
}

func TestAHousesCostIsTheLargestOfItsLastMeasures(t *testing.T) {
	t.Setenv("ADMINISTRATION_HOME", t.TempDir())
	c := &contract{Project: t.TempDir(), Key: "example.com/acme/shop"}
	if _, ok := loadCosts(c).cost("after"); ok {
		t.Fatal("a house never measured has a cost")
	}
	for _, mb := range []int64{3000, 500, 700, 900} {
		recordCost(c, "after", sideCost{PeakMB: mb, SteadyMB: mb / 2, Secs: 10})
	}
	sc, ok := loadCosts(c).cost("after")
	// The 3000 is the oldest of four, and only three are kept.
	if !ok || sc.PeakMB != 900 || sc.SteadyMB != 450 {
		t.Errorf("cost %+v %v", sc, ok)
	}
	if _, ok := loadCosts(c).cost("before"); ok {
		t.Error("the before side has the after side's cost")
	}
	// Heavy by memory, with no build hook: said, with what was measured the
	// way it sets up now (the measures above were made some other way).
	if h := heavySetup(c); h != "" {
		t.Errorf("said from measures of another setup: %q", h)
	}
	recordCost(c, "after", sideCost{PeakMB: 2048, Secs: 30, Recipe: sideRecipe(c, "after")})
	if h := heavySetup(c); !strings.Contains(h, "2.0 GB") || !strings.Contains(h, "build hook") {
		t.Errorf("heavy: %q", h)
	}
	// With a build hook it isn't said.
	if err := os.MkdirAll(filepath.Join(c.Project, ".administration"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.Project, ".administration", "build"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if h := heavySetup(c); h != "" {
		t.Errorf("said with a build hook: %q", h)
	}
}

func TestMemoryAdmitsARoomWithNothingAtWorkToWaitFor(t *testing.T) {
	t.Setenv("ADMINISTRATION_HOME", t.TempDir())
	setMemory(t, 16<<20, 1<<20) // 16 GB, 1 GB available: under the reserve.
	need := sideCost{PeakMB: 4000}
	// No room is at work and nothing sets up, so nothing will free memory:
	// it goes, rather than wait for itself.
	if why := memoryWait(need, true, 0, 0, "after"); why != "" {
		t.Errorf("waited with nothing to wait for: %q", why)
	}
	// With another setting up, it waits for that one.
	if why := memoryWait(need, true, 2000, 1, "after"); !strings.Contains(why, "about 3.9 GB to set up") || !strings.Contains(why, "the 1 setting up") {
		t.Errorf("why: %q", why)
	}
	// What those setting up were admitted for counts as taken.
	setMemory(t, 16<<20, 9<<20) // 9 GB available, 2.4 kept: 6.6 over.
	if why := memoryWait(need, true, 2000, 1, "after"); why != "" {
		t.Errorf("6.6 GB over the reserve, 2 GB taken, 3.9 needed: %q", why)
	}
	if why := memoryWait(need, true, 3000, 1, "after"); why == "" {
		t.Error("6.6 GB over the reserve, 2.9 GB taken, 3.9 needed: admitted")
	}
}

// setMemory makes the machine's memory what a meminfo file says, in kB.
func setMemory(t *testing.T, totalKB, availKB int64) {
	t.Helper()
	mi := filepath.Join(t.TempDir(), "meminfo")
	if err := os.WriteFile(mi, []byte(fmt.Sprintf("MemTotal: %d kB\nMemAvailable: %d kB\n", totalKB, availKB)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ADMINISTRATION_MEMINFO", mi)
}

// lineTest is a machine with plenty of memory, its own rooms folder and a
// house with a setup hook.
func lineTest(t *testing.T) (string, *contract) {
	t.Helper()
	rooms := filepath.Join(os.TempDir(), "adm-turn-"+randomSuffix(t))
	t.Cleanup(func() { os.RemoveAll(rooms) })
	t.Setenv("ADMINISTRATION_ROOMS", rooms)
	t.Setenv("ADMINISTRATION_HOME", t.TempDir())
	t.Setenv("ADMINISTRATION_MAX_SETUPS", "2")
	setMemory(t, 64<<20, 60<<20)
	c := &contract{Project: t.TempDir(), Key: "example.com/acme/shop"}
	adm := filepath.Join(c.Project, ".administration")
	if err := os.MkdirAll(adm, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(adm, "setup"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(rooms, ".pacing"), c
}

// otherUp puts another process's setup in the line, under way: a ticket
// held through its own open file, as that process would hold it.
func otherUp(t *testing.T, pacing, name string, tk ticket) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(pacing, "line"), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(pacing, "line", name), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(tk)
	if _, err := f.Write(b); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
}

// turnWaits says why setupTurn waits, failing if it doesn't.
func turnWaits(t *testing.T, c *contract, side string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	why := ""
	if _, err := setupTurn(ctx, c, side, func(w string) { why = w }); err == nil {
		endSetupTurn()
		t.Fatal("went")
	}
	return why
}

func TestTheSetupTurnIsHeldOnceAndLetGo(t *testing.T) {
	pacing, c := lineTest(t)
	ctx := context.Background()
	// Never measured: it goes alone, holding the turn exclusive.
	alone, err := setupTurn(ctx, c, "after", nil)
	if err != nil || !alone {
		t.Fatal(alone, err)
	}
	if _, ok := tryLock(filepath.Join(pacing, "turn.lock"), syscall.LOCK_SH); ok {
		t.Fatal("a setup alone doesn't hold the turn exclusive")
	}
	// Asked again by the same process: kept, not waited for.
	if _, err := setupTurn(ctx, c, "before", nil); err != nil {
		t.Fatal(err)
	}
	endSetupTurn()
	unlock, ok := tryLockFile(filepath.Join(pacing, "turn.lock"))
	if !ok {
		t.Fatal("the turn wasn't let go")
	}
	unlock()
	if es, _ := os.ReadDir(filepath.Join(pacing, "line")); len(es) != 0 {
		t.Errorf("tickets left in line: %v", es)
	}
	// Measured the way it sets up now: it goes shared.
	recordCost(c, "after", sideCost{PeakMB: 1000, Recipe: sideRecipe(c, "after")})
	if alone, err := setupTurn(ctx, c, "after", nil); err != nil || alone {
		t.Fatal(alone, err)
	}
	if unlock, ok := tryLock(filepath.Join(pacing, "turn.lock"), syscall.LOCK_SH); !ok {
		t.Error("a shared setup holds the turn exclusive")
	} else {
		unlock()
	}
	endSetupTurn()
	// A ticket whose process is gone (nobody holds it) is passed over and
	// cleared.
	dead := filepath.Join(pacing, "line", "0000000000000000001-0000001")
	if err := os.WriteFile(dead, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waited := false
	if _, err := setupTurn(ctx, c, "after", func(string) { waited = true }); err != nil {
		t.Fatal(err)
	}
	endSetupTurn()
	if waited {
		t.Error("waited behind a dead ticket")
	}
	if _, err := os.Stat(dead); err == nil {
		t.Error("the dead ticket is still there")
	}
}

func TestSetupsGoTogetherAsTheMemoryAndTheLimitAllow(t *testing.T) {
	pacing, c := lineTest(t)
	recordCost(c, "after", sideCost{PeakMB: 4000, Recipe: sideRecipe(c, "after")})
	ctx := context.Background()
	// One other under way, of at most two: this one goes beside it.
	otherUp(t, pacing, "0000000000000000001-0000001", ticket{Up: true, NeedMB: 4000})
	if alone, err := setupTurn(ctx, c, "after", nil); err != nil || alone {
		t.Fatal(alone, err)
	}
	// Its ticket says it's up, and what it was admitted for.
	es, _ := os.ReadDir(filepath.Join(pacing, "line"))
	if len(es) != 2 {
		t.Fatalf("line: %v", es)
	}
	_, up, upMB, _, _, _ := lineNow(filepath.Join(pacing, "line"), "")
	if up != 2 || upMB != 8000 {
		t.Errorf("up %d, %d MB", up, upMB)
	}
	endSetupTurn()
	// Two under way: the most at once.
	otherUp(t, pacing, "0000000000000000002-0000002", ticket{Up: true, NeedMB: 10})
	if why := turnWaits(t, c, "after"); !strings.Contains(why, "2 setting up, the most at once") {
		t.Errorf("why: %q", why)
	}
}

func TestASetupNeverMeasuredSoGoesAlone(t *testing.T) {
	pacing, c := lineTest(t)
	// Measured, but before the house had a build hook: not how it sets up
	// now.
	recordCost(c, "after", sideCost{PeakMB: 8000, Recipe: sideRecipe(c, "after")})
	if err := os.WriteFile(filepath.Join(c.Project, ".administration", "build"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadCosts(c).costOf("after", sideRecipe(c, "after")); ok {
		t.Fatal("a measure made without the build hook counts with it")
	}
	otherUp(t, pacing, "0000000000000000001-0000001", ticket{Up: true, NeedMB: 10})
	if why := turnWaits(t, c, "after"); !strings.Contains(why, "goes alone once, to be measured") {
		t.Errorf("why: %q", why)
	}
}

func TestNothingGoesBesideASetupAlone(t *testing.T) {
	pacing, c := lineTest(t)
	recordCost(c, "after", sideCost{PeakMB: 10, Recipe: sideRecipe(c, "after")})
	otherUp(t, pacing, "0000000000000000001-0000001", ticket{Up: true, Alone: true, NeedMB: 10})
	if why := turnWaits(t, c, "after"); !strings.Contains(why, "setting up alone") {
		t.Errorf("why: %q", why)
	}
}

func TestTheLineGoesInTurn(t *testing.T) {
	pacing, c := lineTest(t)
	recordCost(c, "after", sideCost{PeakMB: 10, Recipe: sideRecipe(c, "after")})
	// One asked first and still waiting: it goes first, however much room
	// there is.
	otherUp(t, pacing, "0000000000000000001-0000001", ticket{})
	if why := turnWaits(t, c, "after"); why != "waiting for setup (1 ahead)" {
		t.Errorf("why: %q", why)
	}
}

func TestAChecksSetupGoesFirst(t *testing.T) {
	pacing, c := lineTest(t)
	recordCost(c, "after", sideCost{PeakMB: 10, Recipe: sideRecipe(c, "after")})
	// A contract's room asked first and still waiting: a room-check goes
	// before it all the same.
	otherUp(t, pacing, "0000000000000000001-0000001", ticket{})
	paceFirst = true
	t.Cleanup(func() { paceFirst = false })
	if _, err := setupTurn(context.Background(), c, "after", nil); err != nil {
		t.Fatal(err)
	}
	endSetupTurn()
}

func TestAMeasureTakenBesideAnotherSetupIsntKept(t *testing.T) {
	pacing, c := lineTest(t)
	recordCost(c, "after", sideCost{PeakMB: 10, Recipe: sideRecipe(c, "after")})
	p, err := paceSide(context.Background(), c, "after", nil)
	if err != nil {
		t.Fatal(err)
	}
	otherUp(t, pacing, "0000000000000000001-0000001", ticket{Up: true, NeedMB: 10})
	time.Sleep(600 * time.Millisecond)
	p.done(true)
	endSetupTurn()
	if n := len(loadCosts(c).Sides["after"]); n != 1 {
		t.Errorf("%d measures kept, one of them taken beside another setup", n)
	}
}

func randomSuffix(t *testing.T) string {
	id, err := randomID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestTheLineForRoomsGoesByPlacement(t *testing.T) {
	a := seat{contract: "bbbb", order: 1, index: 2}
	b := seat{contract: "aaaa", order: 2, index: 0}
	c := seat{contract: "bbbb", order: 1, index: 3}
	if !a.before(b) || b.before(a) || !a.before(c) {
		t.Error("seats out of line")
	}
}

func TestAPersonsOwnHookRunsInItsOwnFolder(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("ADMINISTRATION_CONFIG", cfg)
	own := filepath.Join(cfg, "hooks", "closed")
	if d := hookFolder(own, "/work/project"); d != filepath.Join(cfg, "hooks") {
		t.Errorf("the person's hook runs in %s", d)
	}
	if d := hookFolder("/work/project/.administration/hooks/closed", "/work/project"); d != "/work/project" {
		t.Errorf("the project's hook runs in %s", d)
	}
	// A house kept on the machine is the project's, not the person's own hooks.
	kept := filepath.Join(cfg, "projects", "example.com", "acme", "shop", "hooks", "closed")
	if d := hookFolder(kept, "/work/project"); d != "/work/project" {
		t.Errorf("a kept house's hook runs in %s", d)
	}
}

func TestEachSideDeclaresItsCleanupBesideItsManifest(t *testing.T) {
	if f := cleanupFile("/b/c/x/room.json"); f != "/b/c/x/cleanup" {
		t.Error(f)
	}
	if f := cleanupFile("/b/c/x/room-before.json"); f != "/b/c/x/cleanup-before" {
		t.Error(f)
	}
}

func TestARoomPlanIsReadAndSaid(t *testing.T) {
	p, err := parseRooms(" a+b , c>d>e, f+g>h, solo ")
	if err != nil {
		t.Fatal(err)
	}
	if p.String() != "a+b, c>d>e, f+g>h, solo" {
		t.Errorf("String: %q", p.String())
	}
	want := "a and b together; c, then d, then e, in turns; f and g, then h, in turns, together where joined; solo in a room of its own"
	if p.Said() != want {
		t.Errorf("Said: %q", p.Said())
	}
	c := &contract{Rooms: p.String()}
	if !c.together("a", "b") || c.together("c", "d") || c.together("a", "c") || c.together("a", "zed") {
		t.Error("together")
	}
	if m := c.mates("f"); len(m) != 1 || m[0] != "g" {
		t.Errorf("mates: %v", m)
	}
	if len(c.mates("h")) != 0 || len(c.mates("zed")) != 0 {
		t.Error("mates of one alone in its turn, or of one not planned")
	}
	// Four rooms for nine planned users, and the two apart.
	if n := planRooms(p, 11); n != 6 {
		t.Errorf("rooms: %d", n)
	}
	if _, err := parseRooms("a+,b"); err == nil {
		t.Error("an empty name was taken")
	}
}
