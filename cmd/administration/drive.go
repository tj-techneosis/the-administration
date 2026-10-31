package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The screen for equipment: a user who sees the screen gets the same
// person's controls on a phone, a VM or a desktop window as in the browser,
// whatever the project uses to reach it. The project writes a driver, one
// program that knows its own device; the Administration owns the screen in
// front of it: the tools, the pictures, what changed since the last look,
// and the scale between what the professional sees and the device's pixels.
//
// The driver is called with the equipment's args (filled from its setup)
// and then one action:
//
//	screenshot            a PNG of the whole screen on stdout
//	click X Y [HOLD_MS]   a tap or click; held that long when given
//	drag X1 Y1 X2 Y2      press, move, release
//	scroll X Y DX DY      scroll at that point (DY > 0 is down)
//	type TEXT             type it on the keyboard, into what has focus
//	key NAME              press one key (Enter, Back, Home, Tab, ...)
//
// Coordinates are the device's own pixels. A non-zero exit is a failure,
// and what it printed on stderr goes back to the professional.

var driveTools = []map[string]any{
	{"name": "screen_take_screenshot", "description": "Look at the screen: the picture comes back to you. Coordinates for the other tools are pixels in this picture.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}},
	{"name": "screen_look", "description": "Look at the screen and be told what changed since your last look: the box (x, y, width, height in the picture) where anything changed, or that nothing did. Use it after every action that should show a result.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}},
	{"name": "screen_click", "description": "Tap or click at x, y in the last picture. hold_ms holds it down (a long press).",
		"inputSchema": map[string]any{"type": "object", "required": []string{"x", "y"}, "properties": map[string]any{
			"x": map[string]any{"type": "number"}, "y": map[string]any{"type": "number"}, "hold_ms": map[string]any{"type": "number"}}}},
	{"name": "screen_drag", "description": "Press at x1, y1, move to x2, y2 and let go (a swipe on a phone).",
		"inputSchema": map[string]any{"type": "object", "required": []string{"x1", "y1", "x2", "y2"}, "properties": map[string]any{
			"x1": map[string]any{"type": "number"}, "y1": map[string]any{"type": "number"},
			"x2": map[string]any{"type": "number"}, "y2": map[string]any{"type": "number"}}}},
	{"name": "screen_scroll", "description": "Scroll at x, y by dy pixels of the picture (positive is down) and dx (positive is right).",
		"inputSchema": map[string]any{"type": "object", "required": []string{"x", "y", "dy"}, "properties": map[string]any{
			"x": map[string]any{"type": "number"}, "y": map[string]any{"type": "number"},
			"dx": map[string]any{"type": "number"}, "dy": map[string]any{"type": "number"}}}},
	{"name": "screen_type_text", "description": "Type text on the keyboard into whatever has focus (tap a field first). submit presses Enter after.",
		"inputSchema": map[string]any{"type": "object", "required": []string{"text"}, "properties": map[string]any{
			"text": map[string]any{"type": "string"}, "submit": map[string]any{"type": "boolean"}}}},
	{"name": "screen_press_key", "description": "Press one key.",
		"inputSchema": map[string]any{"type": "object", "required": []string{"key"}, "properties": map[string]any{
			"key": map[string]any{"type": "string"}}}},
	{"name": "screen_wait", "description": "Wait up to 10 seconds (for something loading or moving), then look.",
		"inputSchema": map[string]any{"type": "object", "required": []string{"seconds"}, "properties": map[string]any{
			"seconds": map[string]any{"type": "number"}}}},
}

func driveToolNames() []string {
	var out []string
	for _, t := range driveTools {
		out = append(out, t["name"].(string))
	}
	return out
}

// The picture the professional is sent fits what a model reads well; the
// screen keeps the factor to put its coordinates back on the device.
const (
	shotEdge   = 1456
	shotPixels = 1150000
)

type drive struct {
	prog  string
	args  []string
	keys  []string
	mu    sync.Mutex
	scale int         // device pixels per picture pixel, from the last picture
	last  *image.RGBA // the picture at the last look, for what changed
	// The device failing, not the professional's call: screenshots in a
	// row that failed or weren't a picture (a bad tap or an unknown key is
	// the professional's). At lostAfter it's gone, the worker is told
	// through the file, and nothing more is asked of it.
	fails    int
	lost     string
	lostFile string
	stderr   string // the driver's last stderr, for a picture that isn't one
}

// lostAfter is how many failed screenshots in a row mean the device is gone:
// a professional can't tell a dead screen from a slow one, and kept going on
// one for its whole purse.
const lostAfter = 3

// cmdDrive is `administration _drive DRIVER [ARGS...] [-- KEY...]`: the
// screen's MCP server in front of a project's driver. The keys after --
// are the ones the driver says it knows, for the tool's description.
func cmdDrive(args []string) error {
	if len(args) < 1 {
		return usageError{"_drive DRIVER [ARGS...] [-- KEY...]"}
	}
	d := &drive{prog: args[0], scale: 1, lostFile: os.Getenv("ADMINISTRATION_EQUIPMENT_LOST")}
	rest := args[1:]
	for i, a := range rest {
		if a == "--" {
			d.keys, rest = rest[i+1:], rest[:i]
			break
		}
	}
	d.args = rest
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 1<<24)
	out := json.NewEncoder(os.Stdout)
	var omu sync.Mutex
	for in.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if json.Unmarshal(in.Bytes(), &req) != nil || req.ID == nil {
			continue
		}
		reply := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		switch req.Method {
		case "initialize":
			reply["result"] = map[string]any{"protocolVersion": "2024-11-05",
				"capabilities": map[string]any{"tools": map[string]any{}},
				"serverInfo":   map[string]any{"name": "administration-screen", "version": version}}
		case "tools/list":
			tools := make([]map[string]any, 0, len(driveTools))
			for _, t := range driveTools {
				if t["name"] == "screen_press_key" && len(d.keys) > 0 {
					t = cloneTool(t, "Press one key: "+strings.Join(d.keys, ", ")+".")
				}
				tools = append(tools, t)
			}
			reply["result"] = map[string]any{"tools": tools}
		case "tools/call":
			reply["result"] = d.guarded(req.Params.Name, req.Params.Arguments)
		case "ping":
			reply["result"] = map[string]any{}
		default:
			reply["error"] = map[string]any{"code": -32601, "message": "no method " + req.Method}
		}
		omu.Lock()
		err := out.Encode(reply)
		omu.Unlock()
		if err != nil {
			return err
		}
	}
	return in.Err()
}

