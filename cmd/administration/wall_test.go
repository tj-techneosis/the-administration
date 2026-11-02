package main

import (
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
	if err := checkManifest(ok, names, ports, ""); err != nil {
		t.Errorf("web, api and aux: %v", err)
	}
	bad := &manifest{URL: "http://127.0.0.1:4100/", Origins: []string{"http://127.0.0.1:4102"}, Data: "seed"}
	if err := checkManifest(bad, names, ports, ""); err == nil || !strings.Contains(err.Error(), "db port") {
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
