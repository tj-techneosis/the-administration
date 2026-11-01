package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// A professional's record: what it found, its plan, and what it couldn't
// reach. It files them with the house's own tools (record_finding,
// record_plan, record_step, record_unreached), whose calls arrive in its
// stream already in parts, so nothing has to be read back out of prose.
// A FINDING: line it writes as text still counts, as before. Either way
// the worker keeps one record, findings.jsonl, a line per finding,
// unreachable thing or one not counted, in the order filed; the report, the
// summary, verdicts and links all go by it (a finding is "name #n": n is its
// number among the findings, the entry's n, not its line in the file).

// houseTools is the record's tools, for the professional's allowed list.
var houseTools = []string{"record_finding", "record_plan", "record_step", "record_unreached"}

var houseToolDefs = []map[string]any{
	{"name": "record_finding", "description": "File one finding the moment you find it: something wrong, with where and what you expected instead. Never a check that held.",
		"inputSchema": map[string]any{"type": "object", "required": []string{"what"}, "properties": map[string]any{
			"what":     map[string]any{"type": "string", "description": "What happened, and what you expected instead."},
			"where":    map[string]any{"type": "string", "description": "The URL and the steps to it, or the file and line in the working tree (src/cart.ts:412)."},
			"mark":     map[string]any{"type": "string", "enum": []string{"NEW", "CHANGE", "OLD"}, "description": "On a contract on changes only: NEW, CHANGE or OLD, as your brief says."},
			"evidence": map[string]any{"type": "string", "description": "What showed it: the text on the screen, the call and what came back."}}}},
	{"name": "record_plan", "description": "Write your plan before you start: each thing you'll check, in order. They're numbered 1, 2, 3...; call it again to add steps, numbered on from your last (a plan of 5 plus 2 more makes steps 6 and 7).",
		"inputSchema": map[string]any{"type": "object", "required": []string{"steps"}, "properties": map[string]any{
			"steps": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}}},
	{"name": "record_step", "description": "Answer a step of your plan: done (after any findings it gave), or not reached and why.",
		"inputSchema": map[string]any{"type": "object", "required": []string{"step", "done"}, "properties": map[string]any{
			"step": map[string]any{"type": "integer"}, "done": map[string]any{"type": "boolean"},
			"why": map[string]any{"type": "string", "description": "When not done: what stopped you."}}}},
	{"name": "record_unreached", "description": "Something you meant to try and could not reach, and why (not a step of your plan; for one, record_step with done false).",
		"inputSchema": map[string]any{"type": "object", "required": []string{"what", "why"}, "properties": map[string]any{
			"what": map[string]any{"type": "string"}, "why": map[string]any{"type": "string"}}}},
}

