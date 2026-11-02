package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// The build hook (docs/design.md, The build hook): a project that lets the
// Administration build what its rooms install has .administration/build.
// It's run as the person, outside every wall, like setup, once per commit:
//
//	build <commit> <out>     in a clean checkout at the commit
//
// with the house's env/ folder (the untracked config a checkout lacks: a
// .env, a local.properties) copied in first, at the same paths. It puts
// what it built in <out> and prints its path as its last line (or prints
// nothing, and <out> is the build). The result is kept per commit in the
// books, so every room of a contract, and the next contract on the same
// commit, installs the same thing; equipment's setup gets it as
// ADMINISTRATION_BUILD, and the commit as ADMINISTRATION_BUILD_COMMIT.

// buildHook is the house's build hook, "" when it has none.
func buildHook(c *contract) string {
	hook := filepath.Join(contractHouse(c), "build")
	if st, err := os.Stat(hook); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
		return hook
	}
	return ""
}

// buildHookUnrunnable says why the house's build hook can't be run when
// there is one that buildHook passes over (a file with no execute bit, as a
// session that wrote it often leaves it), "" otherwise. Rooms would
// otherwise install with no build and nothing said.
func buildHookUnrunnable(c *contract) string {
	hook := filepath.Join(contractHouse(c), "build")
	st, err := os.Stat(hook)
	if err != nil || buildHook(c) != "" {
		return ""
	}
	if st.IsDir() {
		return fmt.Sprintf("the house's build hook (%s) is a folder, not a program", hook)
	}
	return fmt.Sprintf("the house's build hook (%s) isn't executable, so nothing would build what the rooms install: chmod +x it, or remove it", hook)
}

// buildsDir is where a project's builds are kept, one folder per commit.
func buildsDir(c *contract) (string, error) {
	books, err := booksDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(books, "builds", projectFolder(contractKey(c))), nil
}

// projectFolder is a project's folder name under builds/ and copies/: its
// name, made safe, and a hash of its whole key, so two projects called
// the same don't meet.
func projectFolder(key string) string {
	sum := sha256.Sum256([]byte(key))
	name := strings.Trim(nameSafe.ReplaceAllString(filepath.Base(key), "-"), "-")
	if name == "" {
		name = "project"
	}
	return name + "-" + hex.EncodeToString(sum[:])[:12]
}

var nameSafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func buildTimeout() time.Duration {
	if d, err := time.ParseDuration(os.Getenv("ADMINISTRATION_BUILD_TIMEOUT")); err == nil && d > 0 {
		return d
	}
	return time.Hour
}

// buildCommit is what a contract's build hook builds, as the build is kept:
// the commit chosen with --build, else HEAD's, with the uncommitted work's
// hash when there was some.
func buildCommit(c *contract) string {
	if c.Build != "" {
		return c.Build
	}
	return c.buildKey()
}

// ensureBuild is the build of the contract's commit: from the cache, or
// built now. Several rooms asking at once build it once; the others wait.
// The build's lock is held shared until release is called: a room holds
// it for as long as it installs or runs the build, so prune doesn't
// remove it and a build of the same commit made another way waits for it.
// A build that failed while this one waited for it is that failure, not
// run again. waiting, when it's given, is told once each time this has to
// wait for the lock, and why, so a room's status says what it waits for.
func ensureBuild(ctx context.Context, c *contract, hook string, waiting func(string)) (artifact, sha string, release func(), err error) {
	release = func() {}
	sha = buildCommit(c)
	if sha == "" {
		return "", "", release, errors.New("the house has a build hook, and the project has no commit to build")
	}
	root, err := buildsDir(c)
	if err != nil {
		return "", "", release, err
	}
	dir := filepath.Join(root, sha)
	lock := filepath.Join(dir, ".lock")
	asked := time.Now()
	say := func(why string) func() {
		if waiting == nil {
			return nil
		}
		return func() { waiting(why) }
	}
	for {
		// Read again each time round: the hook or env/ can change while
		// this waits.
		recipe := buildRecipe(c, hook)
		l, err := lockWaitingSaying(ctx, lock, syscall.LOCK_SH, say("waiting for the build of "+short(sha)+", which another room is making"))
		if err != nil {
			return "", "", release, err
		}
		if a, ok := builtAlready(dir, recipe); ok {
			touchBuild(dir)
			return a, sha, l, nil
		}
		l()
		if err := failedSince(dir, recipe, asked); err != nil {
			return "", "", release, err
		}
		again, err := buildLocked(ctx, c, hook, sha, dir, lock, recipe, asked, say)
		if err != nil || !again {
			return "", "", release, err
		}
		// Built (by this one or another): take it shared, as above.
	}
}

