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
	// Its own turn on the machine and its own memory, as the room's app
	// had (a worker keeps the turn from one to the other).
	p, err := paceSetup(ctx, c, "before", s, save)
	if err != nil {
		return nil, err
	}
	defer p.done(false)
	clone := filepath.Join(s.Room, "before-src")
	app := filepath.Join(s.Room, "before")
	// The repository is cloned whole, from its top, as the contract's copy
	// is: a project in a subfolder of it (apps/web) is that subfolder of the
	// clone.
	top, err := git(c.Project, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("the before checkout: %v", err)
	}
	rel, err := filepath.Rel(realPath(top), realPath(c.Project))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		rel = "."
	}
	src := filepath.Join(clone, rel)
	if err := os.MkdirAll(app, 0o700); err != nil {
		return nil, err
	}
	// A clone in the room that borrows the project's objects (--shared):
	// the project's repo is only read, never written, not even a worktree
	// entry.
	// Both through gitCmd, as the contract's copy is made: the clone from the
	// room, the checkout in the clone, no filter run and no LFS fetched.
	for _, step := range []struct {
		dir  string
		args []string
	}{
		{s.Room, []string{"clone", "--quiet", "--shared", "--no-checkout", top, clone}},
		{clone, []string{"-C", clone, "-c", "advice.detachedHead=false", "checkout", "--quiet", "--detach", c.Changes.MergeBase}},
	} {
		cmd := gitCmd(step.dir, step.args...)
		cmd.Env = append(cmd.Env, "GIT_LFS_SKIP_SMUDGE=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("the before checkout (%.8s): %v: %s", c.Changes.MergeBase, err, strings.TrimSpace(string(out)))
		}
	}
	// The hook and the files beside it are the project's current ones (a
	// seed script, a helper): an older checkout has none or old ones.
	if err := os.MkdirAll(src, 0o700); err != nil {
		return nil, err // the subfolder didn't exist yet at the merge base
	}
	_ = os.RemoveAll(filepath.Join(src, ".administration"))
	if out, err := exec.Command("cp", "-a", contractHouse(c), filepath.Join(src, ".administration")).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("the before checkout's .administration: %v: %s", err, strings.TrimSpace(string(out)))
	}
	s.BeforeSrc = src
	save()
	names := roomPorts(c.Project)
	ps, err := freePorts(len(names))
	if err != nil {
		return nil, err
	}
	var kv []string
	for i, n := range names {
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
	cmd := exec.Command(filepath.Join(contractHouse(c), "setup"), src, app)
	cmd.Dir = src
	cmd.Env = append(beforeEnv(roomEnv(c, ci.Name, app, ports, mf, readTag(filepath.Dir(pd), ci.Name)), src), "ADMINISTRATION_SIDE=before")
	// The build hook's build of the merge base, when the house has one:
	// the setup installs it instead of building the app in every room.
	cmd.Env = append(cmd.Env, buildEnv(&status{BuildArtifact: s.BeforeBuildArtifact, BuildCommit: s.BeforeBuildCommit})...)
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
	if err := checkManifest(&m, names, ps, ci.SignsInAs); err != nil {
		return nil, err
	}
	s.BeforeURL = m.URL
	save()
	if err := answers(ctx, m.URL, 60*time.Second); err != nil {
		return nil, err
	}
	p.done(true)
	return &m, nil
}

// beforeBuildContract is the contract as the build hook sees its before
// app: the merge base, with no uncommitted work and no chosen build, so its
// build is kept under the merge base's commit like any other.
func beforeBuildContract(c *contract) *contract {
	bc := *c
	bc.Commit, bc.Work, bc.Build, bc.Checkout = "", "", "", ""
	if c.Changes != nil {
		bc.Commit = c.Changes.MergeBase
	}
	return &bc
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
	pd := filepath.Join(dir, name)
	defer func() {
		// What its setup declared for taking down what it started outside
		// its process group, after the teardown hook, and even when the
		// setup never wrote its manifest.
		if f := cleanupFile(filepath.Join(pd, "room-before.json")); isFile(f) {
			ports, _ := os.ReadFile(filepath.Join(pd, "ports-before"))
			logf, _ := os.OpenFile(filepath.Join(pd, "teardown.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
			app := ""
			if s.Room != "" {
				app = filepath.Join(s.Room, "before")
			}
			env := roomEnv(c, name, app, strings.TrimSpace(string(ports)), filepath.Join(pd, "room-before.json"), readTag(dir, name))
			if s.BeforeSrc != "" {
				env = beforeEnv(env, s.BeforeSrc)
			}
			runCleanup(c, f, env, logf)
			if logf != nil {
				logf.Close()
			}
		}
		if s.BeforeSrc == "" {
			return
		}
		_ = os.RemoveAll(s.BeforeSrc)
		if s.Room != "" {
			_ = os.RemoveAll(filepath.Join(s.Room, "before-src")) // the whole clone, for a subfolder's
		}
	}()
	if s.BeforeSrc == "" {
		return
	}
	hook := filepath.Join(contractHouse(c), "teardown")
	if st, err := os.Stat(hook); err == nil && st.Mode()&0o111 != 0 {
		ports, _ := os.ReadFile(filepath.Join(pd, "ports-before"))
		logf, _ := os.OpenFile(filepath.Join(pd, "teardown.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		ctx, cancel := context.WithTimeout(context.Background(), teardownLimit)
		app := filepath.Join(s.Room, "before")
		cmd := exec.Command(hook, s.BeforeSrc, app)
		cmd.Dir = hookDir(c)
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
