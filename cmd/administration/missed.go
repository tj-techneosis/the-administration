package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// Misses: what a professional reached, saw and passed over, told back by
// the person (or whoever compared the report with what they know). Each is
// a file in the project, so the team shares them; the Director's next
// review tunes the professional who should have caught it, so the kind of
// thing is a finding next time, and apply files the miss away as tuned.

func missedDir(project string) string { return filepath.Join(adminDir(project), "missed") }

var missedName = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}(-[0-9]+)?\.md$`)

// cmdMissed is `administration missed WHAT... [--contract ID] [--by NAME]`.
func cmdMissed(args []string) error {
	f, err := parseFlags(args, map[string]bool{"project": true, "contract": true, "by": true})
	if err != nil {
		return err
	}
	project, err := projectDir(f.get("project"))
	if err != nil {
		return err
	}
	what := strings.TrimSpace(strings.Join(f.pos, " "))
	if what == "" {
		return usageError{`missed "what it saw and passed over, and why it's a problem" [--contract ID] [--by NAME]`}
	}
	var by []string
	if n := f.get("by"); n != "" {
		for _, s := range strings.Split(n, ",") {
			if s = strings.TrimSpace(s); !nameRE.MatchString(s) {
				return usageError{"--by: a professional's name"}
			} else {
				by = append(by, s)
			}
		}
	}
	cid := f.get("contract")
	if cid != "" {
		c, _, err := loadContract(cid)
		if err != nil {
			return err
		}
		if len(by) == 0 {
			// Whoever worked it: the Director picks which should have seen it.
			for _, p := range c.Professionals {
				by = append(by, p.Name)
			}
		}
	}
	dir := missedDir(project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	base := time.Now().Format("20060102-150405")
	name := base + ".md"
	for i := 2; ; i++ {
		if _, err := os.Stat(filepath.Join(dir, name)); os.IsNotExist(err) {
			break
		}
		name = fmt.Sprintf("%s-%d.md", base, i)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "---\n")
	if cid != "" {
		fmt.Fprintf(&b, "contract: %s\n", cid)
	}
	if len(by) > 0 {
		fmt.Fprintf(&b, "by: [%s]\n", strings.Join(by, ", "))
	}
	fmt.Fprintf(&b, "---\n%s\n", what)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(b.String()), 0o644); err != nil {
		return err
	}
	fmt.Printf("Noted %s. The Director tunes for it on its next review: administration director review\n",
		houseFile(project, "missed", name))
	return nil
}

// openMisses are the misses not yet tuned for, by file name.
func openMisses(project string) []string {
	files, _ := filepath.Glob(filepath.Join(missedDir(project), "*.md"))
	var out []string
	for _, f := range files {
		if missedName.MatchString(filepath.Base(f)) {
			out = append(out, filepath.Base(f))
		}
	}
	sort.Strings(out)
	return out
}

// checkTuned holds a tuned professional to its old self: the same file,
// and nothing about what it is or what it gets changed, only how it judges.
func checkTuned(project, out string) []string {
	files, _ := filepath.Glob(filepath.Join(out, "professionals", "*.md"))
	if len(files) == 0 {
		return nil
	}
	v, err := loadRoster(project)
	if err != nil {
		return []string{"professionals/: " + err.Error()}
	}
	var problems []string
	for _, f := range files {
		base := filepath.Base(f)
		name := strings.TrimSuffix(base, ".md")
		var old *professional
		for _, p := range v.Professionals {
			if p.Name == name {
				old = p
			}
		}
		if old == nil {
			problems = append(problems, fmt.Sprintf("professionals/%s: no professional %s on the roster (a new one is a candidate)", base, name))
			continue
		}
		if !projectOwns(project, old.Path) {
			problems = append(problems, fmt.Sprintf("professionals/%s: %s isn't the project's own (%s); propose the change in proposals.md", base, name, old.Path))
			continue
		}
		b, _ := os.ReadFile(f)
		p, err := parseProfessional(b, f, old.Source, old.Source == "agent")
		if err != nil {
			problems = append(problems, fmt.Sprintf("professionals/%s: %v", base, err))
			continue
		}
		if p.Name != old.Name || p.Kind != old.Kind || p.Browser != old.Browser || p.Coins != old.Coins ||
			p.SignsInAs != old.SignsInAs || p.Sees != old.Sees || p.Model != old.Model ||
			!slices.Equal(p.Uses, old.Uses) || !slices.Equal(p.Knows, old.Knows) {
			problems = append(problems, fmt.Sprintf("professionals/%s: a tuning changes only the persona's text; keep its frontmatter as it was", base))
		}
	}
	return problems
}

// projectOwns is whether a file is the project's own: in it, or in its
// .administration wherever that is (a worktree's main checkout, a link).
func projectOwns(project, path string) bool {
	return inside(project, path) || inside(adminDir(project), path)
}

func inside(dir, path string) bool {
	r, err := filepath.Rel(dir, path)
	return err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) && !filepath.IsAbs(r)
}

// applyTuned writes each tuned professional over its file, keeping the old
// one in the run's backup, and files away the misses the run handled.
func applyTuned(project, out, backup string) (int, error) {
	files, _ := filepath.Glob(filepath.Join(out, "professionals", "*.md"))
	v, err := loadRoster(project)
	if err != nil && len(files) > 0 {
		return 0, err
	}
	n := 0
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".md")
		for _, p := range v.Professionals {
			if p.Name != name {
				continue
			}
			copyFile(p.Path, filepath.Join(backup, "professionals", filepath.Base(p.Path)))
			b, _ := os.ReadFile(f)
			if err := os.WriteFile(p.Path, b, 0o644); err != nil {
				return n, err
			}
			fmt.Printf("  tuned %s (%s)\n", name, p.Path)
			n++
		}
	}
	b, _ := os.ReadFile(filepath.Join(out, "missed.txt"))
	for _, l := range strings.Split(string(b), "\n") {
		id, _, _ := strings.Cut(strings.TrimSpace(l), ":")
		id = strings.TrimSpace(id)
		if !missedName.MatchString(id) {
			continue
		}
		src := filepath.Join(missedDir(project), id)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		dst := filepath.Join(missedDir(project), "tuned", id)
		_ = os.MkdirAll(filepath.Dir(dst), 0o755)
		if err := os.Rename(src, dst); err == nil {
			fmt.Printf("  missed/%s: %s\n", id, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), id+":")))
		}
	}
	return n, nil
}
