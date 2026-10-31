package main

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestValidTag(t *testing.T) {
	good := newTag("ab12", "newcomer")
	for _, tag := range []string{good, good + "/pro", good + "/setup"} {
		if !validTag(tag) {
			t.Errorf("validTag(%q) = false", tag)
		}
	}
	for _, tag := range []string{"", "/", "ab12", "ab12-newcomer", "ab12-newcomer-", "ab12-newcomer-123", "AB12-x-0123456789abcdef", "-x-0123456789abcdef"} {
		if validTag(tag) {
			t.Errorf("validTag(%q) = true", tag)
		}
	}
	if len(tagged("")) != 0 || len(tagged("/")) != 0 {
		t.Error("an invalid tag matched something")
	}
}

// Starts and signals real processes, so only inside tests/isolated.sh.
func TestClearTaggedOnlyTakesTheTag(t *testing.T) {
	if os.Getenv("ADMINISTRATION_TEST_ISOLATED") != "1" {
		t.Skip("run through tests/isolated.sh")
	}
	tag := newTag("ab12", "newcomer")
	other := newTag("ab12", "new")
	start := func(env []string, args ...string) *exec.Cmd {
		c := exec.Command(args[0], args[1:]...)
		c.Env = env
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		return c
	}
	pro := start(tagEnv(os.Environ(), tag+"/pro"), "sleep", "60")
	argvOnly := start(os.Environ(), "sh", "-c", "sleep 61", tagVar+"="+tag) // the tag in argv, not env
	neighbour := start(tagEnv(os.Environ(), other), "sleep", "62")       // a prefix of the name
	bystander := start(os.Environ(), "sleep", "63")
	defer func() {
		for _, c := range []*exec.Cmd{pro, argvOnly, neighbour, bystander} {
			_ = c.Process.Kill()
			_ = c.Wait()
		}
	}()
	time.Sleep(200 * time.Millisecond)
	got := tagged(tag)
	if _, ok := got[pro.Process.Pid]; !ok || len(got) != 1 {
		t.Fatalf("tagged(%q) = %v, want only %d", tag, got, pro.Process.Pid)
	}
	if !taggedAlive(pro.Process.Pid, tag) || taggedAlive(bystander.Process.Pid, tag) {
		t.Fatal("taggedAlive is wrong")
	}
	clearTagged(tag)
	done := make(chan error, 1)
	go func() { done <- pro.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the tagged process survived")
	}
	for _, c := range []*exec.Cmd{argvOnly, neighbour, bystander} {
		if c.ProcessState != nil || c.Process.Signal(syscall.Signal(0)) != nil {
			t.Errorf("%v was signalled", c.Args)
		}
	}
}

