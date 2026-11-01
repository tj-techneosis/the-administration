package main

import (
	"strings"
	"testing"
)

func call(r *report, name, input string) bool {
	ch, _ := r.applyRecord("mcp__house__"+name, []byte(input))
	return ch
}

func TestTheRecordTakesItsToolsAsWhatTheyAre(t *testing.T) {
	var r report
	call(&r, "record_plan", `{"steps":["the cart","the receipt"]}`)
	call(&r, "record_plan", `{"steps":["checkout on a phone"]}`)
	// Two findings that share their evidence's wording are two findings.
	call(&r, "record_finding", `{"what":"the total is $5 too high","where":"/cart","mark":"NEW","evidence":"screenshot of the cart page"}`)
	call(&r, "record_finding", `{"what":"the receipt prints twice","where":"/cart","evidence":"screenshot of the cart page"}`)
	// A mark written into what as well is taken once.
	call(&r, "record_finding", `{"what":"OLD: the logo is blurry","mark":"OLD"}`)
	// Something unreached that starts with a number isn't a step's answer.
	call(&r, "record_unreached", `{"what":"2 of the settings pages","why":"no link to them"}`)
	call(&r, "record_step", `{"step":2,"done":false,"why":"the printer was off"}`)
	call(&r, "record_step", `{"step":1,"done":true}`)
	if len(r.findings) != 3 || r.findings[2] != "OLD: the logo is blurry" {
		t.Fatalf("findings %q", r.findings)
	}
	if done, steps := r.planDone(); done != 1 || steps != 3 {
		t.Fatalf("plan %d of %d", done, steps)
	}
	if strings.Join(r.unreached, " | ") != "2 of the settings pages (no link to them) | step 2 (the receipt): the printer was off" {
		t.Fatalf("unreached %q", r.unreached)
	}
	// A call the server refuses records nothing.
	if call(&r, "record_finding", `{"what":"  "}`) || call(&r, "record_step", `{"step":"3","done":true}`) {
		t.Fatal("a refused call was recorded")
	}
}

func TestTheClosingMessageRestatesWithoutCounting(t *testing.T) {
	var r report
	r.add([]byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"FINDING: the upload panel covers the save button"}]}}`))
	r.add([]byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"x","content":"ok"}]}}`))
	// The final message, before its result: a restatement and a new one.
	r.add([]byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"What broke:\nFINDING: the docked panel hides the save button\nFINDING: the export downloads an empty file"}]}}`))
	r.add([]byte(`{"type":"result","subtype":"success","result":"Done."}`))
	if len(r.findings) != 2 || len(r.notCounted) != 1 || r.notCounted[0] != "the docked panel hides the save button" {
		t.Fatalf("counted %q, not counted %q", r.findings, r.notCounted)
	}
	n := 0
	for _, e := range r.entries {
		if e.Kind == "finding" {
			n++
			if e.N != n {
				t.Fatalf("entry %q numbered %d", e.Text, e.N)
			}
		}
	}
}
