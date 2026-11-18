package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Kept rooms (docs/design.md, Kept rooms): one room, set up as a
// contract's is (the house's setup hook, its build hook's build of one
// commit, seed data), kept up with nobody in it, for people to look at the
// app at that commit. It's a contract with one seat and no professional: it
// waits in the same line for a room, and holds one, so it can never take a
// room from a contract; status shows it, and its build is the one every
// contract on that commit installs. Its time starts when it's up, and when
// it runs out the room is torn down. Closed or run out, it leaves nothing
// in the books.

// keptRoom is what makes a contract a kept room.
type keptRoom struct {
	// The commit as it was asked for (a ref, or a hash).
	Ref string `json:"ref"`
	// How long it's kept once it's up, in hours; room-extend adds to it.
	Hours float64 `json:"hours"`
	// The ports the house publishes the room on, beyond the room's own
	// (--publish): the placer's to choose, so a forwarder can serve the
	// room to other machines at an address people know.
	Publish []int `json:"publish,omitempty"`
}

// The most ports a kept room may publish, and the lowest one.
const (
	keptMaxPublish = 16
	keptMinPublish = 1025
)

// keptPublish reads --publish: ports and ranges, comma separated
// (8443-8447, or 8443,8445), each once, above the privileged ports.
func keptPublish(f *flags) ([]int, error) {
	if !f.has("publish") {
		return nil, nil
	}
	v := strings.TrimSpace(f.get("publish"))
	bad := func(why string) error {
		return usageError{fmt.Sprintf("--publish %q: %s", v, why)}
	}
	var out []int
	seen := map[int]bool{}
	for _, part := range strings.Split(v, ",") {
		lo, hi, isRange := strings.Cut(strings.TrimSpace(part), "-")
		if !isRange {
			hi = lo
		}
		a, err1 := strconv.Atoi(strings.TrimSpace(lo))
		b, err2 := strconv.Atoi(strings.TrimSpace(hi))
		if err1 != nil || err2 != nil || a > b {
			return nil, bad("ports and ranges, comma separated, such as 8443-8447 or 8443,8445")
		}
		if a < keptMinPublish || b > 65535 {
			return nil, bad(fmt.Sprintf("ports from %d to 65535", keptMinPublish))
		}
		if b-a+1+len(out) > keptMaxPublish {
			return nil, bad(fmt.Sprintf("at most %d ports", keptMaxPublish))
		}
		for p := a; p <= b; p++ {
			if seen[p] {
				return nil, bad(fmt.Sprintf("%d is named twice", p))
			}
			seen[p] = true
			out = append(out, p)
		}
	}
	return out, nil
}

// publishedBy is the kept room still standing (not ended) that publishes
// one of ports, and the port: two rooms can't be served on one.
func publishedBy(ports []int) (string, int) {
	books, err := booksDir()
	if err != nil {
		return "", 0
	}
	for _, c := range keptRooms() {
		if readStatus(filepath.Join(books, "contracts", c.ID), keptSeat).terminal() {
			continue
		}
		for _, p := range c.KeptRoom.Publish {
			if slices.Contains(ports, p) {
				return c.ID, p
			}
		}
	}
	return "", 0
}

func joinPorts(ps []int) string {
	var s []string
	for _, p := range ps {
		s = append(s, strconv.Itoa(p))
	}
	return strings.Join(s, ",")
}

// keptSeat is the one seat of a kept room.
const keptSeat = "room"

// The time a kept room is given when nobody says, and the most it can have
// left at once (a typo of hours shouldn't hold a room for a month).
const (
	keptDefaultHours = 8
	keptMaxHours     = 72
)

// keptHours reads --hours: a number of hours, more than none and at most
// the most a kept room can have left.
func keptHours(f *flags) (float64, error) {
	if !f.has("hours") {
		return keptDefaultHours, nil
	}
	h, err := strconv.ParseFloat(strings.TrimSpace(f.get("hours")), 64)
	if err != nil || h <= 0 || h != h {
		return 0, usageError{fmt.Sprintf("--hours %q: a number of hours, such as 8 or 1.5", f.get("hours"))}
	}
	if h > keptMaxHours {
		return 0, usageError{fmt.Sprintf("--hours %s: a kept room has at most %d hours left at once; extend it later with room-extend", f.get("hours"), keptMaxHours)}
	}
	return h, nil
}

