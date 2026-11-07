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
	// LeftOut is how many changed files the handout never carries for
	// looking like secrets. (The house's own .administration is left out
	// too, and not counted: it's what runs the contract, not what's under
	// it.)
	LeftOut int    `json:"left_out,omitempty"`
	Patch   string `json:"patch"`
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
	cmd := gitCmd(dir, args...)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(errb.String()))
	}
	return string(out), nil
}

// nulls is git's -z output as names, exactly as they are: a name with a
// newline or spaces at its ends is one name, not two or a trimmed one.
func nulls(s string) []string {
	var out []string
	for _, n := range strings.Split(s, "\x00") {
		if n != "" {
			out = append(out, n)
		}
	}
	return out
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
	// In any case, as secret() matches: .ENV is .env on a disk that
	// ignores case, and a secret on one that doesn't.
	out := []string{":(exclude).administration"}
	for _, g := range secretGlobs {
		out = append(out, ":(exclude,icase,glob)**/"+g)
	}
	return out
}()

// gitSafe keeps the repo's own config from running anything while we read it.
// core.quotePath=false keeps a non-ASCII name as it is, not "caf\303\251",
// so it can be opened and listed. log.showSignature=false keeps a log from
// checking a signed commit with whatever gpg.program the repo names. gitCmd
// adds the filters' commands, which are named by the config itself.
var gitSafe = []string{"-c", "core.fsmonitor=false", "-c", "diff.external=", "-c", "core.hooksPath=/dev/null", "-c", "core.quotePath=false",
	"-c", "log.showSignature=false"}

// gitCmd is git run in dir with nothing of the repo's own config run: on
// top of gitSafe, every content filter its config names is emptied (a
// diff against the working tree runs a file's clean filter to compare it,
// so a repo's `[filter "x"] clean = ...` with `* filter=x` would run as the
// person each time the house reads it), and GIT_OPTIONAL_LOCKS=0 keeps a
// diff from taking the index's lock and writing it back, so a commit the
// person makes at the same moment never meets the house's lock. The cost of
// the filters: a file whose filter changes it (Git LFS, git-crypt) can be
// listed as changed when its stat data is stale, and is then read as it is
// on disk.
func gitCmd(dir string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", append(append([]string{}, gitSafe...), args...)...)
	cmd.Dir = dir
	cmd.Env = append(gitConfigEnv(os.Environ(), gitNoFilters(dir)), "GIT_OPTIONAL_LOCKS=0")
	return cmd
}

// gitConfigEnv is env with each of the key and value pairs kv added as git
// config, after any the person's environment already gives. The key and
// value go apart (GIT_CONFIG_KEY_n, GIT_CONFIG_VALUE_n), where -c joins them
// with an = that git splits at the first one: a filter named "a=b" could
// never be emptied with -c.
func gitConfigEnv(env, kv []string) []string {
	if len(kv) == 0 {
		return env
	}
	n := 0
	var out []string
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "GIT_CONFIG_COUNT="); ok {
			n, _ = strconv.Atoi(v)
			continue
		}
		out = append(out, e)
	}
	for i := 0; i+1 < len(kv); i += 2 {
		out = append(out, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", n, kv[i]), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", n, kv[i+1]))
		n++
	}
	return append(out, fmt.Sprintf("GIT_CONFIG_COUNT=%d", n))
}

