package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// Sharing a room (docs/design.md, The room plan). The users a contract's
// plan puts together (--rooms a+b) work in one room at once: the first to
// get there sets it up, and the others join it as soon as it answers, each
// with its own browser, desk and coins. A room setup is minutes; joining
// one is a moment, and a room's servers cost the machine far more than a
// browser does.
//
// The user whose room it is opens it for sharing once it's up: a record in
// the contract's .rooms folder (the room, its owner, its turn in the plan,
// who's in it, which logins are taken). A waiting user the plan puts with
// them joins under the books' lock: it takes a login the manifest hasn't
// given out (the role's own, then the manifest's "logins" for it; when
// they're all taken, users share one), and gets its own manifests, with
// that login, in its own folder. It works in a room folder of its own (its
// desk and its browser's profile) on the shared app, and owns none of the
// room's servers. A user leaving a room with others still in it only
// leaves; the last out hands it on to the plan's next turn (handon.go) or
// tears it down, with its owner's papers. A user waits for its turn-mates'
// room while one is being set up, rather than set up one of its own.

// sharedRoom is a room open for sharing, as its record says.
type sharedRoom struct {
	Owner   string            `json:"owner"`
	Room    string            `json:"room"`
	Group   int               `json:"group"`
	Stage   int               `json:"stage"`
	Members []string          `json:"members"`
	Logins  map[string][]int  `json:"logins,omitempty"` // role: the logins given out, by index
	Taken   map[string]int    `json:"taken,omitempty"`  // member: the login it has
	Role    map[string]string `json:"role,omitempty"`   // member: the role it signs in as
	// Everyone who has joined it, for its owner's record.
	Joined []string `json:"joined,omitempty"`
	// Its last user is out, and it's being handed on or torn down: nobody
	// joins it, and close tears it down if that never finished.
	Closing bool `json:"closing,omitempty"`
}

func sharesDir(dir string) string { return filepath.Join(dir, ".rooms") }

func loadShares(dir string) map[string]*sharedRoom {
	out := map[string]*sharedRoom{}
	files, _ := filepath.Glob(filepath.Join(sharesDir(dir), "*.json"))
	for _, f := range files {
		var r sharedRoom
		if b, err := os.ReadFile(f); err == nil && json.Unmarshal(b, &r) == nil && r.Owner != "" {
			out[r.Owner] = &r
		}
	}
	return out
}

func saveShare(dir string, r *sharedRoom) error {
	if err := os.MkdirAll(sharesDir(dir), 0o700); err != nil {
		return err
	}
	return writeJSON(filepath.Join(sharesDir(dir), r.Owner+".json"), r)
}

func dropShare(dir, owner string) {
	_ = os.Remove(filepath.Join(sharesDir(dir), owner+".json"))
}

// loginsFor is a role's logins in a manifest: its own, then the more the
// manifest gives for users sharing the room.
func loginsFor(m *manifest, roleName string) []role {
	if m == nil || roleName == "" || roleName == "any" {
		return nil
	}
	r, ok := m.Roles[roleName]
	if !ok {
		return nil
	}
	return append([]role{r}, m.Logins[roleName]...)
}

// offerRoom opens a user's room, now up, to the users the plan puts with
// it. It does nothing for a user the plan puts with nobody.
func offerRoom(c *contract, dir, name string, ci *castItem, s *status) {
	sp, ok := c.spot(name)
	if !ok || len(c.mates(name)) == 0 || !handsOn(ci) || s.Room == "" || s.SharedRoom != "" {
		return
	}
	unlock, err := lockBooks()
	if err != nil {
		return
	}
	defer unlock()
	r := &sharedRoom{Owner: name, Room: s.Room, Group: sp.group, Stage: sp.stage, Members: []string{name},
		Logins: map[string][]int{}, Taken: map[string]int{name: 0}, Role: map[string]string{name: ci.SignsInAs}}
	holdLogins(r, ci.SignsInAs, readManifestOr(filepath.Join(dir, name, "room.json")))
	_ = saveShare(dir, r)
}

// holdLogins marks the logins a member holds as given out: its role's own
// (index 0) or the one it picked; for "any", the first of every role.
func holdLogins(r *sharedRoom, roleName string, m *manifest) {
	if r.Logins == nil {
		r.Logins = map[string][]int{}
	}
	add := func(role string, i int) {
		if !slices.Contains(r.Logins[role], i) {
			r.Logins[role] = append(r.Logins[role], i)
		}
	}
	switch roleName {
	case "":
	case "any":
		if m != nil {
			for k := range m.Roles {
				add(k, 0)
			}
		}
	default:
		add(roleName, 0)
	}
}

