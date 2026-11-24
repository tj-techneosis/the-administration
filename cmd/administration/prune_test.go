package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func pruneBooks(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	books := filepath.Join(root, "books")
	t.Setenv("ADMINISTRATION_HOME", books)
	t.Setenv("ADMINISTRATION_ROOMS", filepath.Join(root, "rooms"))
	if err := os.MkdirAll(books, 0o700); err != nil {
		t.Fatal(err)
	}
	return books
}

func writeOld(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	then := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(path, then, then); err != nil {
		t.Fatal(err)
	}
}

// Prune is refused while a command holds the books' use lock (a place,
// close, verify, verdicts or resume), so nothing it removes is in use.
func TestPruneIsRefusedWhileTheBooksAreInUse(t *testing.T) {
	pruneBooks(t)
	unuse, err := useBooks()
	if err != nil {
		t.Fatal(err)
	}
	var b busy
	if err := cmdPrune(nil); !errors.As(err, &b) {
		t.Fatalf("prune while the books are in use: %v", err)
	}
	unuse()
	if err := cmdPrune(nil); err != nil {
		t.Fatal(err)
	}
}

func TestPruneRefusesANegativeAge(t *testing.T) {
	pruneBooks(t)
	var u usageError
	for _, age := range []string{"-5m", "-1d"} {
		if err := cmdPrune([]string{"--older", age}); !errors.As(err, &u) {
			t.Errorf("--older %s: %v", age, err)
		}
	}
}

