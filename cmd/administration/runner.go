package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
)

// The hook runner. A room's hooks (setup, teardown, reset, the build hook,
// the cleanup lines a setup declares) are the branch's code: an install
// script in a dependency it pulls runs with whatever the hook has. Run as
// the person, that's their sign-ins, their keys and the books. With a hook
// runner configured on the machine, every one of them runs through it
// instead, as another account that has none of that:
//
//	# ~/.config/administration/config.toml
//	[hooks]
//	runner = ["sudo", "-n", "-u", "administration-hooks", "--"]
//	group = "administration-hooks"
//
// runner is a command the hook's own command line is put after; group is a
// group the person and the hook account are both in, which the room's
// folders are given to so the hook can write them and the person can read
// and clear them. The setting is the machine's, never the project's: a
// project's roster.toml is in the branch under review, which could take it
// out, or name a runner that runs as the person.
//
// With a runner, a hook gets:
//   - a copy of the project in the room (room/src), at the contract's
//     commit and work, with the house copied into it, never the person's
//     checkout (in their home, and a postinstall that can write a checkout
//     can write its .git/hooks);
//   - the room's own folders (app, before, papers, home, build), given to
//     the group; its manifest and cleanup file are written in papers/ and
//     read back into the books after;
//   - the environment the hook contract passes, with HOME set to the room's
//     home/ and nothing that names the person's home (HOME, USER, TMPDIR,
//     MISE_*, XDG_*, a PATH entry or any value under it);
//   - no fallback: a runner that can't run a command as another account
//     (sudo refuses, the account is missing, it runs as the person or as
//     root) fails the room before any of the branch's code runs.
//
// What the hook account leaves running is stopped as that account (the
// Administration can't signal another account's processes): a copy of
// this program in the rooms folder clears its processes by their tag.
type hookRunner struct {
	Prefix []string
	Group  string
	gid    int

	pre    sync.Once
	preErr error
}

type machineConfig struct {
	Hooks struct {
		Runner []string `toml:"runner"`
		Group  string   `toml:"group"`
	} `toml:"hooks"`
}

var (
	runnerOnce sync.Once
	runnerNow  *hookRunner
	runnerErr  error
)

// configuredRunner is the machine's hook runner, nil when none is set, or
// why the one set can't be used. Read once per process.
func configuredRunner() (*hookRunner, error) {
	runnerOnce.Do(func() { runnerNow, runnerErr = loadRunner(filepath.Join(configDir(), "config.toml")) })
	return runnerNow, runnerErr
}

func loadRunner(path string) (*hookRunner, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("the machine's config (%s) can't be read: %v", path, err)
	}
	var cfg machineConfig
	if err := toml.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("the machine's config (%s): %v", path, err)
	}
	h := cfg.Hooks
	if len(h.Runner) == 0 && h.Group == "" {
		return nil, nil
	}
	if len(h.Runner) == 0 || strings.TrimSpace(h.Runner[0]) == "" {
		return nil, fmt.Errorf("the machine's config (%s): [hooks] group is set without a runner", path)
	}
	if h.Group == "" {
		return nil, fmt.Errorf("the machine's config (%s): [hooks] runner is set without the group the rooms are shared with", path)
	}
	g, err := user.LookupGroup(h.Group)
	if err != nil {
		return nil, fmt.Errorf("the machine's config (%s): the hooks' group %q: %v", path, h.Group, err)
	}
	gid, _ := strconv.Atoi(g.Gid)
	groups, _ := os.Getgroups()
	if gid != os.Getgid() && !slices.Contains(groups, gid) {
		return nil, fmt.Errorf("the machine's config (%s): this account isn't in the hooks' group %q, so it can't share the rooms with the hook account (a new membership needs a new login)", path, h.Group)
	}
	return &hookRunner{Prefix: h.Runner, Group: h.Group, gid: gid}, nil
}