func readManifestOr(path string) *manifest {
	m, _ := readManifest(path)
	return m
}

// joinRoom puts a waiting user in the room of its turn-mates, under the
// books' lock: its own manifests (with a login of its own when the manifest
// has one left) and its own room folder. It says whether it did.
func joinRoom(c *contract, dir, name string, ci *castItem) bool {
	sp, ok := c.spot(name)
	if !ok || len(c.mates(name)) == 0 || !handsOn(ci) {
		return false
	}
	unlock, err := lockBooks()
	if err != nil {
		return false
	}
	defer unlock()
	pd := filepath.Join(dir, name)
	shares := loadShares(dir)
	owners := make([]string, 0, len(shares))
	for o := range shares {
		owners = append(owners, o)
	}
	sort.Strings(owners)
	for _, o := range owners {
		r := shares[o]
		if r.Closing || r.Group != sp.group || r.Stage != sp.stage || slices.Contains(r.Members, name) || refused(pd, o) {
			continue
		}
		od := filepath.Join(dir, o)
		m, err := readManifest(filepath.Join(od, "room.json"))
		if err != nil {
			refuse(pd, o)
			continue
		}
		if ci.SignsInAs != "" && ci.SignsInAs != "any" {
			if _, ok := m.Roles[ci.SignsInAs]; !ok {
				continue
			}
		}
		// A login of its own, the first the room hasn't given out.
		pick := 0
		if logins := loginsFor(m, ci.SignsInAs); len(logins) > 1 {
			for i := range logins {
				if !slices.Contains(r.Logins[ci.SignsInAs], i) {
					pick = i
					break
				}
			}
		}
		write := func(src string) error {
			mm, err := readManifest(filepath.Join(od, src))
			if err != nil {
				return err
			}
			if ci.SignsInAs != "" && ci.SignsInAs != "any" {
				if _, ok := mm.Roles[ci.SignsInAs]; !ok {
					return fmt.Errorf("%s has no %s login", src, ci.SignsInAs)
				}
			}
			if logins := loginsFor(mm, ci.SignsInAs); pick > 0 && pick < len(logins) {
				mm.Roles[ci.SignsInAs] = logins[pick]
			} else if ci.SignsInAs == "any" || ci.SignsInAs != "" && slices.Contains(r.Logins[ci.SignsInAs], pick) {
				// Every login of its role is given out (or it takes all of
				// them): it shares one, and its brief says so.
				mm.SharedLogin = true
			}
			return writeJSON(filepath.Join(pd, src), mm)
		}
		if write("room.json") != nil || c.Before && write("room-before.json") != nil {
			refuse(pd, o)
			continue
		}
		if err := writeJSON(filepath.Join(pd, "share.taken"), map[string]string{"owner": o}); err != nil {
			refuse(pd, o)
			continue
		}
		r.Members = append(r.Members, name)
		r.Joined = append(r.Joined, name)
		if r.Taken == nil {
			r.Taken = map[string]int{}
		}
		if r.Role == nil {
			r.Role = map[string]string{}
		}
		if r.Logins == nil {
			r.Logins = map[string][]int{}
		}
		r.Taken[name], r.Role[name] = pick, ci.SignsInAs
		if ci.SignsInAs == "any" {
			holdLogins(r, "any", m)
		} else if ci.SignsInAs != "" && !slices.Contains(r.Logins[ci.SignsInAs], pick) {
			r.Logins[ci.SignsInAs] = append(r.Logins[ci.SignsInAs], pick)
		}
		if saveShare(dir, r) != nil {
			_ = os.Remove(filepath.Join(pd, "share.taken"))
			refuse(pd, o)
			continue
		}
		return true
	}
	return false
}

// refuse notes a room a user couldn't join (its papers wouldn't copy): it
// doesn't wait for that one again, and sets up its own if it must.
func refuse(pd, owner string) {
	if f, err := os.OpenFile(filepath.Join(pd, "share.refused"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		fmt.Fprintln(f, owner)
		f.Close()
	}
}

func refused(pd, owner string) bool {
	b, _ := os.ReadFile(filepath.Join(pd, "share.refused"))
	return slices.Contains(strings.Fields(string(b)), owner)
}

func readManifest(path string) (*manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("the room's manifest: %v", err)
	}
	return &m, nil
}

