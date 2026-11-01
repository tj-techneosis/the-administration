package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// The desk's files: what a user can put into the page, the way a person
// picks a file from their own computer. The project names them in
// roster.toml, copied from the project or its house, or made to a size:
//
//	[desk]
//	files = ["fixtures/receipt.pdf", ".administration/desk/avatar.jpg"]
//
//	[[desk.make]]
//	name = "batch-{n}.jpg"   # {n} counts 1, 2, 3...
//	size = "5MB"
//	count = 20
//
// Each room gets its own copies in desk/files, and a user reaches them only
// by name, through the screen's browser_choose_file and browser_drop_file:
// the house turns a name into the desk's copy, so nothing else on the
// machine can go into a page.

type deskMake struct {
	Name  string `toml:"name"`
	Size  string `toml:"size"`
	Count int    `toml:"count"`
}

type deskFile struct {
	Name string
	Size int64
}

const (
	deskFileLimit  = 200 << 20 // one file
	deskTotalLimit = 1 << 30   // a room's desk
	deskMakeLimit  = 200       // files one [[desk.make]] makes
)

// A desk file's name: letters, digits, dots, dashes, underscores and
// spaces, starting with a letter or digit and ending with one, so no two
// names look the same in a brief.
var deskNameRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._ -]{0,99}[A-Za-z0-9_-])?$`)

// deskDir is where a room's files are.
func deskDir(room string) string { return filepath.Join(room, "desk", "files") }

// fillDesk puts the project's declared files on a room's desk; with no
// room it only checks them (place does, before anyone starts).
func fillDesk(project, room string) ([]deskFile, error) {
	cfg := loadProjectConfig(project).Desk
	if len(cfg.Files) == 0 && len(cfg.Make) == 0 {
		return nil, nil
	}
	dir := ""
	if room != "" {
		dir = deskDir(room)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	var out []deskFile
	seen := map[string]bool{}
	var total int64
	add := func(name string, size int64) error {
		// One file on a case-insensitive disk (macOS's) is one name.
		if seen[strings.ToLower(name)] {
			return fmt.Errorf("desk: two files called %q", name)
		}
		seen[strings.ToLower(name)] = true
		if total += size; total > deskTotalLimit {
			return fmt.Errorf("desk: over %d MB in all; put less on it", deskTotalLimit>>20)
		}
		out = append(out, deskFile{name, size})
		return nil
	}
	for _, rel := range cfg.Files {
		src := housePath(project, rel)
		name := filepath.Base(rel)
		if filepath.IsAbs(rel) || !inProject(project, src) {
			return nil, fmt.Errorf("desk file %q: a file inside the project or its house", rel)
		}
		if secret(name) {
			return nil, fmt.Errorf("desk file %q looks like a secret; it never goes in a room", rel)
		}
		if !deskNameRE.MatchString(name) {
			return nil, fmt.Errorf("desk file %q: a desk file's name is letters, digits, dots, dashes, underscores and spaces, starting and ending with a letter or digit", rel)
		}
		st, err := os.Lstat(src)
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("desk file %q: there's no such file", rel)
		}
		if err != nil || !st.Mode().IsRegular() {
			return nil, fmt.Errorf("desk file %q: not a regular file (a folder, or a link)", rel)
		}
		// Every folder on the way followed: a link to a folder outside the
		// project is still outside it.
		real, err := filepath.EvalSymlinks(src)
		if err != nil || !within(real, realPath(project)) && !within(real, realPath(adminDir(project))) {
			return nil, fmt.Errorf("desk file %q is outside the project and its house (through a link)", rel)
		}
		if st.Size() > deskFileLimit {
			return nil, fmt.Errorf("desk file %q is over %d MB", rel, deskFileLimit>>20)
		}
		if err := add(name, st.Size()); err != nil {
			return nil, err
		}
		if dir != "" {
			if err := copyRegular(real, filepath.Join(dir, name)); err != nil {
				return nil, err
			}
		}
	}
	for _, mk := range cfg.Make {
		size, err := parseSize(mk.Size)
		if err != nil || size > deskFileLimit {
			return nil, fmt.Errorf("desk make %q: size %q: a size like 300KB or 5MB, up to %d MB", mk.Name, mk.Size, deskFileLimit>>20)
		}
		count := max(mk.Count, 1)
		if count > deskMakeLimit || count > 1 && !strings.Contains(mk.Name, "{n}") {
			return nil, fmt.Errorf("desk make %q: count up to %d, with {n} in the name when it's more than one", mk.Name, deskMakeLimit)
		}
		for n := 1; n <= count; n++ {
			name := strings.ReplaceAll(mk.Name, "{n}", strconv.Itoa(n))
			if !deskNameRE.MatchString(name) {
				return nil, fmt.Errorf("desk make %q: not a name a desk file can have", mk.Name)
			}
			if err := add(name, size); err != nil {
				return nil, err
			}
			if dir != "" {
				p := filepath.Join(dir, name)
				if err := makeFile(p, size, n); err != nil {
					return nil, err
				}
				// A size smaller than the format itself makes it bigger:
				// the brief says what it really is.
				if st, err := os.Stat(p); err == nil {
					out[len(out)-1].Size = st.Size()
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// deskList is where a room's desk names its files, for the screen: only a
// name on it goes into a page, whatever else lands in the folder.
func deskList(room string) string { return filepath.Join(room, "desk-files.json") }

func writeDeskList(room string, files []deskFile) error {
	names := make([]string, len(files))
	for i, f := range files {
		names[i] = f.Name
	}
	b, _ := json.Marshal(names)
	return os.WriteFile(deskList(room), b, 0o600)
}

func copyRegular(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// parseSize reads 300KB, 5MB, 1.5MB or a plain count of bytes.
func parseSize(s string) (int64, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	mult := 1.0
	for _, u := range []struct {
		suffix string
		m      float64
	}{{"GB", 1 << 30}, {"MB", 1 << 20}, {"KB", 1 << 10}, {"B", 1}} {
		if strings.HasSuffix(s, u.suffix) {
			s, mult = strings.TrimSpace(strings.TrimSuffix(s, u.suffix)), u.m
			break
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 || math.IsNaN(f) || math.IsInf(f, 0) || f*mult > 1<<40 {
		return 0, fmt.Errorf("not a size")
	}
	return int64(f * mult), nil
}

// makeFile writes a file of about size bytes that opens as what its name
// says: a real picture, PDF or text, padded where its format allows, so an
// app that checks what it's given takes it. Each n differs, so the files
// aren't all the same (an app may refuse a duplicate).
func makeFile(path string, size int64, n int) error {
	var head bytes.Buffer
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif":
		img := image.NewRGBA(image.Rect(0, 0, 64, 48))
		c := color.RGBA{uint8(40 * n), uint8(90 + 13*n), uint8(160 + 7*n), 255}
		for y := 0; y < 48; y++ {
			for x := 0; x < 64; x++ {
				img.Set(x, y, c)
			}
		}
		var err error
		switch ext {
		case ".png":
			err = png.Encode(&head, img)
		case ".gif":
			err = gif.Encode(&head, img, nil)
		default:
			err = jpeg.Encode(&head, img, nil)
		}
		if err != nil {
			return err
		}
	case ".pdf":
		return makePDF(path, size, n)
	case ".json":
		// Valid JSON however big: the padding is a string inside it.
		pad := max(size-40, 0)
		b := []byte(fmt.Sprintf(`{"file": %d, "padding": "`, n))
		b = append(b, bytes.Repeat([]byte("x"), int(pad))...)
		b = append(b, []byte("\"}\n")...)
		return os.WriteFile(path, b, 0o600)
	case ".txt", ".csv", ".md":
		if ext == ".csv" {
			fmt.Fprintf(&head, "id,name,amount\n")
			for i := 1; int64(head.Len()) < size && i < 1<<20; i++ {
				fmt.Fprintf(&head, "%d,item %d-%d,%d.%02d\n", i, n, i, i*3, i%100)
			}
		} else {
			fmt.Fprintf(&head, "File %d.\n", n)
		}
	default:
		fmt.Fprintf(&head, "file %d\n", n)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(head.Bytes()); err != nil {
		return err
	}
	// The rest is padding after the format's end, which readers skip;
	// text is padded with more lines.
	pad := size - int64(head.Len())
	fill := []byte(strings.Repeat(fmt.Sprintf("padding %d\n", n), 1024))
	for pad > 0 {
		k := min(pad, int64(len(fill)))
		if _, err := f.Write(fill[:k]); err != nil {
			return err
		}
		pad -= k
	}
	return nil
}

