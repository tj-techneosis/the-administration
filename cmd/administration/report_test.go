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

func TestAHeldCheckIsKeptButNotCounted(t *testing.T) {
	var r report
	r.scan("FINDING: Held: the cart total matches the receipt\n" +
		"FINDING: Works as expected: the refund path\n" +
		"FINDING: No issue - the header wraps cleanly at 320px\n" +
		"FINDING: the OK button is hidden behind the keyboard at 320px\n" +
		"FINDING: the plan page 404s for a new account\n" +
		"FINDING: NEW: covered items are no longer struck through")
	if len(r.findings) != 3 || len(r.notCounted) != 3 {
		t.Fatalf("counted %q, not counted %q", r.findings, r.notCounted)
	}
	if md := r.markdown(&contract{}, &castItem{Name: "x"}, "back", ""); !strings.Contains(md, "## Not counted") || !strings.Contains(md, "- Held: the cart total") {
		t.Fatalf("not counted lines aren't in the report:\n%s", md)
	}
}

func TestRealDefectsThatOpenLikeAHeldCheckStillCount(t *testing.T) {
	var r report
	r.scan("FINDING: Holds: the lock holds forever after a crash\n" +
		"FINDING: Covered - the banner covers the Pay button\n" +
		"FINDING: Plan-B checkout fails for a saved card\n" +
		"FINDING: Passed: an expired coupon is accepted\n" +
		"FINDING: Plan-based pricing charges twice\n" +
		"FINDING: Plan 3: the Pay button is hidden behind the keyboard\n" +
		"FINDING: Held - the refund total matches")
	if len(r.findings) != 6 || len(r.notCounted) != 1 {
		t.Fatalf("counted %q, not counted %q", r.findings, r.notCounted)
	}
}