// joinedRoom makes the room a user joined its own to work in: a room folder
// of its own for its desk and browser, the shared app's address and builds.
// It owns none of the room's servers, so it has nothing to tear down.
func joinedRoom(c *contract, dir, name string, s *status, save func()) (m, before *manifest, err error) {
	pd := filepath.Join(dir, name)
	var t struct {
		Owner string `json:"owner"`
	}
	b, err := os.ReadFile(filepath.Join(pd, "share.taken"))
	if err != nil {
		return nil, nil, err
	}
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, nil, err
	}
	owner := seatStatus(dir, t.Owner)
	rooms, err := roomsDir()
	if err != nil {
		return nil, nil, err
	}
	room := roomPath(rooms, c, name)
	for _, d := range []string{"app", "desk", "tmp"} {
		if err := os.MkdirAll(filepath.Join(room, d), 0o700); err != nil {
			return nil, nil, err
		}
	}
	if m, err = readManifest(filepath.Join(pd, "room.json")); err != nil {
		return nil, nil, err
	}
	if c.Before {
		if before, err = readManifest(filepath.Join(pd, "room-before.json")); err != nil {
			return nil, nil, err
		}
	}
	s.Room, s.SharedRoom, s.SharedWith = room, owner.Room, t.Owner
	s.URL, s.BeforeURL = m.URL, owner.BeforeURL
	s.Builds, s.BuildArtifact, s.BuildCommit = owner.Builds, owner.BuildArtifact, owner.BuildCommit
	s.BeforeBuildArtifact, s.BeforeBuildCommit = owner.BeforeBuildArtifact, owner.BeforeBuildCommit
	// Nothing of the room's is its to tear down: the last out does that.
	s.Queued, s.TornDown = false, true
	save()
	return m, before, nil
}

// leaveRoom takes a user out of the room it shares, if it does. stay is
// true when there's nothing more for the user's worker to do with the room:
// others are still in it (its owner leaves it to them), or it's a joiner's,
// which the last out hands on to the plan's next turn or tears down with
// the owner's papers. When nobody else was ever in it, it's the owner's as
// before (stay false, nothing done). done says the user finished its part
// (back, or out of coins), so the room may be handed on.
func leaveRoom(ctx context.Context, c *contract, dir, name string, s *status, save func(), done bool) (stay bool) {
	// A joiner is known by its papers, share.taken, even one that failed
	// before it made the room its own.
	owner := name
	if s.SharedWith != "" {
		owner = s.SharedWith
	} else if b, err := os.ReadFile(filepath.Join(dir, name, "share.taken")); err == nil {
		var t struct {
			Owner string `json:"owner"`
		}
		if json.Unmarshal(b, &t) == nil && t.Owner != "" {
			owner = t.Owner
		}
	}
	joiner := owner != name
	unlock, err := lockBooks()
	if err != nil {
		// Never tear a room down not knowing who's in it: close does it.
		return true
	}
	r := loadShares(dir)[owner]
	if r == nil || r.Closing {
		unlock()
		return joiner
	}
	r.Members = slices.DeleteFunc(r.Members, func(n string) bool { return n == name })
	delete(r.Taken, name)
	delete(r.Role, name)
	// The logins given out are those the members still in it hold: a login
	// shared by two stays given out while either has it.
	r.Logins = map[string][]int{}
	om := readManifestOr(filepath.Join(dir, owner, "room.json"))
	for m, role := range r.Role {
		switch role {
		case "", "any":
			holdLogins(r, role, om)
		default:
			if !slices.Contains(r.Logins[role], r.Taken[m]) {
				r.Logins[role] = append(r.Logins[role], r.Taken[m])
			}
		}
	}
	if len(r.Members) > 0 {
		if !joiner {
			// The owner, leaving it to the others: said before the lock goes,
			// so the last out never finds it still holding the room.
			s.TornDown = true
			s.SharedOn = joinAnd(r.Members)
			save()
		}
		_ = saveShare(dir, r)
		unlock()
		return true
	}
	if !joiner {
		// The owner, last out: its own room, as ever.
		dropShare(dir, owner)
		unlock()
		if len(r.Joined) > 0 {
			s.Hosted = joinAnd(r.Joined)
			save()
		}
		return false
	}
	// The last out, in its owner's room. The record stays, closing, until
	// the room is handed on or torn down, so a worker that dies doing it
	// leaves it for close to tear down.
	r.Closing = true
	_ = saveShare(dir, r)
	unlock()
	ost := readStatus(dir, owner)
	ost.TornDown = false
	// The owner's status gets only what this changes: the rest is its
	// worker's, read again each time.
	osave := func() {
		updateStatus(dir, owner, func(cur *status) {
			cur.TornDown, cur.HandedTo, cur.Leftovers = ost.TornDown, ost.HandedTo, ost.Leftovers
		})
	}
	var oci *castItem
	for i := range c.Professionals {
		if c.Professionals[i].Name == owner {
			oci = &c.Professionals[i]
		}
	}
	m, _ := readManifest(filepath.Join(dir, owner, "room.json"))
	var bm *manifest
	if c.Before {
		bm, _ = readManifest(filepath.Join(dir, owner, "room-before.json"))
	}
	// A room with a before app whose manifest won't read can't have it put
	// back: torn down, not handed on half reset.
	if !done || c.Before && bm == nil || !handOn(ctx, c, dir, owner, oci, &ost, m, bm, osave) {
		teardown(c, dir, owner, &ost)
		osave()
	}
	dropShare(dir, owner)
	if ost.Leftovers != "" {
		s.Leftovers = ost.Leftovers
	}
	save()
	return true
}

