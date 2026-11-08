package party

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testSubject = "usr_01J8Z3Q4R5S6T7V8W9X0Y1Z2A3"

func TestProxySignsInOneMemberAndChecksCurrentAccess(t *testing.T) {
	var logins atomic.Int32
	var allowed atomic.Bool
	var revoked atomic.Bool
	allowed.Store(true)
	neko := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/login":
			logins.Add(1)
			revoked.Store(false)
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["username"] != testSubject || body["password"] != "member" {
				t.Error("wrong login")
			}
			http.SetCookie(w, &http.Cookie{Name: "NEKO_SESSION", Value: "session-one", Path: "/", HttpOnly: true})
			_, _ = w.Write([]byte(`{"id":"session-1"}`))
		case "/api/room/control/reset", "/api/room/control/give/session-1":
			if r.Header.Get("Authorization") != "Bearer admin" {
				t.Error("admin call not authenticated")
			}
			w.WriteHeader(http.StatusNoContent)
		case "/api/whoami":
			if revoked.Load() {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			if r.Header.Get("Cookie") != "NEKO_SESSION=session-one" {
				t.Error("member cookie missing")
			}
			if r.Header.Get("Authorization") != "" || r.Header.Get("X-Openvibe-Subject") != "" {
				t.Error("visitor identity leaked to neko")
			}
			_, _ = w.Write([]byte("desktop"))
		}
	}))
	defer neko.Close()
	_, portText, _ := net.SplitHostPort(strings.TrimPrefix(neko.URL, "http://"))
	port, _ := strconv.Atoi(portText)
	p := NewProxy(Plan{MemberPassword: "member", APIToken: "admin"}, port, func(_ context.Context, subject string) (Access, error) {
		return Access{Active: true, Member: allowed.Load() && subject == testSubject, Holder: testSubject}, nil
	})
	srv := httptest.NewServer(p)
	defer srv.Close()
	request := func(path, subject, method string) *http.Response {
		req, _ := http.NewRequest(method, srv.URL+path, nil)
		req.Header.Set("X-Openvibe-Subject", subject)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		return res
	}
	if res := request("/", testSubject, "GET"); res.StatusCode != 200 || res.Header.Get("Set-Cookie") != "" {
		t.Fatal("approved member did not reach neko without leaking its cookie")
	}
	if res := request("/", testSubject, "GET"); res.StatusCode != 200 {
		t.Fatal("second request failed")
	}
	if logins.Load() != 1 {
		t.Fatal("member should have one private neko session")
	}
	if res := request("/api/login", testSubject, "POST"); res.StatusCode != 200 || logins.Load() != 1 {
		t.Fatal("member bootstrap did not reuse the private neko session")
	}
	revoked.Store(true)
	p.mu.Lock()
	s := p.sessions[testSubject]
	s.checked = time.Time{}
	p.sessions[testSubject] = s
	p.mu.Unlock()
	if res := request("/", testSubject, "GET"); res.StatusCode != 200 || logins.Load() != 2 {
		t.Fatal("expired neko session did not sign in again")
	}
	if res := request("/api/room/control/take", testSubject, "POST"); res.StatusCode != 403 {
		t.Fatal("direct control API should be blocked")
	}
	allowed.Store(false)
	if res := request("/", testSubject, "GET"); res.StatusCode != 403 {
		t.Fatal("removed member reached desktop")
	}
}
