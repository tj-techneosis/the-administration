package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Handing a room on (docs/design.md, The room plan). A room takes minutes
// to set up and seconds to put back: a user who's done hands its room to
// the next user the contract's plan puts in it (--rooms a>b) still waiting
// for one, once the house's reset hook has put its data back the way the
// setup left it, and that user starts at once instead of setting up a room
// of its own. A house without a reset hook, a user the plan puts in no
// room with others, or a room with nobody waiting for it, is torn down as
// before.
//
// The finished user's worker resets the room, then, under the books' lock,
// leaves the room's papers (its handoff.json, the room's manifests, ports
// and cleanup) with the first user of the contract still waiting for a room
// or a setup turn, and waits for it to take them. That user's worker looks
// for them while it waits; it takes them (handoff.json becomes
// handoff.taken, under the lock) only while it hasn't started a setup of its
// own. One not taken in time is taken back, and the room torn down.

// handoff is a room's papers, as one user leaves them for the next.
type handoff struct {
	From      string `json:"from"`
	Room      string `json:"room"`
	Ports     []int  `json:"ports,omitempty"`
	Groups    []int  `json:"groups,omitempty"`
	URL       string `json:"url,omitempty"`
	BeforeSrc string `json:"before_src,omitempty"`
	BeforeURL string `json:"before_url,omitempty"`
	// The setup tags of what runs in the room: the first user's and each
	// one's it passed through since, for teardown to clear.
	Tags                []string    `json:"tags,omitempty"`
	Builds              []roomBuild `json:"builds,omitempty"`
	BuildArtifact       string      `json:"build_artifact,omitempty"`
	BuildCommit         string      `json:"build_commit,omitempty"`
	BeforeBuildArtifact string      `json:"before_build_artifact,omitempty"`
	BeforeBuildCommit   string      `json:"before_build_commit,omitempty"`
	Resets              int         `json:"resets"`
}

// The room's papers that go with it: what the setup wrote and teardown
// reads. The cleanup files move (whoever holds them takes the stack down);
// the rest are copied, so the one who handed it on keeps its own record.
var (
	roomPapersMoved  = []string{"cleanup", "cleanup-before"}
	roomPapersCopied = []string{"room.json", "room-before.json", "ports", "ports-before"}
)

// resetHook is the house's reset hook, "" when it has none.
func resetHook(c *contract) string {
	hook := filepath.Join(contractHouse(c), "reset")
	if st, err := os.Stat(hook); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
		return hook
	}
	return ""
}

// handsOn says whether a user's room can be handed on, or taken: a browser
// user with no equipment (equipment is the user's own, and a device's state
// isn't the app's to reset).
func handsOn(ci *castItem) bool {
	return ci != nil && ci.Kind == "user" && !ci.NoBrowser && len(ci.Uses) == 0
}

// handTimeout is how long a room's papers wait to be taken.
func handTimeout() time.Duration {
	return max(10*time.Second, 4*pollEvery())
}

// nextTaker is the first user the contract's plan puts in the same room as
// name (handon.go, roomplan.go), in the plan's order, that could take it
// now: waiting for a room or a setup turn, with no room and no papers of its
// own yet, signing in as a role the room has. A user the plan doesn't put
// in a room with others hands its room to nobody.
func nextTaker(c *contract, dir, name string, m *manifest) string {
	plan := c.plan()
	me, ok := plan.spots()[name]
	if !ok {
		return ""
	}
	byName := map[string]*castItem{}
	for i := range c.Professionals {
		byName[c.Professionals[i].Name] = &c.Professionals[i]
	}
	var order []string
	for _, st := range plan[me.group] {
		order = append(order, st...)
	}
	for _, u := range order {
		ci := byName[u]
		if ci == nil || ci.Name == name || !handsOn(ci) {
			continue
		}
		pd := filepath.Join(dir, ci.Name)
		// Papers of its own already, in a room it joined, or setting one up.
		if isFile(filepath.Join(pd, "handoff.json")) || isFile(filepath.Join(pd, "handoff.taken")) || isFile(filepath.Join(pd, "share.taken")) || isFile(filepath.Join(pd, "setting-up")) {
			continue
		}
		s := seatStatus(dir, ci.Name)
		if s.State != "waiting" || s.Room != "" || s.terminal() {
			continue
		}
		if ci.SignsInAs != "" && m != nil {
			if _, ok := m.Roles[ci.SignsInAs]; !ok {
				continue
			}
		}
		return ci.Name
	}
	return ""
}