// deskPaths turns names a user gave into the desk's files, refusing any
// that isn't one: no path, nothing outside the desk, no link out of it.
func deskPaths(dir string, names []string) ([]string, error) {
	if dir == "" {
		return nil, fmt.Errorf("there are no files on your desk")
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("name the file (or files) from your desk")
	}
	// Only what was put there: the list beside the desk, not whatever is
	// in the folder now.
	var listed []string
	if b, err := os.ReadFile(deskList(filepath.Dir(filepath.Dir(dir)))); err == nil {
		_ = json.Unmarshal(b, &listed)
	}
	var out []string
	twice := map[string]bool{}
	for _, n := range names {
		if twice[n] {
			return nil, fmt.Errorf("%q twice: a person picks a file once", n)
		}
		twice[n] = true
		if n != filepath.Base(n) || !deskNameRE.MatchString(n) || !slices.Contains(listed, n) {
			return nil, fmt.Errorf("%q isn't a file on your desk: give its name as listed, nothing else", n)
		}
		p := filepath.Join(dir, n)
		st, err := os.Lstat(p)
		if err != nil || !st.Mode().IsRegular() {
			return nil, fmt.Errorf("%q isn't a file on your desk", n)
		}
		out = append(out, p)
	}
	return out, nil
}

// deskNote tells a user what's on their desk, for its brief; a
// screen-reader user aims a drop by the page's ref.
func deskNote(files []deskFile, reader bool) string {
	if len(files) == 0 {
		return ""
	}
	var names []string
	for _, f := range files {
		names = append(names, fmt.Sprintf("%s (%s)", f.Name, sizeText(f.Size)))
	}
	return "Files on your desk, as on a person's own computer: " + strings.Join(names, ", ") +
		". To upload one, open the page's file picker (click its upload button), then browser_choose_file with the names (with no names, it closes the picker, as Cancel does). To drag one onto the page, browser_drop_file with the names and " +
		map[bool]string{false: "where to drop them", true: "the drop area's element and ref from the page's snapshot"}[reader] +
		". Only these names work; there's nothing else on your computer.\n"
}

