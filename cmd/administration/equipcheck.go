package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// cmdEquipmentCheck starts one piece of equipment the way a contract would
// (in a fresh room, with the app's room first if it needs one), says what its
// setup printed, takes one picture through a screen, and clears it all away.
// It's room-check for equipment: a device that won't boot or a driver whose
// pictures are broken shows up here, not as a contract's lost purse.
func cmdEquipmentCheck(args []string) error {
	f, err := parseFlags(args, map[string]bool{"project": true, "shot": true})
	if err != nil {
		return err
	}
	if len(f.pos) != 1 {
		return usageError{"equipment-check NAME [--project DIR] [--shot FILE]"}
	}
	project, err := projectDir(f.get("project"))
	if err != nil {
		return err
	}
	eqName := f.pos[0]
	eq, err := checkEquipment(project, eqName)
	if err != nil {
		return err
	}
	if eq.NeedsApp {
		if _, _, err := houseSetup(project, false); err != nil {
			return usageError{fmt.Sprintf("%s reads the app's room (needs_app), and %v", eqName, err)}
		}
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
	name := "equipment-check"
	pd := filepath.Join(dir, name)
	if err := os.MkdirAll(pd, 0o700); err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	// Killed outright, it can't clean up; this says it's still running, and
	// sweepChecks clears it once it isn't.
	writeCheckPid(dir)
	c := &contract{ID: id, Target: "equipment check", Project: project, PlacedAt: time.Now().UTC(), PlacedBy: placedBy(),
		Professionals: []castItem{{Name: name, Kind: "user", Source: "house", Uses: []string{eqName}, NoBrowser: !eq.NeedsApp}}}
	ci := &c.Professionals[0]
	if err := writeJSON(filepath.Join(dir, "contract.json"), c); err != nil {
		return err
	}
	tag := newTag(id, name)
	if err := os.WriteFile(filepath.Join(pd, "tag"), []byte(tag+"\n"), 0o600); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	s := status{State: "setup"}
	save := func() { _ = writeJSON(filepath.Join(pd, "status.json"), s) }
	// Said after teardown, so "cleared away" is true when it's said.
	done := ""
	defer func() {
		teardown(c, dir, name, &s)
		if s.Room != "" {
			_ = os.RemoveAll(filepath.Dir(s.Room))
		}
		if s.Leftovers != "" {
			fmt.Printf("After teardown: %s\n", s.Leftovers)
		}
		if done != "" {
			fmt.Println(done)
		}
	}()
	interrupted := func() error {
		if ctx.Err() != nil {
			fmt.Println("Interrupted; clearing it away.")
			return usageError{"equipment check interrupted"}
		}
		return nil
	}
	logTail := func() {
		if t := tail(filepath.Join(pd, "equipment-"+eqName+".log"), 30); t != "" {
			fmt.Printf("\nThe end of its setup's output:\n%s\n", t)
		}
	}

	if eq.NeedsApp {
		fmt.Printf("It reads the app's room, so building that first (up to %s)...\n", setupTimeout())
		if _, err := buildRoom(ctx, c, ci, pd, &s, save); err != nil {
			if e := interrupted(); e != nil {
				return e
			}
			fmt.Printf("The app's room didn't come up: %v\n", err)
			var r refusal
			if errors.As(err, &r) {
				return r
			}
			return startError{"equipment check failed"}
		}
	} else if err := makeRoom(c, ci, pd, &s, save); err != nil {
		return err
	}
	fmt.Printf("Starting %s (up to %s)...\n", eqName, setupTimeout())
	t0 := time.Now()
	vars, err := startEquipment(ctx, c, ci, pd, tag, &s, save)
	if err != nil {
		if e := interrupted(); e != nil {
			return e
		}
		fmt.Printf("It didn't start after %s: %v\n", time.Since(t0).Round(time.Second), err)
		logTail()
		var r refusal
		if errors.As(err, &r) {
			return r
		}
		return startError{"equipment check failed"}
	}
	fmt.Printf("It started in %s.", time.Since(t0).Round(time.Second))
	var printed []string
	for k, v := range vars[eqName] {
		if k != "room" && k != "port" {
			printed = append(printed, k+"="+v)
		}
	}
	sort.Strings(printed)
	if len(printed) > 0 {
		fmt.Printf(" Its setup printed: %s.", strings.Join(printed, ", "))
	}
	fmt.Println()
	if eq.Server != "screen" {
		fmt.Printf("It's a %q server, not a screen, so there's no picture to take; a small first contract through it proves the rest.\n", eq.Server)
		if f.has("shot") {
			fmt.Println("(--shot is for a screen; nothing saved.)")
		}
		done = fmt.Sprintf("Cleared away. Users can work through %s.", eqName)
		return nil
	}
	servers, _, err := equipmentServers(c, ci, s.Room, vars, "")
	if err != nil {
		return err
	}
	for _, srv := range servers {
		m, _ := srv.(map[string]any)
		b, n, err := screenshotThrough(ctx, m)
		if err != nil {
			fmt.Printf("Its screen didn't give a picture: %v\n", err)
			return startError{"equipment check failed"}
		}
		out := f.get("shot")
		if out == "" {
			out = filepath.Join(books, "equipment-check-"+eqName+".png")
		}
		if err := os.WriteFile(out, b, 0o600); err != nil {
			return err
		}
		fmt.Printf("Its screen answered: %s Saved what a professional would see to %s.\n", n, out)
	}
	done = fmt.Sprintf("Cleared away. Users can work through %s.", eqName)
	return nil
}

// screenshotThrough starts a screen's server as a professional's would be
// started, asks it for one look, and returns the picture and its note.
func screenshotThrough(ctx context.Context, srv map[string]any) ([]byte, string, error) {
	cmdPath, _ := srv["command"].(string)
	args, _ := srv["args"].([]string)
	cmd := exec.CommandContext(ctx, cmdPath, args...)
	cmd.Env = os.Environ()
	if env, ok := srv["env"].(map[string]string); ok {
		for k, v := range env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	reqs := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"screen_take_screenshot","arguments":{}}}` + "\n"
	cmd.Stdin = strings.NewReader(reqs)
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		return nil, "", err
	}
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	for sc.Scan() {
		var r struct {
			ID     int `json:"id"`
			Result struct {
				IsError bool `json:"isError"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
					Data string `json:"data"`
				} `json:"content"`
			} `json:"result"`
		}
		if json.Unmarshal(sc.Bytes(), &r) != nil || r.ID != 2 {
			continue
		}
		var img []byte
		var note string
		for _, c := range r.Result.Content {
			switch c.Type {
			case "image":
				img, _ = base64.StdEncoding.DecodeString(c.Data)
			case "text":
				note = c.Text
			}
		}
		if r.Result.IsError || img == nil {
			return nil, "", fmt.Errorf("%s", note)
		}
		return img, note, nil
	}
	return nil, "", fmt.Errorf("the screen server gave no answer")
}
