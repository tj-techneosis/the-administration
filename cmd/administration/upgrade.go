package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
func cmdUpgrade(args []string) error {
	f, err := parseFlags(args, map[string]bool{"no-browser": false})
	if err != nil {
		return err
	}
	if len(f.pos) != 0 {
		return usageError{"upgrade [--no-browser]"}
	}
	src := os.Getenv("ADMINISTRATION_SRC")
	if src == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		src = filepath.Join(home, ".local", "share", "the-administration")
	}
	if _, err := os.Stat(filepath.Join(src, "install.sh")); err != nil {
		if os.Getenv("ADMINISTRATION_SRC") != "" {
			return fmt.Errorf("no copy of the Administration at %s to upgrade from", src)
		}
		return upgradeRelease(f.has("no-browser"))
	}
	// Installed from a release since (an old clone's owner who ran the
	// one-liner): upgrade the release, not the clone it left behind.
	if os.Getenv("ADMINISTRATION_SRC") == "" && installedFromRelease() {
		return upgradeRelease(f.has("no-browser"))
	}
	before := pluginVersion(src)
	if out, err := exec.Command("git", "-C", src, "remote").Output(); err == nil && strings.TrimSpace(string(out)) != "" {
		pull := exec.Command("git", "-C", src, "pull", "--ff-only", "--quiet")
		pull.Stdout, pull.Stderr = os.Stdout, os.Stderr
		if err := pull.Run(); err != nil {
			return fmt.Errorf("updating %s: %v", src, err)
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
		return fmt.Errorf("install.sh: %v", err)
	}
	after := pluginVersion(src)
	if loaded := loadedPluginVersion(); after != "" && (after != loaded || after != before) {
		fmt.Printf("\nThe plugin changed too (%s). In Claude Code: /plugin marketplace update, update the-administration, then /reload-plugins in each open session.\n", after)
	} else {
		fmt.Printf("\nOpen Claude sessions use it from their next task; nothing to reload.\n")
	}
	return nil
}

// upgradeRelease runs the installer as a person would, piped into sh, so it
// fetches and checks the latest release.
func upgradeRelease(noBrowser bool) error {
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