// roomRunner is the runner for work that runs a room's hooks: nil when
// none is configured, an error when one is configured and can't be used.
func roomRunner() (*hookRunner, error) {
	r, err := configuredRunner()
	if err != nil {
		return nil, refusal{err.Error() + "; no hook was run"}
	}
	if r == nil {
		return nil, nil
	}
	if err := r.preflight(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *hookRunner) String() string { return strings.Join(r.Prefix, " ") }

// preflight runs `id -u` through the runner: it must work, without asking
// anyone anything, and as an account that's neither this one nor root.
func (r *hookRunner) preflight() error {
	r.pre.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		args := append(append([]string{}, r.Prefix[1:]...), "/usr/bin/env", "-i", "PATH=/usr/bin:/bin", "/bin/sh", "-c", "id -u")
		cmd := exec.CommandContext(ctx, r.Prefix[0], args...)
		cmd.Env = runnerOwnEnv()
		cmd.Stdin = nil
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		err := cmd.Run()
		said := oneLine(strings.TrimSpace(errb.String()), 300)
		switch {
		case err != nil:
			if said != "" {
				said = ": " + said
			}
			r.preErr = refusal{fmt.Sprintf("the hook runner (%s) can't run a command (%v%s); no hook was run, and none runs as this account instead", r, err, said)}
		default:
			uid, perr := strconv.Atoi(strings.TrimSpace(out.String()))
			switch {
			case perr != nil:
				r.preErr = refusal{fmt.Sprintf("the hook runner (%s) ran `id -u` and printed %q, not an account's number; no hook was run", r, oneLine(out.String(), 80))}
			case uid == 0:
				r.preErr = refusal{fmt.Sprintf("the hook runner (%s) runs commands as root, and must run them as an account of their own; no hook was run", r)}
			case uid == os.Getuid():
				r.preErr = refusal{fmt.Sprintf("the hook runner (%s) runs commands as this same account, which keeps nothing from the hooks; no hook was run", r)}
			}
		}
	})
	return r.preErr
}

