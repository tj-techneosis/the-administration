package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestASecretIsKnownByItsPathInAnyCase(t *testing.T) {
	for _, p := range []string{".env", ".ENV", "app/.env.local", "KEY.PEM", "certs/Server.Pem", "Credentials.json",
		".git/config", "sub/.git/config", ".aws/credentials", "home/.AWS/config", "android/local.properties", "ID_RSA"} {
		if !secret(p) {
			t.Errorf("%q isn't a secret", p)
		}
	}
	for _, p := range []string{"config", "docs/config.md", "credentials", "aws/notes.md", "src/env.go", ".aws"} {
		if secret(p) {
			t.Errorf("%q is a secret", p)
		}
	}
}

func TestLstatInFollowsNoLinkOnTheWay(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	writeT(t, filepath.Join(outside, "deploy.txt"), "private")
	writeT(t, filepath.Join(root, "real", "a.txt"), "a")
	if err := os.Symlink(outside, filepath.Join(root, "keys")); err != nil {
		t.Fatal(err)
	}
	if _, err := lstatIn(root, "keys/deploy.txt"); !os.IsNotExist(err) {
		t.Errorf("a file through a linked folder was found: %v", err)
	}
	if st, err := lstatIn(root, "keys"); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link itself: %v %v", st, err)
	}
	if st, err := lstatIn(root, "real/a.txt"); err != nil || !st.Mode().IsRegular() {
		t.Errorf("a file in a folder: %v %v", st, err)
	}
	for _, bad := range []string{"../x", "/etc/hosts"} {
		if _, err := lstatIn(root, bad); err == nil {
			t.Errorf("%q was found", bad)
		}
	}
}

// A tracked folder replaced in the work by a link to one outside the
// project: its files are removed, as git says, and nothing of the outside
// folder is hashed or copied.
func TestTheWorkIsNeverReadThroughALinkedFolder(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	writeT(t, filepath.Join(outside, "deploy.txt"), "outside\n")
	project := filepath.Join(root, "project")
	writeT(t, filepath.Join(project, "keys", "deploy.txt"), "tracked\n")
	gitT(t, project, "init", "-q")
	gitT(t, project, "add", "-A")
	gitT(t, project, "commit", "-qm", "keys")
	head := gitT(t, project, "rev-parse", "HEAD")[:40]
	if err := os.RemoveAll(filepath.Join(project, "keys")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(project, "keys")); err != nil {
		t.Fatal(err)
	}
	work, err := workHash(project)
	if err != nil || work == "" {
		t.Fatalf("workHash: %q %v", work, err)
	}
	writeT(t, filepath.Join(outside, "deploy.txt"), "outside, changed\n")
	if again, _ := workHash(project); again != work {
		t.Errorf("the hash read the outside file (%s, now %s)", work, again)
	}
	p, err := makeCheckout(project, head, work, filepath.Join(root, "copy"))
	if err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(p, "keys", "deploy.txt")); err == nil {
		t.Fatalf("the copy has keys/deploy.txt: %q", b)
	}
}

// A name git has is that name, newline and spaces and all.
func TestTheWorkIsListedByGitsOwnNames(t *testing.T) {
	project := t.TempDir()
	writeT(t, filepath.Join(project, "README"), "hi\n")
	gitT(t, project, "init", "-q")
	gitT(t, project, "add", "-A")
	gitT(t, project, "commit", "-qm", "i")
	writeT(t, filepath.Join(project, " spaced \n../x"), "y")
	_, untracked, err := uncommitted(project)
	if err != nil {
		t.Fatal(err)
	}
	if len(untracked) != 1 || untracked[0] != " spaced \n../x" {
		t.Fatalf("untracked = %q", untracked)
	}
}

// The project's own config names a clean filter; reading the work runs it
// never, and takes no lock on the index.
func TestReadingTheWorkRunsNoFilter(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	marker := filepath.Join(root, "ran")
	writeT(t, filepath.Join(project, "a.txt"), "a\n")
	writeT(t, filepath.Join(project, ".gitattributes"), "* filter=x\n")
	gitT(t, project, "init", "-q")
	gitT(t, project, "add", "-A")
	gitT(t, project, "commit", "-qm", "i")
	gitT(t, project, "config", "filter.x.clean", "touch '"+marker+"'; cat")
	gitT(t, project, "config", "filter.x.required", "true")
	writeT(t, filepath.Join(project, "a.txt"), "b\n")
	if _, err := workHash(project); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the repo's clean filter ran")
	}
	if !strings.Contains(strings.Join(gitCmd(project, "status").Env, " "), "GIT_OPTIONAL_LOCKS=0") {
		t.Error("git takes the index's lock")
	}
}

