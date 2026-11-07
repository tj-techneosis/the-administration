package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Rooms left behind. A room's processes are cleared by its teardown, by the
// tag the Administration gave them. A room whose teardown never ran (its
// setup run by hand, its folder deleted along with its cleanup, a machine
// that went down mid-contract) leaves servers running that nothing owns and
// nothing will ever stop: they hold memory until someone notices.
//
// sweepRooms finds them by what every room's processes carry in their
// environment, ADMINISTRATION_ROOM=<folder>, so a room the Administration
// never handed out counts too, and stops the ones whose room is gone. A
// process is taken as left behind only when all of these hold:
//
//   - it's this user's, and not this process or its parent;
//   - its ADMINISTRATION_ROOM names a folder that no longer exists;
//   - its own working folder is gone too (the kernel says "(deleted)"), so
//     a room being set up again in the same place is never touched;
//   - no open contract owns it: not by its contract, its tag, or its room.
//
// It needs to read a process's working folder, which only Linux gives
// (/proc); elsewhere it finds nothing.

// leftRoom is one room's processes left behind.
type leftRoom struct {
	Room string
	Pids []int
	RSS  int64 // their resident memory, in bytes, when found
}

func (r leftRoom) String() string {
	held := gb(r.RSS)
	if r.RSS < 1<<30 {
		held = fmt.Sprintf("%d MB", r.RSS>>20)
	}
	return fmt.Sprintf("%s: %s, %s", r.Room, plural(len(r.Pids), "process", "processes"), held)
}

// leftBehind is every room left behind, by room, in the order of its path.
func leftBehind() []leftRoom {
	if runtime.GOOS != "linux" {
		return nil
	}
	ids, rooms := openOwners()
	self, parent := os.Getpid(), os.Getppid()
	byRoom := map[string]*leftRoom{}
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == self || pid == parent {
			continue
		}
		room, ok := leftProcess(pid, ids, rooms)
		if !ok {
			continue
		}
		r := byRoom[room]
		if r == nil {
			r = &leftRoom{Room: room}
			byRoom[room] = r
		}
		r.Pids = append(r.Pids, pid)
		r.RSS += procRSS(pid)
	}
	var out []leftRoom
	for _, r := range byRoom {
		sort.Ints(r.Pids)
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Room < out[j].Room })
	return out
}

// leftProcess is the room pid was left behind in, if it was (the checks
// above), read fresh each time it's asked.
func leftProcess(pid int, openIDs map[string]bool, openRooms []string) (string, bool) {
	p := "/proc/" + strconv.Itoa(pid)
	if !ownProcess(p) || zombie(pid) {
		return "", false
	}
	env, err := os.ReadFile(p + "/environ")
	if err != nil {
		return "", false
	}
	var room, id, tag string
	for _, kv := range bytes.Split(env, []byte{0}) {
		k, v, _ := strings.Cut(string(kv), "=")
		switch k {
		case "ADMINISTRATION_ROOM":
			room = v
		case "ADMINISTRATION_CONTRACT":
			id = v
		case tagVar:
			tag = v
		}
	}
	// Only a room named in full, and never the top of the file system.
	if room == "" || !filepath.IsAbs(room) || filepath.Clean(room) != room || filepath.Dir(room) == room {
		return "", false
	}
	if openIDs[id] || tag != "" && openIDs[strings.SplitN(tag, "-", 2)[0]] {
		return "", false
	}
	for _, r := range openRooms {
		if room == r || strings.HasPrefix(room, r+"/") || strings.HasPrefix(r, room+"/") {
			return "", false
		}
	}
	if _, err := os.Lstat(room); !errors.Is(err, fs.ErrNotExist) {
		return "", false
	}
	cwd, err := os.Readlink(p + "/cwd")
	if err != nil || !strings.HasSuffix(cwd, " (deleted)") {
		return "", false
	}
	return room, true
}

