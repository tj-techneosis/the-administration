package main

import (
	"bufio"
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
	go s.readChild(fromChild)
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 1<<26)
	var wg sync.WaitGroup
	for in.Scan() {
		var m rpc
		if json.Unmarshal(in.Bytes(), &m) != nil {
			continue
		}
		if m.Method == "tools/call" && m.ID != nil {
			// Its own calls can take a while (typing); the rest go on.
			wg.Add(1)
			go func() { defer wg.Done(); s.call(m) }()
			continue
		}
		s.toServer(in.Bytes())
	}
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
					kept = append(kept, readerFileTools...)
				} else {
					kept = append(kept, screenOwn...)
				}
				m.Result, _ = json.Marshal(map[string]any{"tools": kept})
			}
		}
		s.toClient(m)
	}
}

// ask calls a tool on the server and waits for its answer.
func (s *screen) ask(tool string, args map[string]any) rpc {
	s.wmu.Lock()
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
		for _, r := range text {
			key := string(r)
			switch r {
			case '\n':
				key = "Enter"
			case '\t':
				key = "Tab"
			}
			if last = s.ask("browser_press_key", map[string]any{"key": key}); last.Error != nil {
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
			// Aimed as a screen-reader user aims: the element and its ref
			// from the page's snapshot, through the server's own drop.
			el, _ := p.Arguments["element"].(string)
			ref, _ := p.Arguments["ref"].(string)
			if ref == "" {
				refuse("say where to drop: the element and its ref, from the page's snapshot")
				return
			}
			reply(s.ask("browser_drop", map[string]any{"element": el, "ref": ref, "paths": paths}))
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
	{"name": "browser_drop_file", "description": "Drag files from your desk and drop them on an element of the page, as a person drags from their own computer onto a drop area. element and ref are the drop area's, from the page's snapshot.",
		"inputSchema": map[string]any{"type": "object", "required": []string{"names", "element", "ref"}, "properties": map[string]any{
			"names":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"element": map[string]any{"type": "string"}, "ref": map[string]any{"type": "string"}}}},
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
