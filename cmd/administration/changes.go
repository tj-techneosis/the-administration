package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// A contract on changes: the diff from where the branch left its base,
// working tree included, handed to critics as a file they may read.
// contractHandout is where a contract keeps what every professional is
// handed (the changes' patch): hidden, so the contract's folder holds only
// professionals, and a script going through */status.json finds only them.
const contractHandout = ".handout"

type changes struct {
	Base      string   `json:"base"`
	MergeBase string   `json:"merge_base"`
	Head      string   `json:"head"`
	Files     []string `json:"files"`
	Untracked []string `json:"untracked,omitempty"`
	Patch     string   `json:"patch"`
	// The same diff split by file, with an index of what each changed:
	// a critic reads its own files' parts, not every file's.
	Split string `json:"split,omitempty"`
}

func git(dir string, args ...string) (string, error) {
	out, err := gitRaw(dir, args...)
	return strings.TrimSpace(out), err
}

// gitRaw is git's output untouched (a patch's last line matters).
func gitRaw(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append(append([]string{}, gitSafe...), args...)...)
	cmd.Dir = dir
	var errb bytes.Buffer
	cmd.Stderr = &errb
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(errb.String()))
	}
	return string(out), nil
}

func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// Secrets never go in a handout, even when they changed; nor does the
// roster itself, which isn't what's under contract.
var secretPathspecs = func() []string {
	out := []string{":(exclude).administration"}
	for _, g := range secretGlobs {
		out = append(out, ":(exclude,glob)**/"+g)
	}
	return out
}()

// gitSafe keeps the repo's own config from running anything while we read it.
// core.quotePath=false keeps a non-ASCII name as it is, not "caf\303\251",
// so it can be opened and listed.
var gitSafe = []string{"-c", "core.fsmonitor=false", "-c", "diff.external=", "-c", "core.hooksPath=/dev/null", "-c", "core.quotePath=false"}

func collectChanges(project, base, handout string) (*changes, error) {
	if base == "" {
		for _, b := range []string{"origin/HEAD", "origin/main", "origin/master", "main", "master"} {
			if _, err := git(project, "rev-parse", "--verify", "--quiet", b+"^{commit}"); err == nil {
				base = b
				break
			}
		}
		if base == "" {
			return nil, usageError{"--changes: no main branch found to compare with; pass --base REF"}
		}
	}
	if _, err := git(project, "rev-parse", "--verify", "--quiet", "HEAD^{commit}"); err != nil {
		return nil, usageError{"--changes needs at least one commit to compare against"}
	}
	mb, err := git(project, "merge-base", "HEAD", base)
	if err != nil {
		return nil, usageError{err.Error()}
	}
	head, _ := git(project, "rev-parse", "--short", "HEAD")
	ch := &changes{Base: base, MergeBase: mb, Head: head}
	files, err := git(project, append([]string{"diff", "--no-ext-diff", "--no-textconv", "--name-only", mb, "--", "."}, secretPathspecs...)...)
	if err != nil {
		return nil, err
	}
	ch.Files = lines(files)
	untracked, _ := git(project, append([]string{"ls-files", "--others", "--exclude-standard", "--", "."}, secretPathspecs...)...)
	ch.Untracked = lines(untracked)
	if len(ch.Files)+len(ch.Untracked) == 0 {
		return nil, usageError{fmt.Sprintf("--changes: nothing has changed since %s (%s)", base, mb[:min(8, len(mb))])}
	}
	patch, err := gitRaw(project, append([]string{"diff", "--no-ext-diff", "--no-textconv", "-M", mb, "--", "."}, secretPathspecs...)...)
	if err != nil {
		return nil, err
	}
	// New files nobody has added yet are part of the changes too: their
	// contents go in as additions, text files up to 256 KB each.
	var skipped []string // listed as changed, with no part in the patch
	for _, f := range ch.Untracked {
		st, err := os.Lstat(filepath.Join(project, f))
		if err != nil || !st.Mode().IsRegular() || st.Size() > 256<<10 {
			skipped = append(skipped, f)
			continue
		}
		cmd := exec.Command("git", append(append([]string{}, gitSafe...), "diff", "--no-index", "--no-ext-diff", "--no-textconv", "--", os.DevNull, f)...)
		cmd.Dir = project
		out, _ := cmd.Output() // exits 1 when the files differ, which they do
		if !bytes.Contains(out, []byte("Binary files")) {
			patch += string(out)
		} else {
			skipped = append(skipped, f)
		}
	}
	if err := os.MkdirAll(handout, 0o700); err != nil {
		return nil, err
	}
	ch.Patch = filepath.Join(handout, "changes.patch")
	if err := os.WriteFile(ch.Patch, []byte(patch), 0o600); err != nil {
		return nil, err
	}
	if dir := filepath.Join(handout, "changes"); splitPatch(patch, skipped, dir) == nil {
		ch.Split = dir
	} else {
		_ = os.RemoveAll(dir)
	}
	return ch, nil
}

