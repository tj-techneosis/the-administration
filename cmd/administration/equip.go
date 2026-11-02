package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Equipment (docs/equipment.md): what a user works through when the thing
// under test isn't the web app. The project's hooks start a fresh one for
// each room and print what the server needs to find it (KEY=VALUE lines);
// the professional gets the server's declared tools and nothing else.

// eqVars are what an equipment's setup printed, by equipment name.
type eqVars map[string]map[string]string

// makeRoom is a room with no web app in it: the folders and ports, for a
// user who works only through equipment.
func makeRoom(c *contract, ci *castItem, pd string, s *status, save func()) error {
	rooms, err := roomsDir()
	if err != nil {
		return err
	}
	room := roomPath(rooms, c, ci.Name)
	for _, d := range []string{"app", "desk", "tmp"} {
		if err := os.MkdirAll(filepath.Join(room, d), 0o700); err != nil {
			return err
		}
	}
	names := roomPorts(c.Project)
	ps, err := freePorts(len(names))
	if err != nil {
		return err
	}
	var kv []string
	for i, n := range names {
		kv = append(kv, fmt.Sprintf("%s=%d", n, ps[i]))
	}
	s.Room, s.Ports = room, ps
	save()
	return os.WriteFile(filepath.Join(pd, "ports"), []byte(strings.Join(kv, ",")+"\n"), 0o600)
}

var eqLine = regexp.MustCompile(`^([A-Z][A-Z0-9_]{0,40})=(.*)$`)

