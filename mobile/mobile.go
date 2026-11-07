// Package mobile is the Link phone's Go core. It is the same pairing and connection code the Node runs
// (internal/link), bound with `gomobile bind` into an AAR the Android app links against, so the phone pairs and
// stays connected exactly as `openvibe-node pair --network … --pairing … --code …` does.
//
// The core keeps no secret on disk: Pair returns the credentials as JSON for the app to encrypt with a key in the
// Android Keystore, and every later call takes that JSON back. Nothing here logs a credential or a token: the link's
// logger is discarded and the status strings carry only what the person needs to read.
//
// The exported surface is deliberately small and gomobile-friendly: strings, errors and one callback interface.
package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/OpenVibers/OpenVibe.Node/internal/credentials"
	"github.com/OpenVibers/OpenVibe.Node/internal/link"
	"github.com/OpenVibers/OpenVibe.Node/internal/protocol"
)

// Version is what the phone tells the services it is.
const Version = "0.1.0"

// DeviceKind is how the phone shows up on the person's account (OpenVibe.Bot's devices).
const DeviceKind = "phone"

const userAgent = "openvibe-link/" + Version

// pairing is the credential bundle Pair and Bind return as JSON. The field names match the ones the CLI stores, so
// a credential file from a phone could be read by the Node and the other way round.
type pairing struct {
	Principal      string              `json:"principal"`
	NodeCredential string              `json:"node_credential"`
	Network        string              `json:"network"`
	DeviceID       string              `json:"device_id,omitempty"`
	RobotID        string              `json:"robot_id,omitempty"`
	Server         string              `json:"server"`
	PairedFor      *protocol.PairedFor `json:"paired_for,omitempty"`
	PairedAt       time.Time           `json:"paired_at"`
}

// Listener hears the link's state. Called from the link's goroutines, one at a time.
type Listener interface {
	// OnState receives JSON: {"state":"connected"|"offline"|"stopped"|"revoked","detail":"…"}.
	OnState(event string)
}

// NormalizeCode accepts what a person types (abcd 1234) and returns the Crockford form ABCD1234.
func NormalizeCode(code string) (string, error) {
	return link.NormalizeCode(code)
}

