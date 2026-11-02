package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// The screen: what a user who sees the screen talks to instead of the
// browser server itself. It runs the pinned browser server as its child and
// passes on only a person's controls (look at the screen, point, click,
// scroll, press keys), adds typing on the real keyboard (the server keeps
// its own for skills), and turns the house's two checks into tools of their
// own with the scripts fixed inside. Nothing that reads or runs script in
// the page gets through: a person at a screen has nothing like it.

// screenPassed are the browser server's tools a person's controls map to.
var screenPassed = []string{
	"browser_navigate", "browser_navigate_back", "browser_take_screenshot",
	"browser_mouse_click_xy", "browser_mouse_move_xy", "browser_mouse_wheel", "browser_mouse_drag_xy",
	"browser_press_key", "browser_resize", "browser_tabs", "browser_wait_for", "browser_handle_dialog",
	"browser_emulate_media", "browser_close",
}

// screenOwn are the screen's own tools.
var screenOwn = []map[string]any{
	{"name": "browser_type_text", "description": "Type text on the keyboard into whatever has focus (click a field first), one key at a time, as a person types. submit presses Enter after.",
		"inputSchema": map[string]any{"type": "object", "required": []string{"text"}, "properties": map[string]any{
			"text": map[string]any{"type": "string"}, "submit": map[string]any{"type": "boolean"}}}},
	{"name": "browser_choose_file", "description": "Pick files from your desk for the file picker the page has open (click its upload button first), as a person picks from their own computer. names are the files' names as your brief lists them; no names closes the picker, as Cancel does.",
		"inputSchema": map[string]any{"type": "object", "required": []string{"names"}, "properties": map[string]any{
			"names": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}}},
	{"name": "browser_drop_file", "description": "Drag files from your desk and drop them onto the page at x, y (pixels in the screenshot), as a person drags from their own computer onto a drop area.",
		"inputSchema": map[string]any{"type": "object", "required": []string{"names", "x", "y"}, "properties": map[string]any{
			"names": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"x":     map[string]any{"type": "number"}, "y": map[string]any{"type": "number"}}}},
	{"name": "browser_look", "description": "The person's-eye check: what appeared on the page since the last look and where it is against the window (in it, below it by N px), and how each control in the window is drawn (as a button, underlined like a link, or plain text). Run it on each screen, then after every action that should show a result.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}},
	{"name": "browser_layout_check", "description": "The house layout check: text cut off by its box, squeezed to a sliver, clipped by a fixed height, past the right edge, and actions below the fold. Each is a lead to confirm on the screenshot.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}},
}

// screenTools are the names a screen user is allowed, for its settings.
func screenToolNames() []string {
	out := append([]string{}, screenPassed...)
	for _, t := range screenOwn {
		out = append(out, t["name"].(string))
	}
	return out
}

type rpc struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

// cmdScreen is `administration _screen NODE PLAYWRIGHT_CLI ARGS...`.
func cmdScreen(args []string) error {
	files, reader := "", false
	for len(args) > 0 {
		if args[0] == "--reader" {
			reader, args = true, args[1:]
		} else if args[0] == "--files" && len(args) > 1 {
			files, args = args[1], args[2:]
		} else {
			break
		}
	}
	if len(args) < 2 {
		return usageError{"_screen [--reader] [--files DIR] NODE CLI [ARGS...]"}
	}
	dropClaudeEnv()
	child := exec.Command(args[0], args[1:]...)
	child.Stderr = os.Stderr
	toChild, err := child.StdinPipe()
	if err != nil {
		return err
	}
	fromChild, err := child.StdoutPipe()
	if err != nil {
		return err
	}
	if err := child.Start(); err != nil {
		return err
	}
	s := &screen{files: files, reader: reader, toChild: toChild, out: json.NewEncoder(os.Stdout), waiting: map[string]chan rpc{}}
	childDone := make(chan struct{})
	go func() { s.readChild(fromChild); close(childDone) }()
	clientDone := make(chan struct{})
	var wg sync.WaitGroup
	var smu sync.Mutex // stopping, and every wg.Add before it is set
	stopping := false
	go func() {
		defer close(clientDone)
		in := bufio.NewScanner(os.Stdin)
		in.Buffer(make([]byte, 1<<20), 1<<26)
		for in.Scan() {
			var m rpc
			if json.Unmarshal(in.Bytes(), &m) != nil {
				continue
			}
			if m.Method == "tools/call" {
				if m.ID == nil {
					// A call sent as a notification wants no answer, and
					// would pass the screen's checks by: it goes nowhere.
					continue
				}
				// Its own calls can take a while (typing); the rest go on.
				smu.Lock()
				if !stopping {
					wg.Add(1)
					go func() { defer wg.Done(); s.call(m) }()
				}
				smu.Unlock()
				continue
			}
			s.toServer(in.Bytes())
		}
	}()
	// The screen ends with either side: a browser server that has gone
	// answers nothing more, and its client is told so by the screen ending
	// (the calls the screen was waiting on are failed first).
	select {
	case <-clientDone:
	case <-childDone:
	}
	smu.Lock()
	stopping = true
	smu.Unlock()
	wg.Wait()
	toChild.Close()
	return child.Wait()
}

type screen struct {
	files   string     // the room's desk files, the only ones it hands a page
	// A screen-reader user's: every browser tool passes (but the ones that
	// read any path), plus the desk's, with a drop aimed by the page's ref.
	reader  bool
	mu      sync.Mutex // the writes to each side
	toChild io.Writer
	out     *json.Encoder
	wmu     sync.Mutex // the waiting map and next
	waiting map[string]chan rpc
	next    int
	listIDs sync.Map // ids of the client's tools/list requests
	// The browser server's drop arguments that aim it, from its schema,
	// once a tools list has come through (under wmu).
	dropKeys []string
	// gone is set (under wmu) once the browser server's output has ended:
	// nothing will answer a call after that.
	gone bool
}

func (s *screen) toServer(b []byte) {
	var m rpc
	if json.Unmarshal(b, &m) == nil && m.Method == "tools/list" && m.ID != nil {
		s.listIDs.Store(string(m.ID), true)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.toChild.Write(append(append([]byte{}, b...), '\n'))
}

func (s *screen) toClient(v any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.out.Encode(v)
}

// readChild passes the server's messages on, except the answers to the
// screen's own calls, and filters every tools list down to a person's
// controls plus the screen's own.
func (s *screen) readChild(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	for sc.Scan() {
		var m rpc
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		m.Result = ownHints(m.Result)
		id := string(m.ID)
		s.wmu.Lock()
		ch, mine := s.waiting[id]
		delete(s.waiting, id)
		s.wmu.Unlock()
		if mine {
			ch <- m
			continue
		}
		if _, isList := s.listIDs.LoadAndDelete(id); isList && m.Result != nil {
			var res struct {
				Tools []map[string]any `json:"tools"`
			}
			if json.Unmarshal(m.Result, &res) == nil {
				var kept []map[string]any
				for _, t := range res.Tools {
					name, _ := t["name"].(string)
					if s.reader && !slices.Contains(deniedBrowserTools, name) || !s.reader && slices.Contains(screenPassed, name) {
						kept = append(kept, t)
					}
				}
				if s.reader {
					kept = append(kept, readerTools(res.Tools)...)
					s.wmu.Lock()
					s.dropKeys = dropAimKeys(res.Tools)
					s.wmu.Unlock()
				} else {
					kept = append(kept, screenOwn...)
				}
				m.Result, _ = json.Marshal(map[string]any{"tools": kept})
			}
		}
		s.toClient(m)
	}
	// The server has gone: every call the screen is waiting on is failed
	// now, or it would wait for ever, and the screen with it.
	s.wmu.Lock()
	s.gone = true
	for id, ch := range s.waiting {
		ch <- serverGone()
		delete(s.waiting, id)
	}
	s.wmu.Unlock()
}

// isError says whether a tool's answer is a failure it reports itself.
func isError(r rpc) bool {
	var res struct {
		IsError bool `json:"isError"`
	}
	return r.Result != nil && json.Unmarshal(r.Result, &res) == nil && res.IsError
}

// resultText is the text of a tool's answer.
func resultText(r rpc) string {
	var res struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	_ = json.Unmarshal(r.Result, &res)
	var parts []string
	for _, c := range res.Content {
		parts = append(parts, c.Text)
	}
	return strings.Join(parts, " ")
}

// serverGone is the answer to a call the browser server will never answer.
func serverGone() rpc {
	return rpc{Error: json.RawMessage(`{"code":-32603,"message":"the browser has closed: nothing more can be done in it"}`)}
}

// ask calls a tool on the server and waits for its answer.
func (s *screen) ask(tool string, args map[string]any) rpc {
	s.wmu.Lock()
	if s.gone {
		s.wmu.Unlock()
		return serverGone()
	}
	s.next++
	id := fmt.Sprintf(`"administration-screen-%d"`, s.next)
	ch := make(chan rpc, 1)
	s.waiting[id] = ch
	s.wmu.Unlock()
	params, _ := json.Marshal(map[string]any{"name": tool, "arguments": args})
	b, _ := json.Marshal(rpc{JSONRPC: "2.0", ID: json.RawMessage(id), Method: "tools/call", Params: params})
	s.mu.Lock()
	s.toChild.Write(append(b, '\n'))
	s.mu.Unlock()
	return <-ch
}

func (s *screen) call(m rpc) {
	var p struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	_ = json.Unmarshal(m.Params, &p)
	reply := func(r rpc) { r.ID, r.JSONRPC = m.ID, "2.0"; s.toClient(r) }
	refuse := func(msg string) {
		res, _ := json.Marshal(map[string]any{"content": []any{map[string]any{"type": "text", "text": msg}}, "isError": true})
		reply(rpc{Result: res})
	}
	switch p.Name {
	case "browser_type_text":
		text, _ := p.Arguments["text"].(string)
		var last rpc
		rs := []rune(text)
		for i, r := range rs {
			key := string(r)
			switch r {
			case '\r':
				if i+1 < len(rs) && rs[i+1] == '\n' {
					continue // a Windows line end is one Enter
				}
				key = "Enter"
			case '\n':
				key = "Enter"
			case '\t':
				key = "Tab"
			}
			// A key the server couldn't press stops the typing there, and
			// says so: the rest would go in without it.
			if last = s.ask("browser_press_key", map[string]any{"key": key}); last.Error != nil || isError(last) {
				if last.Error == nil {
					refuse(fmt.Sprintf("typing stopped at character %d of %d (%q): that key couldn't be pressed (%s); the characters before it were typed", i+1, len(rs), r, oneLine(resultText(last), 200)))
					return
				}
				reply(last)
				return
			}
		}
		if submit, _ := p.Arguments["submit"].(bool); submit {
			last = s.ask("browser_press_key", map[string]any{"key": "Enter"})
		}
		if last.Result == nil && last.Error == nil {
			res, _ := json.Marshal(map[string]any{"content": []any{map[string]any{"type": "text", "text": "nothing to type"}}})
			last = rpc{Result: res}
		}
		reply(last)
	case "browser_choose_file":
		raw, ok := p.Arguments["names"].([]any)
		if !ok {
			refuse("names is a list of your desk's file names, like [\"photo.jpg\"]; an empty list closes the picker")
			return
		}
		names := stringList(raw)
		if len(names) != len(raw) {
			refuse("names is a list of file names (text)")
			return
		}
		if len(names) == 0 {
			// No names closes the picker, as Cancel does.
			reply(s.ask("browser_file_upload", map[string]any{}))
			return
		}
		paths, err := deskPaths(s.files, names)
		if err != nil {
			refuse(err.Error())
			return
		}
		r := s.ask("browser_file_upload", map[string]any{"paths": paths})
		if r.Error == nil && strings.Contains(string(r.Result), "No file chooser") {
			refuse("no file picker is open: click the page's upload button (or its file field) first, then choose")
			return
		}
		reply(r)
	case "browser_drop_file":
		raw, _ := p.Arguments["names"].([]any)
		paths, err := deskPaths(s.files, stringList(raw))
		if err != nil {
			refuse(err.Error())
			return
		}
		if s.reader {
			// Aimed as a screen-reader user aims everything, through the
			// server's own drop: its arguments as it names them (a ref once,
			// a target since), with the desk's files as its paths.
			s.wmu.Lock()
			known := s.dropKeys
			s.wmu.Unlock()
			if known == nil {
				// No tools list through yet: the names servers have used.
				known = dropAimNames
			}
			args := map[string]any{}
			for k, v := range p.Arguments {
				if dropAims(k) && slices.Contains(known, k) {
					args[k] = v
				}
			}
			if len(args) == 0 {
				refuse("say where to drop: the drop area, as the page's snapshot names it")
				return
			}
			args["paths"] = paths
			reply(s.ask("browser_drop", args))
			return
		}
		x, xok := p.Arguments["x"].(float64)
		y, yok := p.Arguments["y"].(float64)
		if !xok || !yok {
			refuse("x and y are where to drop, as numbers: pixels in the screenshot")
			return
		}
		fn, err := dropScript(paths, x, y)
		if err != nil {
			refuse(err.Error())
			return
		}
		// The server echoes the script it ran, files and all: only what the
		// drop said goes back.
		reply(resultOnly(s.ask("browser_evaluate", map[string]any{"function": fn})))
	case "browser_look":
		reply(s.ask("browser_evaluate", map[string]any{"function": lookCheck}))
	case "browser_layout_check":
		reply(s.ask("browser_evaluate", map[string]any{"function": layoutCheck}))
	default:
		if why := offTheWall(p.Name, p.Arguments); why != "" {
			refuse(why)
			return
		}
		// offTheWall writes a scheme-less address out as http://, so the
		// server can't read it as a scheme of its own.
		var whole map[string]any
		if p.Arguments != nil && json.Unmarshal(m.Params, &whole) == nil {
			whole["arguments"] = p.Arguments
			if params, err := json.Marshal(whole); err == nil {
				m.Params = params
			}
		}
		if s.reader {
			if slices.Contains(deniedBrowserTools, p.Name) {
				refuse(p.Name + " is off: files go in from your desk, with browser_choose_file and browser_drop_file")
				return
			}
			b, _ := json.Marshal(m)
			s.toServer(b)
			return
		}
		if !slices.Contains(screenPassed, p.Name) {
			refuse(p.Name + " isn't something a person at a screen has: look with browser_take_screenshot, act with the mouse and keyboard tools")
			return
		}
		// Passed on as it came; its answer finds its way back by id.
		b, _ := json.Marshal(m)
		s.toServer(b)
	}
}

// screenServer is the MCP server entry for a screen user's browser: the
// screen in front of the pinned browser server.
// readerFileTools are a screen-reader user's file tools: the same choosing,
// and a drop aimed by the page's ref, as it aims everything else.
var readerFileTools = []map[string]any{
	screenOwn[1],
	{"name": "browser_drop_file", "description": readerDropText,
		"inputSchema": map[string]any{"type": "object", "required": []string{"names", "element", "target"}, "properties": map[string]any{
			"names":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"element": map[string]any{"type": "string"}, "target": map[string]any{"type": "string"}}}},
}

const readerDropText = "Drag files from your desk and drop them on an element of the page, as a person drags from their own computer onto a drop area. Say which element as you do for a click, from the page's snapshot; names are the desk files'."

// readerTools are a screen-reader user's file tools, with the drop aimed as
// the browser server's own drop is (its schema, without the paths and data
// only the house gives, plus the desk's names): a server that renamed how
// an element is named (ref became target) broke a drop written for the old.
func readerTools(server []map[string]any) []map[string]any {
	for _, t := range server {
		if t["name"] != "browser_drop" {
			continue
		}
		schema, ok := t["inputSchema"].(map[string]any)
		props, ok2 := schema["properties"].(map[string]any)
		if !ok || !ok2 {
			break
		}
		own := map[string]any{"names": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "The desk files to drop, by name."}}
		for k, v := range props {
			if dropAims(k) {
				own[k] = v
			}
		}
		required := []string{"names"}
		if req, ok := schema["required"].([]any); ok {
			for _, r := range req {
				if r, ok := r.(string); ok && dropAims(r) {
					required = append(required, r)
				}
			}
		}
		return []map[string]any{screenOwn[1], {"name": "browser_drop_file", "description": readerDropText,
			"inputSchema": map[string]any{"type": "object", "required": required, "properties": own}}}
	}
	return readerFileTools
}

// dropAims says whether one of the browser server's drop arguments is one
// that says where to drop (a ref, a target, the element's name), as opposed
// to what. Only the names servers have used to aim pass: anything else, a
// file, a path, data or a name nobody has seen yet, stays the house's to
// give, however a later server spells it.
func dropAims(k string) bool {
	return slices.Contains(dropAimNames, k)
}

var dropAimNames = []string{"element", "ref", "target", "selector"}

// dropAimKeys is the drop arguments the server's schema lists that aim it.
func dropAimKeys(server []map[string]any) []string {
	for _, t := range server {
		if t["name"] != "browser_drop" {
			continue
		}
		schema, _ := t["inputSchema"].(map[string]any)
		props, _ := schema["properties"].(map[string]any)
		keys := []string{}
		for k := range props {
			if dropAims(k) {
				keys = append(keys, k)
			}
		}
		return keys
	}
	return nil
}

// offTheWall is why a browser tool call the screen would pass on goes no
// further, or "". The browser runs as the person: a file it writes goes
// where its filename says, and a page that isn't on the web (file:,
// view-source:, chrome:) never reaches the gate, which only sees what goes
// through a proxy. So a file is named, never pathed, and the browser only
// goes to web pages: every url argument, whatever the tool, is checked, and
// one with no scheme is written out as http:// in args, so the server can't
// take its host for a scheme. An argument the screen doesn't know (a later
// server's) is held to a plain name too, at any depth: a folder or a path in
// it goes no further.
func offTheWall(tool string, args map[string]any) string {
	if slices.Contains(deniedBrowserTools, tool) {
		return "" // refused whole, below
	}
	for k, v := range args {
		l := strings.ToLower(k)
		if l == "url" {
			u, _ := v.(string)
			if u == "" && v != nil && tool != "browser_navigate" {
				continue // a new tab with no page
			}
			page, ok := webPage(u)
			if !ok {
				return fmt.Sprintf("%s: %q isn't a web page; you reach the app's pages, on http or https (write it as http://host:port/...)", tool, u)
			}
			args[k] = page
			continue
		}
		if why := plainNames(tool, k, v, fileArg(l)); why != "" {
			return why
		}
	}
	return ""
}

// knownArgs are the browser server's arguments that never name a file or a
// page (as its tools take them in the pinned release): what an element is,
// text to type or find, keys, numbers, choices. Their values may hold a
// slash ("1/2", a date, a CSS selector) and are left as they are.
var knownArgs = []string{"element", "target", "ref", "selector", "xpath", "text", "textgone", "regex", "key", "submit",
	"accept", "prompttext", "type", "value", "values", "name", "fields", "function", "slowly", "doubleclick", "button",
	"modifiers", "clickcount", "delay", "x", "y", "deltax", "deltay", "startx", "starty", "endx", "endy", "startelement",
	"starttarget", "endelement", "endtarget", "width", "height", "time", "action", "index", "fullpage", "scale", "depth",
	"boxes", "level", "all", "static", "filter", "part", "colorscheme", "reducedmotion", "forcedcolors", "contrast",
	"media", "role", "accessiblename", "items"}

// fileArg says whether an argument is one that names a file.
func fileArg(l string) bool {
	return strings.Contains(l, "filename") || strings.HasSuffix(l, "file") || l == "files" ||
		strings.HasSuffix(l, "path") && l != "xpath" || l == "paths"
}

// plainNames is why one argument goes no further, or "": a file's name must
// be text and a plain name, and an argument the screen doesn't know may hold
// no folders or paths anywhere in it.
func plainNames(tool, k string, v any, file bool) string {
	l := strings.ToLower(k)
	if !file && slices.Contains(knownArgs, l) {
		return ""
	}
	if _, text := v.(string); file && !text {
		return fmt.Sprintf("%s: %s is a file's name, as text", tool, k)
	}
	switch v := v.(type) {
	case string:
		if v != "" && (v != filepath.Base(v) || v == ".." || v == "." || strings.ContainsAny(v, "/\\")) {
			return fmt.Sprintf("%s: %s is a plain file name, no folders (%q); what you save goes in your own folder", tool, k, v)
		}
	case map[string]any:
		for k2, v2 := range v {
			if why := plainNames(tool, k2, v2, fileArg(strings.ToLower(k2))); why != "" {
				return why
			}
		}
	case []any:
		for _, x := range v {
			if why := plainNames(tool, k, x, false); why != "" {
				return why
			}
		}
	}
	return ""
}

// webPage is the page a URL to navigate to names, if it's a web page: an
// http(s) URL as it is, or a host with no scheme (localhost:3000/x,
// [::1]:3000, my_app:3000, example.com), which is written out as http://.
// Anything else (file:, data:, javascript:, view-source:, about:) isn't.
func webPage(u string) (string, bool) {
	t := strings.TrimSpace(u)
	l := strings.ToLower(t)
	if strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://") {
		return t, !strings.ContainsAny(t, "\x00\t\r\n")
	}
	// A scheme-less address: a host, then perhaps a port of digits, then
	// the end or the start of a path, query or fragment.
	host, rest := l, ""
	if i := strings.IndexAny(l, "/?#"); i >= 0 {
		host, rest = l[:i], l[i:]
	}
	if host == "" || strings.ContainsAny(rest, "\\\x00\t\r\n") {
		return "", false
	}
	name, port := host, ""
	if strings.HasPrefix(host, "[") {
		end := strings.Index(host, "]")
		if end < 0 {
			return "", false
		}
		name, port = host[1:end], host[end+1:]
		if name == "" || strings.Trim(name, "0123456789abcdef:.") != "" {
			return "", false
		}
	} else {
		if i := strings.Index(host, ":"); i >= 0 {
			name, port = host[:i], host[i:]
		}
		if name == "" || strings.Trim(name, "abcdefghijklmnopqrstuvwxyz0123456789.-_") != "" {
			return "", false
		}
	}
	if port != "" {
		digits := strings.TrimPrefix(port, ":")
		if digits == port || digits == "" || strings.Trim(digits, "0123456789") != "" {
			return "", false
		}
	}
	return "http://" + t, true
}

// webURL says whether a URL to navigate to is a web page (see webPage).
func webURL(u string) bool {
	_, ok := webPage(u)
	return ok
}

// readerServer is a screen-reader user's browser server behind the screen,
// for its desk's files.
func readerServer(node, configPath, files string) (map[string]any, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return map[string]any{"command": self, "args": []string{"_screen", "--reader", "--files", files, node, playwrightCLI(), "--config", configPath}}, nil
}

// screenServer is the screen in front of the browser server; files is the
// room's desk files, the only ones it will hand a page.
func screenServer(node, configPath, files string) (map[string]any, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return map[string]any{"command": self, "args": []string{"_screen", "--files", files, node, playwrightCLI(), "--config", configPath}}, nil
}

func stringList(l []any) []string {
	var out []string
	for _, x := range l {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// dropLimit is how much a drop carries: the files go into the page whole.
const dropLimit = 25 << 20

// dropScript drops the desk's files on whatever is at x, y, as a drag from
// outside the page does: dragenter, dragover and drop, with the files in
// the event's DataTransfer.
func dropScript(paths []string, x, y float64) (string, error) {
	type file struct {
		Name string `json:"name"`
		Type string `json:"type"`
		Data string `json:"data"`
	}
	var files []file
	total := 0
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return "", err
		}
		if total += len(b); total > dropLimit {
			return "", fmt.Errorf("that's over %d MB to drop at once; drop fewer, or choose them through the page's file picker", dropLimit>>20)
		}
		t := mime.TypeByExtension(filepath.Ext(p))
		if t == "" {
			t = "application/octet-stream"
		}
		files = append(files, file{filepath.Base(p), t, base64.StdEncoding.EncodeToString(b)})
	}
	js, _ := json.Marshal(files)
	return fmt.Sprintf(`() => {
  const files = %s;
  const el = document.elementFromPoint(%g, %g);
  if (!el) return "nothing on the page at %g, %g";
  if (el.tagName === "IFRAME") return "that spot is inside a frame the drop can't reach from here; nothing was dropped";
  const dt = new DataTransfer();
  for (const f of files) {
    const bin = atob(f.data), bytes = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
    dt.items.add(new File([bytes], f.name, {type: f.type}));
  }
  const opts = {bubbles: true, cancelable: true, clientX: %g, clientY: %g, dataTransfer: dt};
  const where = el.tagName.toLowerCase() + (el.id ? "#" + el.id : "");
  el.dispatchEvent(new DragEvent("dragenter", opts));
  // As a browser does: a page that doesn't take the drag over a spot gets
  // no drop there, and the person's file opens in the tab instead.
  if (el.dispatchEvent(new DragEvent("dragover", opts))) {
    el.dispatchEvent(new DragEvent("dragleave", opts));
    return "the page didn't take a drop on " + where + ": a person's file would open in the tab (or be refused) instead";
  }
  el.dispatchEvent(new DragEvent("drop", opts));
  return "dropped " + files.length + " file(s) on " + where;
}`, js, x, y, x, y, x, y), nil
}

// The browser server's hints name its own tools. A page's file picker
// "can be handled by browser_file_upload", which a user here doesn't have
// (files come from the desk); told that, a user was stuck behind the
// picker for the rest of its time. The hint is turned to the tools it has.
var fileHint = []byte("can be handled by browser_file_upload")

func ownHints(res json.RawMessage) json.RawMessage {
	if res == nil || !bytes.Contains(res, fileHint) {
		return res
	}
	return bytes.ReplaceAll(res, fileHint, []byte("can be handled by browser_choose_file (a file from your desk; no names closes the picker, as Cancel does)"))
}

// resultOnly is a browser server's answer cut to its "### Result" section,
// and the page it was on, for a call whose script the server would echo.
func resultOnly(r rpc) rpc {
	if r.Result == nil {
		return r
	}
	var res struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if json.Unmarshal(r.Result, &res) != nil {
		return r
	}
	var keep []string
	for _, c := range res.Content {
		for _, sec := range strings.Split("\n"+c.Text, "\n### ") {
			if strings.HasPrefix(sec, "Result") || strings.HasPrefix(sec, "Error") || strings.HasPrefix(sec, "Page") {
				keep = append(keep, "### "+strings.TrimSpace(sec))
			}
		}
	}
	if len(keep) == 0 {
		keep = []string{"done"}
	}
	out, _ := json.Marshal(map[string]any{"content": []any{map[string]any{"type": "text", "text": strings.Join(keep, "\n")}}, "isError": res.IsError})
	r.Result = out
	return r
}
