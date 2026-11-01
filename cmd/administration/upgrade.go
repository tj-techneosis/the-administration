package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// guide is how a Claude session uses the Administration: the plugin's skill
// is only a trigger that runs `administration guide`.
//
//go:embed guide.md
var guide string

// installer is where an install without a checkout gets install.sh, which
// downloads the latest release and checks it against the release's sums.
const installer = "https://raw.githubusercontent.com/tj-techneosis/the-administration/main/install.sh"

// cmdUpgrade brings the Administration up to date: a checkout (the person's
// copy, built from source) is pulled and rebuilt; otherwise the latest
// release is installed. The skill reads its instructions from the binary
// (`administration guide`), so open sessions use the new version from their
// next task; only a change to the plugin itself (its trigger, its commands)
// needs Claude Code to reload it, and this says when.
func cmdUpgrade(args []string) (int, error) {
	f, err := parseFlags(args, map[string]bool{"no-browser": false, "check": false})
	if err != nil {
		return exitError, err
	}
	if len(f.pos) != 0 {
		return exitError, usageError{"upgrade [--no-browser] | upgrade --check"}
	}
	src := os.Getenv("ADMINISTRATION_SRC")
	if src == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return exitError, err
		}
		src = filepath.Join(home, ".local", "share", "the-administration")
	}
	if _, err := os.Stat(filepath.Join(src, "install.sh")); err != nil {
		if os.Getenv("ADMINISTRATION_SRC") != "" {
			return exitError, fmt.Errorf("no copy of the Administration at %s to upgrade from", src)
		}
		if f.has("check") {
			return checkRelease()
		}
		return exitOK, upgradeRelease(f.has("no-browser"))
	}
	// Installed from a release since (an old clone's owner who ran the
	// one-liner): upgrade the release, not the clone it left behind.
	if os.Getenv("ADMINISTRATION_SRC") == "" && installedFromRelease() {
		if f.has("check") {
			return checkRelease()
		}
		return exitOK, upgradeRelease(f.has("no-browser"))
	}
	if f.has("check") {
		return checkCheckout(src)
	}
	before := pluginVersion(src)
	if out, err := exec.Command("git", "-C", src, "remote").Output(); err == nil && strings.TrimSpace(string(out)) != "" {
		pull := exec.Command("git", "-C", src, "pull", "--ff-only", "--quiet")
		pull.Stdout, pull.Stderr = os.Stdout, os.Stderr
		if err := pull.Run(); err != nil {
			return exitError, fmt.Errorf("updating %s: %v", src, err)
		}
	} else {
		fmt.Printf("%s has no remote to pull from; installing it as it is.\n", src)
	}
	var iargs []string
	if f.has("no-browser") {
		iargs = append(iargs, "--no-browser")
	}
	install := exec.Command(filepath.Join(src, "install.sh"), iargs...)
	install.Stdout, install.Stderr = os.Stdout, os.Stderr
	if err := install.Run(); err != nil {
		return exitError, fmt.Errorf("install.sh: %v", err)
	}
	after := pluginVersion(src)
	if loaded := loadedPluginVersion(); after != "" && (after != loaded || after != before) {
		fmt.Printf("\nThe plugin changed too (%s). In Claude Code: /plugin marketplace update, update the-administration, then /reload-plugins in each open session.\n", after)
	} else {
		fmt.Printf("\nOpen Claude sessions use it from their next task; nothing to reload.\n")
	}
	return exitOK, nil
}

