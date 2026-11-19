package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// A session that agrees with the verifier's verdict in a word keeps the
// verifier's reason: the books still say why the finding is false.
func TestABareAgreementKeepsTheReasonBefore(t *testing.T) {
	old := []verdict{
		{Finding: 1, Verdict: "false", Why: "the claim function already excludes boxes with no order", By: "verifier"},
		{Finding: 2, Verdict: "false", Why: "the migration isn't applied on prod yet", By: "verifier"},
		{Finding: 3, Verdict: "confirmed", Why: "staff-action.ts:210 reads the stale carrier", By: "verifier"},
		{Finding: 4, Verdict: "likely", Why: "traced, not run", By: "verifier"},
		{Finding: 5, Verdict: "confirmed", By: "verifier"},
	}
	vs := []verdict{
		{Finding: 1, Verdict: "false", Why: "Agree with the verifier."},
		{Finding: 2, Verdict: "false"},
		{Finding: 3, Verdict: "confirmed", Why: "Reproduced it in the room: the PO box went to UPS."},
		{Finding: 4, Verdict: "false", Why: "agreed"},
		{Finding: 5, Verdict: "confirmed", Why: "agree"},
	}
	got := map[int]verdict{}
	for _, v := range mergeVerdicts(old, vs) {
		got[v.Finding] = v
	}
	want := map[int]string{
		1: "the claim function already excludes boxes with no order", // a bare agreement keeps it
		2: "the migration isn't applied on prod yet",                  // no reason keeps it
		3: "Reproduced it in the room: the PO box went to UPS.",       // a reason of its own replaces it
		4: "agreed",                                                   // another verdict: the earlier reason was for the other one
		5: "agree",                                                    // nothing to keep
	}
	for n, w := range want {
		if got[n].Why != w {
			t.Errorf("finding %d: why = %q, want %q", n, got[n].Why, w)
		}
	}
	for n := 1; n <= 5; n++ {
		if got[n].Verdict != vs[n-1].Verdict {
			t.Errorf("finding %d: verdict = %q, want the session's %q", n, got[n].Verdict, vs[n-1].Verdict)
		}
	}
}

func TestBareAgreement(t *testing.T) {
	for _, s := range []string{"", "  ", "Agree with the verifier.", "agree", "Agreed.", "I agree with the verifier", "agrees with verifier", "Concur.", "same", "OK", "agree with the check"} {
		if !bareAgreement(s) {
			t.Errorf("%q should be a bare agreement", s)
		}
	}
	for _, s := range []string{"Agree with the verifier: the claim function filters them first.", "agree, and the test at x_test.go:40 shows it", "false: the box can't reach Precision"} {
		if bareAgreement(s) {
			t.Errorf("%q says something of its own", s)
		}
	}
}

// status --json's folded counts are the report's: the same claim from two
// professionals is one finding, with the checks' verdicts on it.
func TestFoldedCountsAreTheReports(t *testing.T) {
	dir := t.TempDir()
	c := &contract{ID: "abcd", Professionals: []castItem{{Name: "a"}, {Name: "b"}}}
	write := func(name string, texts []string, vs []verdict) {
		pd := filepath.Join(dir, name)
		if err := os.MkdirAll(pd, 0o700); err != nil {
			t.Fatal(err)
		}
		recs := ""
		for i, tx := range texts {
			recs += fmt.Sprintf(`{"n":%d,"kind":"finding","text":%q,"source":"tool","at":"2026-01-01T00:00:00Z"}`+"\n", i+1, tx)
		}
		if err := os.WriteFile(filepath.Join(pd, "findings.jsonl"), []byte(recs), 0o600); err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(vs)
		if err := os.WriteFile(filepath.Join(pd, "verdicts.json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	same := "the checkout total adds the sales tax twice on every order"
	write("a", []string{same, "the refund button stays enabled after the refund is sent"},
		[]verdict{{Finding: 1, Verdict: "confirmed", By: "verifier"}, {Finding: 2, Verdict: "false", By: "verifier"}})
	write("b", []string{same, "the invoice footer shows last year's company address"},
		[]verdict{{Finding: 1, Verdict: "confirmed", By: "verifier"}, {Finding: 2, Verdict: "likely", By: "verifier"}})
	n := foldedCounts(c, dir)
	want := foldedCount{Findings: 3, Confirmed: 1, Likely: 1, False: 1}
	if *n != want {
		t.Fatalf("folded = %+v, want %+v", *n, want)
	}
}