// runnerOwnEnv is the runner's own environment (sudo's, say): enough to
// find it, nothing for the hook, which gets its own on its command line.
func runnerOwnEnv() []string {
	var env []string
	for _, k := range []string{"PATH", "LANG", "LC_ALL"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// hookShim runs the hook in its folder with a umask that leaves what it
// makes writable by the group, so the person can clear the room after.
const hookShim = `umask 002; cd "$1" || exit 126; shift; exec "$@"`

// command is hook with args, run through the runner in dir, with env (the
// hook contract's, cleaned by hookEnv) on its command line: sudo and its
// like clear an environment, and arguments are passed as they are.
func (r *hookRunner) command(hook string, args []string, dir string, env []string) *exec.Cmd {
	a := append([]string{}, r.Prefix[1:]...)
	a = append(a, "/usr/bin/env", "-i")
	a = append(a, env...)
	a = append(a, "/bin/sh", "-c", hookShim, "administration-hook", dir, hook)
	a = append(a, args...)
	cmd := exec.Command(r.Prefix[0], a...)
	cmd.Dir = "/"
	cmd.Env = runnerOwnEnv()
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == tagVar {
			runnerTags.Store(cmd, v)
		}
	}
	return cmd
}

// runnerTags is the tag each command started through the runner carries,
// so a hook stopped for its time limit is stopped as the hook account.
var runnerTags sync.Map

// secretNameRE is a variable name that looks like it holds a secret: none
// of these reaches the hook account, whatever the house's pass_env says.
var secretNameRE = regexp.MustCompile(`(?i)(_TOKEN$|_KEY$|_SECRET$|PASSWORD|_DSN$|OAUTH|^GH_|^GITHUB_|^ANTHROPIC_|^CLAUDE_|^SSH_AUTH_SOCK$)`)

// secretPassEnv is the first name in the house's [setup] pass_env that
// looks like a secret, or "". With a hook runner set, such a name is a room
// failure before any hook runs: the hook account is meant to hold no keys.
func secretPassEnv(project string) string {
	for _, k := range loadProjectConfig(project).Setup.PassEnv {
		if secretNameRE.MatchString(k) {
			return k
		}
	}
	return ""
}

// passEnvRefusal is the refusal for a secret-named pass_env under a runner.
func passEnvRefusal(project string) error {
	if k := secretPassEnv(project); k != "" {
		return refusal{fmt.Sprintf("the house's [setup] pass_env names %s, which looks like a secret, and a hook runner is set: the hook account holds no keys, so it isn't passed; no hook was run", k)}
	}
	return nil
}

// hookEnv is a hook's environment for the hook account: the contract's
// variables, HOME the room's own, and nothing that names this account's
// home or looks like a secret.
func hookEnv(env []string, home string) []string {
	own, _ := os.UserHomeDir()
	drop := map[string]bool{"HOME": true, "USER": true, "LOGNAME": true, "SHELL": true, "TMPDIR": true, "MAIL": true}
	var out []string
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || drop[k] || strings.HasPrefix(k, "MISE_") || strings.HasPrefix(k, "__MISE_") || strings.HasPrefix(k, "XDG_") || secretNameRE.MatchString(k) {
			continue
		}
		if k == "PATH" {
			var keep []string
			for _, p := range filepath.SplitList(v) {
				if p != "" && filepath.IsAbs(p) && (own == "" || !within(p, own)) {
					keep = append(keep, p)
				}
			}
			v = strings.Join(keep, string(filepath.ListSeparator))
		} else if own != "" && strings.Contains(v, own) {
			continue
		}
		out = append(out, k+"="+v)
	}
	if !slices.ContainsFunc(out, func(kv string) bool { return strings.HasPrefix(kv, "PATH=") }) {
		out = append(out, "PATH=/usr/local/bin:/usr/bin:/bin")
	}
	return append(out, "HOME="+home)
}

// setEnv sets k in env, replacing it if it's there.
func setEnv(env []string, k, v string) []string {
	for i, kv := range env {
		if strings.HasPrefix(kv, k+"=") {
			env[i] = k + "=" + v
			return env
		}
	}
	return append(env, k+"="+v)
}

// share gives a folder this account makes to the group: writable (and
// keeping its group for what's made in it) or only passable.
func (r *hookRunner) share(dir string, writable bool) error {
	if err := os.Chown(dir, -1, r.gid); err != nil {
		return err
	}
	mode := os.FileMode(0o710)
	if writable {
		mode = 0o770 | os.ModeSetgid
	}
	return os.Chmod(dir, mode)
}

// shareTree gives a tree this account made (a copy of the project) to the
// group, writable by it. Links are left as they are.
func (r *hookRunner) shareTree(root string) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			return r.share(p, true)
		}
		st, err := d.Info()
		if err != nil {
			return err
		}
		if err := os.Chown(p, -1, r.gid); err != nil {
			return err
		}
		mode := st.Mode().Perm() | 0o060
		if st.Mode()&0o100 != 0 {
			mode |= 0o010
		}
		return os.Chmod(p, mode)
	})
}

// check says whether the hook account can write each of dirs and run
// each of progs, before anything of the branch's runs.
func (r *hookRunner) check(dirs, progs []string) error {
	script := `w=1
for a; do
  case "$a" in
    --x) w=0; continue;;
  esac
  if [ $w = 1 ]; then [ -d "$a" ] && [ -w "$a" ] || { echo "write $a"; exit 1; }
  else [ -x "$a" ] || { echo "run $a"; exit 1; }
  fi
done`
	args := append(append([]string{}, r.Prefix[1:]...), "/usr/bin/env", "-i", "PATH=/usr/bin:/bin", "/bin/sh", "-c", script, "check")
	args = append(append(append(args, dirs...), "--x"), progs...)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.Prefix[0], args...)
	cmd.Env = runnerOwnEnv()
	out, err := cmd.Output()
	if err != nil {
		what := strings.TrimSpace(string(out))
		if what == "" {
			what = err.Error()
		} else {
			what = "can't " + what
		}
		return refusal{fmt.Sprintf("the hook account (through %s) %s; no hook was run. The rooms folder and the room must be passable by the group %q (docs/hook-runner.md)", r, what, r.Group)}
	}
	return nil
}

