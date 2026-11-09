package egress

import (
	"bufio"
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// The WebSocket is the byte pipe already supported by Connect; no destination or credential is in its URL.
type wsConn struct {
	*websocket.Conn
	reader io.Reader
	mu     sync.Mutex
}

func (c *wsConn) Read(b []byte) (int, error) {
	for {
		if c.reader == nil {
			kind, r, err := c.NextReader()
			if err != nil {
				return 0, err
			}
			if kind != websocket.BinaryMessage {
				return 0, errors.New("binary stream required")
			}
			c.reader = r
		}
		n, err := c.reader.Read(b)
		if err == io.EOF {
			c.reader = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}
func (c *wsConn) Write(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.WriteMessage(websocket.BinaryMessage, b); err != nil {
		return 0, err
	}
	return len(b), nil
}
func (c *wsConn) LocalAddr() net.Addr  { return c.UnderlyingConn().LocalAddr() }
func (c *wsConn) RemoteAddr() net.Addr { return c.UnderlyingConn().RemoteAddr() }
func (c *wsConn) SetDeadline(t time.Time) error {
	_ = c.SetReadDeadline(t)
	return c.SetWriteDeadline(t)
}

type Endpoint struct {
	Client *Client
	Node   string
	Budget *Budget
	Dial   DialFunc
	Pause  func() bool
}

func (e *Endpoint) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/__ov_egress" || r.Method != "GET" {
		http.Error(w, "refused", 403)
		return
	}
	session, credential := r.Header.Get("X-OV-Session"), r.Header.Get("X-OV-Egress")
	grant, err := e.Client.Open(r.Context(), session, credential, e.Node)
	if err != nil || e.Pause() || e.Budget.Reserve(0, grant.Cap) != nil {
		http.Error(w, "refused", 403)
		return
	}
	ws, err := (&websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "" }}).Upgrade(w, r, nil)
	if err != nil {
		return
	}
	ws.SetReadLimit(64 << 10)
	conn := &wsConn{Conn: ws}
	defer conn.Close()
	ctx, cancel := context.WithCancel(r.Context())
	var currentCap atomic.Int64
	currentCap.Store(grant.Cap)
	defer cancel()
	// Revocation, a stop, opt-out and a newly reported battery state close existing streams too.
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				g, err := e.Client.Open(ctx, session, credential, e.Node)
				currentCap.Store(g.Cap)
				if err != nil || e.Pause() || e.Budget.Reserve(0, g.Cap) != nil {
					cancel()
					_ = conn.Close()
					return
				}
			}
		}
	}()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	br := bufio.NewReader(conn)
	first, err := br.Peek(1)
	if err != nil {
		return
	}
	dial := e.Dial
	if dial == nil {
		dial = PublicDial
	}
	var upstream net.Conn
	if first[0] == 5 {
		upstream, err = e.socks(ctx, conn, br, session, credential, dial)
	} else {
		upstream, br, err = e.http(ctx, conn, br, session, credential, dial, grant.Cap)
	}
	if err != nil {
		return
	}
	defer upstream.Close()
	go func() { <-ctx.Done(); _ = upstream.Close() }()
	_ = conn.SetReadDeadline(time.Time{})
	copyBudget := func(dst net.Conn, src io.Reader) {
		buf := make([]byte, 32<<10)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				if e.Budget.Reserve(int64(n), currentCap.Load()) != nil {
					return
				}
				if _, werr := dst.Write(buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}
	done := make(chan struct{}, 1)
	go func() { copyBudget(upstream, br); _ = upstream.Close(); _ = conn.Close(); done <- struct{}{} }()
	copyBudget(conn, upstream)
	_ = conn.Close()
	_ = upstream.Close()
	<-done
}
func (e *Endpoint) http(ctx context.Context, c net.Conn, br *bufio.Reader, session, credential string, dial DialFunc, cap int64) (net.Conn, *bufio.Reader, error) {
	r, next, err := readRequest(br)
	br = next
	if err != nil {
		return nil, br, err
	}
	expected := "Basic " + base64.StdEncoding.EncodeToString([]byte(session+":"+credential))
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Proxy-Authorization")), []byte(expected)) != 1 {
		_, _ = io.WriteString(c, "HTTP/1.1 407 Proxy Authentication Required\r\nConnection: close\r\n\r\n")
		return nil, br, errors.New("refused")
	}
	address := r.Host
	if r.Method != "CONNECT" {
		if r.URL.Scheme != "http" || r.URL.User != nil {
			return nil, br, errors.New("HTTP proxy required")
		}
		address = r.URL.Host
		if r.URL.Port() == "" {
			address = net.JoinHostPort(r.URL.Hostname(), "80")
		}
	}
	upstream, err := dial(ctx, address)
	if err != nil {
		_, _ = io.WriteString(c, "HTTP/1.1 403 Forbidden\r\nConnection: close\r\n\r\n")
		return nil, br, err
	}
	if r.Method == "CONNECT" {
		_, err = io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n")
	} else {
		r.Header.Del("Proxy-Authorization")
		r.Header.Del("Proxy-Connection")
		r.Close = true
		// Count request headers and bodies as well as the response. Reserve in the writer before any bytes leave.
		err = r.Write(&budgetWriter{Writer: upstream, budget: e.Budget, cap: cap})
	}
	if err != nil {
		_ = upstream.Close()
		return nil, br, err
	}
	return upstream, br, nil
}