// splitPatch writes each file's part of a patch to its own file in dir,
// and INDEX.md listing them: the file, lines added and removed, and where
// its part is; and the files listed as changed that have no part (binary,
// a link, too big). An error, or a patch with no parts, leaves no split.
func splitPatch(patch string, skipped []string, dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	var index strings.Builder
	index.WriteString("# The changes, by file\n\nEach changed file, its lines added and removed, and its part of the diff. changes.patch beside this folder is all of it.\n\n")
	parts := strings.Split(patch, "\ndiff --git ")
	n := 0
	for i, p := range parts {
		if i > 0 {
			p = "diff --git " + p
		}
		if !strings.HasPrefix(p, "diff --git ") {
			continue
		}
		name := partName(p)
		add, del, body := 0, 0, false
		for _, l := range strings.Split(p, "\n") {
			switch {
			case strings.HasPrefix(l, "@@"):
				body = true
			case !body:
			case strings.HasPrefix(l, "+"):
				add++
			case strings.HasPrefix(l, "-"):
				del++
			}
		}
		n++
		safe := strings.NewReplacer("/", "__", " ", "_", "\"", "", "\\", "_").Replace(name)
		if len(safe) > 120 {
			safe = safe[len(safe)-120:]
		}
		file := fmt.Sprintf("%03d-%s.patch", n, safe)
		if err := os.WriteFile(filepath.Join(dir, file), []byte(strings.TrimRight(p, "\n")+"\n"), 0o600); err != nil {
			return err
		}
		fmt.Fprintf(&index, "- %s (+%d -%d): %s\n", name, add, del, file)
	}
	if n == 0 {
		return errors.New("no files in the patch")
	}
	if len(skipped) > 0 {
		index.WriteString("\nChanged, but not in the diff (binary, a link, or over 256 KB): read them in the project if they matter:\n\n")
		for _, f := range skipped {
			fmt.Fprintf(&index, "- %s\n", f)
		}
	}
	return os.WriteFile(filepath.Join(dir, "INDEX.md"), []byte(index.String()), 0o600)
}

// partName is the file a patch part is about: its new name (+++ b/), or
// its old one for a deletion, unquoted when git quoted it.
func partName(p string) string {
	var old string
	for _, l := range strings.Split(p, "\n") {
		if strings.HasPrefix(l, "@@") {
			break
		}
		if v, ok := strings.CutPrefix(l, "+++ "); ok && v != "/dev/null" {
			return strings.TrimPrefix(unquotePath(v), "b/")
		}
		if v, ok := strings.CutPrefix(l, "--- "); ok && v != "/dev/null" {
			old = strings.TrimPrefix(unquotePath(v), "a/")
		}
	}
	if old != "" {
		return old
	}
	// No content lines (a mode change, a pure rename): the header's second
	// path, after the last " b/".
	head, _, _ := strings.Cut(p, "\n")
	if i := strings.LastIndex(head, ` "b/`); i >= 0 {
		return strings.TrimPrefix(unquotePath(head[i+1:]), "b/")
	}
	if i := strings.LastIndex(head, " b/"); i >= 0 {
		return head[i+3:]
	}
	return strings.TrimPrefix(head, "diff --git ")
}

