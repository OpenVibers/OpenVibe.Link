package connect

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeServer plays OpenVibe Connect's side of ovconnect-ws/1 for one test: it reads the hello, answers ready (or
// refuses), then lets the test send requests and read what the Link answers.
type fakeServer struct {
	t      *testing.T
	srv    *httptest.Server
	conns  chan *serverConn
	refuse int // a close code to answer the hello with (0: ready)
	hellos chan map[string]any
}

type serverConn struct {
	ws     *websocket.Conn
	wmu    sync.Mutex
	mu     sync.Mutex
	frames map[uint32]chan frame
}

type frame struct {
	json map[string]any
	data []byte
	end  bool
}

func newFakeServer(t *testing.T, refuse int) *fakeServer {
	f := &fakeServer{t: t, conns: make(chan *serverConn, 4), refuse: refuse, hellos: make(chan map[string]any, 4)}
	up := websocket.Upgrader{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		_, data, err := ws.ReadMessage()
		if err != nil {
			return
		}
		var hello map[string]any
		_ = json.Unmarshal(data, &hello)
		f.hellos <- hello
		if f.refuse != 0 {
			_ = ws.WriteMessage(websocket.TextMessage, []byte(`{"t":"error","code":"key_invalid","message":"That connect key is not one we know."}`))
			_ = ws.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(f.refuse, "That connect key is not one we know."))
			time.Sleep(50 * time.Millisecond)
			_ = ws.Close()
			return
		}
		_ = ws.WriteMessage(websocket.TextMessage, []byte(`{"t":"ready","v":1,"share":{"id":"shr_1","host":"agents--alex.openvibe.link","url":"https://agents--alex.openvibe.link","mode":"owner"}}`))
		c := &serverConn{ws: ws, frames: map[uint32]chan frame{}}
		f.conns <- c
		for {
			mt, data, err := ws.ReadMessage()
			if err != nil {
				return
			}
			if mt == websocket.BinaryMessage {
				id := binary.BigEndian.Uint32(data[1:5])
				c.ch(id) <- frame{data: append([]byte(nil), data[5:]...), end: data[0] == kindEnd}
				continue
			}
			var m map[string]any
			_ = json.Unmarshal(data, &m)
			id, _ := m["id"].(float64)
			c.ch(uint32(id)) <- frame{json: m}
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeServer) url() string { return "ws" + strings.TrimPrefix(f.srv.URL, "http") + "/tunnel" }

func (c *serverConn) ch(id uint32) chan frame {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.frames[id] == nil {
		c.frames[id] = make(chan frame, 256)
	}
	return c.frames[id]
}

func (c *serverConn) sendJSON(v any) {
	b, _ := json.Marshal(v)
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.ws.WriteMessage(websocket.TextMessage, b)
}

func (c *serverConn) sendFrame(kind byte, id uint32, p []byte) {
	buf := make([]byte, 5+len(p))
	buf[0] = kind
	binary.BigEndian.PutUint32(buf[1:5], id)
	copy(buf[5:], p)
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.ws.WriteMessage(websocket.BinaryMessage, buf)
}

// collect reads one stream's answer: the head, then data until end (or an abort).
func (c *serverConn) collect(t *testing.T, id uint32) (status int, headers map[string]string, body []byte, abort string) {
	t.Helper()
	headers = map[string]string{}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case f := <-c.ch(id):
			if f.json != nil {
				switch f.json["t"] {
				case "res":
					status = int(f.json["status"].(float64))
					for _, h := range f.json["headers"].([]any) {
						p := h.([]any)
						headers[p[0].(string)] = p[1].(string)
					}
				case "abort":
					return status, headers, body, f.json["code"].(string)
				}
				continue
			}
			if f.end {
				return status, headers, body, ""
			}
			body = append(body, f.data...)
		case <-deadline:
			t.Fatalf("stream %d: no answer", id)
		}
	}
}

func localApp(t *testing.T) (port int, seen chan *http.Request) {
	seen = make(chan *http.Request, 16)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(strings.NewReader(string(b)))
		seen <- r
		w.Header().Set("X-App", "yes")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"method": r.Method, "path": r.URL.RequestURI(), "host": r.Host, "user": r.Header.Get("X-OpenVibe-User"), "body": string(b)})
	})
	mux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("z", 3<<20)))
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	// A raw upgrade: answer 101, then echo with a prefix.
	return ln.Addr().(*net.TCPAddr).Port, seen
}

func echoUpgradeApp(t *testing.T) int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				br := bufio.NewReader(c)
				req, err := http.ReadRequest(br)
				if err != nil || req.Header.Get("Upgrade") == "" {
					return
				}
				_, _ = c.Write([]byte("HTTP/1.1 101 Switching Protocols\r\nUpgrade: echo\r\nConnection: Upgrade\r\n\r\n"))
				buf := make([]byte, 1024)
				for {
					n, err := br.Read(buf)
					if n > 0 {
						_, _ = c.Write(append([]byte("echo:"), buf[:n]...))
					}
					if err != nil {
						return
					}
				}
			}(c)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func start(t *testing.T, f *fakeServer, port int) (*serverConn, chan Ready, context.CancelFunc, chan error) {
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan Ready, 4)
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Share{Key: "ovc_" + strings.Repeat("A", 43), Port: port, Server: f.url()}, Options{Version: "test", LocalHost: "127.0.0.1", OnReady: func(r Ready) { ready <- r }, MinWait: 20 * time.Millisecond})
	}()
	t.Cleanup(cancel)
	select {
	case c := <-f.conns:
		return c, ready, cancel, done
	case err := <-done:
		t.Fatalf("Run ended: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("no connection")
	}
	return nil, nil, nil, nil
}