// buildLocked builds the commit into dir under the build's lock, held
// exclusive. again is true when it's there now, or was built while this
// one waited: take it shared.
//
// The exclusive lock waits for every room that holds the commit's build
// shared, which a room does for as long as it runs: a hook or env/ changed
// while rooms run the earlier build is built again once they're done, and
// the room waiting says so in its status.
func buildLocked(ctx context.Context, c *contract, hook, sha, dir, lock, recipe string, asked time.Time, say func(string) func()) (again bool, err error) {
	why := "waiting for the build of " + short(sha) + ", which another room is making"
	if r, err := os.ReadFile(filepath.Join(dir, "recipe")); err == nil && strings.TrimSpace(string(r)) != recipe {
		why = "waiting to build " + short(sha) + " again (the house's build hook or env/ changed): rooms still running the build made before hold it until they're done"
	}
	unlock, err := lockWaitingSaying(ctx, lock, syscall.LOCK_EX, say(why))
	if err != nil {
		return false, err
	}
	defer unlock()
	// The hook or env/ may have changed while this waited: what's built
	// now, and recorded as how, is what's there now. ensureBuild reads the
	// recipe again when it takes the build shared.
	recipe = buildRecipe(c, hook)
	if _, ok := builtAlready(dir, recipe); ok {
		return true, nil // built while this one waited
	}
	if err := failedSince(dir, recipe, asked); err != nil {
		return false, err
	}
	_ = os.Remove(filepath.Join(dir, "recipe"))
	_ = os.Remove(filepath.Join(dir, "failed"))
	if _, err := runBuild(ctx, c, hook, sha, dir); err != nil {
		var hf hookFailed
		if ctx.Err() == nil && errors.As(err, &hf) {
			// For the rooms waiting on it: one failure, not one each. Only
			// the hook's own: a room cut off, or a checkout this contract
			// couldn't make (its project gone, its work changed), is no
			// answer for another room, which tries for itself.
			_ = os.WriteFile(filepath.Join(dir, "failed"), []byte(recipe+"\n"+err.Error()+"\n"), 0o600)
		}
		return false, err
	}
	if err := os.WriteFile(filepath.Join(dir, "recipe"), []byte(recipe+"\n"), 0o600); err != nil {
		return false, err
	}
	return true, nil
}

