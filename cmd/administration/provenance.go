package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

// Provenance (docs/design.md, The build under test): which commit a
// contract is on, and which build each room actually ran. A contract
// records the project's HEAD when it's placed, and a hash of the
// uncommitted work on top of it when there is some. Every room's build is held to it: equipment says what it
// installed (BUILD_COMMIT=), a web room is built from the checkout, and a
// build that isn't the contract's is refused, unless the person placed it
// with --build on purpose. Critics and verifiers read a copy of the
// project at that commit, so a builder who keeps committing while the
// contract runs moves nothing under them.

// A room's build: where it came from (an equipment's name, "app" for the
// web room's setup, "build hook"), the commit it says it was built from,
// and the app's own name and version when the equipment gives them.
type roomBuild struct {
	From    string `json:"from"`
	Commit  string `json:"commit,omitempty"`
	App     string `json:"app,omitempty"`
	Version string `json:"version,omitempty"`
	// Why the build was let in although it isn't exactly the contract's
	// (built without the uncommitted work), or that it said nothing.
	Note string `json:"note,omitempty"`
	// Refused: the room never opened on it.
	Refused bool `json:"refused,omitempty"`
	// Built with the contract's uncommitted work: the build hook's, or a web
	// room's from the checkout as placed.
	withWork bool
}

func (b roomBuild) String() string {
	s := b.From + ": "
	if b.Commit == "" {
		s += "build not reported"
	} else {
		s += short(b.Commit)
	}
	var id []string
	for _, p := range []string{b.App, b.Version} {
		if p != "" {
			id = append(id, p)
		}
	}
	if len(id) > 0 {
		s += " (" + strings.Join(id, " ") + ")"
	}
	if b.Note != "" {
		s += "; " + b.Note
	}
	return s
}

// short is a commit as it's printed: its first ten letters, and a build
// key's uncommitted work after it (<commit>-work-<hash>) as it is.
func short(sha string) string {
	if commit, work, ok := strings.Cut(sha, "-work-"); ok && hexRE.MatchString(commit) {
		return short(commit) + "-work-" + work
	}
	if len(sha) > 10 && hexRE.MatchString(sha) {
		return sha[:10]
	}
	return sha
}

// tagName says whether s can only be a tag's name, so refs/tags/<s> names
// that tag and nothing reached from it (v1~1, v1^{/fix}, v1@{1}).
func tagName(s string) bool {
	return s != "" && !strings.HasPrefix(s, "-") && !strings.ContainsAny(s, "~^:?*[\\ \t") &&
		!strings.Contains(s, "@{") && !strings.Contains(s, "..")
}

var hexRE = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// workHashWhole is the size past which an uncommitted file is hashed by its
// size and modification time rather than read.
const workHashWhole = 8 << 20

// tested is the commit the contract is on: HEAD when it was placed. ""
// outside git. The uncommitted work on top of it, if any, is Work.
func (c *contract) tested() string {
	if c.Tested != "" {
		return c.Tested
	}
	return c.Commit
}

// buildKey is what a build of the contract is kept under: --build's commit,
// else HEAD's, with the uncommitted work's hash when there was some.
func (c *contract) buildKey() string {
	switch {
	case c.Build != "":
		return c.Build
	case c.Work != "":
		return c.Commit + "-" + c.Work
	}
	return c.Commit
}

// want is the commit a room's build must be: --build's, else HEAD's.
func (c *contract) want() string {
	if c.Build != "" {
		return c.Build
	}
	return c.Commit
}

// testedLine is the contract's build, for status and the summary.
func (c *contract) testedLine() string {
	switch {
	case c.Build != "" && c.Build != c.Commit:
		return fmt.Sprintf("Tested build: %s, chosen with --build (HEAD was %s)", short(c.Build), short(c.Commit))
	case c.Work != "":
		return fmt.Sprintf("Tested build: %s plus uncommitted work (%s)", short(c.Commit), c.Work)
	case c.Commit != "":
		return "Tested build: " + short(c.Commit)
	}
	return ""
}