// gitNoFilters is the config, as key and value pairs, that empties each
// filter the config in dir names (the repo's, the person's, the system's),
// whatever its name: one with an = in it, or the empty one that
// `filter=` in .gitattributes picks. Reading the config runs nothing.
func gitNoFilters(dir string) []string {
	cmd := exec.Command("git", append(append([]string{}, gitSafe...), "config", "--null", "--get-regexp", `^filter\.`)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	out, _ := cmd.Output() // exits 1 when there's none
	var opts []string
	seen := map[string]bool{}
	for _, rec := range nulls(string(out)) {
		key, _, _ := strings.Cut(rec, "\n")
		name, ok := strings.CutPrefix(key, "filter.")
		if i := strings.LastIndex(name, "."); ok && i >= 0 {
			name = name[:i]
		} else {
			continue
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		for _, v := range [][2]string{{"clean", ""}, {"smudge", ""}, {"process", ""}, {"required", "false"}} {
			opts = append(opts, "filter."+name+"."+v[0], v[1])
		}
	}
	return opts
}

// collectChanges is everything since base, in handout: HEAD and the
// working tree, or, with to, that commit alone (a --build contract's).
func collectChanges(project, base, to, handout string) (*changes, error) {
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
	tip, diffTo := "HEAD", []string{""}
	if to != "" {
		tip, diffTo = to, []string{"", to}
	}
	mb, err := git(project, "merge-base", tip, base)
	if err != nil {
		return nil, usageError{err.Error()}
	}
	diffTo[0] = mb
	head, _ := git(project, "rev-parse", "--short", tip)
	ch := &changes{Base: base, MergeBase: mb, Head: head}
	files, err := gitRaw(project, append(append(append([]string{"diff", "-z", "--no-ext-diff", "--no-textconv", "--name-only"}, diffTo...), "--", "."), secretPathspecs...)...)
	if err != nil {
		return nil, err
	}
	ch.Files = nulls(files)
	if to == "" {
		untracked, _ := gitRaw(project, append([]string{"ls-files", "-z", "--others", "--exclude-standard", "--", "."}, secretPathspecs...)...)
		ch.Untracked = nulls(untracked)
	}
	// What was left out is counted, never named in the handout: a
	// critic's brief says how many, and why it can't see them.
	if all, err := gitRaw(project, append(append([]string{"diff", "-z", "--no-ext-diff", "--no-textconv", "--name-only"}, diffTo...), "--", ".", secretPathspecs[0])...); err == nil {
		n := len(nulls(all))
		if to == "" {
			others, _ := gitRaw(project, "ls-files", "-z", "--others", "--exclude-standard", "--", ".", secretPathspecs[0])
			n += len(nulls(others))
		}
		ch.LeftOut = max(0, n-len(ch.Files)-len(ch.Untracked))
	}
	if len(ch.Files)+len(ch.Untracked) == 0 {
		if ch.LeftOut > 0 {
			return nil, usageError{fmt.Sprintf("--changes: since %s (%s), only files that look like secrets have changed (%d), and those are never handed to a critic, so there's nothing to read", base, mb[:min(8, len(mb))], ch.LeftOut)}
		}
		return nil, usageError{fmt.Sprintf("--changes: nothing has changed since %s (%s)", base, mb[:min(8, len(mb))])}
	}
	patch, err := gitRaw(project, append(append(append([]string{"diff", "--no-ext-diff", "--no-textconv", "-M"}, diffTo...), "--", "."), secretPathspecs...)...)
	if err != nil {
		return nil, err
	}
	// New files nobody has added yet are part of the changes too: their
	// contents go in as additions, text files up to 256 KB each.
	var skipped []string // listed as changed, with no part in the patch
	for _, f := range ch.Untracked {
		st, err := lstatIn(project, f)
		if err != nil || !st.Mode().IsRegular() || st.Size() > 256<<10 {
			skipped = append(skipped, f)
			continue
		}
		cmd := gitCmd(project, "diff", "--no-index", "--no-ext-diff", "--no-textconv", "--", os.DevNull, f)
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
	if dir := filepath.Join(handout, "changes"); splitPatch(patch, skipped, ch.LeftOut, dir) == nil {
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
func splitPatch(patch string, skipped []string, leftOut int, dir string) error {
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
	if leftOut > 0 {
		fmt.Fprintf(&index, "\n%s\n", secretsLeftOutNote(leftOut))
	}
	return os.WriteFile(filepath.Join(dir, "INDEX.md"), []byte(index.String()), 0o600)
}

// secretsLeftOutNote says what a handout doesn't carry, for whoever reads it.
func secretsLeftOutNote(n int) string {
	return fmt.Sprintf("%d more changed file(s) aren't in the diff, and never are: they look like secrets (environment files, keys, credentials). Don't look for them; if your part needed them, say in your report that they weren't reviewed.", n)
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
	projectHooks := true
	if f.has("from") {
		// A one-off specialist from a contract: the file it worked from.
		c, dir, err := loadContract(f.get("from"))
		if err != nil {
			return err
		}
		// From a read-only contract: its checkout is code under review,
		// so nothing is written into it, and no project's own hook runs
		// for it (the machine's do). Keep them in a checkout you trust.
		found := false
		for _, p := range c.Professionals {
			found = found || p.Name == src && p.Source == "contract"
		}
		if !nameRE.MatchString(src) || !found {
			return usageError{fmt.Sprintf("%s wasn't a one-off on contract %s (hire --from takes a professional placed with --with)", src, c.ID)}
		}
		if c.ReadOnly {
			if err := notIntoReviewed(c, project); err != nil {
				return err
			}
			projectHooks = false
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
	fireHooksFrom("hired", project, map[string]any{"name": p.Name, "file": dst, "kind": p.Kind, "knows": p.Knows}, projectHooks)
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


// baseWarnings says when the changes' base isn't what the person likely
// thinks it is: the branch is missing commits that are on the local default
// branch (so it's tested without work already done there), or the local
// default branch is ahead of the remote one the changes are compared with.
// A branch cut from the remote's main while local main had unpushed fixes
// was once tested without them, and a bug main had fixed came back as a
// finding.
func baseWarnings(project, base string) []string {
	local, remote := "", ""
	remotes, _ := git(project, "remote")
	for _, r := range lines(remotes) {
		if rest, ok := strings.CutPrefix(base, r+"/"); ok {
			remote = base
			if rest == "HEAD" {
				// origin/HEAD is a name for the remote's default branch.
				if sym, err := git(project, "symbolic-ref", "--quiet", "--short", "refs/remotes/"+base); err == nil && sym != "" {
					remote = sym
					rest = strings.TrimPrefix(sym, r+"/")
				}
			}
			local = rest
			break
		}
	}
	if remote == "" {
		local = strings.TrimPrefix(base, "refs/heads/")
		if up, err := git(project, "rev-parse", "--abbrev-ref", "--symbolic-full-name", local+"@{upstream}"); err == nil && up != "" {
			remote = up
		}
	}
	if local == "" {
		return nil
	}
	if _, err := git(project, "rev-parse", "--verify", "--quiet", "refs/heads/"+local); err != nil {
		return nil
	}
	count := func(rng string) int {
		out, err := git(project, "rev-list", "--count", rng)
		if err != nil {
			return 0
		}
		n, _ := strconv.Atoi(out)
		return n
	}
	var out []string
	missing := count("HEAD.." + "refs/heads/" + local)
	if n := missing; n > 0 {
		w := fmt.Sprintf("this branch is missing %s that %s has: they won't be in what's tested, and a bug %s already fixed can come back as a finding. Rebase onto %s first, or place it anyway knowing that.",
			plural(n, "commit", "commits"), local, local, local)
		out = append(out, w)
	}
	if remote != "" && remote != local {
		if n := count(remote + "..refs/heads/" + local); n > 0 {
			w := fmt.Sprintf("local %s is %s ahead of %s", local, plural(n, "commit", "commits"), remote)
			tip, _ := git(project, "rev-parse", "--verify", "--quiet", "refs/heads/"+local+"^{commit}")
			head, _ := git(project, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
			switch {
			case base != remote && !strings.HasSuffix(base, "/HEAD"):
				w += ": not pushed yet"
			case missing > 0:
				w += fmt.Sprintf(", which the changes are compared with: test on top of local %s? (--base %s)", local, local)
			case head == tip:
				// HEAD is local main: its unpushed commits are the changes.
				w = ""
			default:
				// This branch has them already, so they're in the changes
				// as if they were its own.
				w += fmt.Sprintf(", which the changes are compared with, and this branch has them: they're in the changes as if they were its own (--base %s to leave them out)", local)
			}
			if w != "" {
				out = append(out, w)
			}
		}
	}
	return out
}

// notIntoReviewed refuses to write into a read-only contract's reviewed
// checkout or its house from project: where it would be written, not where
// it was asked for (a linked worktree's house is its main checkout's, and
// a subfolder's is inside the checkout), through links, even for a house
// not made yet.
func notIntoReviewed(c *contract, project string) error {
	to := resolvedPath(adminDir(project))
	for _, h := range []string{resolvedPath(c.Project), resolvedPath(adminDir(c.Project)), resolvedPath(contractHouse(c))} {
		if to == h || within(to, h) {
			return usageError{fmt.Sprintf("contract %s was read-only, on %s: writing here would go into %s, the code it reviewed or its house; run it in a checkout you trust (--project DIR)", c.ID, c.Project, to)}
		}
	}
	return nil
}
