package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
)

// booksDir is where contracts are recorded: briefs, settings, statuses,
// reports. It sits in the person's home, out of every room's reach.
func booksDir() (string, error) {
	if d := os.Getenv("ADMINISTRATION_HOME"); d != "" {
		return d, nil
	}
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "administration"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "administration"), nil
}

// adminDir is a project's house, its .administration folder: its roster,
// hooks and professionals. One that's a link is followed to where it really
// is, so a walled Director can be let in to read it. A linked git worktree
// has none of its own (it isn't committed, so a new worktree doesn't get it)
// and shares its main checkout's. A project with none uses the house kept
// on this machine for its origin (house.go). Where there's none at all,
// it's the project's own, to be made.
func adminDir(project string) string {
	if d, ok := adminDirs.Load(project); ok {
		return d.(string)
	}
	d := findAdminDir(project)
	adminDirs.Store(project, d)
	return d
}

var adminDirs sync.Map

// outsideHouse is a project's .administration when it lives outside the
// project, "" when it's the project's own.
func outsideHouse(project string) string {
	if ad := adminDir(project); isDir(ad) && !within(ad, project) {
		return ad
	}
	return ""
}

// contractHouse is a contract's .administration: where it was when the
// contract was placed, while that's still there (the worktree it was on may
// be gone), else where it is now.
func contractHouse(c *contract) string {
	if c.House != "" && isDir(c.House) {
		return c.House
	}
	return adminDir(c.Project)
}

// hookDir is where a contract's hooks run: its project, or its house's
// checkout when the project (a worktree) has been removed since.
func hookDir(c *contract) string {
	if isDir(c.Project) {
		return c.Project
	}
	return filepath.Dir(contractHouse(c))
}

func findAdminDir(project string) string {
	own := filepath.Join(project, ".administration")
	// A linked worktree uses its main checkout's: the hooks, roster and
	// training live there, uncommitted, and a worktree has at most the
	// part that was committed (professionals shared with the team).
	if main := mainCheckout(project); main != "" {
		if d := filepath.Join(main, ".administration"); isDir(d) {
			return realPath(d)
		}
	}
	if st, err := os.Lstat(own); err == nil && st.Mode()&os.ModeSymlink != 0 {
		return realPath(own)
	}
	// A project that can't carry one keeps it on the machine, by its origin
	// remote, so every clone and worktree of it finds the same house. The
	// project's own (or its main checkout's) always comes first.
	if !isDir(own) {
		if kept := keptHouse(project); kept != "" && isDir(kept) {
			return realPath(kept)
		}
	}
	return own
}

// mainCheckout is where a project in a linked git worktree sits in the
// repository's main working tree (the same subfolder, for a project inside
// the worktree), or "" when the project isn't in a linked worktree. Git
// knows the main working tree only as the folder its .git is in, so a
// repository whose git folder is elsewhere (bare, --separate-git-dir) has
// none, and its worktrees keep their own.
func mainCheckout(project string) string {
	if project == "" || !isDir(project) {
		return ""
	}
	out, err := git(project, "rev-parse", "--git-dir", "--git-common-dir", "--show-toplevel")
	if err != nil {
		return ""
	}
	dirs := strings.Split(out, "\n")
	if len(dirs) != 3 {
		return ""
	}
	for i, d := range dirs {
		if !filepath.IsAbs(d) {
			dirs[i] = filepath.Join(project, d)
		}
		dirs[i] = filepath.Clean(dirs[i])
	}
	if dirs[0] == dirs[1] || filepath.Base(dirs[1]) != ".git" {
		return ""
	}
	sub, err := filepath.Rel(realPath(dirs[2]), realPath(project))
	if err != nil || strings.HasPrefix(sub, "..") {
		sub = "."
	}
	return filepath.Join(filepath.Dir(dirs[1]), sub)
}

// realPath is p absolute with every link followed, or as given if it can't be.
func realPath(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		p = a
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// maxRoomTmp is the longest a room's tmp folder may be: Playwright puts its
// socket inside, with about 20 characters more, and refused 86 in all.
const maxRoomTmp = 60

// roomsDir is where professionals work. It must be outside the home folder,
// so a single "nothing under ~" rule covers everything a professional must
// not reach (the Wall, in the README).
// defaultRooms is the rooms folder when ADMINISTRATION_ROOMS doesn't say:
// one per user, in a place every account can make one, so two accounts on
// one machine never meet (the folder must be its owner's, below).
func defaultRooms(goos string, uid int) string {
	if goos == "darwin" {
		return fmt.Sprintf("/Users/Shared/administration-%d", uid)
	}
	return fmt.Sprintf("/var/tmp/administration-%d", uid)
}

func roomsDir() (string, error) {
	d := os.Getenv("ADMINISTRATION_ROOMS")
	if d == "" {
		d = defaultRooms(runtime.GOOS, os.Getuid())
	}
	// Workers and close run elsewhere: a relative path would be another
	// folder for each.
	if a, err := filepath.Abs(d); err == nil {
		d = a
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		return "", err
	}
	// In a shared folder, someone else could plant the rooms folder (or a
	// link to theirs) first. It must be a real folder, and ours.
	st, err := os.Lstat(d)
	if err != nil {
		return "", err
	}
	if !st.IsDir() {
		return "", refusal{fmt.Sprintf("the rooms (%s) must be a folder, not a link", d)}
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok && int(sys.Uid) != os.Getuid() {
		return "", refusal{fmt.Sprintf("the rooms (%s) belong to someone else", d)}
	}
	if err := os.Chmod(d, 0o700); err != nil {
		return "", err
	}
	if err := outsideHome(d); err != nil {
		return "", err
	}
	return d, nil
}

func outsideHome(d string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	rd, err := filepath.EvalSymlinks(d)
	if err != nil {
		return err
	}
	rh, err := filepath.EvalSymlinks(home)
	if err != nil {
		rh = home
	}
	if within(rd, rh) || within(rh, rd) {
		return refusal{fmt.Sprintf("the rooms (%s) must be outside the home folder (%s)", rd, rh)}
	}
	return nil
}

// within reports whether p is base or inside it.
func within(p, base string) bool {
	rel, err := filepath.Rel(base, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

// projectDir is the project a contract is placed in: --project, else the git
// top level of the working directory, else the working directory.
func projectDir(flag string) (string, error) {
	if flag != "" {
		return filepath.Abs(flag)
	}
	if out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output(); err == nil {
		return strings.TrimSpace(string(out)), nil
	}
	return os.Getwd()
}

func claudeBin() string {
	if b := os.Getenv("ADMINISTRATION_CLAUDE"); b != "" {
		return b
	}
	return "claude"
}
