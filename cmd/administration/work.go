package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// startError is a room or a professional that failed to start; it exits 2.
type startError struct{ msg string }

func (e startError) Error() string { return e.msg }

// The room's manifest, written by .administration/setup to $ADMINISTRATION_MANIFEST.
type manifest struct {
	URL     string          `json:"url"`
	Origins []string        `json:"origins"`
	Data    string          `json:"data"`
	Roles   map[string]role `json:"roles"`
}

// A role's test login: a username or an email, and its local password.
type role struct {
	Login    string `json:"login"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (r role) id() string {
	if r.Login != "" {
		return r.Login
	}
	return r.Email
}

// One worker per professional: build the room (users only), run the
// professional to the end of their coins, keep their report, clear the room.
func work(id, name string) error {
	c, dir, err := loadContract(id)
	if err != nil {
		return err
	}
	var ci *castItem
	for i := range c.Professionals {
		if c.Professionals[i].Name == name {
			ci = &c.Professionals[i]
		}
	}
	if ci == nil {
		return fmt.Errorf("%s is not on contract %s", name, id)
	}
	pd := filepath.Join(dir, name)
	// The placer's PATH, so the worker finds claude, node and the project's
	// tools however it was started.
	if c.Path != "" {
		os.Setenv("PATH", c.Path)
	}
	tag := os.Getenv(tagVar)
	if !validTag(tag) || tag != readTag(dir, name) {
		return fmt.Errorf("%s has no process tag; workers are started by place or start", name)
	}
	// close sends SIGTERM: stop the professional, keep what they wrote.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	now := time.Now().UTC().Truncate(time.Second)
	s := status{State: "setup", WorkerPID: os.Getpid(), StartedAt: &now}
	save := func() { _ = writeJSON(filepath.Join(pd, "status.json"), s) }
	finish := func(state, reason string) {
		t := time.Now().UTC().Truncate(time.Second)
		s.State, s.Reason, s.FinishedAt = state, reason, &t
		save()
	}
	save()

	var m *manifest
	var vars eqVars
	var before *manifest
	if ci.Kind == "critic" && len(ci.Uses) > 0 {
		// Its equipment needs a room: no app in it, the hooks start what the
		// commands run on.
		err = makeRoom(c, ci, pd, &s, save)
		if err == nil {
			vars, err = startEquipment(ctx, c, ci, pd, tag, &s, save)
		}
		if err != nil {
			teardown(c, dir, name, &s)
			if ctx.Err() != nil {
				finish("closed", "closed while the room was being built")
			} else {
				finish("failed", err.Error())
			}
			return nil
		}
	}
	if ci.Kind == "user" {
		if ci.NoBrowser {
			err = makeRoom(c, ci, pd, &s, save)
		} else {
			m, err = buildRoom(ctx, c, ci, pd, &s, save)
			if err == nil && c.Before {
				before, err = beforeRoom(ctx, c, ci, pd, &s, save)
			}
		}
		if err == nil && len(ci.Uses) > 0 {
			vars, err = startEquipment(ctx, c, ci, pd, tag, &s, save)
		}
		if err == nil {
			err = sealSandboxed(c, ci, s.Room)
		}
		if err != nil {
			teardown(c, dir, name, &s)
			var r refusal
			if errors.As(err, &r) {
				finish("refused", r.msg)
			} else if ctx.Err() != nil {
				finish("closed", "closed while the room was being built")
			} else {
				finish("failed", err.Error())
			}
			return nil
		}
	}

	s.State = "working"
	save()
	state, reason := runProfessional(ctx, c, ci, pd, tag, m, before, vars, &s, save)
	if ci.Kind == "user" || len(ci.Uses) > 0 {
		teardown(c, dir, name, &s)
	}
	finish(state, reason)
	return nil
}

func freePorts(n int) ([]int, error) {
	var ls []net.Listener
	defer func() {
		for _, l := range ls {
			l.Close()
		}
	}()
	var ports []int
	for range n {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		ls = append(ls, l)
		ports = append(ports, l.Addr().(*net.TCPAddr).Port)
	}
	return ports, nil
}

var portNames = []string{"web", "api", "db", "aux"}

// setupEnv is what the setup and teardown hooks get: enough to build a
// stack, plus whatever the project names in roster.toml ([setup] pass_env).
// Not the person's whole environment: a token in it would be one debug page
// away from a professional.
func setupEnv(project string) []string {
	keep := []string{"PATH", "HOME", "USER", "LOGNAME", "SHELL", "LANG", "LC_ALL", "LC_CTYPE", "TMPDIR", "TERM"}
	keep = append(keep, loadProjectConfig(project).Setup.PassEnv...)
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "MISE_") || strings.HasPrefix(kv, "__MISE_") {
			env = append(env, kv)
		}
	}
	for _, k := range keep {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

func roomEnv(c *contract, name, room, ports, mf, tag string) []string {
	env := append(tagEnv(setupEnv(c.Project), tag+"/setup"),
		"ADMINISTRATION_CONTRACT="+c.ID,
		"ADMINISTRATION_PROFESSIONAL="+name,
		"ADMINISTRATION_MAIN="+c.Project,
		// The project itself, even when MAIN is the release check's before
		// clone (where a shared install like node_modules isn't).
		"ADMINISTRATION_PROJECT="+c.Project,
		// The room, and the folder in it the app goes in (the room's app/,
		// or before/ for a release check's other side).
		"ADMINISTRATION_ROOM="+filepath.Dir(room),
		"ADMINISTRATION_APP="+room,
		"ADMINISTRATION_PORTS="+ports,
		"ADMINISTRATION_MANIFEST="+mf,
	)
	if c.Data != "" {
		env = append(env, "ADMINISTRATION_DATA="+c.Data)
	}
	for _, kv := range strings.Split(ports, ",") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env = append(env, "ADMINISTRATION_PORT_"+strings.ToUpper(k)+"="+v)
		}
	}
	return env
}

// roomPath is a professional's room: a short slot in the contract's folder,
// not its name. Paths in a room end up in Unix socket paths (the browser's,
// which can't pass about 100 bytes), and names run to 64 characters.
func roomPath(rooms string, c *contract, name string) string {
	for i, p := range c.Professionals {
		if p.Name == name {
			return filepath.Join(rooms, c.ID, fmt.Sprintf("r%d", i+1))
		}
	}
	return filepath.Join(rooms, c.ID, name)
}

// buildRoom runs the project's setup hook as the person, outside any
// sandbox: this is the risky work, and it's done by the Administration, never
// by a professional. Then it holds the room to the Rules.
func buildRoom(ctx context.Context, c *contract, ci *castItem, pd string, s *status, save func()) (*manifest, error) {
	rooms, err := roomsDir()
	if err != nil {
		return nil, err
	}
	// A room is three folders. app is the copy of the app, built by setup and
	// never writable by the professional. desk is the professional's working
	// directory, where the browser may save screenshots. tmp holds the
	// browser's throwaway profile.
	room := roomPath(rooms, c, ci.Name)
	for _, d := range []string{"app", "desk", "tmp"} {
		if err := os.MkdirAll(filepath.Join(room, d), 0o700); err != nil {
			return nil, err
		}
	}
	app := filepath.Join(room, "app")
	ps, err := freePorts(len(portNames))
	if err != nil {
		return nil, err
	}
	var kv []string
	for i, n := range portNames {
		kv = append(kv, fmt.Sprintf("%s=%d", n, ps[i]))
	}
	ports := strings.Join(kv, ",")
	mf := filepath.Join(pd, "room.json")
	s.Room, s.Ports = room, ps
	save()
	_ = os.WriteFile(filepath.Join(pd, "ports"), []byte(ports+"\n"), 0o600)

	logf, err := os.OpenFile(filepath.Join(pd, "setup.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	defer logf.Close()
	sctx, cancel := context.WithTimeout(ctx, setupTimeout())
	defer cancel()
	cmd := exec.Command(filepath.Join(c.Project, ".administration", "setup"), c.Project, app)
	cmd.Dir = c.Project
	cmd.Env = roomEnv(c, ci.Name, app, ports, mf, readTag(filepath.Dir(pd), ci.Name))
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("setup: %v", err)
	}
	// The servers setup leaves running stay in its process group; teardown
	// clears the group in case the project's own teardown misses one.
	s.Groups = append(s.Groups, cmd.Process.Pid)
	save()
	if err := waitGroup(sctx, cmd); err != nil {
		if sctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("setup took longer than %s; see setup.log", setupTimeout())
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("setup failed (%v); see %s", err, filepath.Join(pd, "setup.log"))
	}

	b, err := os.ReadFile(mf)
	if err != nil {
		return nil, errors.New("setup finished without writing $ADMINISTRATION_MANIFEST")
	}
	var m manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("the room's manifest: %v", err)
	}
	if err := checkManifest(&m, ps, ci.SignsInAs); err != nil {
		return nil, err
	}
	s.URL = m.URL
	save()
	if err := answers(ctx, m.URL, 60*time.Second); err != nil {
		return nil, err
	}
	return &m, nil
}

// waitGroup waits for cmd; if ctx ends first, the whole group is stopped.
func waitGroup(ctx context.Context, cmd *exec.Cmd) error {
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		killGroup(cmd.Process.Pid, done)
		return ctx.Err()
	}
}

// killGroup asks a process group to leave, then makes it.
func killGroup(pgid int, done <-chan error) {
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}

// How long a project's teardown hook may take.
const teardownLimit = 5 * time.Minute

// manifestPorts is the ports the room's manifest names: the gate opens these
// and no others (not the db or aux ports, which the app uses internally).
func manifestPorts(m *manifest) []int {
	var ports []int
	seen := map[int]bool{}
	for _, o := range append([]string{m.URL}, m.Origins...) {
		u, err := url.Parse(o)
		if err != nil {
			continue
		}
		if p, err := strconv.Atoi(u.Port()); err == nil && !seen[p] {
			seen[p] = true
			ports = append(ports, p)
		}
	}
	return ports
}

func setupTimeout() time.Duration {
	if d, err := time.ParseDuration(os.Getenv("ADMINISTRATION_SETUP_TIMEOUT")); err == nil && d > 0 {
		return d
	}
	return 15 * time.Minute
}

// roomData is what a room's data may be: seed data, or a local copy the
// project's own tooling made on this machine. Never production.
func roomData(d string) bool { return d == "seed" || d == "local" }

// checkManifest holds the room to Rule 1: seed or local data, and nothing
// but this room's own ports on this machine.
func checkManifest(m *manifest, ports []int, signsInAs string) error {
	if !roomData(m.Data) {
		return refusal{fmt.Sprintf("the room's data is %q; contracts run on seed data or a local copy on this machine (\"seed\" or \"local\"), never production (no business on consecrated ground)", m.Data)}
	}
	if m.URL == "" {
		return errors.New("the room's manifest has no url")
	}
	own := map[string]bool{}
	for _, p := range ports {
		own[fmt.Sprint(p)] = true
	}
	origins := append([]string{m.URL}, m.Origins...)
	for _, o := range origins {
		u, err := url.Parse(o)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return refusal{fmt.Sprintf("origin %q: only http(s) on this machine", o)}
		}
		h := u.Hostname()
		if h != "127.0.0.1" && h != "localhost" {
			return refusal{fmt.Sprintf("origin %q is not on this machine; professionals only reach their own room", o)}
		}
		if !own[u.Port()] {
			return refusal{fmt.Sprintf("origin %q is not on one of this room's ports (%v)", o, ports)}
		}
	}
	if signsInAs == "any" {
		if len(m.Roles) < 2 {
			return errors.New("this professional signs in as every role, and the room has fewer than two logins (.administration/roles)")
		}
	} else if signsInAs != "" {
		r, ok := m.Roles[signsInAs]
		if !ok || r.id() == "" || r.Password == "" {
			return fmt.Errorf("this professional signs in as %q, and the room has no such login (.administration/roles)", signsInAs)
		}
	}
	return nil
}

// allowedOrigins is what Playwright may request: the room's origins under
// both local names.
func allowedOrigins(m *manifest) []string {
	seen := map[string]bool{}
	var out []string
	for _, o := range append([]string{m.URL}, m.Origins...) {
		u, _ := url.Parse(o)
		for _, h := range []string{"127.0.0.1", "localhost"} {
			v := fmt.Sprintf("%s://%s:%s", u.Scheme, h, u.Port())
			if !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	return out
}

func answers(ctx context.Context, u string, within time.Duration) error {
	deadline := time.Now().Add(within)
	// Only ever this machine (the manifest is checked first), so a room's
	// self-signed certificate is fine.
	client := &http.Client{Timeout: 5 * time.Second,
		Transport:     &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for {
		req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
		if resp, err := client.Do(req); err == nil {
			resp.Body.Close()
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the room's url %s never answered", u)
		}
		time.Sleep(time.Second)
	}
}

// teardown runs the project's teardown hook (if any) and clears whatever is
// left of the room's process groups. The room's files stay until close.
func teardown(c *contract, dir, name string, s *status) {
	if s.TornDown || s.Room == "" {
		return
	}
	pd := filepath.Join(dir, name)
	var ci *castItem
	for i := range c.Professionals {
		if c.Professionals[i].Name == name {
			ci = &c.Professionals[i]
		}
	}
	if ci != nil && len(ci.Uses) > 0 {
		stopEquipment(c, ci, pd, readTag(dir, name), s)
	}
	teardownBefore(c, dir, name, s)
	hook := filepath.Join(c.Project, ".administration", "teardown")
	if st, err := os.Stat(hook); err == nil && st.Mode()&0o111 != 0 && (ci == nil || !ci.NoBrowser && ci.Kind != "critic") {
		ports, _ := os.ReadFile(filepath.Join(pd, "ports"))
		logf, _ := os.OpenFile(filepath.Join(pd, "teardown.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		ctx, cancel := context.WithTimeout(context.Background(), teardownLimit)
		app := filepath.Join(s.Room, "app")
		cmd := exec.Command(hook, c.Project, app)
		cmd.Dir = c.Project
		cmd.Env = roomEnv(c, name, app, strings.TrimSpace(string(ports)), filepath.Join(pd, "room.json"), readTag(dir, name))
		cmd.Stdout, cmd.Stderr = logf, logf
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if cmd.Start() == nil {
			_ = waitGroup(ctx, cmd)
		}
		cancel()
		if logf != nil {
			logf.Close()
		}
	}
	// Whatever setup left running carries its tag and goes now. What it
	// started outside its own descendants (docker compose -d, a daemon) is
	// teardown's job; if a port still answers, the books say so.
	clearTagged(readTag(dir, name) + "/setup")
	// A process killed a moment ago can still hold its port: give it two
	// seconds to let go before calling it a leftover.
	open := listening(s.Ports)
	for i := 0; i < 8 && len(open) > 0; i++ {
		time.Sleep(250 * time.Millisecond)
		open = listening(open)
	}
	if len(open) > 0 {
		s.Leftovers = fmt.Sprintf("still listening after teardown: %v (check .administration/teardown)", open)
	}
	s.TornDown = true
}

// The tools a browser professional may use. An allowlist, so a new
// Playwright tool is off until someone decides otherwise. Left out on
// purpose: browser_file_upload and browser_drop (they read files from the
// machine into a page), browser_run_code_unsafe (it runs code in the
// Playwright server, outside every wall).
// seesScreen: a user perceives the app as a person at a screen, unless its
// persona uses a screen reader (it then gets what one announces: the page's
// accessibility snapshot).
// screenEquipment says whether any of a user's equipment is seen as a
// screen: the house's screen for a device, or a window seen like the browser.
func screenEquipment(c *contract, ci *castItem) bool {
	for _, u := range ci.Uses {
		if eq, err := checkEquipment(c.Project, u); err == nil && (eq.Server == "screen" || eq.Server == "playwright") {
			return true
		}
	}
	return false
}

func seesScreen(ci *castItem) bool { return ci.Kind == "user" && ci.Sees != "screen-reader" }

var browserTools = []string{
	"browser_navigate", "browser_navigate_back", "browser_click", "browser_type",
	"browser_fill_form", "browser_select_option", "browser_press_key", "browser_hover",
	"browser_drag", "browser_snapshot", "browser_find", "browser_take_screenshot",
	"browser_wait_for", "browser_resize", "browser_tabs", "browser_console_messages",
	"browser_network_requests", "browser_network_request", "browser_handle_dialog",
	"browser_emulate_media", "browser_evaluate", "browser_close",
	"browser_mouse_click_xy", "browser_mouse_move_xy", "browser_mouse_wheel", "browser_mouse_drag_xy",
	"browser_press_sequentially",
}

var deniedBrowserTools = []string{"browser_file_upload", "browser_drop", "browser_run_code_unsafe"}

// Keep in step with mcp= in install.sh, which installs it into the tools
// folder: the server the wall depends on is never resolved from the room.
const playwrightMCP = "@playwright/mcp@0.0.83"

func toolsDir() string {
	if d := os.Getenv("ADMINISTRATION_TOOLS"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "administration", "tools")
}

func playwrightCLI() string {
	return filepath.Join(toolsDir(), "node_modules", "@playwright", "mcp", "cli.js")
}

// Files a professional never reads and a handout never carries.
var secretGlobs = []string{".env", ".env.*", ".envrc", ".npmrc", ".netrc", ".pypirc", ".dev.vars",
	"*.pem", "*.key", "*.p12", "*.pfx", "id_rsa*", "id_ed25519*", "id_ecdsa*", "*.tfvars", "*.tfstate",
	"credentials*.json", "secrets.*", "settings.local.json", ".git/config"}

// settingsFor is the wall in Claude Code's own terms. --restricted already
// takes away every tool that runs commands, ignores the person's settings and
// confines the file tools to the working directory; these deny the rest
// outright, in case a later release loosens one of those.
func settingsFor(c *contract, ci *castItem, room string) map[string]any {
	deny := []string{"WebFetch", "WebSearch", "NotebookEdit", "Agent"}
	shell := len(sandboxedCommands(c, ci, room)) > 0
	if !shell {
		// Only a user with a sandboxed tool writes: its own test inputs, in
		// its desk (an Edit rule on the command line; a deny here would
		// override it). Everyone else writes nothing.
		deny = append(deny, "Bash", "Edit", "Write")
	} else {
		// Claude Code runs some read-only commands without a rule; git reads
		// config from where it's run, and cd would move it there.
		deny = append(deny, "Bash(git)", "Bash(git *)", "Bash(cd)", "Bash(cd *)")
	}
	for _, g := range secretGlobs {
		deny = append(deny, "Read(**/"+g+")")
	}
	if ci.Kind != "critic" {
		// A user's room is outside home, so all of home is off limits.
		deny = append(deny, "Read(~/**)", "Read(//Users/**)", "Read(//home/**)")
		for _, t := range deniedBrowserTools {
			deny = append(deny, "mcp__playwright__"+t)
		}
	}
	settings := map[string]any{
		"permissions":                map[string]any{"deny": deny, "disableBypassPermissionsMode": "disable"},
		"disableAllHooks":            true,
		"enableAllProjectMcpServers": false,
		"autoMemoryEnabled":          false,
	}
	if shell {
		home, _ := os.UserHomeDir()
		// The OS sandbox under every command: no network at all, nothing of
		// the person's readable, and no running unsandboxed. Commands still
		// need an allow rule each (autoAllow off), and if the sandbox can't
		// start, nothing runs rather than everything running bare.
		settings["sandbox"] = map[string]any{
			"enabled":                  true,
			"autoAllowBashIfSandboxed": false,
			"allowUnsandboxedCommands": false,
			"failIfUnavailable":        true,
			"network": map[string]any{"allowedDomains": []string{}, "allowLocalBinding": false, "strictAllowlist": true,
				"allowAllUnixSockets": false},
			// Home, the session's runtime folder (its sockets), container
			// sockets, other rooms and shared temp folders are all out of
			// reach; the tool's own build and the desk can't be rewritten
			// except where the desk says.
			"filesystem": map[string]any{
				"denyRead": append([]string{home, "/home", "/Users", "/root", "/run/user", "/var/run/docker.sock",
					"/run/docker.sock", "/tmp", "/mnt", "/media"}, otherRooms(room)...),
				"denyWrite": []string{filepath.Join(room, "app"), filepath.Join(room, "equipment")},
			},
		}
	}
	return settings
}

// browserConfig is Playwright's own config file: an isolated profile, every
// connection through the gate (loopback too, which Chromium otherwise sends
// direct), no WebRTC over UDP, no DNS prefetch, no service workers, and
// Chromium's own sandbox on.
func browserConfig(m *manifest, room, gateAddr string, show, screen bool) map[string]any {
	desk := filepath.Join(room, "desk")
	cfg := map[string]any{
		"browser": map[string]any{
			"browserName": "chromium",
			"isolated":    true,
			"launchOptions": map[string]any{
				"headless":        !show,
				"chromiumSandbox": chromiumSandbox(),
				"proxy":           map[string]any{"server": "http://" + gateAddr, "bypass": "<-loopback>"},
				"args": []string{"--proxy-bypass-list=<-loopback>",
					"--force-webrtc-ip-handling-policy=disable_non_proxied_udp",
					"--webrtc-ip-handling-policy=disable_non_proxied_udp",
					"--dns-prefetch-disable", "--disable-background-networking"},
			},
			"contextOptions": map[string]any{"serviceWorkers": "block", "acceptDownloads": false},
		},
		"network":   map[string]any{"allowedOrigins": allowedOrigins(m)},
		"outputDir": filepath.Join(desk, ".shots"),
	}
	if screen {
		// Coordinate clicks, and no text dump of the page after each action:
		// what it knows of the page is what the screenshots show.
		cfg["capabilities"] = []string{"core", "core-input", "core-navigation", "core-tabs", "vision"}
		cfg["snapshot"] = map[string]any{"mode": "none"}
	}
	return cfg
}

func mcpFor(node, configPath string) map[string]any {
	return map[string]any{"mcpServers": map[string]any{
		"playwright": map[string]any{
			"command": node,
			"args":    []string{playwrightCLI(), "--config", configPath},
		},
	}}
}

func brief(c *contract, ci *castItem, m, before *manifest, room string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You have taken a contract from the Administration.\n\n")
	fmt.Fprintf(&b, "Contract %s: %s\n\n", c.ID, c.Target)
	if ci.Task != "" {
		fmt.Fprintf(&b, "Your own task in it, from whoever placed it: %s\nDo this first and fully; the contract's target is the context.\n\n", ci.Task)
	}
	fmt.Fprintf(&b, "You are %s, working alone. Write as you go:\n", ci.Name)
	fmt.Fprintf(&b, "- First write your plan: one line for each thing you'll check, numbered, as PLAN 1: ..., PLAN 2: ... Take them from your own task, your persona's checks, every screen, flow or path the contract names, and each size you test. When one is done, write COVERED 3 (after any FINDING: lines it gave); one you can't do is UNREACHED: 3 and why. There's no clock for you to watch: you'll be stopped if you take too long, and everything you've written is kept.\n")
	fmt.Fprintf(&b, "- The moment you find something, write one line starting FINDING: with what happened, where (the URL and the steps, or the file and line), and what you expected instead.\n")
	fmt.Fprintf(&b, "- For anything you meant to try and could not reach, write one line starting UNREACHED: and why.\n")
	fmt.Fprintf(&b, "- A FINDING: line is only for something wrong. A check that held, or a comparison you ruled out, goes in your report under what held, never on a FINDING: line.\n")
	fmt.Fprintf(&b, "- Something wrong that you can excuse is still a FINDING: text cut off whose full version shows elsewhere, a control that's only a scroll away, a task that takes more steps than before. Put the excuse in the line and let the reader weigh it; don't drop it.\n")
	fmt.Fprintf(&b, "- End with a short report: what you tried, what held, what broke. Under what broke, give every finding again as its own FINDING: line (repeats are merged): only FINDING: lines are counted, and a problem written any other way is lost.\n\n")
	if ci.Kind == "user" && len(ci.Uses) > 0 {
		fmt.Fprintf(&b, "You work through the project's equipment, started fresh for you:\n")
		for _, u := range ci.Uses {
			about := ""
			if eq, err := checkEquipment(c.Project, u); err == nil {
				about = strings.TrimSpace(eq.About)
			}
			if eq, err := checkEquipment(c.Project, u); err == nil && eq.Server == "sandboxed" {
				fmt.Fprintf(&b, "- %s (run with Bash, these commands only: %s): %s\n", u, strings.Join(sandboxedCommands(c, &castItem{Uses: []string{u}}, room), ", "), about)
				continue
			}
			fmt.Fprintf(&b, "- %s (its tools are named mcp__eq_%s__…): %s\n", u, strings.ReplaceAll(u, "-", "_"), about)
		}
		if len(sandboxedCommands(c, ci, room)) > 0 {
			fmt.Fprintf(&b, "A command runs in an OS sandbox: no network, nothing outside the room, and the tool's own files can't be changed. A finding about a command is what it did wrong: the exact command, what it printed or wrote, and what it should have done. Check output files, exit codes and messages, not just that it ran; a result without an exit code means it exited 0. Run each command on its own: chaining with && ; | or a cd isn't allowed. Your desk (the working directory) is where its files go.\n")
		}
		if seesScreen(ci) && screenEquipment(c, ci) {
			fmt.Fprintf(&b, "On equipment with a screen you are a person in front of it, and the screen is all you have: there is no list of what's on it, no text of it, nothing but the picture. Look before you act and after every action (screen_look, or browser_look in a window, says what changed), and decide from the picture alone what's there, what it says and what looks like something you can press. Tap or click where you see the thing (its x and y in the picture), drag or scroll to move, type into what you tapped, press keys for the rest. If you can't tell what to press, or whether something worked, a person can't either: that's a finding.\n")
		}
		if ci.NoBrowser {
			fmt.Fprintf(&b, "Those tools are the only way in. Take a screenshot when you need to see what a person would see, and say in each finding which tool calls get there.\n")
			fmt.Fprintf(&b, "A finding is anything that would trip up the person you are, however small, with the steps to see it: they can't tell something is a button, don't know it worked, have to scroll to find what they need, read a message that's no longer true, or have to ask someone. Judge it as them, not as a designer. Leave out only your own preferences that wouldn't change what they do: a colour you'd pick, a wording you like better that means the same.\n")
		}
	}
	shots := ci.Kind == "user" && !ci.NoBrowser
	for _, u := range ci.Uses {
		if eq, err := checkEquipment(c.Project, u); err == nil && slices.Contains(eq.Tools, "browser_take_screenshot") {
			shots = true
		}
	}
	if shots {
		// Found live: with a filename the screenshot goes to disk and the
		// professional gets only text back, so it never sees the screen.
		fmt.Fprintf(&b, "To see the screen, call browser_take_screenshot with no filename: the picture comes back to you. Given a filename it's only saved, and you see nothing.\n")
	}
	if seesScreen(ci) && !ci.NoBrowser {
		fmt.Fprintf(&b, "You are a person in front of a screen, and the screen is all you have. Take a screenshot before you act and after every action, and decide from the picture alone what's there, what it says and what looks like something you can press. Click with browser_mouse_click_xy where you see the thing in the picture (its x and y in the screenshot); scroll with browser_mouse_wheel; type with browser_type_text into what you clicked; press keys with browser_press_key (Tab, Enter, Escape, the arrows: a dropdown opens with a click and is chosen with the arrows and Enter). There is no list of the page's elements for you: what isn't in the screenshot isn't there for you until you scroll to it or it appears. If you can't tell what to press, or whether something worked, a person can't either: that's a finding.\n")
	} else if ci.Kind == "user" && ci.Sees == "screen-reader" && !ci.NoBrowser {
		fmt.Fprintf(&b, "You use a screen reader: what you perceive of the page is what browser_snapshot reads out (names, roles, the order they come in), and you move by keyboard. A control with no name, the wrong role, an order that makes no sense, or something you can't reach by keyboard is what trips you up.\n")
	}
	if ci.Kind == "user" {
		if h := loadProjectConfig(c.Project).Handout.Files; len(h) > 0 {
			var names []string
			for _, f := range h {
				names = append(names, filepath.Base(f))
			}
			fmt.Fprintf(&b, "The project's own rules (%s) are at the end of your instructions, whole, and in ./handout/. A finding that breaks one of them is a finding, whatever it looks like; name the rule.\n", strings.Join(names, ", "))
		}
	}
	if ci.Kind == "user" && !ci.NoBrowser {
		fmt.Fprintf(&b, "The app is running at %s. Use the browser tools; that is the only way in, and only this app's own address will load.\n", m.URL)
		if c.Data != "" {
			fmt.Fprintf(&b, "Its data is the project's %q set: %s\n", c.Data, strings.TrimSpace(loadProjectConfig(c.Project).Data[c.Data].About))
		}
		if before != nil {
			fmt.Fprintf(&b, "This is a release check. The same app as it was before these changes is running at %s, with its own data and logins (below), in a browser of its own: its tools are named mcp__before__… and work the same as this app's (mcp__playwright__…). Signing in or out in one never touches the other. Do every flow you test on both, the same way, at the same width: a regression is only visible next to what came before, so never skip the before side, and use your coins for it. Report what's different: something that worked before and doesn't now, looks different, or goes somewhere else. Start each such finding with NEW: (the change caused it) after the FINDING: mark, and say what the before app did. Broken on both but worse on the new one (cut off further, on more screens or more days, more steps, a new case that hits it) is NEW: say what each app shows. Only something the same on both is old: write it as a FINDING: starting OLD:, once, briefly. What's the same on both and fine is what held.\n", before.URL)
		}
		fmt.Fprintf(&b, "If the contract or your persona names a device or a screen width (a phone, 320px, a Chromebook, large text), resize the browser to it before anything else, and test there; say in your report which sizes you used.\n")
		if seesScreen(ci) {
			fmt.Fprintf(&b, "On every screen you look at, at every width you test, run browser_layout_check. It lists candidates: text cut off by its box, text squeezed to a sliver, content clipped by a fixed height, anything past the right edge (a scroll_width over the viewport means sideways scrolling), and actions pushed below the fold. Each is a lead, not a finding: look at it on the screenshot, and report the ones a person in your shoes would trip over, with the width and the element.\n")
			fmt.Fprintf(&b, "Run browser_look once on each screen, then again after every action that should show a result (save, send, add, open, pay, undo, cancel). appeared_since_last_look is what the action put on the page and where it is against the window; controls_in_the_window is how each control there is drawn. A confirmation, an error or the next step that appeared below the window, and a control drawn as plain text, are findings when the person you are would miss them: say the size, the action and what it said.\n")
		} else {
			fmt.Fprintf(&b, "On every screen you look at, at every width you test, run the house layout check with browser_evaluate and this function:\n%s\nIt lists candidates: text cut off by its box, text squeezed to a sliver, content clipped by a fixed height, anything past the right edge (a scroll_width over the viewport means sideways scrolling), and actions pushed below the fold. Each is a lead, not a finding: look at it on screen (a screenshot), and report the ones a person in your shoes would trip over, with the width and the element.\n", layoutCheck)
			fmt.Fprintf(&b, "To measure what the screen shows (never to read the page instead of looking), run the person's-eye check with browser_evaluate once on each screen, then again after every action that should show a result (save, send, add, open, pay, undo, cancel), with this function:\n%s\nappeared_since_last_look is what the action put on the page and where it is against the window; controls_in_the_window is how each control there is drawn. A confirmation, an error or the next step that appeared below the window, and a control drawn as plain text, are findings when the person you are would miss them: say the size, the action and what the check said.\n", lookCheck)
		}
		fmt.Fprintf(&b, "A finding is anything that would trip up the person you are, however small, with the steps to see it: they can't tell something is a button, don't know it worked, have to scroll to find what they need, read a message that's no longer true, or have to ask someone. Judge it as them, not as a designer. Leave out only your own preferences that wouldn't change what they do: a colour you'd pick, a wording you like better that means the same.\n")
		switch ci.SignsInAs {
		case "":
			fmt.Fprintf(&b, "You have no login: sign up, or go without, the way a newcomer would.\n")
		case "any":
			// Testing one role against another needs every login.
			names := make([]string, 0, len(m.Roles))
			for k := range m.Roles {
				names = append(names, k)
			}
			sort.Strings(names)
			fmt.Fprintf(&b, "The test logins in this room, one per role:\n")
			for _, k := range names {
				fmt.Fprintf(&b, "- %s: %s / %s\n", k, m.Roles[k].id(), m.Roles[k].Password)
			}
		default:
			r := m.Roles[ci.SignsInAs]
			fmt.Fprintf(&b, "Sign in as %s: %s / %s\n", ci.SignsInAs, r.id(), r.Password)
		}
		if before != nil && ci.SignsInAs != "" {
			fmt.Fprintf(&b, "On the before app (%s), the logins are its own:\n", before.URL)
			for _, k := range sortedRoles(before.Roles) {
				if ci.SignsInAs == "any" || k == ci.SignsInAs {
					fmt.Fprintf(&b, "- %s: %s / %s\n", k, before.Roles[k].id(), before.Roles[k].Password)
				}
			}
		}
	} else if ci.Kind == "critic" && len(ci.Uses) > 0 {
		fmt.Fprintf(&b, "You read code with Read, Grep and Glob, in the project at your working directory, and you can run what the project's equipment gives you, started fresh for you:\n")
		for _, u := range ci.Uses {
			about := ""
			if eq, err := checkEquipment(c.Project, u); err == nil {
				about = strings.TrimSpace(eq.About)
			}
			fmt.Fprintf(&b, "- %s (its tools are named mcp__eq_%s__…): %s\n", u, strings.ReplaceAll(u, "-", "_"), about)
		}
		fmt.Fprintf(&b, "Reading finds a suspicion; running it settles one. When you suspect an input goes wrong, feed it through the code with those tools and quote what came back. A finding you ran says so, with the call and its output; one you only read says that too. Cite file and line for every finding.\n")
	} else if ci.Kind == "critic" {
		fmt.Fprintf(&b, "You read code only: Read, Grep and Glob, in the project at your working directory. Cite file and line for every finding.\n")
	}
	if ci.Kind == "critic" {
		fmt.Fprintf(&b, "Use your time. A first read finds the obvious; the bugs that ship are in how pieces meet. For each path in scope, follow the data from where it comes in (a form, a request, a stored row, an offline queue) through every function that touches it to where it is shown, stored or charged, across files, and ask at each step what happens on the second time, the empty case, the other branch, the retry. Only stop when you have followed every path, or your coins are nearly gone.\n")
	}
	if ch := c.Changes; ch != nil {
		fmt.Fprintf(&b, "\nThe contract is on these changes: everything since %s (merge base %.8s), working tree included.\n", ch.Base, ch.MergeBase)
		if ci.Kind == "critic" {
			fmt.Fprintf(&b, "The diff is %s. Read it first, then the code around it.\n", ch.Patch)
			fmt.Fprintf(&b, "This is a release check: start each finding, after the FINDING: mark, with one of three, and say which lines of the diff it comes from.\n- NEW: these changes make the app behave differently for the worse: something that worked before doesn't, or now goes wrong. Say what the code did before (the old lines, or that the path didn't exist) and what it does now; a risk you can't trace to lines the diff added or changed isn't NEW.\n- CHANGE: the change's own text is wrong but nothing that ran before behaves differently: a comment or document that misstates the code, a test that can fail or tests the wrong thing, a check that's missing.\n- OLD: it was already so before these changes.\n")
		}
		fmt.Fprintf(&b, "Changed files:\n")
		for i, f := range append(ch.Files, ch.Untracked...) {
			if i == 200 {
				fmt.Fprintf(&b, "- … and %d more\n", len(ch.Files)+len(ch.Untracked)-200)
				break
			}
			fmt.Fprintf(&b, "- %s\n", f)
		}
	}
	fmt.Fprintf(&b, "\nThe Rules: no claim without evidence; say what you could not reach; this is test data on this machine only, so never try to reach anything else.\n")
	return b.String()
}

// childEnv is what a professional's claude inherits: enough to run and log
// in, and nothing from the session that placed the contract.
func childEnv(tmp string) []string {
	keep := []string{"PATH", "HOME", "USER", "LOGNAME", "SHELL", "LANG", "LC_ALL", "TMPDIR", "TERM",
		"CLAUDE_CONFIG_DIR", "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_OAUTH_TOKEN",
		"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX"}
	// Not CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: with it set, Claude Code
	// let a professional write anywhere in its working directory, whatever
	// the permission rules said (found by a live probe, 2026-10-24).
	env := []string{"DISABLE_AUTOUPDATER=1"}
	// A claude (or node) installed through mise may be a wrapper that hangs
	// without mise's own variables. They're tool settings, not secrets.
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "MISE_") || strings.HasPrefix(kv, "__MISE_") {
			env = append(env, kv)
		}
	}
	for _, k := range keep {
		if k == "TMPDIR" && tmp != "" {
			env = append(env, "TMPDIR="+tmp)
			continue
		}
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// displayEnv is what a browser needs to open a window on the person's
// screen, given only when they asked to watch (--show-browsers). It lets the
// browser draw on that screen; the professional still has nothing but the
// browser tools, so it can't look at anything else there. macOS needs none.
func displayEnv() []string {
	var env []string
	for _, k := range []string{"DISPLAY", "XAUTHORITY", "WAYLAND_DISPLAY", "XDG_RUNTIME_DIR"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

func runProfessional(ctx context.Context, c *contract, ci *castItem, pd, tag string, m, before *manifest, vars eqVars, s *status, save func()) (string, string) {
	settings := filepath.Join(pd, "settings.json")
	if err := writeJSON(settings, settingsFor(c, ci, s.Room)); err != nil {
		return "failed", err.Error()
	}
	b := brief(c, ci, m, before, s.Room)
	_ = os.WriteFile(filepath.Join(pd, "brief.md"), []byte(b), 0o600)
	// The brief goes in on stdin, as the first of the session's messages: when
	// the professional thinks it's done, the Administration may send it back.
	args := []string{"-p", "--input-format", "stream-json", "--restricted", "--strict-mcp-config",
		"--settings", settings, "--no-session-persistence", "--permission-prompts", "none",
		"--output-format", "stream-json", "--verbose",
		"--append-system-prompt-file", filepath.Join(pd, "persona.md")}
	cwd, tmp := c.Project, ""
	if ci.Kind == "user" {
		needNode := !ci.NoBrowser
		for _, u := range ci.Uses {
			if eq, err := checkEquipment(c.Project, u); err == nil && eq.Server == "playwright" {
				needNode = true
			}
		}
		node, err := exec.LookPath("node")
		if needNode && err != nil {
			return "failed", "node isn't on PATH; the browser needs it"
		}
		if _, err := os.Stat(playwrightCLI()); needNode && err != nil {
			return "failed", "the browser isn't installed (" + playwrightCLI() + "); run install.sh"
		}
		servers := map[string]any{}
		var allowed []string
		if !ci.NoBrowser {
			g, err := openGate(manifestPorts(withBefore(m, before)), filepath.Join(pd, "gate.log"))
			if err != nil {
				return "failed", "could not open the gate: " + err.Error()
			}
			defer func() { s.Blocked = g.close() }()
			// A screen reader's user moves by keyboard and the page's names,
			// not by pointing at the picture.
			var tools []string
			for _, t := range browserTools {
				if !strings.HasPrefix(t, "browser_mouse_") {
					tools = append(tools, t)
				}
			}
			if seesScreen(ci) {
				tools = screenToolNames()
			}
			// One browser per app, each with a profile of its own: the two
			// apps share a host, and cookies don't care about ports, so in
			// one browser signing in to one app signed the user out of the
			// other.
			type side struct {
				key, file string
				m         *manifest
			}
			sides := []side{{"playwright", "browser.json", m}}
			if before != nil {
				sides = append(sides, side{"before", "browser-before.json", before})
			}
			for _, sd := range sides {
				cfg := filepath.Join(pd, sd.file)
				if err := writeJSON(cfg, browserConfig(sd.m, s.Room, g.addr(), c.ShowBrowsers, seesScreen(ci))); err != nil {
					return "failed", err.Error()
				}
				srv := mcpFor(node, cfg)["mcpServers"].(map[string]any)["playwright"]
				if seesScreen(ci) {
					if srv, err = screenServer(node, cfg); err != nil {
						return "failed", err.Error()
					}
				}
				servers[sd.key] = srv
				for _, t := range tools {
					allowed = append(allowed, "mcp__"+sd.key+"__"+t)
				}
			}
		}
		eqs, eqAllowed, err := equipmentServers(c, ci, s.Room, vars, node)
		if err != nil {
			return "failed", err.Error()
		}
		for k, v := range eqs {
			servers[k] = v
		}
		allowed = append(allowed, eqAllowed...)
		mcp := filepath.Join(pd, "mcp.json")
		if err := writeJSON(mcp, map[string]any{"mcpServers": servers}); err != nil {
			return "failed", err.Error()
		}
		var denied []string
		for k := range servers {
			for _, t := range deniedBrowserTools {
				denied = append(denied, "mcp__"+k+"__"+t)
			}
		}
		if seesScreen(ci) && !ci.NoBrowser {
			// The page's text: a person at a screen has nothing like it.
			for _, k := range []string{"playwright", "before"} {
				if _, ok := servers[k]; !ok {
					continue
				}
				for _, t := range browserTools {
					if !slices.Contains(screenPassed, t) {
						denied = append(denied, "mcp__"+k+"__"+t)
					}
				}
			}
		}
		sort.Strings(denied)
		// The project's handout, if it has one: the user may read its desk
		// and nothing else (anything outside the working directory asks,
		// and nobody is there to answer).
		tools := ""
		if names, err := copyHandout(c.Project, filepath.Join(s.Room, "desk")); err != nil {
			return "failed", err.Error()
		} else if len(names) > 0 {
			tools = "Read"
			allowed = append(allowed, "Read("+absRule(filepath.Join(s.Room, "desk"))+"/**)")
		}
		if cmds := sandboxedCommands(c, ci, s.Room); len(cmds) > 0 {
			// Its desk is where the tool's input and output go: it reads
			// and writes there, and nowhere else.
			tools = "Read,Grep,Glob,Write,Bash"
			desk := absRule(filepath.Join(s.Room, "desk"))
			allowed = append(allowed, "Read("+desk+"/**)", "Edit("+desk+"/**)")
			for _, cmd := range cmds {
				allowed = append(allowed, "Bash("+cmd+")", "Bash("+cmd+" *)")
			}
			args = append(args, "--add-dir", filepath.Join(s.Room, "equipment"))
		}
		args = append(args, "--mcp-config", mcp, "--tools", tools,
			"--allowedTools", strings.Join(allowed, ","), "--disallowedTools", strings.Join(denied, ","))
		cwd, tmp = filepath.Join(s.Room, "desk"), filepath.Join(s.Room, "tmp")
	} else {
		args = append(args, "--tools", "Read,Grep,Glob")
		if c.Changes != nil {
			args = append(args, "--add-dir", filepath.Dir(c.Changes.Patch))
		}
		if len(ci.Uses) > 0 {
			// Its commands, and nothing else it can run.
			eqs, eqAllowed, err := equipmentServers(c, ci, s.Room, vars, "")
			if err != nil {
				return "failed", err.Error()
			}
			mcp := filepath.Join(pd, "mcp.json")
			if err := writeJSON(mcp, map[string]any{"mcpServers": eqs}); err != nil {
				return "failed", err.Error()
			}
			args = append(args, "--mcp-config", mcp, "--allowedTools", strings.Join(eqAllowed, ","))
		}
	}
	if ci.Model != "" && ci.Model != "inherit" {
		args = append(args, "--model", ci.Model)
	}

	stream, err := os.OpenFile(filepath.Join(pd, "stream.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "failed", err.Error()
	}
	defer stream.Close()
	errf, _ := os.OpenFile(filepath.Join(pd, "claude.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	defer errf.Close()

	cmd := exec.Command(claudeBin(), args...)
	cmd.Dir = cwd
	cmd.Env = tagEnv(childEnv(tmp), tag+"/pro")
	if ci.Kind == "user" && c.ShowBrowsers {
		cmd.Env = append(cmd.Env, displayEnv()...)
	}
	_ = writeJSON(filepath.Join(pd, "argv.json"), map[string]any{"argv": args, "cwd": cwd})
	cmd.Stderr = errf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "failed", err.Error()
	}
	say := func(text string) {
		msg, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user",
			"content": []any{map[string]any{"type": "text", "text": text}}}})
		_, _ = stdin.Write(append(msg, '\n'))
	}
	// A pipe of our own, not StdoutPipe: Wait mustn't depend on every
	// descendant letting go of stdout, and the reader is closed if one holds
	// it after the professional is gone.
	out, w, err := os.Pipe()
	if err != nil {
		return "failed", err.Error()
	}
	defer out.Close()
	cmd.Stdout = w
	if err := cmd.Start(); err != nil {
		w.Close()
		return "failed", fmt.Sprintf("could not start %s: %v", claudeBin(), err)
	}
	w.Close()
	s.Groups = append(s.Groups, cmd.Process.Pid)
	save()
	say(b)

	rep := &report{}
	var repMu sync.Mutex
	ended := make(chan struct{}, 8) // each time it thinks it's done
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		tee := io.TeeReader(out, stream)
		sc := bufio.NewScanner(tee)
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		for sc.Scan() {
			repMu.Lock()
			before := rep.results
			if rep.add(sc.Bytes()) {
				s.Findings, s.Unreached = len(rep.findings), len(rep.unreached)
				save()
			}
			after := rep.results
			repMu.Unlock()
			if after > before {
				select {
				case ended <- struct{}{}:
				default:
				}
			}
		}
		// A line too long to scan (a huge tool result) ends the scan, not
		// the stream: keep draining so claude never blocks on a full pipe.
		_, _ = io.Copy(io.Discard, tee)
	}()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	// The limit is the Administration's, not the professional's: it never
	// hears of it. When it says it's done, it's sent back to what its plan
	// left unanswered, then once for a careful person's second pass; the
	// limit stops it wherever it is.
	coins := time.Duration(ci.CoinsMin * float64(coinUnit()))
	limit := time.NewTimer(coins)
	defer limit.Stop()
	started := time.Now()
	state, reason := "back", ""
	rounds, secondPass := 0, false
wait:
	for {
		select {
		case err = <-done:
			if code := exitCode(err); code != nil {
				s.ExitCode = code
			}
			break wait
		case <-limit.C:
			state, reason = "out", "stopped at the limit"
			killGroup(cmd.Process.Pid, done)
			break wait
		case <-ctx.Done():
			state, reason = "closed", "stopped by close"
			killGroup(cmd.Process.Pid, done)
			break wait
		case <-ended:
			repMu.Lock()
			failed, open, planned := rep.isError != "", rep.unanswered(), len(rep.plan) > 0
			repMu.Unlock()
			msg := ""
			switch {
			case failed || rounds >= maxSendBacks:
			case !planned && rounds == 0:
				msg = "Before you finish: you wrote no plan. Write it now (PLAN 1: ..., one line for each thing to check, from your task, your persona and every surface the contract names), then do each and mark it COVERED n or UNREACHED: n and why."
			case len(open) > 0:
				msg = "Not done yet. These from your plan have no answer:\n- " + strings.Join(open, "\n- ") +
					"\nDo each now, as the person you are, and mark it COVERED n (after any FINDING: lines) or UNREACHED: n and why."
			case !secondPass && time.Since(started) < coins*6/10:
				secondPass = true
				msg = secondPassText
			}
			if msg == "" {
				stdin.Close()
			} else {
				rounds++
				say(msg)
			}
		}
	}
	stdin.Close()
	// The browser runs in a process group of its own; everything the
	// professional started carries its tag and goes with it.
	clearTagged(tag + "/pro")
	select {
	case <-readDone:
	case <-time.After(15 * time.Second):
		out.Close()
		<-readDone
	}
	s.Findings, s.Unreached = len(rep.findings), len(rep.unreached)
	s.CostUSD, s.Turns, s.TokensIn, s.TokensOut = rep.cost, rep.turns, rep.tokensIn, rep.tokensOut
	if state == "back" && rep.final == "" {
		state = "failed"
		reason = "ended without a report"
		if s.ExitCode != nil && *s.ExitCode != 0 {
			reason = fmt.Sprintf("claude exited %d without a report; see claude.log", *s.ExitCode)
		}
		if rep.isError != "" {
			reason = rep.isError
		}
	}
	// A user is only as good as their browser. One that never got a page
	// to load did no work, whatever their report says, whether they came
	// back or were shown out still trying.
	if ci.Kind == "user" && !ci.NoBrowser && (state == "back" || state == "out") && rep.browserOK == 0 {
		state = "failed"
		switch {
		case rep.browserRun == 0:
			reason = "never used the browser"
		case rep.browserErr != "":
			reason = "the browser never worked: " + rep.browserErr
		default:
			reason = "the browser never loaded a page (its calls got no answer)"
		}
	}
	if err := os.WriteFile(filepath.Join(pd, "report.md"), []byte(rep.markdown(c, ci, state, reason)), 0o600); err != nil {
		return "failed", err.Error()
	}
	return state, reason
}

func exitCode(err error) *int {
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		code = -1
	}
	return &code
}

// maxSendBacks is how many times a professional that thinks it's done is
// sent back to its work.
const maxSendBacks = 4

// secondPassText sends a professional whose plan is all answered back over
// the same ground the way a careful person checks their own work.
const secondPassText = "Your plan is answered. Now a careful person's second pass over the same screens and code: do each flow a second time; take the other branch (cancel instead of save, the wrong input, the empty one, the second tap); leave and come back; go back after an error; try the smallest size your rules name. Number what you try as more PLAN lines and mark them the same way. Add a FINDING: for anything that would trip up the person you are, however small."

// coinUnit is what one coin buys: a minute. The tests shrink it.
func coinUnit() time.Duration {
	if d, err := time.ParseDuration(os.Getenv("ADMINISTRATION_COIN")); err == nil && d > 0 {
		return d
	}
	return time.Minute
}

// chromiumSandbox is always on, except inside the test suite's private
// namespace (tests/isolated.sh), where Chromium can't nest its own.
func chromiumSandbox() bool {
	return !(os.Getenv("ADMINISTRATION_TEST_ISOLATED") == "1" && os.Getenv("ADMINISTRATION_TEST_NO_CHROMIUM_SANDBOX") == "1")
}

func secret(name string) bool {
	for _, g := range secretGlobs {
		if ok, _ := filepath.Match(g, name); ok {
			return true
		}
	}
	return false
}

// absRule is a path as Claude Code's permission rules spell an absolute one.
func absRule(p string) string { return "/" + filepath.Clean(p) }

// sandboxedCommands is what a professional may run in the OS sandbox: the
// commands of each sandboxed equipment it uses, a relative path meaning one
// inside that equipment's folder in the room (what its setup built there).
func sandboxedCommands(c *contract, ci *castItem, room string) []string {
	var out []string
	seen := map[string]bool{}
	for _, u := range ci.Uses {
		eq, err := checkEquipment(c.Project, u)
		if err != nil || eq.Server != "sandboxed" {
			continue
		}
		dir := filepath.Join(room, "equipment", u)
		for _, cmd := range eq.Commands {
			if checkCommand(cmd) != nil {
				continue
			}
			if strings.Contains(cmd, "/") && !filepath.IsAbs(cmd) {
				if cmd = filepath.Join(dir, cmd); !within(cmd, dir) {
					continue
				}
			}
			if !seen[cmd] {
				seen[cmd] = true
				out = append(out, cmd)
			}
		}
	}
	return out
}

// sealSandboxed checks what each sandboxed equipment's setup built in the
// room and makes it read-only: a link out of its folder would make a listed
// command something else, and nothing in the room may rewrite what it's
// allowed to run.
func sealSandboxed(c *contract, ci *castItem, room string) error {
	for _, u := range ci.Uses {
		if eq, err := checkEquipment(c.Project, u); err != nil || eq.Server != "sandboxed" {
			continue
		}
		dir := filepath.Join(room, "equipment", u)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if err := checkAppLinks(dir); err != nil {
			return err
		}
		_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.Type()&fs.ModeSymlink == 0 {
				if info, err := d.Info(); err == nil {
					_ = os.Chmod(p, info.Mode().Perm()&^0o222)
				}
			}
			return nil
		})
	}
	return nil
}

// otherRooms is every other room that exists as this one starts: the other
// professionals' on this contract and every other contract's. (Denying the
// rooms folder and allowing this room back leaves the room's own mounts
// read-only under the sandbox, which then can't start; so they're listed.)
func otherRooms(room string) []string {
	if room == "" {
		return nil
	}
	var out []string
	contract := filepath.Dir(room)
	rooms := filepath.Dir(contract)
	cs, _ := os.ReadDir(rooms)
	for _, c := range cs {
		if !c.IsDir() {
			continue
		}
		if filepath.Join(rooms, c.Name()) != contract {
			out = append(out, filepath.Join(rooms, c.Name()))
			continue
		}
		ps, _ := os.ReadDir(contract)
		for _, p := range ps {
			if p.IsDir() && filepath.Join(contract, p.Name()) != room {
				out = append(out, filepath.Join(contract, p.Name()))
			}
		}
	}
	return out
}

func checkAppLinks(app string) error {
	return filepath.WalkDir(app, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.Type()&fs.ModeSymlink == 0 {
			return err
		}
		target, err := filepath.EvalSymlinks(p)
		if err != nil {
			return fmt.Errorf("the build has a broken link %s", p)
		}
		ra, _ := filepath.EvalSymlinks(app)
		if !within(target, ra) {
			return refusal{fmt.Sprintf("the build links out of the room (%s -> %s); copy files, not links (cp -RL)", p, target)}
		}
		return nil
	})
}

func sortedRoles(r map[string]role) []string {
	names := make([]string, 0, len(r))
	for k := range r {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}
