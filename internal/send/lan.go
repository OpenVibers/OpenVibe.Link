package send

// Direct delivery on the same network: a Link listens on a random local port and accepts a sealed Locker envelope
// only from another of the owner's machines. The sender proves it with an HMAC from the owner's LAN secret, which
// both machines read from Network with their own node tokens; no relay round trip is needed, and an unauthenticated
// neighbour cannot put anything in the inbox. The payload is always ciphertext: the listener never has a key.

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	lanPath      = "/api/link-send/v1"
	lanMaxBody   = 40 << 20
	lanWindow    = 5 * time.Minute
	lanMaxItems  = 100
	lanMaxNonces = 4096
)

// Incoming is one sealed item that arrived directly. It is held in memory until the person accepts, declines or Link
// exits; the relay copy is still there if they restart, because the sender only takes the direct path when accepted.
type Incoming struct {
	ID         string
	Sender     string
	SenderName string
	Envelope   string
	Bytes      int64
	CreatedAt  time.Time
}

// LAN is one Link's direct listener and its small queue of received items.
type LAN struct {
	target string
	mu     sync.Mutex
	secret []byte
	items  map[string]Incoming
	nonces map[string]time.Time
	ln     net.Listener
	server *http.Server
}

func NewLAN(target string) *LAN {
	return &LAN{target: target, items: map[string]Incoming{}, nonces: map[string]time.Time{}}
}

// Listen opens a random port. It is loopback-only in tests; on a real machine it is reachable on the local network,
// which is the point, and every request still has to carry the owner's HMAC.
func (l *LAN) Listen(addr string) error {
	if addr == "" {
		addr = "0.0.0.0:0"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	l.ln = ln
	l.server = &http.Server{Handler: l.handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = l.server.Serve(ln) }()
	return nil
}

func (l *LAN) Close() error {
	if l.server != nil {
		return l.server.Close()
	}
	return nil
}

// Port is the listening port, or 0 when Listen did not succeed.
func (l *LAN) Port() int {
	if l.ln == nil {
		return 0
	}
	if a, ok := l.ln.Addr().(*net.TCPAddr); ok {
		return a.Port
	}
	return 0
}

// URLs are the addresses to advertise to Network: every non-loopback IPv4 address of this machine, plus loopback for
// two Links on one computer (a test, or two profiles).
func (l *LAN) URLs() []string {
	port := l.Port()
	if port == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(host string) {
		if host == "" || seen[host] {
			return
		}
		seen[host] = true
		out = append(out, "http://"+net.JoinHostPort(host, strconv.Itoa(port)))
	}
	add("127.0.0.1")
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			ip, _, err := net.ParseCIDR(a.String())
			if err != nil || ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.To4() == nil {
				continue
			}
			add(ip.String())
		}
	}
	return out
}

func (l *LAN) SetSecret(encoded string) error {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil || len(raw) < 16 {
		return errors.New("the LAN sender secret is not valid")
	}
	l.mu.Lock()
	l.secret = append([]byte(nil), raw...)
	l.mu.Unlock()
	return nil
}

func (l *LAN) Items() []Incoming {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Incoming, 0, len(l.items))
	for _, item := range l.items {
		out = append(out, item)
	}
	return out
}

func (l *LAN) Accept(id string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	item, ok := l.items[id]
	if !ok {
		return "", errors.New("that item is no longer waiting")
	}
	return item.Envelope, nil
}

func (l *LAN) Decline(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.items[id]; !ok {
		return errors.New("that item is no longer waiting")
	}
	delete(l.items, id)
	return nil
}

func (l *LAN) Complete(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.items[id]; !ok {
		return errors.New("that item is no longer waiting")
	}
	delete(l.items, id)
	return nil
}

type lanRequest struct {
	Sender   string `json:"sender_node"`
	Target   string `json:"target_node"`
	Envelope string `json:"envelope"`
	At       int64  `json:"at"`
	Nonce    string `json:"nonce"`
	Mac      string `json:"mac"`
}

