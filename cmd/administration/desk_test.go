package main

import (
	"encoding/json"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTheDeskHasTheDeclaredFilesAndNothingElse(t *testing.T) {
	project, room := t.TempDir(), t.TempDir()
	write := func(p, s string) {
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(project, "fixtures", "receipt.pdf"), "%PDF-1.4 a receipt")
	write(filepath.Join(project, ".administration", "roster.toml"),
		"[desk]\nfiles = [\"fixtures/receipt.pdf\"]\n\n[[desk.make]]\nname = \"batch-{n}.jpg\"\nsize = \"300KB\"\ncount = 3\n\n[[desk.make]]\nname = \"logo.png\"\nsize = \"20KB\"\n")
	files, err := fillDesk(project, room)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range files {
		names = append(names, f.Name)
	}
	if got := strings.Join(names, " "); got != "batch-1.jpg batch-2.jpg batch-3.jpg logo.png receipt.pdf" {
		t.Fatalf("desk: %s", got)
	}
	if err := writeDeskList(room, files); err != nil {
		t.Fatal(err)
	}
	dir := deskDir(room)
	// Made files are the size asked and open as what they're called.
	st, _ := os.Stat(filepath.Join(dir, "batch-2.jpg"))
	if st.Size() != 300<<10 {
		t.Fatalf("batch-2.jpg is %d bytes", st.Size())
	}
	f, _ := os.Open(filepath.Join(dir, "batch-2.jpg"))
	if _, err := jpeg.Decode(f); err != nil {
		t.Fatalf("batch-2.jpg isn't a JPEG: %v", err)
	}
	f.Close()
	f, _ = os.Open(filepath.Join(dir, "logo.png"))
	if _, err := png.Decode(f); err != nil {
		t.Fatalf("logo.png isn't a PNG: %v", err)
	}
	f.Close()
	// Only a name on the desk goes into a page.
	if _, err := deskPaths(dir, []string{"logo.png", "receipt.pdf"}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"../roster.toml", "/etc/passwd", "fixtures/receipt.pdf", "missing.png", ".."} {
		if _, err := deskPaths(dir, []string{bad}); err == nil {
			t.Errorf("%q went through", bad)
		}
	}
	_ = os.Symlink("/etc/hosts", filepath.Join(dir, "link.txt"))
	if _, err := deskPaths(dir, []string{"link.txt"}); err == nil {
		t.Error("a link out of the desk went through")
	}
	// A file put in the folder after it was filled isn't on the desk.
	write(filepath.Join(dir, "planted.txt"), "x")
	if _, err := deskPaths(dir, []string{"planted.txt"}); err == nil {
		t.Error("a file that wasn't declared went through")
	}
	if _, err := deskPaths(dir, []string{"logo.png", "logo.png"}); err == nil {
		t.Error("one file picked twice went through")
	}
	if _, err := deskPaths("", []string{"logo.png"}); err == nil {
		t.Error("a room with no desk handed out a file")
	}
	// Through a link to a folder outside the project, it's still outside.
	outside := t.TempDir()
	write(filepath.Join(outside, "tax.pdf"), "private")
	_ = os.Symlink(outside, filepath.Join(project, "fixtures", "out"))
	write(filepath.Join(project, ".administration", "roster.toml"), "[desk]\nfiles = [\"fixtures/out/tax.pdf\"]\n")
	adminDirs.Delete(project)
	if _, err := fillDesk(project, t.TempDir()); err == nil || !strings.Contains(err.Error(), "through a link") {
		t.Errorf("a file through a linked folder went through: %v", err)
	}
	for _, bad := range []string{"NaN", "Inf", "1e30MB", "-1KB"} {
		if _, err := parseSize(bad); err == nil {
			t.Errorf("size %q went through", bad)
		}
	}
	// A file from outside the project is refused when the desk is filled.
	write(filepath.Join(project, ".administration", "roster.toml"), "[desk]\nfiles = [\"../outside.txt\"]\n")
	adminDirs.Delete(project)
	if _, err := fillDesk(project, t.TempDir()); err == nil {
		t.Error("a desk file outside the project went through")
	}
}

func TestADropCarriesTheFilesToThePoint(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.txt")
	_ = os.WriteFile(p, []byte("hello"), 0o600)
	js, err := dropScript([]string{p}, 120, 80)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"name":"a.txt"`, `"type":"text/plain; charset=utf-8"`, "elementFromPoint(120, 80)", `"drop"`} {
		if !strings.Contains(js, want) {
			t.Errorf("want %s in the script", want)
		}
	}
}

func TestADropAnswersWithItsResultNotTheScript(t *testing.T) {
	raw := `{"content":[{"type":"text","text":"### Result\n\"dropped 1 file(s) on div#drop\"\n### Ran Playwright code\n` + "```js\\nawait page.evaluate(" + strings.Repeat("A", 5000) + ")\\n```" + `\n### Page\n- Page URL: http://127.0.0.1:1/notes"}]}`
	got := string(resultOnly(rpc{Result: []byte(raw)}).Result)
	if strings.Contains(got, "AAAA") || !strings.Contains(got, "dropped 1 file(s) on div#drop") || !strings.Contains(got, "Page URL") {
		t.Fatalf("%s", got)
	}
}

func TestMadeFilesOpenAsWhatTheyAre(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.gif", "b.json", "c.pdf"} {
		if err := makeFile(filepath.Join(dir, name), 50<<10, 1); err != nil {
			t.Fatal(err)
		}
	}
	f, _ := os.Open(filepath.Join(dir, "a.gif"))
	if _, err := gif.Decode(f); err != nil {
		t.Errorf("a.gif isn't a GIF: %v", err)
	}
	f.Close()
	b, _ := os.ReadFile(filepath.Join(dir, "b.json"))
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Errorf("b.json isn't JSON: %v", err)
	}
	pdf, _ := os.ReadFile(filepath.Join(dir, "c.pdf"))
	if !strings.HasSuffix(string(pdf), "%%EOF\n") || !strings.Contains(string(pdf[len(pdf)-200:]), "startxref") || len(pdf) < 45<<10 {
		t.Errorf("c.pdf doesn't end as a PDF does (%d bytes)", len(pdf))
	}
}
