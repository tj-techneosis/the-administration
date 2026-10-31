package main

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The gate lets the browser reach the room's own ports and nothing else,
// whatever form the request takes.
func TestGate(t *testing.T) {
	room := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "room") }))
	defer room.Close()
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "other") }))
	defer other.Close()
	port := func(s *httptest.Server) int { p, _ := strconv.Atoi(s.URL[strings.LastIndex(s.URL, ":")+1:]); return p }

	g, err := openGate([]int{port(room)}, filepath.Join(t.TempDir(), "gate.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer g.close()
	proxy, _ := url.Parse("http://" + g.addr())
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxy)}}

	get := func(u string) int {
		resp, err := client.Get(u)
		if err != nil {
			return -1
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := get(room.URL); c != 200 {
		t.Errorf("room: %d", c)
	}
	if c := get(strings.Replace(room.URL, "127.0.0.1", "localhost", 1)); c != 200 {
		t.Errorf("room as localhost: %d", c)
	}
	for _, u := range []string{other.URL, "http://example.com/", "http://10.0.0.1:" + strconv.Itoa(port(room)) + "/",
		"http://[::1]:" + strconv.Itoa(port(room)) + "/"} {
		if c := get(u); c != http.StatusForbidden {
			t.Errorf("%s: want 403, got %d", u, c)
		}
	}
	connect := func(target string) string {
		c, err := net.Dial("tcp", g.addr())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
		line, _ := bufio.NewReader(c).ReadString('\n')
		return strings.TrimSpace(line)
	}
	if l := connect(fmt.Sprintf("127.0.0.1:%d", port(other))); !strings.Contains(l, "403") {
		t.Errorf("CONNECT to another port: %q", l)
	}
	if l := connect("example.com:443"); !strings.Contains(l, "403") {
		t.Errorf("CONNECT out: %q", l)
	}
	if l := connect(fmt.Sprintf("127.0.0.1:%d", port(room))); !strings.Contains(l, "200") {
		t.Errorf("CONNECT to the room: %q", l)
	}
	if n := g.close(); n < 6 {
		t.Errorf("blocked count %d", n)
	}
}
