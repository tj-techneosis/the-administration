package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
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
	if len(args) < 2 {
		return usageError{"_screen NODE CLI [ARGS...]"}
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
	s := &screen{toChild: toChild, out: json.NewEncoder(os.Stdout), waiting: map[string]chan rpc{}}
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
					if name, _ := t["name"].(string); slices.Contains(screenPassed, name) {
						kept = append(kept, t)
					}
				}
				kept = append(kept, screenOwn...)
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
	case "browser_look":
		reply(s.ask("browser_evaluate", map[string]any{"function": lookCheck}))
	case "browser_layout_check":
		reply(s.ask("browser_evaluate", map[string]any{"function": layoutCheck}))
	default:
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
func screenServer(node, configPath string) (map[string]any, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return map[string]any{"command": self, "args": []string{"_screen", node, playwrightCLI(), "--config", configPath}}, nil
}