func TestHelloAndHTTP(t *testing.T) {
	f := newFakeServer(t, 0)
	port, seen := localApp(t)
	c, ready, _, _ := start(t, f, port)
	hello := <-f.hellos
	if hello["t"] != "hello" || hello["v"].(float64) != 1 || hello["port"].(float64) != float64(port) || !strings.HasPrefix(hello["key"].(string), "ovc_") {
		t.Fatalf("hello %v", hello)
	}
	if r := <-ready; r.Host != "agents--alex.openvibe.link" {
		t.Fatalf("ready %v", r)
	}

	// A GET with the headers the server sends: Host is the server's choice, the visitor's identity passes.
	c.sendJSON(map[string]any{"t": "req", "id": 1, "method": "GET", "path": "/hello?x=1", "headers": [][]string{{"host", "localhost:" + strconv.Itoa(port)}, {"x-openvibe-user", "alex"}}})
	c.sendFrame(kindEnd, 1, nil)
	status, headers, body, abort := c.collect(t, 1)
	if abort != "" || status != 200 || headers["x-app"] != "yes" {
		t.Fatalf("GET: %d %v %q", status, headers, abort)
	}
	var got map[string]string
	_ = json.Unmarshal(body, &got)
	if got["path"] != "/hello?x=1" || got["user"] != "alex" || got["host"] != "localhost:"+strconv.Itoa(port) {
		t.Fatalf("the app saw %v", got)
	}
	<-seen

	// A POST whose body arrives in pieces.
	c.sendJSON(map[string]any{"t": "req", "id": 2, "method": "POST", "path": "/post", "headers": [][]string{{"content-type", "text/plain"}}})
	c.sendFrame(kindData, 2, []byte("hello "))
	c.sendFrame(kindData, 2, []byte("from the phone"))
	c.sendFrame(kindEnd, 2, nil)
	_, _, body, _ = c.collect(t, 2)
	_ = json.Unmarshal(body, &got)
	if got["body"] != "hello from the phone" || got["method"] != "POST" {
		t.Fatalf("POST: %v", got)
	}

	// A 3 MB answer arrives whole, in chunks.
	c.sendJSON(map[string]any{"t": "req", "id": 3, "method": "GET", "path": "/big", "headers": [][]string{}})
	c.sendFrame(kindEnd, 3, nil)
	_, _, body, _ = c.collect(t, 3)
	if len(body) != 3<<20 {
		t.Fatalf("big: %d bytes", len(body))
	}
}

func TestNothingListeningIsLocalRefused(t *testing.T) {
	f := newFakeServer(t, 0)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	c, _, _, _ := start(t, f, port)
	c.sendJSON(map[string]any{"t": "req", "id": 1, "method": "GET", "path": "/", "headers": [][]string{}})
	c.sendFrame(kindEnd, 1, nil)
	if _, _, _, abort := c.collect(t, 1); abort != "local_refused" {
		t.Fatalf("abort %q", abort)
	}
}

func TestUpgradeIsABytePipe(t *testing.T) {
	f := newFakeServer(t, 0)
	port := echoUpgradeApp(t)
	c, _, _, _ := start(t, f, port)
	c.sendJSON(map[string]any{"t": "raw", "id": 7, "head": "GET /ws HTTP/1.1\r\nHost: localhost\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n"})
	got := ""
	deadline := time.After(5 * time.Second)
	sent := false
	for !strings.Contains(got, "echo:ping") {
		select {
		case fr := <-c.ch(7):
			got += string(fr.data)
			if strings.Contains(got, "\r\n\r\n") && !sent {
				sent = true
				c.sendFrame(kindData, 7, []byte("ping"))
			}
		case <-deadline:
			t.Fatalf("no echo: %q", got)
		}
	}
	if !strings.HasPrefix(got, "HTTP/1.1 101") {
		t.Fatalf("got %q", got)
	}
}

func TestAWrongKeyStops(t *testing.T) {
	f := newFakeServer(t, 4002)
	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), Share{Key: "ovc_" + strings.Repeat("B", 43), Port: 80, Server: f.url()}, Options{MinWait: 10 * time.Millisecond})
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrStopped) {
			t.Fatalf("err %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("did not stop")
	}
}

func TestReconnectsAfterADrop(t *testing.T) {
	f := newFakeServer(t, 0)
	port, _ := localApp(t)
	c, ready, _, _ := start(t, f, port)
	<-ready
	_ = c.ws.Close() // the link drops (Wi-Fi, sleep, a restart on the server)
	select {
	case <-f.conns:
	case <-time.After(5 * time.Second):
		t.Fatal("no reconnect")
	}
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("not ready again")
	}
}

func TestSavedSharesAreTheOwnersOnly(t *testing.T) {
	dir := t.TempDir()
	key := "ovc_" + strings.Repeat("C", 43)
	if err := Add(dir, Share{Key: key, Port: 8790}); err != nil {
		t.Fatal(err)
	}
	if err := Add(dir, Share{Key: key, Port: 8791}); err != nil {
		t.Fatal(err)
	}
	list, _ := Load(dir)
	if len(list) != 1 || list[0].Port != 8791 {
		t.Fatalf("the same key replaces: %v", list)
	}
	fi, _ := os.Stat(filepath.Join(dir, File))
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 { // Windows keeps no Unix mode bits
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
	if n, _ := Remove(dir, "8791"); n != 1 {
		t.Fatal("remove by port")
	}
	if list, _ := Load(dir); len(list) != 0 {
		t.Fatal("removed")
	}
	if Redact(key) != "ovc_CCCCCC…" {
		t.Fatalf("redact %q", Redact(key))
	}
}
