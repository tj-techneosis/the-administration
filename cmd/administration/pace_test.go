package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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
	// Heavy by memory, with no build hook: said, with what was measured.
	recordCost(c, "after", sideCost{PeakMB: 2048, Secs: 30})
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
	mi := filepath.Join(t.TempDir(), "meminfo")
	// 16 GB, 1 GB available: under the reserve.
	if err := os.WriteFile(mi, []byte("MemTotal: 16777216 kB\nMemAvailable: 1048576 kB\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ADMINISTRATION_MEMINFO", mi)
	c := &contract{Project: t.TempDir(), Key: "k"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// No room is at work, so nothing will free memory: it goes, rather
	// than wait for itself.
	if err := admitByMemory(ctx, c, "after", nil); err != nil {
		t.Fatal(err)
	}
}

func TestTheSetupTurnIsHeldOnceAndLetGo(t *testing.T) {
	rooms := filepath.Join(os.TempDir(), "adm-turn-"+randomSuffix(t))
	t.Cleanup(func() { os.RemoveAll(rooms) })
	t.Setenv("ADMINISTRATION_ROOMS", rooms)
	ctx := context.Background()
	if err := setupTurn(ctx, nil); err != nil {
		t.Fatal(err)
	}
	// The lock is held: another can't take it.
	if _, ok := tryLockFile(filepath.Join(rooms, ".pacing", "turn.lock")); ok {
		t.Fatal("the turn's lock isn't held")
	}
	// Asked again by the same process: kept, not waited for.
	if err := setupTurn(ctx, nil); err != nil {
		t.Fatal(err)
	}
	endSetupTurn()
	unlock, ok := tryLockFile(filepath.Join(rooms, ".pacing", "turn.lock"))
	if !ok {
		t.Fatal("the turn wasn't let go")
	}
	unlock()
	if es, _ := os.ReadDir(filepath.Join(rooms, ".pacing", "line")); len(es) != 0 {
		t.Errorf("tickets left in line: %v", es)
	}
	// A ticket whose process is gone (nobody holds it) is passed over and
	// cleared.
	dead := filepath.Join(rooms, ".pacing", "line", "0000000000000000001-0000001")
	if err := os.WriteFile(dead, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waited := false
	if err := setupTurn(ctx, func(int) { waited = true }); err != nil {
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
