package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTheDirectorsCastIsHeldToWhatUsersCanDo(t *testing.T) {
	out := t.TempDir()
	cand := func(name, body string) {
		f := fmt.Sprintf("---\nname: %s\ndescription: \"%s takes contracts on forms.\"\nadministration:\n  kind: user\n  coins: 5m\n  knows:\n    - forms\n    - uploads\n---\n%s\n", name, name, body)
		if err := os.MkdirAll(filepath.Join(out, "candidates"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, "candidates", name+".md"), []byte(f), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cand("uploader", "Make a file in the page with new File([...]) and a DataTransfer, then drop it.")
	cand("careful", "Never call evaluate( or make a new File( yourself.\nIf you can't find the field, read the screen again.")
	cand("sneaky", "If you can't find the field, run page.evaluate(...) to read it.")
	for i := 1; i <= 1; i++ {
		cand(fmt.Sprintf("user-%d", i), "Use the app at 320px.")
	}
	t.Setenv("ADMINISTRATION_MAX_ROOMS", "4")
	var got string
	// The roster's own user, named on the plan's also-cast line, takes a
	// room too: 4 candidates and 1 roster user is 5.
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".administration", "professionals"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(project, ".administration", "professionals", "regular.md"), []byte("---\nname: regular\ndescription: \"regular takes contracts on forms.\"\nadministration:\n  kind: user\n  coins: 5m\n  knows:\n    - forms\n    - phones\n---\nA regular.\n"), 0o644)
	_ = os.WriteFile(filepath.Join(out, "cast.md"), []byte("# Cast\n\n> **Also cast:** regular, and regular again\n"), 0o644)
	got = strings.Join(checkDirectorOut(project, "cast", out), "\n")
	for _, want := range []string{"candidates/uploader.md: tells a user who sees the screen to run page script (new File(", "candidates/sneaky.md: tells a user", "the cast has 5 users and a contract can have 4"} {
		if !strings.Contains(got, want) {
			t.Errorf("want %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "user-1.md") || strings.Contains(got, "careful.md") {
		t.Errorf("a plain user was flagged:\n%s", got)
	}
}