// recordCommit notes what the contract is on: HEAD, a hash of any
// uncommitted work, and --build's commit if the person chose one. A project
// outside git (or with no commit yet) records nothing and checks nothing.
func recordCommit(c *contract, build string) error {
	head, err := git(c.Project, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if err != nil || head == "" {
		if build != "" {
			if _, err := git(c.Project, "rev-parse", "--git-dir"); err != nil {
				return usageError{"--build: the project isn't in git, so there's no commit to hold a build to"}
			}
			return usageError{"--build: the project has no commits to hold a build to"}
		}
		return nil
	}
	c.Commit = head
	c.Work, _ = workHash(c.Project)
	if build != "" {
		// A hash is looked up as an object only (hexCommit). Anything else
		// (a tag, a branch, HEAD) is the commit it names now, and the
		// contract is held to that commit from then on.
		full, err := "", error(nil)
		if b := strings.ToLower(build); hexRE.MatchString(b) {
			var many bool
			if full, many = hexCommitOf(c.Project, b); many {
				return usageError{fmt.Sprintf("--build %s: more than one commit here starts with that; give more of it", build)}
			}
		} else if !strings.HasPrefix(build, "-") {
			// Never an option to git.
			full, err = git(c.Project, "rev-parse", "--verify", "--quiet", build+"^{commit}")
		}
		switch {
		case err == nil && full != "":
			c.Build = full
			// Critics and verifiers read the commit the rooms are held to.
			c.Tested = full
		case hexRE.MatchString(strings.ToLower(build)):
			// A build from a commit this checkout doesn't have (someone
			// else's): held to the letters given.
			c.Build = strings.ToLower(build)
		default:
			return usageError{fmt.Sprintf("--build %s: not a commit here, nor a commit's hash", build)}
		}
		// A chosen build is that commit and nothing else: the uncommitted
		// work isn't in it, so it isn't what's tested.
		c.Work = ""
	}
	return nil
}

// hexCommit is the commit a hash (whole, or its first letters) names in
// the project, "" when it names none or more than one. Only objects are
// looked at: given a short hash, git would take a branch or tag of that
// name first (a branch called 1234567 names wherever it is now), and a
// hash is what was built.
func hexCommit(project, h string) string {
	sha, _ := hexCommitOf(project, h)
	return sha
}

// hexCommitOf is hexCommit, and whether h names more than one commit.
func hexCommitOf(project, h string) (sha string, many bool) {
	ids := []string{h}
	if len(h) != 40 && len(h) != 64 {
		out, _ := git(project, "rev-parse", "--disambiguate="+h)
		ids = lines(out)
	}
	found := ""
	for _, id := range ids {
		if !hexRE.MatchString(id) || len(id) < 40 {
			continue
		}
		// A whole hash is always taken as an object, so this peels a
		// tag's object to its commit and passes over a tree or a file.
		r, err := git(project, "rev-parse", "--verify", "--quiet", id+"^{commit}")
		if err != nil || r == "" {
			continue
		}
		if found != "" && found != r {
			return "", true
		}
		found = r
	}
	return found, false
}

// uncommitted is the project's files that differ from HEAD: changed,
// removed, and new ones not ignored, never secrets. The names are git's
// own (-z), so one with a newline or spaces at its ends is that file.
func uncommitted(project string) (changed, untracked []string, err error) {
	out, err := gitRaw(project, append([]string{"diff", "-z", "--no-ext-diff", "--no-textconv", "--no-renames", "--no-relative", "--name-only", "HEAD", "--", "."}, secretPathspecs...)...)
	if err != nil {
		return nil, nil, err
	}
	u, err := gitRaw(project, append([]string{"ls-files", "-z", "--others", "--exclude-standard", "--", "."}, secretPathspecs...)...)
	if err != nil {
		return nil, nil, err
	}
	return nulls(out), nulls(u), nil
}

// workHash names the uncommitted work as it is now: a hash of every
// uncommitted file's name and contents (a removed one as removed), "" when
// there's none. It's read only, as everything the house does in git is: the
// work isn't committed anywhere, so a build or a copy of it is made from
// the files, and this says whether they're still the ones placed.
func workHash(project string) (string, error) {
	changed, untracked, err := uncommitted(project)
	if err != nil || len(changed)+len(untracked) == 0 {
		return "", err
	}
	top, err := git(project, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	// diff --name-only names files from the top of the repository;
	// ls-files, from the project.
	h := sha256.New()
	add := func(base, f string) {
		fmt.Fprintf(h, "%s\x00", f)
		p := filepath.Join(base, f)
		// Never through a link on the way: a tracked folder replaced by a
		// link is its files removed, as git says, not the files of
		// wherever the link points.
		st, err := lstatIn(base, f)
		switch {
		case err != nil:
			h.Write([]byte("\x00removed"))
		case !st.Mode().IsRegular():
			fmt.Fprintf(h, "\x00%v", st.Mode().Type())
		case st.Size() > workHashWhole:
			// A big file (a build, a dump) is known by its size and time,
			// not read through on every room.
			fmt.Fprintf(h, "\x00%d\x00%d", st.Size(), st.ModTime().UnixNano())
		default:
			if fh, _, err := openRegular(p, st); err == nil {
				_, _ = io.Copy(h, fh)
				fh.Close()
			}
		}
		h.Write([]byte{0})
	}
	for _, f := range changed {
		add(top, f)
	}
	for _, f := range untracked {
		add(project, f)
	}
	return "work-" + hex.EncodeToString(h.Sum(nil))[:10], nil
}

// workChanged is overlayWork's error when the work isn't the one the
// contract was placed with any more.
type workChanged struct{ want, got string }

func (e workChanged) Error() string {
	return fmt.Sprintf("the uncommitted work has changed since the contract was placed (%s, now %s): commit it, or place the contract again", e.want, orNone(e.got))
}

// overlayWork copies the project's uncommitted work onto a copy of it at
// HEAD (dest is the copy of the repository's top), and says if the work
// isn't the one the contract was placed with any more: before the copy,
// and after it, so a file saved while it was copied can't land in a copy
// named for the work as it was.
func overlayWork(project, dest, want string) error {
	if got, err := workHash(project); err != nil {
		return err
	} else if got != want {
		return workChanged{want, got}
	}
	if err := copyWork(project, dest); err != nil {
		return err
	}
	if got, err := workHash(project); err != nil {
		return err
	} else if got != want {
		return workChanged{want, got}
	}
	return nil
}

// leftOutFile is the file in a copy's top folder naming the uncommitted
// files too big to copy (workHashWhole), so what reads the copy can say they
// aren't in it.
const leftOutFile = ".administration-left-out"

// leftOut is the uncommitted files the copy at root (copyRoot) left out.
func leftOut(root string) []string {
	b, err := os.ReadFile(filepath.Join(root, leftOutFile))
	if err != nil {
		return nil
	}
	return lines(string(b))
}

// leftOutNote says which uncommitted files the contract's copy left out,
// "" when none.
func leftOutNote(c *contract) string {
	if c.Work == "" || c.Build != "" {
		return ""
	}
	root, err := copyRoot(c, c.tested(), c.Work)
	if err != nil {
		return ""
	}
	files := leftOut(root)
	if len(files) == 0 {
		return ""
	}
	if len(files) > 5 {
		files = append(files[:5:5], fmt.Sprintf("and %d more", len(files)-5))
	}
	return fmt.Sprintf("uncommitted files over %d MB were left out of the copy (not even at the commit's version): %s", workHashWhole>>20, strings.Join(files, ", "))
}

// copyWork copies the uncommitted files onto dest, as they are now.
func copyWork(project, dest string) error {
	changed, untracked, err := uncommitted(project)
	if err != nil {
		return err
	}
	top, err := git(project, "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	rel, _ := filepath.Rel(realPath(top), realPath(project))
	var left []string
	cp := func(base, f, dest, name string) error {
		// As workHash reads it: no link followed on the way, so what's
		// copied is the project's own file.
		src := filepath.Join(base, f)
		st, err := lstatIn(base, f)
		if err != nil {
			// Removed in the work: gone from the copy too, unless the way
			// to it is a link (then it was never in the copy's own files).
			if dst, ok, _ := copyPath(dest, name, false); ok {
				_ = os.Remove(dst)
			}
			return nil
		}
		if !st.Mode().IsRegular() {
			return nil // a link or a folder: not copied out of the project
		}
		dst, _, err := copyPath(dest, name, true)
		if err != nil {
			return err
		}
		if st.Size() > workHashWhole {
			// A big uncommitted file (a dump, a build) is no code to read,
			// and copying it would make every contract as big: left out,
			// not left at the commit's version. Named, so nothing says the
			// copy has all the work.
			_ = os.RemoveAll(dst)
			left = append(left, name)
			return nil
		}
		in, _, err := openRegular(src, st)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, st.Mode().Perm()|0o600)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	}
	for _, f := range changed {
		if err := cp(top, f, dest, f); err != nil {
			return err
		}
	}
	for _, f := range untracked {
		if err := cp(project, f, dest, filepath.Join(rel, f)); err != nil {
			return err
		}
	}
	if len(left) > 0 {
		var b strings.Builder
		for _, f := range left {
			// One a line: a name with a newline in it is said as its quoted
			// form.
			if strings.ContainsAny(f, "\n\r") {
				f = fmt.Sprintf("%q", f)
			}
			b.WriteString(f + "\n")
		}
		return os.WriteFile(filepath.Join(dest, leftOutFile), []byte(b.String()), 0o600)
	}
	return nil
}

// copyPath is where name goes in the copy at dest, made safe to write: the
// copy has the commit's links in it, and a file the work put where a link
// was (or in a folder where a link to one was) must land in the copy, not
// wherever the link points. With make, a link or file on the way is
// replaced by a folder and a link or folder at name itself is removed;
// without, ok is false when the way to name isn't all folders.
func copyPath(dest, name string, make bool) (dst string, ok bool, err error) {
	parts := strings.Split(filepath.Clean(name), string(filepath.Separator))
	for _, p := range parts {
		if p == ".." || p == "" {
			return "", false, fmt.Errorf("%q is not a path in the project", name)
		}
	}
	dir, ok, err := foldersIn(dest, parts[:len(parts)-1], make)
	if !ok || err != nil {
		return "", false, err
	}
	dst = filepath.Join(dir, parts[len(parts)-1])
	if st, err := os.Lstat(dst); make && err == nil && !st.Mode().IsRegular() {
		if err := os.RemoveAll(dst); err != nil {
			return "", false, err
		}
	}
	return dst, true, nil
}

// foldersIn is dest/parts as folders all the way, none of them a link:
// with make, a link or a file on the way is replaced by a folder and a
// missing one is made; without, ok is false when the way isn't all folders.
func foldersIn(dest string, parts []string, make bool) (dir string, ok bool, err error) {
	dir = dest
	for _, p := range parts {
		if p == ".." || p == "" {
			return "", false, fmt.Errorf("%q is not a path in the folder", filepath.Join(parts...))
		}
		dir = filepath.Join(dir, p)
		st, err := os.Lstat(dir)
		if err == nil && st.IsDir() {
			continue
		}
		if !make {
			return "", false, nil
		}
		if err == nil {
			if err := os.Remove(dir); err != nil {
				return "", false, err
			}
		}
		if err := os.Mkdir(dir, 0o700); err != nil {
			return "", false, err
		}
	}
	return dir, true, nil
}

// copyKept says which copy of the project the contract reads is there:
// "whole" for the one at its commit with its work (or the copy it was
// placed with), "commit" for the one without the work (the work had changed
// before it was made again), "" for none (pruned, or never made, and then
// a reader can have had the project as it is now).
func copyKept(c *contract) string {
	if c.Checkout != "" && isDir(c.Checkout) {
		return "whole"
	}
	if c.tested() == "" {
		return ""
	}
	if root, err := copyRoot(c, c.tested(), c.Work); err == nil && isDir(root) {
		return "whole"
	}
	if c.Work != "" {
		if root, err := copyRoot(c, c.tested(), ""); err == nil && isDir(root) {
			return "commit"
		}
	}
	return ""
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// makeCheckout is a plain copy of the project at sha, in dest: a clone
// that borrows the project's objects, checked out, its .git then removed,
// so what's left is the files and nothing that points back; with work
// (the hash of the uncommitted work), that work copied on top. The
// project's own repository is only read. A project in a subfolder of its
// repository is that subfolder of the copy. It returns the folder to read.
func makeCheckout(project, sha, work, dest string) (string, error) {
	top, err := git(project, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(realPath(top), realPath(project))
	if err != nil || strings.HasPrefix(rel, "..") {
		rel = "."
	}
	if isDir(dest) {
		return filepath.Join(dest, rel), nil // made already, by whoever got there first
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return "", err
	}
	// Made beside dest and renamed into place whole, so two at once can't
	// meet in one folder, and a failure removes only its own.
	tmp, err := os.MkdirTemp(filepath.Dir(dest), "."+strings.TrimPrefix(filepath.Base(dest), ".")+"-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp) // gone by then if the rename worked
	// Each run where its config is the one that applies: the clone from the
	// books, the checkout in the clone (whose config has nothing of the
	// project's), with any filter the person's own config names emptied.
	for _, step := range []struct {
		dir  string
		args []string
	}{
		{filepath.Dir(tmp), []string{"clone", "--quiet", "--shared", "--no-checkout", top, tmp}},
		{tmp, []string{"-C", tmp, "-c", "advice.detachedHead=false", "checkout", "--quiet", "--detach", sha}},
	} {
		cmd := gitCmd(step.dir, step.args...)
		// The clone has none of the project's LFS objects: a file kept in
		// LFS is its pointer in the copy, not a failed checkout.
		cmd.Env = append(cmd.Env, "GIT_LFS_SKIP_SMUDGE=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("a checkout at %s: %v: %s", short(sha), err, strings.TrimSpace(string(out)))
		}
	}
	_ = os.RemoveAll(filepath.Join(tmp, ".git"))
	// The project's folder, at this commit, is folders all the way: one
	// that was a link then (to anywhere) would have the house's env/ and
	// the build hook, or a critic, sent through it.
	dir := tmp
	for _, p := range strings.Split(rel, string(filepath.Separator)) {
		if p == "." {
			continue
		}
		dir = filepath.Join(dir, p)
		if st, err := os.Lstat(dir); err == nil && !st.IsDir() {
			return "", fmt.Errorf("at %s, the project's folder (%s in its repository) isn't a folder", short(sha), rel)
		}
	}
	if work != "" {
		if err := overlayWork(project, tmp, work); err != nil {
			return "", err
		}
	}
	if err := os.Rename(tmp, dest); err != nil && !isDir(dest) {
		return "", err
	}
	return filepath.Join(dest, rel), nil
}

// checkoutDir is where a contract kept its own copy of the project before
// copies were shared (copyRoot); prune still clears it.
func checkoutDir(dir string) string { return filepath.Join(dir, ".checkout") }

// copyRoot is where the copy of the project at sha (with the uncommitted
// work's hash, when there's some) is kept: one per commit and work, in the
// books beside the builds, shared by every contract on it. Copies are
// only read, so sharing one is safe; prune removes it once no contract it
// keeps whole is on it.
func copyRoot(c *contract, sha, work string) (string, error) {
	books, err := booksDir()
	if err != nil {
		return "", err
	}
	name := sha
	if work != "" {
		name += "-" + work
	}
	return filepath.Join(books, "copies", projectFolder(contractKey(c)), name), nil
}

// projectCopy is the folder critics and verifiers read as the project: the
// contract's checkout at its tested commit, made now if it isn't there yet
// (one placed with no critic, or pruned), or the project itself when there
// is no commit to copy (a contract that recorded none), or it can't be had;
// why says which.
func projectCopy(c *contract, dir string) (path, why string) {
	if c.Checkout != "" && isDir(c.Checkout) {
		return c.Checkout, ""
	}
	sha := c.tested()
	if sha == "" {
		return c.Project, "the contract recorded no commit (placed before contracts kept their commit, or outside git), so this reads the project as it is now"
	}
	if !isDir(c.Project) {
		return c.Project, "the project is gone, so there's no copy of it at " + short(sha)
	}
	note := ""
	work := c.Work
	// The work placed is gone (committed, changed): the commit is still the
	// nearest thing to what was tested.
	without := func() {
		work, note = "", fmt.Sprintf("the uncommitted work the contract was placed with (%s) has changed since, so the copy at %s is without it", c.Work, short(sha))
	}
	if work != "" {
		if now, _ := workHash(c.Project); now != work {
			without()
		}
	}
	for {
		root, err := copyRoot(c, sha, work)
		if err != nil {
			return c.Project, err.Error() + "; reading the project as it is now"
		}
		p, err := makeCheckout(c.Project, sha, work, root)
		var wc workChanged
		if errors.As(err, &wc) && work != "" {
			// It changed while the copy was made.
			without()
			continue
		}
		if err != nil {
			return c.Project, err.Error() + "; reading the project as it is now"
		}
		// Not written back to contract.json: this runs in workers and
		// verifiers, and the contract's file is place's and close's to
		// write. The copy is found again at copyRoot, which makeCheckout
		// returns as it is when it's there already.
		c.Checkout = p
		return p, note
	}
}

// liveDrift says how the project's checkout differs from the copy the
// contract reads, "" when it doesn't (or there's no copy to differ from).
func liveDrift(c *contract) string {
	sha := c.tested()
	if sha == "" {
		return ""
	}
	head, _ := git(c.Project, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	now, _ := workHash(c.Project)
	want := c.Work
	if c.Build != "" {
		want = "" // a --build contract's copy has no work
	}
	switch {
	case head != sha:
		return fmt.Sprintf("is at %s, and the copy at %s", short(head), short(sha))
	case now != want && want == "":
		return "has uncommitted work the copy doesn't"
	case now != want:
		return "has uncommitted work that has changed since the copy was made"
	}
	return ""
}

// checkBuild holds what a room says it runs to the contract's commit. A
// build that names nothing is let in and said so; one from another commit
// is refused, with how to test it on purpose. A build of HEAD when the
// contract also has uncommitted work is let in with a note: the work under
// review may not all be in it.
func checkBuild(c *contract, b roomBuild) (roomBuild, error) {
	got := strings.TrimSpace(b.Commit)
	dirty := false
	// Every mark, in whatever order the build's stamp put them on.
	for cut := true; cut; {
		cut = false
		for _, suf := range []string{"-dirty", "+dirty", "-modified"} {
			if strings.HasSuffix(strings.ToLower(got), suf) {
				got, dirty, cut = got[:len(got)-len(suf)], true, true
			}
		}
	}
	if hexRE.MatchString(strings.ToLower(got)) {
		got = strings.ToLower(got)
	}
	b.Commit = got
	want := c.want()
	if got == "" {
		if want != "" {
			b.Note = "it didn't say which commit it was built from"
		}
		return b, nil
	}
	if want == "" {
		if dirty && b.Note == "" {
			b.Note = "built with uncommitted work"
		}
		return b, nil
	}
	full := got
	if hexRE.MatchString(got) {
		if r := hexCommit(c.Project, got); r != "" {
			full = r
		}
	} else if !tagName(got) {
		// Not a tag's name (v1~1, v1^{/fix}): nothing here makes it a commit
		// that was built.
	} else if r, err := git(c.Project, "rev-parse", "--verify", "--quiet", "refs/tags/"+got+"^{commit}"); err == nil && r != "" {
		// A tag this checkout knows. Only a tag: a branch or @ names
		// wherever the checkout is now, not what was built.
		full = r
	}
	if !hexRE.MatchString(full) {
		// Not a commit, and nothing here makes it one (a version, a branch,
		// a tag from elsewhere): --build couldn't name it either.
		b.Refused = true
		return b, refusal{fmt.Sprintf("%s says it was built from %q, which isn't a commit: have its setup print BUILD_COMMIT= with the commit's hash (the one stamped in the build), and BUILD_VERSION= for a version", b.From, got)}
	}
	// One may be short and the other whole, either way round: --build with
	// the first letters of someone else's commit, and equipment that reports
	// all of it.
	same := func(sha string) bool {
		if sha == "" {
			return false
		}
		if full == sha {
			return true
		}
		n := min(len(full), len(sha))
		return n >= 7 && full[:n] == sha[:n]
	}
	if same(want) {
		b.Commit = full
		switch {
		case b.Note != "":
			// The caller knows better (the work changed while it built).
		case dirty && c.Work == "":
			b.Note = "built with uncommitted work, though the contract has none"
		case dirty || c.Build == "" && c.Work != "" && !b.withWork:
			if dirty {
				b.Note = "built with uncommitted work, which may not be the work under contract"
			} else {
				b.Note = "built from " + short(c.Commit) + " without the uncommitted work"
			}
		}
		return b, nil
	}
	b.Commit = full // what a tag named, so the refused build shows its commit
	b.Refused = true
	what := "the contract is on " + short(want) + " (HEAD when it was placed)"
	if c.Build != "" {
		what = "the contract was placed to test " + short(want) + " (--build)"
	}
	return b, refusal{fmt.Sprintf("%s runs a build of %s, and %s: rebuild it from %s, or place the contract with --build %s to test that build on purpose",
		b.From, short(full), what, short(want), full)}
}

// buildFromVars is the build an equipment's setup reported.
func buildFromVars(name string, v map[string]string) roomBuild {
	return roomBuild{From: name, Commit: v["BUILD_COMMIT"], App: v["BUILD_APP"], Version: v["BUILD_VERSION"]}
}

// addBuild records a room's build in its status, once per source.
func addBuild(s *status, b roomBuild) {
	for i, o := range s.Builds {
		if o.From == b.From {
			s.Builds[i] = b
			return
		}
	}
	s.Builds = append(s.Builds, b)
}

// provenanceNote is the summary's word on the build: the commit the
// contract is on, what each room ran, and what place warned about the
// base. "" for a contract with nothing to say (outside git).
func provenanceNote(c *contract, dir string) string {
	var b strings.Builder
	if t := c.testedLine(); t != "" {
		b.WriteString(t + ".")
		switch copied := copyKept(c); {
		case copied == "whole" || isDir(checkoutDir(dir)):
			b.WriteString(" Critics and verifiers read the project as it was then, not as it is now.")
			if n := leftOutNote(c); n != "" {
				b.WriteString(" " + strings.ToUpper(n[:1]) + n[1:] + ".")
			}
		case copied == "commit":
			fmt.Fprintf(&b, " Critics and verifiers read the project at %s as it was then, without the uncommitted work, which had changed before a copy of it could be made again.", short(c.tested()))
		}
		if c.Build != "" && c.tested() != c.Build {
			fmt.Fprintf(&b, " This checkout doesn't have %s, so critics and verifiers read HEAD (%s) instead.", short(c.Build), short(c.Commit))
		}
		b.WriteString("\n")
	}
	for _, p := range c.Professionals {
		for _, rb := range readStatus(dir, p.Name).Builds {
			if rb.Refused {
				fmt.Fprintf(&b, "- %s's room was refused %s\n", p.Name, rb)
			} else {
				fmt.Fprintf(&b, "- %s's room ran %s\n", p.Name, rb)
			}
		}
	}
	for _, w := range c.BaseWarnings {
		fmt.Fprintf(&b, "- At place: %s\n", w)
	}
	if b.Len() > 0 {
		b.WriteString("\n")
	}
	return b.String()
}
