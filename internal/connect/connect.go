// Package connect keeps OpenVibe Connect shares up from the Node: each share is one port on this machine served at
// https://<label>--<user>.openvibe.link behind OpenVibe sign-in (OpenVibe.Connect, ADR-057, Website Connect v0).
//
// The Node opens one outgoing WebSocket per share to wss://connect.openvibe.link/tunnel and speaks ovconnect-ws/1,
// the same protocol as the one-file client (OpenVibe.Connect public/ov-connect.js):
//
//	→ {"t":"hello","v":1,"key":"ovc_…","client":"openvibe-link/…","port":8790}
//	  or, served by this computer as OpenVibe Link (node.go, CON-4): {"t":"hello","v":1,"share":"shr_…","node_token":…,"tunnel":"ovt_…",…}
//	← {"t":"ready","v":1,"share":{"id","host","url","mode"}}            or {"t":"error",…} and a close
//	← {"t":"req","id":N,"method","path","headers":[[k,v]…]}             an HTTP request for the local port
//	← {"t":"raw","id":N,"head":"GET /ws HTTP/1.1\r\n…"}                  a WebSocket upgrade: a byte pipe
//	→ {"t":"res","id":N,"status":200,"headers":[[k,v]…]}
//	↔ binary [kind 1 byte][id 4 bytes BE][payload]: kind 1 data, 2 end
//	↔ {"t":"pause"|"resume"|"abort","id":N}
//
// It only ever connects to localhost on the share's own port: the server cannot make the Node reach anything else.
// A wrong key (close 4002), a closed share (4003) or another client taking over (4001) stops that share; anything
// else reconnects with backoff from 1 s to 30 s.
package connect

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// DefaultServer is OpenVibe Connect's tunnel endpoint.
const DefaultServer = "wss://connect.openvibe.link/tunnel"

const (
	protocolVersion = 1
	kindData        = 1
	kindEnd         = 2
	chunkSize       = 32 * 1024
)

// Share is one share the Node keeps up: its connect key (or, served by this computer as OpenVibe Link, its id) and
// the local port it serves.
type Share struct {
	Key    string `json:"key,omitempty"`
	ID     string `json:"share,omitempty"` // shr_…: this computer serves it with its own node token, no key (CON-4)
	Port   int    `json:"port"`
	Server string `json:"server,omitempty"` // default DefaultServer
	Name   string `json:"name,omitempty"`   // the share's address, learnt from "ready" (shown by `share list`)
}

// Ready is what the server says once a share is connected.
type Ready struct {
	ID   string `json:"id"`
	Host string `json:"host"`
	URL  string `json:"url"`
	Mode string `json:"mode"`
}

// ErrStopped is returned when the server ends a share for good: a wrong key, a closed share, or another client.
var ErrStopped = errors.New("connect: share stopped by the server")

// Options tune a client; the zero value is right for production.
type Options struct {
	Log          *slog.Logger
	Version      string
	LocalHost    string // default "localhost"
	OnDisconnect func()
	OnReady      func(Ready) // called after each successful hello
	OnRequest    func(method, path string, status int, d time.Duration)
	Dialer       *websocket.Dialer
	MinWait      time.Duration // reconnect backoff floor (default 1 s)
	MaxWait      time.Duration // reconnect backoff ceiling (default 30 s)

	// NodeToken buys this computer's node token for openvibe.link, for shares it serves by id (node.go). An error
	// wrapping ErrStopped (the machine was removed on OpenVibe) stops the share; any other is tried again.
	NodeToken   func(ctx context.Context) (string, error)
	HTTPClient  *http.Client  // Connect's API, for tunnel tokens (default: 20 s timeout)
	ReauthEvery time.Duration // default DefaultReauthEvery
}

// Label names a share in logs and lists without its key: the id, or the key's first characters.
func (s Share) Label() string {
	if s.ID != "" {
		return s.ID
	}
	return Redact(s.Key)
}

