package main

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// resetRunnerT forgets the runner read so far, so each test reads its own
// machine config.
func resetRunnerT(t *testing.T) {
	t.Helper()
	reset := func() {
		runnerOnce, runnerNow, runnerErr = sync.Once{}, nil, nil
		reaperOnce, reaperPath, reaperErr = sync.Once{}, "", nil
	}
	reset()
	t.Cleanup(reset)
}

// fakeRunnerT is a runner that records each command line it's given, one
// per line, says the hook account's number is uid when asked `id -u`, and
// otherwise runs what it's given (as this account: the plumbing is what's
// tested here; tests/live/ has the real account). fail makes it refuse
// everything, as sudo -n does when it would need a password.
func fakeRunnerT(t *testing.T, uid string, fail bool) (dir, log string) {
	t.Helper()
	root := t.TempDir()
	cfg := filepath.Join(root, "config")
	log = filepath.Join(root, "runner.log")
	runner := filepath.Join(root, "runner")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + log + "\n"
	if fail {
		script += "echo 'runner: a password is required' >&2\nexit 1\n"
	} else {
		script += "case \"$*\" in *'-c id -u') echo " + uid + "; exit 0;; esac\nshift 2\nexec \"$@\"\n"
	}
	writeT(t, runner, script)
	if err := os.Chmod(runner, 0o755); err != nil {
		t.Fatal(err)
	}
	g, err := user.LookupGroupId(strconv.Itoa(os.Getgid()))
	if err != nil {
		t.Fatal(err)
	}
	writeT(t, filepath.Join(cfg, "config.toml"), "[hooks]\nrunner = [\""+runner+"\", \"--as\", \"hooks\"]\ngroup = \""+g.Name+"\"\n")
	t.Setenv("ADMINISTRATION_CONFIG", cfg)
	t.Setenv("ADMINISTRATION_ROOMS", filepath.Join(root, "rooms"))
	resetRunnerT(t)
	return cfg, log
}

// Unset is today's behaviour: no runner, and a hook started as this
// account, in its folder, with the environment it was given.
func TestNoHookRunnerConfiguredLeavesHooksAsTheyWere(t *testing.T) {
	t.Setenv("ADMINISTRATION_CONFIG", t.TempDir())
	resetRunnerT(t)
	r, err := roomRunner()
	if r != nil || err != nil {
		t.Fatalf("runner %v, err %v with no config", r, err)
	}
	if _, hr, err := runnerFor(t.TempDir()); hr != nil || err != nil {
		t.Fatalf("runnerFor gave %v, %v with no config", hr, err)
	}
	cmd := startHook(nil, "/bin/hook", []string{"a", "b"}, "/some/dir", []string{"K=v"})
	if cmd.Path != "/bin/hook" || strings.Join(cmd.Args, " ") != "/bin/hook a b" || cmd.Dir != "/some/dir" || strings.Join(cmd.Env, " ") != "K=v" {
		t.Fatalf("started %s %v in %s with %v", cmd.Path, cmd.Args, cmd.Dir, cmd.Env)
	}
	// A [hooks] table with nothing in it is none too.
	cfg := t.TempDir()
	writeT(t, filepath.Join(cfg, "config.toml"), "[hooks]\n")
	t.Setenv("ADMINISTRATION_CONFIG", cfg)
	resetRunnerT(t)
	if r, err := configuredRunner(); r != nil || err != nil {
		t.Fatalf("an empty [hooks] gave %v, %v", r, err)
	}
}

// A config that names a runner but can't be used is said, and never taken
// as none: a hook isn't run as this account because the runner was mistyped.
func TestAHookRunnerMisconfiguredIsRefusedNotIgnored(t *testing.T) {
	g, _ := user.LookupGroupId(strconv.Itoa(os.Getgid()))
	for name, body := range map[string]string{
		"no group":      "[hooks]\nrunner = [\"sudo\", \"-n\", \"-u\", \"hooks\", \"--\"]\n",
		"no runner":     "[hooks]\ngroup = \"" + g.Name + "\"\n",
		"no such group": "[hooks]\nrunner = [\"sudo\"]\ngroup = \"no-such-group-here\"\n",
		"not toml":      "[hooks\nrunner = ",
	} {
		t.Run(name, func(t *testing.T) {
			cfg := t.TempDir()
			writeT(t, filepath.Join(cfg, "config.toml"), body)
			t.Setenv("ADMINISTRATION_CONFIG", cfg)
			resetRunnerT(t)
			r, err := roomRunner()
			if err == nil || r != nil {
				t.Fatalf("roomRunner gave %v, %v", r, err)
			}
			if !strings.Contains(err.Error(), "no hook was run") {
				t.Fatalf("refused with %q", err)
			}
			if _, _, err := runnerFor(t.TempDir()); err == nil {
				t.Fatal("teardown would run the hook as this account")
			}
		})
	}
}

