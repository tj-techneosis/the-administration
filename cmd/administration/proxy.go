package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"strconv"
	"sync"
	"time"
)

// gate is the browser's only way out: an HTTP proxy that lets through
// requests to this room's own ports on this machine, and nothing else.
// Playwright's own origin list is a convenience, not a boundary (it doesn't
// see WebSockets or follow redirects), so every connection the browser makes,
// loopback included, goes through here.
type gate struct {
	ports   map[string]bool
	srv     *http.Server
	ln      net.Listener
	logf    *os.File
	mu      sync.Mutex
	blocked int
	tunnels map[net.Conn]bool
}

func openGate(ports []int, logPath string) (*gate, error) {
	g := &gate{ports: map[string]bool{}, tunnels: map[net.Conn]bool{}}
	for _, p := range ports {
		g.ports[strconv.Itoa(p)] = true
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	g.ln = ln
	if g.logf, err = os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	// Whatever host the browser names, the gate only ever dials 127.0.0.1,
	// and only on an allowed port: a name can't be made to resolve elsewhere.
	dial := func(ctx context.Context, _, addr string) (net.Conn, error) {
		_, port, err := net.SplitHostPort(addr)
		if err != nil || !g.ports[port] {
			return nil, fmt.Errorf("refused %s", addr)
		}
		return dialer.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", port))
	}
	rp := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.URL.Scheme, r.Out.URL.Host = "http", r.In.URL.Host
			r.Out.Host = r.In.Host
		},
		Transport: &http.Transport{Proxy: nil, DialContext: dial, ForceAttemptHTTP2: false,
			MaxIdleConns: 20, IdleConnTimeout: 30 * time.Second},
		ErrorLog: log.New(io.Discard, "", 0),
	}
	g.srv = &http.Server{
		ReadHeaderTimeout: 30 * time.Second,
		ErrorLog:          log.New(io.Discard, "", 0),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host, port, ok := g.target(r)
			if !ok {
				g.refuse(w, r)
				return
			}
			if r.Method == http.MethodConnect {
				g.tunnel(w, r, dial, host+":"+port)
				return
			}
			rp.ServeHTTP(w, r)
		}),
	}
	go g.srv.Serve(ln)
	return g, nil
}

func (g *gate) addr() string { return g.ln.Addr().String() }

// target says whether a request is for this room: a local name and one of
// the room's ports.
func (g *gate) target(r *http.Request) (host, port string, ok bool) {
	hp := r.URL.Host
	if r.Method == http.MethodConnect {
		hp = r.Host
	}
	if hp == "" {
		return "", "", false // not a proxy request
	}
	host, port, err := net.SplitHostPort(hp)
	if err != nil {
		host, port = hp, "80"
		if r.URL.Scheme == "https" {
			port = "443"
		}
	}
	if host != "127.0.0.1" && host != "localhost" {
		return "", "", false
	}
	if r.Method != http.MethodConnect && r.URL.Scheme != "http" && r.URL.Scheme != "ws" {
		return "", "", false
	}
	return host, port, g.ports[port]
}

func (g *gate) refuse(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	g.blocked++
	fmt.Fprintf(g.logf, "%s blocked %s %s%s\n", time.Now().Format(time.TimeOnly), r.Method, r.Host, r.URL.Path)
	g.mu.Unlock()
	http.Error(w, "the Administration: this room's browser only reaches the room", http.StatusForbidden)
}

func (g *gate) tunnel(w http.ResponseWriter, r *http.Request, dial func(context.Context, string, string) (net.Conn, error), addr string) {
	up, err := dial(r.Context(), "tcp", addr)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		up.Close()
		http.Error(w, "no hijack", http.StatusInternalServerError)
		return
	}
	down, buf, err := hj.Hijack()
	if err != nil {
		up.Close()
		return
	}
	g.mu.Lock()
	g.tunnels[up], g.tunnels[down] = true, true
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		delete(g.tunnels, up)
		delete(g.tunnels, down)
		g.mu.Unlock()
	}()
	_, _ = down.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	if buf.Reader.Buffered() > 0 {
		_, _ = io.CopyN(up, buf, int64(buf.Reader.Buffered()))
	}
	go func() { _, _ = io.Copy(up, down); up.Close() }()
	_, _ = io.Copy(down, up)
	down.Close()
}

func (g *gate) close() int {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = g.srv.Shutdown(ctx)
	g.mu.Lock()
	defer g.mu.Unlock()
	// Shutdown leaves hijacked tunnels alone; they go too.
	for c := range g.tunnels {
		c.Close()
	}
	g.logf.Close()
	return g.blocked
}

func gateCmd(args []string) error {
	var ports []int
	for _, a := range args {
		p, err := strconv.Atoi(a)
		if err != nil {
			return usageError{"_gate PORT..."}
		}
		ports = append(ports, p)
	}
	logPath := os.Getenv("ADMINISTRATION_GATE_LOG")
	if logPath == "" {
		logPath = os.DevNull
	}
	g, err := openGate(ports, logPath)
	if err != nil {
		return err
	}
	fmt.Println(g.addr())
	_, _ = io.Copy(io.Discard, os.Stdin)
	fmt.Fprintln(os.Stderr, "blocked", g.close())
	return nil
}
