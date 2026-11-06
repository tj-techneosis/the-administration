package main

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// What a contract was placed on, beside its commit: the branch checked out
// and the pull request open for it, so the books can be joined to any other
// record of the same change (by commit, branch or pull request) long after
// the contract is pruned (books.go, ledgerLine).

// branchOf is the branch the project has checked out, "" on a detached HEAD
// or outside git.
func branchOf(project string) string {
	b, err := git(project, "symbolic-ref", "--short", "-q", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(b)
}

// ghBin is the GitHub CLI: ADMINISTRATION_GH, or gh on PATH.
func ghBin() string {
	if g := os.Getenv("ADMINISTRATION_GH"); g != "" {
		return g
	}
	return "gh"
}

// branchLine says what branch and pull request a contract was placed on,
// "" for the default branch or none.
func (c *contract) branchLine() string {
	if c.Branch == "" || c.Branch == "main" || c.Branch == "master" {
		return ""
	}
	if c.PR > 0 {
		return "On branch " + c.Branch + ", pull request #" + strconv.Itoa(c.PR)
	}
	return "On branch " + c.Branch
}

// prOf is the number of the pull request for a branch, asked of the GitHub
// CLI in the project, 0 when there's none, no gh, or no answer within a few
// seconds: it's for the books, never worth holding a placement up for. The
// default branch has no pull request of its own, so it isn't asked.
func prOf(project, branch string) int {
	if branch == "" || branch == "main" || branch == "master" || strings.HasPrefix(branch, "-") {
		return 0
	}
	bin, err := exec.LookPath(ghBin())
	if err != nil {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "pr", "view", branch, "--json", "number", "--jq", ".number")
	cmd.Dir = project
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1", "NO_COLOR=1")
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || n < 1 {
		return 0
	}
	return n
}