// Run keeps one share connected until ctx ends or the server stops it (ErrStopped).
func Run(ctx context.Context, s Share, o Options) error {
	if s.Port < 1 || s.Port > 65535 {
		return fmt.Errorf("connect: port %d is not a port", s.Port)
	}
	switch {
	case s.ID != "":
		if !ShareIDRe.MatchString(s.ID) {
			return errors.New("connect: the share is its shr_… id from connect.openvibe.link")
		}
		if o.NodeToken == nil {
			return errors.New("connect: this share is served by the computer itself; pair the Node with OpenVibe first (openvibe.link/machines)")
		}
	case !strings.HasPrefix(s.Key, "ovc_"):
		return errors.New("connect: the key is the share's ovc_… connect key")
	}
	if o.ReauthEvery == 0 {
		o.ReauthEvery = DefaultReauthEvery
	}
	if o.Log == nil {
		o.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if o.MinWait == 0 {
		o.MinWait = time.Second
	}
	if o.MaxWait == 0 {
		o.MaxWait = 30 * time.Second
	}
	// The Node service's status lists the shares it keeps up (LiveShares). A foreground share has its own process,
	// so the service's status never shows it.
	markLive(s)
	defer forgetLive(s)
	userReady, userRequest := o.OnReady, o.OnRequest
	o.OnReady = func(r Ready) {
		liveReady(s, r)
		if userReady != nil {
			userReady(r)
		}
	}
	o.OnRequest = func(method, path string, status int, d time.Duration) {
		liveRequest(s)
		if userRequest != nil {
			userRequest(method, path, status, d)
		}
	}
	wait := o.MinWait
	for {
		err := runOnce(ctx, s, o, func() { wait = o.MinWait })
		if o.OnDisconnect != nil {
			o.OnDisconnect()
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		liveDown(s, err)
		if errors.Is(err, ErrStopped) {
			return err
		}
		o.Log.Info("connect: disconnected; trying again", "port", s.Port, "in", wait.String(), "err", errString(err))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		wait *= 2
		if wait > o.MaxWait {
			wait = o.MaxWait
		}
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type tunnel struct {
	ws      *websocket.Conn
	wmu     sync.Mutex
	share   Share
	o       Options
	mu      sync.Mutex
	streams map[uint32]*stream
}

type stream struct {
	body   *io.PipeWriter // http: the request body's writer
	conn   net.Conn       // raw: the local socket
	paused chan struct{}  // non-nil while the server asked us to pause sending
	pmu    sync.Mutex

	// The inbox: data frames from the server wait here so a slow local reader never stalls the tunnel's read loop.
	// Over inboxHigh bytes the server is asked to pause this stream; under inboxLow it resumes.
	qmu      sync.Mutex
	q        [][]byte
	qbytes   int
	ended    bool
	held     bool
	dead     bool
	wake     chan struct{} // never closed: drop sets dead and wakes the drainer
	writerOn bool
}

const (
	inboxHigh = 1 << 20
	inboxLow  = 256 << 10
)

func (st *stream) waitResume(ctx context.Context) {
	st.pmu.Lock()
	ch := st.paused
	st.pmu.Unlock()
	if ch == nil {
		return
	}
	select {
	case <-ch:
	case <-ctx.Done():
	}
}

func (st *stream) setPaused(p bool) {
	st.pmu.Lock()
	defer st.pmu.Unlock()
	if p && st.paused == nil {
		st.paused = make(chan struct{})
	} else if !p && st.paused != nil {
		close(st.paused)
		st.paused = nil
	}
}

func runOnce(ctx context.Context, s Share, o Options, onReady func()) error {
	server := s.Server
	if server == "" {
		server = DefaultServer
	}
	hello := map[string]any{"t": "hello", "v": protocolVersion, "client": "openvibe-link/" + o.Version, "port": s.Port}
	if s.ID != "" {
		// Served by this computer (node.go): a fresh node token and a tunnel token for this one hello.
		tok, err := o.NodeToken(ctx)
		if err != nil {
			return err
		}
		ticket, info, err := tunnelToken(ctx, o, server, s.ID, tok)
		if err != nil {
			return err
		}
		if info.LocalPort != 0 && info.LocalPort != s.Port {
			o.Log.Warn("connect: the share's page names another port; this computer serves the one it was given", "share", s.ID, "page", info.LocalPort, "serving", s.Port)
		}
		hello["share"], hello["node_token"], hello["tunnel"] = s.ID, tok, ticket
	} else {
		hello["key"] = s.Key
	}
	dialer := o.Dialer
	if dialer == nil {
		dialer = &websocket.Dialer{HandshakeTimeout: 15 * time.Second, Proxy: http.ProxyFromEnvironment}
	}
	ws, _, err := dialer.DialContext(ctx, server, http.Header{"User-Agent": {"openvibe-link/" + o.Version}})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	t := &tunnel{ws: ws, share: s, o: o, streams: map[uint32]*stream{}}
	defer t.closeAll()
	go func() { <-ctx.Done(); _ = ws.Close() }()
	// The server pings every 25 s; answering is gorilla's default ping handler. A dead link shows as a read error.
	if err := t.sendJSON(hello); err != nil {
		return err
	}
	reauthing := false
	for {
		mt, data, err := ws.ReadMessage()
		if err != nil {
			var ce *websocket.CloseError
			if errors.As(err, &ce) && (ce.Code == 4001 || ce.Code == 4002 || ce.Code == 4003) {
				o.Log.Warn("connect: the server stopped this share", "port", s.Port, "code", ce.Code, "reason", ce.Text)
				return fmt.Errorf("%w: %s", ErrStopped, ce.Text)
			}
			return err
		}
		if mt == websocket.BinaryMessage {
			t.onBinary(ctx, data)
			continue
		}
		var m struct {
			T       string     `json:"t"`
			ID      uint32     `json:"id"`
			Method  string     `json:"method"`
			Path    string     `json:"path"`
			Headers [][]string `json:"headers"`
			Head    string     `json:"head"`
			Share   Ready      `json:"share"`
			Message string     `json:"message"`
		}
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		switch m.T {
		case "ready":
			onReady()
			if s.ID != "" && !reauthing {
				reauthing = true
				go t.reauthLoop(ctx)
			}
			o.Log.Info("connect: connected", "url", m.Share.URL, "port", s.Port)
			if o.OnReady != nil {
				o.OnReady(m.Share)
			}
		case "error":
			o.Log.Warn("connect: the server refused", "port", s.Port, "message", m.Message)
		case "req":
			t.onRequest(ctx, m.ID, m.Method, m.Path, m.Headers)
		case "raw":
			t.onRaw(ctx, m.ID, m.Head)
		case "pause", "resume":
			if st := t.get(m.ID); st != nil {
				st.setPaused(m.T == "pause")
			}
		case "abort":
			t.drop(m.ID)
		}
	}
}

// reauthLoop sends a fresh node token every ReauthEvery while the tunnel is up (a share served by this computer). A
// token it cannot buy is skipped: Connect closes the tunnel once the last one runs out, and Run reconnects or stops.
func (t *tunnel) reauthLoop(ctx context.Context) {
	tick := time.NewTicker(t.o.ReauthEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		tok, err := t.o.NodeToken(ctx)
		if err != nil {
			t.o.Log.Warn("connect: no fresh node token", "share", t.share.ID, "err", err)
			continue
		}
		if err := t.sendJSON(map[string]any{"t": "reauth", "node_token": tok}); err != nil {
			return
		}
	}
}

func (t *tunnel) get(id uint32) *stream {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.streams[id]
}

func (t *tunnel) put(id uint32, st *stream) {
	t.mu.Lock()
	t.streams[id] = st
	t.mu.Unlock()
}

func (t *tunnel) drop(id uint32) {
	t.mu.Lock()
	st := t.streams[id]
	delete(t.streams, id)
	t.mu.Unlock()
	if st == nil {
		return
	}
	if st.body != nil {
		_ = st.body.CloseWithError(errors.New("aborted"))
	}
	if st.conn != nil {
		_ = st.conn.Close()
	}
	st.setPaused(false)
	st.qmu.Lock()
	st.dead = true
	st.q, st.qbytes = nil, 0
	wake := st.wake
	st.qmu.Unlock()
	if wake != nil {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
}

func (t *tunnel) closeAll() {
	t.mu.Lock()
	ids := make([]uint32, 0, len(t.streams))
	for id := range t.streams {
		ids = append(ids, id)
	}
	t.mu.Unlock()
	for _, id := range ids {
		t.drop(id)
	}
}

func (t *tunnel) sendJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	t.wmu.Lock()
	defer t.wmu.Unlock()
	_ = t.ws.SetWriteDeadline(time.Now().Add(30 * time.Second))
	return t.ws.WriteMessage(websocket.TextMessage, b)
}

func (t *tunnel) sendFrame(kind byte, id uint32, payload []byte) error {
	buf := make([]byte, 5+len(payload))
	buf[0] = kind
	binary.BigEndian.PutUint32(buf[1:5], id)
	copy(buf[5:], payload)
	t.wmu.Lock()
	defer t.wmu.Unlock()
	_ = t.ws.SetWriteDeadline(time.Now().Add(30 * time.Second))
	return t.ws.WriteMessage(websocket.BinaryMessage, buf)
}

func (t *tunnel) abort(id uint32, code, message string) {
	_ = t.sendJSON(map[string]any{"t": "abort", "id": id, "code": code, "message": message})
	t.drop(id)
}

func (t *tunnel) onBinary(ctx context.Context, data []byte) {
	if len(data) < 5 {
		return
	}
	kind, id, payload := data[0], binary.BigEndian.Uint32(data[1:5]), data[5:]
	st := t.get(id)
	if st == nil {
		return
	}
	st.qmu.Lock()
	if kind == kindData {
		st.q = append(st.q, append([]byte(nil), payload...))
		st.qbytes += len(payload)
	} else if kind == kindEnd {
		st.ended = true
	}
	pause := st.qbytes > inboxHigh && !st.held
	if pause {
		st.held = true
	}
	if !st.writerOn {
		st.writerOn = true
		st.wake = make(chan struct{}, 1)
		go t.drain(id, st)
	}
	wake := st.wake
	st.qmu.Unlock()
	if pause {
		_ = t.sendJSON(map[string]any{"t": "pause", "id": id})
	}
	select {
	case wake <- struct{}{}:
	default:
	}
}

// drain writes a stream's inbox to the local side, in order, off the read loop.
func (t *tunnel) drain(id uint32, st *stream) {
	for {
		<-st.wake
		for {
			st.qmu.Lock()
			if st.dead {
				st.qmu.Unlock()
				return
			}
			if len(st.q) == 0 {
				ended := st.ended
				st.qmu.Unlock()
				if ended {
					if st.body != nil {
						_ = st.body.Close()
					} else if c, ok := st.conn.(interface{ CloseWrite() error }); ok {
						_ = c.CloseWrite()
					}
					return
				}
				break
			}
			chunk := st.q[0]
			st.q = st.q[1:]
			st.qbytes -= len(chunk)
			resume := st.held && st.qbytes < inboxLow
			if resume {
				st.held = false
			}
			st.qmu.Unlock()
			var err error
			if st.body != nil {
				_, err = st.body.Write(chunk)
			} else if st.conn != nil {
				_, err = st.conn.Write(chunk)
			}
			if err != nil {
				t.drop(id)
				return
			}
			if resume {
				_ = t.sendJSON(map[string]any{"t": "resume", "id": id})
			}
		}
	}
}

func (t *tunnel) localAddr() string {
	h := t.o.LocalHost
	if h == "" {
		h = "localhost"
	}
	return net.JoinHostPort(h, strconv.Itoa(t.share.Port))
}

// onRequest carries one HTTP request to the local port. The request body arrives as data frames on a pipe, so a large
// upload streams; the response streams back in chunks with the server's pause/resume.
func (t *tunnel) onRequest(ctx context.Context, id uint32, method, path string, headers [][]string) {
	pr, pw := io.Pipe()
	st := &stream{body: pw}
	t.put(id, st)
	go func() {
		started := time.Now()
		req, err := http.NewRequestWithContext(ctx, method, "http://"+t.localAddr()+path, pr)
		if err != nil {
			t.abort(id, "local_error", "bad request")
			return
		}
		for _, h := range headers {
			if len(h) != 2 {
				continue
			}
			if strings.EqualFold(h[0], "host") {
				req.Host = h[1]
				continue
			}
			if strings.EqualFold(h[0], "content-length") {
				if n, err := strconv.ParseInt(h[1], 10, 64); err == nil {
					req.ContentLength = n
				}
				continue
			}
			req.Header.Add(h[0], h[1])
		}
		if method == http.MethodGet || method == http.MethodHead {
			// A body-less request still gets its "end" frame; nothing waits on the pipe.
			if req.ContentLength <= 0 {
				req.Body = http.NoBody
				req.ContentLength = 0
				go func() { _, _ = io.Copy(io.Discard, pr) }()
			}
		}
		resp, err := localClient.Do(req)
		if err != nil {
			code := "local_error"
			var oe *net.OpError
			if errors.As(err, &oe) && oe.Op == "dial" {
				code = "local_refused"
			}
			if t.o.OnRequest != nil {
				t.o.OnRequest(method, path, 0, time.Since(started))
			}
			t.abort(id, code, err.Error())
			return
		}
		defer resp.Body.Close()
		out := make([][]string, 0, len(resp.Header)+1)
		for k, vs := range resp.Header {
			for _, v := range vs {
				out = append(out, []string{strings.ToLower(k), v})
			}
		}
		if resp.ContentLength >= 0 && resp.Header.Get("Content-Length") == "" && method != http.MethodHead {
			out = append(out, []string{"content-length", strconv.FormatInt(resp.ContentLength, 10)})
		}
		if err := t.sendJSON(map[string]any{"t": "res", "id": id, "status": resp.StatusCode, "headers": out}); err != nil {
			return
		}
		if t.o.OnRequest != nil {
			t.o.OnRequest(method, path, resp.StatusCode, time.Since(started))
		}
		buf := make([]byte, chunkSize)
		for {
			st.waitResume(ctx)
			n, rerr := resp.Body.Read(buf)
			if n > 0 {
				if err := t.sendFrame(kindData, id, buf[:n]); err != nil {
					return
				}
			}
			if rerr == io.EOF {
				_ = t.sendFrame(kindEnd, id, nil)
				t.mu.Lock()
				delete(t.streams, id)
				t.mu.Unlock()
				return
			}
			if rerr != nil {
				t.abort(id, "local_error", "the response broke off")
				return
			}
		}
	}()
}

// onRaw opens a plain TCP connection to the local port for an upgrade, writes the request head the server built, and
// then only moves bytes. The local app answers the 101 itself.
func (t *tunnel) onRaw(ctx context.Context, id uint32, head string) {
	conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", t.localAddr())
	if err != nil {
		t.abort(id, "local_refused", err.Error())
		return
	}
	st := &stream{conn: conn}
	t.put(id, st)
	if _, err := io.Copy(conn, bufio.NewReader(bytes.NewReader([]byte(head)))); err != nil {
		t.abort(id, "local_error", err.Error())
		return
	}
	go func() {
		buf := make([]byte, chunkSize)
		for {
			st.waitResume(ctx)
			n, rerr := conn.Read(buf)
			if n > 0 {
				if err := t.sendFrame(kindData, id, buf[:n]); err != nil {
					return
				}
			}
			if rerr != nil {
				_ = t.sendFrame(kindEnd, id, nil)
				t.drop(id)
				return
			}
		}
	}()
}

// localClient talks to the local app: no proxy (it is localhost), no redirects followed (the visitor's browser
// follows them), no automatic decompression (the bytes pass as the app sent them).
var localClient = &http.Client{
	Transport:     &http.Transport{Proxy: nil, DisableCompression: true, MaxIdleConnsPerHost: 16, IdleConnTimeout: 60 * time.Second, ResponseHeaderTimeout: 2 * time.Minute},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}
