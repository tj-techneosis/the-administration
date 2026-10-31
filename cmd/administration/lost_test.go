package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOnboardingSaysWhatEachAgentLoses(t *testing.T) {
	rd := t.TempDir()
	in, out := filepath.Join(rd, "in"), filepath.Join(rd, "out")
	write := func(p, s string) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	agent := func(name, tools string) {
		write(filepath.Join(in, "agents", name+".md"), "---\nname: "+name+"\ndescription: d\n"+tools+"---\nbody\n")
	}
	agent("prober", "tools: Bash, Read, Grep, Glob\n")
	agent("reader", "tools: Read, Grep, Glob\n")
	agent("equipped", "tools:\n  - Bash\n  - Read\n")
	agent("owner", "tools: Bash, Read, mcp__playwright__browser_click, mcp__playwright__browser_install\n")
	agent("untold", "")
	write(filepath.Join(out, "roster.toml"), "[critics.prober]\nknows = [\"x\"]\n[critics.reader]\nknows = [\"x\"]\n"+
		"[critics.equipped]\nknows = [\"x\"]\nuses = [\"probe\"]\n[critics.untold]\nknows = [\"x\"]\n")
	write(filepath.Join(out, "convert", "owner.toml"), "kind = \"user\"\n")
	agent("cli-tester", "tools: Bash, Write\n")
	write(filepath.Join(out, "convert", "cli-tester.toml"), "kind = \"user\"\nuses = [\"cli\"]\n")
	write(filepath.Join(rd, "project", ".administration", "roster.toml"),
		"[equipment.cli]\nabout = \"the tool\"\nserver = \"sandboxed\"\ncommands = [\"bin/tool\"]\n")
	got := strings.Join(onboardLosses(filepath.Join(rd, "project"), in, out), "\n")
	for _, want := range []string{
		"prober (joins as a critic): Bash: runs nothing: no probes, scripts, tests or database queries",
		"equipped (joins as a critic): Bash: runs only what probe gives it",
		"owner (joins as a user): Bash: no shell",
		"Read: no files of the project, only its handout",
		"mcp__playwright__browser_install: not in the browser's allowlist",
		"cli-tester (joins as a user): Bash: a shell only for cli's commands, in the OS sandbox; Write: writes only in its desk",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, not := range []string{"reader", "untold", "browser_click"} {
		if strings.Contains(got, not) {
			t.Errorf("%q shouldn't be listed:\n%s", not, got)
		}
	}
	var note strings.Builder
	toolsNote(&note, filepath.Join(in, "agents"))
	if n := note.String(); !strings.Contains(n, "- prober: Bash, Read, Grep, Glob") || strings.Contains(n, "- reader") {
		t.Errorf("the Director's note:\n%s", n)
	}
}