// ownProcess is whether the process at /proc/<pid> runs as this user (its
// real and effective uid both).
func ownProcess(p string) bool {
	b, err := os.ReadFile(p + "/status")
	if err != nil {
		return false
	}
	uid := strconv.Itoa(os.Getuid())
	for _, l := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(l, "Uid:"); ok {
			f := strings.Fields(rest)
			return len(f) >= 2 && f[0] == uid && f[1] == uid
		}
	}
	return false
}

// procRSS is a process's resident memory, in bytes (0 when it can't say).
func procRSS(pid int) int64 {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/statm")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) < 2 {
		return 0
	}
	pages, _ := strconv.ParseInt(f[1], 10, 64)
	return pages * int64(os.Getpagesize())
}

// openOwners is what open contracts own: their ids, and their users' rooms
// (each room's folder and the folder it's in).
func openOwners() (map[string]bool, []string) {
	ids := map[string]bool{}
	var rooms []string
	books, err := booksDir()
	if err != nil {
		return ids, nil
	}
	files, _ := filepath.Glob(filepath.Join(books, "contracts", "*", "contract.json"))
	for _, f := range files {
		c, dir, err := loadContract(filepath.Base(filepath.Dir(f)))
		if err != nil || c.ClosedAt != nil {
			continue
		}
		ids[c.ID] = true
		for _, p := range c.Professionals {
			if s := readStatus(dir, p.Name); s.Room != "" {
				rooms = append(rooms, filepath.Clean(s.Room), filepath.Dir(filepath.Clean(s.Room)))
			}
		}
	}
	return ids, rooms
}

// sweepGrace is how long a room's processes have to go after SIGTERM before
// what stayed is killed.
const sweepGrace = 5 * time.Second

// sweepRooms stops the processes of every room left behind: SIGTERM, a grace
// period, then SIGKILL for whatever is still left behind. Each round looks
// again, with every check, rather than signalling the pids it first found: a
// pid can be reused meanwhile, and a process can start another (which
// carries the room too) after the first look. It says one line per room it
// cleared: its folder, how many processes, the memory they held.
func sweepRooms() []string {
	left := leftBehind()
	if len(left) == 0 {
		return nil
	}
	these := map[string]bool{}
	for _, r := range left {
		these[r.Room] = true
	}
	// What these rooms still run, by a fresh look.
	still := func() []int {
		var out []int
		for _, r := range leftBehind() {
			if these[r.Room] {
				out = append(out, r.Pids...)
			}
		}
		return out
	}
	termed := map[int]bool{}
	for end := time.Now().Add(sweepGrace); ; {
		pids := still()
		if len(pids) == 0 || !time.Now().Before(end) {
			break
		}
		for _, pid := range pids {
			if !termed[pid] {
				_ = syscall.Kill(pid, syscall.SIGTERM)
				termed[pid] = true
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	for range 10 {
		pids := still()
		if len(pids) == 0 {
			break
		}
		for _, pid := range pids {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		time.Sleep(100 * time.Millisecond)
	}
	var lines []string
	for _, r := range left {
		lines = append(lines, "stopped a room left behind, "+r.String())
	}
	return lines
}

// cmdSweep is sweep: the rooms left behind, stopped (or, with --dry-run,
// only listed), for a timer to run on a quiet machine between contracts.
func cmdSweep(args []string) error {
	f, err := parseFlags(args, map[string]bool{"dry-run": false})
	if err != nil {
		return err
	}
	if len(f.pos) > 0 {
		return usageError{"sweep takes no arguments"}
	}
	if runtime.GOOS != "linux" {
		fmt.Println("Rooms left behind are found on Linux only (by /proc); nothing to do here.")
		return nil
	}
	if f.has("dry-run") {
		left := leftBehind()
		if len(left) == 0 {
			fmt.Println("No rooms left behind.")
		}
		for _, r := range left {
			fmt.Println("Left behind: " + r.String())
		}
		return nil
	}
	lines := sweepRooms()
	if len(lines) == 0 {
		fmt.Println("No rooms left behind.")
	}
	for _, l := range lines {
		fmt.Println(strings.ToUpper(l[:1]) + l[1:] + ".")
	}
	return nil
}
