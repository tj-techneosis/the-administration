package main

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// The project's house is what it sets up for the Administration: setup,
// teardown, roster.toml, professionals/, equipment, handouts. It's the
// project's .administration/ folder, committed with it. A project that
// can't carry one keeps it on the machine instead, under projectsDir() at
// the path of its origin remote (github.com/acme/shop), so every clone and
// worktree of it finds the same house. The project's own always comes
// first.
type projectHouse struct {
	Dir string `json:"dir"`
	// "project" (its .administration/), "machine" (kept for it under
	// projectsDir()), or "none" (Dir is then where one would be made).
	Where string `json:"where"`
	// The origin it was found by, as host/owner/repo.
	Origin string   `json:"origin,omitempty"`
	Looked []string `json:"looked,omitempty"`
}

// projectsDir is where the houses kept on this machine are, one per remote.
func projectsDir() string {
	if d := os.Getenv("ADMINISTRATION_PROJECTS"); d != "" {
		return d
	}
	return filepath.Join(configDir(), "projects")
}

var origins sync.Map // project → its origin as host/owner/repo, or ""

// originOf is the project's origin remote as host/owner/repo, or "" when it
// has none. A worktree's is its repository's.
func originOf(project string) string {
	if v, ok := origins.Load(project); ok {
		return v.(string)
	}
	cmd := exec.Command("git", "remote", "get-url", "origin")
	cmd.Dir = project
	out, err := cmd.Output()
	key := ""
	if err == nil {
		key = remoteKey(strings.TrimSpace(string(out)))
	}
	origins.Store(project, key)
	return key
}

var scpRemote = regexp.MustCompile(`^(?:[^@/]+@)?([^:/]+):(.+)$`)

// remoteKey is a remote URL as host/owner/repo: git@host:owner/repo.git,
// ssh://git@host:22/owner/repo, https://host/owner/repo(.git) all come out
// the same. A remote on this machine (a path, file://) has none.
func remoteKey(remote string) string {
	host, path := "", ""
	if strings.Contains(remote, "://") {
		u, err := url.Parse(remote)
		if err != nil || u.Scheme == "file" {
			return ""
		}
		host, path = u.Hostname(), u.Path
	} else if m := scpRemote.FindStringSubmatch(remote); m != nil && !strings.HasPrefix(remote, "/") && !strings.HasPrefix(remote, ".") {
		host, path = m[1], m[2]
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	host = strings.ToLower(host)
	if host == "" || path == "" {
		return ""
	}
	parts := append([]string{host}, strings.Split(path, "/")...)
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || strings.ContainsAny(p, `\`+"\x00") {
			return ""
		}
	}
	return strings.Join(parts, "/")
}

// houseOf finds the project's house: its .administration/, else the one
// kept on this machine for its origin.
func houseOf(project string) projectHouse {
	own := filepath.Join(project, ".administration")
	h := projectHouse{Dir: own, Where: "none", Looked: []string{own}}
	if isDir(own) {
		h.Where, h.Looked = "project", nil
		return h
	}
	if project == "" {
		return h
	}
	if h.Origin = originOf(project); h.Origin != "" {
		kept := filepath.Join(projectsDir(), filepath.FromSlash(h.Origin))
		if isDir(kept) {
			return projectHouse{Dir: kept, Where: "machine", Origin: h.Origin}
		}
		h.Looked = append(h.Looked, kept)
	}
	return h
}

// houseDir is the folder the project's house is in (or would be).
func houseDir(project string) string { return houseOf(project).Dir }

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// houseFile names a file in the project's house for a person to read:
// .administration/setup when it's the project's own, its full path when it's
// kept on the machine.
func houseFile(project string, parts ...string) string {
	h := houseOf(project)
	if h.Where == "machine" {
		return filepath.Join(append([]string{h.Dir}, parts...)...)
	}
	return filepath.Join(append([]string{".administration"}, parts...)...)
}

// noHouse says where the project's house was looked for and not found.
func (h projectHouse) noHouse(project string) string {
	if h.Origin == "" {
		return fmt.Sprintf("%s has no .administration/setup: no %s, and no origin remote to find a house kept on this machine by (in %s)", project, h.Looked[0], projectsDir())
	}
	return fmt.Sprintf("%s has no .administration/setup: no %s, and no house kept on this machine for %s at %s", project, h.Looked[0], h.Origin, h.Looked[1])
}

// houseSetup is the project's executable setup hook, or why there isn't one.
// A setup that isn't executable is made so when fix is set (a session that
// wrote it often can't chmod it, and running it is what every room does).
func houseSetup(project string, fix bool) (string, bool, error) {
	h := houseOf(project)
	if h.Where == "none" {
		return "", false, usageError{h.noHouse(project) + " (see the README, A House of Your Own)"}
	}
	hook := filepath.Join(h.Dir, "setup")
	st, err := os.Stat(hook)
	missing := err != nil || st.IsDir()
	if !missing && st.Mode()&0o111 != 0 {
		return hook, false, nil
	}
	if missing || !fix {
		what := "no executable"
		if missing && fix {
			what = "no"
		}
		return "", false, usageError{fmt.Sprintf("%s has %s %s (see the README, A House of Your Own)", project, what, houseFile(project, "setup"))}
	}
	if err := os.Chmod(hook, st.Mode()|0o111); err != nil {
		return "", false, err
	}
	return hook, true, nil
}

// housePath is a path roster.toml names (a handout, equipment's programs, a
// critic's file), made absolute by one rule: a path that starts with
// .administration/ is in the house, wherever the house is; any other is the
// project's, or the house's when the project has nothing there. So a roster
// written for the project's own .administration/ works unchanged when the
// house is kept on the machine, and paths written from the house itself
// (equipment/phone/driver) work in both.
func housePath(project, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	clean := filepath.Clean(p)
	dir := houseDir(project)
	if rest, ok := strings.CutPrefix(filepath.ToSlash(clean), ".administration/"); ok {
		return filepath.Join(dir, filepath.FromSlash(rest))
	}
	abs := filepath.Join(project, clean)
	if _, err := os.Lstat(abs); err != nil {
		if alt := filepath.Join(dir, clean); within(alt, dir) {
			if _, err := os.Lstat(alt); err == nil {
				return alt
			}
		}
	}
	return abs
}

// inProject says whether p is in the project or in its house.
func inProject(project, p string) bool {
	return within(p, project) || within(p, houseDir(project))
}
