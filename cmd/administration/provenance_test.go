package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

func writeT(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readT(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A link the commit has, replaced in the work by a file or by a folder of
// new files: the copy gets the files, and what the link pointed at is
// never written.
func TestTheCopyNeverWritesThroughTheCommitsLinks(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	writeT(t, filepath.Join(outside, "real.yml"), "keep me\n")
	project := filepath.Join(root, "project")
	writeT(t, filepath.Join(project, "README"), "hi\n")
	gitT(t, project, "init", "-q")
	for _, l := range [][2]string{{filepath.Join(outside, "real.yml"), "cfg"}, {outside, "lib"}} {
		if err := os.Symlink(l[0], filepath.Join(project, l[1])); err != nil {
			t.Fatal(err)
		}
	}
	gitT(t, project, "add", "-A")
	gitT(t, project, "commit", "-qm", "links")
	head := gitT(t, project, "rev-parse", "HEAD")[:40]

	// The work: cfg a file, lib a folder with a new file in it.
	for _, l := range []string{"cfg", "lib"} {
		if err := os.Remove(filepath.Join(project, l)); err != nil {
			t.Fatal(err)
		}
	}
	writeT(t, filepath.Join(project, "cfg"), "the work's\n")
	writeT(t, filepath.Join(project, "lib", "new.go"), "package lib\n")

	work, err := workHash(project)
	if err != nil || work == "" {
		t.Fatalf("workHash: %q %v", work, err)
	}
	dest := filepath.Join(root, "copy")
	p, err := makeCheckout(project, head, work, dest)
	if err != nil {
		t.Fatal(err)
	}
	if got := readT(t, filepath.Join(outside, "real.yml")); got != "keep me\n" {
		t.Fatalf("the link's target was written: %q", got)
	}
	if _, err := os.Stat(filepath.Join(outside, "new.go")); err == nil {
		t.Fatal("a new file landed through the commit's link to a folder")
	}
	if got := readT(t, filepath.Join(p, "cfg")); got != "the work's\n" {
		t.Fatalf("the copy's cfg = %q", got)
	}
	if st, err := os.Lstat(filepath.Join(p, "lib")); err != nil || !st.IsDir() {
		t.Fatalf("the copy's lib isn't a folder: %v %v", st, err)
	}
	if got := readT(t, filepath.Join(p, "lib", "new.go")); got != "package lib\n" {
		t.Fatalf("the copy's lib/new.go = %q", got)
	}
}

// A file renamed in the work is gone from the copy under its old name,
// even when the repository's own config would show renames, or paths
// relative to a subfolder.
func TestTheCopyHasARenameAsARename(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	writeT(t, filepath.Join(project, "a.txt"), "the same words, long enough to be a rename\n")
	writeT(t, filepath.Join(project, "sub", "keep.txt"), "kept\n")
	gitT(t, project, "init", "-q")
	gitT(t, project, "config", "diff.renames", "true")
	gitT(t, project, "config", "diff.relative", "true")
	gitT(t, project, "add", "-A")
	gitT(t, project, "commit", "-qm", "one")
	head := gitT(t, project, "rev-parse", "HEAD")[:40]
	gitT(t, project, "mv", "a.txt", "b.txt")

	work, err := workHash(project)
	if err != nil || work == "" {
		t.Fatalf("workHash: %q %v", work, err)
	}
	p, err := makeCheckout(project, head, work, filepath.Join(root, "copy"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(p, "a.txt")); err == nil {
		t.Fatal("the copy still has a.txt, renamed in the work")
	}
	if _, err := os.Stat(filepath.Join(p, "b.txt")); err != nil {
		t.Fatal("the copy has no b.txt")
	}
	if _, err := os.Stat(filepath.Join(p, "sub", "keep.txt")); err != nil {
		t.Fatal("the copy lost sub/keep.txt")
	}
}

// --build chooses what critics and verifiers read too, and its build has
// no uncommitted work in it.
func TestBuildIsWhatCriticsRead(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	writeT(t, filepath.Join(project, "f"), "one\n")
	gitT(t, project, "init", "-q")
	gitT(t, project, "add", "-A")
	gitT(t, project, "commit", "-qm", "one")
	first := gitT(t, project, "rev-parse", "HEAD")[:40]
	writeT(t, filepath.Join(project, "f"), "two\n")
	gitT(t, project, "commit", "-qam", "two")
	writeT(t, filepath.Join(project, "f"), "three, uncommitted\n")

	c := &contract{Project: project}
	if err := recordCommit(c, first[:8]); err != nil {
		t.Fatal(err)
	}
	if c.tested() != first || c.Work != "" {
		t.Fatalf("tested %q work %q, want %q and none", c.tested(), c.Work, first)
	}
	c = &contract{Project: project}
	if err := recordCommit(c, "0123456"); err != nil {
		t.Fatal(err)
	}
	if c.tested() != c.Commit {
		t.Fatalf("a build this checkout doesn't have: tested %q, want HEAD %q", c.tested(), c.Commit)
	}
}

// A kept build is handed out only when the hook and env/ it was built with
// are still the house's.
func TestABuildIsKeptByItsRecipe(t *testing.T) {
	house := t.TempDir()
	hook := filepath.Join(house, "build")
	writeT(t, hook, "#!/bin/sh\n")
	writeT(t, filepath.Join(house, "env", ".env"), "API=one\n")
	c := &contract{House: house}
	r1 := buildRecipe(c, hook)
	writeT(t, filepath.Join(house, "env", ".env"), "API=two\n")
	r2 := buildRecipe(c, hook)
	writeT(t, hook, "#!/bin/sh\necho changed\n")
	r3 := buildRecipe(c, hook)
	if r1 == r2 || r2 == r3 || r1 == r3 {
		t.Fatalf("recipes %s %s %s should all differ", r1, r2, r3)
	}

	dir := t.TempDir()
	art := filepath.Join(dir, "out", "app")
	writeT(t, art, "x")
	writeT(t, filepath.Join(dir, "artifact"), art+"\n")
	if _, ok := builtAlready(dir, r3); ok {
		t.Fatal("a build with no recipe was handed out")
	}
	writeT(t, filepath.Join(dir, "recipe"), r3+"\n")
	if _, ok := builtAlready(dir, r3); !ok {
		t.Fatal("the build made by this recipe wasn't handed out")
	}
	if _, ok := builtAlready(dir, r1); ok {
		t.Fatal("a build made by another recipe was handed out")
	}
}

// A build with uncommitted work is made from the contract's own checkout:
// the work as placed, not the working tree as it is now.
func TestABuildWithWorkIsMadeFromTheContractsCheckout(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ADMINISTRATION_HOME", filepath.Join(root, "books"))
	project := filepath.Join(root, "project")
	writeT(t, filepath.Join(project, "app", "f"), "one\n")
	gitT(t, project, "init", "-q")
	gitT(t, project, "add", "-A")
	gitT(t, project, "commit", "-qm", "one")
	sub := filepath.Join(project, "app")
	writeT(t, filepath.Join(sub, "f"), "placed\n")
	c := &contract{ID: "ab12", Project: sub}
	if err := recordCommit(c, ""); err != nil || c.Work == "" {
		t.Fatalf("recordCommit: %v, work %q", err, c.Work)
	}
	top, err := copyRoot(c, c.Commit, c.Work)
	if err != nil {
		t.Fatal(err)
	}
	p, err := makeCheckout(sub, c.Commit, c.Work, top)
	if err != nil {
		t.Fatal(err)
	}
	c.Checkout = p
	writeT(t, filepath.Join(sub, "f"), "edited after place\n")

	work, err := buildCheckout(c, c.Commit, c.Work, filepath.Join(root, "src"))
	if err != nil {
		t.Fatal(err)
	}
	if got := readT(t, filepath.Join(work, "f")); got != "placed\n" {
		t.Fatalf("the build's checkout has %q, want the work as placed", got)
	}
}

// A short hash names an object, never a branch that happens to be called
// the same: a branch names wherever it is now.
func TestAHashIsNeverTakenForABranch(t *testing.T) {
	project := t.TempDir()
	writeT(t, filepath.Join(project, "f"), "one\n")
	gitT(t, project, "init", "-q")
	gitT(t, project, "add", "-A")
	gitT(t, project, "commit", "-qm", "one")
	first := gitT(t, project, "rev-parse", "HEAD")[:40]
	writeT(t, filepath.Join(project, "f"), "two\n")
	gitT(t, project, "commit", "-qam", "two")
	head := gitT(t, project, "rev-parse", "HEAD")[:40]
	// A branch named with the first letters of the older commit, at HEAD.
	gitT(t, project, "branch", first[:7])
	gitT(t, project, "branch", "1234567")

	if got := hexCommit(project, first[:7]); got != first {
		t.Fatalf("hexCommit(%s) = %q, want the commit %s, not the branch at %s", first[:7], got, first, head)
	}
	if got := hexCommit(project, "1234567"); got != "" {
		t.Fatalf("hexCommit(1234567) = %q, want none: it's only a branch", got)
	}
	c := &contract{Project: project}
	if err := recordCommit(c, first[:7]); err != nil || c.Build != first {
		t.Fatalf("--build %s: build %q (%v), want %s", first[:7], c.Build, err, first)
	}
	c = &contract{Project: project, Commit: head}
	if _, err := checkBuild(c, roomBuild{From: "phone", Commit: "1234567"}); err == nil {
		t.Fatal("BUILD_COMMIT=1234567, a branch at HEAD, was let in as HEAD's build")
	}
}

// A build's marks come off in whatever order its stamp put them on.
func TestEveryDirtyMarkComesOff(t *testing.T) {
	project := t.TempDir()
	writeT(t, filepath.Join(project, "f"), "one\n")
	gitT(t, project, "init", "-q")
	gitT(t, project, "add", "-A")
	gitT(t, project, "commit", "-qm", "one")
	head := gitT(t, project, "rev-parse", "HEAD")[:40]
	c := &contract{Project: project, Commit: head}
	for _, got := range []string{head[:9] + "-modified-dirty", head[:9] + "-dirty-modified", head[:9] + "+dirty"} {
		b, err := checkBuild(c, roomBuild{From: "phone", Commit: got})
		if err != nil || b.Commit != head || b.Note == "" {
			t.Fatalf("%s: %+v %v", got, b, err)
		}
	}
	b, _ := checkBuild(&contract{Project: project}, roomBuild{From: "phone", Commit: "abc1234-dirty"})
	if b.Note == "" {
		t.Fatal("a dirty build on a contract with no commit lost its mark without a word")
	}
}

// What copyHouseEnv copies is all in the recipe: a mode or a new folder
// builds again.
func TestARecipeHasEnvsModesAndFolders(t *testing.T) {
	house := t.TempDir()
	hook := filepath.Join(house, "build")
	writeT(t, hook, "#!/bin/sh\n")
	writeT(t, filepath.Join(house, "env", "ci.sh"), "echo hi\n")
	c := &contract{House: house}
	r1 := buildRecipe(c, hook)
	if err := os.Chmod(filepath.Join(house, "env", "ci.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	r2 := buildRecipe(c, hook)
	if err := os.Mkdir(filepath.Join(house, "env", "cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	r3 := buildRecipe(c, hook)
	if r1 == r2 || r2 == r3 {
		t.Fatalf("recipes %s %s %s should all differ", r1, r2, r3)
	}
	if buildRecipe(c, hook) != r3 {
		t.Fatal("the same env/ gave another recipe")
	}
}

// A copy made again after the work changed is the commit without it, and
// the summary and the verifier say so rather than "as it was then".
func TestACopyWithoutTheWorkIsSaidSo(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ADMINISTRATION_HOME", filepath.Join(root, "books"))
	project := filepath.Join(root, "project")
	writeT(t, filepath.Join(project, "f"), "one\n")
	gitT(t, project, "init", "-q")
	gitT(t, project, "add", "-A")
	gitT(t, project, "commit", "-qm", "one")
	writeT(t, filepath.Join(project, "f"), "placed\n")
	c := &contract{ID: "ab12", Project: project}
	if err := recordCommit(c, ""); err != nil || c.Work == "" {
		t.Fatalf("recordCommit: %v, work %q", err, c.Work)
	}
	dir := filepath.Join(root, "books", "contracts", "ab12")
	if note := provenanceNote(c, dir); strings.Contains(note, "as it was then") {
		t.Fatalf("no copy yet, and the summary says %q", note)
	}
	writeT(t, filepath.Join(project, "f"), "edited after place\n")
	p, why := projectCopy(c, dir)
	if p == project || why == "" {
		t.Fatalf("projectCopy = %q, %q: want the commit's copy, said", p, why)
	}
	if got := readT(t, filepath.Join(p, "f")); got != "one\n" {
		t.Fatalf("the copy has %q, want the commit's", got)
	}
	c.Checkout = ""
	note := provenanceNote(c, dir)
	if strings.Contains(note, "as it was then, not as it is now") || !strings.Contains(note, "without the uncommitted work") {
		t.Fatalf("summary: %q", note)
	}
}

func TestDevicePortsMustBeARange(t *testing.T) {
	for v, ok := range map[string]bool{"5620-5680": true, "5700-5720": true, "5680-5620": false, "5621-5681": false, "56-5680": false, "5620-70000": false} {
		if got := devicePortsBad(v) == ""; got != ok {
			t.Errorf("devicePortsBad(%q) = %q", v, devicePortsBad(v))
		}
	}
	t.Setenv("ADMINISTRATION_DEVICE_PORTS", "5680-5620")
	if devicePorts() != "5620-5680" {
		t.Fatalf("a backwards range was handed on: %s", devicePorts())
	}
}

// A setup that echoes the hook's commit installed the hook's build, work
// and all.
func TestEchoingTheHooksCommitIsTheHooksBuild(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	s := &status{BuildArtifact: "/b/app", BuildCommit: sha + "-work-abc"}
	if !hookBuildNamed(s, sha) || !hookBuildNamed(s, sha[:7]) || hookBuildNamed(s, "fedcba9") || hookBuildNamed(s, "") {
		t.Fatal("hookBuildNamed")
	}
	env := strings.Join(buildEnv(s), " ")
	if !strings.Contains(env, "ADMINISTRATION_BUILD_COMMIT="+sha+" ") || !strings.Contains(env, "ADMINISTRATION_BUILD_WORK=work-abc") {
		t.Fatalf("buildEnv: %s", env)
	}
	c := &contract{Project: t.TempDir(), Commit: sha, Work: "work-abc"}
	b, err := checkBuild(c, roomBuild{From: "phone", Commit: sha, withWork: hookBuildNamed(s, sha)})
	if err != nil || b.Note != "" {
		t.Fatalf("%+v %v", b, err)
	}
}

// A reported tag is a tag's name, not something reached from one (v1~0 is
// the commit, but nothing says that's what was built), and --build never
// passes its value to git as an option.
func TestATagIsATagsNameAndBuildIsNoOption(t *testing.T) {
	project := t.TempDir()
	writeT(t, filepath.Join(project, "f"), "one\n")
	gitT(t, project, "init", "-q")
	gitT(t, project, "add", "-A")
	gitT(t, project, "commit", "-qm", "one")
	first := gitT(t, project, "rev-parse", "HEAD")[:40]
	gitT(t, project, "tag", "v1")
	c := &contract{Project: project, Commit: first}
	if _, err := checkBuild(c, roomBuild{From: "phone", Commit: "v1"}); err != nil {
		t.Fatalf("BUILD_COMMIT=v1: %v", err)
	}
	for _, bad := range []string{"v1~0", "v1^{commit}", "v1@{0}"} {
		if _, err := checkBuild(c, roomBuild{From: "phone", Commit: bad}); err == nil {
			t.Fatalf("BUILD_COMMIT=%s was let in as a tag", bad)
		}
	}
	for _, ref := range []string{"-q", "--all"} {
		if err := recordCommit(&contract{Project: project}, ref); err == nil || !strings.Contains(err.Error(), "not a commit here") {
			t.Fatalf("--build %s: %v", ref, err)
		}
	}
}

// Outside git, --build says so, not that there's no commit yet.
func TestBuildOutsideGitSaysSo(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	err := recordCommit(&contract{Project: dir}, "0123456")
	if err == nil || !strings.Contains(err.Error(), "isn't in git") {
		t.Fatalf("err %v", err)
	}
	gitT(t, dir, "init", "-q")
	err = recordCommit(&contract{Project: dir}, "0123456")
	if err == nil || !strings.Contains(err.Error(), "no commits") {
		t.Fatalf("err %v", err)
	}
}

// An uncommitted file too big to copy is named as left out, so nothing
// says the copy has all the work.
func TestABigUncommittedFileIsNamedAsLeftOut(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ADMINISTRATION_HOME", filepath.Join(root, "books"))
	project := filepath.Join(root, "project")
	writeT(t, filepath.Join(project, "f"), "one\n")
	gitT(t, project, "init", "-q")
	gitT(t, project, "add", "-A")
	gitT(t, project, "commit", "-qm", "one")
	writeT(t, filepath.Join(project, "dump.sql"), strings.Repeat("x", workHashWhole+1))
	c := &contract{Project: project, Key: "k"}
	if err := recordCommit(c, ""); err != nil || c.Work == "" {
		t.Fatalf("recordCommit: %v, work %q", err, c.Work)
	}
	if leftOutNote(c) != "" {
		t.Fatal("a note with no copy")
	}
	dest, err := copyRoot(c, c.Commit, c.Work)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := makeCheckout(project, c.Commit, c.Work, dest); err != nil {
		t.Fatal(err)
	}
	if got := leftOut(dest); len(got) != 1 || got[0] != "dump.sql" {
		t.Fatalf("left out %q", got)
	}
	if n := leftOutNote(c); !strings.Contains(n, "dump.sql") {
		t.Fatalf("note %q", n)
	}
}

// A project in a subfolder that was a link at the commit: the copy is
// refused, so nothing (the house's env/, the build hook) goes through it.
func TestTheProjectsFolderIsNeverALinkInTheCopy(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "repo")
	writeT(t, filepath.Join(repo, "README"), "hi\n")
	gitT(t, repo, "init", "-q")
	if err := os.Symlink(outside, filepath.Join(repo, "app")); err != nil {
		t.Fatal(err)
	}
	gitT(t, repo, "add", "-A")
	gitT(t, repo, "commit", "-qm", "a link")
	linked := gitT(t, repo, "rev-parse", "HEAD")[:40]
	gitT(t, repo, "rm", "-q", "app")
	writeT(t, filepath.Join(repo, "app", "f"), "real\n")
	gitT(t, repo, "add", "-A")
	gitT(t, repo, "commit", "-qm", "a folder")
	sub := filepath.Join(repo, "app")
	if _, err := makeCheckout(sub, linked, "", filepath.Join(root, "copy")); err == nil || !strings.Contains(err.Error(), "isn't a folder") {
		t.Fatalf("err %v, want the linked folder refused", err)
	}
	head := gitT(t, repo, "rev-parse", "HEAD")[:40]
	p, err := makeCheckout(sub, head, "", filepath.Join(root, "copy2"))
	if err != nil || readT(t, filepath.Join(p, "f")) != "real\n" {
		t.Fatalf("copy at HEAD: %v", err)
	}
}