// startEquipment runs each piece's setup, in order, as the person and
// outside any wall, like the house's setup hook.
func startEquipment(ctx context.Context, c *contract, ci *castItem, pd, tag string, s *status, save func()) (eqVars, error) {
	vars := eqVars{}
	for _, name := range ci.Uses {
		eq, err := checkEquipment(c.Project, name)
		if err != nil {
			return nil, err
		}
		v := map[string]string{"room": s.Room, "port": strconv.Itoa(eqPort(s))}
		if s.BuildArtifact != "" {
			v["build"] = s.BuildArtifact
			v["build_commit"], _, _ = strings.Cut(s.BuildCommit, "-work-")
		}
		if eq.Server == "sandboxed" {
			// Where its setup builds the tool.
			if err := os.MkdirAll(filepath.Join(s.Room, "equipment", name), 0o700); err != nil {
				return nil, err
			}
		}
		if eq.Setup != "" {
			hook, _ := projectProgram(c.Project, eq.Setup)
			logf, err := os.OpenFile(filepath.Join(pd, "equipment-"+name+".log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			if err != nil {
				return nil, err
			}
			var out bytes.Buffer
			sctx, cancel := context.WithTimeout(ctx, setupTimeout())
			cmd := exec.Command(hook)
			cmd.Dir = c.Project
			cmd.Env = append(equipmentEnv(c, ci, name, s, v, tag), buildEnv(s)...)
			cmd.Stdout, cmd.Stderr = io.MultiWriter(&out, logf), logf
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			if err := cmd.Start(); err != nil {
				cancel()
				logf.Close()
				return nil, fmt.Errorf("equipment %s: %v", name, err)
			}
			s.Groups = append(s.Groups, cmd.Process.Pid)
			save()
			err = waitGroup(sctx, cmd)
			timedOut := sctx.Err() == context.DeadlineExceeded
			cancel()
			logf.Close()
			if err != nil {
				if timedOut {
					return nil, fmt.Errorf("equipment %s: setup took longer than %s; see %s", name, setupTimeout(), filepath.Base(logf.Name()))
				}
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				return nil, fmt.Errorf("equipment %s: setup failed (%v); see %s", name, err, filepath.Base(logf.Name()))
			}
			sc := bufio.NewScanner(&out)
			for sc.Scan() {
				if m := eqLine.FindStringSubmatch(strings.TrimSpace(sc.Text())); m != nil {
					shared, _ := eq.sharedHosts()
					hit, err := equipmentGround(name, m[1], m[2], shared)
					if err != nil {
						return nil, err
					}
					if hit != "" && !slices.Contains(s.Shared, name+": "+hit) {
						s.Shared = append(s.Shared, name+": "+hit)
						save()
					}
					v[m[1]] = m[2]
				}
			}
		}
		// What build the room runs: as its setup said, or the build hook's.
		b := buildFromVars(name, v)
		b.withWork = hookBuildNamed(s, b.Commit)
		if b.Commit == "" && s.BuildCommit != "" && eq.Setup != "" {
			// Taken on trust: the setup was handed the hook's build, and is
			// held to have installed it.
			b.From, b.Commit, b.withWork = name+" (build hook)", c.want(), true
		}
		if b.Commit != "" || b.App != "" || eq.Server == "screen" || eq.Server == "playwright" {
			b, err = checkBuild(c, b)
			addBuild(s, b)
			save()
			if err != nil {
				return nil, err
			}
		}
		vars[name] = v
	}
	return vars, nil
}

// buildEnv is what a room's hooks hear of the build hook's build: where it
// is, its commit, and the uncommitted work's hash when it was built with
// some (ADMINISTRATION_BUILD_WORK, as the hook heard it).
func buildEnv(s *status) []string {
	if s.BuildArtifact == "" {
		return nil
	}
	commit, work, ok := strings.Cut(s.BuildCommit, "-work-")
	env := []string{"ADMINISTRATION_BUILD=" + s.BuildArtifact, "ADMINISTRATION_BUILD_COMMIT=" + commit}
	if ok {
		env = append(env, "ADMINISTRATION_BUILD_WORK=work-"+work)
	}
	return env
}

// hookBuildNamed says whether a reported build names the build hook's,
// made with the uncommitted work: a setup that installs
// $ADMINISTRATION_BUILD and echoes $ADMINISTRATION_BUILD_COMMIT reports
// the bare commit, and its build has the work all the same.
func hookBuildNamed(s *status, reported string) bool {
	commit, _, ok := strings.Cut(s.BuildCommit, "-work-")
	got := strings.ToLower(strings.TrimSpace(reported))
	return ok && s.BuildArtifact != "" && len(got) >= 7 && hexRE.MatchString(got) && strings.HasPrefix(commit, got)
}

// equipmentGround holds what an equipment's setup prints to Rule 1, as a
// room's manifest is held: data is seed or a local copy, and an address is
// on this machine. An address is a URL, a database string (jdbc:..., a DSN
// with user@host), or a bare host:port or IP; 10.0.2.2 is this machine as
// an Android emulator sees it. It can't see what a built app talks to; the
// setup says, or the Director's plan and the person's review must.
//
// A shared non-production server the equipment declares (shared = [...] in
// roster.toml) is the one exception: it's allowed, and returned, so the
// worker can tell the professional and the summary can mark what was seen
// against it. DATA may then also be "shared".
func equipmentGround(name, key, val string, shared []string) (string, error) {
	if key == "DATA" && !roomData(val) && !(val == "shared" && len(shared) > 0) {
		return "", refusal{fmt.Sprintf("equipment %s: its setup says DATA=%s; equipment works on seed data or a local copy (\"seed\" or \"local\"), or \"shared\" with a shared server it declares, never production (no business on consecrated ground)", name, val)}
	}
	hit := ""
	for _, h := range addressHosts(val) {
		if localHost(h) {
			continue
		}
		if s := sharedMatch(h, shared); s != "" {
			// Under a declared host is not a free pass: a name there that
			// looks like production needs declaring (and confirming) itself.
			if lh := strings.ToLower(strings.Trim(h, "[]")); lh != s && prodLabel.MatchString(lh) {
				return "", refusal{fmt.Sprintf("equipment %s: its setup points %s at %s, under the shared %s but named like production; if it isn't, declare it in shared and not_production itself (no business on consecrated ground)", name, key, h, s)}
			}
			hit = s
			continue
		}
		hint := "equipment works with the room's own app or a local stand-in"
		if len(shared) == 0 {
			hint += ", or a shared test server it declares (shared = [\"" + strings.ToLower(h) + "\"] in roster.toml, never production)"
		} else {
			hint += ", or the shared servers it declares (" + strings.Join(shared, ", ") + ")"
		}
		return "", refusal{fmt.Sprintf("equipment %s: its setup points %s at %s, which isn't on this machine; %s (no business on consecrated ground)", name, key, h, hint)}
	}
	return hit, nil
}

// sharedMatch is the declared shared host h is, or one it's under.
func sharedMatch(h string, shared []string) string {
	h = strings.ToLower(strings.Trim(h, "[]"))
	for _, s := range shared {
		if h == s || strings.HasSuffix(h, "."+s) {
			return s
		}
	}
	return ""
}

var (
	bareHostPort = regexp.MustCompile(`^\[?([A-Za-z0-9.:-]+?)\]?:(\d{1,5})(/.*)?$`)
	// A key=value database string: host=, hostaddr=, server=, data source=.
	dsnHost = regexp.MustCompile(`(?i)\b(?:host|hostaddr|server|data source)\s*=\s*['"]?([A-Za-z0-9.:\[\],-]+)`)
)

// addressHosts is every host a value names: as a URL (after any jdbc: or
// quotes; a URL's user@host is the URL's), a key=value database string,
// host:port or a bare IP. Not a bare name or an email: a setup prints
// those for other reasons (a seed login, a device serial).
func addressHosts(val string) []string {
	v := strings.Trim(strings.TrimSpace(val), `"'`)
	var hs []string
	// A value can list several (ORIGINS=a,b): each is checked.
	for _, t := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		t = strings.TrimPrefix(strings.Trim(t, `"'`), "jdbc:")
		if u, err := url.Parse(t); err == nil && u.Scheme != "" && u.Host != "" {
			hs = append(hs, u.Hostname())
		} else if strings.HasPrefix(t, "//") {
			if u, err := url.Parse("x:" + t); err == nil && u.Host != "" {
				hs = append(hs, u.Hostname())
			}
		} else if m := bareHostPort.FindStringSubmatch(t); m != nil && hostLike(m[1]) {
			hs = append(hs, m[1])
		} else if ip := net.ParseIP(t); ip != nil {
			hs = append(hs, t)
		}
	}
	for _, m := range dsnHost.FindAllStringSubmatch(v, -1) {
		for _, h := range strings.Split(m[1], ",") { // libpq lists hosts with commas
			if h != "" {
				hs = append(hs, h)
			}
		}
	}
	return hs
}