// A build a room holds (shared, while it installs and runs it) isn't
// removed, however old; once it's let go, it is, whole, and nothing of
// it is left in the books.
func TestPruneLeavesABuildARoomHolds(t *testing.T) {
	books := pruneBooks(t)
	d := filepath.Join(books, "builds", "app-0123456789ab", "abc")
	writeOld(t, filepath.Join(d, "out", "app.apk"), "x")
	writeOld(t, filepath.Join(d, "artifact"), filepath.Join(d, "out", "app.apk")+"\n")
	release, err := lockWaiting(context.Background(), filepath.Join(d, ".lock"), syscall.LOCK_SH)
	if err != nil {
		t.Fatal(err)
	}
	if err := cmdPrune([]string{"--older", "1h"}); err != nil {
		t.Fatal(err)
	}
	if !isDir(filepath.Join(d, "out")) {
		t.Fatal("prune removed a build a room holds")
	}
	release()
	if err := cmdPrune([]string{"--older", "1h", "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	if !isDir(d) {
		t.Fatal("a dry run removed the build")
	}
	if err := cmdPrune([]string{"--older", "1h"}); err != nil {
		t.Fatal(err)
	}
	if isDir(d) {
		t.Fatal("prune left an unused old build")
	}
	if es, _ := os.ReadDir(filepath.Join(books, ".pruned")); len(es) > 0 {
		t.Fatalf("prune left %d things set aside", len(es))
	}
}

// A dry run looks at a build's lock without making one.
func TestPruneDryRunMakesNoLocks(t *testing.T) {
	books := pruneBooks(t)
	d := filepath.Join(books, "builds", "app-0123456789ab", "abc")
	writeOld(t, filepath.Join(d, "build.log"), "failed\n")
	if err := cmdPrune([]string{"--older", "1h", "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(d, ".lock")); err == nil {
		t.Fatal("a dry run made a lock file")
	}
}

// What prune removes it renames aside first: a copy is in its place whole
// or not at all, and a prune cut off part way leaves it set aside, for the
// next prune to clear, not half of it where makeCheckout takes it as made.
func TestAsideTakesACopyWholeAndTheNextPruneClearsIt(t *testing.T) {
	books := pruneBooks(t)
	d := filepath.Join(books, "copies", "app-0123456789ab", "abc")
	writeOld(t, filepath.Join(d, "a", "b.txt"), "x")
	moved, err := aside(books, d)
	if err != nil {
		t.Fatal(err)
	}
	if isDir(d) || !isDir(filepath.Join(moved, "a")) {
		t.Fatalf("not moved whole: %s", moved)
	}
	if err := cmdPrune(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(moved); err == nil {
		t.Fatal("the next prune left what was set aside")
	}
}

// A copy still being made (makeCheckout's .<name>-XXXX) for an open
// contract is that contract's, not a leftover.
func TestPruneLeavesACopyBeingMadeForAnOpenContract(t *testing.T) {
	books := pruneBooks(t)
	c := &contract{ID: "abcd", Project: "/p", Key: "/p", Commit: "abc", PlacedAt: time.Now()}
	root, _ := copyRoot(c, "abc", "")
	tmp := filepath.Join(filepath.Dir(root), ".abc-123456")
	writeOld(t, filepath.Join(tmp, "a.txt"), "x")
	cut := filepath.Join(filepath.Dir(root), ".def-123456")
	writeOld(t, filepath.Join(cut, "a.txt"), "x")
	then := time.Now().Add(-48 * time.Hour)
	for _, d := range []string{tmp, cut} {
		if err := os.Chtimes(d, then, then); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(books, "contracts", "abcd"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(books, "contracts", "abcd", "contract.json"), c); err != nil {
		t.Fatal(err)
	}
	if err := cmdPrune([]string{"--older", "1s", "--hold", holdFile(t, "")}); err != nil {
		t.Fatal(err)
	}
	if !isDir(tmp) {
		t.Fatal("prune removed a copy being made for an open contract")
	}
	if isDir(cut) {
		t.Fatal("prune left a copy cut off that no contract is on")
	}
}

// Rooms that wait for a build that fails take its failure; they don't
// each run the hook again. The next contract (asking after the failure)
// does.
func TestABuildThatFailedWhileARoomWaitedIsThatFailure(t *testing.T) {
	d := t.TempDir()
	asked := time.Now()
	if err := failedSince(d, "r1", asked); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "failed"), []byte("r1\nthe build of abc failed (exit status 1); see build.log\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := failedSince(d, "r1", asked); err == nil || !strings.Contains(err.Error(), "see build.log") {
		t.Fatalf("a waiter: %v", err)
	}
	if err := failedSince(d, "r2", asked); err != nil {
		t.Fatalf("made another way: %v", err)
	}
	if err := failedSince(d, "r1", time.Now().Add(time.Second)); err != nil {
		t.Fatalf("asked after it failed: %v", err)
	}
}

// A build's lock that can't be opened is an error at once, not a wait
// that looks like another build.
func TestLockWaitingSaysWhenItCantOpenTheLock(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root opens anything")
	}
	d := filepath.Join(t.TempDir(), "build")
	if err := os.MkdirAll(d, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(d, 0o700)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := lockWaiting(ctx, filepath.Join(d, ".lock"), syscall.LOCK_EX)
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want the open error", err)
	}
}

// newID makes the contract's folder, so two places at once can't share an
// id.
func TestNewIDReservesTheFolder(t *testing.T) {
	books := t.TempDir()
	if err := os.MkdirAll(filepath.Join(books, "contracts"), 0o700); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for range 200 {
		id, err := newID(books)
		if err != nil {
			t.Fatal(err)
		}
		if seen[id] {
			t.Fatalf("%s twice", id)
		}
		seen[id] = true
		if !isDir(filepath.Join(books, "contracts", id)) {
			t.Fatalf("%s's folder isn't made", id)
		}
	}
}

// closedContract writes a contract on project /p closed two days ago,
// with two professionals who each found something.
func closedContract(t *testing.T, books, id string) string {
	t.Helper()
	then := time.Now().Add(-48 * time.Hour).UTC().Truncate(time.Second)
	c := &contract{ID: id, Target: "t", Project: "/p", Key: "/p", PlacedAt: then, ClosedAt: &then,
		Professionals: []castItem{{Name: "a", Kind: "critic"}, {Name: "b", Kind: "critic"}}}
	dir := filepath.Join(books, "contracts", id)
	for _, p := range c.Professionals {
		if err := os.MkdirAll(filepath.Join(dir, p.Name), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := writeJSON(filepath.Join(dir, p.Name, "status.json"), status{State: "back", Findings: 3}); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeJSON(filepath.Join(dir, "contract.json"), c); err != nil {
		t.Fatal(err)
	}
	return dir
}

// ledgerOf is the project's ledger's lines, each checked to be whole.
func ledgerOf(t *testing.T, books string) []ledgerLine {
	t.Helper()
	b, err := os.ReadFile(projectLedger(books, "/p"))
	if err != nil {
		t.Fatal(err)
	}
	var out []ledgerLine
	for _, s := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if s == "" {
			continue
		}
		var l ledgerLine
		if err := json.Unmarshal([]byte(s), &l); err != nil {
			t.Fatalf("a ledger line that isn't whole: %q", s)
		}
		out = append(out, l)
	}
	return out
}

// A contract past the keep goes to the ledger, a line per professional
// with its numbers, then from the books, and nothing is left set aside.
func TestPruneMovesAContractPastTheKeepToTheLedger(t *testing.T) {
	books := pruneBooks(t)
	dir := closedContract(t, books, "aaaa")
	if err := cmdPrune([]string{"--keep", "0", "--older", "1h"}); err != nil {
		t.Fatal(err)
	}
	if isDir(dir) {
		t.Fatal("the contract is still in the books")
	}
	lines := ledgerOf(t, books)
	if len(lines) != 2 || lines[0].Findings != 3 || lines[1].Findings != 3 {
		t.Fatalf("ledger: %+v", lines)
	}
	if es, _ := os.ReadDir(filepath.Join(books, ".pruned")); len(es) > 0 {
		t.Fatalf("prune left %d things set aside", len(es))
	}
}

// A prune cut off after the ledger has some or all of a contract's lines
// (and one cut off in the middle of a line) leaves the contract in the
// books; the next one writes only the lines missing, so each professional
// is in the ledger once, with its numbers.
func TestAPruneCutOffNeitherLosesNorDoublesTheLedger(t *testing.T) {
	books := pruneBooks(t)
	dir := closedContract(t, books, "aaaa")
	c, _, err := loadContract("aaaa")
	if err != nil {
		t.Fatal(err)
	}
	one := *c
	one.Professionals = c.Professionals[:1]
	if err := appendLedger(books, "/p", &one, dir); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(projectLedger(books, "/p"), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"contract":"aaaa","na`) // a write cut off
	f.Close()
	if err := cmdPrune([]string{"--keep", "0", "--older", "1h"}); err != nil {
		t.Fatal(err)
	}
	if isDir(dir) {
		t.Fatal("the contract is still in the books")
	}
	b, _ := os.ReadFile(projectLedger(books, "/p"))
	var whole []ledgerLine
	for _, s := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var l ledgerLine
		if json.Unmarshal([]byte(s), &l) == nil {
			whole = append(whole, l)
		}
	}
	if len(whole) != 2 || whole[0].Name == whole[1].Name {
		t.Fatalf("ledger: %s", b)
	}
	lines, err := history("/p")
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 {
		t.Fatalf("history has %d lines, want 2", len(lines))
	}
}

// history counts a line the ledger has twice (from an older prune) once.
func TestHistoryCountsALedgerLineOnce(t *testing.T) {
	books := pruneBooks(t)
	dir := closedContract(t, books, "aaaa")
	c, _, _ := loadContract("aaaa")
	for range 2 {
		b, _ := json.Marshal(lineFor(c, dir, c.Professionals[0]))
		path := projectLedger(books, "/p")
		_ = os.MkdirAll(filepath.Dir(path), 0o700)
		f, _ := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		_, _ = f.Write(append(b, '\n'))
		f.Close()
	}
	lines, err := history("/p")
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 {
		t.Fatalf("history has %d lines, want 2 (a once, b from the books)", len(lines))
	}
}

// A prune that can't remove something carries on with the rest, says what
// it did, and then fails naming what it couldn't; the contract it couldn't
// remove is left whole, its numbers in the ledger once.
func TestAPruneThatFailsPartWayStillDoesTheRest(t *testing.T) {
	books := pruneBooks(t)
	dir := closedContract(t, books, "aaaa")
	cp := filepath.Join(books, "copies", "app-0123456789ab", "abc")
	writeOld(t, filepath.Join(cp, "a.txt"), "x")
	then := time.Now().Add(-48 * time.Hour)
	_ = os.Chtimes(cp, then, then)
	// Nothing can be set aside while .pruned is a file.
	if err := os.WriteFile(filepath.Join(books, ".pruned"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	hold := holdFile(t, "")
	err := cmdPrune([]string{"--keep", "0", "--older", "1h", "--hold", hold})
	if err == nil || !strings.Contains(err.Error(), "aaaa") || !strings.Contains(err.Error(), "abc") {
		t.Fatalf("got %v, want both failures named", err)
	}
	if !isDir(dir) || !isDir(cp) {
		t.Fatal("removed something it said it couldn't")
	}
	if n := len(ledgerOf(t, books)); n != 2 {
		t.Fatalf("ledger has %d lines, want 2", n)
	}
	_ = os.Remove(filepath.Join(books, ".pruned"))
	if err := cmdPrune([]string{"--keep", "0", "--older", "1h", "--hold", hold}); err != nil {
		t.Fatal(err)
	}
	if isDir(dir) || isDir(cp) {
		t.Fatal("the next prune left them")
	}
	if n := len(ledgerOf(t, books)); n != 2 {
		t.Fatalf("ledger has %d lines after the next prune, want 2", n)
	}
}

// A dry run takes no lock and makes nothing: it looks while the books are
// in use, and leaves books that aren't there yet not there.
func TestPruneDryRunIsReadOnly(t *testing.T) {
	books := pruneBooks(t)
	unuse, err := useBooks()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmdPrune([]string{"--dry-run"}); err != nil {
		t.Fatalf("a dry run while the books are in use: %v", err)
	}
	unuse()
	fresh := filepath.Join(filepath.Dir(books), "none")
	t.Setenv("ADMINISTRATION_HOME", fresh)
	if err := cmdPrune([]string{"--dry-run"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fresh); err == nil {
		t.Fatal("a dry run made the books")
	}
}

// An age in days too big for a duration is refused, not wrapped round to
// one that prunes everything.
func TestPruneRefusesAnAgeThatDoesntFit(t *testing.T) {
	pruneBooks(t)
	var u usageError
	for _, age := range []string{"99999999999d", "106752d"} {
		if err := cmdPrune([]string{"--older", age}); !errors.As(err, &u) {
			t.Errorf("--older %s: %v", age, err)
		}
	}
	if d, err := parseAge("106751d"); err != nil || d <= 0 {
		t.Errorf("106751d: %v %v", d, err)
	}
}

// While a prune holds the books, a command waits for it, then goes on;
// one that waits too long gives up as busy instead of hanging.
func TestUseBooksWaitsForAPruneThenGivesUp(t *testing.T) {
	books := pruneBooks(t)
	unlock, ok := tryLockFile(useLock(books))
	if !ok {
		t.Fatal("can't take the lock")
	}
	old := useWait
	defer func() { useWait = old }()
	useWait = 300 * time.Millisecond
	var b busy
	if _, err := useBooks(); !errors.As(err, &b) {
		t.Fatalf("held past the wait: %v", err)
	}
	useWait = 10 * time.Second
	got := make(chan error, 1)
	go func() {
		unuse, err := useBooks()
		if err == nil {
			unuse()
			unuse() // a second call does nothing
		}
		got <- err
	}()
	time.Sleep(200 * time.Millisecond)
	select {
	case err := <-got:
		t.Fatalf("didn't wait for the prune: %v", err)
	default:
	}
	unlock()
	select {
	case err := <-got:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("still waiting after the prune let go")
	}
}

// A close waits for one already running on the same contract, then finds
// it closed and only prints its summary's path: it doesn't stop, tear
// down and sum it up again.
func TestASecondCloseWaitsForTheFirst(t *testing.T) {
	books := pruneBooks(t)
	t.Setenv("ADMINISTRATION_MATCH", "off")
	c := &contract{ID: "cccc", Target: "t", Project: "/p", Key: "/p", PlacedAt: time.Now()}
	dir := filepath.Join(books, "contracts", "cccc")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(dir, "contract.json"), c); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockFile(filepath.Join(dir, "close.lock")) // the first close
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan error, 1)
	go func() { got <- cmdClose([]string{"cccc"}) }()
	time.Sleep(300 * time.Millisecond)
	select {
	case err := <-got:
		t.Fatalf("didn't wait for the first close: %v", err)
	default:
	}
	now := time.Now().UTC()
	c.ClosedAt = &now
	if err := writeJSON(filepath.Join(dir, "contract.json"), c); err != nil {
		t.Fatal(err)
	}
	unlock()
	if err := <-got; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "summary.md")); err == nil {
		t.Fatal("the second close summed the contract up again")
	}
}

// minedFile writes prune --mined's list.
func minedFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "mined")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// stripped is whether a contract's raw material is gone and its summary kept.
func stripped(t *testing.T, dir string) bool {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, "contract.json")); err != nil {
		t.Fatalf("%s lost its summary: %v", dir, err)
	}
	_, err := os.Stat(filepath.Join(dir, "a", "stream.jsonl"))
	return err != nil
}