// failedSince is the error of a build of the commit made by recipe that
// failed since t, nil when there's none: what a room that asked for the
// build while another was making it gets, instead of making it again.
func failedSince(dir, recipe string, t time.Time) error {
	path := filepath.Join(dir, "failed")
	st, err := os.Stat(path)
	if err != nil || st.ModTime().Before(t) {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	r, msg, _ := strings.Cut(strings.TrimSpace(string(b)), "\n")
	if r != recipe || msg == "" {
		return nil
	}
	return errors.New(msg)
}

// buildRecipe names how a commit is built: the hook and the house's env/,
// entry by entry: each one's path, kind and mode, and a file's contents.
// A kept build made another way (the hook changed, env/'s API address did,
// a script there was made executable, a folder was added) is built again,
// not handed out as this one.
func buildRecipe(c *contract, hook string) string {
	h := sha256.New()
	if b, err := os.ReadFile(hook); err == nil {
		h.Write(b)
	}
	env := filepath.Join(contractHouse(c), "env")
	_ = filepath.WalkDir(env, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == env {
			return nil
		}
		rel, _ := filepath.Rel(env, p)
		mode := d.Type()
		if st, err := d.Info(); err == nil {
			mode = st.Mode()
		}
		// What copyHouseEnv copies: the kind, and the permission bits it
		// writes the file with.
		fmt.Fprintf(h, "\x00%s\x00%v\x00", rel, mode.Type()|mode.Perm()|0o600)
		if !d.Type().IsRegular() {
			return nil
		}
		if f, err := os.Open(p); err == nil {
			_, _ = io.Copy(h, f)
			f.Close()
		}
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// builtAlready is a kept build's artifact, if it's there, still exists, and
// was made by recipe.
func builtAlready(dir, recipe string) (string, bool) {
	if r, err := os.ReadFile(filepath.Join(dir, "recipe")); err != nil || strings.TrimSpace(string(r)) != recipe {
		return "", false
	}
	b, err := os.ReadFile(filepath.Join(dir, "artifact"))
	if err != nil {
		return "", false
	}
	a := strings.TrimSpace(string(b))
	if _, err := os.Stat(a); err != nil {
		return "", false
	}
	return a, true
}

// lockWaiting takes a lock on path (how: syscall.LOCK_EX or LOCK_SH),
// waiting for it as long as ctx lets it. A lock file that can't be opened
// is an error, not a wait.
func lockWaiting(ctx context.Context, path string, how int) (func(), error) {
	return lockWaitingSaying(ctx, path, how, nil)
}

// lockWaitingSaying is lockWaiting, calling waiting (when it isn't nil)
// once, the first time the lock is held by another.
func lockWaitingSaying(ctx context.Context, path string, how int, waiting func()) (func(), error) {
	for {
		// Prune may have removed the build's folder, lock and all, while
		// this waited (it renames the folder aside while it holds the
		// lock): start it anew, and hold a lock only on the file that's at
		// path now, not on one removed under it.
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if errors.Is(err, fs.ErrNotExist) {
			continue // removed between the two
		}
		if err != nil {
			return nil, fmt.Errorf("the build's lock: %v", err)
		}
		if syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB) == nil {
			held, err1 := f.Stat()
			now, err2 := os.Stat(path)
			if err1 == nil && err2 == nil && os.SameFile(held, now) {
				return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
			}
			f.Close()
			continue
		}
		f.Close()
		if waiting != nil {
			waiting()
			waiting = nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// hookFailed is a build that failed in the hook itself (it failed, took too
// long, or printed no build): what every room waiting on it would get too.
type hookFailed struct{ error }

func (e hookFailed) Unwrap() error { return e.error }

// runBuild builds sha into dir/out from a clean checkout in dir/src, which
// goes once it's done: what the hook keeps is what it put in out.
func runBuild(ctx context.Context, c *contract, hook, sha, dir string) (string, error) {
	src, out := filepath.Join(dir, "src"), filepath.Join(dir, "out")
	_ = os.RemoveAll(src)
	_ = os.RemoveAll(out)
	if err := os.MkdirAll(out, 0o700); err != nil {
		return "", err
	}
	if !isDir(c.Project) {
		return "", fmt.Errorf("the project (%s) is gone, so %s can't be built", c.Project, short(sha))
	}
	commit, uncommittedWork := c.Commit, c.Work
	if c.Build != "" {
		commit, uncommittedWork = c.Build, ""
	}
	work, err := buildCheckout(c, commit, uncommittedWork, src)
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(src)
	if err := copyHouseEnv(filepath.Join(contractHouse(c), "env"), work); err != nil {
		return "", err
	}
	logPath := filepath.Join(dir, "build.log")
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	defer logf.Close()
	// The copy's word on what it left out is for its readers, not the
	// build's checkout; the build's log says it instead.
	if left := leftOut(src); len(left) > 0 {
		fmt.Fprintf(logf, "administration: uncommitted files over %d MB aren't in this checkout: %s\n", workHashWhole>>20, strings.Join(left, ", "))
		_ = os.Remove(filepath.Join(src, leftOutFile))
	}
	bctx, cancel := context.WithTimeout(ctx, buildTimeout())
	defer cancel()
	var stdout bytes.Buffer
	cmd := exec.Command(hook, commit, out)
	cmd.Dir = work
	cmd.Env = append(setupEnv(c.Project),
		"ADMINISTRATION_PROJECT="+c.Project,
		"ADMINISTRATION_HOUSE="+contractHouse(c),
		"ADMINISTRATION_SRC="+work,
		"ADMINISTRATION_BUILD_COMMIT="+commit,
		"ADMINISTRATION_BUILD_OUT="+out,
		// The checkout has no .git: a script that asks git for its commit
		// gets an error, not the repository the books happen to be in.
		// ADMINISTRATION_BUILD_COMMIT is the commit.
		"GIT_CEILING_DIRECTORIES="+dir,
	)
	if uncommittedWork != "" {
		// The checkout has the uncommitted work on top: a build that
		// stamps its commit can say so.
		cmd.Env = append(cmd.Env, "ADMINISTRATION_BUILD_WORK="+uncommittedWork)
	}
	cmd.Stdout, cmd.Stderr = io.MultiWriter(&stdout, logf), logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return "", hookFailed{fmt.Errorf("the build hook: %v", err)}
	}
	if err := waitGroup(bctx, cmd); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if bctx.Err() == context.DeadlineExceeded {
			return "", hookFailed{fmt.Errorf("the build of %s took longer than %s (ADMINISTRATION_BUILD_TIMEOUT); see %s", short(sha), buildTimeout(), logPath)}
		}
		return "", hookFailed{fmt.Errorf("the build of %s failed (%v); see %s", short(sha), err, logPath)}
	}
	artifact := out
	if ls := lines(stdout.String()); len(ls) > 0 {
		artifact = ls[len(ls)-1]
		if !filepath.IsAbs(artifact) {
			artifact = filepath.Join(out, artifact)
		}
	}
	artifact = filepath.Clean(artifact)
	if !within(artifact, out) && artifact != out {
		return "", hookFailed{fmt.Errorf("the build hook printed %s, which isn't in its out folder (%s): copy what it built there, as the checkout goes once it's done; see %s", artifact, out, logPath)}
	}
	if _, err := os.Stat(artifact); err != nil {
		return "", hookFailed{fmt.Errorf("the build hook printed %s, and there's nothing there; see %s", artifact, logPath)}
	}
	// In out with every link followed too: a link there (a checkout's,
	// copied with it) to the checkout that goes, or anywhere else, isn't
	// the build kept.
	if real, err := filepath.EvalSymlinks(artifact); err != nil || !within(real, realPath(out)) {
		return "", hookFailed{fmt.Errorf("the build hook printed %s, which is a link out of its out folder (%s): copy what it built there; see %s", artifact, out, logPath)}
	}
	if err := os.WriteFile(filepath.Join(dir, "artifact"), []byte(artifact+"\n"), 0o600); err != nil {
		return "", err
	}
	return artifact, nil
}

// buildCheckout is the checkout a build is made in, at src: with
// uncommitted work, a copy of the contract's own checkout, which has the work
// as it was placed (the live project may have moved on since); otherwise,
// or when there's no such copy, a checkout of the commit made now.
func buildCheckout(c *contract, commit, work, src string) (string, error) {
	if work != "" && c.Checkout != "" {
		if top, err := copyRoot(c, commit, work); err == nil {
			rel, err := filepath.Rel(top, c.Checkout)
			if err == nil && !strings.HasPrefix(rel, "..") && isDir(c.Checkout) {
				if out, err := exec.Command("cp", "-a", top, src).CombinedOutput(); err != nil {
					return "", fmt.Errorf("a copy of the contract's checkout: %v: %s", err, strings.TrimSpace(string(out)))
				}
				return filepath.Join(src, rel), nil
			}
		}
	}
	return makeCheckout(c.Project, commit, work, src)
}

// copyHouseEnv copies the house's env/ folder into a checkout, file by
// file at the same paths: the config a checkout of the commit doesn't have
// because nobody commits it. Links are refused, so what's copied is what's
// in the house, and none in the checkout is written through (the commit
// can have a link where env/ has a file or a folder): it's replaced, as
// copyPath does for the uncommitted work, so the house's files land in the
// checkout and nowhere else.
func copyHouseEnv(from, to string) error {
	if !isDir(from) {
		return nil
	}
	return filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		switch {
		case d.IsDir():
			if rel == "." {
				return nil
			}
			_, _, err := foldersIn(to, strings.Split(rel, string(filepath.Separator)), true)
			return err
		case d.Type()&fs.ModeSymlink != 0:
			return fmt.Errorf("the house's env/%s is a link; copy the file there instead", rel)
		case !d.Type().IsRegular():
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		st, err := d.Info()
		if err != nil {
			return err
		}
		dst, _, err := copyPath(to, rel, true)
		if err != nil {
			return err
		}
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, st.Mode().Perm()|0o600)
		if err != nil {
			return err
		}
		if _, err := out.Write(b); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}

// cmdBuild is `administration build [REF]`: the build hook's build of a
// commit (HEAD by default), from the cache or made now. It's how a person
// tries the hook, or builds ahead of a contract.
func cmdBuild(args []string) error {
	f, err := parseFlags(args, map[string]bool{"project": true})
	if err != nil {
		return err
	}
	if len(f.pos) > 1 {
		return usageError{"build [REF] [--project DIR]"}
	}
	project, err := projectDir(f.get("project"))
	if err != nil {
		return err
	}
	c := &contract{Project: project, Key: projectKey(project), House: outsideHouse(project)}
	hook := buildHook(c)
	if hook == "" {
		return usageError{fmt.Sprintf("%s has no executable %s; see docs/design.md, The build hook", project, houseFile(project, "build"))}
	}
	ref := "HEAD"
	if len(f.pos) == 1 {
		ref = f.pos[0]
	}
	// Never an option to git: "-" starts none of a commit's names.
	if strings.HasPrefix(ref, "-") {
		return usageError{fmt.Sprintf("%s isn't a commit here", ref)}
	}
	sha, err := git(project, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil || sha == "" {
		return usageError{fmt.Sprintf("%s isn't a commit here", ref)}
	}
	c.Commit = sha
	if ref == "HEAD" {
		if w, err := workHash(project); err == nil && w != "" {
			c.Work = w
			fmt.Fprintf(os.Stderr, "administration: the work isn't all committed; building HEAD with it (%s), as a contract placed now would\n", w)
		}
	}
	// Ctrl-C stops the hook too (it runs in its own process group, which
	// the terminal's signal doesn't reach), so no build of it carries on
	// unseen under the next one.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	a, built, release, err := ensureBuild(ctx, c, hook, func(why string) {
		fmt.Fprintf(os.Stderr, "administration: %s\n", why)
	})
	if err != nil {
		if ctx.Err() != nil {
			return usageError{"build interrupted; the build hook was stopped"}
		}
		return err
	}
	release()
	fmt.Printf("%s: %s\n", short(built), a)
	return nil
}

// touchBuild marks a kept build used now, so prune keeps it.
func touchBuild(dir string) {
	now := time.Now()
	_ = os.Chtimes(filepath.Join(dir, "artifact"), now, now)
}
