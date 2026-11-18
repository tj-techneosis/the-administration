package main

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRefusedCommandsIgnoreCaseAndVersion(t *testing.T) {
	refused := []string{"Bash", "PYTHON3", "/bin/SH", "Perl", "/usr/bin/Env", "python3.12m", "python3.13t", "pythonw",
		"Rscript", "R", "mawk", "nawk", "luajit", "irb", "cmake", "ninja", "gradle", "mvn", "rake", "bundle", "meson",
		"rlwrap", "entr", "convert", "magick", "hg", "m4", "dc", "ld-musl-x86_64.so.1", "sqlite3"}
	allowed := []string{"jq", "wc", "head", "./bin/mytool", "pdftotext", "mtw", "file", "exiftool", "tool2"}
	for _, c := range refused {
		if checkCommand(c) == nil {
			t.Errorf("%q should be refused", c)
		}
	}
	for _, c := range allowed {
		if err := checkCommand(c); err != nil {
			t.Errorf("%q should be allowed: %v", c, err)
		}
	}
}

func TestTheDbPortIsNeverAnOrigin(t *testing.T) {
	names, ports := []string{"web", "api", "db", "aux"}, []int{4100, 4101, 4102, 4103}
	ok := &manifest{URL: "http://127.0.0.1:4100/", Origins: []string{"http://127.0.0.1:4101", "http://localhost:4103"}, Data: "seed"}
	if err := checkManifest(ok, names, ports, "", nil); err != nil {
		t.Errorf("web, api and aux: %v", err)
	}
	bad := &manifest{URL: "http://127.0.0.1:4100/", Origins: []string{"http://127.0.0.1:4102"}, Data: "seed"}
	if err := checkManifest(bad, names, ports, "", nil); err == nil || !strings.Contains(err.Error(), "db port") {
		t.Errorf("an origin on the db port: %v", err)
	}
}

func TestTheScreenKeepsFilesNamedAndPagesOnTheWeb(t *testing.T) {
	for _, u := range []string{"http://127.0.0.1:4100/", "HTTPS://localhost:4100", "localhost:4100/x", "127.0.0.1:4100", "localhost"} {
		if why := offTheWall("browser_navigate", map[string]any{"url": u}); why != "" {
			t.Errorf("%q refused: %s", u, why)
		}
	}
	for _, u := range []string{"file:///etc/hostname", "FILE:///etc/hostname", "view-source:file:///etc/hostname", "chrome://version",
		"data:text/html,hi", "javascript:alert(1)", "about:blank", "/etc/hostname", "blob:http://127.0.0.1:4100/x", ""} {
		if offTheWall("browser_navigate", map[string]any{"url": u}) == "" {
			t.Errorf("%q passed", u)
		}
	}
	for _, f := range []string{"/etc/hosts", "../app/x.png", "shots/x.png", "..", `..\x.png`} {
		if offTheWall("browser_take_screenshot", map[string]any{"filename": f}) == "" {
			t.Errorf("filename %q passed", f)
		}
	}
	if offTheWall("browser_evaluate", map[string]any{"function": "() => 1", "filename": []any{"x"}}) == "" {
		t.Error("a filename that isn't text passed")
	}
	for _, a := range []map[string]any{{"filename": "page.png"}, {"filename": ""}, {"type": "png"}, {"xpath": "//a/b"}} {
		if why := offTheWall("browser_take_screenshot", a); why != "" {
			t.Errorf("%v refused: %s", a, why)
		}
	}
}

func TestAReaderDropTakesOnlyWhereToDrop(t *testing.T) {
	server := []map[string]any{{"name": "browser_drop", "inputSchema": map[string]any{
		"required": []any{"target", "filePath"},
		"properties": map[string]any{"element": map[string]any{}, "target": map[string]any{}, "paths": map[string]any{},
			"data": map[string]any{}, "filePath": map[string]any{}, "files": map[string]any{}, "sourceDir": map[string]any{}}}}}
	tools := readerTools(server)
	props := tools[1]["inputSchema"].(map[string]any)["properties"].(map[string]any)
	if len(props) != 3 || props["element"] == nil || props["target"] == nil || props["names"] == nil {
		t.Errorf("drop properties = %v", props)
	}
	if req := tools[1]["inputSchema"].(map[string]any)["required"].([]string); strings.Join(req, ",") != "names,target" {
		t.Errorf("required = %v", req)
	}
	if keys := dropAimKeys(server); len(keys) != 2 {
		t.Errorf("aim keys = %v", keys)
	}
}

