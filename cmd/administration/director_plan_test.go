package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestAnEquipmentPlanWithMadeUpToolsGoesBackForRepair(t *testing.T) {
	p := filepath.Join(t.TempDir(), "equipment.md")
	plan := "## w\n```toml\n[equipment.w]\nabout = \"the window\"\nserver = \"playwright\"\ntools = [\"browser_click\", \"screenshot\", \"drop_files\"]\n```\n"
	if err := os.WriteFile(p, []byte(plan), 0o600); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(checkEquipmentPlan(p, nil), "\n")
	for _, want := range []string{`"screenshot" isn't a browser tool`, `"drop_files" isn't a browser tool`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, `"browser_click" isn't`) {
		t.Errorf("a real tool was refused:\n%s", got)
	}
	if checkEquipmentPlan(filepath.Join(t.TempDir(), "none.md"), nil) != nil {
		t.Error("no plan is no problem")
	}
}

func TestTheProjectNoteNamesItsEquipment(t *testing.T) {
	project := t.TempDir()
	adm := filepath.Join(project, ".administration")
	if err := os.MkdirAll(adm, 0o755); err != nil {
		t.Fatal(err)
	}
	roster := "[equipment.window]\nabout = \"the desktop window\"\nserver = \"playwright\"\ntools = [\"browser_click\"]\n"
	if err := os.WriteFile(filepath.Join(adm, "roster.toml"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	projectNote(&b, project)
	got := b.String()
	for _, want := range []string{"no .administration/setup, so no user has the web browser", "- window: the desktop window"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestADeclaredProgramIsMadeExecutableButNotALinkOut(t *testing.T) {
	project, outside := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "tool"), []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := projectProgram(project, "tool"); err != nil {
		t.Fatalf("a plain file in the project was refused: %v", err)
	}
	if st, _ := os.Stat(filepath.Join(project, "tool")); st.Mode()&0o100 == 0 {
		t.Error("not made executable")
	}
	if err := os.WriteFile(filepath.Join(outside, "theirs"), []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "theirs"), filepath.Join(project, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := projectProgram(project, "link"); err == nil {
		t.Error("a link out of the project was accepted")
	}
	if st, _ := os.Stat(filepath.Join(outside, "theirs")); st.Mode()&0o111 != 0 {
		t.Error("a file outside the project was made executable")
	}
}

func TestApplyKeepsARosterTableThePersonAlreadyHas(t *testing.T) {
	var have map[string]any
	if _, err := toml.Decode("[critics.a]\nknows = [\"x\"]\n", &have); err != nil {
		t.Fatal(err)
	}
	add, kept := newSections("[critics.a]\nknows = [\"y\"]\n\n[critics.b]\nknows = [\"z\"]\n", have)
	if len(kept) != 1 || kept[0] != "critics.a" || strings.Contains(add, "critics.a") || !strings.Contains(add, "[critics.b]") {
		t.Errorf("kept %v, added:\n%s", kept, add)
	}
}

func TestAnExistingAgentLoadsWithLooseFrontmatter(t *testing.T) {
	b := []byte("---\nname: tools-critic\ndescription: SCOPE: the tools only. Not the shop.\ntools: Bash, Read\n---\nBody.\n")
	p, err := parseProfessional(b, "tools-critic.md", "agents", true)
	if err != nil || p.Description != "SCOPE: the tools only. Not the shop." {
		t.Fatalf("got %+v, %v", p, err)
	}
	if _, err := parseProfessional(b, "x.md", "project", false); err == nil {
		t.Error("a hire's own file must still be strict YAML")
	}
}

func TestDirectorListItemsWithAColonAreQuoted(t *testing.T) {
	in := "---\nname: x\nadministration:\n  constraints:\n    - reads only: Read, Grep\n    - \"already: quoted\"\n---\n- body: stays\n"
	got := string(quoteColonItems([]byte(in)))
	want := "---\nname: x\nadministration:\n  constraints:\n    - \"reads only: Read, Grep\"\n    - \"already: quoted\"\n---\n- body: stays\n"
	if got != want {
		t.Errorf("got:\n%s", got)
	}
}

func TestAPlanBlockForExistingEquipmentIsAChange(t *testing.T) {
	p := filepath.Join(t.TempDir(), "equipment.md")
	plan := "```toml\n[equipment.cli]\ncommands = [\"a\", \"b\"]\n```\n"
	if err := os.WriteFile(p, []byte(plan), 0o600); err != nil {
		t.Fatal(err)
	}
	have := map[string]equipment{"cli": {About: "the cli", Server: "commands", Commands: []string{"a"}}}
	if got := checkEquipmentPlan(p, have); got != nil {
		t.Errorf("a change to existing equipment was refused: %v", got)
	}
	if got := checkEquipmentPlan(p, nil); len(got) == 0 {
		t.Error("a new piece with no about or server passed")
	}
}