// guarded is call, held to the device's health: once it has failed
// lostAfter times running, it is lost, and every call says so.
func (d *drive) guarded(name string, a map[string]any) map[string]any {
	if d.lost != "" {
		return textResult(d.lost, true)
	}
	res := d.call(name, a)
	if d.fails < lostAfter {
		return res
	}
	why := ""
	if c, ok := res["content"].([]any); ok && len(c) > 0 {
		if m, ok := c[0].(map[string]any); ok {
			why, _ = m["text"].(string)
		}
	}
	d.lost = fmt.Sprintf("The equipment has failed %d times in a row (last: %s). It's lost: the device, not the app. Stop using it now: write UNREACHED: for each thing left in your plan, saying the equipment was lost, and then your report.", d.fails, oneLine(why, 300))
	if d.lostFile != "" {
		_ = os.WriteFile(d.lostFile, []byte(time.Now().Format("15:04")+" "+oneLine(why, 300)+"\n"), 0o600)
	}
	return textResult(d.lost, true)
}

func cloneTool(t map[string]any, desc string) map[string]any {
	c := map[string]any{}
	for k, v := range t {
		c[k] = v
	}
	c["description"] = desc
	return c
}

func textResult(s string, failed bool) map[string]any {
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": s}}, "isError": failed}
}

// run calls the driver with one action and returns what it printed.
func (d *drive) run(action ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, d.prog, append(append([]string{}, d.args...), action...)...)
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	d.stderr = oneLine(e.String(), 300)
	if err != nil {
		msg := strings.TrimSpace(e.String())
		if msg == "" {
			msg = err.Error()
		}
		if len(msg) > 2000 {
			msg = msg[:2000]
		}
		return nil, fmt.Errorf("%s: %s", action[0], msg)
	}
	return o.Bytes(), nil
}

func num(a map[string]any, k string) (float64, bool) {
	switch v := a[k].(type) {
	case float64:
		return v, true
	case string:
		f, err := strconv.ParseFloat(v, 64)
		return f, err == nil
	}
	return 0, false
}

// at is a picture coordinate on the device.
func (d *drive) at(v float64) string {
	return strconv.Itoa(int(v*float64(d.scale) + float64(d.scale)/2))
}

