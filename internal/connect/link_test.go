package connect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

const testShareID = "shr_01K9ZZZZZZZZZZZZZZZZZZZZZZ"

// nodeServer plays OpenVibe Connect for a share served by this computer (CON-4): the node API (a tunnel token per
// hello, the computer's shares) and /tunnel, which records the hello and every reauth.
type nodeServer struct {
	srv      *httptest.Server
	hellos   chan map[string]any
	reauths  chan string
	tickets  atomic.Int32
	mu       sync.Mutex
	bearers  []string
	notMine  bool // the tunnel-token call answers 404
	pagePort int
}

func newNodeServer(t *testing.T) *nodeServer {
	n := &nodeServer{hellos: make(chan map[string]any, 8), reauths: make(chan string, 16), pagePort: 8790}
	up := websocket.Upgrader{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/link/shares", func(w http.ResponseWriter, r *http.Request) {
		n.mu.Lock()
		n.bearers = append(n.bearers, r.Header.Get("Authorization"))
		n.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"shares": []map[string]any{{"id": testShareID, "host": "portal--alex.openvibe.link", "url": "https://portal--alex.openvibe.link", "local_port": n.pagePort, "mode": "owner"}}})
	})
	mux.HandleFunc("/api/v1/link/shares/"+testShareID+"/tunnel-token", func(w http.ResponseWriter, r *http.Request) {
		n.mu.Lock()
		n.bearers = append(n.bearers, r.Header.Get("Authorization"))
		n.mu.Unlock()
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			return
		}
		if n.notMine {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"code":"share.not_found","detail":"This computer does not serve that share."}`))
			return
		}
		i := n.tickets.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"tunnel_token": fmt.Sprintf("ovt_%043d", i), "expires_in": 60, "share": map[string]any{"id": testShareID, "local_port": n.pagePort}})
	})
	mux.HandleFunc("/tunnel", func(w http.ResponseWriter, r *http.Request) {
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		_, data, err := ws.ReadMessage()
		if err != nil {
			return
		}
		var hello map[string]any
		_ = json.Unmarshal(data, &hello)
		n.hellos <- hello
		_ = ws.WriteMessage(websocket.TextMessage, []byte(`{"t":"ready","v":1,"node":true,"share":{"id":"`+testShareID+`","host":"portal--alex.openvibe.link","url":"https://portal--alex.openvibe.link","mode":"owner","local_port":8790}}`))
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				return
			}
			var m map[string]any
			_ = json.Unmarshal(data, &m)
			if m["t"] == "reauth" {
				tok, _ := m["link_token"].(string)
				n.reauths <- tok
				_ = ws.WriteMessage(websocket.TextMessage, []byte(`{"t":"reauthed","until":"2026-11-03T00:00:00Z"}`))
			}
		}
	})
	n.srv = httptest.NewServer(mux)
	t.Cleanup(n.srv.Close)
	return n
}

func (n *nodeServer) url() string { return "ws" + strings.TrimPrefix(n.srv.URL, "http") + "/tunnel" }

// countingTokens hands out node-token-1, node-token-2, … (a fresh one each call).
func countingTokens() (func(context.Context) (string, error), *atomic.Int32) {
	var n atomic.Int32
	return func(context.Context) (string, error) { return fmt.Sprintf("node-token-%d", n.Add(1)), nil }, &n
}

func TestAShareServedByThisComputer(t *testing.T) {
	n := newNodeServer(t)
	tokens, _ := countingTokens()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan Ready, 2)
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Share{ID: testShareID, Port: 9100, Server: n.url()}, Options{Version: "test", LinkToken: tokens, ReauthEvery: 40 * time.Millisecond, OnReady: func(r Ready) { ready <- r }, MinWait: 20 * time.Millisecond})
	}()
	var hello map[string]any
	select {
	case hello = <-n.hellos:
	case err := <-done:
		t.Fatalf("Run ended: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("no hello")
	}
	if hello["share"] != testShareID || hello["link_token"] != "node-token-1" || hello["tunnel"] != fmt.Sprintf("ovt_%043d", 1) || hello["port"].(float64) != 9100 {
		t.Fatalf("hello %v", hello)
	}
	if _, has := hello["key"]; has {
		t.Fatal("no key in a computer's hello")
	}
	if r := <-ready; r.Host != "portal--alex.openvibe.link" {
		t.Fatalf("ready %v", r)
	}
	// It keeps the tunnel with fresh node tokens.
	first, second := <-n.reauths, <-n.reauths
	if first == second || !strings.HasPrefix(first, "node-token-") {
		t.Fatalf("reauths %q %q", first, second)
	}
	n.mu.Lock()
	bearer := n.bearers[0]
	n.mu.Unlock()
	if bearer != "Bearer node-token-1" {
		t.Fatalf("the tunnel-token call carried %q", bearer)
	}
}

func TestAShareThisComputerDoesNotServeStops(t *testing.T) {
	n := newNodeServer(t)
	n.notMine = true
	tokens, _ := countingTokens()
	err := Run(context.Background(), Share{ID: testShareID, Port: 9100, Server: n.url()}, Options{Version: "test", LinkToken: tokens, MinWait: 10 * time.Millisecond})
	if !errors.Is(err, ErrStopped) {
		t.Fatalf("want ErrStopped, got %v", err)
	}
}

func TestARemovedMachineStops(t *testing.T) {
	n := newNodeServer(t)
	revoked := func(context.Context) (string, error) { return "", fmt.Errorf("%w: refused", ErrStopped) }
	err := Run(context.Background(), Share{ID: testShareID, Port: 9100, Server: n.url()}, Options{Version: "test", LinkToken: revoked, MinWait: 10 * time.Millisecond})
	if !errors.Is(err, ErrStopped) {
		t.Fatalf("want ErrStopped, got %v", err)
	}
	if n.tickets.Load() != 0 {
		t.Fatal("no tunnel token without a node token")
	}
}

func TestAShareByIDNeedsThePairing(t *testing.T) {
	if err := Run(context.Background(), Share{ID: testShareID, Port: 9100}, Options{}); err == nil || !strings.Contains(err.Error(), "openvibe.link/machines") {
		t.Fatalf("got %v", err)
	}
	if err := Run(context.Background(), Share{ID: "shr_nope", Port: 9100}, Options{LinkToken: func(context.Context) (string, error) { return "x", nil }}); err == nil {
		t.Fatal("a bad id")
	}
}

func TestLinkSharesAndTheAPIURL(t *testing.T) {
	n := newNodeServer(t)
	got, err := LinkShares(context.Background(), nil, n.url(), "node-token-9", "test")
	if err != nil || len(got) != 1 || got[0].ID != testShareID || got[0].LocalPort != 8790 {
		t.Fatalf("%v %v", got, err)
	}
	for in, want := range map[string]string{
		"wss://connect.openvibe.website/tunnel": "https://connect.openvibe.website/x",
		"":                                      "https://connect.openvibe.link/x",
		"ws://127.0.0.1:5240/tunnel":            "http://127.0.0.1:5240/x",
	} {
		if u, err := apiURL(in, "/x"); err != nil || u != want {
			t.Fatalf("%q → %q %v", in, u, err)
		}
	}
	if _, err := apiURL("ws://evil.example/tunnel", "/x"); err == nil {
		t.Fatal("plain http to another host")
	}
}

func TestSavedSharesByID(t *testing.T) {
	dir := t.TempDir()
	if err := Add(dir, Share{ID: testShareID, Port: 8790}); err != nil {
		t.Fatal(err)
	}
	if err := Add(dir, Share{Key: "ovc_" + strings.Repeat("K", 43), Port: 8791}); err != nil {
		t.Fatal(err)
	}
	if err := Add(dir, Share{ID: testShareID, Port: 8792}); err != nil { // the same id again replaces it
		t.Fatal(err)
	}
	got, _ := Load(dir)
	if len(got) != 2 {
		t.Fatalf("%v", got)
	}
	if k, _ := Remove(dir, testShareID); k != 1 {
		t.Fatalf("removed %d", k)
	}
	if got, _ := Load(dir); len(got) != 1 || got[0].Port != 8791 {
		t.Fatalf("%v", got)
	}
}
