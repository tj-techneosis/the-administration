package main

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Only an http(s) page or a scheme-less host goes, and the host is written
// out as http://, so the server can't take it for a scheme.
func TestOnlyAWebPageIsNavigatedTo(t *testing.T) {
	for u, want := range map[string]string{"localhost:4100/x": "http://localhost:4100/x", "[::1]:3000/x": "http://[::1]:3000/x",
		"my_app:3000": "http://my_app:3000", "localhost": "http://localhost", "example.com/a?b=1": "http://example.com/a?b=1",
		"https://example.com/": "https://example.com/", "Web:8080": "http://Web:8080",
		// A scheme followed by digits is a host and port once written out.
		"file:0/etc/hostname": "http://file:0/etc/hostname"} {
		args := map[string]any{"url": u}
		if why := offTheWall("browser_navigate", args); why != "" || args["url"] != want {
			t.Errorf("%q: %q, %v", u, why, args["url"])
		}
	}
	for _, u := range []string{"data:1,<script>x</script>", "javascript:1;alert(1)", "view-source:127.0.0.1:1",
		"about:blank", "localhost:abc", "localhost:", "[::1", "[zz]:1", "a b:1", `\\share\x`, "localhost:1\\x", "//evil"} {
		if offTheWall("browser_navigate", map[string]any{"url": u}) == "" {
			t.Errorf("%q passed", u)
		}
	}
	// Every tool's url, not only navigate's.
	if offTheWall("browser_tabs", map[string]any{"action": "new", "url": "data:1,x"}) == "" {
		t.Error("a new tab on data: passed")
	}
	if why := offTheWall("browser_tabs", map[string]any{"action": "list"}); why != "" {
		t.Error(why)
	}
	if why := offTheWall("browser_navigate", map[string]any{"url": "localhost:1"}); !strings.Contains(offTheWall("browser_navigate", map[string]any{"url": "file:///x"}), "http://") || why != "" {
		t.Error("the refusal says how to write it")
	}
}

// An argument the screen doesn't know is held to a plain name, at any
// depth; the known ones keep their slashes.
func TestAnUnknownArgumentCarriesNoPath(t *testing.T) {
	for _, a := range []map[string]any{{"outputDir": "/tmp"}, {"saveAs": "../x"}, {"options": map[string]any{"dir": "/etc"}},
		{"list": []any{map[string]any{"to": "a/b"}}}, {"filename": 3}, {"path": []any{"x"}}} {
		if offTheWall("browser_take_screenshot", a) == "" {
			t.Errorf("%v passed", a)
		}
	}
	for _, a := range []map[string]any{{"text": "1/2"}, {"element": "Docs/Help link", "target": "e12"},
		{"fields": []any{map[string]any{"name": "Date", "value": "12/31/2026"}}}, {"other": "plain", "n": 3.0}} {
		if why := offTheWall("browser_fill_form", a); why != "" {
			t.Errorf("%v: %s", a, why)
		}
	}
}

func TestADropIsAimedOnlyByKnownNames(t *testing.T) {
	for _, k := range []string{"uri", "href", "link", "upload", "attachment", "body", "payload", "source", "location", "Target"} {
		if dropAims(k) {
			t.Errorf("%s aims a drop", k)
		}
	}
	for _, k := range []string{"element", "ref", "target", "selector"} {
		if !dropAims(k) {
			t.Errorf("%s doesn't aim a drop", k)
		}
	}
}

func TestMoreShellsAndSpellingsAreRefused(t *testing.T) {
	for _, c := range []string{"ash", "rbash", "mksh", "yash", "xonsh", "nu", "python3.12d", "python3.13td", "python3-dbg",
		"bash.exe", "view", "vimdiff", "rvim", "gvim", "gmake", "bmake", "apt-get", "apt", "bat", "most", "journalctl", "git-upload-pack"} {
		if checkCommand(c) == nil {
			t.Errorf("%q should be refused", c)
		}
	}
	for _, c := range []string{"jq", "wc", "pdftotext", "exiftool", "tool2", "mtw", "pwd", "ffprobe"} {
		if err := checkCommand(c); err != nil {
			t.Errorf("%q: %v", c, err)
		}
	}
}

func TestNothingInAGitFolderIsTheProjects(t *testing.T) {
	for _, p := range []string{".git/config.worktree", ".git/hooks/pre-commit", ".git/packed-refs", "sub/.git/modules/m/config",
		".GIT/FETCH_HEAD", ".git/worktrees/w/config"} {
		if !secret(p) {
			t.Errorf("%s isn't kept out", p)
		}
	}
	if secret(".github/workflows/ci.yml") || secret("docs/git.md") {
		t.Error(".github or a file named for git is kept out")
	}
	got := secretDenies()
	for _, want := range []string{"Read(**/.[eE][nN][vV])", "Read(//**/[iI][dD]_[rR][sS][aA]*)"} {
		if !slices.Contains(got, want) {
			t.Errorf("no %s", want)
		}
	}
}

