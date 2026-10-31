package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// cmdResume places a new contract for one professional on what an earlier
// one left undone: the things they wrote down as unreached, and the steps
// of their plan they never answered (equipment lost, out of coins). Same
// target, same project, the same changes and data; the task is only those,
// so they don't spend the new purse on what's already done.
func cmdResume(args []string) error {
	f, err := parseFlags(args, map[string]bool{"coins": true})
	if err != nil {
		return err
	}
	if len(f.pos) != 2 {
		return usageError{"resume ID NAME [--coins 10m]"}
	}
	c, dir, err := loadContract(f.pos[0])
	if err != nil {
		return err
	}
	name := f.pos[1]
	var ci *castItem
	for i := range c.Professionals {
		if c.Professionals[i].Name == name {
			ci = &c.Professionals[i]
		}
	}
	if ci == nil {
		return usageError{fmt.Sprintf("%s isn't on contract %s", name, c.ID)}
	}
	v, err := loadRoster(c.Project)
	if err != nil {
		return err
	}
	onRoster := false
	for _, p := range v.Professionals {
		onRoster = onRoster || p.Name == name
	}
	if !onRoster {
		return usageError{fmt.Sprintf("%s isn't on the roster (a one-off of contract %s); keep them first: administration hire --from %s %s", name, c.ID, c.ID, name)}
	}
	rp, err := os.ReadFile(filepath.Join(dir, name, "report.md"))
	if err != nil {
		return usageError{fmt.Sprintf("%s has no report on contract %s yet; wait for it, or close it", name, c.ID)}
	}
	left := append(reportSection(string(rp), "## Could not reach"), reportSection(string(rp), "## Not covered")...)
	st := readStatus(dir, name)
	coins := f.get("coins")
	if coins == "" {
		coins = (time.Duration(ci.CoinsMin * float64(time.Minute))).Round(time.Second).String()
	}
	pargs := []string{c.Target, "--cast", name, "--project", c.Project, "--coins", coins, "--persona-from", dir}
	switch {
	case len(left) > 0:
		for i, l := range left {
			left[i] = fmt.Sprintf("%d) %s", i+1, leadNum.ReplaceAllString(l, ""))
		}
		task := fmt.Sprintf("Pick up contract %s where you left off. What you did then is done; this is what you couldn't reach, and all of your work now: %s", c.ID, strings.Join(left, "; "))
		pargs = append(pargs, "--only", name+"="+task)
	case st.EquipmentLost != "" || st.State == "failed" || st.State == "out":
		// Lost, broken or out of coins before it wrote anything down:
		// nothing to pick up from, so its whole part again, with its own
		// task if it had one.
		if ci.Task != "" {
			flag := "--brief"
			if ci.TaskOnly {
				flag = "--only"
			}
			pargs = append(pargs, flag, name+"="+ci.Task)
		}
		left = []string{"all of it"}
	default:
		return usageError{fmt.Sprintf("%s left nothing undone on contract %s", name, c.ID)}
	}
	if c.Changes != nil {
		// The same changes: from the merge base the contract was placed on,
		// not wherever its base branch has moved since.
		pargs = append(pargs, "--changes")
		if base := c.Changes.MergeBase; base != "" {
			pargs = append(pargs, "--base", base)
		} else if c.Changes.Base != "" {
			pargs = append(pargs, "--base", c.Changes.Base)
		}
		if !c.Before {
			pargs = append(pargs, "--no-before")
		}
	}
	if c.Data != "" {
		pargs = append(pargs, "--data", c.Data)
	}
	if ci.Model != "" {
		pargs = append(pargs, "--model", ci.Model)
	}
	if c.ShowBrowsers {
		pargs = append(pargs, "--show-browsers")
	}
	fmt.Printf("Resuming %s from contract %s: %s left undone.\n", name, c.ID, map[bool]string{true: "all of their part", false: fmt.Sprint(len(left))}[left[0] == "all of it"])
	return cmdPlace(pargs)
}

// leadNum is a number a professional put on an item themselves ("2. ...").
var leadNum = regexp.MustCompile(`^\d+[.)]\s*`)

// reportSection is the bullets under one heading of a report.
func reportSection(rp, heading string) []string {
	i := strings.Index(rp, heading)
	if i < 0 {
		return nil
	}
	sec := rp[i+len(heading):]
	if j := strings.Index(sec, "\n## "); j >= 0 {
		sec = sec[:j]
	}
	var out []string
	for _, l := range strings.Split(sec, "\n") {
		if t, ok := strings.CutPrefix(l, "- "); ok && strings.TrimSpace(t) != "" {
			out = append(out, strings.TrimSpace(t))
		}
	}
	return out
}