func (l *LAN) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(lanPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req lanRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, lanMaxBody)).Decode(&req); err != nil {
			http.Error(w, "unreadable transfer", http.StatusBadRequest)
			return
		}
		l.mu.Lock()
		secret := append([]byte(nil), l.secret...)
		l.mu.Unlock()
		if len(secret) == 0 {
			http.Error(w, "this machine has not turned on receiving", http.StatusForbidden)
			return
		}
		if req.Target != l.target {
			http.Error(w, "that item is for another machine", http.StatusNotFound)
			return
		}
		if req.Sender == "" || req.Sender == l.target || req.Nonce == "" || len(req.Nonce) > 128 {
			http.Error(w, "the sender is not one of this machine's owner's machines", http.StatusForbidden)
			return
		}
		if len(req.Envelope) == 0 || len(req.Envelope) > 32*1024*1024 {
			http.Error(w, "the encrypted item is missing or too large", http.StatusBadRequest)
			return
		}
		now := time.Now()
		at := time.Unix(req.At, 0)
		if req.At == 0 || now.Sub(at) > lanWindow || at.Sub(now) > lanWindow {
			http.Error(w, "the transfer is too old", http.StatusForbidden)
			return
		}
		want := lanMAC(secret, req.Sender, req.Target, req.At, req.Nonce, req.Envelope)
		if subtle.ConstantTimeCompare([]byte(req.Mac), []byte(want)) != 1 {
			http.Error(w, "the sender is not one of this machine's owner's machines", http.StatusForbidden)
			return
		}
		l.mu.Lock()
		defer l.mu.Unlock()
		if _, seen := l.nonces[req.Nonce]; seen {
			http.Error(w, "that transfer was already received", http.StatusConflict)
			return
		}
		if len(l.nonces) > lanMaxNonces {
			for nonce, when := range l.nonces {
				if now.Sub(when) > lanWindow {
					delete(l.nonces, nonce)
				}
			}
		}
		if len(l.items) >= lanMaxItems {
			http.Error(w, "this machine's transfer list is full; accept or decline an item first", http.StatusTooManyRequests)
			return
		}
		l.nonces[req.Nonce] = now
		id := "lan_" + hex.EncodeToString(random(16))
		l.items[id] = Incoming{ID: id, Sender: req.Sender, Envelope: req.Envelope, Bytes: int64(len(req.Envelope)), CreatedAt: now}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"` + id + `"}`))
	})
	return mux
}

func lanMAC(secret []byte, sender, target string, at int64, nonce, envelope string) string {
	sum := sha256.Sum256([]byte(envelope))
	h := hmac.New(sha256.New, secret)
	fmt.Fprintf(h, "openvibe-link-send-lan-v1\n%s\n%s\n%d\n%s\n%s\n", sender, target, at, nonce, base64.RawURLEncoding.EncodeToString(sum[:]))
	return hex.EncodeToString(h.Sum(nil))
}

func random(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

// sendLAN tries the target's advertised local endpoints. Any failure (not on the network, a timeout, an old secret)
// falls back to the relay by returning an error.
func (c *Client) sendLAN(ctx context.Context, target, envelope string) error {
	if c.Self == "" {
		return errors.New("this machine has no id for a direct transfer")
	}
	machines, err := c.Targets(ctx)
	if err != nil {
		return err
	}
	var endpoints []string
	for _, m := range machines {
		if m.ID == target {
			endpoints = m.LAN
			break
		}
	}
	if len(endpoints) == 0 {
		return errors.New("the receiving machine is not on this network")
	}
	encoded, err := c.LANKey(ctx)
	if err != nil {
		return err
	}
	secret, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(secret) < 16 {
		return errors.New("the LAN sender secret is not valid")
	}
	for _, base := range endpoints {
		at := time.Now().Unix()
		nonce := hex.EncodeToString(random(16))
		body, _ := json.Marshal(lanRequest{
			Sender: c.Self, Target: target, Envelope: envelope, At: at, Nonce: nonce,
			Mac: lanMAC(secret, c.Self, target, at, nonce, envelope),
		})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+lanPath, strings.NewReader(string(body)))
		if err != nil {
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		client := &http.Client{Timeout: 2 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if resp.StatusCode == http.StatusCreated {
			return nil
		}
	}
	return errors.New("the direct transfer did not arrive")
}
