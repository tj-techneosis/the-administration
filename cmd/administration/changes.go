package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// A contract on changes: the diff from where the branch left its base,
// working tree included, handed to critics as a file they may read.
type changes struct {
	Base      string   `json:"base"`
	MergeBase string   `json:"merge_base"`
	Head      string   `json:"head"`
	Files     []string `json:"files"`
	Untracked []string `json:"untracked,omitempty"`
	Patch     string   `json:"patch"`
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
var gitSafe = []string{"-c", "core.fsmonitor=false", "-c", "diff.external=", "-c", "core.hooksPath=/dev/null"}

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
	for _, f := range ch.Untracked {
		st, err := os.Lstat(filepath.Join(project, f))
		if err != nil || !st.Mode().IsRegular() || st.Size() > 256<<10 {
			continue
		}
		cmd := exec.Command("git", append(append([]string{}, gitSafe...), "diff", "--no-index", "--no-ext-diff", "--no-textconv", "--", os.DevNull, f)...)
		cmd.Dir = project
		out, _ := cmd.Output() // exits 1 when the files differ, which they do
		if !bytes.Contains(out, []byte("Binary files")) {
			patch += string(out)
		}
	}
	if err := os.MkdirAll(handout, 0o700); err != nil {
		return nil, err
	}
	ch.Patch = filepath.Join(handout, "changes.patch")
	if err := os.WriteFile(ch.Patch, []byte(patch), 0o600); err != nil {
		return nil, err
	}
	return ch, nil
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
		src = filepath.Join(project, ".administration", "candidates", src+".md")
	}
	p, dst, err := hireFile(project, src, f.has("replace"))
	if err != nil {
		return err
	}
	if within(src, filepath.Join(project, ".administration", "candidates")) {
		_ = os.Remove(src)
	}
	fmt.Printf("%s is on the roster (%s). Knows: %s. Commit the file to share them with the team.\n",
		p.Name, rel(project, dst), strings.Join(p.Knows, ", "))
	fireHooks("hired", project, map[string]any{"name": p.Name, "file": dst, "kind": p.Kind, "knows": p.Knows})
	return nil
}

// hireFile writes the professional at src onto the project's roster, after
// the same checks the roster applies.
func hireFile(project, src string, replace bool) (*professional, string, error) {
	b, err := os.ReadFile(src)
	if err != nil {
		return nil, "", fmt.Errorf("no candidate at %s (the Director writes them to .administration/candidates/)", src)
	}
	p, err := parseProfessional(b, src, "project", false)
	if err != nil {
		return nil, "", fmt.Errorf("%s isn't ready to hire: %v", src, err)
	}
	dst := filepath.Join(project, ".administration", "professionals", p.Name+".md")
	if _, err := os.Stat(dst); err == nil && !replace {
		return nil, "", fmt.Errorf("%s is already on the roster (%s); pass --replace to retrain them", p.Name, rel(project, dst))
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