// hostLike: an IP, localhost, or a dotted name with a letter in it, so a
// time (10:30:00) or a version (1.2:3) isn't read as an address.
func hostLike(h string) bool {
	return net.ParseIP(h) != nil || h == "localhost" || strings.Contains(h, ".") && strings.ContainsAny(strings.ToLower(h), "abcdefghijklmnopqrstuvwxyz")
}

// localHost: a loopback name or address, any address of this machine's
// (0.0.0.0, ::), or the name a device or VM on it uses for it.
func localHost(h string) bool {
	h = strings.ToLower(strings.Trim(h, "[]"))
	switch h {
	case "localhost", "10.0.2.2", // the Android emulator's name for this machine
		"host.docker.internal", "gateway.docker.internal", "host.containers.internal", "host.lima.internal":
		return true
	}
	if strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && (ip.IsLoopback() || ip.IsUnspecified())
}

// eqPort is a fresh port the equipment may listen on, kept in the status so
// the leftover check after teardown knows it.
func eqPort(s *status) int {
	ps, err := freePorts(1)
	if err != nil {
		return 0
	}
	s.Ports = append(s.Ports, ps[0])
	return ps[0]
}

func equipmentEnv(c *contract, ci *castItem, name string, s *status, v map[string]string, tag string) []string {
	env := append(tagEnv(setupEnv(c.Project), tag+"/setup"),
		"ADMINISTRATION_CONTRACT="+c.ID,
		"ADMINISTRATION_PROFESSIONAL="+ci.Name,
		"ADMINISTRATION_MAIN="+c.Project,
		"ADMINISTRATION_HOUSE="+contractHouse(c),
		"ADMINISTRATION_ROOM="+s.Room,
		"ADMINISTRATION_EQUIPMENT="+name,
		// Where a sandboxed equipment's setup puts the build its commands run.
		"ADMINISTRATION_EQUIPMENT_DIR="+filepath.Join(s.Room, "equipment", name),
		"ADMINISTRATION_PORT="+v["port"],
		// The emulator console ports kept for rooms (docs/equipment.md,
		// Room devices apart from yours): a device's setup claims one
		// here, and the person's own development devices stay out of it.
		"ADMINISTRATION_DEVICE_PORTS="+devicePorts(),
	)
	if c.ShowBrowsers {
		env = append(append(env, "ADMINISTRATION_SHOW=1"), displayEnv()...)
	}
	return env
}