func TestATagIsAWordOfItsOwn(t *testing.T) {
	for in, want := range map[string]string{
		"NEW: the total is wrong": "NEW", "NEW — the total": "NEW", "**NEW** `cart.ts:88`": "NEW", "OLD (both)": "OLD",
		"CHANGE: the comment": "CHANGE", "NEWLINE at end of file is missing": "", "New users see a blank page": "",
		"OLDER browsers get nothing": "", "CHANGES to the cart": "",
		// Any case, with a colon: a "New:" restatement lands with its "NEW:" twin.
		"New: no word that it worked": "NEW", "**New:** the total": "NEW", "old: both": "OLD", "Change: the comment": "CHANGE",
		"New users: a blank page": "", "Changed: the cart": "", "Older: both": "",
	} {
		if got := findingTag(in); got != want {
			t.Errorf("findingTag(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTheSummaryKeepsWhatHeldAndDropsRestatements(t *testing.T) {
	prose := "## Their report\n\nWhat I tried: read the cart.\n\nPLAN 1: the cart. COVERED 1.\n\n" +
		"What held:\n- the total matches the receipt\n\n**All findings, repeated:**\n1. NEW the tax is added twice\n2. OLD the logo\n\n" +
		"FINDING: NEW: the tax is added twice\nUNREACHED: billing\n\nWhat I'd check next: refunds."
	got := stripMarked(prose)
	for _, keep := range []string{"What I tried: read the cart.", "What held:", "- the total matches the receipt", "What I'd check next: refunds."} {
		if !strings.Contains(got, keep) {
			t.Errorf("dropped %q:\n%s", keep, got)
		}
	}
	for _, drop := range []string{"PLAN 1", "All findings", "tax is added twice", "the logo", "UNREACHED"} {
		if strings.Contains(got, drop) {
			t.Errorf("kept %q:\n%s", drop, got)
		}
	}
}

func TestRepeatsAtTheCloseAreFoldedButNewOnesKept(t *testing.T) {
	var r report
	r.scan("FINDING: the upload panel covers the save button\n" +
		"FINDING: NEW: the save button does nothing on /settings")
	// The closing message: one paraphrase, one that says it's a repeat in
	// words no match would catch, one close in words but under another
	// mark, and one new.
	r.closing = true
	r.scan("FINDING: the docked panel hides the save button\n" +
		"FINDING: the refund note (already reported)\n" +
		"FINDING: OLD: Help and README disagree on the timeout\n" +
		"FINDING: the export button downloads an empty file")
	r.closing = false
	// The paraphrase and the self-declared repeat are kept, not counted.
	if len(r.findings) != 4 || len(r.notCounted) != 2 || r.notCounted[0] != "the docked panel hides the save button" {
		t.Fatalf("counted %q, not counted %q", r.findings, r.notCounted)
	}
	// A one-word finding doesn't swallow every finding that uses its word,
	// and two findings on different things aren't one.
	var w report
	w.scan("FINDING: Crash\nFINDING: the save button is hidden")
	w.closing = true
	w.scan("FINDING: Crash when exporting a report\nFINDING: the export button is hidden")
	if len(w.findings) != 4 {
		t.Fatalf("counted %q, not counted %q", w.findings, w.notCounted)
	}
	// Along the way, the same paraphrase is a finding of its own.
	var s report
	s.scan("FINDING: the upload panel covers the save button")
	s.scan("FINDING: the docked panel hides the save button")
	if len(s.findings) != 2 {
		t.Fatalf("along the way: %q", s.findings)
	}
}

func TestAResumeIsSizedToThePlanLeft(t *testing.T) {
	r := report{plan: map[int]string{1: "a", 2: "b", 3: "c", 4: "d"}, answered: map[int]bool{1: true, 2: true}, covered: map[int]bool{1: true, 2: false}}
	if got := r.coinsFor(10); !strings.Contains(got, "--coins 30m") || !strings.Contains(got, "covered 1 of their 4 plan steps in 10 coins") {
		t.Fatalf("one of four in 10: %q", got)
	}
	r.answered, r.covered = map[int]bool{}, map[int]bool{}
	if got := r.coinsFor(10); !strings.Contains(got, "--coins 20m") {
		t.Fatalf("none answered: %q", got)
	}
	if got := (&report{}).coinsFor(10); got != "" {
		t.Fatalf("no plan: %q", got)
	}
}

func TestALeadingBacktickIsKeptAndNoneIsNothingLeft(t *testing.T) {
	var r report
	r.scan("FINDING:`unchecked` is never shown on the summary\n" +
		"`FINDING:` the save button does nothing\n" +
		"UNREACHED: None\n" +
		"UNREACHED: n/a\n" +
		"UNREACHED: billing, no link to it")
	want := []string{"`unchecked` is never shown on the summary", "the save button does nothing"}
	if strings.Join(r.findings, "|") != strings.Join(want, "|") || len(r.unreached) != 1 {
		t.Fatalf("findings %q, unreached %q", r.findings, r.unreached)
	}
}

func TestACoveredLineAnswersEveryStepItNames(t *testing.T) {
	var r report
	r.scan("PLAN 1: a\nPLAN 2: b\nPLAN 3: c\nPLAN 4: d\nPLAN 5: e\nPLAN 6: f\n" +
		"COVERED 1, 2 and 4-5\nUNREACHED: 3 of the forms never loaded")
	if done, steps := r.planDone(); done != 4 || steps != 6 || len(r.unanswered()) != 1 {
		t.Fatalf("done %d of %d, unanswered %q", done, steps, r.unanswered())
	}
}

func TestMarksAndAnswersAreReadOnlyWhereTheyAreMeant(t *testing.T) {
	var r report
	r.scan("PLAN 1: a\nPLAN 2: b\nPLAN 3: c\nPLAN 4: d\n" +
		"**COVERED 1, 2 and 3.**\n" +
		"Covered 4 pages of the site, all fine\n" +
		"FINDING: none\n" +
		"FINDING: Import skips rows already listed in the sheet\n" +
		"FINDING: the refund note is wrong (already reported)")
	if done, steps := r.planDone(); done != 3 || steps != 4 {
		t.Fatalf("covered %d of %d", done, steps)
	}
	if len(r.findings) != 1 || !strings.HasPrefix(r.findings[0], "Import skips rows") || len(r.notCounted) != 1 {
		t.Fatalf("counted %q, not counted %q", r.findings, r.notCounted)
	}
}

func TestAPatchPartIsNamedForItsFile(t *testing.T) {
	for head, want := range map[string]string{
		"diff --git a/dir b/f.txt b/dir b/f.txt\n--- a/dir b/f.txt\n+++ b/dir b/f.txt\n@@ -1 +1 @@\n-x\n+y": "dir b/f.txt",
		"diff --git \"a/caf\\303\\251.png\" \"b/caf\\303\\251.png\"\nBinary files differ":          "café.png",
		"diff --git a/gone.txt b/gone.txt\ndeleted file mode 100644\n--- a/gone.txt\n+++ /dev/null\n@@ -1 +0,0 @@\n-x": "gone.txt",
	} {
		if got := partName(head); got != want {
			t.Errorf("%q: got %q, want %q", head, got, want)
		}
	}
}