// A filter git can't be told about with -c (its name has an =, or is the
// empty one `filter=` picks) is emptied all the same.
func TestAFilterOfAnyNameRunsNever(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	writeT(t, filepath.Join(project, "x.txt"), "a\n")
	writeT(t, filepath.Join(project, "y.txt"), "a\n")
	writeT(t, filepath.Join(project, ".gitattributes"), "x.txt filter=a=b\ny.txt filter=\n")
	gitT(t, project, "init", "-q")
	gitT(t, project, "add", "-A")
	gitT(t, project, "commit", "-qm", "i")
	cfg, err := os.OpenFile(filepath.Join(project, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	m1, m2 := filepath.Join(root, "ran-ab"), filepath.Join(root, "ran-empty")
	_, _ = cfg.WriteString("[filter \"a=b\"]\n\tclean = touch '" + m1 + "'; cat\n[filter \"\"]\n\tclean = touch '" + m2 + "'; cat\n")
	cfg.Close()
	writeT(t, filepath.Join(project, "x.txt"), "b\n")
	writeT(t, filepath.Join(project, "y.txt"), "b\n")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.abbrev")
	t.Setenv("GIT_CONFIG_VALUE_0", "9")
	if out, err := gitRaw(project, "diff", "--stat"); err != nil || !strings.Contains(out, "x.txt") {
		t.Fatalf("%v %q", err, out)
	}
	for _, m := range []string{m1, m2} {
		if _, err := os.Stat(m); err == nil {
			t.Errorf("%s ran", filepath.Base(m))
		}
	}
	if abbrev, _ := git(project, "config", "core.abbrev"); abbrev != "9" {
		t.Errorf("the person's own GIT_CONFIG_* are lost: %q", abbrev)
	}
}

// fakeServer answers the screen's own calls as a browser server would,
// with answer, until it is closed.
func fakeScreen(t *testing.T, answer func(args map[string]any) (string, bool)) (*screen, *io.PipeWriter, *[]string) {
	toChildR, toChildW := io.Pipe()
	fromChildR, fromChildW := io.Pipe()
	outR, outW := io.Pipe()
	go func() { _, _ = io.Copy(io.Discard, outR) }()
	s := &screen{toChild: toChildW, out: json.NewEncoder(outW), waiting: map[string]chan rpc{}}
	go s.readChild(fromChildR)
	var pressed []string
	go func() {
		sc := bufio.NewScanner(toChildR)
		for sc.Scan() {
			var m rpc
			_ = json.Unmarshal(sc.Bytes(), &m)
			var p struct {
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(m.Params, &p)
			pressed = append(pressed, p.Arguments["key"].(string))
			text, bad := answer(p.Arguments)
			res, _ := json.Marshal(map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "isError": bad})
			b, _ := json.Marshal(rpc{JSONRPC: "2.0", ID: m.ID, Result: res})
			_, _ = fromChildW.Write(append(b, '\n'))
		}
	}()
	return s, fromChildW, &pressed
}

func TestTypingStopsAtAKeyThatFailed(t *testing.T) {
	s, _, pressed := fakeScreen(t, func(a map[string]any) (string, bool) {
		if a["key"] == "€" {
			return "Unknown key: €", true
		}
		return "ok", false
	})
	var got rpc
	s.out = nil
	r, w := io.Pipe()
	s.out = json.NewEncoder(w)
	done := make(chan struct{})
	go func() { _ = json.NewDecoder(r).Decode(&got); close(done) }()
	params, _ := json.Marshal(map[string]any{"name": "browser_type_text", "arguments": map[string]any{"text": "a\r\nb€cd"}})
	go s.call(rpc{ID: json.RawMessage("7"), Method: "tools/call", Params: params})
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("no answer")
	}
	if !isError(got) || !strings.Contains(resultText(got), "character 5 of 7") {
		t.Errorf("got %s", got.Result)
	}
	if strings.Join(*pressed, ",") != "a,Enter,b,€" {
		t.Errorf("pressed %v", *pressed)
	}
}

func TestAServerThatGoesFailsTheCallsWaitingOnIt(t *testing.T) {
	s, fromChild, _ := fakeScreen(t, func(map[string]any) (string, bool) { return "ok", false })
	s.wmu.Lock()
	ch := make(chan rpc, 1)
	s.waiting[`"administration-screen-99"`] = ch
	s.wmu.Unlock()
	fromChild.Close()
	select {
	case r := <-ch:
		if r.Error == nil {
			t.Error("answered without an error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the waiting call was never answered")
	}
	done := make(chan rpc)
	go func() { done <- s.ask("browser_press_key", map[string]any{"key": "a"}) }()
	select {
	case r := <-done:
		if r.Error == nil {
			t.Error("a call after it went got no error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a call after the server went waits for ever")
	}
}