// cmdHouse is the record's MCP server. It only says "Recorded.", or why a
// call can't be: the worker reads each call from the stream, so the record
// has one keeper.
func cmdHouse(args []string) error {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 1<<24)
	out := json.NewEncoder(os.Stdout)
	for in.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"params"`
		}
		if json.Unmarshal(in.Bytes(), &req) != nil || req.ID == nil {
			continue
		}
		reply := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		switch req.Method {
		case "initialize":
			reply["result"] = map[string]any{"protocolVersion": "2024-11-05",
				"capabilities": map[string]any{"tools": map[string]any{}},
				"serverInfo":   map[string]any{"name": "administration-record", "version": version}}
		case "tools/list":
			reply["result"] = map[string]any{"tools": houseToolDefs}
		case "tools/call":
			// "Recorded.", or what's wrong with the call, and nothing else.
			text, bad := "Recorded.", false
			if _, err := parseRecord(req.Params.Name, req.Params.Arguments); err != nil {
				text, bad = err.Error(), true
			}
			reply["result"] = map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "isError": bad}
		case "ping":
			reply["result"] = map[string]any{}
		default:
			reply["error"] = map[string]any{"code": -32601, "message": "no method " + req.Method}
		}
		if err := out.Encode(reply); err != nil {
			return err
		}
	}
	return in.Err()
}

// houseAllowed is the record's tools as allow rules.
func houseAllowed() []string {
	var out []string
	for _, t := range houseTools {
		out = append(out, "mcp__house__"+t)
	}
	return out
}

// houseServer is the record's server, for a professional's MCP config.
func houseServer() (map[string]any, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return map[string]any{"command": self, "args": []string{"_house"}}, nil
}

// recordCall is a call to one of the record's tools, in its parts.
type recordCall struct {
	What     string   `json:"what"`
	Where    string   `json:"where"`
	Mark     string   `json:"mark"`
	Evidence string   `json:"evidence"`
	Steps    []string `json:"steps"`
	Step     *int     `json:"step"`
	Done     *bool    `json:"done"`
	Why      string   `json:"why"`
}

// parseRecord reads a call to the record's tools and says what's wrong
// with it: the server answers that, so the professional calls again,
// rather than "Recorded." for something the worker can't record.
func parseRecord(name string, input json.RawMessage) (recordCall, error) {
	var in recordCall
	if err := json.Unmarshal(input, &in); err != nil {
		return in, fmt.Errorf("not recorded: %v (what and where are text, step a number, done true or false, steps a list of text)", err)
	}
	blank := func(s string) bool { return strings.TrimSpace(s) == "" }
	switch strings.TrimPrefix(name, "mcp__house__") {
	case "record_finding":
		if blank(in.What) {
			return in, fmt.Errorf("not recorded: what is empty; say what happened and what you expected")
		}
		if m := strings.ToUpper(strings.TrimSpace(in.Mark)); m != "" && m != "NEW" && m != "CHANGE" && m != "OLD" {
			return in, fmt.Errorf("not recorded: mark is NEW, CHANGE or OLD, or left out")
		}
	case "record_plan":
		if len(in.Steps) == 0 || slices.ContainsFunc(in.Steps, blank) {
			return in, fmt.Errorf("not recorded: steps is a list of the things you'll check, none empty")
		}
	case "record_step":
		if in.Step == nil || *in.Step < 1 || in.Done == nil {
			return in, fmt.Errorf("not recorded: step is your plan's step number, and done true or false")
		}
	case "record_unreached":
		if blank(in.What) || blank(in.Why) {
			return in, fmt.Errorf("not recorded: say what you couldn't reach and why")
		}
	default:
		return in, fmt.Errorf("no tool %s", name)
	}
	return in, nil
}

var leadingMark = regexp.MustCompile(`(?i)^\s*(NEW|CHANGE|OLD)\s*:\s*`)

// findingBody is a recorded finding as one line: its mark, what, where,
// evidence. A mark written into what as well is taken once.
func (in recordCall) findingBody() (body, what string) {
	flat := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	what = flat(in.What)
	mark := strings.ToUpper(strings.TrimSpace(in.Mark))
	if m := leadingMark.FindStringSubmatch(what); m != nil {
		if mark == "" {
			mark = strings.ToUpper(m[1])
		}
		what = what[len(m[0]):]
	}
	body = what
	if in.Where != "" {
		body += " (at " + flat(in.Where) + ")"
	}
	if in.Evidence != "" {
		body += " Evidence: " + flat(in.Evidence)
	}
	if mark != "" {
		body = mark + ": " + body
	}
	return body, what
}

// applyRecord puts a call to the record's tools on the record directly, as
// what it is, never through the patterns written lines are read with; ok
// is false for any other tool or a call the server refused.
func (r *report) applyRecord(name string, input json.RawMessage) (changed, ok bool) {
	in, err := parseRecord(name, input)
	if err != nil {
		return false, false
	}
	r.fromTool = true
	defer func() { r.fromTool = false }()
	if r.plan == nil {
		r.plan, r.answered, r.covered = map[int]string{}, map[int]bool{}, map[int]bool{}
	}
	switch strings.TrimPrefix(name, "mcp__house__") {
	case "record_finding":
		body, what := in.findingBody()
		if r.what == nil {
			r.what = map[string]string{}
		}
		r.what[body] = what
		return r.file("FINDING", body), true
	case "record_plan":
		// Numbered on from the last step, as the tool says.
		next := 1
		for n := range r.plan {
			next = max(next, n+1)
		}
		for i, step := range in.Steps {
			r.plan[next+i] = oneLine(step, 160)
		}
		return true, true
	case "record_step":
		n := *in.Step
		r.answered[n], r.covered[n] = true, *in.Done
		if *in.Done {
			return true, true
		}
		// Not done: what the step was goes with why, for the report and a
		// resume (which has no plan of this one's to look it up in).
		what := fmt.Sprintf("step %d", n)
		if p, ok := r.plan[n]; ok {
			what += " (" + p + ")"
		}
		r.file("UNREACHED", what+": "+strings.Join(strings.Fields(in.Why), " "))
		return true, true
	case "record_unreached":
		return r.file("UNREACHED", strings.Join(strings.Fields(in.What), " ")+" ("+strings.Join(strings.Fields(in.Why), " ")+")"), true
	}
	return false, false
}

// recordShown is a call to the record's tools as watch shows it.
func recordShown(name string, input json.RawMessage) []string {
	in, err := parseRecord(name, input)
	if err != nil {
		return []string{"✗ " + err.Error()}
	}
	switch strings.TrimPrefix(name, "mcp__house__") {
	case "record_finding":
		body, _ := in.findingBody()
		return []string{"★ FINDING: " + body}
	case "record_plan":
		return []string{"  plan: " + strings.Join(in.Steps, "; ")}
	case "record_step":
		if *in.Done {
			return []string{fmt.Sprintf("  step %d done", *in.Step)}
		}
		return []string{fmt.Sprintf("★ UNREACHED: step %d: %s", *in.Step, in.Why)}
	case "record_unreached":
		return []string{"★ UNREACHED: " + in.What + " (" + in.Why + ")"}
	}
	return nil
}

// entry is one line of findings.jsonl.
type entry struct {
	N      int    `json:"n,omitempty"` // a finding's number: "name #n"
	Kind   string `json:"kind"`        // finding, unreached, not_counted
	Text   string `json:"text"`
	Mark   string `json:"mark,omitempty"`
	Source string `json:"source"` // tool or text
	At     string `json:"at"`
}

// note keeps a line of the record as it's filed.
func (r *report) note(kind, text string) {
	e := entry{Kind: kind, Text: text, Source: "text", At: time.Now().UTC().Format(time.RFC3339)}
	if r.fromTool {
		e.Source = "tool"
	}
	if kind == "finding" {
		e.N = len(r.findings)
		e.Mark = findingTag(text)
	}
	r.entries = append(r.entries, e)
}

// writeRecord writes the record whole (it's small): findings.jsonl beside
// the report.
func (r *report) writeRecord(pd string) error {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	for _, e := range r.entries {
		if err := enc.Encode(e); err != nil {
			return err
		}
	}
	tmp := filepath.Join(pd, "findings.jsonl.tmp")
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(pd, "findings.jsonl"))
}

// readRecord is a professional's record, or nil when it has none (a
// contract from before there was one).
func readRecord(dir, name string) []entry {
	f, err := os.Open(filepath.Join(dir, name, "findings.jsonl"))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var e entry
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			out = append(out, e)
		}
	}
	if sc.Err() != nil {
		return nil // can't be read whole: the report it was written with
	}
	return out
}

// findingsOf is a professional's findings in order, from its record, or
// from its report when there's no record.
func findingsOf(dir, name string) ([]string, bool) {
	if rec := readRecord(dir, name); rec != nil {
		var out []string
		for _, e := range rec {
			if e.Kind == "finding" {
				out = append(out, e.Text)
			}
		}
		return out, true
	}
	rp, err := os.ReadFile(filepath.Join(dir, name, "report.md"))
	if err != nil {
		return nil, false
	}
	return reportFindings(string(rp)), true
}

// unreachedOf is what a professional couldn't reach, from its record, or
// from its report when there's no record.
func unreachedOf(dir, name string) []string {
	if rec := readRecord(dir, name); rec != nil {
		var out []string
		for _, e := range rec {
			if e.Kind == "unreached" {
				out = append(out, e.Text)
			}
		}
		return out
	}
	rp, _ := os.ReadFile(filepath.Join(dir, name, "report.md"))
	return reportSection(string(rp), "## Could not reach")
}