// stopEquipment runs each piece's teardown; what the setups left running
// carries the room's setup tag and is cleared with the rest of the room.
func stopEquipment(c *contract, ci *castItem, pd, tag string, s *status) {
	for _, name := range ci.Uses {
		eq, err := checkEquipment(c.Project, name)
		if err != nil || eq.Teardown == "" {
			continue
		}
		hook, _ := projectProgram(c.Project, eq.Teardown)
		logf, _ := os.OpenFile(filepath.Join(pd, "equipment-"+name+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		ctx, cancel := context.WithTimeout(context.Background(), teardownLimit)
		cmd := exec.Command(hook)
		cmd.Dir = c.Project
		cmd.Env = equipmentEnv(c, ci, name, s, map[string]string{"port": ""}, tag)
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
}

var eqVar = regexp.MustCompile(`\{([A-Za-z][A-Za-z0-9_]*)\}`)

func fill(arg string, v map[string]string) (string, error) {
	var missing string
	out := eqVar.ReplaceAllStringFunc(arg, func(m string) string {
		k := m[1 : len(m)-1]
		if val, ok := v[k]; ok {
			return val
		}
		missing = k
		return m
	})
	if missing != "" {
		return "", fmt.Errorf("{%s} in the args, and the setup didn't print %s=", missing, missing)
	}
	return out, nil
}

// equipmentServers are the MCP servers for a user's equipment, and the
// tools it may call on them.
func equipmentServers(c *contract, ci *castItem, room string, vars eqVars, node string) (map[string]any, []string, error) {
	servers := map[string]any{}
	var allowed []string
	for _, name := range ci.Uses {
		eq, err := checkEquipment(c.Project, name)
		if err != nil {
			return nil, nil, err
		}
		args := eq.Args
		if c.ShowBrowsers {
			args = append(append([]string{}, args...), eq.ShowArgs...)
		}
		var filled []string
		for _, a := range args {
			f, err := fill(a, vars[name])
			if err != nil {
				return nil, nil, fmt.Errorf("equipment %s: %v", name, err)
			}
			filled = append(filled, f)
		}
		if eq.Server == "sandboxed" {
			continue // a shell in the OS sandbox, not a server: see sandboxedCommands
		}
		key := "eq_" + strings.ReplaceAll(name, "-", "_")
		tools := eq.Tools
		switch eq.Server {
		case "playwright":
			servers[key] = map[string]any{"command": node, "args": append([]string{playwrightCLI()}, filled...), "env": blankClaudeEnv()}
			if seesScreen(ci) {
				// A window seen the way the browser is: the screen in front,
				// pictures and coordinates, no text of the page.
				cfg := filepath.Join(room, "equipment", name+"-screen.json")
				if err := os.MkdirAll(filepath.Dir(cfg), 0o700); err != nil {
					return nil, nil, err
				}
				if err := writeJSON(cfg, map[string]any{
					"capabilities": []string{"core", "core-input", "core-navigation", "core-tabs", "vision"},
					"snapshot":     map[string]any{"mode": "none"}}); err != nil {
					return nil, nil, err
				}
				self, err := os.Executable()
				if err != nil {
					return nil, nil, err
				}
				// The same desk files as the browser's, for a window's own
				// file picker.
				files := ""
				if isDir(deskDir(room)) {
					files = deskDir(room)
				}
				servers[key] = map[string]any{"command": self, "args": append([]string{"_screen", "--files", files, node, playwrightCLI(), "--config", cfg}, filled...)}
				tools = windowScreenTools(eq.Tools)
			} else if ci.Kind == "user" {
				// A screen-reader user's window is behind the screen too, as
				// its browser is: files only from the desk, named and never
				// pathed, and only web pages.
				self, err := os.Executable()
				if err != nil {
					return nil, nil, err
				}
				files := ""
				if isDir(deskDir(room)) {
					files = deskDir(room)
				}
				servers[key] = map[string]any{"command": self, "args": append([]string{"_screen", "--reader", "--files", files, node, playwrightCLI()}, filled...)}
			}
		case "screen":
			self, err := os.Executable()
			if err != nil {
				return nil, nil, err
			}
			drv, _ := projectProgram(c.Project, eq.Driver)
			dargs := append([]string{"_drive", drv}, filled...)
			if len(eq.Keys) > 0 {
				dargs = append(append(dargs, "--"), eq.Keys...)
			}
			// The driver is the person's code, run outside the wall: it gets
			// the project's passed environment (where the SDK is) and what
			// every piece's setup printed.
			env := eqEnv(vars)
			for _, kv := range setupEnv(c.Project) {
				if k, v, ok := strings.Cut(kv, "="); ok {
					env[k] = v
				}
			}
			// Where it says the device is lost, for the worker to record.
			env["ADMINISTRATION_EQUIPMENT_LOST"] = filepath.Join(room, "equipment-lost")
			env["ADMINISTRATION_HOUSE"] = contractHouse(c)
			if eq.Restart != "" {
				// The restart hook hears what the setup did, so it can boot
				// the same device again and put the same build back on it.
				hook, _ := projectProgram(c.Project, eq.Restart)
				env["ADMINISTRATION_EQUIPMENT_RESTART"] = hook
				// It runs where the setup hook does, not in the room's desk.
				env["ADMINISTRATION_EQUIPMENT_RESTART_DIR"] = hookDir(c)
				for k, v := range map[string]string{"ADMINISTRATION_CONTRACT": c.ID, "ADMINISTRATION_PROFESSIONAL": ci.Name,
					"ADMINISTRATION_MAIN": c.Project, "ADMINISTRATION_ROOM": room, "ADMINISTRATION_EQUIPMENT": name,
					"ADMINISTRATION_EQUIPMENT_DIR": filepath.Join(room, "equipment", name), "ADMINISTRATION_PORT": vars[name]["port"],
					"ADMINISTRATION_BUILD": vars[name]["build"], "ADMINISTRATION_BUILD_COMMIT": vars[name]["build_commit"],
					"ADMINISTRATION_DEVICE_PORTS": devicePorts()} {
					if v != "" {
						env[k] = v
					}
				}
				// The setup's window or none: a restart boots the device as
				// the setup did.
				if c.ShowBrowsers {
					env["ADMINISTRATION_SHOW"] = "1"
					for _, kv := range displayEnv() {
						if k, v, ok := strings.Cut(kv, "="); ok {
							env[k] = v
						}
					}
				}
			}
			servers[key] = map[string]any{"command": self, "args": dargs, "env": env}
			tools = driveToolNames()
		case "commands":
			self, err := os.Executable()
			if err != nil {
				return nil, nil, err
			}
			cargs := []string{"_equip", filepath.Join(room, "desk")}
			tools = nil
			for _, cmd := range eq.Commands {
				abs, _ := projectProgram(c.Project, cmd)
				cargs = append(cargs, abs)
				tools = append(tools, commandTool(abs))
			}
			// What every piece's setup printed, so a declared program can
			// reach the rest of the room's equipment (a drop tool needs the
			// window's debug port): EQ_<EQUIPMENT>_<KEY>.
			servers[key] = map[string]any{"command": self, "args": cargs, "env": eqEnv(vars)}
		default:
			abs, _ := projectProgram(c.Project, eq.Server)
			servers[key] = map[string]any{"command": abs, "args": filled, "env": blankClaudeEnv()}
		}
		for _, t := range tools {
			allowed = append(allowed, "mcp__"+key+"__"+t)
		}
	}
	return servers, allowed, nil
}

// eqEnv is what every piece's setup printed, as EQ_<EQUIPMENT>_<KEY>.
func eqEnv(vars eqVars) map[string]string {
	env := map[string]string{}
	for eqName, v := range vars {
		for k, val := range v {
			env["EQ_"+strings.ToUpper(strings.ReplaceAll(eqName, "-", "_"))+"_"+strings.ToUpper(k)] = val
		}
	}
	return env
}

// windowScreenTools are what a screen user gets on a window reached
// through the browser server: a person's controls and the screen's own,
// and the ones that move it elsewhere only if the project declared them.
func windowScreenTools(declared []string) []string {
	elsewhere := []string{"browser_navigate", "browser_navigate_back", "browser_resize", "browser_tabs", "browser_emulate_media", "browser_close"}
	var out []string
	for _, t := range screenToolNames() {
		if slices.Contains(elsewhere, t) && !slices.Contains(declared, t) {
			continue
		}
		out = append(out, t)
	}
	return out
}

// commandTool is the tool the command server offers for one program.
func commandTool(path string) string {
	b := regexp.MustCompile(`[^A-Za-z0-9_]`).ReplaceAllString(filepath.Base(path), "_")
	return "run_" + b
}

// cmdEquip is the house's command server: an MCP server, run outside the
// professional's wall, with one tool per declared program. A call runs that
// program with the arguments given, as a list (never a shell), in the
// room's desk, and returns what it printed. It's how a user reaches a
// wrapper the person wrote (one that runs a tool in a VM, say) without
// being handed a shell, or the sandbox stopping the wrapper.
func cmdEquip(args []string) error {
	if len(args) < 2 {
		return usageError{"_equip DESK PROGRAM..."}
	}
	desk, progs := args[0], args[1:]
	dropClaudeEnv()
	byTool := map[string]string{}
	for _, p := range progs {
		byTool[commandTool(p)] = p
	}
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 1<<24)
	out := json.NewEncoder(os.Stdout)
	for in.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name      string `json:"name"`
				Arguments struct {
					Args []string `json:"args"`
				} `json:"arguments"`
			} `json:"params"`
		}
		if json.Unmarshal(in.Bytes(), &req) != nil || req.ID == nil {
			continue // a notification, or nothing we read
		}
		reply := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		switch req.Method {
		case "initialize":
			reply["result"] = map[string]any{"protocolVersion": "2024-11-05",
				"capabilities": map[string]any{"tools": map[string]any{}},
				"serverInfo":   map[string]any{"name": "administration-equipment", "version": version}}
		case "tools/list":
			var tools []any
			for t, p := range byTool {
				tools = append(tools, map[string]any{"name": t,
					"description": "Run " + filepath.Base(p) + " with these arguments (a list, no shell). Returns its exit code and output.",
					"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
						"args": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}}})
			}
			reply["result"] = map[string]any{"tools": tools}
		case "tools/call":
			p, ok := byTool[req.Params.Name]
			if !ok {
				reply["error"] = map[string]any{"code": -32602, "message": "no tool " + req.Params.Name}
				break
			}
			text, failed := runDeclared(p, req.Params.Arguments.Args, desk)
			reply["result"] = map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "isError": failed}
		case "ping":
			reply["result"] = map[string]any{}
		default:
			reply["error"] = map[string]any{"code": -32601, "message": "no method " + req.Method}
		}
		if err := out.Encode(reply); err != nil {
			return err
		}
	}
	return in.Err()
}

