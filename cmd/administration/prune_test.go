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
	if err := cmdPrune([]string{"--older", "1s"}); err != nil {
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
	err := cmdPrune([]string{"--keep", "0", "--older", "1h"})
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
	if err := cmdPrune([]string{"--keep", "0", "--older", "1h"}); err != nil {
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
