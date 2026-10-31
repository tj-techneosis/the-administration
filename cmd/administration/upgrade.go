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

// cmdUpgrade brings the person's copy of the Administration up to date and
// installs from it. The skill reads its instructions from the binary
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
		return fmt.Errorf("no copy of the Administration at %s to upgrade from (set ADMINISTRATION_SRC to yours)", src)
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
