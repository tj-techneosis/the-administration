package main

import (
	"context"
	"os"
	"os/exec"
	"regexp"
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
	var parts []string
	if c.Branch != "" && c.Branch != "main" && c.Branch != "master" {
		on := "On branch " + c.Branch
		if c.PR > 0 {
			on += ", pull request #" + strconv.Itoa(c.PR)
		}
		parts = append(parts, on)
	}
	if c.Ticket != "" {
		parts = append(parts, "ticket "+c.Ticket)
	}
	if c.Release != "" {
		parts = append(parts, "release "+c.Release)
	}
	if c.RequestedBy != "" {
		parts = append(parts, "asked for by "+c.RequestedBy)
	}
	if len(parts) == 0 {
		return ""
	}
	s := strings.Join(parts, "; ")
	return strings.ToUpper(s[:1]) + s[1:]
}

// base is the commit a contract's changes were measured from (the merge
// base of HEAD and --base), "" for one not on changes.
func (c *contract) base() string {
	if c.Changes == nil {
		return ""
	}
	return c.Changes.MergeBase
}

// whereLine says where a contract was placed (office.go): its office and
// the system its rooms ran on, "" for one placed before either was kept.
func (c *contract) whereLine() string {
	w := c.placedWhere()
	switch c.HouseFrom {
	case "":
	case "kept":
		w = joinWhere(w, "house kept on this machine (--house-from)")
	default:
		w = joinWhere(w, "house from "+short(c.HouseFrom)+" (--house-from)")
	}
	if c.ReadOnly {
		w = joinWhere(w, "read-only: critics that only read; the project's hooks didn't run, and nothing was written into it")
	}
	return w
}

// joinWhere adds a part to the where line, capitalised when it's first.
func joinWhere(w, part string) string {
	if w == "" {
		return strings.ToUpper(part[:1]) + part[1:]
	}
	return w + "; " + part
}

func (c *contract) placedWhere() string {
	switch {
	case c.Office != "" && c.OS != "":
		return "Placed at office " + c.Office + " (" + c.OS + ")"
	case c.Office != "":
		return "Placed at office " + c.Office
	case c.OS != "":
		return "Placed on " + c.OS
	}
	return ""
}

// The shapes place's --ticket and --release take: a tracker's id (SHOP-12,
// acme/shop#12) and a version (v0.6.3, 2.1.0-rc1).
var (
	ticketRE  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/#-]{0,63}$`)
	versionRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,39}$`)
)

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