func hoursSaid(h float64) string {
	return (time.Duration(h * float64(time.Hour))).Round(time.Second).String()
}

// cmdRoomKeep places a kept room on a commit and returns at once: its
// room is set up in the background, in line with the contracts' rooms.
func cmdRoomKeep(args []string) error {
	f, err := parseFlags(args, map[string]bool{"project": true, "hours": true, "data": true, "no-queue": false, "json": false, "house-from": true, "publish": true})
	if err != nil {
		return err
	}
	if len(f.pos) != 1 {
		return usageError{"room-keep REF [--project DIR] [--hours 8] [--data NAME] [--house-from REF] [--publish PORTS] [--no-queue] [--json]"}
	}
	ref := strings.TrimSpace(f.pos[0])
	if ref == "" || strings.HasPrefix(ref, "-") {
		return usageError{fmt.Sprintf("room-keep %q: give the commit to keep a room of (a hash, a branch or a tag)", ref)}
	}
	hours, err := keptHours(f)
	if err != nil {
		return err
	}
	publish, err := keptPublish(f)
	if err != nil {
		return err
	}
	// Until its contract.json names it and its worker has started, as place.
	unuse, err := useBooks()
	if err != nil {
		return err
	}
	defer unuse()
	office, err := officeName()
	if err != nil {
		return err
	}
	project, err := projectDir(f.get("project"))
	if err != nil {
		return err
	}
	houseSHA, houseKept := "", false
	if f.has("house-from") {
		dir, sha, name, done, err := houseFrom(project, strings.TrimSpace(f.get("house-from")), false)
		if err != nil {
			return err
		}
		defer done()
		pinHouse(project, dir)
		houseSHA, houseKept = sha, sha == ""
		switch {
		case houseKept:
			fmt.Fprintln(os.Stderr, "House: the one kept on this machine for this project.")
		case name == sha:
			fmt.Fprintf(os.Stderr, "House: from commit %s.\n", short(sha))
		default:
			fmt.Fprintf(os.Stderr, "House: from %s, at %s.\n", name, short(sha))
		}
	}
	// The house's hooks run as the person, outside every wall, and the
	// commit kept is often a branch nobody has reviewed: its own
	// .administration/ (or a worktree's main checkout's) is never the
	// house. It comes from a ref (--house-from), or is the one kept on this
	// machine for a checkout that has none of its own.
	key := projectKey(project)
	if !f.has("house-from") {
		h := houseOf(project)
		if h.Where != "machine" || lexists(filepath.Join(project, ".administration")) {
			return usageError{"a kept room runs the house's hooks as you, outside every wall, so its house can't come from the checkout, which the branch can write: give --house-from REMOTE/BRANCH (origin/main) or a full commit id you trust, or :kept for the house kept on this machine"}
		}
		fmt.Fprintf(os.Stderr, "administration: the house kept on this machine for %s (by the project's origin remote): %s\n", h.Origin, h.Dir)
		// Nor does the checkout's roster name the books it goes into.
		key = untrustedKey(project)
	}
	if _, _, err := houseSetup(project, false); err != nil {
		return usageError{"a kept room is set up as a contract's is, and " + err.Error()}
	}
	// REF is resolved as --house-from's is: a full commit id, refs/..., or
	// REMOTE/BRANCH, never a bare name git would look up across kinds of
	// ref (a tag named like a branch would shadow it).
	sha, _, err := resolveHouseRef(project, ref)
	if err != nil {
		if u, ok := err.(usageError); ok {
			return usageError{strings.Replace(u.msg, "--house-from ", "room-keep ", 1)}
		}
		return err
	}
	held := &contract{Project: project, Key: key, House: outsideHouse(project)}
	if err := recordCommit(held, sha); err != nil {
		if u, ok := err.(usageError); ok {
			return usageError{strings.Replace(u.msg, "--build ", "room-keep ", 1)}
		}
		return err
	}
	if held.Build == "" {
		return usageError{"a kept room is held to one commit, and the project isn't in git"}
	}
	if held.Tested == "" {
		return usageError{fmt.Sprintf("room-keep %s: this checkout doesn't have that commit; fetch it first (git fetch)", ref)}
	}
	if bad := buildHookUnrunnable(held); bad != "" {
		return usageError{bad}
	}
	if buildHook(held) == "" {
		// The setup builds the room from the checkout, so the checkout must
		// be that commit and nothing more.
		if held.Commit != held.Build {
			return usageError{fmt.Sprintf("room-keep %s: the house has no build hook, so the setup builds the room from the checkout, which is at %s: check out %s first, or give the house a build hook (docs/design.md, The build hook)",
				ref, short(held.Commit), short(held.Build))}
		}
		if w, _ := workHash(project); w != "" {
			return usageError{fmt.Sprintf("room-keep %s: the house has no build hook, so the setup builds the room from the checkout, which has uncommitted work: commit or stash it first", ref)}
		}
	}
	data, err := dataSet(project, f.get("data"), "")
	if err != nil {
		return err
	}
	if bad := badPorts(project); len(bad) > 0 {
		portWarning(bad)
	}

	// The books' lock until the worker has started, as place: two placed
	// at once can't both slip under the room limit.
	unlock, err := lockBooks()
	if err != nil {
		return err
	}
	defer func() { unlock() }()
	rooms, err := roomsDir()
	if err != nil {
		return err
	}
	if len(rooms)+len("/xxxx/r99/tmp") > maxRoomTmp {
		return usageError{fmt.Sprintf("the rooms folder's path (%s) is too long for the browser's socket; set ADMINISTRATION_ROOMS to a folder of at most %d characters", rooms, maxRoomTmp-len("/xxxx/r99/tmp"))}
	}
	// Under the books' lock, so two kept at once can't both take a port.
	if id, p := publishedBy(publish); id != "" {
		return usageError{fmt.Sprintf("--publish: kept room %s publishes %d; close it first (room-close %s), or publish on others", id, p, id)}
	}
	// Nor one anything else holds: another room, or anything of the machine's.
	if held := heldPorts(publish); len(held) > 0 {
		return usageError{fmt.Sprintf("--publish: already held on this machine: %s; publish on others", joinPorts(held))}
	}
	queued := ""
	if inUse, waiting, ids := roomUse(); inUse+waiting+1 > maxRooms() && len(ids) > 0 {
		line := fmt.Sprintf("%d of %d rooms are in use", inUse, maxRooms())
		if waiting > 0 {
			line += fmt.Sprintf(" and %s for one", plural(waiting, "user is waiting", "users are waiting"))
		}
		if f.has("no-queue") {
			return busy{fmt.Sprintf("%s (contract %s): keep it again once one frees.", line, holderNames(ids))}
		}
		queued = fmt.Sprintf("Queued, %d ahead: %s (contract %s). It takes a room by itself, in turn, as one frees.", len(ids), line, holderNames(ids))
	}
	books, err := booksDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(books, "contracts"), 0o700); err != nil {
		return err
	}
	id, err := newID(books)
	if err != nil {
		return err
	}
	dir := filepath.Join(books, "contracts", id)
	written := false
	defer func() {
		if !written {
			os.RemoveAll(dir)
		}
	}()
	c := &contract{ID: id, Target: "kept room at " + short(held.Build), Project: project, Key: held.Key, House: held.House,
		PlacedAt: time.Now().UTC().Truncate(time.Second), PlacedBy: placedBy(), Seq: time.Now().UnixNano(),
		Path: os.Getenv("PATH"), Version: version, HouseFrom: houseSHA, Data: data,
		Commit: held.Commit, Build: held.Build, Tested: held.Tested,
		Professionals: []castItem{{Name: keptSeat, Kind: "user", Source: "house"}},
		KeptRoom:      &keptRoom{Ref: ref, Hours: hours, Publish: publish}}
	if houseKept {
		c.HouseFrom = "kept"
	}
	c.stampWhere(office)
	if c.Commit != "" {
		c.Branch = branchOf(project)
	}
	c.Swept = sweepRooms()
	for _, l := range c.Swept {
		fmt.Fprintln(os.Stderr, "administration: "+l)
	}
	if err := os.MkdirAll(filepath.Join(dir, keptSeat), 0o700); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dir, "contract.json"), c); err != nil {
		return err
	}
	written = true
	startErr := startWorkers(c, dir)
	unlock()
	unlock = func() {}
	unuse()
	if f.has("json") {
		if err := printJSON(keptViewOf(c, dir)); err != nil {
			return err
		}
		return startErr
	}
	fmt.Printf("Kept room %s placed on %s, for %s once it's up.\n", id, c.Build, hoursSaid(hours))
	if queued != "" {
		fmt.Println(queued)
	}
	fmt.Printf("See it with: administration rooms (its address once it's up); close it with: administration room-close %s\n", id)
	return startErr
}

