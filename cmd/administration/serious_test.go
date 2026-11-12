package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The rule is the owners' wording, word for word, in the verifier's prompt
// and the critics' brief alike: a change to one without the other would
// make the verifier and the professionals judge by two rules.
func TestTheSeriousRuleIsTheSameWordsEverywhere(t *testing.T) {
	want := "a finding is serious only if a real user, or an attacker who exists today, would hit it, and it causes data loss, wrong output or exposure"
	if seriousRule != want {
		t.Fatalf("seriousRule is %q, want the agreed wording %q", seriousRule, want)
	}
	prompt, err := house.ReadFile("house/verifier.prompt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(strings.Fields(string(prompt)), " "), want) {
		t.Fatalf("the verifier's prompt doesn't carry the rule word for word")
	}
}

func TestAFindingRuledNotSeriousGoesToTheBacklog(t *testing.T) {
	minor := &summed{text: "a", verdict: "confirmed", ruledMinor: true}
	serious := &summed{text: "b", verdict: "confirmed", ruledSerious: true}
	unruled := &summed{text: "c", verdict: "likely"}
	both := &summed{text: "e", verdict: "confirmed", ruledMinor: true, ruledSerious: true}
	falseOne := &summed{text: "d", verdict: "false", ruledMinor: true}
	release, backlog := backlogOf([]*summed{minor, serious, unruled, both, falseOne})
	if len(backlog) != 1 || backlog[0] != minor {
		t.Fatalf("backlog = %v, want only the finding ruled not serious and never serious", backlog)
	}
	if len(release) != 4 {
		t.Fatalf("release = %d findings, want the serious, the unruled, the mixed and the false one", len(release))
	}
}

// A check that ruled a finding serious keeps it in the release, whichever
// check's verdict wins and whichever fold brought them together.
func TestAnySeriousRulingKeepsAFoldedFindingInTheRelease(t *testing.T) {
	a := &summed{text: "the total adds tax twice at checkout", by: []string{"r #1"}, verdict: "confirmed", ruledMinor: true}
	b := &summed{text: "checkout charges tax two times over", by: []string{"q #2"}, verdict: "likely", ruledSerious: true}
	all := foldSameAs([]*summed{a, b}, map[string]string{"q #2": "r #1"}, map[string][]string{})
	if len(all) != 1 {
		t.Fatalf("not folded: %d", len(all))
	}
	if _, backlog := backlogOf(all); len(backlog) != 0 {
		t.Fatalf("a finding one check ruled serious went to the backlog")
	}
}

// The consequence and who a recorded finding ends with don't make two
// different bugs look like one.
func TestTheConsequenceAndWhoDontFoldTwoBugs(t *testing.T) {
	a := "the export is empty Consequence: no orders exported Who hits it today: staff exporting orders"
	b := "the photo upload hangs Consequence: no orders exported Who hits it today: staff exporting orders"
	if sameFinding(a, b) {
		t.Fatal("two bugs with the same consequence and who were folded into one")
	}
}

// A release whose changes caused only findings ruled not serious says so,
// not that nothing broke.
func TestTheHeadlineCountsNewFindingsInTheBacklog(t *testing.T) {
	back := []*summed{{text: "x", tag: "NEW", verdict: "confirmed", ruledMinor: true}}
	got := releaseVerdict(nil, 0, back)
	if strings.Contains(got, "Nobody reported anything") || !strings.Contains(got, "1 finding these changes caused") {
		t.Fatalf("%q", got)
	}
}