type budgetWriter struct {
	io.Writer
	budget *Budget
	cap    int64
}

func (w *budgetWriter) Write(b []byte) (int, error) {
	if err := w.budget.Reserve(int64(len(b)), w.cap); err != nil {
		return 0, err
	}
	return w.Writer.Write(b)
}
func (e *Endpoint) socks(ctx context.Context, c net.Conn, br *bufio.Reader, session, credential string, dial DialFunc) (net.Conn, error) {
	head := make([]byte, 2)
	if _, err := io.ReadFull(br, head); err != nil {
		return nil, err
	}
	methods := make([]byte, int(head[1]))
	if _, err := io.ReadFull(br, methods); err != nil {
		return nil, err
	}
	found := false
	for _, m := range methods {
		if m == 2 {
			found = true
		}
	}
	if !found {
		_, _ = c.Write([]byte{5, 255})
		return nil, errors.New("authentication required")
	}
	_, _ = c.Write([]byte{5, 2})
	if _, err := io.ReadFull(br, head); err != nil || head[0] != 1 {
		return nil, errors.New("authentication required")
	}
	user := make([]byte, int(head[1]))
	if _, err := io.ReadFull(br, user); err != nil {
		return nil, err
	}
	n, err := br.ReadByte()
	if err != nil {
		return nil, err
	}
	pass := make([]byte, int(n))
	if _, err = io.ReadFull(br, pass); err != nil {
		return nil, err
	}
	if subtle.ConstantTimeCompare(user, []byte(session)) != 1 || subtle.ConstantTimeCompare(pass, []byte(credential)) != 1 {
		_, _ = c.Write([]byte{1, 1})
		return nil, errors.New("refused")
	}
	_, _ = c.Write([]byte{1, 0})
	req := make([]byte, 4)
	if _, err = io.ReadFull(br, req); err != nil {
		return nil, err
	}
	if req[0] != 5 || req[1] != 1 || req[2] != 0 {
		_, _ = c.Write([]byte{5, 7, 0, 1, 0, 0, 0, 0, 0, 0})
		return nil, errors.New("CONNECT only")
	}
	var host string
	switch req[3] {
	case 3:
		n, err = br.ReadByte()
		if err != nil || n == 0 {
			return nil, errors.New("invalid host")
		}
		b := make([]byte, int(n))
		if _, err = io.ReadFull(br, b); err != nil {
			return nil, err
		}
		host = string(b)
	case 1, 4:
		size := 4
		if req[3] == 4 {
			size = 16
		}
		b := make([]byte, size)
		if _, err = io.ReadFull(br, b); err != nil {
			return nil, err
		}
		host = net.IP(b).String()
	default:
		return nil, errors.New("invalid address")
	}
	if _, err = io.ReadFull(br, head); err != nil {
		return nil, err
	}
	up, err := dial(ctx, net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(head)))))
	if err != nil {
		_, _ = c.Write([]byte{5, 2, 0, 1, 0, 0, 0, 0, 0, 0})
		return nil, err
	}
	_, err = c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
	if err != nil {
		up.Close()
		return nil, err
	}
	return up, nil
}