// upgradeRelease runs the installer as a person would, piped into sh, so it
// fetches and checks the latest release.
func upgradeRelease(noBrowser bool) error {
	// The installer that came with this release, as the one-liner runs it
	// (on stdin, so it downloads the latest): what's on main today may not
	// be what this install trusts.
	if bundled := releaseInstaller(); bundled != "" && os.Getenv("ADMINISTRATION_INSTALLER") == "" {
		f, err := os.Open(bundled)
		if err != nil {
			return err
		}
		defer f.Close()
		args := []string{"-s", "--"}
		if noBrowser {
			args = append(args, "--no-browser")
		}
		install := exec.Command("sh", args...)
		install.Stdin, install.Stdout, install.Stderr = f, os.Stdout, os.Stderr
		if err := install.Run(); err != nil {
			return fmt.Errorf("installing the latest release: %v", err)
		}
		fmt.Printf("\nOpen Claude sessions use it from their next task. If the plugin itself changed, in Claude Code: /plugin marketplace update, then /reload-plugins.\n")
		return nil
	}
	url := installer
	if v := os.Getenv("ADMINISTRATION_INSTALLER"); v != "" {
		url = v
	}
	// Fetched whole first: piped, a failed download would be an empty
	// script that sh runs happily.
	script := `set -e; s=$(curl -fsSL --connect-timeout 15 --max-time 120 "$1"); printf '%s\n' "$s" | sh -s -- $2`
	flag := ""
	if noBrowser {
		flag = "--no-browser"
	}
	install := exec.Command("sh", "-c", script, "sh", url, flag)
	install.Stdout, install.Stderr = os.Stdout, os.Stderr
	if err := install.Run(); err != nil {
		return fmt.Errorf("installing the latest release: %v", err)
	}
	fmt.Printf("\nOpen Claude sessions use it from their next task. If the plugin itself changed, in Claude Code: /plugin marketplace update, then /reload-plugins.\n")
	return nil
}

// installedFromRelease: this binary is one install.sh unpacked from a
// release (its version has a folder under the books' releases).
func installedFromRelease() bool {
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		home, _ := os.UserHomeDir()
		data = filepath.Join(home, ".local", "share")
	}
	if !strings.HasPrefix(version, "v") {
		return false
	}
	st, err := os.Stat(filepath.Join(data, "administration", "releases", version))
	return err == nil && st.IsDir()
}

func pluginVersion(src string) string {
	var p struct {
		Version string `json:"version"`
	}
	b, err := os.ReadFile(filepath.Join(src, "plugin", ".claude-plugin", "plugin.json"))
	if err != nil || json.Unmarshal(b, &p) != nil {
		return ""
	}
	return p.Version
}

// loadedPluginVersion is the version Claude Code installed, or "" when it
// can't tell.
func loadedPluginVersion() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	var ip struct {
		Plugins map[string][]struct {
			Version string `json:"version"`
		} `json:"plugins"`
	}
	b, err := os.ReadFile(filepath.Join(home, ".claude", "plugins", "installed_plugins.json"))
	if err != nil || json.Unmarshal(b, &ip) != nil {
		return ""
	}
	for k, v := range ip.Plugins {
		if strings.HasPrefix(k, "the-administration@") && len(v) > 0 {
			return v[0].Version
		}
	}
	return ""
}

// readmePath is where this install's README is: the release's, the
// checkout's, one beside the binary, or else the one online. Only a README
// that opens as this one does ("# The Administration") is taken, so
// another project's beside the binary isn't.
func readmePath() string {
	home, _ := os.UserHomeDir()
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		data = filepath.Join(home, ".local", "share")
	}
	dirs := []string{filepath.Join(data, "administration", "releases", version)}
	if src := os.Getenv("ADMINISTRATION_SRC"); src != "" {
		dirs = append(dirs, src)
	}
	// The checkout upgrade uses: ~/.local/share/the-administration.
	dirs = append(dirs, filepath.Join(home, ".local", "share", "the-administration"))
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			dirs = append(dirs, filepath.Dir(filepath.Dir(real)), filepath.Dir(real))
		}
	}
	for _, d := range dirs {
		p := filepath.Join(d, "README.md")
		if b, err := os.ReadFile(p); err == nil && strings.HasPrefix(string(b), "# The Administration") {
			return p
		}
	}
	return "https://github.com/tj-techneosis/the-administration#readme"
}

// releaseInstaller is the install.sh this release was unpacked with, or "".
func releaseInstaller() string {
	if !strings.HasPrefix(version, "v") {
		return ""
	}
	if p := filepath.Join(releasesDir(), version, "install.sh"); isFile(p) {
		return p
	}
	return ""
}

func releasesDir() string {
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		home, _ := os.UserHomeDir()
		data = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(data, "administration", "releases")
}