func (d *drive) call(name string, a map[string]any) map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	need := func(keys ...string) ([]float64, error) {
		var out []float64
		for _, k := range keys {
			v, ok := num(a, k)
			if !ok {
				return nil, fmt.Errorf("%s: a number", k)
			}
			out = append(out, v)
		}
		return out, nil
	}
	var err error
	switch name {
	case "screen_take_screenshot":
		return d.shot(false)
	case "screen_look":
		return d.shot(true)
	case "screen_wait":
		s, _ := num(a, "seconds")
		time.Sleep(time.Duration(min(max(s, 0), 10) * float64(time.Second)))
		return d.shot(true)
	case "screen_click":
		var v []float64
		if v, err = need("x", "y"); err == nil {
			act := []string{"click", d.at(v[0]), d.at(v[1])}
			if h, ok := num(a, "hold_ms"); ok && h > 0 {
				act = append(act, strconv.Itoa(int(min(h, 10000))))
			}
			_, err = d.run(act...)
		}
	case "screen_drag":
		var v []float64
		if v, err = need("x1", "y1", "x2", "y2"); err == nil {
			_, err = d.run("drag", d.at(v[0]), d.at(v[1]), d.at(v[2]), d.at(v[3]))
		}
	case "screen_scroll":
		var v []float64
		if v, err = need("x", "y"); err == nil {
			dx, _ := num(a, "dx")
			dy, _ := num(a, "dy")
			_, err = d.run("scroll", d.at(v[0]), d.at(v[1]),
				strconv.Itoa(int(dx)*d.scale), strconv.Itoa(int(dy)*d.scale))
		}
	case "screen_type_text":
		text, _ := a["text"].(string)
		if text != "" {
			_, err = d.run("type", text)
		}
		if submit, _ := a["submit"].(bool); err == nil && submit {
			_, err = d.run("key", "Enter")
		}
	case "screen_press_key":
		key, _ := a["key"].(string)
		if strings.TrimSpace(key) == "" {
			err = fmt.Errorf("key: the name of one key")
		} else {
			_, err = d.run("key", key)
		}
	default:
		return textResult(name+" isn't something a person at a screen has: look with screen_take_screenshot, act with the screen's tools", true)
	}
	if err != nil {
		return textResult(err.Error(), true)
	}
	return textResult("done; look to see what it did", false)
}

// shot takes a picture, scales it for the professional and, on a look, says
// what changed since the last look.
func (d *drive) shot(look bool) map[string]any {
	b, err := d.run("screenshot")
	if err != nil {
		d.fails++
		return textResult(err.Error(), true)
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		d.fails++
		msg := fmt.Sprintf("the driver's screenshot isn't a PNG (%s): %d bytes, starting %q", err, len(b), b[:min(len(b), 16)])
		if d.stderr != "" {
			msg += "; the driver said: " + d.stderr
		}
		return textResult(msg, true)
	}
	d.fails = 0
	k := shotScale(img.Bounds().Dx(), img.Bounds().Dy())
	small := shrink(img, k)
	note := fmt.Sprintf("The screen, %dx%d.", small.Bounds().Dx(), small.Bounds().Dy())
	if look {
		switch {
		case d.last == nil || d.last.Bounds() != small.Bounds():
			note += " Your first look at it at this size."
		default:
			if r, ok := changed(d.last, small); ok {
				note += fmt.Sprintf(" Changed since your last look: the box at x=%d y=%d, %dx%d.", r.Min.X, r.Min.Y, r.Dx(), r.Dy())
			} else {
				note += " Nothing changed since your last look."
			}
		}
		d.last = small
	}
	d.scale = k
	var enc bytes.Buffer
	if err := png.Encode(&enc, small); err != nil {
		return textResult(err.Error(), true)
	}
	return map[string]any{"content": []any{
		map[string]any{"type": "image", "data": base64.StdEncoding.EncodeToString(enc.Bytes()), "mimeType": "image/png"},
		map[string]any{"type": "text", "text": note},
	}}
}

// shotScale is the smallest whole factor that brings a w by h screen within
// what the professional is sent.
func shotScale(w, h int) int {
	k := 1
	for max(w, h)/k > shotEdge || (w/k)*(h/k) > shotPixels {
		k++
	}
	return k
}

// shrink averages each k by k block into one pixel.
func shrink(src image.Image, k int) *image.RGBA {
	r := src.Bounds()
	w, h := r.Dx()/k, r.Dy()/k
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	n := uint32(k * k)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var sr, sg, sb, sa uint32
			for j := 0; j < k; j++ {
				for i := 0; i < k; i++ {
					cr, cg, cb, ca := src.At(r.Min.X+x*k+i, r.Min.Y+y*k+j).RGBA()
					sr, sg, sb, sa = sr+cr, sg+cg, sb+cb, sa+ca
				}
			}
			dst.SetRGBA(x, y, color.RGBA{uint8(sr / n >> 8), uint8(sg / n >> 8), uint8(sb / n >> 8), uint8(sa / n >> 8)})
		}
	}
	return dst
}

// changed is the box around every pixel that differs by more than a little
// (a blinking cursor and anti-aliasing aside, any change a person sees).
func changed(a, b *image.RGBA) (image.Rectangle, bool) {
	var box image.Rectangle
	found := false
	r := a.Bounds()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			i := a.PixOffset(x, y)
			d := 0
			for c := 0; c < 3; c++ {
				v := int(a.Pix[i+c]) - int(b.Pix[i+c])
				d += max(v, -v)
			}
			if d > 24 {
				p := image.Rect(x, y, x+1, y+1)
				if !found {
					box, found = p, true
				} else {
					box = box.Union(p)
				}
			}
		}
	}
	return box, found
}
