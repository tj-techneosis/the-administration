package main

import (
	"strings"
	"testing"
)

func TestMarkLineVariants(t *testing.T) {
	cases := map[string]string{
		"FINDING: a":              "FINDING",
		"- **FINDING:** b":        "FINDING",
		"1. FINDING: c":           "FINDING",
		"`FINDING`: d":            "FINDING",
		"FINDING (high): e":       "FINDING",
		"Finding: f":              "FINDING",
		"  * UNREACHED: g":        "UNREACHED",
		"**Unreached**: h":        "UNREACHED",
	}
	for line, kind := range cases {
		m := markLine.FindStringSubmatch(line)
		if m == nil {
			t.Errorf("%q: not a marked line", line)
			continue
		}
		if got := m[1]; got != kind && got != map[string]string{"FINDING": "Finding", "UNREACHED": "Unreached"}[kind] {
			t.Errorf("%q: kind %q", line, got)
		}
	}
	for _, line := range []string{"No findings here.", "Unreached - x", "The FINDING was that", "FINDING:"} {
		if markLine.MatchString(line) {
			t.Errorf("%q: matched", line)
		}
	}
	r := &report{}
	r.scan("## Findings\n- one thing\n- two things\n\nprose\nfinding: three")
	if len(r.findings) != 3 {
		t.Errorf("findings = %q", r.findings)
	}
}

func TestManifestPorts(t *testing.T) {
	m := &manifest{URL: "http://127.0.0.1:4100/", Origins: []string{"http://localhost:4101", "http://127.0.0.1:4100/api"}}
	got := manifestPorts(m)
	if len(got) != 2 || got[0] != 4100 || got[1] != 4101 {
		t.Errorf("manifestPorts = %v", got)
	}
}

// The shape of a real stream when Playwright's browser can't start: the
// error comes back as a string in a tool_result marked is_error.
func TestBrowserThatNeverWorked(t *testing.T) {
	r := &report{}
	r.add([]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_1","name":"mcp__playwright__browser_navigate","input":{"url":"http://127.0.0.1:1/"}}]}}`))
	r.add([]byte(`{"type":"user","message":{"content":[{"type":"tool_result","content":"### Error\nError: async initializeServer: Socket directory path is too long (86 bytes)","is_error":true,"tool_use_id":"toolu_1"}]}}`))
	if r.browserRun != 1 || r.browserOK != 0 || !strings.Contains(r.browserErr, "Socket directory path is too long") {
		t.Fatalf("run %d ok %d err %q", r.browserRun, r.browserOK, r.browserErr)
	}
	r.add([]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_2","name":"mcp__playwright__browser_navigate","input":{}}]}}`))
	r.add([]byte(`{"type":"user","message":{"content":[{"type":"tool_result","content":[{"type":"text","text":"- page"}],"is_error":false,"tool_use_id":"toolu_2"}]}}`))
	if r.browserOK != 1 {
		t.Fatalf("ok %d", r.browserOK)
	}
}

func TestARestatedFindingIsCountedOnce(t *testing.T) {
	var r report
	r.scan("FINDING: Every slide in the second copy is `inert`, so a person can't click or tap it. `Carousel.tsx:336` sets inert on the copy.")
	r.scan("FINDING: Check 1, `request`. This is the same defect with a different table. See `request/actions.ts:79,95,165`.")
	// The closing report, restated in other words.
	r.scan("What broke:\nFINDING: Every second-copy slide is `inert` (`Carousel.tsx:336`), so a person can't click or tap the slides in the copy.\n" +
		"FINDING: Check 1, `reserve`. This is the same pattern. See `reserve/actions.ts:186-209,285,312`.")
	if len(r.findings) != 3 {
		t.Fatalf("want the restatement merged and reserve kept apart from request, got %d:\n%s", len(r.findings), strings.Join(r.findings, "\n"))
	}
}
