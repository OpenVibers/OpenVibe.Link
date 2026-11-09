package egress

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestBudgetPersistsAndSharesCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	b, err := NewBudget(path)
	if err != nil {
		t.Fatal(err)
	}
	var ok atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if b.Reserve(10, 100) == nil {
				ok.Add(1)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 10 {
		t.Fatalf("cap allowed %d chunks", ok.Load())
	}
	b, err = NewBudget(path)
	if err != nil {
		t.Fatal(err)
	}
	if b.Reserve(1, 100) == nil {
		t.Fatal("restart reset the cap")
	}
	b.Now = func() time.Time { return time.Now().AddDate(0, 1, 0) }
	if b.Reserve(10, 100) != nil {
		t.Fatal("month did not reset")
	}
	b.Now = time.Now
	if b.Reserve(91, 100) == nil {
		t.Fatal("clock reversal reset the cap")
	}
}
func TestDNSRejectsMixedAnswersAndTranslationRanges(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "::1", "64:ff9b::7f00:1", "2002:7f00:1::", "2001::1", "::ffff:192.168.1.1"} {
		if Public(netip.MustParseAddr(s)) {
			t.Fatalf("allowed %s", s)
		}
	}
	called := false
	_, err := checkedDial(context.Background(), "site.example:443", func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}, {IP: net.ParseIP("10.1.1.1")}}, nil
	}, func(context.Context, string, string) (net.Conn, error) { called = true; return nil, nil })
	if err == nil || called {
		t.Fatal("mixed DNS answer was dialed")
	}
	var address string
	_, err = checkedDial(context.Background(), "site.example:443", func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
	}, func(_ context.Context, _, a string) (net.Conn, error) { address = a; return nil, nil })
	if err != nil || address != "8.8.8.8:443" {
		t.Fatal("DNS result not pinned")
	}
}
func TestMandatoryBrowserFlags(t *testing.T) {
	flags := strings.Join(BrowserArgs("169.254.240.1:12345"), " ")
	for _, s := range []string{"--proxy-server=http://169.254.240.1:12345", "--proxy-bypass-list=<-loopback>", "MAP * ~NOTFOUND", "disable_non_proxied_udp", "--disable-quic", "DnsOverHttps"} {
		if !strings.Contains(flags, s) {
			t.Fatalf("missing %s", s)
		}
	}
}

func TestAuthenticatedCONNECTAndSOCKSWithRemoteDNS(t *testing.T) {
	const session = "ses_test"
	credential := strings.Repeat("a", 43)
	var checks atomic.Int64
	network := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]string
		_ = json.NewDecoder(r.Body).Decode(&b)
		if b["credential"] != credential {
			w.WriteHeader(403)
			return
		}
		checks.Add(1)
		_ = json.NewEncoder(w).Encode(Grant{Session: session, Node: "lnk_test", Enabled: true, Cap: 100000, Expires: time.Now().Add(time.Minute).UnixMilli()})
	}))
	defer network.Close()
	budget, _ := NewBudget(filepath.Join(t.TempDir(), "usage"))
	targets := make(chan string, 4)
	endpoint := &Endpoint{Client: &Client{Base: network.URL}, Node: "lnk_test", Budget: budget, Pause: func() bool { return false }, Dial: func(_ context.Context, address string) (net.Conn, error) {
		targets <- address
		a, b := net.Pipe()
		go func() { defer b.Close(); _, _ = io.Copy(b, b) }()
		return a, nil
	}}
	server := httptest.NewServer(endpoint)
	defer server.Close()
	connect := func(pass string) (*wsConn, *http.Response, error) {
		ws, r, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/__ov_egress", http.Header{"X-OV-Session": {session}, "X-OV-Egress": {pass}})
		if err != nil {
			return nil, r, err
		}
		return &wsConn{Conn: ws}, r, nil
	}
	if _, r, err := connect("wrong"); err == nil || r.StatusCode != 403 {
		t.Fatal("wrong session credential accepted")
	}
	c, _, err := connect(credential)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(c, "CONNECT site.example:443 HTTP/1.1\r\nHost: site.example:443\r\nProxy-Authorization: Basic "+base64.StdEncoding.EncodeToString([]byte(session+":"+credential))+"\r\n\r\n")
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("CONNECT failed: %v", err)
	}
	_, _ = c.Write([]byte("hello"))
	b := make([]byte, 5)
	if _, err = io.ReadFull(br, b); err != nil || string(b) != "hello" {
		t.Fatal("CONNECT bytes lost")
	}
	c.Close()
	if <-targets != "site.example:443" {
		t.Fatal("resolved before reaching Node")
	}
	c, _, err = connect(credential)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.Write([]byte{5, 1, 2})
	answer := make([]byte, 2)
	_, _ = io.ReadFull(c, answer)
	if answer[1] != 2 {
		t.Fatal("SOCKS auth missing")
	}
	auth := append([]byte{1, byte(len(session))}, []byte(session)...)
	auth = append(auth, byte(len(credential)))
	auth = append(auth, []byte(credential)...)
	_, _ = c.Write(auth)
	_, _ = io.ReadFull(c, answer)
	if answer[1] != 0 {
		t.Fatal("SOCKS auth refused")
	}
	host := "site.example"
	request := append([]byte{5, 1, 0, 3, byte(len(host))}, []byte(host)...)
	request = append(request, 1, 187)
	_, _ = c.Write(request)
	reply := make([]byte, 10)
	if _, err = io.ReadFull(c, reply); err != nil || reply[1] != 0 {
		t.Fatal("SOCKS refused")
	}
	c.Close()
	if <-targets != "site.example:443" {
		t.Fatal("SOCKS DNS not left to Node")
	}
	if checks.Load() != 2 {
		t.Fatalf("every open must check Network, got %d", checks.Load())
	}
}

func TestCapAndPowerRefuseTunnel(t *testing.T) {
	network := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(Grant{Session: "s", Node: "n", Enabled: true, Cap: 10, Expires: time.Now().Add(time.Minute).UnixMilli()})
	}))
	defer network.Close()
	b, _ := NewBudget(filepath.Join(t.TempDir(), "usage"))
	if err := b.Reserve(10, 10); err != nil {
		t.Fatal(err)
	}
	// Spent cap closes as soon as data would be forwarded; a cap lowered below existing usage refuses even an open.
	if b.Reserve(1, 10) == nil {
		t.Fatal("cap did not stop bytes")
	}
	e := &Endpoint{Client: &Client{Base: network.URL}, Node: "n", Budget: b, Pause: func() bool { return true }}
	r := httptest.NewRequest("GET", "http://local/__ov_egress", nil)
	r.Header.Set("X-OV-Session", "s")
	r.Header.Set("X-OV-Egress", "test")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("battery or metered state did not pause")
	}
}