// Pair redeems a pairing code on OpenVibe.Network and binds the phone as a device on OpenVibe.Bot. It returns the
// credential bundle as JSON. A bind that fails is not fatal: Start binds again, exactly as the Node does.
func Pair(networkURL, botURL, code, name, pairingID string) (string, error) {
	code, err := link.NormalizeCode(code)
	if err != nil {
		return "", err
	}
	// The QR on openvibe.bot/link/phone carries the pair_… id, so a wrong code is counted against that pairing's code,
	// exactly as `openvibe-node pair --network … --pairing … --code …` does. A typed code may omit it.
	if pairingID != "" && !link.PairingIDRe.MatchString(pairingID) {
		return "", errors.New("the pairing id is not valid; scan the QR again for a fresh code")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := link.PairNetwork(ctx, nil, networkURL, protocol.NodePairingRequest{Code: code, Pairing: pairingID, Name: name}, userAgent)
	if err != nil {
		return "", err
	}
	c.Server = trimSlash(botURL)
	if c.Server == "" {
		c.Server = "https://openvibe.bot"
	}
	p := pairingOf(c)
	if bound, err := link.Bind(ctx, nil, c, link.NewTokenSource(c, nil, userAgent), userAgent); err == nil {
		p = pairingOf(bound)
	} else if errors.Is(err, link.ErrNodeRevoked) {
		return "", err
	}
	return marshalPairing(p)
}

// Bind binds a paired phone whose device record is missing (the bind at pairing failed). It returns the bundle.
func Bind(credsJSON string) (string, error) {
	c, err := parsePairing(credsJSON)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	bound, err := link.Bind(ctx, nil, c, link.NewTokenSource(c, nil, userAgent), userAgent)
	if err != nil {
		return "", err
	}
	return marshalPairing(pairingOf(bound))
}

// Info is what the "This phone" screen shows, as JSON: the device, its account and its binding state.
func Info(credsJSON string) (string, error) {
	p, err := parsePairingJSON(credsJSON)
	if err != nil {
		return "", err
	}
	out, _ := json.Marshal(struct {
		DeviceID  string `json:"device_id"`
		Principal string `json:"principal"`
		Server    string `json:"server"`
		Network   string `json:"network"`
		Kind      string `json:"kind"`
		Bound     bool   `json:"bound"`
	}{p.DeviceID, p.Principal, p.Server, p.Network, DeviceKind, p.DeviceID != ""})
	return string(out), nil
}

// Link keeps the phone's one outbound connection to OpenVibe.Bot alive while it runs.
type Link struct {
	mu     sync.Mutex
	h      *handler
	l      *link.Link
	cancel context.CancelFunc
	done   chan struct{}
}

// NewLink prepares a connection from a credential bundle. Start dials; nothing is sent before a pairing exists.
func NewLink(credsJSON string) (*Link, error) {
	c, err := parsePairing(credsJSON)
	if err != nil {
		return nil, err
	}
	u := c.DeviceURL
	if u == "" {
		if u, err = link.DeviceURL(c.Server); err != nil {
			return nil, err
		}
	}
	h := &handler{}
	if c.NetworkPaired() {
		l := link.New(link.Options{
			URL: u, Tokens: link.NewTokenSource(c, nil, userAgent), UserAgent: userAgent,
			Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		}, h)
		return &Link{h: h, l: l}, nil
	}
	l := link.New(link.Options{
		URL: u, Credential: c.Credential, UserAgent: userAgent,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, h)
	return &Link{h: h, l: l}, nil
}

// Start runs the link until Stop (or a revocation that retrying cannot fix). It returns immediately.
func (l *Link) Start(listener Listener) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cancel != nil {
		return nil
	}
	l.h.setListener(listener)
	ctx, cancel := context.WithCancel(context.Background())
	l.cancel, l.done = cancel, make(chan struct{})
	go func() {
		defer close(l.done)
		if err := l.l.Run(ctx); err != nil {
			emit(l.h.listener(), "revoked", err.Error())
		} else {
			emit(l.h.listener(), "stopped", "")
		}
	}()
	return nil
}

// Stop closes the connection. It is safe to call when the link is not running.
func (l *Link) Stop() {
	l.mu.Lock()
	cancel, done := l.cancel, l.done
	l.cancel, l.done = nil, nil
	l.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
}

// Status is the connection's state as JSON.
func (l *Link) Status() string {
	st := l.l.Stats()
	out, _ := json.Marshal(struct {
		Connected  bool   `json:"connected"`
		Reconnects int64  `json:"reconnects"`
		LastError  string `json:"last_error"`
	}{l.l.Connected(), st.Reconnects, st.LastError})
	return string(out)
}

// handler is the link's callbacks, passed on to the app's listener.
type handler struct {
	mu  sync.Mutex
	lis Listener
}

func (h *handler) setListener(l Listener) { h.mu.Lock(); h.lis = l; h.mu.Unlock() }

func (h *handler) listener() Listener { h.mu.Lock(); defer h.mu.Unlock(); return h.lis }

func (h *handler) Connected() { emit(h.listener(), "connected", "") }

func (h *handler) Frame(protocol.Frame) {}

func (h *handler) Disconnected(err error) {
	detail := ""
	if err != nil {
		detail = err.Error()
	}
	emit(h.listener(), "offline", detail)
}

func emit(l Listener, state, detail string) {
	if l == nil {
		return
	}
	b, _ := json.Marshal(struct {
		State  string `json:"state"`
		Detail string `json:"detail,omitempty"`
	}{state, detail})
	l.OnState(string(b))
}

func pairingOf(c *credentials.Credentials) pairing {
	return pairing{
		Principal: c.Principal, NodeCredential: c.NodeCredential.Reveal(), Network: c.Network,
		DeviceID: c.DeviceID, RobotID: c.RobotID, Server: c.Server, PairedFor: c.PairedFor, PairedAt: c.PairedAt,
	}
}

func marshalPairing(p pairing) (string, error) {
	b, err := json.Marshal(p)
	return string(b), err
}

func parsePairingJSON(s string) (pairing, error) {
	var p pairing
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		return p, errors.New("the pairing is not valid")
	}
	return p, nil
}

func parsePairing(s string) (*credentials.Credentials, error) {
	p, err := parsePairingJSON(s)
	if err != nil {
		return nil, err
	}
	if p.Principal == "" || p.NodeCredential == "" || p.Network == "" {
		return nil, errors.New("this phone is not paired")
	}
	c := &credentials.Credentials{
		Principal: p.Principal, NodeCredential: credentials.NewSecret(p.NodeCredential), Network: trimSlash(p.Network),
		DeviceID: p.DeviceID, RobotID: p.RobotID, Server: trimSlash(p.Server), PairedFor: p.PairedFor, PairedAt: p.PairedAt,
	}
	if c.Server == "" {
		c.Server = "https://openvibe.bot"
	}
	return c, nil
}

func trimSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}
