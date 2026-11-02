package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Prune holds a build's lock while it renames the folder aside: one
// waiting for that lock starts the folder anew and locks the file there,
// not the one removed under it.
func TestABuildWaitingOnPruneLocksTheFolderAnew(t *testing.T) {
	d := filepath.Join(t.TempDir(), "build")
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(d, ".lock")
	unprune, ok := tryLockFile(lock)
	if !ok {
		t.Fatal("no lock")
	}
	got := make(chan error, 1)
	var unlock func()
	go func() {
		var err error
		unlock, err = lockWaiting(context.Background(), lock, syscall.LOCK_EX)
		got <- err
	}()
	time.Sleep(200 * time.Millisecond)
	if err := os.Rename(d, d+"-gone"); err != nil {
		t.Fatal(err)
	}
	unprune()
	select {
	case err := <-got:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("still waiting after the folder was removed")
	}
	defer unlock()
	if _, ok := tryLockFile(lock); ok {
		t.Fatal("the lock at the path isn't held: the waiter locked the removed file")
	}
}

// buildProjectT is a project with one commit and a build hook that writes
// what says into its build.
func buildProjectT(t *testing.T, says string) (*contract, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("ADMINISTRATION_HOME", filepath.Join(root, "books"))
	project := filepath.Join(root, "project")
	writeT(t, filepath.Join(project, "f"), "one\n")
	gitT(t, project, "init", "-q")
	gitT(t, project, "add", "-A")
	gitT(t, project, "commit", "-qm", "one")
	house := filepath.Join(root, "house")
	hook := filepath.Join(house, "build")
	setHookT(t, hook, says)
	c := &contract{Project: project, Key: "k", House: house, Commit: gitT(t, project, "rev-parse", "HEAD")[:40]}
	return c, hook
}

func setHookT(t *testing.T, hook, says string) {
	t.Helper()
	writeT(t, hook, "#!/bin/sh\necho "+says+" > \"$2/app\"\n")
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}
}

// A hook changed while a room runs the commit's build: the rebuild waits
// for the room, says so while it waits, and builds with the hook as it is
// once it has the lock, not as it was when it asked.
func TestARebuildWaitsForTheRoomsAndBuildsWithTheHookAsItIsThen(t *testing.T) {
	c, hook := buildProjectT(t, "old")
	a, _, room, err := ensureBuild(context.Background(), c, hook, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := readT(t, filepath.Join(a, "app")); got != "old\n" {
		t.Fatalf("built %q", got)
	}
	setHookT(t, hook, "newer")
	said := make(chan string, 4)
	type result struct {
		a   string
		err error
	}
	got := make(chan result, 1)
	go func() {
		a, _, release, err := ensureBuild(context.Background(), c, hook, func(why string) { said <- why })
		release()
		got <- result{a, err}
	}()
	select {
	case why := <-said:
		if !strings.Contains(why, "rooms still running the build made before") {
			t.Fatalf("waiting says %q", why)
		}
	case r := <-got:
		t.Fatalf("rebuilt under a room still running the old build: %+v", r)
	case <-time.After(5 * time.Second):
		t.Fatal("the rebuild said nothing while it waited")
	}
	// Changed again while it waits: the build is the hook as it is now.
	setHookT(t, hook, "newest")
	room()
	select {
	case r := <-got:
		if r.err != nil {
			t.Fatal(r.err)
		}
		if b := readT(t, filepath.Join(r.a, "app")); b != "newest\n" {
			t.Fatalf("built %q, want the hook as it was when the lock was had", b)
		}
		dir := filepath.Dir(r.a)
		if rec := strings.TrimSpace(readT(t, filepath.Join(dir, "recipe"))); rec != buildRecipe(c, hook) {
			t.Fatalf("recorded recipe %s, want the hook's now (%s)", rec, buildRecipe(c, hook))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the rebuild still waits after the room let go")
	}
}

// Only the hook's own failure is kept for the rooms waiting on it: a room
// whose checkout couldn't be made (its project gone) leaves the next to try.
func TestOnlyTheHooksOwnFailureIsSharedWithTheRoomsWaiting(t *testing.T) {
	c, hook := buildProjectT(t, "x")
	root, err := buildsDir(c)
	if err != nil {
		t.Fatal(err)
	}
	failed := filepath.Join(root, c.Commit, "failed")
	gone := *c
	gone.Project = filepath.Join(t.TempDir(), "gone")
	if _, _, _, err := ensureBuild(context.Background(), &gone, hook, nil); err == nil || !strings.Contains(err.Error(), "is gone") {
		t.Fatalf("err %v, want the project gone", err)
	}
	if _, err := os.Stat(failed); err == nil {
		t.Fatal("a contract's own trouble was kept as the build's failure")
	}
	writeT(t, hook, "#!/bin/sh\nexit 3\n")
	if _, _, _, err := ensureBuild(context.Background(), c, hook, nil); err == nil {
		t.Fatal("a failing hook built")
	}
	if _, err := os.Stat(failed); err != nil {
		t.Fatal("the hook's failure wasn't kept for the rooms waiting on it")
	}
}

// A build hook that isn't executable is said, not passed over.
func TestABuildHookThatIsntExecutableIsSaid(t *testing.T) {
	c, hook := buildProjectT(t, "x")
	if buildHookUnrunnable(c) != "" {
		t.Fatal("an executable hook was called unrunnable")
	}
	if err := os.Chmod(hook, 0o644); err != nil {
		t.Fatal(err)
	}
	if buildHook(c) != "" || !strings.Contains(buildHookUnrunnable(c), "isn't executable") {
		t.Fatalf("hook %q, unrunnable %q", buildHook(c), buildHookUnrunnable(c))
	}
	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	if buildHookUnrunnable(c) != "" {
		t.Fatal("no hook at all was called unrunnable")
	}
}

// A build key with uncommitted work prints its commit short.
func TestAWorkKeyIsPrintedShort(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	if got := short(sha + "-work-abcdef0123"); got != "0123456789-work-abcdef0123" {
		t.Fatalf("short = %q", got)
	}
	if got := short(sha); got != "0123456789" {
		t.Fatalf("short = %q", got)
	}
	if got := short("v1.0-work-x"); got != "v1.0-work-x" {
		t.Fatalf("short = %q", got)
	}
}