func closedWithStream(t *testing.T, books, id string) string {
	t.Helper()
	dir := closedContract(t, books, id)
	writeOld(t, filepath.Join(dir, "a", "stream.jsonl"), "{}\n")
	return dir
}

// With --mined, only the old contracts FILE lists are stripped; an
// unlisted one is kept whole however old, and so is a listed one still
// younger than --older.
func TestPruneMinedStripsOnlyListedRuns(t *testing.T) {
	books := pruneBooks(t)
	listed := closedWithStream(t, books, "aaaa")
	unlisted := closedWithStream(t, books, "bbbb")
	if err := os.WriteFile(filepath.Join(unlisted, "reviewed"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	file := minedFile(t, "aaaa\n")
	if err := cmdPrune([]string{"--mined", file, "--older", "1h", "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	if stripped(t, listed) {
		t.Fatal("a dry run stripped a contract")
	}
	if err := cmdPrune([]string{"--mined", file, "--older", "30d"}); err != nil {
		t.Fatal(err)
	}
	if stripped(t, listed) {
		t.Fatal("a listed contract younger than --older was stripped")
	}
	if err := cmdPrune([]string{"--mined", file, "--older", "1h"}); err != nil {
		t.Fatal(err)
	}
	if !stripped(t, listed) {
		t.Fatal("a listed old contract wasn't stripped")
	}
	if stripped(t, unlisted) {
		t.Fatal("an unlisted contract (old and reviewed) was stripped")
	}
}

// An empty FILE, or one of nothing but junk, prunes nothing.
func TestPruneMinedEmptyFilePrunesNothing(t *testing.T) {
	books := pruneBooks(t)
	dir := closedWithStream(t, books, "aaaa")
	build := filepath.Join(books, "builds", "app-0123456789ab", "abc")
	writeOld(t, filepath.Join(build, "build.log"), "failed\n")
	for _, body := range []string{"", "\n\n", "not an id\n"} {
		if err := cmdPrune([]string{"--mined", minedFile(t, body), "--older", "1h"}); err != nil {
			t.Fatal(err)
		}
		if stripped(t, dir) || !isDir(build) {
			t.Fatalf("a --mined FILE of %q pruned something", body)
		}
	}
}

// A FILE that's missing is refused, and nothing is pruned.
func TestPruneMinedMissingFileRefuses(t *testing.T) {
	books := pruneBooks(t)
	dir := closedWithStream(t, books, "aaaa")
	err := cmdPrune([]string{"--mined", filepath.Join(t.TempDir(), "nope"), "--older", "1h"})
	if err == nil || !strings.Contains(err.Error(), "can't read") || strings.Contains(err.Error(), "\n") {
		t.Fatalf("a missing FILE: %v", err)
	}
	if stripped(t, dir) {
		t.Fatal("a refused prune stripped a contract")
	}
}

// Lines that aren't contract ids are ignored and counted; the ids among
// them still count.
func TestPruneMinedIgnoresAndCountsJunk(t *testing.T) {
	ids, ignored, err := readMined(minedFile(t, "aaaa\n  bbbb  \n\nAAAA\naaaaa\n# a note\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || !ids["aaaa"] || !ids["bbbb"] || ignored != 3 {
		t.Fatalf("ids %v, %d ignored", ids, ignored)
	}
	books := pruneBooks(t)
	dir := closedWithStream(t, books, "aaaa")
	if err := cmdPrune([]string{"--mined", minedFile(t, "junk\naaaa\n"), "--older", "1h"}); err != nil {
		t.Fatal(err)
	}
	if !stripped(t, dir) {
		t.Fatal("a listed contract among junk lines wasn't stripped")
	}
}

// Under --mined nothing is removed, past the keep or not: a listed old
// contract beyond --keep is stripped to its summary and stays.
func TestPruneMinedNeverRemoves(t *testing.T) {
	books := pruneBooks(t)
	a := closedWithStream(t, books, "aaaa")
	b := closedWithStream(t, books, "bbbb")
	file := minedFile(t, "aaaa\nbbbb\n")
	if err := cmdPrune([]string{"--mined", file, "--older", "1h", "--keep", "0"}); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{a, b} {
		if !isDir(dir) {
			t.Fatalf("%s was removed under --mined", dir)
		}
		if !stripped(t, dir) {
			t.Fatalf("%s wasn't stripped", dir)
		}
		if _, err := os.Stat(filepath.Join(dir, "contract.json")); err != nil {
			t.Fatalf("%s lost its summary: %v", dir, err)
		}
	}
}

// writeContract puts a contract on project /p, at commit sha, in the books:
// open, or closed two days ago.
func writeContract(t *testing.T, books, id, sha string, closed bool) *contract {
	t.Helper()
	c := &contract{ID: id, Target: "t", Project: "/p", Key: "/p", Commit: sha, PlacedAt: time.Now().Add(-49 * time.Hour)}
	if closed {
		then := time.Now().Add(-48 * time.Hour).UTC().Truncate(time.Second)
		c.ClosedAt = &then
	}
	dir := filepath.Join(books, "contracts", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(dir, "contract.json"), c); err != nil {
		t.Fatal(err)
	}
	return c
}

// oldCopy makes the shared copy of /p at sha, made two days ago.
func oldCopy(t *testing.T, c *contract, sha string) string {
	t.Helper()
	root, _ := copyRoot(c, sha, "")
	writeOld(t, filepath.Join(root, "a.txt"), "x")
	then := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(root, then, then); err != nil {
		t.Fatal(err)
	}
	return root
}

// holdFile is a hold list (prune --hold) with body, ended as the helper
// ends it.
func holdFile(t *testing.T, body string) string {
	t.Helper()
	return rawHoldFile(t, body+"# end\n")
}

// rawHoldFile is a hold list with exactly body.
func rawHoldFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hold")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// heldContract is a closed contract on branch of a worktree of shop-app,
// keyed shop-app, with its copies with and without uncommitted work.
func heldContract(t *testing.T, books, id, sha, branch string) (bare, work string) {
	t.Helper()
	c := writeContract(t, books, id, sha, true)
	c.Project, c.Key, c.Branch, c.Work = "/wt/"+branch, "shop-app", branch, "w-"+sha
	if err := writeJSON(filepath.Join(books, "contracts", id, "contract.json"), c); err != nil {
		t.Fatal(err)
	}
	writeOld(t, filepath.Join(books, "contracts", id, "reviewed"), "")
	bare = oldCopy(t, c, sha)
	work, _ = copyRoot(c, sha, c.Work)
	writeOld(t, filepath.Join(work, "a.txt"), "x")
	then := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(work, then, then); err != nil {
		t.Fatal(err)
	}
	return bare, work
}

// Close and verify leave a run's copy: only prune removes copies.
func TestCloseKeepsTheRunsCopy(t *testing.T) {
	books := pruneBooks(t)
	t.Setenv("ADMINISTRATION_MATCH", "off")
	c := writeContract(t, books, "aaaa", "abc", false)
	root := oldCopy(t, c, "abc")
	if err := cmdClose([]string{"aaaa"}); err != nil {
		t.Fatal(err)
	}
	if !isDir(root) {
		t.Fatal("close removed the closed run's copy")
	}
}

// Without --hold, prune removes no copy at all, a closed run's or a
// leftover; builds and transcripts go as ever.
func TestPruneWithoutAHoldListKeepsEveryCopy(t *testing.T) {
	books := pruneBooks(t)
	bare, work := heldContract(t, books, "aaaa", "abc", "dev-1-x")
	leftover := filepath.Join(books, "copies", projectFolder("shop-app"), "zzz")
	writeOld(t, filepath.Join(leftover, "a.txt"), "x")
	then := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(leftover, then, then); err != nil {
		t.Fatal(err)
	}
	b1 := madeBuild(t, books, "b1", 1*time.Hour)
	b2 := madeBuild(t, books, "b2", 2*time.Hour)
	b3 := madeBuild(t, books, "b3", 3*time.Hour)
	if err := cmdPrune([]string{"--older", "30d"}); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{bare, work, leftover} {
		if !isDir(d) {
			t.Fatalf("prune without --hold removed the copy %s", d)
		}
	}
	if !isDir(b1) || !isDir(b2) || isDir(b3) {
		t.Fatal("builds should go as ever without --hold")
	}
}

// With --hold, a closed run's copies (with and without its work) stay
// while its repo and branch are on the list, by the project's folder,
// the key or org/repo; the rest go. An open run's stay whatever the list.
func TestPruneHoldKeepsHeldCopiesAndRemovesTheRest(t *testing.T) {
	books := pruneBooks(t)
	heldBare, heldWork := heldContract(t, books, "aaaa", "abc", "dev-1-held")
	orgBare, orgWork := heldContract(t, books, "bbbb", "bcd", "dev-2-org")
	goneBare, goneWork := heldContract(t, books, "cccc", "cde", "dev-3-live")
	other, otherWork := heldContract(t, books, "dddd", "def", "dev-1-held")
	// The same branch name on another repo isn't held.
	oc, _, err := loadContract("dddd")
	if err != nil {
		t.Fatal(err)
	}
	oc.Key, oc.Project = "shop", "/wt/shop"
	if err := writeJSON(filepath.Join(books, "contracts", "dddd", "contract.json"), oc); err != nil {
		t.Fatal(err)
	}
	other, _ = copyRoot(oc, "def", "")
	otherWork, _ = copyRoot(oc, "def", oc.Work)
	for _, d := range []string{other, otherWork} {
		writeOld(t, filepath.Join(d, "a.txt"), "x")
		then := time.Now().Add(-48 * time.Hour)
		_ = os.Chtimes(d, then, then)
	}
	open := writeContract(t, books, "eeee", "efg", false)
	openCopy := oldCopy(t, open, "efg")
	hold := holdFile(t, "# not live yet\n\nshop-app dev-1-held\nacme/shop-app dev-2-org\n")
	if err := cmdPrune([]string{"--older", "30d", "--hold", hold, "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	if !isDir(goneBare) || !isDir(other) {
		t.Fatal("a dry run removed a copy")
	}
	if err := cmdPrune([]string{"--older", "30d", "--hold", hold}); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{heldBare, heldWork, orgBare, orgWork, openCopy} {
		if !isDir(d) {
			t.Errorf("prune removed a held or open copy %s", d)
		}
	}
	for _, d := range []string{goneBare, goneWork, other, otherWork} {
		if isDir(d) {
			t.Errorf("prune kept an unheld closed copy %s", d)
		}
	}
	// Taken off the list (live now), it goes on the next prune.
	if err := cmdPrune([]string{"--older", "30d", "--hold", holdFile(t, "")}); err != nil {
		t.Fatal(err)
	}
	if isDir(heldBare) || isDir(orgWork) || !isDir(openCopy) {
		t.Fatal("an empty hold list should let every closed copy go, and keep an open one's")
	}
}

// A hold list that can't be read, or has a line that isn't a repo and a
// branch, is refused before anything is touched.
func TestPruneHoldRefusesABrokenList(t *testing.T) {
	books := pruneBooks(t)
	bare, _ := heldContract(t, books, "aaaa", "abc", "dev-1-x")
	b3 := madeBuild(t, books, "b3", 3*time.Hour)
	madeBuild(t, books, "b1", 1*time.Hour)
	madeBuild(t, books, "b2", 2*time.Hour)
	for _, path := range []string{
		filepath.Join(t.TempDir(), "nope"),
		holdFile(t, "shop-app\n"),
		holdFile(t, "shop-app a notasha\n"),
		rawHoldFile(t, ""),                           // cut short before anything
		rawHoldFile(t, "shop-app dev-1-x\n"),        // cut short: no end
		rawHoldFile(t, "# end\nshop-app dev-1-x\n"), // goes on after its end
	} {
		err := cmdPrune([]string{"--older", "30d", "--hold", path})
		if err == nil || !strings.Contains(err.Error(), "--hold") || !strings.Contains(err.Error(), "pruned nothing") {
			t.Fatalf("%s: %v", path, err)
		}
	}
	if !isDir(bare) || !isDir(b3) {
		t.Fatal("a refused prune removed something")
	}
}

// placedContract is a closed contract on shop-app, closed ago, on branch
// at commit sha, with its copy of the commit.
func placedContract(t *testing.T, books, id, sha, branch string, ago time.Duration) string {
	t.Helper()
	c := writeContract(t, books, id, sha, true)
	then := time.Now().Add(-ago).UTC().Truncate(time.Second)
	c.Project, c.Key, c.Branch, c.ClosedAt = "/home/x/.worktrees/shop-app/"+id, "shop-app", branch, &then
	if err := writeJSON(filepath.Join(books, "contracts", id, "contract.json"), c); err != nil {
		t.Fatal(err)
	}
	writeOld(t, filepath.Join(books, "contracts", id, "reviewed"), "")
	return oldCopy(t, c, sha)
}

// A contract's recorded branch may not be the queue's: placed from an
// adm/ worktree, detached, or with an owner's prefix. Its commit places
// it; when nothing can, one on a held repo is held for 30 days, but one on
// the default branch isn't.
func TestPruneHoldPlacesContractsByCommitAndHoldsTheUnsure(t *testing.T) {
	books := pruneBooks(t)
	day := 24 * time.Hour
	keep := map[string]string{
		"adm/ branch, recent":  placedContract(t, books, "aaaa", "1111111aaaa", "adm/fix1", 2*day),
		"no branch, recent":    placedContract(t, books, "bbbb", "2222222bbbb", "", 2*day),
		"owner prefix":         placedContract(t, books, "cccc", "3333333cccc", "someone/dev-9-x", 40*day),
		"sha prefix, old adm/": placedContract(t, books, "dddd", "abcdef0123456789", "adm/fix2", 40*day),
		"merge sha, main":      placedContract(t, books, "eeee", "fedcba9876543210", "main", 40*day),
	}
	gone := map[string]string{
		"main, no commit listed": placedContract(t, books, "ffff", "4444444ffff", "main", 2*day),
		"adm/ older than 30d":    placedContract(t, books, "gggg", "5555555gggg", "adm/fix3", 31*day),
		"no branch, older":       placedContract(t, books, "hhhh", "6666666hhhh", "", 31*day),
		"another branch":         placedContract(t, books, "iiii", "7777777iiii", "dev-8-live", 2*day),
	}
	hold := holdFile(t, "acme/shop-app dev-9-x 9999999\n"+
		"acme/shop-app dev-10 abcdef0 FEDCBA9876543210\n")
	if err := cmdPrune([]string{"--older", "30d", "--hold", hold}); err != nil {
		t.Fatal(err)
	}
	for why, d := range keep {
		if !isDir(d) {
			t.Errorf("%s: prune removed a held copy", why)
		}
	}
	for why, d := range gone {
		if isDir(d) {
			t.Errorf("%s: prune kept an unheld copy", why)
		}
	}
	// None of it on a repo the list doesn't name.
	if (holdList{"shop": {{branch: "dev-9-x"}}}).holds(&contract{Key: "shop-app", Project: "/p/shop-app", Branch: "adm/x", ClosedAt: &time.Time{}}) {
		t.Fatal("held a contract on a repo the list doesn't name")
	}
}

func TestSameBranch(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{{"dev-1", "dev-1", true}, {"someone/dev-1", "dev-1", true}, {"dev-1", "owner/dev-1", true}, {"a/dev-1", "b/dev-1", true}, {"dev-1", "dev-2", false}, {"main", "dev-1", false}} {
		if got := sameBranch(c.a, c.b); got != c.want {
			t.Errorf("sameBranch(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

// A held contract past the keep is stripped, not removed: its hold must
// outlive this prune.
func TestPruneHoldKeepsAHeldContractPastTheKeep(t *testing.T) {
	books := pruneBooks(t)
	heldContract(t, books, "aaaa", "abc", "dev-1-held")
	heldContract(t, books, "bbbb", "bcd", "dev-2-live")
	if err := cmdPrune([]string{"--keep", "0", "--older", "1h", "--hold", holdFile(t, "shop-app dev-1-held\n")}); err != nil {
		t.Fatal(err)
	}
	if !isDir(filepath.Join(books, "contracts", "aaaa")) {
		t.Fatal("prune removed a held contract past the keep")
	}
	if isDir(filepath.Join(books, "contracts", "bbbb")) {
		t.Fatal("prune kept an unheld contract past the keep")
	}
}

// madeBuild makes a build of /p at sha with an artifact last used ago.
func madeBuild(t *testing.T, books, sha string, ago time.Duration) string {
	t.Helper()
	d := filepath.Join(books, "builds", projectFolder("/p"), sha)
	writeOld(t, filepath.Join(d, "out", "app.apk"), "x")
	writeOld(t, filepath.Join(d, "artifact"), filepath.Join(d, "out", "app.apk")+"\n")
	then := time.Now().Add(-ago)
	if err := os.Chtimes(filepath.Join(d, "artifact"), then, then); err != nil {
		t.Fatal(err)
	}
	return d
}

// A project keeps its last two builds used, and any an open run's rooms
// install, however recent the others; a dry run removes none of them.
func TestPruneKeepsTheLastTwoBuildsAndAnOpenRuns(t *testing.T) {
	books := pruneBooks(t)
	b1 := madeBuild(t, books, "b1", 1*time.Hour)
	b2 := madeBuild(t, books, "b2", 2*time.Hour)
	b3 := madeBuild(t, books, "b3", 3*time.Hour)
	b4 := madeBuild(t, books, "b4", 4*time.Hour)
	b5 := madeBuild(t, books, "b5", 5*time.Hour)
	writeContract(t, books, "aaaa", "b4", false)
	if err := cmdPrune([]string{"--older", "30d", "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{b1, b2, b3, b4, b5} {
		if !isDir(d) {
			t.Fatalf("a dry run removed %s", d)
		}
	}
	if err := cmdPrune([]string{"--older", "30d"}); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{b1, b2, b4} {
		if !isDir(d) {
			t.Errorf("prune removed %s", filepath.Base(d))
		}
	}
	for _, d := range []string{b3, b5} {
		if isDir(d) {
			t.Errorf("prune kept %s, past the last two", filepath.Base(d))
		}
	}
	if err := cmdPrune([]string{"--older", "30d", "--builds", "0"}); err != nil {
		t.Fatal(err)
	}
	if isDir(b1) || isDir(b2) || !isDir(b4) {
		t.Fatal("--builds 0 should leave only the open run's build")
	}
	var u usageError
	if err := cmdPrune([]string{"--builds", "-1"}); !errors.As(err, &u) {
		t.Fatalf("--builds -1: %v", err)
	}
}

// A build being made (no artifact yet) isn't one of the last used, and
// doesn't push a made one out.
func TestPruneDoesntCountABuildBeingMade(t *testing.T) {
	books := pruneBooks(t)
	b1 := madeBuild(t, books, "b1", 1*time.Hour)
	b2 := madeBuild(t, books, "b2", 2*time.Hour)
	making := filepath.Join(books, "builds", projectFolder("/p"), "b0")
	if err := os.MkdirAll(making, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(making, "build.log"), []byte("building\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmdPrune([]string{"--older", "30d"}); err != nil {
		t.Fatal(err)
	}
	if !isDir(b1) || !isDir(b2) || !isDir(making) {
		t.Fatal("prune removed a build it should keep")
	}
}

// A closed run not yet reviewed keeps its copy with the uncommitted work:
// once the work has changed it can't be made again, and its verify would
// judge the findings against a tree without them. Its copy of the commit
// alone goes. Once it's reviewed, the work copy goes too.
func TestPruneKeepsAnUnreviewedRunsWorkCopy(t *testing.T) {
	books := pruneBooks(t)
	c := writeContract(t, books, "aaaa", "abc", true)
	c.Work = "w1"
	if err := writeJSON(filepath.Join(books, "contracts", "aaaa", "contract.json"), c); err != nil {
		t.Fatal(err)
	}
	bare := oldCopy(t, c, "abc")
	work, err := copyRoot(c, "abc", "w1")
	if err != nil || work == bare {
		t.Fatalf("the work copy's root: %q, %v", work, err)
	}
	writeOld(t, filepath.Join(work, "a.txt"), "x")
	then := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(work, then, then); err != nil {
		t.Fatal(err)
	}
	if err := cmdPrune([]string{"--older", "30d", "--hold", holdFile(t, "")}); err != nil {
		t.Fatal(err)
	}
	if !isDir(work) {
		t.Fatal("prune removed an unreviewed run's copy with its work")
	}
	if isDir(bare) {
		t.Fatal("prune kept an unreviewed run's copy of the commit alone")
	}
	writeOld(t, filepath.Join(books, "contracts", "aaaa", "reviewed"), "")
	if err := cmdPrune([]string{"--older", "30d", "--hold", holdFile(t, "")}); err != nil {
		t.Fatal(err)
	}
	if isDir(work) {
		t.Fatal("prune kept a reviewed run's work copy")
	}
}