// The runner is tried before anything of the branch's runs: refused (a
// password wanted), running as this account, or as root, it's refused.
func TestABrokenHookRunnerRefusesBeforeAnyHookRuns(t *testing.T) {
	cases := map[string]struct {
		uid  string
		fail bool
		says string
	}{
		"refuses": {"", true, "a password is required"},
		"as me":   {strconv.Itoa(os.Getuid()), false, "this same account"},
		"as root": {"0", false, "as root"},
		"garbled": {"hello", false, "not an account's number"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, log := fakeRunnerT(t, tc.uid, tc.fail)
			_, err := roomRunner()
			if err == nil || !strings.Contains(err.Error(), tc.says) || !strings.Contains(err.Error(), "no hook was run") {
				t.Fatalf("roomRunner: %v", err)
			}
			// And a build with it: refused, its hook never started.
			c, hook := buildProjectT(t, "built")
			ran := filepath.Join(t.TempDir(), "ran")
			writeT(t, hook, "#!/bin/sh\ntouch "+ran+"\necho x > \"$2/app\"\n")
			_ = os.Chmod(hook, 0o755)
			if _, err := runBuild(context.Background(), c, hook, c.Commit, filepath.Join(t.TempDir(), "b")); err == nil {
				t.Fatal("built through a broken runner")
			}
			if isFile(ran) {
				t.Fatal("the build hook ran")
			}
			if b, _ := os.ReadFile(log); strings.Contains(string(b), hook) || strings.Contains(string(b), "/build") {
				t.Fatalf("the runner was handed the hook:\n%s", b)
			}
		})
	}
}

// Set, the build hook runs through it: on a copy of the commit in the
// rooms (not the books), with HOME its own and nothing naming this
// account's home, and what it built is copied into the books.
func TestTheBuildHookRunsThroughTheHookRunner(t *testing.T) {
	_, log := fakeRunnerT(t, "4242", false)
	c, hook := buildProjectT(t, "built")
	c.ID = "ab12"
	seen := filepath.Join(t.TempDir(), "seen")
	writeT(t, hook, "#!/bin/sh\n{ pwd; env; umask; } > "+seen+"\necho built > \"$2/app\"\n")
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	t.Setenv("MISE_SHELL", "bash")
	t.Setenv("PATH", filepath.Join(home, ".local", "bin")+":"+os.Getenv("PATH"))
	dir := filepath.Join(t.TempDir(), "b")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	a, err := runBuild(context.Background(), c, hook, c.Commit, dir)
	if err != nil {
		t.Fatal(err)
	}
	if a != filepath.Join(dir, "out") || readT(t, filepath.Join(a, "app")) != "built\n" {
		t.Fatalf("the build is at %s", a)
	}
	runs := readT(t, log)
	if !strings.Contains(runs, "--as hooks /usr/bin/env -i ") || !strings.Contains(runs, "/house/build "+c.Commit) {
		t.Fatalf("the runner wasn't handed the build hook:\n%s", runs)
	}
	got := readT(t, seen)
	rooms := os.Getenv("ADMINISTRATION_ROOMS")
	lines := strings.Split(got, "\n")
	if !within(lines[0], rooms) {
		t.Fatalf("built in %s, not in the rooms (%s)", lines[0], rooms)
	}
	for _, l := range lines[1:] {
		k, v, _ := strings.Cut(l, "=")
		switch {
		case k == "HOME":
			if !within(v, rooms) {
				t.Fatalf("HOME is %s", v)
			}
		case k == "USER" || k == "LOGNAME" || strings.HasPrefix(k, "MISE_"):
			t.Fatalf("the hook got %s", l)
		case k == "ADMINISTRATION_HOUSE" || k == "ADMINISTRATION_PROJECT":
			if !within(v, rooms) {
				t.Fatalf("%s", l)
			}
		case home != "" && strings.Contains(v, home):
			t.Fatalf("the hook got this account's home in %s", l)
		}
	}
	if !strings.Contains(got, "ADMINISTRATION_TAG="+c.ID+"-build-") {
		t.Fatalf("the build carries no tag of its own:\n%s", got)
	}
	if !strings.HasSuffix(strings.TrimSpace(got), "0002") {
		t.Fatalf("the hook's umask isn't 0002:\n%s", got)
	}
	// Its staging folder is gone.
	left, _ := filepath.Glob(filepath.Join(rooms, ".build-*"))
	if len(left) > 0 {
		t.Fatalf("left behind: %v", left)
	}
}