func unquotePath(v string) string {
	v = strings.TrimRight(v, "\t")
	if strings.HasPrefix(v, "\"") {
		if u, err := strconv.Unquote(v); err == nil {
			return u
		}
	}
	return v
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// cmdHire puts a professional on the project's roster: a file the Director
// wrote (by name, from .administration/candidates/) or any path. It must pass
// the same checks the roster applies, tags included.
func cmdHire(args []string) error {
	f, err := parseFlags(args, map[string]bool{"project": true, "replace": false, "from": true})
	if err != nil {
		return err
	}
	if len(f.pos) != 1 {
		return usageError{"hire NAME|FILE, or hire --from CONTRACT NAME"}
	}
	project, err := projectDir(f.get("project"))
	if err != nil {
		return err
	}
	src := f.pos[0]
	if f.has("from") {
		// A one-off specialist from a contract: the file it worked from.
		c, dir, err := loadContract(f.get("from"))
		if err != nil {
			return err
		}
		found := false
		for _, p := range c.Professionals {
			found = found || p.Name == src && p.Source == "contract"
		}
		if !nameRE.MatchString(src) || !found {
			return usageError{fmt.Sprintf("%s wasn't a one-off on contract %s (hire --from takes a professional placed with --with)", src, c.ID)}
		}
		src = filepath.Join(dir, src, "hire.md")
	} else if !strings.ContainsAny(src, "/.") {
		src = filepath.Join(adminDir(project), "candidates", src+".md")
	}
	p, dst, err := hireFile(project, src, f.has("replace"))
	if err != nil {
		return err
	}
	// A path through a linked .administration is the same candidate.
	cands := filepath.Join(adminDir(project), "candidates")
	if within(src, cands) || within(realPath(src), realPath(cands)) {
		_ = os.Remove(src)
	}
	share := "Commit the file to share them with the team."
	switch houseOf(project).Where {
	case "checkout":
		share = "Commit the file in the main checkout to share them with the team."
	case "machine":
		share = "The house is kept on this machine, so it's not in the repo to commit."
	}
	fmt.Printf("%s is on the roster (%s). Knows: %s. %s\n",
		p.Name, houseFile(project, "professionals", filepath.Base(dst)), strings.Join(p.Knows, ", "), share)
	fireHooks("hired", project, map[string]any{"name": p.Name, "file": dst, "kind": p.Kind, "knows": p.Knows})
	return nil
}

// hireFile writes the professional at src onto the project's roster, after
// the same checks the roster applies.
func hireFile(project, src string, replace bool) (*professional, string, error) {
	b, err := os.ReadFile(src)
	if err != nil {
		return nil, "", fmt.Errorf("no candidate at %s (director apply puts them in %s)", src, houseFile(project, "candidates"))
	}
	p, err := parseProfessional(b, src, "project", false)
	if err != nil {
		return nil, "", fmt.Errorf("%s isn't ready to hire: %v", src, err)
	}
	dst := filepath.Join(adminDir(project), "professionals", p.Name+".md")
	if _, err := os.Stat(dst); err == nil && !replace {
		return nil, "", fmt.Errorf("%s is already on the roster (%s); pass --replace to retrain them", p.Name, shown(project, dst))
	}
	v, err := loadRoster(project)
	if err != nil {
		return nil, "", err
	}
	for _, q := range v.Professionals {
		if q.Name == p.Name && q.Source == "agent" {
			return nil, "", fmt.Errorf("%s is already on the roster as one of the project's own agents", p.Name)
		}
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return nil, "", err
	}
	if err := os.WriteFile(dst, b, 0o644); err != nil {
		return nil, "", err
	}
	return p, dst, nil
}

