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
	hook := filepath.Join(project, ".administration", "setup")
	st, err := os.Stat(hook)
	if err != nil {
		return usageError{fmt.Sprintf("%s has no .administration/setup to check (see the README, A House of Your Own). room-check checks the setup hook only; equipment is proven by a small first contract with a user who works through it", project)}
	}
	if st.Mode()&0o111 == 0 {
		// A session that wrote the hook often can't chmod it; this is the
		// person's own file in their own project, and running it is what
		// every room does.
		if err := os.Chmod(hook, st.Mode()|0o111); err != nil {
			return err
		}
		fmt.Printf("Made %s executable.\n", rel(project, hook))
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
	c := &contract{ID: id, Target: "room check", Project: project, PlacedAt: time.Now().UTC(), PlacedBy: placedBy(), Data: data,
		Professionals: []castItem{{Name: name, Kind: "user", Source: "house"}}}
	ci := &c.Professionals[0]
	if f.has("changes") {
		if c.Changes, err = collectChanges(project, f.get("base"), filepath.Join(dir, "handout")); err != nil {
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
	fmt.Printf("Building a room with %s (up to %s)...\n", rel(project, hook), setupTimeout())
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
	if berr != nil {
		fmt.Printf("The room didn't come up after %s: %v\n", took, berr)
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