// updateStatus changes a seat's status, read afresh, under its own lock:
// for a worker writing another seat's (the last out of a shared room, its
// owner's), which must not put back what the seat's own worker wrote since.
func updateStatus(dir, name string, change func(*status)) {
	unlock, err := lockFile(filepath.Join(dir, name, "status.lock"))
	if err != nil {
		return
	}
	defer unlock()
	cur := readStatus(dir, name)
	change(&cur)
	_ = writeJSON(filepath.Join(dir, name, "status.json"), cur)
}

// closeShares tears down any shared room still standing when its contract
// closes, its users' workers gone without the last of them doing it, and
// says so in its owner's status, so close doesn't tear it down again.
func closeShares(c *contract, dir string) {
	for owner := range loadShares(dir) {
		s := readStatus(dir, owner)
		// Handed on already (its last out died after): the taker's now.
		if s.HandedTo == "" {
			s.TornDown = false
			teardown(c, dir, owner, &s)
			updateStatus(dir, owner, func(cur *status) { cur.TornDown, cur.Leftovers = s.TornDown, s.Leftovers })
		}
		dropShare(dir, owner)
	}
}

// shareWait is why a user the plan puts with others waits for their room
// rather than set up its own: a room of its turn-mates is open, or one of
// them ahead of it in the plan is getting one. "" when none is.
func shareWait(c *contract, dir, name string, ci *castItem) string {
	mates := c.mates(name)
	if len(mates) == 0 || !handsOn(ci) {
		return ""
	}
	me, _ := c.spot(name)
	pd := filepath.Join(dir, name)
	shares := loadShares(dir)
	for _, r := range shares {
		if r.Closing || r.Group != me.group || r.Stage != me.stage || refused(pd, r.Owner) {
			continue
		}
		if ci.SignsInAs != "" && ci.SignsInAs != "any" {
			if m, err := readManifest(filepath.Join(dir, r.Owner, "room.json")); err != nil || m.Roles[ci.SignsInAs] == (role{}) {
				continue // a room without its login is no place for it
			}
		}
		return fmt.Sprintf("waiting to join %s's room", r.Owner)
	}
	for _, u := range mates {
		if sp, _ := c.spot(u); sp.order > me.order {
			continue
		}
		// A mate whose worker hasn't said anything yet, that's waiting in
		// line, or that's in a room it didn't open (one of its own that
		// never went up for sharing, or another's) isn't getting one to
		// share.
		if !isFile(filepath.Join(dir, u, "status.json")) || refused(pd, u) {
			continue
		}
		st := seatStatus(dir, u)
		if st.terminal() || st.Queued || st.SharedRoom != "" || st.Room != "" && shares[u] == nil && st.State == "working" {
			continue
		}
		// Ahead of it in the plan, and past the line for rooms: its room is
		// being set up, or handed on to it.
		return fmt.Sprintf("waiting to join %s's room, being set up", u)
	}
	return ""
}