// reap stops the hook account's processes carrying tag, as that account:
// a copy of this program, in the rooms folder where the account can run
// it, clears them as clearTagged does for this one's.
func (r *hookRunner) reap(tag string) {
	if !validTag(tag) {
		return
	}
	bin, err := reaperBin(r)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	args := append(append([]string{}, r.Prefix[1:]...), "/usr/bin/env", "-i", "PATH=/usr/bin:/bin", bin, "_reap", tag)
	cmd := exec.CommandContext(ctx, r.Prefix[0], args...)
	cmd.Env = runnerOwnEnv()
	_ = cmd.Run()
}

var (
	reaperOnce sync.Once
	reaperPath string
	reaperErr  error
)

// reaperBin is this program, copied (once a run) into the rooms folder's
// .bin, given to the group read and run only.
func reaperBin(r *hookRunner) (string, error) {
	reaperOnce.Do(func() {
		rooms, err := roomsDir()
		if err != nil {
			reaperErr = err
			return
		}
		self, err := os.Executable()
		if err != nil {
			reaperErr = err
			return
		}
		dir := filepath.Join(rooms, ".bin")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			reaperErr = err
			return
		}
		if err := r.share(dir, false); err != nil {
			reaperErr = err
			return
		}
		b, err := os.ReadFile(self)
		if err != nil {
			reaperErr = err
			return
		}
		tmp, err := os.CreateTemp(dir, ".administration-")
		if err != nil {
			reaperErr = err
			return
		}
		_, werr := tmp.Write(b)
		cerr := tmp.Close()
		if werr != nil || cerr != nil {
			os.Remove(tmp.Name())
			reaperErr = errors.Join(werr, cerr)
			return
		}
		_ = os.Chown(tmp.Name(), -1, r.gid)
		_ = os.Chmod(tmp.Name(), 0o750)
		reaperPath = filepath.Join(dir, "administration")
		if err := os.Rename(tmp.Name(), reaperPath); err != nil {
			os.Remove(tmp.Name())
			reaperErr = err
		}
	})
	return reaperPath, reaperErr
}

// cmdReap is `_reap TAG`: run as the hook account, it clears that
// account's processes carrying the tag.
func cmdReap(args []string) error {
	if len(args) != 1 || !validTag(args[0]) {
		return usageError{"_reap TAG"}
	}
	clearTaggedHere(args[0])
	return nil
}

// removeTree removes a room's folder, or anything the hook account may
// have made in: what this account can't remove (a folder the hook account
// made without the group's write) is emptied by the hook account first.
func removeTree(path string) error {
	err := os.RemoveAll(path)
	if err == nil || !isDir(path) {
		return err
	}
	r, rerr := configuredRunner()
	if r == nil || rerr != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	// Each folder's contents, depth first, as the hook account: what's its
	// own goes, what isn't stays for this account.
	script := `find "$1" -depth -mindepth 1 -user "$(id -u)" -exec rm -rf -- {} + 2>/dev/null; find "$1" -depth -mindepth 1 -type d -exec chmod u+rwx,g+rwx {} + 2>/dev/null; exit 0`
	args := append(append([]string{}, r.Prefix[1:]...), "/usr/bin/env", "-i", "PATH=/usr/bin:/bin", "/bin/sh", "-c", script, "remove", path)
	cmd := exec.CommandContext(ctx, r.Prefix[0], args...)
	cmd.Env = runnerOwnEnv()
	_ = cmd.Run()
	return os.RemoveAll(path)
}

