package egress

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func echoDial(_ context.Context, _ string) (net.Conn, error) {
	a, b := net.Pipe()
	go func() { defer b.Close(); _, _ = io.Copy(b, b) }()
	return a, nil
}
func proxyConnect(t *testing.T, address string) (net.Conn, *bufio.Reader, *http.Response) {
	t.Helper()
	c, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	_, _ = io.WriteString(c, "CONNECT web.example:443 HTTP/1.1\r\nHost: web.example:443\r\n\r\n")
	br := bufio.NewReader(c)
	r, err := http.ReadResponse(br, nil)
	if err != nil {
		c.Close()
		t.Fatal(err)
	}
	return c, br, r
}
func TestBridgeThroughNodeAndCapClosesTheTunnel(t *testing.T) {
	network := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]string
		_ = json.NewDecoder(r.Body).Decode(&b)
		if b["credential"] != strings.Repeat("c", 43) {
			w.WriteHeader(403)
			return
		}
		_ = json.NewEncoder(w).Encode(Grant{Session: "s", Node: "n", Enabled: true, Cap: 4, Expires: time.Now().Add(time.Minute).UnixMilli()})
	}))
	defer network.Close()
	budget, _ := NewBudget(filepath.Join(t.TempDir(), "usage"))
	targets := make(chan string, 1)
	endpoint := httptest.NewServer(&Endpoint{Client: &Client{Base: network.URL}, Node: "n", Budget: budget, Pause: func() bool { return false }, Dial: func(ctx context.Context, s string) (net.Conn, error) { targets <- s; return echoDial(ctx, s) }})
	defer endpoint.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := Bridge(ctx, listener, Plan{Mode: "link", Relay: "ws" + strings.TrimPrefix(endpoint.URL, "http") + "/__ov_egress", Session: "s", Credential: strings.Repeat("c", 43), Offline: "wait"})
	defer server.Close()
	conn, br, response := proxyConnect(t, listener.Addr().String())
	defer conn.Close()
	if response.StatusCode != 200 {
		t.Fatal("bridge failed")
	}
	if <-targets != "web.example:443" {
		t.Fatal("destination was resolved before reaching Node")
	}
	_, _ = conn.Write([]byte("ab"))
	echo := make([]byte, 2)
	if _, err = io.ReadFull(br, echo); err != nil || string(echo) != "ab" {
		t.Fatal("bridge did not carry bytes")
	}
	// Two bytes each way consumed the whole allowance; no fifth byte may leave the device.
	_, _ = conn.Write([]byte("x"))
	if _, err = br.ReadByte(); err == nil {
		t.Fatal("spent bandwidth did not close tunnel")
	}
}
func TestBridgeOfflineChoicesAndAuthorizationFailures(t *testing.T) {
	for _, tc := range []struct {
		name, choice string
		status, want int
		direct       bool
	}{{"offline fallback", "openvibe", 503, 200, true}, {"offline wait", "wait", 503, 503, false}, {"authorization refused", "openvibe", 403, 503, false}} {
		t.Run(tc.name, func(t *testing.T) {
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status) }))
			defer remote.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			var used atomic.Bool
			server := Bridge(ctx, listener, Plan{Mode: "link", Relay: "ws" + strings.TrimPrefix(remote.URL, "http"), Offline: tc.choice}, func(ctx context.Context, s string) (net.Conn, error) { used.Store(true); return echoDial(ctx, s) })
			defer server.Close()
			conn, _, response := proxyConnect(t, listener.Addr().String())
			conn.Close()
			if response.StatusCode != tc.want || used.Load() != tc.direct {
				t.Fatalf("status %d, direct %v", response.StatusCode, used.Load())
			}
		})
	}
}