// holdKept keeps a kept room up until its time runs out (hours from ready,
// read again each round, so room-extend reaches it) or it's closed. expired
// says which.
func holdKept(ctx context.Context, c *contract, s *status, save func()) (state, reason string, expired bool) {
	ready := time.Now()
	if s.ReadyAt != nil {
		ready = *s.ReadyAt
	}
	hours := c.KeptRoom.Hours
	for {
		if kc, _, err := loadContract(c.ID); err == nil && kc.KeptRoom != nil {
			hours = kc.KeptRoom.Hours
		}
		until := ready.Add(time.Duration(hours * float64(time.Hour))).UTC().Truncate(time.Second)
		if s.KeptUntil == nil || !s.KeptUntil.Equal(until) {
			s.KeptUntil = &until
			save()
		}
		left := time.Until(until)
		if left <= 0 {
			return "closed", fmt.Sprintf("its time ran out (%s)", hoursSaid(hours)), true
		}
		select {
		case <-ctx.Done():
			return "closed", "closed", false
		case <-time.After(min(left, pollEvery())):
		}
	}
}

// keptRun is the worker's part for a kept room once it's up: hold it, tear
// it down, and, when its time ran out, leave nothing in the books.
func keptRun(ctx context.Context, c *contract, dir string, s *status, save func(), finish func(string, string)) {
	ready := time.Now().UTC().Truncate(time.Second)
	s.State, s.Reason, s.ReadyAt = "up", "", &ready
	save()
	state, reason, expired := holdKept(ctx, c, s, save)
	teardown(c, dir, keptSeat, s)
	finish(state, reason)
	if expired {
		// Under close.lock, so a room-close at the same moment does it once.
		if unlock, err := lockFile(filepath.Join(dir, "close.lock")); err == nil {
			removeKept(c, dir)
			unlock()
		}
	}
}