// A room's hooks work from the room's copy of the project, with the house
// in it, never the checkout: the environment points there, the manifest
// and cleanup file are written in the room's papers/, and read back into
// the books.
func TestARoomsHooksGetTheRoomsCopyNotTheCheckout(t *testing.T) {
	fakeRunnerT(t, "4242", false)
	c, _ := buildProjectT(t, "x")
	c.ID = "ab12"
	writeT(t, filepath.Join(c.House, "setup"), "#!/bin/sh\n")
	r, err := roomRunner()
	if err != nil || r == nil {
		t.Fatalf("runner %v, %v", r, err)
	}
	rooms, err := roomsDir()
	if err != nil {
		t.Fatal(err)
	}
	room := filepath.Join(rooms, c.ID, "r1")
	if err := os.MkdirAll(filepath.Join(room, "app"), 0o700); err != nil {
		t.Fatal(err)
	}
	hr, err := prepareHookRoom(r, c, room)
	if err != nil {
		t.Fatal(err)
	}
	if !within(hr.main, room) || readT(t, filepath.Join(hr.main, "f")) != "one\n" || !isFile(hr.hook("setup")) {
		t.Fatalf("the copy: %+v", hr)
	}
	for _, d := range []string{rooms, filepath.Dir(room), room} {
		st, _ := os.Stat(d)
		if st.Mode().Perm() != 0o710 {
			t.Fatalf("%s is %v, want passable by the group only", d, st.Mode())
		}
	}
	for _, d := range []string{filepath.Join(room, "app"), hr.papers, hr.home, hr.main} {
		st, _ := os.Stat(d)
		if st.Mode().Perm() != 0o770 || st.Mode()&os.ModeSetgid == 0 {
			t.Fatalf("%s is %v, want the group's to write", d, st.Mode())
		}
	}
	pd := t.TempDir()
	mf := filepath.Join(pd, "room.json")
	env := hr.env([]string{"ADMINISTRATION_MAIN=" + c.Project, "ADMINISTRATION_PROJECT=" + c.Project, "ADMINISTRATION_MANIFEST=" + mf, "ADMINISTRATION_CLEANUP=" + filepath.Join(pd, "cleanup"), "HOME=/home/someone", "PATH=/usr/bin"}, mf, "")
	want := map[string]string{
		"ADMINISTRATION_MAIN":     hr.main,
		"ADMINISTRATION_PROJECT":  hr.main,
		"ADMINISTRATION_HOUSE":    hr.house,
		"ADMINISTRATION_MANIFEST": filepath.Join(hr.papers, "room.json"),
		"ADMINISTRATION_CLEANUP":  filepath.Join(hr.papers, "cleanup"),
		"HOME":                    hr.home,
	}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if w, ok := want[k]; ok {
			if v != w {
				t.Fatalf("%s=%s, want %s", k, v, w)
			}
			delete(want, k)
		}
	}
	if len(want) > 0 {
		t.Fatalf("not set: %v", want)
	}
	// The before app's clone, in the room already, stays what it builds from.
	before := filepath.Join(room, "before-src")
	env = hr.env([]string{"ADMINISTRATION_MAIN=" + before}, "", "")
	if !strings.Contains(strings.Join(env, "\n"), "ADMINISTRATION_MAIN="+before+"\n") {
		t.Fatalf("the before side's MAIN: %v", env)
	}
	writeT(t, filepath.Join(hr.papers, "room.json"), "{}")
	writeT(t, filepath.Join(hr.papers, "cleanup"), "docker compose down\n")
	hr.papersBack(pd)
	if readT(t, mf) != "{}" || readT(t, filepath.Join(pd, "cleanup")) != "docker compose down\n" {
		t.Fatal("papers not read back into the books")
	}
	// Teardown finds it again from the room alone.
	if _, again, err := runnerFor(room); err != nil || again.main != hr.main {
		t.Fatalf("runnerFor: %v, %v", again, err)
	}
	// A room whose setup never got a copy: nothing to tear down, and no
	// hook run as this account.
	if _, _, err := runnerFor(filepath.Join(rooms, c.ID, "r2")); err != errNoHookCopy {
		t.Fatalf("a room with no copy: %v", err)
	}
}