// hookRoom is where a room's hooks work through the runner: the folders
// it gets, given to the group, and the copy of the project with the house
// in it. It's made once per room (a room handed on keeps it).
type hookRoom struct {
	room, papers, home, build string
	main                      string // the copy of the project (a subfolder of src for a project in one)
	house                     string // the house, copied into main
}

func hookRoomOf(room string) *hookRoom {
	h := &hookRoom{room: room, papers: filepath.Join(room, "papers"), home: filepath.Join(room, "home"), build: filepath.Join(room, "build")}
	if b, err := os.ReadFile(filepath.Join(room, "hook-main")); err == nil {
		h.main = strings.TrimSpace(string(b))
		h.house = filepath.Join(h.main, ".administration")
	}
	return h
}

// prepareHookRoom makes the room ready for hooks run through r: the rooms
// folder, the contract's and the room's passable by the group, the room's
// own folders writable by it, and the copy of the project at the
// contract's commit and work with the house in it. A contract with no
// commit (outside git) has nothing to copy, and is refused.
func prepareHookRoom(r *hookRunner, c *contract, room string) (*hookRoom, error) {
	h := hookRoomOf(room)
	for _, d := range []string{filepath.Dir(filepath.Dir(room)), filepath.Dir(room), room} {
		if err := r.share(d, false); err != nil {
			return nil, err
		}
	}
	for _, d := range []string{filepath.Join(room, "app"), h.papers, h.home, h.build} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
		if err := r.share(d, true); err != nil {
			return nil, err
		}
	}
	if h.main != "" && isDir(h.main) {
		return h, nil
	}
	if c.Commit == "" {
		return nil, refusal{fmt.Sprintf("the hook runner works from a copy of the project at the contract's commit, and %s recorded none (outside git?); no hook was run", c.Project)}
	}
	commit, work := c.Commit, c.Work
	if c.Build != "" {
		commit, work = c.Build, ""
	}
	src := filepath.Join(room, "src")
	_ = removeTree(src)
	main, err := buildCheckout(c, commit, work, src)
	if err != nil {
		return nil, fmt.Errorf("the hooks' copy of the project: %v", err)
	}
	_ = os.Remove(filepath.Join(src, leftOutFile))
	house := filepath.Join(main, ".administration")
	_ = os.RemoveAll(house)
	if out, err := exec.Command("cp", "-a", contractHouse(c), house).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("the hooks' copy of the house: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if err := r.shareTree(src); err != nil {
		return nil, fmt.Errorf("the hooks' copy of the project: %v", err)
	}
	if err := os.WriteFile(filepath.Join(room, "hook-main"), []byte(main+"\n"), 0o600); err != nil {
		return nil, err
	}
	h.main, h.house = main, house
	return h, nil
}

// env is a hook's environment in the room: the contract's, with the
// project, the house, the manifest and the cleanup file the room's own.
// main is what the hook builds from when it isn't the room's copy (the
// before app's clone, already in the room); mf is the manifest the hook
// writes, in the books, "" for the one env names.
func (h *hookRoom) env(env []string, mf, main string) []string {
	for _, kv := range env {
		// One in the room already (the before app's clone) stays.
		if v, ok := strings.CutPrefix(kv, "ADMINISTRATION_MAIN="); ok && main == "" && within(v, h.room) {
			main = v
		}
	}
	if main == "" {
		main = h.main
	}
	if h.main != "" {
		env = setEnv(env, "ADMINISTRATION_MAIN", main)
		env = setEnv(env, "ADMINISTRATION_PROJECT", h.main)
		env = setEnv(env, "ADMINISTRATION_HOUSE", h.house)
	}
	if mf == "" {
		for _, kv := range env {
			if v, ok := strings.CutPrefix(kv, "ADMINISTRATION_MANIFEST="); ok {
				mf = v
			}
		}
	}
	if mf != "" {
		env = setEnv(env, "ADMINISTRATION_MANIFEST", filepath.Join(h.papers, filepath.Base(mf)))
		env = setEnv(env, "ADMINISTRATION_CLEANUP", filepath.Join(h.papers, filepath.Base(cleanupFile(mf))))
	}
	// The checkout has no .git: git asked for its commit says so, rather
	// than finding a repository above the room.
	env = setEnv(env, "GIT_CEILING_DIRECTORIES", h.room)
	return hookEnv(env, h.home)
}