// removeKept takes a kept room out of the books and the rooms folder.
func removeKept(c *contract, dir string) {
	if rooms, err := roomsDir(); err == nil {
		_ = os.RemoveAll(filepath.Join(rooms, c.ID))
	}
	_ = os.RemoveAll(dir)
}

// closeKept stops a kept room's worker, which tears its room down, and
// takes it out of the books. It tears the room down itself when the worker
// is gone or doesn't stop. leftovers is what teardown found still running.
func closeKept(c *contract, dir string) (leftovers string, err error) {
	unlock, err := lockFile(filepath.Join(dir, "close.lock"))
	if err != nil {
		return "", err
	}
	defer unlock()
	if !isFile(filepath.Join(dir, "contract.json")) {
		return "", nil // closed meanwhile, or its time ran out
	}
	tag := readTag(dir, keptSeat)
	s := readStatus(dir, keptSeat)
	if !s.terminal() && taggedAlive(s.WorkerPID, tag) {
		_ = syscall.Kill(s.WorkerPID, syscall.SIGTERM)
		deadline := time.Now().Add(closeGrace() + teardownLimit + 15*time.Second)
		for time.Now().Before(deadline) {
			// Done once it says so: on its way out, it waits for this
			// close's lock.
			if s = readStatus(dir, keptSeat); s.terminal() || !taggedAlive(s.WorkerPID, tag) {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	s = readStatus(dir, keptSeat)
	clearTagged(tag)
	if s.Room != "" && !s.TornDown {
		teardown(c, dir, keptSeat, &s)
	}
	removeKept(c, dir)
	return s.Leftovers, nil
}

func cmdRoomClose(args []string) error {
	f, err := parseFlags(args, map[string]bool{})
	if err != nil {
		return err
	}
	if len(f.pos) != 1 {
		return usageError{"room-close ID"}
	}
	c, dir, err := loadKept(f.pos[0])
	if err != nil {
		return err
	}
	return closeKeptSaying(c, dir)
}

func closeKeptSaying(c *contract, dir string) error {
	left, err := closeKept(c, dir)
	if err != nil {
		return err
	}
	fmt.Printf("Kept room %s closed and torn down.\n", c.ID)
	if left != "" {
		fmt.Printf("After teardown: %s\n", left)
	}
	return nil
}

// loadKept is a kept room's contract; any other contract is refused.
func loadKept(id string) (*contract, string, error) {
	c, dir, err := openContract(id)
	if err != nil {
		if !strings.HasPrefix(err.Error(), "no contract ") {
			return nil, "", err
		}
		return nil, "", fmt.Errorf("no kept room %s (closed, or its time ran out)", id)
	}
	if c.KeptRoom == nil {
		return nil, "", usageError{fmt.Sprintf("%s is a contract, not a kept room: close it with administration close %s", id, id)}
	}
	return c, dir, nil
}

// notKept refuses a kept room to a command that works on contracts.
func notKept(c *contract, what string) error {
	if c.KeptRoom == nil {
		return nil
	}
	return usageError{fmt.Sprintf("%s is a kept room, not a contract: %s doesn't apply (administration rooms shows it; room-close %s closes it)", c.ID, what, c.ID)}
}

func cmdRoomExtend(args []string) error {
	f, err := parseFlags(args, map[string]bool{"hours": true, "json": false})
	if err != nil {
		return err
	}
	if len(f.pos) != 1 {
		return usageError{"room-extend ID [--hours 8] [--json]"}
	}
	more, err := keptHours(f)
	if err != nil {
		return err
	}
	if _, _, err := loadKept(f.pos[0]); err != nil {
		return err
	}
	// Under the books' lock: two extends at once both count.
	unlock, err := lockBooks()
	if err != nil {
		return err
	}
	defer unlock()
	c, dir, err := loadKept(f.pos[0])
	if err != nil {
		return err
	}
	s := readStatus(dir, keptSeat)
	if s.terminal() {
		return usageError{fmt.Sprintf("kept room %s is %s (%s): nothing to extend; close it and keep another", c.ID, s.State, s.Reason)}
	}
	hours := c.KeptRoom.Hours + more
	if s.ReadyAt != nil {
		left := hours - time.Since(*s.ReadyAt).Hours()
		if left > keptMaxHours {
			return usageError{fmt.Sprintf("that would leave it %s; a kept room has at most %d hours left at once", hoursSaid(left), keptMaxHours)}
		}
	} else if hours > keptMaxHours {
		return usageError{fmt.Sprintf("that would give it %s once it's up; a kept room has at most %d hours at once", hoursSaid(hours), keptMaxHours)}
	}
	c.KeptRoom.Hours = hours
	if err := writeJSON(filepath.Join(dir, "contract.json"), c); err != nil {
		return err
	}
	v := keptViewOf(c, dir)
	if f.has("json") {
		return printJSON(v)
	}
	if v.Until != nil {
		fmt.Printf("Kept room %s extended by %s: until %s (%s left).\n", c.ID, hoursSaid(more), v.Until.Local().Format("Mon 15:04"), (time.Duration(v.LeftSecs) * time.Second).String())
	} else {
		fmt.Printf("Kept room %s extended by %s: kept for %s once it's up.\n", c.ID, hoursSaid(more), hoursSaid(hours))
	}
	return nil
}

// keptView is a kept room as rooms --json says it, for a tool that puts
// the room's addresses in front of people.
type keptView struct {
	// No paths, and not who placed it (its Claude session): a tool shows
	// these to people.
	ID       string    `json:"id"`
	State    string    `json:"state"` // waiting, setup, up; failed or closed when its worker ended
	Reason   string    `json:"reason,omitempty"`
	Ref      string    `json:"ref"`
	Commit   string    `json:"commit"`
	Data     string    `json:"data,omitempty"`
	PlacedAt time.Time `json:"placed_at"`
	// Its time: hours once it's up, until when, and how long is left.
	Hours    float64    `json:"hours"`
	ReadyAt  *time.Time `json:"ready_at,omitempty"`
	Until    *time.Time `json:"until,omitempty"`
	LeftSecs int        `json:"left_secs,omitempty"`
	Waited   float64    `json:"waited_secs,omitempty"`
	// Where it answers: the app's address, the other addresses its pages
	// load, and the ports the room was given, by name.
	URL     string          `json:"url,omitempty"`
	Origins []string        `json:"origins,omitempty"`
	Ports   map[string]int  `json:"ports,omitempty"`
	Logins  map[string]role `json:"logins,omitempty"`
}

func keptViewOf(c *contract, dir string) keptView {
	pd := filepath.Join(dir, keptSeat)
	s := readStatus(dir, keptSeat)
	// A failed setup's reason names its log in the books: just the log's
	// name here (status ID says where).
	reason := strings.ReplaceAll(s.Reason, pd+string(filepath.Separator), "")
	v := keptView{ID: c.ID, State: s.State, Reason: reason, Ref: c.KeptRoom.Ref,
		Commit: c.Build, Data: c.Data, PlacedAt: c.PlacedAt, Hours: c.KeptRoom.Hours,
		ReadyAt: s.ReadyAt, Waited: s.WaitedSecs}
	if s.ReadyAt != nil {
		until := s.ReadyAt.Add(time.Duration(c.KeptRoom.Hours * float64(time.Hour))).UTC().Truncate(time.Second)
		v.Until = &until
		if !s.terminal() {
			v.LeftSecs = max(0, int(time.Until(until).Seconds()))
		}
	}
	if s.State == "up" {
		if m, err := readManifest(filepath.Join(pd, "room.json")); err == nil {
			v.URL, v.Origins, v.Logins = m.URL, m.Origins, m.Roles
		}
		if b, err := os.ReadFile(filepath.Join(pd, "ports")); err == nil {
			v.Ports = map[string]int{}
			for _, kv := range strings.Split(strings.TrimSpace(string(b)), ",") {
				if k, p, ok := strings.Cut(kv, "="); ok {
					if n, err := strconv.Atoi(p); err == nil {
						v.Ports[k] = n
					}
				}
			}
		}
	}
	return v
}

// keptLine is a kept room in status's words.
func keptLine(c *contract, dir string) string {
	v := keptViewOf(c, dir)
	v.Reason = readStatus(dir, keptSeat).Reason
	who, _, _ := strings.Cut(c.PlacedBy, " (claude:")
	head := fmt.Sprintf("Kept room %s at %s, placed %s by %s: ", c.ID, short(c.Build), c.PlacedAt.Local().Format("Mon 15:04"), who)
	switch v.State {
	case "up":
		return head + fmt.Sprintf("up at %s, %s left", v.URL, (time.Duration(v.LeftSecs)*time.Second).String())
	case "waiting", "queued":
		return head + orWord(v.Reason, "waiting for a room")
	case "setup":
		return head + orWord(v.Reason, "setting up")
	}
	return head + v.State + map[bool]string{true: ": " + v.Reason, false: ""}[v.Reason != ""]
}

func orWord(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// keptRooms is the kept rooms in the books, oldest first.
func keptRooms() []*contract {
	books, err := booksDir()
	if err != nil {
		return nil
	}
	files, _ := filepath.Glob(filepath.Join(books, "contracts", "*", "contract.json"))
	var out []*contract
	for _, f := range files {
		if c, _, err := loadContract(filepath.Base(filepath.Dir(f))); err == nil && c.KeptRoom != nil && c.ClosedAt == nil {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return contractOrder(out[i]) < contractOrder(out[j]) })
	return out
}

// expireKept clears the kept rooms whose worker has ended (it failed, or
// died) once their time is past: from when they were up, or ended, by
// their hours. A worker that runs clears its own room when its time runs
// out; this is for the ones that can't.
func expireKept() {
	for _, c := range keptRooms() {
		dir, err := contractDir(c.ID)
		if err != nil {
			continue
		}
		s := readStatus(dir, keptSeat)
		if !s.terminal() {
			continue
		}
		from := c.PlacedAt
		switch {
		case s.ReadyAt != nil:
			from = *s.ReadyAt
		case s.FinishedAt != nil:
			from = *s.FinishedAt
		}
		if time.Since(from) >= time.Duration(c.KeptRoom.Hours*float64(time.Hour)) {
			_, _ = closeKept(c, dir)
		}
	}
}

func cmdRooms(args []string) error {
	f, err := parseFlags(args, map[string]bool{"json": false})
	if err != nil {
		return err
	}
	if len(f.pos) != 0 {
		return usageError{"rooms [--json]"}
	}
	expireKept()
	views := []keptView{}
	for _, c := range keptRooms() {
		if dir, err := contractDir(c.ID); err == nil {
			views = append(views, keptViewOf(c, dir))
		}
	}
	if f.has("json") {
		return printJSON(views)
	}
	if len(views) == 0 {
		fmt.Printf("No kept rooms. %s\n", roomsLine())
		return nil
	}
	for _, c := range keptRooms() {
		if dir, err := contractDir(c.ID); err == nil {
			fmt.Println(keptLine(c, dir))
		}
	}
	fmt.Println(roomsLine())
	return nil
}

// lexists says whether anything is at path, a broken link included.
func lexists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