func TestAFindingMustSayItsConsequenceAndWhoHitsIt(t *testing.T) {
	if _, err := parseRecord("record_finding", []byte(`{"what":"the total adds tax twice"}`)); err == nil || !strings.Contains(err.Error(), "consequence") {
		t.Fatalf("a finding with no consequence or who was recorded: %v", err)
	}
	in, err := parseRecord("record_finding", []byte(`{"what":"the total adds tax twice","consequence":"a wrong total","who":"every shopper at checkout"}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := in.findingBody()
	if !strings.Contains(body, "Consequence: a wrong total") || !strings.Contains(body, "Who hits it today: every shopper at checkout") {
		t.Fatalf("the finding's body doesn't carry them: %q", body)
	}
}

// Through gatherFindings, as a summary reads the books: two finders of the
// same bug, their checks disagreeing on the verdict and the ruling, in both
// orders, and an unruled second check.
func TestGatheredFindingsKeepAnySeriousRulingInTheRelease(t *testing.T) {
	no, yes := false, true
	cases := []struct {
		name   string
		a, b   verdict
		inBack bool
	}{
		{"confirmed minor, likely serious", verdict{Verdict: "confirmed", Serious: &no}, verdict{Verdict: "likely", Serious: &yes}, false},
		{"likely serious, confirmed minor", verdict{Verdict: "likely", Serious: &yes}, verdict{Verdict: "confirmed", Serious: &no}, false},
		{"minor, then unruled", verdict{Verdict: "confirmed", Serious: &no}, verdict{Verdict: "confirmed"}, true},
		{"unruled, then minor", verdict{Verdict: "confirmed"}, verdict{Verdict: "confirmed", Serious: &no}, true},
		{"both unruled", verdict{Verdict: "confirmed"}, verdict{Verdict: "confirmed"}, false},
	}
	for _, tc := range cases {
		dir := t.TempDir()
		c := &contract{ID: "abcd", Professionals: []castItem{{Name: "a"}, {Name: "b"}}}
		for _, p := range []struct {
			name string
			v    verdict
		}{{"a", tc.a}, {"b", tc.b}} {
			pd := filepath.Join(dir, p.name)
			if err := os.MkdirAll(pd, 0o700); err != nil {
				t.Fatal(err)
			}
			rec := `{"n":1,"kind":"finding","text":"the checkout total adds the sales tax twice on every order","source":"tool","at":"2026-01-01T00:00:00Z"}` + "\n"
			if err := os.WriteFile(filepath.Join(pd, "findings.jsonl"), []byte(rec), 0o600); err != nil {
				t.Fatal(err)
			}
			v := p.v
			v.Finding, v.By = 1, "verifier"
			b, _ := json.Marshal([]verdict{v})
			if err := os.WriteFile(filepath.Join(pd, "verdicts.json"), b, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		all, _ := gatherFindings(c, dir)
		if len(all) != 1 {
			t.Fatalf("%s: not folded into one: %d", tc.name, len(all))
		}
		_, backlog := backlogOf(all)
		if (len(backlog) == 1) != tc.inBack {
			t.Fatalf("%s: in the backlog = %v, want %v", tc.name, len(backlog) == 1, tc.inBack)
		}
	}
}

// The kept fold: a finding whose mark a check corrected (retag) folds into
// another of that mark; a serious ruling on either keeps it in the release.
func TestTheKeptFoldKeepsASeriousRulingInTheRelease(t *testing.T) {
	no, yes := false, true
	for _, order := range [][2]*bool{{&yes, &no}, {&no, &yes}} {
		dir := t.TempDir()
		c := &contract{ID: "abcd", Changes: &changes{}, Professionals: []castItem{{Name: "a"}, {Name: "b"}}}
		for i, p := range []struct{ name, text, tag string }{
			{"a", "NEW: the checkout total adds the sales tax twice on every order", ""},
			{"b", "OLD: the checkout total adds the sales tax twice on every order", "NEW"},
		} {
			pd := filepath.Join(dir, p.name)
			if err := os.MkdirAll(pd, 0o700); err != nil {
				t.Fatal(err)
			}
			mark := strings.SplitN(p.text, ":", 2)[0]
			rec := fmt.Sprintf(`{"n":1,"kind":"finding","text":%q,"mark":%q,"source":"tool","at":"2026-01-01T00:00:00Z"}`+"\n", p.text, mark)
			if err := os.WriteFile(filepath.Join(pd, "findings.jsonl"), []byte(rec), 0o600); err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal([]verdict{{Finding: 1, Verdict: "confirmed", By: "verifier", Tag: p.tag, Serious: order[i]}})
			if err := os.WriteFile(filepath.Join(pd, "verdicts.json"), b, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		all, _ := gatherFindings(c, dir)
		if len(all) != 1 {
			t.Fatalf("the retagged finding didn't fold: %d", len(all))
		}
		if _, backlog := backlogOf(all); len(backlog) != 0 {
			t.Fatalf("a finding one check ruled serious went to the backlog through the kept fold")
		}
	}
}

// A release with a failed professional and only backlog NEW findings still
// says part of it went unchecked.
func TestTheBacklogHeadlineKeepsTheFailedNote(t *testing.T) {
	back := []*summed{{text: "x", tag: "NEW", verdict: "confirmed", ruledMinor: true}}
	got := releaseVerdict(nil, 1, back)
	if !strings.Contains(got, "Some professionals failed, so part of it went unchecked.\n") {
		t.Fatalf("%q", got)
	}
}