// hook is the house's hook by name, in the room's copy.
func (h *hookRoom) hook(name string) string {
	if h.house == "" {
		return ""
	}
	return filepath.Join(h.house, name)
}

// papersBack copies what the hook wrote in papers/ (the manifest, the
// cleanup file) into the books, where the rest of the Administration
// reads them. What's there in the books already is replaced, and one the
// hook didn't write is left as it is.
func (h *hookRoom) papersBack(pd string) {
	for _, f := range []string{"room.json", "room-before.json", "cleanup", "cleanup-before"} {
		b, err := os.ReadFile(filepath.Join(h.papers, f))
		if err != nil {
			continue
		}
		_ = os.WriteFile(filepath.Join(pd, f), b, 0o600)
	}
}

// stageBuild copies the build hook's build (in the books) into the room,
// where the hook account can read it, and returns where it is there.
func (h *hookRoom) stageBuild(r *hookRunner, artifact, as string) (string, error) {
	if artifact == "" {
		return "", nil
	}
	dst := filepath.Join(h.build, as)
	_ = removeTree(dst)
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return "", err
	}
	in := filepath.Join(dst, filepath.Base(artifact))
	if out, err := exec.Command("cp", "-a", artifact, in).CombinedOutput(); err != nil {
		return "", fmt.Errorf("the build hook's build, into the room: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if err := r.shareTree(dst); err != nil {
		return "", err
	}
	return in, nil
}

// withBuild is env with ADMINISTRATION_BUILD at the room's copy of it.
func (h *hookRoom) withBuild(r *hookRunner, env []string, artifact, as string) ([]string, error) {
	in, err := h.stageBuild(r, artifact, as)
	if err != nil || in == "" {
		return env, err
	}
	return setEnv(env, "ADMINISTRATION_BUILD", in), nil
}

// startHook starts a room's hook through the runner when there is one,
// else as it always has: as this account, in dir, with env. Either way in
// its own process group.
func startHook(r *hookRunner, hook string, args []string, dir string, env []string) *exec.Cmd {
	var cmd *exec.Cmd
	if r != nil {
		cmd = r.command(hook, args, dir, env)
	} else {
		cmd = exec.Command(hook, args...)
		cmd.Dir = dir
		cmd.Env = env
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd
}

// errNoHookCopy is a room with no copy of the project for the hook runner:
// its setup never ran through it, so it left nothing to tear down.
var errNoHookCopy = errors.New("the room has no copy of the project for the hook runner, so no hook was run in it")

// runnerFor is the runner and the room's hook folders for a hook run in a
// room set up already (teardown, reset, cleanup): none when no runner is
// configured, and an error, so the hook isn't run at all, when one is and
// it can't be used or the room has no copy of the project for it (the
// setup never got that far, or the room was set up before the runner was).
func runnerFor(room string) (*hookRunner, *hookRoom, error) {
	if c, err := configuredRunner(); c == nil && err == nil {
		return nil, nil, nil
	}
	r, err := roomRunner()
	if err != nil {
		return nil, nil, err
	}
	hr := hookRoomOf(room)
	if room == "" || hr.main == "" || !isDir(hr.main) {
		return nil, nil, errNoHookCopy
	}
	return r, hr, nil
}
