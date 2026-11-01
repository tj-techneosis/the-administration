package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// cmdRoomCheck builds one room with the project's setup hook, checks it the
// way a contract would (seed data, a URL that answers, the logins it says),
// prints what a browser professional would get, and clears it away. It's how
// a session that has just written a setup hook finds out it works, with the
// same permission as every other administration command. Nothing goes in
// the books. With --changes it also builds the before app, the way a release
// check does, so a data set that needs the change's own code fails here and
// not in a contract.
func cmdRoomCheck(args []string) error {
	f, err := parseFlags(args, map[string]bool{"project": true, "data": true, "changes": false, "base": true})
	if err != nil {
		return err
	}
	if len(f.pos) != 0 {
		return usageError{"room-check [--project DIR] [--data NAME] [--changes [--base REF]]"}
	}
	if f.has("base") && !f.has("changes") {
		return usageError{"--base goes with --changes"}
	}
	project, err := projectDir(f.get("project"))
	if err != nil {
		return err
	}
	data, err := dataSet(project, f.get("data"), "")
	if err != nil {
		return err
	}
	if h := houseOf(project); h.Where == "machine" {
		fmt.Printf("The house kept on this machine for %s: %s\n", h.Origin, h.Dir)
	}
	// A session that wrote the hook often can't chmod it; this is the
	// person's own file, and running it is what every room does.
	hook, fixed, err := houseSetup(project, true)
	if err != nil {
		if u, ok := err.(usageError); ok {
			return usageError{u.msg + ". room-check checks the setup hook only; equipment-check NAME checks a piece of equipment"}
		}
		return err
	}
	if fixed {
		fmt.Printf("Made %s executable.\n", shown(project, hook))
	}
	books, err := booksDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(books, "contracts"), 0o700); err != nil {
		return err
	}
	id, err := newID(books)
	if err != nil {
		return err
	}
	dir := filepath.Join(books, "contracts", id)
	name := "room-check"
	pd := filepath.Join(dir, name)
	if err := os.MkdirAll(pd, 0o700); err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	// Killed outright, it can't clean up; this says it's still running, and
	// sweepChecks clears it once it isn't.
	writeCheckPid(dir)
	if bad := badPorts(project); len(bad) > 0 {
		portWarning(bad)
	}
	c := &contract{ID: id, Target: "room check", Project: project, PlacedAt: time.Now().UTC(), PlacedBy: placedBy(), Data: data,
		Professionals: []castItem{{Name: name, Kind: "user", Source: "house"}}}
	ci := &c.Professionals[0]
	if f.has("changes") {
		if c.Changes, err = collectChanges(project, f.get("base"), filepath.Join(dir, contractHandout)); err != nil {
			return err
		}
	}
	if err := writeJSON(filepath.Join(dir, "contract.json"), c); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(pd, "tag"), []byte(newTag(id, name)+"\n"), 0o600); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	s := status{State: "setup"}
	save := func() { _ = writeJSON(filepath.Join(pd, "status.json"), s) }
	fmt.Printf("Building a room with %s (up to %s)...\n", shown(project, hook), setupTimeout())
	t0 := time.Now()
	m, berr := buildRoom(ctx, c, ci, pd, &s, save)
	took := time.Since(t0).Round(time.Second)
	logTail := tail(filepath.Join(pd, "setup.log"), 30)
	var bm *manifest
	var bdone time.Duration
	var bTail string
	before := berr == nil && c.Changes != nil && c.Changes.MergeBase != ""
	if before {
		fmt.Printf("Building the before app (%.8s, without the changes), as a release check does...\n", c.Changes.MergeBase)
		t1 := time.Now()
		bm, berr = beforeRoom(ctx, c, ci, pd, &s, save)
		bdone = time.Since(t1).Round(time.Second)
		if berr != nil {
			logTail = tail(filepath.Join(pd, "setup-before.log"), 30)
			bTail = "the before app's "
			took = bdone
		}
	}
	teardown(c, dir, name, &s)
	if s.Room != "" {
		_ = os.RemoveAll(filepath.Dir(s.Room))
	}
	if berr != nil && ctx.Err() != nil {
		if bTail != "" {
			fmt.Println("Stopped while the before app was building (the room with the changes had come up); it's cleared away.")
		} else {
			fmt.Println("Stopped before the room came up; it's cleared away.")
		}
		return startError{"room check stopped"}
	}
	if berr != nil {
		// The check's folder goes when it returns, the log with it: keep a
		// copy where the person can open it (the before app's, when that's
		// what failed).
		why := berr.Error()
		failed := "setup.log"
		if bTail != "" {
			failed = "setup-before.log"
		}
		if kept := keepCheckLogs(books, id, pd, failed); kept != "" {
			why = strings.ReplaceAll(why, filepath.Join(pd, failed), kept)
			why = strings.ReplaceAll(why, "see "+failed, "see "+kept)
			fmt.Printf("The full %slog is kept at %s.\n", map[bool]string{true: "before app's setup ", false: "setup "}[bTail != ""], kept)
		}
		if took < time.Second {
			fmt.Printf("The room's setup failed at once: %s\n", why)
		} else {
			fmt.Printf("The room didn't come up after %s: %s\n", took, why)
		}
		if logTail != "" {
			fmt.Printf("\nThe end of %ssetup's output:\n%s\n", bTail, logTail)
		}
		if bTail != "" {
			fmt.Printf("\nThe app with the changes came up; the same hook and data set failed on the code before them (ADMINISTRATION_SIDE=before). A data set that seeds what the changes add must skip it where that code isn't there yet.\n")
		}
		return startError{"room check failed"}
	}
	what := "seed data"
	if m.Data == "local" {
		what = "a local copy of data"
	}
	if data != "" {
		what += " (data set " + data + ")"
	}
	fmt.Printf("The room came up in %s and answered at %s, on %s.\n", took, m.URL, what)
	if bm != nil {
		fmt.Printf("The before app came up too, in %s, at %s.\n", bdone, bm.URL)
	} else if c.Changes != nil {
		fmt.Printf("No before app: the changes have no merge base to build it from.\n")
	}
	if len(m.Origins) > 0 {
		fmt.Printf("Other addresses the browser may load: %s\n", strings.Join(m.Origins, ", "))
	}
	roles := make([]string, 0, len(m.Roles))
	for k, r := range m.Roles {
		roles = append(roles, fmt.Sprintf("%s (%s)", k, r.id()))
	}
	sort.Strings(roles)
	if len(roles) > 0 {
		fmt.Printf("Logins it made, for signs_in_as: %s. Passwords not shown.\n", strings.Join(roles, ", "))
	} else {
		fmt.Printf("It made no logins: professionals can only use it signed out (or sign up).\n")
	}
	if s.Leftovers != "" {
		fmt.Printf("After teardown: %s\n", s.Leftovers)
	}
	fmt.Printf("Cleared away. Browser professionals can take contracts here.\n")
	return nil
}

func tail(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// keepCheckLogs copies a failed check's setup logs out of its folder, which
// goes when the check returns, into the books' checks folder (the last
// twenty logs kept), and returns where the one that failed went.
func keepCheckLogs(books, id, pd, failed string) string {
	out := filepath.Join(books, "checks")
	if os.MkdirAll(out, 0o700) != nil {
		return ""
	}
	// The id goes with its contract folder and may be drawn again: the time
	// keeps one check's log from replacing another's.
	stamp := time.Now().Format("20060102-150405")
	kept := ""
	for _, name := range []string{"setup.log", "setup-before.log"} {
		b, err := os.ReadFile(filepath.Join(pd, name))
		if err != nil {
			continue
		}
		dst := filepath.Join(out, stamp+"-"+id+"-"+name)
		if os.WriteFile(dst, b, 0o600) == nil && name == failed {
			kept = dst
		}
	}
	logs, _ := filepath.Glob(filepath.Join(out, "*.log"))
	sort.Sort(sort.Reverse(sort.StringSlice(logs))) // newest first, by the stamp
	for _, l := range logs[min(len(logs), 20):] {
		_ = os.Remove(l)
	}
	return kept
}