// checkRelease only asks whether a newer release is out: 0 when there's
// none, 5 when there is, 1 when it can't tell. A scheduled job can watch
// for one this way and ring the person.
func checkRelease() (int, error) {
	base := "https://github.com/tj-techneosis/the-administration/releases"
	if v := os.Getenv("ADMINISTRATION_RELEASES"); v != "" {
		base = strings.TrimRight(v, "/")
	}
	latest, err := fetchText(base + "/latest/download/VERSION")
	if err != nil {
		return exitError, fmt.Errorf("couldn't find out the latest release: %v", err)
	}
	latest = strings.TrimSpace(latest)
	if !releaseRE.MatchString(latest) {
		return exitError, fmt.Errorf("the latest release's VERSION says %q, not a release", oneLine(latest, 40))
	}
	if !releaseRE.MatchString(version) {
		fmt.Printf("The latest release is %s; this build (%s) isn't a release, so there's nothing to compare.\n", latest, version)
		return exitError, nil
	}
	switch {
	case newerRelease(latest, version):
		fmt.Printf("%s is out (this is %s): administration upgrade installs it.\n", latest, version)
		return exitNewer, nil
	case newerRelease(version, latest):
		fmt.Printf("Up to date: this is %s, newer than the latest release (%s).\n", version, latest)
	default:
		fmt.Printf("Up to date: %s is the latest release.\n", latest)
	}
	return exitOK, nil
}

// checkCheckout says whether the person's checkout is behind its upstream.
func checkCheckout(src string) (int, error) {
	gitOut := func(args ...string) (string, error) {
		cmd := exec.Command("git", append([]string{"-C", src}, args...)...)
		var errb strings.Builder
		cmd.Stderr = &errb
		out, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("%s", oneLine(strings.TrimSpace(errb.String())+" ("+err.Error()+")", 300))
		}
		return strings.TrimSpace(string(out)), nil
	}
	remotes, err := gitOut("remote")
	if err != nil {
		return exitError, fmt.Errorf("couldn't read %s as a git checkout: %v", src, err)
	}
	if remotes == "" {
		fmt.Printf("%s has no remote to compare with, so there's no telling whether it's behind.\n", src)
		return exitError, nil
	}
	if _, err := gitOut("fetch", "--quiet"); err != nil {
		return exitError, fmt.Errorf("couldn't fetch %s's upstream: %v", src, err)
	}
	counts, err := gitOut("rev-list", "--left-right", "--count", "HEAD...@{upstream}")
	if err != nil {
		return exitError, fmt.Errorf("couldn't compare %s with its upstream: %v", src, err)
	}
	ahead, behind, _ := strings.Cut(counts, "\t")
	if atoi(behind) > 0 && atoi(ahead) > 0 {
		// upgrade pulls --ff-only, which can't take this.
		fmt.Printf("Your copy (%s) has %s of its own and is %s behind: administration upgrade can't fast-forward it; merge or rebase it yourself.\n",
			src, plural(atoi(ahead), "commit", "commits"), plural(atoi(behind), "commit", "commits"))
		return exitError, nil
	}
	if atoi(behind) > 0 {
		fmt.Printf("Your copy (%s) is %s behind: administration upgrade pulls and rebuilds it.\n", src, plural(atoi(behind), "commit", "commits"))
		return exitNewer, nil
	}
	fmt.Printf("Up to date: your copy (%s) has everything upstream has.\n", src)
	return exitOK, nil
}

// A release, or a pre-release of one (v0.5.0-rc1).
var releaseRE = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)

// newerRelease says whether release a is later than b: by number, and a
// release is later than its own pre-releases (v0.5.0 after v0.5.0-rc1).
func newerRelease(a, b string) bool {
	na, pa, _ := strings.Cut(a[1:], "-")
	nb, pb, _ := strings.Cut(b[1:], "-")
	xa, xb := strings.Split(na, "."), strings.Split(nb, ".")
	for i := range 3 {
		if x, y := atoi(xa[i]), atoi(xb[i]); x != y {
			return x > y
		}
	}
	switch {
	case pa == "" && pb != "":
		return true
	case pa != "" && pb != "":
		return pa > pb
	}
	return false
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// fetchText is a small text file over https, or from a file:// URL (a
// release folder on disk, for testing a release before it's out).
func fetchText(u string) (string, error) {
	if p, ok := strings.CutPrefix(u, "file://"); ok {
		b, err := os.ReadFile(p)
		return string(b), err
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Get(u)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: %s", u, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return string(b), err
}
