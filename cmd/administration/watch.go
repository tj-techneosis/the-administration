package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// watch follows what professionals are doing as they do it: every tool call,
// what they say, each FINDING as it's written. It reads the streams the
// workers already keep in the books, so watching changes nothing about the
// work and works anywhere a terminal does. With --panes it opens a pane per
// professional in Herdr or tmux instead, each running watch for one name.
func cmdWatch(args []string) error {
	f, err := parseFlags(args, map[string]bool{"panes": false})
	if err != nil {
		return err
	}
	if len(f.pos) < 1 || len(f.pos) > 2 {
		return usageError{"watch ID [NAME] [--panes]"}
	}
	c, dir, err := loadContract(f.pos[0])
	if err != nil {
		return err
	}
	names := make([]string, 0, len(c.Professionals))
	for _, p := range c.Professionals {
		if len(f.pos) == 1 || p.Name == f.pos[1] {
			names = append(names, p.Name)
		}
	}
	if len(names) == 0 {
		return usageError{fmt.Sprintf("%s isn't on contract %s", f.pos[1], c.ID)}
	}
	if f.has("panes") {
		return openPanes(c.ID, names)
	}
	return follow(os.Stdout, dir, names, pollEvery(), isTTY(os.Stdout))
}

// roomSecrets are the passwords in a professional's rooms (its manifests),
// so a watch line never shows one, whatever field it was typed into.
func roomSecrets(pd string) []string {
	var out []string
	for _, f := range []string{"room.json", "room-before.json"} {
		var m manifest
		if b, err := os.ReadFile(filepath.Join(pd, f)); err == nil && json.Unmarshal(b, &m) == nil {
			for _, r := range m.Roles {
				if len(r.Password) >= 4 {
					out = append(out, r.Password)
				}
			}
		}
	}
	return out
}

func redact(line string, secrets []string) string {
	for _, sec := range secrets {
		line = strings.ReplaceAll(line, sec, "[password]")
	}
	return line
}

// follow prints each professional's stream as it grows, until everyone is
// in a terminal state and every stream is read to the end.
func follow(w io.Writer, dir string, names []string, every time.Duration, color bool) error {
	short := shortNames(names)
	offs := make(map[string]int64, len(names))
	part := make(map[string][]byte, len(names))
	last := make(map[string]string, len(names))
	seen := make(map[string]map[string]bool, len(names))
	for {
		done := true
		for i, n := range names {
			// The status first, the stream after: a state read after the
			// stream could claim back before the last lines are printed.
			s := readStatus(dir, n)
			b, _ := readFrom(filepath.Join(dir, n, "stream.jsonl"), offs[n])
			offs[n] += int64(len(b))
			buf := append(part[n], b...)
			for {
				j := bytes.IndexByte(buf, '\n')
				if j < 0 {
					break
				}
				secrets := roomSecrets(filepath.Join(dir, n))
				for _, l := range watchLines(buf[:j]) {
					l = redact(l, secrets)
					// A closing report repeats the findings already shown.
					if strings.HasPrefix(l, "★ ") {
						if seen[n] == nil {
							seen[n] = map[string]bool{}
						}
						if seen[n][l] {
							continue
						}
						seen[n][l] = true
					}
					fmt.Fprintln(w, paint(color, i, short[n], l))
				}
				buf = buf[j+1:]
			}
			part[n] = buf
			if s.State != last[n] {
				fmt.Fprintln(w, paint(color, i, short[n], "· "+s.State+describeWatch(s)))
				last[n] = s.State
			}
			if !s.terminal() || len(b) > 0 {
				done = false
			}
		}
		if done {
			return nil
		}
		time.Sleep(every)
	}
}

func describeWatch(s status) string {
	if s.Reason != "" {
		return ": " + s.Reason
	}
	return ""
}

