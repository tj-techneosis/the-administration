package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAnOnboardedPersonaKeepsItsOwnWords(t *testing.T) {
	rd := t.TempDir()
	in, out := filepath.Join(rd, "in"), filepath.Join(rd, "out")
	body := "You are Sam on a Chromebook.\n\n1. Can they find it? The first one you fail is the finding.\n2. A bare underlined word is not a button.\n"
	write := func(p, s string) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(in, "agents", "acme-owner-agent.md"), "---\nname: acme-owner-agent\ndescription: the owner\ntools: Bash, Read\n---\n"+body)
	write(filepath.Join(out, "convert", "acme-owner-agent.toml"), `description = "Sam, the owner, on a Chromebook."
kind = "user"
coins = "12m"
knows = ["admin", "workflow"]
signs_in_as = "owner"
handout = ["house-rules.md", "viewport-rules.md"]
notes = """
No shell or database here; the rules it reads are in ./handout/.
"""
`)
	write(filepath.Join(out, "roster.toml"), "[critics.rules]\nknows = [\"code-review\"]\n")
	if p := assembleConversions(in, out); len(p) > 0 {
		t.Fatalf("problems: %v", p)
	}
	cand := filepath.Join(out, "candidates", "acme-owner-agent.md")
	b, err := os.ReadFile(cand)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(b), body) {
		t.Fatalf("the body wasn't carried over word for word:\n%s", b)
	}
	if !strings.Contains(string(b), "## In the Administration\n\nNo shell or database here") {
		t.Fatalf("no note:\n%s", b)
	}
	p, err := parseProfessional(b, cand, "project", false)
	if err != nil {
		t.Fatalf("the candidate doesn't parse: %v\n%s", err, b)
	}
	if p.Kind != "user" || p.SignsInAs != "owner" || p.CoinsMin != 12 || strings.Join(p.Knows, ",") != "admin,workflow" {
		t.Fatalf("frontmatter lost: %+v", p)
	}
	rt, _ := os.ReadFile(filepath.Join(out, "roster.toml"))
	if !strings.Contains(string(rt), "[critics.rules]") || !strings.Contains(string(rt), `[handout]`+"\n"+`files = ["house-rules.md", "viewport-rules.md"]`) {
		t.Fatalf("roster.toml: %s", rt)
	}
}
