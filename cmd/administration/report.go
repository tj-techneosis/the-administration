package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// report is built from the professional's stream as it arrives, so a
// professional shown out at the coin limit still leaves every finding they
// wrote down.
type report struct {
	findings  []string
	unreached []string
	seen      map[string]bool
	final     string
	isError   string
	// From claude's closing event; absent if it was shown out first.
	cost      *float64
	turns     int
	tokensIn  int
	tokensOut int
	// Browser tool calls, and how many pages loaded (browser_navigate came
	// back without an error), so a user whose browser never worked is told
	// apart from one who found nothing. Closing, listing tabs or resizing
	// works with no page at all, so only navigating counts.
	browser    map[string]string
	browserRun int
	browserOK  int
	browserErr string
	// The professional's plan, by number, and the items it answered
	// (covered, or unreached and why), so the Administration can send it
	// back to what it skipped. results counts its closing events: each one
	// is a moment it thought it was done.
	plan     map[int]string
	answered map[int]bool
	results  int
}

var planLine = regexp.MustCompile("(?i)^[\\s>*_#`-]*PLAN\\s*(\\d+)\\s*[:.)-]\\s*[*_`]*\\s*(\\S.*)$")
var answerLine = regexp.MustCompile("(?i)^[\\s>*_#`-]*(?:COVERED|UNREACHED)[*_`]*\\s*:?\\s*[*_`]*\\s*(?:plan\\s*)?(\\d+)\\b")

// unanswered is the plan items with no answer yet, in order.
func (r *report) unanswered() []string {
	var nums []int
	for n := range r.plan {
		if !r.answered[n] {
			nums = append(nums, n)
		}
	}
	sort.Ints(nums)
	var out []string
	for _, n := range nums {
		out = append(out, fmt.Sprintf("PLAN %d: %s", n, r.plan[n]))
	}
	return out
}

// A marked line, as models really write them: "FINDING: x", "- **Finding:** x",
// "1. `FINDING`: x", "FINDING (high): x", "Unreached - x" is not one (no colon).
var markLine = regexp.MustCompile("(?i)^[\\s>*_#`-]*(?:\\d+[.)]\\s*)?[*_`]*(FINDING|UNREACHED)[*_`]*\\s*(?:\\([^)]{0,40}\\))?\\s*[*_`]*\\s*:\\s*[*_`]*\\s*(\\S.*)$")