// The checkout a build runs in can have a link the commit has where the
// house's env/ has a file or a folder: env/ lands in the checkout.
func TestTheHouseEnvIsNeverWrittenThroughALink(t *testing.T) {
	root := t.TempDir()
	env, work, outside := filepath.Join(root, "env"), filepath.Join(root, "work"), filepath.Join(root, "outside")
	writeT(t, filepath.Join(env, ".env.local"), "SECRET=1\n")
	writeT(t, filepath.Join(env, "config", "app.json"), "{}\n")
	writeT(t, filepath.Join(outside, "rc"), "keep me\n")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, l := range [][2]string{{filepath.Join(outside, "rc"), ".env.local"}, {outside, "config"}} {
		if err := os.Symlink(l[0], filepath.Join(work, l[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := copyHouseEnv(env, work); err != nil {
		t.Fatal(err)
	}
	if got := readT(t, filepath.Join(outside, "rc")); got != "keep me\n" {
		t.Fatalf("the link's target was written: %q", got)
	}
	if _, err := os.Stat(filepath.Join(outside, "app.json")); err == nil {
		t.Fatal("env/config/app.json landed through a link")
	}
	if got := readT(t, filepath.Join(work, "config", "app.json")); got != "{}\n" {
		t.Fatalf("work/config/app.json = %q", got)
	}
	if got := readT(t, filepath.Join(work, ".env.local")); got != "SECRET=1\n" {
		t.Fatalf("work/.env.local = %q", got)
	}
}

// A desk's or a handout's file: inside the project with links followed,
// never the house's env/, never a secret by its path.
func TestARoomsFilesAreHeldToOneRule(t *testing.T) {
	project, outside := t.TempDir(), t.TempDir()
	writeT(t, filepath.Join(outside, "notes.md"), "private")
	writeT(t, filepath.Join(project, ".administration", "env", "local.properties"), "storePassword=x")
	writeT(t, filepath.Join(project, ".administration", "env", "config.json"), "{}")
	writeT(t, filepath.Join(project, ".aws", "credentials"), "x")
	writeT(t, filepath.Join(project, ".git", "config"), "x")
	writeT(t, filepath.Join(project, ".ENV"), "x")
	writeT(t, filepath.Join(project, "docs", "rules.md"), "rules")
	if err := os.Symlink(outside, filepath.Join(project, "private")); err != nil {
		t.Fatal(err)
	}
	adminDirs.Delete(project)
	for rel, want := range map[string]string{
		"private/notes.md":                     "through a link",
		".administration/env/config.json":      "env/",
		"env/config.json":                      "env/", // the house's, as the project has no env/
		".administration/env/local.properties": "secret",
		".aws/credentials":                     "secret",
		".git/config":                          "secret",
		".ENV":                                 "secret",
		"../x.md":                              "inside the project",
		"docs":                                 "not a regular file",
		"docs/missing.md":                      "no such file",
	} {
		if _, _, err := rosterFile(project, rel); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", rel, err, want)
		}
	}
	if real, _, err := rosterFile(project, "docs/rules.md"); err != nil || readT(t, real) != "rules" {
		t.Errorf("docs/rules.md: %q %v", real, err)
	}
}

func TestAHandoutHasNoTwoFilesOfOneName(t *testing.T) {
	project := t.TempDir()
	writeT(t, filepath.Join(project, "web", "README.md"), "web")
	writeT(t, filepath.Join(project, "api", "readme.md"), "api")
	writeT(t, filepath.Join(project, ".administration", "roster.toml"), "[handout]\nfiles = [\"web/README.md\", \"api/readme.md\"]\n")
	adminDirs.Delete(project)
	if _, err := handoutFiles(project); err == nil || !strings.Contains(err.Error(), "one name") {
		t.Fatalf("two files of one name went through: %v", err)
	}
}

// keepDesk checks the file again: one swapped for a link out since
// fillDesk checked it isn't kept.
func TestTheKeptDeskIsCheckedAgain(t *testing.T) {
	project, outside := t.TempDir(), t.TempDir()
	writeT(t, filepath.Join(outside, "config"), "private")
	writeT(t, filepath.Join(project, "fixtures", "receipt.pdf"), "%PDF-1.4")
	writeT(t, filepath.Join(project, ".administration", "roster.toml"), "[desk]\nfiles = [\"fixtures/receipt.pdf\"]\n")
	adminDirs.Delete(project)
	if _, err := fillDesk(project, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(project, "fixtures")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(outside, "f"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeT(t, filepath.Join(outside, "f", "receipt.pdf"), "private")
	if err := os.Symlink(filepath.Join(outside, "f"), filepath.Join(project, "fixtures")); err != nil {
		t.Fatal(err)
	}
	to := t.TempDir()
	if err := keepDesk(project, loadProjectConfig(project).Desk, "", to); err == nil {
		t.Fatalf("a desk file swapped for one outside was kept: %q", readT(t, filepath.Join(to, "receipt.pdf")))
	}
}

func TestAFileSwappedAfterItsCheckIsNotRead(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	writeT(t, a, "a")
	writeT(t, b, "b")
	st, _ := os.Lstat(a)
	if err := os.Rename(b, a); err != nil {
		t.Fatal(err)
	}
	if _, _, err := openRegular(a, st); err == nil {
		t.Error("another file at the checked path was opened")
	}
	if err := os.Symlink(a, filepath.Join(dir, "l")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := openRegular(filepath.Join(dir, "l"), nil); err == nil {
		t.Error("a link was opened")
	}
}
