package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Every process the Administration starts for a professional (the worker,
// the setup hook and the servers it leaves running, claude, the browser)
// carries ADMINISTRATION_TAG=<contract>-<name>-<random> in its environment,
// and passes it on to its children. Cleanup signals only processes of this
// user whose environment holds that exact tag. Command lines, paths and
// recorded pids decide nothing: a pid can be reused, a command line can name
// anything, and an earlier version that matched on paths once signalled
// every process of the session.
const tagVar = "ADMINISTRATION_TAG"

func newTag(id, name string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return id + "-" + name + "-" + hex.EncodeToString(b)
}

// validTag is a tag the Administration could have made: never empty, never
// short, so it can only match its own processes.
func validTag(tag string) bool {
	tag, _, _ = strings.Cut(tag, "/")
	i := strings.LastIndex(tag, "-")
	return i > 0 && len(tag)-i-1 == 16 && idRE.MatchString(strings.SplitN(tag, "-", 2)[0])
}

// tagged lists the pids of this user's processes carrying the tag, and
// whether each is a zombie (which counts as gone). A tag also matches its
// sub-tags: "<base>" covers "<base>/setup" and "<base>/pro".
func tagged(tag string) map[int]bool {
	out := map[int]bool{}
	if !validTag(tag) {
		return out
	}
	want := tagVar + "=" + tag
	self := os.Getpid()
	if runtime.GOOS == "linux" {
		entries, _ := os.ReadDir("/proc")
		for _, e := range entries {
			pid, err := strconv.Atoi(e.Name())
			if err != nil || pid == self {
				continue
			}
			env, err := os.ReadFile("/proc/" + e.Name() + "/environ")
			if err != nil {
				continue
			}
			for _, kv := range bytes.Split(env, []byte{0}) {
				if string(kv) == want || strings.HasPrefix(string(kv), want+"/") {
					out[pid] = zombie(pid)
					break
				}
			}
		}
		return out
	}
	// macOS: each of this user's processes, by what the kernel kept of its
	// start (its environment is in there; `ps -E` loses it once a process
	// retitles itself).
	uid := strconv.Itoa(os.Getuid())
	for pid, line := range psLines("-axww", "-o", "pid=,stat=,uid=") {
		if pid == self || line.command != uid {
			continue
		}
		for _, kv := range procArgs(pid) {
			if string(kv) == want || bytes.HasPrefix(kv, []byte(want+"/")) {
				out[pid] = strings.HasPrefix(line.stat, "Z")
				break
			}
		}
	}
	return out
}

type psLine struct{ stat, command string }

func psLines(args ...string) map[int]psLine {
	out := map[int]psLine{}
	b, err := exec.Command("ps", args...).Output()
	if err != nil {
		return out
	}
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) < 3 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		// Keep the command exactly as printed after the stat column.
		rest := strings.TrimLeft(l, " ")
		rest = strings.TrimLeft(rest[len(f[0]):], " ")
		rest = strings.TrimLeft(rest[len(f[1]):], " ")
		out[pid] = psLine{stat: f[1], command: rest}
	}
	return out
}

func zombie(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return true
	}
	// pid (comm) S ...: the state follows the last ')'.
	if i := bytes.LastIndexByte(b, ')'); i > 0 && i+2 < len(b) {
		return b[i+2] == 'Z'
	}
	return false
}

// taggedAlive is whether pid is still a live process carrying the tag. A pid
// that has been reused by something else of the person's is not.
func taggedAlive(pid int, tag string) bool {
	if pid <= 0 || !validTag(tag) {
		return false
	}
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	z, ok := tagged(tag)[pid]
	return ok && !z
}

// clearTagged stops every live process carrying the tag except the caller:
// SIGTERM, a moment to go, then SIGKILL for whatever stayed.
func clearTagged(tag string, except ...int) {
	skip := map[int]bool{os.Getpid(): true}
	for _, p := range except {
		skip[p] = true
	}
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL} {
		hit := false
		for pid, z := range tagged(tag) {
			if skip[pid] || z {
				continue
			}
			_ = syscall.Kill(pid, sig)
			hit = true
		}
		if !hit {
			return
		}
		if sig == syscall.SIGTERM {
			time.Sleep(time.Second)
		}
	}
}

// tagEnv replaces (or adds) the tag in an environment.
func tagEnv(env []string, tag string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, tagVar+"=") {
			out = append(out, kv)
		}
	}
	return append(out, tagVar+"="+tag)
}

// listening is the room's ports that still answer, after teardown.
func listening(ports []int) []int {
	var open []int
	for _, p := range ports {
		c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p)), 300*time.Millisecond)
		if err == nil {
			c.Close()
			open = append(open, p)
		}
	}
	return open
}

// procStarted is when pid started, as ps prints it (to the second, in UTC
// and the C locale, so whoever asks gets the same text), or "" when ps
// can't say: with the pid, it names one process, as a pid is reused, but
// hardly within the second.
func procStarted(pid int) string {
	cmd := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid))
	cmd.Env = append(os.Environ(), "TZ=UTC", "LC_ALL=C")
	b, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.Join(strings.Fields(string(b)), " ")
}

// writeCheckPid says a room-check or equipment-check is still running: its
// pid and when it started, so sweepChecks can tell it from a later process
// that was given the same pid.
func writeCheckPid(dir string) {
	pid := os.Getpid()
	// Whole or not at all: an empty file is a check still starting.
	tmp := filepath.Join(dir, "check.pid.tmp")
	if os.WriteFile(tmp, []byte(fmt.Sprintf("%d %s\n", pid, procStarted(pid))), 0o600) == nil {
		_ = os.Rename(tmp, filepath.Join(dir, "check.pid"))
	}
}

// checkAlive is whether the check that wrote a check.pid still runs.
func checkAlive(b []byte) (alive, empty bool) {
	pidS, started, _ := strings.Cut(strings.TrimSpace(string(b)), " ")
	pid, _ := strconv.Atoi(pidS)
	if pid <= 0 {
		return false, true
	}
	if syscall.Kill(pid, 0) != nil {
		return false, false
	}
	// Recorded with its start (an older check wrote only the pid): a
	// different start is someone else's process. ps failing says nothing.
	if now := procStarted(pid); started != "" && now != "" && now != started {
		return false, false
	}
	return true, false
}