func readFrom(path string, off int64) ([]byte, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	if _, err := fh.Seek(off, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(fh)
}

// watchLines turns one stream event into what a person watching wants:
// the action and its target, the first line of what was said, every
// FINDING line in full, and errors.
func watchLines(line []byte) []string {
	var ev struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
		Turns   int    `json:"num_turns"`
		Message struct {
			Content []struct {
				Type    string          `json:"type"`
				Text    string          `json:"text"`
				Name    string          `json:"name"`
				Input   map[string]any  `json:"input"`
				IsError bool            `json:"is_error"`
				Content json.RawMessage `json:"content"`
			} `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &ev) != nil {
		return nil
	}
	var out []string
	switch ev.Type {
	case "assistant":
		for _, c := range ev.Message.Content {
			switch c.Type {
			case "text":
				said := false
				for _, l := range strings.Split(c.Text, "\n") {
					l = strings.TrimSpace(l)
					if m := markLine.FindStringSubmatch(l); m != nil {
						out = append(out, "★ "+strings.ToUpper(m[1])+": "+m[2])
					} else if l != "" && !said {
						out = append(out, "  "+clip(l, 140))
						said = true
					}
				}
			case "tool_use":
				out = append(out, "→ "+toolLabel(c.Name, c.Input))
			}
		}
	case "user":
		for _, c := range ev.Message.Content {
			if c.Type == "tool_result" && c.IsError {
				out = append(out, "  ✗ "+clip(firstLine(toolText(c.Content)), 140))
			}
		}
	case "result":
		out = append(out, fmt.Sprintf("■ finished (%s, %d turns)", ev.Subtype, ev.Turns))
	}
	return out
}

// The input field worth showing for each tool, in order of preference.
// A click's target is usually a snapshot ref ("e15"), which says nothing,
// so it shows only when it's a selector someone can read.
var watchFields = []string{"url", "element", "target", "file_path", "path", "pattern", "command", "text", "key", "width"}

var snapshotRef = regexp.MustCompile(`^e\d+$`)

func toolLabel(name string, in map[string]any) string {
	name = strings.TrimPrefix(name, "mcp__playwright__browser_")
	if rest, ok := strings.CutPrefix(name, "mcp__eq_"); ok {
		// mcp__eq_linux_cli__run_mytool: the equipment's tool is enough.
		if i := strings.Index(rest, "__"); i >= 0 {
			name = rest[i+2:]
		}
	}
	name = strings.TrimPrefix(name, "mcp__")
	// The command server's calls: the arguments are the whole story.
	if as, ok := in["args"].([]any); ok {
		parts := make([]string, len(as))
		for i, a := range as {
			parts[i] = fmt.Sprint(a)
			if strings.ContainsAny(parts[i], " \t") {
				parts[i] = strconv.Quote(parts[i])
			}
		}
		return name + " " + clip(strings.Join(parts, " "), 140)
	}
	// A form's field names, never its values: those include the room's logins.
	if fs, ok := in["fields"].([]any); ok {
		var ns []string
		for _, f := range fs {
			if m, ok := f.(map[string]any); ok && m["name"] != nil {
				ns = append(ns, fmt.Sprint(m["name"]))
			}
		}
		return name + " " + clip(strings.Join(ns, ", "), 120)
	}
	if t, ok := in["text"]; ok && t != nil {
		where := strings.ToLower(fmt.Sprint(in["element"], in["target"]))
		if strings.Contains(where, "pass") || strings.Contains(where, "secret") || strings.Contains(where, "pin") {
			return name + " (hidden: a password field)"
		}
	}
	for _, k := range watchFields {
		if k == "target" {
			if t, _ := in[k].(string); snapshotRef.MatchString(t) {
				continue
			}
		}
		if v, ok := in[k]; ok {
			s := fmt.Sprint(v)
			if strings.HasPrefix(s, "/") && (k == "file_path" || k == "path") {
				s = tailPath(s)
			}
			if k == "width" {
				s = fmt.Sprintf("%vx%v", v, in["height"])
			}
			return name + " " + clip(strings.Join(strings.Fields(s), " "), 120)
		}
	}
	return name
}

// firstLine is the first line that says something: Playwright's errors
// open with a bare "### Error" heading.
func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(strings.TrimLeft(l, "#")); l != "" && l != "Error" {
			return l
		}
	}
	return "Error"
}

// tailPath keeps the last three parts of an absolute path: the room and
// worktree folders in front say nothing to someone watching.
func tailPath(p string) string {
	parts := strings.Split(p, "/")
	if len(parts) <= 4 {
		return p
	}
	return "…/" + strings.Join(parts[len(parts)-3:], "/")
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// shortNames drops what every name shares (an owner prefix, a kind suffix),
// so the column says what differs.
func shortNames(names []string) map[string]string {
	pre, suf := "", ""
	if len(names) > 1 {
		pre, suf = names[0], names[0]
		for _, n := range names[1:] {
			for !strings.HasPrefix(n, pre) {
				pre = pre[:len(pre)-1]
			}
			for !strings.HasSuffix(n, suf) {
				suf = suf[1:]
			}
		}
		// Cut at a dash, so "acme-shop-p" doesn't eat the start of "phone".
		pre = pre[:strings.LastIndex(pre, "-")+1]
		if i := strings.Index(suf, "-"); i >= 0 {
			suf = suf[i:]
		} else {
			suf = ""
		}
	}
	out := make(map[string]string, len(names))
	if len(names) == 1 {
		out[names[0]] = ""
		return out
	}
	for _, n := range names {
		s := strings.TrimSuffix(strings.TrimPrefix(n, pre), suf)
		if s == "" {
			s = n
		}
		out[n] = s
	}
	return out
}

var watchColors = []string{"36", "33", "35", "32", "34", "31", "96", "93", "95", "92"}

func paint(color bool, i int, name, text string) string {
	if name == "" {
		return text // one professional, in a pane that already carries the name
	}
	if !color {
		return name + " │ " + text
	}
	return "\x1b[" + watchColors[i%len(watchColors)] + "m" + name + "\x1b[0m │ " + text
}

func isTTY(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// openPanes gives each professional a pane of its own, in a new Herdr tab
// or tmux window, each running watch for that one name. The panes are only
// viewers: closing one stops nothing.
func openPanes(id string, names []string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	var env []string
	for _, k := range []string{"ADMINISTRATION_HOME", "XDG_DATA_HOME"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	cmdFor := func(n string) string {
		return shellQuote(self) + " watch " + id + " " + shellQuote(n) + "; echo; echo '(done; close this pane when you like)'; exec cat >/dev/null"
	}
	switch {
	case os.Getenv("HERDR_ENV") == "1":
		return herdrPanes(id, names, env, cmdFor)
	case os.Getenv("TMUX") != "":
		return tmuxPanes(id, names, env, cmdFor)
	}
	return fmt.Errorf("--panes needs Herdr or tmux; outside them, run administration watch %s in a terminal of its own (VS Code's will do)", id)
}

func herdrPanes(id string, names, env []string, cmdFor func(string) string) error {
	run := func(args ...string) (map[string]any, error) {
		out, err := exec.Command("herdr", args...).Output()
		if err != nil {
			return nil, fmt.Errorf("herdr %s: %v", args[0], err)
		}
		var v map[string]any
		_ = json.Unmarshal(out, &v)
		return v, nil
	}
	targs := []string{"tab", "create", "--label", "contract " + id, "--no-focus"}
	for _, e := range env {
		targs = append(targs, "--env", e)
	}
	t, err := run(targs...)
	if err != nil {
		return err
	}
	pane := dig(t, "result", "root_pane", "pane_id")
	if pane == "" {
		return fmt.Errorf("herdr tab create gave no pane")
	}
	panes := []string{pane}
	for i := 1; i < len(names); i++ {
		// Halve the biggest pane each time, across then down, for a grid.
		from := panes[(i-1)/2]
		dir := "right"
		if i%2 == 0 {
			dir = "down"
		}
		sargs := []string{"pane", "split", from, "--direction", dir, "--no-focus"}
		for _, e := range env {
			sargs = append(sargs, "--env", e)
		}
		p, err := run(sargs...)
		if err != nil {
			return err
		}
		if pane = dig(p, "result", "pane", "pane_id"); pane == "" {
			return fmt.Errorf("herdr pane split gave no pane")
		}
		panes = append(panes, pane)
	}
	for i, n := range names {
		_, _ = run("pane", "rename", panes[i], n)
		if _, err := run("pane", "run", panes[i], cmdFor(n)); err != nil {
			return err
		}
	}
	fmt.Printf("Watching in Herdr: tab \"contract %s\", a pane per professional (not focused; switch to it when you like).\n", id)
	return nil
}

func tmuxPanes(id string, names, env []string, cmdFor func(string) string) error {
	var eargs []string
	for _, e := range env {
		eargs = append(eargs, "-e", e)
	}
	args := append([]string{"new-window", "-d", "-P", "-F", "#{window_id}", "-n", "contract-" + id}, eargs...)
	out, err := exec.Command("tmux", append(args, cmdFor(names[0]))...).Output()
	if err != nil {
		return fmt.Errorf("tmux new-window: %v", err)
	}
	win := strings.TrimSpace(string(out))
	for _, n := range names[1:] {
		args := append([]string{"split-window", "-d", "-t", win}, eargs...)
		if err := exec.Command("tmux", append(args, cmdFor(n))...).Run(); err != nil {
			return fmt.Errorf("tmux split-window: %v", err)
		}
		_ = exec.Command("tmux", "select-layout", "-t", win, "tiled").Run()
	}
	fmt.Printf("Watching in tmux: window contract-%s, a pane per professional (not focused).\n", id)
	return nil
}

func dig(v map[string]any, keys ...string) string {
	var cur any = v
	for _, k := range keys {
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = m[k]
	}
	s, _ := cur.(string)
	return s
}
