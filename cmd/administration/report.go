package main

import (
	"slices"
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
	// FINDING: lines that read as a check that held or a plan step, not
	// something wrong: kept apart, not counted, never dropped.
	notCounted []string
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

// heldLine is a FINDING: whose body says nothing is wrong: "Held: ...",
// "No issue - ...". Only words that can't open a real defect, and then a
// colon or a spaced dash: "Holds: the lock holds forever", "Covered - the
// banner covers Pay" and "Plan 3: the Pay button is hidden" all count.
var heldLine = regexp.MustCompile(`(?i)^[*_\s]*(held|no issues?|no problems?|works as expected|not a bug)[*_]*(\s*:|\s+[-–—]\s)`)

// markRE is a release check's mark on a finding, as written: upper case and a
// word of its own, so "NEWLINE missing" and "New users see..." aren't NEW.
var markRE = regexp.MustCompile(`^[*_\s]*(NEW|CHANGE|OLD)([^A-Za-z]|$)`)

func findingTag(t string) string {
	if m := markRE.FindStringSubmatch(t); m != nil {
		return m[1]
	}
	return ""
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
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
		if kind == "FINDING" && heldLine.MatchString(body) {
			r.notCounted = append(r.notCounted, body)
		} else if kind == "FINDING" {
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
	if len(r.notCounted) > 0 {
		fmt.Fprintf(&b, "\n## Not counted\n\nMarked FINDING:, but they read as checks that held or plan steps, not something wrong:\n\n")
		for _, f := range r.notCounted {
			fmt.Fprintf(&b, "- %s\n", f)
		}
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
	total, failed, failedF := 0, 0, 0
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
		if s.EquipmentLost != "" {
			outcome += "; equipment lost at " + s.EquipmentLost + ", so part of their work wasn't done"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %d | %d |\n", p.Name, p.Kind, strings.ReplaceAll(outcome, "|", "/"), s.Findings, s.Unreached)
		total += s.Findings
		if s.State == "failed" {
			failed++
			failedF += s.Findings
		}
	}
	all, folded := gatherFindings(c, dir)
	checked := map[string]int{}
	for _, f := range all {
		if f.verdict != "" {
			checked[f.verdict]++
		}
	}
	fmt.Fprintf(&b, "\n%s in all", plural(total, "finding", "findings"))
	if failedF > 0 {
		fmt.Fprintf(&b, " (%d from professionals who failed)", failedF)
	}
	if folded := len(all); folded < total {
		fmt.Fprintf(&b, ", %d once the same claim from several is folded into one", folded)
	}
	if total == 0 {
		b.WriteString(".\n")
	} else if n := checked["confirmed"] + checked["likely"] + checked["false"]; n == 0 {
		b.WriteString(", none checked yet. The session that placed the contract checks each one against the code before calling it real, and records it with `administration verdicts`.\n")
	} else {
		fmt.Fprintf(&b, "; %d checked: %d confirmed, %d likely, %d false. Unchecked ones aren't real yet.\n", n, checked["confirmed"], checked["likely"], checked["false"])
	}
	if failed > 0 {
		fmt.Fprintf(&b, "%s failed: their part of the contract wasn't done, and what they found (marked \"failed\") wasn't seen working the app.\n", plural(failed, "professional", "professionals"))
	}
	if c.Changes != nil {
		b.WriteString(releaseVerdict(all, failed))
	} else if len(all) > 0 {
		fmt.Fprintf(&b, "\n## Findings\n\nEach with who found it and its number in their report. Several who found the same thing on their own come first.\n\n%s\n", listFindings(all))
	}
	// One-off specialists who found something joined the roster at close,
	// so the next contract casts them rather than training them again.
	if len(c.Kept) > 0 {
		fmt.Fprintf(&b, "\nOn the roster now, for the next contract (one-off specialists who found something; commit their files to share them, delete one to let it go):\n")
		for _, n := range c.Kept {
			fmt.Fprintf(&b, "- %s: %s\n", n, houseFile(c.Project, "professionals", n+".md"))
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
		// Their findings are in the list above, once each. One folded into
		// someone else's stays here in its own words, so a fold that was
		// wrong loses nothing.
		text := string(rp)
		if i := strings.Index(text, "## Findings\n"); i >= 0 {
			if j := strings.Index(text[i:], "\n## "); j >= 0 {
				own := "## Findings\n\nIn the list above."
				if f := folded[p.Name]; len(f) > 0 {
					own += " These were folded there into another's, so here they are as written:\n\n" + strings.Join(f, "\n")
				}
				text = text[:i] + own + "\n" + text[i+j:]
			}
		}
		if i := strings.Index(text, "## Their report\n"); i >= 0 {
			text = text[:i] + stripMarked(text[i:])
		}
		fmt.Fprintf(&b, "\n---\n\n%s", text)
	}
	open := c.OpenAtClose
	if c.ClosedAt == nil || open == nil {
		if v, err := loadRoster(c.Project); err == nil {
			open = v.Open
		}
	}
	if len(open) > 0 {
		fmt.Fprintf(&b, "\n---\n\n# Open rooms\n\nNobody on the roster holds these yet. If this contract needed one, the Director can train it (/hire):\n\n")
		for _, o := range open {
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
	// A file a finding cites, and the line or lines right after it, however
	// it's written: "watch.go:410", "`watch.go` new lines 410-415",
	// "README lines 30-39".
	fileRE = regexp.MustCompile(`(?:[\w.-]+/)*[\w-]+\.(?:go|md|py|sh|ts|tsx|js|jsx|mjs|json|toml|ya?ml|rb|rs|java|kt|swift|css|scss|html|sql|txt|vue|svelte|php|cs|c|h|cpp)\b|\bREADME\b|\bCHANGELOG\b`)
	lineRE = regexp.MustCompile(`^[^\d\n]{0,30}?(\d+)(?:\s*[-–]\s*(\d+))?`)
)

type ref struct {
	file   string
	lo, hi int
}

func refs(s string) []ref {
	var out []ref
	for _, m := range fileRE.FindAllStringIndex(s, -1) {
		lm := lineRE.FindStringSubmatch(s[m[1]:])
		if lm == nil {
			continue
		}
		lo, _ := strconv.Atoi(lm[1])
		hi := lo
		if lm[2] != "" {
			hi, _ = strconv.Atoi(lm[2])
		}
		out = append(out, ref{strings.TrimSuffix(s[m[0]:m[1]], ".md"), lo, max(lo, hi)})
	}
	return out
}

// sameRef: the same file (one path may be the tail of the other) and lines
// within a few of each other.
func sameRef(x, y ref) bool {
	if x.file != y.file && !strings.HasSuffix(x.file, "/"+y.file) && !strings.HasSuffix(y.file, "/"+x.file) {
		return false
	}
	return x.lo <= y.hi+3 && y.lo <= x.hi+3
}

// sameFinding says whether b restates a: most of the shorter one's words
// are in the other, or they cite the same lines of a file and share more
// than half. Tuned on two rounds of real reports: it merged the
// restatements there and none of the findings that only looked alike (the
// same bug in two files, two bugs in one function).
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
	if c < 0.55 {
		return false
	}
	for _, x := range refs(a) {
		for _, y := range refs(b) {
			if sameRef(x, y) {
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

var (
	// A heading in a model's prose: markdown, a bold line, or a short line
	// ending in a colon.
	proseHeading = regexp.MustCompile(`^\s*(#{1,6} \S.*|\*\*[^*]{2,80}\*\*:?\s*|[A-Z][^.!?]{1,60}:\s*)$`)
	listItem = regexp.MustCompile(`^\s*([-*+]|\d+[.)])\s`)
	// One whose section is findings again.
	restatedSection = regexp.MustCompile(`(?i)findings|what broke|broken|regressions?`)
)

// stripMarked is a professional's closing prose without what the summary
// already holds in order: the findings it restates (the brief asks for
// every one again, so a cut-off run loses none), the unreached, and the
// plan with its answers. What's left is what held and how they went about
// it. Their own report keeps it all.
func stripMarked(prose string) string {
	var out []string
	blank := 0
	skipping, afterBlank := false, false
	for _, l := range strings.Split(prose, "\n") {
		// A section that restates findings ("What broke:", "All findings,
		// repeated:") goes whole: its list, up to the next heading or the
		// first plain paragraph after a blank line.
		if proseHeading.MatchString(l) {
			skipping = restatedSection.MatchString(l)
			if skipping {
				continue
			}
		} else if skipping && afterBlank && strings.TrimSpace(l) != "" && !listItem.MatchString(l) && !markLine.MatchString(l) {
			skipping = false
		}
		afterBlank = strings.TrimSpace(l) == ""
		if skipping || markLine.MatchString(l) || bareMark.MatchString(l) || planLine.MatchString(l) || answerLine.MatchString(l) {
			continue
		}
		if strings.TrimSpace(l) == "" {
			if blank++; blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// summed is one claim in the summary: every professional who made it (by
// name and its number in their report, which is what verdicts takes), and
// the check it got.
type summed struct {
	text, tag string // tag: NEW, CHANGE, OLD, or "" when unmarked
	by        []string
	verdict   string
	why       string
	whose     string   // which of by the verdict was recorded on
	checker   string   // "verifier", or empty for the session
	dissent   []string // other checks of it that disagree
	links     []link   // the same bug in another contract
	shared    []string // the shared servers behind it, when everyone who saw it was on one
}

func dissentText(ref, verdict, why string) string {
	t := ref + " was checked " + verdict
	if why != "" {
		t += ": " + oneLine(why, 300)
	}
	return t
}

// gatherFindings reads each professional's findings from their report, with
// any verdicts recorded, and folds the same claim made by several (or by one,
// twice) into one. What's confirmed comes first, then what several found on
// their own; what was found false goes last. It also returns, for each
// professional, the findings folded into another's, as they wrote them.
func gatherFindings(c *contract, dir string) ([]*summed, map[string][]string) {
	var all []*summed
	folded := map[string][]string{}
	for _, p := range c.Professionals {
		rp, err := os.ReadFile(filepath.Join(dir, p.Name, "report.md"))
		if err != nil {
			continue
		}
		vs := map[int]verdict{}
		var vl []verdict
		if b, err := os.ReadFile(filepath.Join(dir, p.Name, "verdicts.json")); err == nil && json.Unmarshal(b, &vl) == nil {
			for _, v := range vl {
				vs[v.Finding] = v
			}
		}
		failed := readStatus(dir, p.Name).State == "failed"
		for n, t := range reportFindings(string(rp)) {
			ref := fmt.Sprintf("%s #%d", p.Name, n+1)
			if failed {
				ref += ", failed"
			}
			tag := findingTag(t)
			v := vs[n+1]
			var into *summed
			for _, f := range all {
				if f.tag == tag && sameFinding(f.text, t) {
					into = f
					break
				}
			}
			if into == nil {
				into = &summed{text: t, tag: tag}
				all = append(all, into)
			} else {
				folded[p.Name] = append(folded[p.Name], fmt.Sprintf("- #%d: %s", n+1, t))
			}
			into.by = append(into.by, ref)
			// Checks of a folded finding can disagree; the best one ranks
			// it, and the others are shown beside it, never dropped.
			switch {
			case v.Verdict == "":
			case into.verdict == "":
				into.verdict, into.why, into.whose, into.checker = v.Verdict, v.Why, ref, v.By
			case v.Verdict == into.verdict:
			case verdictRank(v.Verdict) < verdictRank(into.verdict):
				into.dissent = append(into.dissent, dissentText(into.whose, into.verdict, into.why))
				into.verdict, into.why, into.whose, into.checker = v.Verdict, v.Why, ref, v.By
			default:
				into.dissent = append(into.dissent, dissentText(ref, v.Verdict, v.Why))
			}
		}
	}
	ls := readLinks(dir)
	onShared := map[string][]string{}
	for _, p := range c.Professionals {
		if sh := readStatus(dir, p.Name).Shared; len(sh) > 0 {
			onShared[p.Name] = sh
		}
	}
	for _, f := range all {
		f.links = linksFor(ls, f.by)
		// Seen only against a shared server: say so, as the server may be
		// what's wrong (every device seeing it points there too).
		var hosts []string
		for _, ref := range f.by {
			sh := onShared[strings.SplitN(ref, " #", 2)[0]]
			if len(sh) == 0 {
				hosts = nil
				break
			}
			for _, h := range sh {
				if h = h[strings.Index(h, ": ")+2:]; !slices.Contains(hosts, h) {
					hosts = append(hosts, h)
				}
			}
		}
		f.shared = hosts
	}
	// Seen two ways (a confirmed link to another contract) is the strongest
	// evidence there is, after a check.
	linked := func(f *summed) int {
		for _, l := range f.links {
			if l.State == "confirmed" {
				return 0
			}
		}
		if len(f.links) > 0 {
			return 1
		}
		return 2
	}
	sort.SliceStable(all, func(i, j int) bool {
		if a, b := verdictRank(all[i].verdict), verdictRank(all[j].verdict); a != b {
			return a < b
		}
		if a, b := linked(all[i]), linked(all[j]); a != b {
			return a < b
		}
		return len(all[i].by) > len(all[j].by)
	})
	return all, folded
}

func verdictRank(v string) int {
	return map[string]int{"confirmed": 0, "likely": 1, "": 2, "false": 3}[v]
}

// reportFindings is the bullet list under a report's "## Findings".
func reportFindings(rp string) []string {
	i := strings.Index(rp, "## Findings\n")
	if i < 0 {
		return nil
	}
	sec := rp[i:]
	if j := strings.Index(sec[1:], "\n## "); j >= 0 {
		sec = sec[:j+1]
	}
	var out []string
	for _, l := range strings.Split(sec, "\n") {
		if t, ok := strings.CutPrefix(l, "- "); ok && strings.TrimSpace(t) != "" {
			out = append(out, t)
		}
	}
	return out
}

func listFindings(fs []*summed) string {
	var b strings.Builder
	for _, f := range fs {
		fmt.Fprintf(&b, "- %s (%s)", f.text, strings.Join(f.by, "; "))
		if f.verdict != "" {
			fmt.Fprintf(&b, " **[%s", f.verdict)
			if f.checker == "verifier" {
				b.WriteString(" by a verifier")
			}
			if f.why != "" {
				fmt.Fprintf(&b, ": %s", oneLine(f.why, 300))
			}
			b.WriteString("]**")
		}
		if len(f.dissent) > 0 {
			fmt.Fprintf(&b, " (but %s)", strings.Join(f.dissent, "; "))
		}
		if len(f.shared) > 0 {
			fmt.Fprintf(&b, " (seen against the shared server %s: it could be the server, not the app)", strings.Join(f.shared, ", "))
		}
		for _, l := range f.links {
			// A contract pruned since is gone from the books; say so.
			gone := ""
			if m := otherRef.FindStringSubmatch(l.To); m != nil {
				if dir, err := contractDir(m[1]); err == nil {
					if _, err := os.Stat(filepath.Join(dir, "contract.json")); err != nil {
						gone = " (pruned since)"
					}
				}
			}
			if l.State == "confirmed" {
				fmt.Fprintf(&b, "; **also seen in contract %s**%s", l.To, gone)
			} else if gone != "" {
				continue
			} else {
				fmt.Fprintf(&b, "; maybe the same as contract %s (proposed: %s)", l.To, l.Why)
			}
		}
		b.WriteString("\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// releaseVerdict leads a contract on changes with what it's for: what the
// professionals say these changes broke (findings marked NEW), then what's
// wrong in the change's own text (CHANGE), then what was already so (OLD),
// then what wasn't marked.
func releaseVerdict(all []*summed, failed int) string {
	by := map[string][]*summed{}
	for _, f := range all {
		by[f.tag] = append(by[f.tag], f)
	}
	var b strings.Builder
	b.WriteString("\n## What these changes broke\n\n")
	if newF := by["NEW"]; len(newF) == 0 {
		b.WriteString("Nobody reported anything these changes broke.")
		if failed > 0 {
			b.WriteString(" Some professionals failed, so part of it went unchecked.")
		}
		b.WriteString("\n")
	} else {
		fmt.Fprintf(&b, "%s marked NEW (the professional says the change caused it; check each before calling it real):\n\n%s\n", plural(len(newF), "finding", "findings"), listFindings(newF))
	}
	if chF := by["CHANGE"]; len(chF) > 0 {
		fmt.Fprintf(&b, "\n## Wrong in the changes themselves\n\nNothing here behaves differently from before; the change's own comments, documents or tests are wrong (CHANGE):\n\n%s\n", listFindings(chF))
	}
	if oldF := by["OLD"]; len(oldF) > 0 {
		fmt.Fprintf(&b, "\n## Already so before these changes\n\n%s\n", listFindings(oldF))
	}
	if other := by[""]; len(other) > 0 {
		fmt.Fprintf(&b, "\n## Not marked either way\n\n%s\n", listFindings(other))
	}
	return b.String()
}
