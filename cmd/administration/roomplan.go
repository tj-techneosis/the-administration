package main

import (
	"fmt"
	"strings"
)

// The room plan (docs/design.md, The room plan): how a contract's browser
// users are laid out across rooms, decided when it's cast, because whether
// two users can share a room depends on what each does in this run, not on
// the house. place --rooms "a+b, c>d>e" says it:
//
//   - a+b: a and b together, in one room at once (share.go). For users that
//     don't collide: read-only work, a login each, different parts.
//   - c>d>e: c, then d, then e, in turns in one room, its data put back
//     between them by the house's reset hook (handon.go). For users that
//     use something up (a one-time gift, an invite): each gets it fresh.
//     It's a promise they can wait for each other, not an order to: a
//     machine with room to spare sets another room up for d rather than
//     keep it waiting, and a busy one hands c's room on.
//   - stages mix: a+b>c is a and b together, then c.
//
// A user the plan doesn't name works apart, in a room of its own, as ever:
// one that breaks the data, or has equipment of its own.

// roomPlan is a contract's plan: groups (rooms), each of stages (turns),
// each of users together.
type roomPlan [][][]string

// parseRooms reads --rooms.
func parseRooms(s string) (roomPlan, error) {
	var plan roomPlan
	for _, g := range strings.Split(s, ",") {
		if g = strings.TrimSpace(g); g == "" {
			continue
		}
		var stages [][]string
		for _, st := range strings.Split(g, ">") {
			var users []string
			for _, u := range strings.Split(st, "+") {
				u = strings.TrimSpace(u)
				if u == "" {
					return nil, fmt.Errorf("--rooms: %q has an empty name (a+b is together, a>b in turns, a comma between rooms)", g)
				}
				users = append(users, u)
			}
			stages = append(stages, users)
		}
		plan = append(plan, stages)
	}
	return plan, nil
}

// String is the plan as --rooms writes it.
func (p roomPlan) String() string {
	var gs []string
	for _, g := range p {
		var ss []string
		for _, st := range g {
			ss = append(ss, strings.Join(st, "+"))
		}
		gs = append(gs, strings.Join(ss, ">"))
	}
	return strings.Join(gs, ", ")
}

// Said is the plan in words, for the summary and status.
func (p roomPlan) Said() string {
	var out []string
	for _, g := range p {
		var ss []string
		for _, st := range g {
			ss = append(ss, joinAnd(st))
		}
		switch {
		case len(g) == 1 && len(g[0]) == 1:
			out = append(out, ss[0]+" in a room of its own")
		case len(g) == 1:
			out = append(out, ss[0]+" together")
		case allSingle(g):
			out = append(out, strings.Join(ss, ", then ")+", in turns")
		default:
			out = append(out, strings.Join(ss, ", then ")+", in turns, together where joined")
		}
	}
	return strings.Join(out, "; ")
}

func allSingle(g [][]string) bool {
	for _, st := range g {
		if len(st) > 1 {
			return false
		}
	}
	return true
}

func joinAnd(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

// roomSpot is where the plan puts a user: its room (group), turn (stage)
// and place in the plan's order.
type roomSpot struct{ group, stage, order int }

// spots is every planned user's place.
func (p roomPlan) spots() map[string]roomSpot {
	out := map[string]roomSpot{}
	n := 0
	for gi, g := range p {
		for si, st := range g {
			for _, u := range st {
				out[u] = roomSpot{gi, si, n}
				n++
			}
		}
	}
	return out
}

func (c *contract) plan() roomPlan {
	if c.Rooms == "" {
		return nil
	}
	p, _ := parseRooms(c.Rooms)
	return p
}

// spot is where the contract's plan puts a user; ok is false for one it
// doesn't name, who works apart.
func (c *contract) spot(name string) (roomSpot, bool) {
	s, ok := c.plan().spots()[name]
	return s, ok
}

// mates are the users the plan puts in the same room as name at once.
func (c *contract) mates(name string) []string {
	p := c.plan()
	s, ok := p.spots()[name]
	if !ok {
		return nil
	}
	var out []string
	for _, u := range p[s.group][s.stage] {
		if u != name {
			out = append(out, u)
		}
	}
	return out
}

// together says whether two users may be in one room at once.
func (c *contract) together(a, b string) bool {
	sp := c.plan().spots()
	x, ok1 := sp[a]
	y, ok2 := sp[b]
	return ok1 && ok2 && x.group == y.group && x.stage == y.stage
}

// checkRooms holds a plan to the cast: every name a browser user on it with
// no equipment of its own, named once. It says what a plan in turns can't
// do without a reset hook, for place to warn.
func checkRooms(plan roomPlan, cast []castItem, hasReset bool) (warn string, err error) {
	byName := map[string]castItem{}
	for _, ci := range cast {
		byName[ci.Name] = ci
	}
	seen := map[string]bool{}
	turns := false
	for _, g := range plan {
		if len(g) > 1 {
			turns = true
		}
		for _, st := range g {
			for _, u := range st {
				ci, ok := byName[u]
				switch {
				case !ok:
					return "", fmt.Errorf("--rooms: %s isn't on this contract's cast", u)
				case seen[u]:
					return "", fmt.Errorf("--rooms: %s is in the plan twice", u)
				case ci.Kind != "user":
					return "", fmt.Errorf("--rooms: %s is a critic, and critics have no room: leave it out", u)
				case !handsOn(&ci):
					return "", fmt.Errorf("--rooms: %s has equipment of its own (or no browser), so it works in a room of its own: leave it out", u)
				}
				seen[u] = true
			}
		}
	}
	if turns && !hasReset {
		warn = "the house has no reset hook, so the users planned in turns (a>b) each set up a room of their own; give it one (README, The reset hook) and their rooms are put back and handed on"
	}
	return warn, nil
}

// planRooms is the fewest rooms a contract's users need at once: one for
// each planned room, and one for each user apart.
func planRooms(plan roomPlan, users int) int {
	planned := 0
	for _, g := range plan {
		for _, st := range g {
			planned += len(st)
		}
	}
	return users - planned + len(plan)
}

