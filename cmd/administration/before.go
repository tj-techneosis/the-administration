package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// The release check's other half. A contract on changes, with users, gets a
// second app in each user's room: the project as it was before the change
// (the merge base), built by the same setup hook from a clone in the room.
// The user runs the same flows on both and reports what changed, so a new
// break can be told from something that was always so.

// beforeRoom builds the before app next to the room's own. It returns its
// manifest; the clone and the app go with the room at teardown.
func beforeRoom(ctx context.Context, c *contract, ci *castItem, pd string, s *status, save func()) (*manifest, error) {
	if c.Changes == nil || c.Changes.MergeBase == "" {
		return nil, errors.New("no merge base to build the before app from")
	}
	src := filepath.Join(s.Room, "before-src")
	app := filepath.Join(s.Room, "before")
	if err := os.MkdirAll(app, 0o700); err != nil {
		return nil, err
	}
	// A clone in the room that borrows the project's objects (--shared):
	// the project's repo is only read, never written, not even a worktree
	// entry.
	for _, args := range [][]string{
		{"clone", "--quiet", "--shared", "--no-checkout", c.Project, src},
		{"-C", src, "checkout", "--quiet", "--detach", c.Changes.MergeBase},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			return nil, fmt.Errorf("the before checkout (%.8s): %v: %s", c.Changes.MergeBase, err, strings.TrimSpace(string(out)))
		}
	}
	// The hook and the files beside it are the project's current ones (a
	// seed script, a helper): an older checkout has none or old ones.
	_ = os.RemoveAll(filepath.Join(src, ".administration"))
	if out, err := exec.Command("cp", "-a", filepath.Join(c.Project, ".administration"), filepath.Join(src, ".administration")).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("the before checkout's .administration: %v: %s", err, strings.TrimSpace(string(out)))
	}
	s.BeforeSrc = src
	save()
	ps, err := freePorts(len(portNames))
	if err != nil {
		return nil, err
	}
	var kv []string
	for i, n := range portNames {
		kv = append(kv, fmt.Sprintf("%s=%d", n, ps[i]))
	}
	ports := strings.Join(kv, ",")
	s.Ports = append(s.Ports, ps...)
	save()
	_ = os.WriteFile(filepath.Join(pd, "ports-before"), []byte(ports+"\n"), 0o600)
	mf := filepath.Join(pd, "room-before.json")

	logf, err := os.OpenFile(filepath.Join(pd, "setup-before.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	defer logf.Close()
	sctx, cancel := context.WithTimeout(ctx, setupTimeout())
	defer cancel()
	// The hook is the project's current one: an older checkout may not have
	// it, and it's the person's to trust either way.
	cmd := exec.Command(filepath.Join(c.Project, ".administration", "setup"), src, app)
	cmd.Dir = src
	cmd.Env = append(beforeEnv(roomEnv(c, ci.Name, app, ports, mf, readTag(filepath.Dir(pd), ci.Name)), src), "ADMINISTRATION_SIDE=before")
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("before setup: %v", err)
	}
	s.Groups = append(s.Groups, cmd.Process.Pid)
	save()
	if err := waitGroup(sctx, cmd); err != nil {
		if sctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("the before app's setup took longer than %s; see setup-before.log", setupTimeout())
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("the before app's setup failed (%v); see %s", err, filepath.Join(pd, "setup-before.log"))
	}
	b, err := os.ReadFile(mf)
	if err != nil {
		return nil, errors.New("the before app's setup finished without writing $ADMINISTRATION_MANIFEST")
	}
	var m manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("the before app's manifest: %v", err)
	}
	if err := checkManifest(&m, ps, ci.SignsInAs); err != nil {
		return nil, err
	}
	s.BeforeURL = m.URL
	save()
	if err := answers(ctx, m.URL, 60*time.Second); err != nil {
		return nil, err
	}
	return &m, nil
}

// beforeEnv points the hook at the before checkout instead of the project.
func beforeEnv(env []string, src string) []string {
	for i, kv := range env {
		if strings.HasPrefix(kv, "ADMINISTRATION_MAIN=") {
			env[i] = "ADMINISTRATION_MAIN=" + src
		}
	}
	return env
}

// teardownBefore runs the teardown hook on the before app and removes its
// worktree; the room's tag clears whatever it left running.
func teardownBefore(c *contract, dir, name string, s *status) {
	if s.BeforeSrc == "" {
		return
	}
	pd := filepath.Join(dir, name)
	hook := filepath.Join(c.Project, ".administration", "teardown")
	if st, err := os.Stat(hook); err == nil && st.Mode()&0o111 != 0 {
		ports, _ := os.ReadFile(filepath.Join(pd, "ports-before"))
		logf, _ := os.OpenFile(filepath.Join(pd, "teardown.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		ctx, cancel := context.WithTimeout(context.Background(), teardownLimit)
		app := filepath.Join(s.Room, "before")
		cmd := exec.Command(hook, s.BeforeSrc, app)
		cmd.Dir = c.Project
		cmd.Env = beforeEnv(roomEnv(c, name, app, strings.TrimSpace(string(ports)), filepath.Join(pd, "room-before.json"), readTag(dir, name)), s.BeforeSrc)
		cmd.Stdout, cmd.Stderr = logf, logf
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if cmd.Start() == nil {
			_ = waitGroup(ctx, cmd)
		}
		cancel()
		if logf != nil {
			logf.Close()
		}
	}
	_ = os.RemoveAll(s.BeforeSrc)
}

// withBefore is the room's manifest with the before app's addresses added,
// for the gate and the browser's allowed origins.
func withBefore(m, before *manifest) *manifest {
	if before == nil {
		return m
	}
	mm := *m
	mm.Origins = append(append(append([]string{}, m.Origins...), before.URL), before.Origins...)
	return &mm
}