const equipOutputCap = 64 << 10

// declaredLimit is how long one call of a declared program may take.
var declaredLimit = 10 * time.Minute

// runDeclared runs one declared program in its own process group, so the
// limit ends what it started too, and a child left holding its output
// (a VM, a daemon a wrapper starts) holds the call only a few seconds past
// the program's end: otherwise every later call on this server would wait
// behind it.
func runDeclared(prog string, args []string, desk string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), declaredLimit)
	defer cancel()
	cmd := exec.CommandContext(ctx, prog, args...)
	cmd.Dir = desk
	cmd.Env = os.Environ() // this server's, Claude Code's own taken out
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Sprintf("it ran past %s and was stopped\n%s", declaredLimit, capOutput(buf.String())), true
	}
	if errors.Is(err, exec.ErrWaitDelay) {
		// It ended; what it left behind held the output past the delay.
		err = nil
		if code := cmd.ProcessState.ExitCode(); code != 0 {
			return fmt.Sprintf("exit %d\n%s", code, capOutput(buf.String())), true
		}
	}
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		return "could not run it: " + err.Error(), true
	}
	return fmt.Sprintf("exit %d\n%s", code, capOutput(buf.String())), code != 0
}

func capOutput(o string) string {
	if len(o) > equipOutputCap {
		o = o[:equipOutputCap] + "\n… (cut)"
	}
	return o
}