// handOn hands a finished user's room (name's: whose papers it has) to the
// next user the plan puts in it, waiting for one. It says whether it did:
// when it didn't, the room is still to be torn down.
func handOn(ctx context.Context, c *contract, dir, name string, ci *castItem, s *status, m, before *manifest, save func()) bool {
	if resetHook(c) == "" || !handsOn(ci) || s.Room == "" || s.TornDown || ctx.Err() != nil || m == nil {
		return false
	}
	if nextTaker(c, dir, name, m) == "" {
		return false
	}
	pd := filepath.Join(dir, name)
	s.Reason = "putting its room back for the next user"
	save()
	defer func() { s.Reason = ""; save() }()
	if err := resetRoom(ctx, c, dir, name, s, before != nil); err != nil {
		appendLog(filepath.Join(pd, "reset.log"), fmt.Sprintf("administration: the room isn't handed on: %v\n", err))
		return false
	}
	unlock, err := lockBooks()
	if err != nil {
		return false
	}
	taker := nextTaker(c, dir, name, m)
	if taker == "" {
		unlock()
		return false
	}
	td := filepath.Join(dir, taker)
	h := handoff{From: name, Room: s.Room, Ports: s.Ports, Groups: s.Groups, URL: s.URL,
		BeforeSrc: s.BeforeSrc, BeforeURL: s.BeforeURL, Builds: s.Builds,
		Tags:          append(append([]string{}, s.RoomTags...), readTag(dir, name)),
		BuildArtifact: s.BuildArtifact, BuildCommit: s.BuildCommit,
		BeforeBuildArtifact: s.BeforeBuildArtifact, BeforeBuildCommit: s.BeforeBuildCommit,
		Resets: s.Resets + 1}
	// Said before anything moves: a worker that dies while the papers are
	// on their way leaves this, and its teardown takes them back.
	if os.WriteFile(filepath.Join(pd, "papers.to"), []byte(taker+"\n"), 0o600) != nil {
		unlock()
		return false
	}
	for _, f := range roomPapersCopied {
		if b, err := os.ReadFile(filepath.Join(pd, f)); err == nil {
			_ = os.WriteFile(filepath.Join(td, f), b, 0o600)
		}
	}
	for _, f := range roomPapersMoved {
		_ = os.Rename(filepath.Join(pd, f), filepath.Join(td, f))
	}
	if err := writeJSON(filepath.Join(td, "handoff.json"), h); err != nil {
		takeBack(pd, td)
		unlock()
		return false
	}
	unlock()
	deadline := time.Now().Add(handTimeout())
	for {
		if !isFile(filepath.Join(td, "handoff.json")) {
			s.HandedTo, s.TornDown = taker, true
			save()
			_ = os.Remove(filepath.Join(pd, "papers.to"))
			return true
		}
		if time.Now().After(deadline) {
			unlock, err := lockBooks()
			if err != nil {
				return false
			}
			taken := !isFile(filepath.Join(td, "handoff.json"))
			if !taken {
				_ = os.Remove(filepath.Join(td, "handoff.json"))
				takeBack(pd, td)
				_ = os.Remove(filepath.Join(pd, "papers.to"))
			}
			unlock()
			if taken {
				continue
			}
			appendLog(filepath.Join(pd, "reset.log"), fmt.Sprintf("administration: %s didn't take the room in %s; it's torn down\n", taker, handTimeout()))
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// reclaimPapers settles a room whose papers were on their way to another
// user when its worker stopped (papers.to): taken, the room is the taker's
// now, and true says this one has nothing to tear down; not taken, they
// come back, for this one's teardown.
func reclaimPapers(dir, name string) (given bool) {
	pd := filepath.Join(dir, name)
	b, err := os.ReadFile(filepath.Join(pd, "papers.to"))
	if err != nil {
		return false
	}
	taker := strings.TrimSpace(string(b))
	if !nameRE.MatchString(taker) {
		_ = os.Remove(filepath.Join(pd, "papers.to"))
		return false
	}
	td := filepath.Join(dir, taker)
	unlock, err := lockBooks()
	if err != nil {
		return false
	}
	defer unlock()
	if isFile(filepath.Join(td, "handoff.taken")) && !isFile(filepath.Join(td, "handoff.json")) {
		_ = os.Remove(filepath.Join(pd, "papers.to"))
		return true
	}
	_ = os.Remove(filepath.Join(td, "handoff.json"))
	takeBack(pd, td)
	_ = os.Remove(filepath.Join(pd, "papers.to"))
	return false
}

// takeBack returns the room's papers from a taker that never took them.
func takeBack(pd, td string) {
	for _, f := range roomPapersMoved {
		_ = os.Rename(filepath.Join(td, f), filepath.Join(pd, f))
	}
	for _, f := range roomPapersCopied {
		_ = os.Remove(filepath.Join(td, f))
	}
}

// resetRoom runs the house's reset hook on the room's app (and its before
// app's), as the setup ran there, then clears what the last user left in
// the room: its desk, its browser's profile, what its equipment said.
func resetRoom(ctx context.Context, c *contract, dir, name string, s *status, before bool) error {
	pd := filepath.Join(dir, name)
	logf, err := os.OpenFile(filepath.Join(pd, "reset.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	sides := []struct{ app, src, ports, mf, side string }{
		{filepath.Join(s.Room, "app"), c.Project, "ports", "room.json", "after"},
	}
	if before {
		sides = append(sides, struct{ app, src, ports, mf, side string }{filepath.Join(s.Room, "before"), s.BeforeSrc, "ports-before", "room-before.json", "before"})
	}
	for _, sd := range sides {
		ports, _ := os.ReadFile(filepath.Join(pd, sd.ports))
		rctx, cancel := context.WithTimeout(ctx, setupTimeout())
		cmd := exec.Command(resetHook(c), sd.src, sd.app)
		cmd.Dir = sd.src
		if !isDir(sd.src) {
			// The project's folder gone (a worktree removed): where
			// teardown runs, its house's checkout.
			cmd.Dir = hookDir(c)
		}
		env := roomEnv(c, name, sd.app, strings.TrimSpace(string(ports)), filepath.Join(pd, sd.mf), readTag(dir, name))
		if sd.side == "before" {
			env = append(beforeEnv(env, sd.src), "ADMINISTRATION_SIDE=before")
		}
		cmd.Env = env
		cmd.Stdout, cmd.Stderr = logf, logf
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		fmt.Fprintf(logf, "administration: reset %s (%s)\n", sd.app, time.Now().UTC().Format(time.RFC3339))
		if err := cmd.Start(); err != nil {
			cancel()
			return fmt.Errorf("reset: %v", err)
		}
		err := waitGroup(rctx, cmd)
		timedOut := rctx.Err() == context.DeadlineExceeded
		cancel()
		if err != nil {
			if timedOut {
				return fmt.Errorf("the reset hook took longer than %s", setupTimeout())
			}
			return fmt.Errorf("the reset hook failed (%v); see %s", err, filepath.Join(pd, "reset.log"))
		}
	}
	for _, d := range []string{"desk", "tmp"} {
		p := filepath.Join(s.Room, d)
		if err := os.RemoveAll(p); err != nil {
			return err
		}
		if err := os.MkdirAll(p, 0o700); err != nil {
			return err
		}
	}
	for _, f := range []string{"desk-files.json", "equipment-lost", "equipment-lost-restarted"} {
		_ = os.Remove(filepath.Join(s.Room, f))
	}
	// The room must still answer: a reset that took the app down hands on
	// nothing.
	if s.URL != "" {
		if err := answers(ctx, s.URL, 60*time.Second); err != nil {
			return fmt.Errorf("the room's app doesn't answer after the reset: %v", err)
		}
	}
	return nil
}

func appendLog(path, line string) {
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		f.WriteString(line)
		f.Close()
	}
}

// handWatch is a waiting user's lookout for a room handed on to it: while
// it's open (waiting for a room, or for its setup turn), papers left for it
// are taken and the wait is cut short.
type handWatch struct {
	c         *contract
	ci        *castItem
	shared    bool // the room taken is shared, not handed on
	dir, name string
	mu        sync.Mutex
	open      bool
	taken     bool
	cancel    context.CancelFunc
	stop      chan struct{}
}

// watchHands starts a waiting user's lookout. Its context is the one to
// wait with: it ends when a room is taken.
func watchHands(ctx context.Context, c *contract, ci *castItem, dir, name string) (*handWatch, context.Context) {
	wctx, cancel := context.WithCancel(ctx)
	h := &handWatch{c: c, ci: ci, dir: dir, name: name, cancel: cancel, stop: make(chan struct{})}
	go func() {
		for {
			select {
			case <-h.stop:
				return
			case <-wctx.Done():
				return
			case <-time.After(250 * time.Millisecond):
			}
			h.look()
		}
	}()
	return h, wctx
}

func (h *handWatch) look() {
	h.mu.Lock()
	defer h.mu.Unlock()
	pd := filepath.Join(h.dir, h.name)
	if !h.open || h.taken {
		return
	}
	if !isFile(filepath.Join(pd, "handoff.json")) {
		// A place in a room of the contract's, shared.
		if joinRoom(h.c, h.dir, h.name, h.ci) {
			h.taken, h.shared = true, true
			h.cancel()
		}
		return
	}
	unlock, err := lockBooks()
	if err != nil {
		return
	}
	defer unlock()
	if os.Rename(filepath.Join(pd, "handoff.json"), filepath.Join(pd, "handoff.taken")) == nil {
		h.taken = true
		h.cancel()
	}
}

// setOpen opens or closes the lookout. Closing it says whether a room was
// taken meanwhile: if so, the user has it and mustn't set up its own.
func (h *handWatch) setOpen(open bool) (taken bool) {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.open = open && !h.taken
	return h.taken
}

func (h *handWatch) end() {
	if h != nil {
		close(h.stop)
	}
}

// commitRoom is asked as a room's setup is about to start, once its turn
// has come: false means a room was handed on meanwhile, and the setup is
// called off. Room checks set up their own rooms always.
var commitRoom = func() bool { return true }

// errHanded is a setup called off because a room was handed on.
var errHanded = fmt.Errorf("a room was handed on")

// takeRoom makes the room in a user's papers its own: the room, its
// servers, its build and its manifests.
func takeRoom(c *contract, dir, name string, s *status, save func()) (m, before *manifest, err error) {
	pd := filepath.Join(dir, name)
	b, err := os.ReadFile(filepath.Join(pd, "handoff.taken"))
	if err != nil {
		return nil, nil, err
	}
	var h handoff
	if err := json.Unmarshal(b, &h); err != nil {
		return nil, nil, err
	}
	s.Room, s.Ports, s.URL = h.Room, h.Ports, h.URL
	s.Groups = append(s.Groups, h.Groups...)
	s.BeforeSrc, s.BeforeURL = h.BeforeSrc, h.BeforeURL
	s.Builds, s.RoomTags, s.HandedFrom, s.Resets = h.Builds, h.Tags, h.From, h.Resets
	s.BuildArtifact, s.BuildCommit = h.BuildArtifact, h.BuildCommit
	s.BeforeBuildArtifact, s.BeforeBuildCommit = h.BeforeBuildArtifact, h.BeforeBuildCommit
	s.Queued = false
	save()
	read := func(f string) (*manifest, error) {
		b, err := os.ReadFile(filepath.Join(pd, f))
		if err != nil {
			return nil, err
		}
		var m manifest
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, fmt.Errorf("the room's manifest: %v", err)
		}
		return &m, nil
	}
	if m, err = read("room.json"); err != nil {
		return nil, nil, err
	}
	if c.Before {
		if before, err = read("room-before.json"); err != nil {
			return nil, nil, err
		}
	}
	return m, before, nil
}
