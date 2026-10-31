package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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

// maxRoomTmp is the longest a room's tmp folder may be: Playwright puts its
// socket inside, with about 20 characters more, and refused 86 in all.
const maxRoomTmp = 60

// roomsDir is where professionals work. It must be outside the home folder,
// so a single "nothing under ~" rule covers everything a professional must
// not reach (the Wall, in the README).
func roomsDir() (string, error) {
	d := os.Getenv("ADMINISTRATION_ROOMS")
	if d == "" {
		if runtime.GOOS == "darwin" {
			d = "/Users/Shared/administration"
		} else {
			d = fmt.Sprintf("/var/tmp/administration-%d", os.Getuid())
		}
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