func TestPlacedByInAGitHookIsNotTheCommitsAuthor(t *testing.T) {
	t.Setenv("ADMINISTRATION_PLACED_BY", "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("USER", "sam")
	t.Setenv("GIT_AUTHOR_NAME", "Alex")
	t.Setenv("GIT_INDEX_FILE", "")
	if got := placedBy(); got != "Alex" {
		t.Errorf("placedBy = %q", got)
	}
	t.Setenv("GIT_INDEX_FILE", ".git/index")
	if got := placedBy(); got != "user:sam" {
		t.Errorf("in a hook, placedBy = %q", got)
	}
}

func TestADeclaredProgramsLimitEndsWhatItStarted(t *testing.T) {
	old := declaredLimit
	declaredLimit = time.Second
	defer func() { declaredLimit = old }()
	dir := t.TempDir()
	prog := filepath.Join(dir, "run")
	// A child that keeps the output open long after the limit.
	if err := os.WriteFile(prog, []byte("#!/bin/sh\nsleep 60 &\nsleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	out, failed := runDeclared(prog, nil, dir)
	if d := time.Since(start); d > 20*time.Second {
		t.Errorf("took %s", d)
	}
	if !failed || !strings.Contains(out, "was stopped") {
		t.Errorf("%v %q", failed, out)
	}
	// One that ends at once but leaves a child holding its output.
	if err := os.WriteFile(prog, []byte("#!/bin/sh\necho hi\nsleep 60 &\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	declaredLimit = time.Minute
	start = time.Now()
	out, failed = runDeclared(prog, nil, dir)
	if d := time.Since(start); d > 20*time.Second {
		t.Errorf("a left child held the call %s", d)
	}
	if failed || !strings.HasPrefix(out, "exit 0\nhi") {
		t.Errorf("%v %q", failed, out)
	}
}

func TestADeclaredProgramGetsNoClaudeSignIn(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "tok")
	t.Setenv("ADMINISTRATION_TAG", "x/pro")
	dropClaudeEnv()
	dir := t.TempDir()
	out, _ := runDeclared("/usr/bin/env", nil, dir)
	if strings.Contains(out, "sk-test") || strings.Contains(out, "CLAUDE_CODE_OAUTH_TOKEN") {
		t.Errorf("the sign-in reached it: %q", out)
	}
	if !strings.Contains(out, "ADMINISTRATION_TAG=x/pro") {
		t.Errorf("the tag didn't: %q", out)
	}
}

func TestOtherRoomsNamesThisContractsLaterRooms(t *testing.T) {
	rooms := t.TempDir()
	c := &contract{ID: "ab12", Professionals: []castItem{{Name: "a", Kind: "user"}, {Name: "b", Kind: "critic"}, {Name: "c", Kind: "user"}}}
	room := roomPath(rooms, c, "a")
	if err := os.MkdirAll(room, 0o700); err != nil {
		t.Fatal(err)
	}
	got := otherRooms(c, room)
	want := filepath.Join(rooms, "ab12", "r3")
	if len(got) != 1 || got[0] != want {
		t.Errorf("otherRooms = %v, want [%s]", got, want)
	}
}

func TestACriticNeverFallsBackToTheLiveProject(t *testing.T) {
	t.Setenv("ADMINISTRATION_HOME", t.TempDir())
	project := t.TempDir()
	// A commit the project doesn't have: its copy can't be made.
	c := &contract{ID: "ab12", Project: project, Commit: strings.Repeat("a", 40)}
	if cwd, why := criticCopy(c, t.TempDir()); cwd != "" || !strings.Contains(why, "a critic reads only that") {
		t.Errorf("got %q, %q", cwd, why)
	}
	// One with no commit reads the project, and says so.
	c.Commit = ""
	if cwd, why := criticCopy(c, t.TempDir()); cwd != project || !strings.Contains(why, "recorded no commit") {
		t.Errorf("got %q, %q", cwd, why)
	}
}

func TestSecretDeniesHoldOutsideTheWorkingDirectoryAndInAnyCase(t *testing.T) {
	got := secretDenies("/srv/house")
	for _, want := range []string{"Read(**/.env)", "Read(//**/.env)", "Read(//**/.ENV)", "Read(//**/*.PEM)",
		"Read(//**/.administration/env/**)", "Read(//srv/house/env/**)"} {
		if !slices.Contains(got, want) {
			t.Errorf("no %s", want)
		}
	}
}

func TestOnlyAKeptRoomNamesTheMachineByItsOwnAddress(t *testing.T) {
	names, ports := []string{"web", "api", "db"}, []int{4100, 4101, 4102}
	// This machine, as the test has it: loopback and a tailnet address.
	mine := "100.64.0.2"
	defer func(f func() ([]net.Addr, error)) { interfaceAddrs = f }(interfaceAddrs)
	interfaceAddrs = func() ([]net.Addr, error) {
		return []net.Addr{&net.IPNet{IP: net.ParseIP("127.0.0.1"), Mask: net.CIDRMask(8, 32)},
			&net.IPNet{IP: net.ParseIP(mine), Mask: net.CIDRMask(32, 32)}}, nil
	}
	own := &manifest{URL: "http://127.0.0.1:4100/", Origins: []string{"http://" + mine + ":4101"}, Data: "seed"}
	if err := checkManifest(own, names, ports, "", &keptRoom{}); err != nil {
		t.Errorf("a kept room on this machine's own %s: %v", mine, err)
	}
	if err := checkManifest(own, names, ports, "", nil); err == nil || !strings.Contains(err.Error(), "only reach their own room") {
		t.Errorf("a contract's room on %s: %v", mine, err)
	}
	db := &manifest{URL: "http://" + mine + ":4102/", Data: "seed"}
	if err := checkManifest(db, names, ports, "", &keptRoom{}); err == nil || !strings.Contains(err.Error(), "db port") {
		t.Errorf("a kept room's origin on the db port: %v", err)
	}
	other := &manifest{URL: "http://" + mine + ":4999/", Data: "seed"}
	if err := checkManifest(other, names, ports, "", &keptRoom{}); err == nil || !strings.Contains(err.Error(), "this room's ports") {
		t.Errorf("a kept room's origin on another port: %v", err)
	}
	// Names, resolved by the test: one that is this machine alone is
	// accepted for a kept room; one with a foreign address too is not.
	defer func(f func(context.Context, string) ([]net.IPAddr, error)) { lookupIP = f }(lookupIP)
	lookupIP = func(_ context.Context, h string) ([]net.IPAddr, error) {
		switch h {
		case "me.example":
			return []net.IPAddr{{IP: net.ParseIP(mine)}}, nil
		case "both.example":
			return []net.IPAddr{{IP: net.ParseIP(mine)}, {IP: net.ParseIP("192.0.2.10")}}, nil
		case "away.example":
			return []net.IPAddr{{IP: net.ParseIP("192.0.2.10")}}, nil
		}
		return nil, errors.New("no such host")
	}
	named := &manifest{URL: "http://me.example:4100/", Data: "seed"}
	if err := checkManifest(named, names, ports, "", &keptRoom{}); err != nil {
		t.Errorf("a kept room on a name for this machine: %v", err)
	}
	if err := checkManifest(named, names, ports, "", nil); err == nil || !strings.Contains(err.Error(), "only reach their own room") {
		t.Errorf("a contract's room on a name for this machine: %v", err)
	}
	for _, h := range []string{"both.example", "away.example", "gone.example"} {
		m := &manifest{URL: "http://" + h + ":4100/", Data: "seed"}
		if err := checkManifest(m, names, ports, "", &keptRoom{}); err == nil || !strings.Contains(err.Error(), "not on this machine") {
			t.Errorf("a kept room on %s: %v", h, err)
		}
	}
	for _, h := range []string{"192.0.2.10", "0.0.0.0", "example.invalid"} {
		away := &manifest{URL: "http://" + h + ":4100/", Data: "seed"}
		if err := checkManifest(away, names, ports, "", &keptRoom{}); err == nil || !strings.Contains(err.Error(), "not on this machine") {
			t.Errorf("a kept room on %s: %v", h, err)
		}
	}
}

func TestAKeptRoomsPublishedPortsAreItsOwnAndNoOneElses(t *testing.T) {
	names, ports := []string{"web", "api", "db"}, []int{34100, 34101, 34102}
	m := &manifest{URL: "http://127.0.0.1:34100/", Origins: []string{"http://127.0.0.1:8445/"}, Data: "seed"}
	if err := checkManifest(m, names, ports, "", &keptRoom{Publish: []int{8443, 8444, 8445}}); err != nil {
		t.Errorf("a kept room on a port it publishes: %v", err)
	}
	for what, k := range map[string]*keptRoom{"a kept room publishing none": {}, "a kept room publishing others": {Publish: []int{8448}}, "a contract's room": nil} {
		if err := checkManifest(m, names, ports, "", k); err == nil || !strings.Contains(err.Error(), "this room's ports") {
			t.Errorf("%s, on 8445: %v", what, err)
		}
	}
	// On this machine's own tailnet address, at a published port; never a
	// foreign address, published or not.
	defer func(f func() ([]net.Addr, error)) { interfaceAddrs = f }(interfaceAddrs)
	interfaceAddrs = func() ([]net.Addr, error) {
		return []net.Addr{&net.IPNet{IP: net.ParseIP("100.64.0.2"), Mask: net.CIDRMask(32, 32)}}, nil
	}
	tail := &manifest{URL: "http://100.64.0.2:8443/", Origins: []string{"http://100.64.0.2:8445/"}, Data: "seed"}
	if err := checkManifest(tail, names, ports, "", &keptRoom{Publish: []int{8443, 8444, 8445}}); err != nil {
		t.Errorf("a kept room on its own tailnet address at published ports: %v", err)
	}
	if err := checkManifest(tail, names, ports, "", &keptRoom{Publish: []int{8443}}); err == nil || !strings.Contains(err.Error(), "this room's ports") {
		t.Errorf("an unpublished port on its own tailnet address: %v", err)
	}
	away := &manifest{URL: "http://192.0.2.10:8443/", Data: "seed"}
	if err := checkManifest(away, names, ports, "", &keptRoom{Publish: []int{8443}}); err == nil || !strings.Contains(err.Error(), "not on this machine") {
		t.Errorf("a foreign address at a published port: %v", err)
	}
	// Publishing never makes the db port an origin.
	db := &manifest{URL: "http://127.0.0.1:34102/", Data: "seed"}
	if err := checkManifest(db, names, ports, "", &keptRoom{Publish: []int{34102}}); err == nil || !strings.Contains(err.Error(), "db port") {
		t.Errorf("the db port, published: %v", err)
	}
}

func TestPublishReadsPortsAndRanges(t *testing.T) {
	read := func(v string) ([]int, error) {
		f, err := parseFlags([]string{"--publish", v, "x"}, map[string]bool{"publish": true})
		if err != nil {
			t.Fatal(err)
		}
		return keptPublish(f)
	}
	if got, err := read("8443-8447"); err != nil || !slices.Equal(got, []int{8443, 8444, 8445, 8446, 8447}) {
		t.Errorf("8443-8447: %v %v", got, err)
	}
	if got, err := read(" 8443, 8450-8451 "); err != nil || !slices.Equal(got, []int{8443, 8450, 8451}) {
		t.Errorf("8443,8450-8451: %v %v", got, err)
	}
	for v, why := range map[string]string{"80": "from 1025", "1024": "from 1025", "70000": "from 1025", "8447-8443": "comma separated",
		"abc": "comma separated", "": "comma separated", "8443,8443": "named twice", "8443-8443,8440-8445": "named twice", "9000-9016": "at most 16"} {
		if _, err := read(v); err == nil || !strings.Contains(err.Error(), why) {
			t.Errorf("%q: %v (want %q)", v, err, why)
		}
	}
}