func sizeText(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	}
	return fmt.Sprintf("%d bytes", n)
}

// makePDF writes a one-page PDF a strict reader opens: its objects, the
// padding as comment lines before the cross-reference table, then the
// table, the trailer, startxref and %%EOF at the very end.
func makePDF(path string, size int64, n int) error {
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	objs := []string{
		"<</Type/Catalog/Pages 2 0 R>>",
		"<</Type/Pages/Kids[3 0 R]/Count 1>>",
		"<</Type/Page/Parent 2 0 R/MediaBox[0 0 200 200]>>",
	}
	offsets := make([]int, len(objs))
	for i, o := range objs {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj%sendobj\n", i+1, o)
	}
	line := fmt.Sprintf("%% padding %d\n", n)
	for int64(b.Len()+len(line)+200) < size {
		b.WriteString(line)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, o := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer<</Size %d/Root 1 0 R>>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return os.WriteFile(path, b.Bytes(), 0o600)
}

// hasFileTools is whether a user has browser_choose_file and
// browser_drop_file: one with the web browser (seeing the screen or through
// a screen reader), or one who sees a window equipment shows. Equipment
// that isn't a window has no file picker of the house's.
func hasFileTools(c *contract, ci *castItem) bool {
	if ci.Kind != "user" {
		return false
	}
	if !ci.NoBrowser {
		return true
	}
	if !seesScreen(ci) {
		return false
	}
	for _, u := range ci.Uses {
		if eq, err := checkEquipment(c.Project, u); err == nil && eq.Server == "playwright" {
			return true
		}
	}
	return false
}