// A room's cleanup lines (the branch's) go through the runner too, in the
// room's copy; with the runner broken they aren't run at all.
func TestCleanupLinesRunThroughTheHookRunner(t *testing.T) {
	_, log := fakeRunnerT(t, "4242", false)
	c, _ := buildProjectT(t, "x")
	c.ID = "ab12"
	r, _ := roomRunner()
	rooms, _ := roomsDir()
	room := filepath.Join(rooms, c.ID, "r1")
	if err := os.MkdirAll(filepath.Join(room, "app"), 0o700); err != nil {
		t.Fatal(err)
	}
	hr, err := prepareHookRoom(r, c, room)
	if err != nil {
		t.Fatal(err)
	}
	pd := t.TempDir()
	out := filepath.Join(t.TempDir(), "where")
	f := filepath.Join(pd, "cleanup")
	writeT(t, f, "pwd > "+out+"\n")
	runCleanup(c, room, f, []string{"ADMINISTRATION_MANIFEST=" + filepath.Join(pd, "room.json"), "PATH=/usr/bin:/bin"}, nil)
	if strings.TrimSpace(readT(t, out)) != hr.main {
		t.Fatalf("cleanup ran in %s", readT(t, out))
	}
	if !strings.Contains(readT(t, log), "/bin/sh -c pwd > "+out) {
		t.Fatalf("the runner wasn't handed the cleanup:\n%s", readT(t, log))
	}
	// Broken now: the line isn't run, and the file stays for a later try.
	_ = os.Remove(out)
	writeT(t, f, "pwd > "+out+"\n")
	fakeRunnerT(t, "", true)
	runCleanup(c, room, f, nil, nil)
	if isFile(out) || !isFile(f) {
		t.Fatal("ran the cleanup with the runner broken")
	}
}

// Teardown runs the room's copy's teardown through the runner, on the
// room's copy, never the house's own or the checkout; the cleanup the
// setup wrote in papers/ is read back and run the same way.
func TestTeardownRunsThroughTheHookRunner(t *testing.T) {
	_, log := fakeRunnerT(t, "4242", false)
	c, _ := buildProjectT(t, "x")
	c.ID = "ab12"
	c.Professionals = []castItem{{Name: "nadia", Kind: "user"}}
	seen := filepath.Join(t.TempDir(), "seen")
	writeT(t, filepath.Join(c.House, "teardown"), "#!/bin/sh\necho \"$(pwd) $1 $ADMINISTRATION_MAIN $HOME\" >> "+seen+"\n")
	if err := os.Chmod(filepath.Join(c.House, "teardown"), 0o755); err != nil {
		t.Fatal(err)
	}
	r, _ := roomRunner()
	rooms, _ := roomsDir()
	room := filepath.Join(rooms, c.ID, "r1")
	if err := os.MkdirAll(filepath.Join(room, "app"), 0o700); err != nil {
		t.Fatal(err)
	}
	hr, err := prepareHookRoom(r, c, room)
	if err != nil {
		t.Fatal(err)
	}
	// The house's own teardown changes after the room was set up: the
	// room's copy is what runs.
	writeT(t, filepath.Join(c.House, "teardown"), "#!/bin/sh\necho house >> "+seen+"\n")
	writeT(t, filepath.Join(hr.papers, "cleanup"), "echo cleanup >> "+seen+"\n")
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "nadia"), 0o700); err != nil {
		t.Fatal(err)
	}
	s := &status{Room: room}
	teardown(c, dir, "nadia", s)
	got := strings.Split(strings.TrimSpace(readT(t, seen)), "\n")
	want := hr.main + " " + hr.main + " " + hr.main + " " + hr.home
	if len(got) != 2 || got[0] != want || got[1] != "cleanup" {
		t.Fatalf("teardown saw %q, want %q then cleanup", got, want)
	}
	if n := strings.Count(readT(t, log), " administration-hook "); n != 2 {
		t.Fatalf("%d commands through the runner, want the teardown and the cleanup:\n%s", n, readT(t, log))
	}
}