// devicePorts is the range of emulator console ports kept for rooms, as
// FIRST-LAST: 5620-5680 (even ones; each emulator also takes the odd one
// above it for adb), or ADMINISTRATION_DEVICE_PORTS. Place refuses a range
// that can't be one (devicePortsBad), so one here is passed over for the
// default.
func devicePorts() string {
	if v := os.Getenv("ADMINISTRATION_DEVICE_PORTS"); v != "" && devicePortsBad(v) == "" {
		return v
	}
	return "5620-5680"
}

// devicePortsBad says what's wrong with a range of console ports: it's
// backwards, or starts on an odd port (an emulator's console port is even),
// or isn't FIRST-LAST at all. "" when it's a range.
func devicePortsBad(v string) string {
	m := regexp.MustCompile(`^(\d{4,5})-(\d{4,5})$`).FindStringSubmatch(v)
	if m == nil {
		return fmt.Sprintf("ADMINISTRATION_DEVICE_PORTS=%q isn't FIRST-LAST (5620-5680)", v)
	}
	first, _ := strconv.Atoi(m[1])
	last, _ := strconv.Atoi(m[2])
	switch {
	case first%2 != 0:
		return fmt.Sprintf("ADMINISTRATION_DEVICE_PORTS=%q starts on an odd port; an emulator's console port is even, and the odd one above it is its adb port", v)
	case first > last || last > 65534:
		return fmt.Sprintf("ADMINISTRATION_DEVICE_PORTS=%q isn't a range of ports, first to last", v)
	}
	return ""
}
