package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAFoldRefOutsideTheContractIsNeverAPath(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "contracts", "abcd")
	other := filepath.Join(root, "contracts", "wxyz", "x")
	for _, d := range []string{filepath.Join(dir, "a"), other} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(other, "verdicts.json"), []byte(`[{"finding":3,"verdict":"likely","same_as":"x #1"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &contract{ID: "abcd", Professionals: []castItem{{Name: "a"}}}
	if clearSameAs(c, dir, "../wxyz/x #3") || setSameAs(c, dir, "../wxyz/x #3", "a #1", "likely", "") {
		t.Fatal("a ref outside the contract was taken")
	}
	if setSameAs(c, dir, "a #2", "../wxyz/x #1", "likely", "") {
		t.Fatal("a fold into another contract was taken")
	}
	if _, err := os.Stat(filepath.Join(other, "verdicts.lock")); err == nil {
		t.Error("a lock was made in another contract's folder")
	}
	b, _ := os.ReadFile(filepath.Join(other, "verdicts.json"))
	if string(b) != `[{"finding":3,"verdict":"likely","same_as":"x #1"}]` {
		t.Errorf("another contract's verdicts changed: %s", b)
	}
	// Its own: taken, and the check taken with the fold goes when it's parted.
	if !setSameAs(c, dir, "a #2", "a #1", "confirmed", "the same as a #1: x") || !clearSameAs(c, dir, "a #2") {
		t.Fatal("its own fold wasn't recorded and parted")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a", "verdicts.json")); strings.TrimSpace(string(b)) != "[]" {
		t.Errorf("parted, the taken check stayed: %s", b)
	}
}