// add takes one line of stream-json and reports whether the counts changed.
func (r *report) add(line []byte) bool {
	var ev struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
		IsError bool   `json:"is_error"`
		Result  string `json:"result"`
		Cost    *float64 `json:"total_cost_usd"`
		Turns   int      `json:"num_turns"`
		Usage   struct {
			In          int `json:"input_tokens"`
			CacheRead   int `json:"cache_read_input_tokens"`
			CacheCreate int `json:"cache_creation_input_tokens"`
			Out         int `json:"output_tokens"`
		} `json:"usage"`
		Message struct {
			Content []struct {
				Type      string          `json:"type"`
				Text      string          `json:"text"`
				ID        string          `json:"id"`
				Name      string          `json:"name"`
				ToolUseID string          `json:"tool_use_id"`
				IsError   bool            `json:"is_error"`
				Content   json.RawMessage `json:"content"`
			} `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &ev) != nil {
		return false
	}
	switch ev.Type {
	case "assistant":
		changed := false
		for _, c := range ev.Message.Content {
			if c.Type == "text" && r.scan(c.Text) {
				changed = true
			}
			if c.Type == "tool_use" && strings.HasPrefix(c.Name, "mcp__playwright__") {
				if r.browser == nil {
					r.browser = map[string]string{}
				}
				r.browser[c.ID] = c.Name
				r.browserRun++
			}
		}
		return changed
	case "user":
		for _, c := range ev.Message.Content {
			name, ok := r.browser[c.ToolUseID]
			if c.Type != "tool_result" || !ok {
				continue
			}
			if !c.IsError {
				if name == "mcp__playwright__browser_navigate" {
					r.browserOK++
				}
			} else if r.browserErr == "" {
				r.browserErr = oneLine(toolText(c.Content), 200)
			}
		}
	case "result":
		// Each closing event in a session counts the whole session so far;
		// one with nothing to say (a short answer when sent back) leaves
		// the numbers as they were.
		if ev.Cost != nil {
			r.cost = ev.Cost
		}
		r.turns = max(r.turns, ev.Turns)
		if in := ev.Usage.In + ev.Usage.CacheRead + ev.Usage.CacheCreate; in > 0 {
			r.tokensIn, r.tokensOut = in, ev.Usage.Out
		}
		if ev.IsError {
			r.isError = strings.TrimSpace("claude ended with an error: " + ev.Subtype + " " + oneLine(ev.Result, 200))
			return false
		}
		r.results++
		if f := strings.TrimSpace(ev.Result); f != "" {
			if r.final != "" {
				r.final += "\n\n"
			}
			r.final += f
		}
		return r.scan(ev.Result)
	}
	return false
}

// toolText is a tool result's text: a string, or a list of text parts.
func toolText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(raw, &parts)
	var b []string
	for _, p := range parts {
		b = append(b, p.Text)
	}
	return strings.Join(b, " ")
}

func (r *report) scan(text string) bool {
	if r.seen == nil {
		r.seen = map[string]bool{}
	}
	changed := false
	add := func(kind, body string) {
		body = strings.TrimSpace(strings.TrimRight(body, "*_ "))
		key := kind + ":" + strings.ToLower(strings.Join(strings.Fields(body), " "))
		if body == "" || r.seen[key] {
			return
		}
		r.seen[key] = true
		// The brief asks for every finding again at the end, and a
		// restatement is rarely word for word.
		prior := r.unreached
		if kind == "FINDING" {
			prior = r.findings
		}
		for _, p := range prior {
			if sameFinding(p, body) {
				return
			}
		}
		if kind == "FINDING" {
			r.findings = append(r.findings, body)
		} else {
			r.unreached = append(r.unreached, body)
		}
		changed = true
	}
	for _, l := range strings.Split(text, "\n") {
		if m := planLine.FindStringSubmatch(l); m != nil {
			if r.plan == nil {
				r.plan, r.answered = map[int]string{}, map[int]bool{}
			}
			n, _ := strconv.Atoi(m[1])
			if _, ok := r.plan[n]; !ok {
				r.plan[n] = oneLine(m[2], 160)
			}
		}
		if m := answerLine.FindStringSubmatch(l); m != nil {
			if r.answered == nil {
				r.plan, r.answered = map[int]string{}, map[int]bool{}
			}
			n, _ := strconv.Atoi(m[1])
			r.answered[n] = true
		}
	}
	// A bare "UNREACHED:" heading followed by a bullet list counts each bullet.
	list := ""
	for _, l := range strings.Split(text, "\n") {
		if m := markLine.FindStringSubmatch(l); m != nil {
			list = ""
			add(strings.ToUpper(m[1]), m[2])
			continue
		}
		if m := bareMark.FindStringSubmatch(l); m != nil {
			list = strings.ToUpper(m[1])
			continue
		}
		if list != "" {
			if b := bullet.FindStringSubmatch(l); b != nil {
				add(list, b[1])
				continue
			}
			if strings.TrimSpace(l) != "" {
				list = ""
			}
		}
	}
	return changed
}

var (
	bareMark = regexp.MustCompile("(?i)^[\\s>*_#`-]*[*_`]*(FINDING|UNREACHED)S?[*_`]*\\s*:?\\s*[*_`]*\\s*$")
	bullet   = regexp.MustCompile(`^\s*(?:[-*•]|\d+[.)])\s+(.+)$`)
)

// prose is the final report without the marked lines, which are already
// listed above it.
func prose(text string) string {
	var out []string
	list := false
	for _, l := range strings.Split(text, "\n") {
		if markLine.MatchString(l) {
			continue
		}
		if bareMark.MatchString(l) {
			list = true
			continue
		}
		if list {
			if bullet.MatchString(l) {
				continue
			}
			if strings.TrimSpace(l) != "" {
				list = false
			}
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func (r *report) markdown(c *contract, ci *castItem, state, reason string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s, on %q\n\n", ci.Name, c.Target)
	fmt.Fprintf(&b, "Contract %s · %s · %s · %.0f coins\n\n", c.ID, ci.Kind, state, ci.CoinsMin)
	if state == "failed" && reason != "" {
		fmt.Fprintf(&b, "**Failed: %s.** Anything below wasn't seen working the app; don't take it as tested.\n\n", reason)
	}
	fmt.Fprintf(&b, "## Findings\n\n")
	if len(r.findings) == 0 {
		b.WriteString("None written down.\n")
	}
	for _, f := range r.findings {
		fmt.Fprintf(&b, "- %s\n", f)
	}
	fmt.Fprintf(&b, "\n## Could not reach\n\n")
	if len(r.unreached) == 0 {
		b.WriteString("Nothing said.\n")
	}
	for _, u := range r.unreached {
		fmt.Fprintf(&b, "- %s\n", u)
	}
	// What its own plan said it would check and it never answered: for the
	// reader, and for the Director, which splits a professional that keeps
	// being stopped with its plan half done.
	if open := r.unanswered(); len(open) > 0 {
		fmt.Fprintf(&b, "\n## Not covered (in their plan, never answered)\n\n")
		for _, o := range open {
			fmt.Fprintf(&b, "- %s\n", o)
		}
	}
	fmt.Fprintf(&b, "\n## Their report\n\n")
	switch {
	case prose(r.final) != "":
		b.WriteString(prose(r.final) + "\n")
	case state == "out":
		b.WriteString("Stopped at the limit before writing one. The findings above are what they wrote down as they went.\n")
	case state == "closed":
		b.WriteString("Stopped by close before writing one. The findings above are what they wrote down as they went.\n")
	default:
		b.WriteString("None.\n")
	}
	return b.String()
}

// writeSummary puts every report in one file, with the open rooms at the
// end: the Director's next brief. Checking each finding against the code is
// the skill's job, after this.
func writeSummary(c *contract, dir string) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "# Contract %s: %s\n\n", c.ID, c.Target)
	fmt.Fprintf(&b, "Placed %s by %s, in %s.\n\n", c.PlacedAt.Local().Format(time.DateTime), c.PlacedBy, c.Project)
	fmt.Fprintf(&b, "| professional | kind | outcome | findings | unreached |\n|---|---|---|---|---|\n")
	total, failed := 0, 0
	for _, p := range c.Professionals {
		s := readStatus(dir, p.Name)
		outcome := s.State
		if s.Reason != "" {
			outcome += ": " + s.Reason
		}
		if s.Blocked > 0 {
			outcome += fmt.Sprintf(" (the gate turned away %d requests; see gate.log)", s.Blocked)
		}
		if s.Leftovers != "" {
			outcome += "; " + s.Leftovers
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %d | %d |\n", p.Name, p.Kind, strings.ReplaceAll(outcome, "|", "/"), s.Findings, s.Unreached)
		if s.State == "failed" {
			failed++
			continue
		}
		total += s.Findings
	}
	fmt.Fprintf(&b, "\n%d findings in all, none checked yet. Each one is checked against the code before it is reported as real.\n", total)
	if failed > 0 {
		fmt.Fprintf(&b, "%d professionals failed; their findings aren't in that count, and their part of the contract wasn't done.\n", failed)
	}
	if c.Changes != nil {
		b.WriteString(releaseVerdict(c, dir, failed))
	}
	// One-off specialists who found something joined the roster at close,
	// so the next contract casts them rather than training them again.
	if len(c.Kept) > 0 {
		fmt.Fprintf(&b, "\nOn the roster now, for the next contract (one-off specialists who found something; commit their files to share them, delete one to let it go):\n")
		for _, n := range c.Kept {
			fmt.Fprintf(&b, "- %s: .administration/professionals/%s.md\n", n, n)
		}
	}
	if len(c.NotKept) > 0 {
		fmt.Fprintf(&b, "\nNot put on the roster:\n")
		for _, n := range c.NotKept {
			fmt.Fprintf(&b, "- %s\n", n)
		}
	}
	for _, p := range c.Professionals {
		rp, err := os.ReadFile(filepath.Join(dir, p.Name, "report.md"))
		if err != nil {
			fmt.Fprintf(&b, "\n---\n\n# %s\n\nNo report: %s\n", p.Name, readStatus(dir, p.Name).Reason)
			continue
		}
		fmt.Fprintf(&b, "\n---\n\n%s", rp)
	}
	if v, err := loadRoster(c.Project); err == nil && len(v.Open) > 0 {
		fmt.Fprintf(&b, "\n---\n\n# Open rooms\n\nNobody on the roster holds these yet. If this contract needed one, the Director can train it (/hire):\n\n")
		for _, o := range v.Open {
			if o.Says != "" {
				fmt.Fprintf(&b, "- **%s**: %s\n", o.Name, o.Says)
			} else {
				fmt.Fprintf(&b, "- **%s**\n", o.Name)
			}
		}
	}
	path := filepath.Join(dir, "summary.md")
	return path, os.WriteFile(path, []byte(b.String()), 0o600)
}

var (
	wordRE = regexp.MustCompile(`[a-z0-9_]{4,}`)
	refRE  = regexp.MustCompile(`[\w./-]+\.\w+:\d+`)
)

// sameFinding says whether b restates a: most of the shorter one's words
// are in the other, or they cite the same file and line and share half.
// Tuned on a round of real reports: it merged every restatement there and
// none of the findings that only looked alike (the same bug in two files).
func sameFinding(a, b string) bool {
	wa, wb := wordSet(a), wordSet(b)
	if len(wa) == 0 || len(wb) == 0 {
		return false
	}
	shared := 0
	for w := range wa {
		if wb[w] {
			shared++
		}
	}
	c := float64(shared) / float64(min(len(wa), len(wb)))
	if c >= 0.75 {
		return true
	}
	if c < 0.5 {
		return false
	}
	for _, x := range refRE.FindAllString(a, -1) {
		for _, y := range refRE.FindAllString(b, -1) {
			if x == y {
				return true
			}
		}
	}
	return false
}

func wordSet(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range wordRE.FindAllString(strings.ToLower(s), -1) {
		m[w] = true
	}
	return m
}

// releaseVerdict leads a contract on changes with what it's for: what the
// professionals say these changes broke (findings marked NEW), then what's
// wrong in the change's own text (CHANGE), then what was already so (OLD),
// with the rest below. Nothing here is checked yet;
// the session checks each before calling it real.
func releaseVerdict(c *contract, dir string, failed int) string {
	var newF, chF, oldF []string
	other := 0
	for _, p := range c.Professionals {
		rp, err := os.ReadFile(filepath.Join(dir, p.Name, "report.md"))
		if err != nil {
			continue
		}
		sec := string(rp)
		if i := strings.Index(sec, "## Findings"); i >= 0 {
			sec = sec[i:]
		}
		if j := strings.Index(sec, "## Could not reach"); j >= 0 {
			sec = sec[:j]
		}
		for _, l := range strings.Split(sec, "\n") {
			t, ok := strings.CutPrefix(l, "- ")
			if !ok || strings.TrimSpace(t) == "" || strings.HasPrefix(t, "none") {
				continue
			}
			switch u := strings.ToUpper(strings.TrimLeft(t, "*_ ")); {
			case strings.HasPrefix(u, "NEW"):
				newF = append(newF, fmt.Sprintf("- %s (%s)", t, p.Name))
			case strings.HasPrefix(u, "CHANGE"):
				chF = append(chF, fmt.Sprintf("- %s (%s)", t, p.Name))
			case strings.HasPrefix(u, "OLD"):
				oldF = append(oldF, fmt.Sprintf("- %s (%s)", t, p.Name))
			default:
				other++
			}
		}
	}
	var b strings.Builder
	b.WriteString("\n## What these changes broke\n\n")
	if len(newF) == 0 {
		b.WriteString("Nobody reported anything these changes broke.")
		if failed > 0 {
			b.WriteString(" Some professionals failed, so part of it went unchecked.")
		}
		b.WriteString("\n")
	} else {
		fmt.Fprintf(&b, "%d findings marked NEW (the professional says the change caused it; check each before calling it real):\n\n%s\n", len(newF), strings.Join(newF, "\n"))
	}
	if len(chF) > 0 {
		fmt.Fprintf(&b, "\n## Wrong in the changes themselves\n\nNothing here behaves differently from before; the change's own comments, documents or tests are wrong (CHANGE):\n\n%s\n", strings.Join(chF, "\n"))
	}
	if len(oldF) > 0 {
		fmt.Fprintf(&b, "\n## Already so before these changes\n\n%s\n", strings.Join(oldF, "\n"))
	}
	if other > 0 {
		fmt.Fprintf(&b, "\n%d more findings weren't marked either way; they're in the reports below.\n", other)
	}
	return b.String()
}