// Bridge is bound to one isolated desk. It speaks an ordinary HTTP proxy to Chrome and the authenticated
// WebSocket byte pipe to Link. It never changes to a direct route after Link accepted a connection.
func Bridge(ctx context.Context, listener net.Listener, p Plan, testDial ...DialFunc) *http.Server {
	directDial := PublicDial
	if len(testDial) > 0 {
		directDial = testDial[0]
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var remote net.Conn
		host := r.URL.Hostname()
		if r.Method == "CONNECT" {
			host, _, _ = net.SplitHostPort(r.Host)
		}
		direct := p.Mode == "direct"
		for _, s := range p.Excluded {
			if strings.EqualFold(host, s) || strings.HasSuffix(strings.ToLower(host), "."+s) {
				direct = true
			}
		}
		if !direct {
			ws, resp, err := (&websocket.Dialer{HandshakeTimeout: 10 * time.Second}).DialContext(r.Context(), p.Relay, http.Header{"X-OV-Session": {p.Session}, "X-OV-Egress": {p.Credential}})
			if err == nil {
				remote = &wsConn{Conn: ws}
			} else if p.Offline == "openvibe" && (resp == nil || resp.StatusCode == 503) {
				direct = true
			} else {
				http.Error(w, "Your computer is unavailable.", 503)
				return
			}
		}
		if direct {
			address := r.Host
			if r.Method != "CONNECT" && r.URL.Port() == "" {
				address = net.JoinHostPort(r.URL.Hostname(), "80")
			}
			var err error
			remote, err = directDial(r.Context(), address)
			if err != nil {
				http.Error(w, "Site unavailable", 403)
				return
			}
		}
		defer remote.Close()
		if !direct {
			r.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(p.Session+":"+p.Credential)))
			if err := r.WriteProxy(remote); err != nil {
				return
			}
		} else if r.Method != "CONNECT" {
			r.Header.Del("Proxy-Authorization")
			r.Close = true
			if err := r.Write(remote); err != nil {
				return
			}
		}
		if r.Method != "CONNECT" {
			response, err := http.ReadResponse(bufio.NewReader(remote), r)
			if err != nil {
				http.Error(w, "Site unavailable", 502)
				return
			}
			defer response.Body.Close()
			for k, v := range response.Header {
				w.Header()[k] = v
			}
			w.WriteHeader(response.StatusCode)
			_, _ = io.Copy(w, response.Body)
			return
		}
		client, buffer, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer client.Close()
		if direct {
			_, _ = io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n")
		}
		done := make(chan struct{}, 1)
		go func() { _, _ = io.Copy(remote, buffer); _ = remote.Close(); _ = client.Close(); done <- struct{}{} }()
		_, _ = io.Copy(client, remote)
		_ = remote.Close()
		_ = client.Close()
		<-done
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, MaxHeaderBytes: 32 << 10, BaseContext: func(net.Listener) context.Context { return ctx }}
	go server.Serve(listener)
	go func() { <-ctx.Done(); _ = server.Close() }()
	return server
}

func BrowserArgs(address string) []string {
	host, _, _ := net.SplitHostPort(address)
	return []string{"--proxy-server=http://" + address, "--proxy-bypass-list=<-loopback>", "--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE " + host, "--force-webrtc-ip-handling-policy=disable_non_proxied_udp", "--disable-quic", "--disable-features=DnsOverHttps,UseDnsHttpsSvcbAlpn"}
}

func readRequest(br *bufio.Reader) (*http.Request, *bufio.Reader, error) {
	var header []byte
	for {
		line, err := br.ReadSlice('\n')
		if err != nil {
			return nil, br, err
		}
		header = append(header, line...)
		if len(header) > 32<<10 {
			return nil, br, errors.New("headers too large")
		}
		if string(line) == "\r\n" {
			break
		}
	}
	next := bufio.NewReader(io.MultiReader(bytes.NewReader(header), br))
	r, err := http.ReadRequest(next)
	return r, next, err
}
