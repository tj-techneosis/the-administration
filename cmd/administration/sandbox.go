package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Ubuntu 23.10 and later (24.04 on most servers) let a program build a user
// namespace only when AppArmor says it may, and Chromium's sandbox needs
// one: Playwright's own Chromium has no profile, so every browser user on
// such a machine fails at its first call with "Chromium sandboxing failed!".
// The fix is the machine owner's (a profile for Playwright's Chromium, as
// the stock one for Chrome), so this only says what it is.

const restrictUserns = "/proc/sys/kernel/apparmor_restrict_unprivileged_userns"

// sandboxBlocked says whether this machine keeps Chromium's sandbox from
// starting: AppArmor restricts user namespaces and no profile here names
// Playwright's browsers.
func sandboxBlocked() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	b, err := os.ReadFile(restrictUserns)
	if err != nil || strings.TrimSpace(string(b)) != "1" {
		return false
	}
	profiles, _ := filepath.Glob("/etc/apparmor.d/*")
	for _, p := range profiles {
		if b, err := os.ReadFile(p); err == nil && strings.Contains(string(b), "ms-playwright") {
			return false
		}
	}
	return true
}

// sandboxHint is what to do about it, said once with the error.
const sandboxHint = "this machine's AppArmor keeps Chromium from building its sandbox (" + restrictUserns +
	" is 1, and no profile in /etc/apparmor.d names Playwright's browsers): add one, as the README shows (Checking In, On Ubuntu), then place again"

// browserFailure is a user's browser error, with what to do about it when
// it's the sandbox.
func browserFailure(err string) string {
	if strings.Contains(err, "sandboxing failed") || strings.Contains(err, "No usable sandbox") {
		return err + " (" + sandboxHint + ")"
	}
	return err
}
