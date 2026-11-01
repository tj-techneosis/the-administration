package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEachAccountHasItsOwnRooms(t *testing.T) {
	// Two accounts on one Mac: the first one's folder would refuse the
	// second ("the rooms belong to someone else"), so each gets its own.
	if a, b := defaultRooms("darwin", 501), defaultRooms("darwin", 502); a == b || a != "/Users/Shared/administration-501" {
		t.Fatalf("macOS rooms: %q and %q", a, b)
	}
	if got := defaultRooms("linux", 1000); got != "/var/tmp/administration-1000" {
		t.Fatalf("linux rooms: %q", got)
	}
	// A room's tmp holds the browser's socket, whose path can't be long.
	if l := len(defaultRooms("darwin", 4294967294)) + len("/xxxx/r99/tmp"); l > maxRoomTmp {
		t.Fatalf("a macOS room's tmp is %d characters, over %d", l, maxRoomTmp)
	}
}

func TestALaterReleaseIsNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{{"v0.5.0", "v0.4.0", true}, {"v0.4.10", "v0.4.9", true}, {"v1.0.0", "v0.99.99", true}, {"v0.4.0", "v0.4.0", false}, {"v0.4.0", "v0.5.0", false},
		{"v0.5.0", "v0.5.0-rc1", true}, {"v0.5.0-rc2", "v0.5.0-rc1", true}, {"v0.5.0-rc1", "v0.5.0", false}, {"v0.5.0-rc1", "v0.4.9", true}} {
		if got := newerRelease(c.a, c.b); got != c.want {
			t.Errorf("newerRelease(%s, %s) = %v", c.a, c.b, got)
		}
	}
}

func TestCheckSaysWhatIsOut(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "latest", "download"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "latest", "download", "VERSION"), []byte("v0.5.0\n"), 0o644)
	t.Setenv("ADMINISTRATION_RELEASES", "file://"+dir+"/")
	was := version
	defer func() { version = was }()
	for v, want := range map[string]int{"v0.4.0": exitNewer, "v0.5.0-rc1": exitNewer, "v0.5.0": exitOK, "v0.6.0": exitOK, "dev": exitError} {
		version = v
		if got, _ := checkRelease(); got != want {
			t.Errorf("this %s, latest v0.5.0: exit %d, want %d", v, got, want)
		}
	}
}
