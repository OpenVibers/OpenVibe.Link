package access

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/OpenVibers/OpenVibe.Link/internal/credentials"
)

type testTokens struct{}

func (testTokens) Invalidate() {}

func (testTokens) Token(context.Context) (credentials.Secret, error) {
	return credentials.NewSecret("test-node-token"), nil
}

func TestAccessClientRoutesAndRefusals(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-node-token" {
			t.Errorf("missing node authorization")
		}
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/link/self/access-requests" {
			var body map[string]any
			if json.NewDecoder(r.Body).Decode(&body) != nil || body["target"] != "lnk_other" || body["duration_seconds"] != float64(3600) {
				t.Errorf("wrong request shape")
			}
		}
		if strings.HasSuffix(r.URL.Path, "/answer") {
			var body map[string]any
			if json.NewDecoder(r.Body).Decode(&body) != nil || body["approve"] != true {
				t.Errorf("wrong answer shape")
			}
		}
		if strings.HasSuffix(r.URL.Path, "/token") {
			_ = json.NewEncoder(w).Encode(map[string]any{"grant_token": "signed-test-token"})
			return
		}
		if strings.Contains(r.URL.Path, "/revoke") {
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"code":"access.not_allowed","detail":"test-node-token"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "req_12345678", "status": "pending"})
	}))
	defer server.Close()
	c := &Client{Origin: server.URL, Tokens: testTokens{}}
	ctx := context.Background()
	if _, err := c.Request(ctx, "lnk_other", []string{"agent"}, 3600, "work", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := c.List(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Answer(ctx, "req_12345678", true, []string{"agent"}, 1800); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GrantToken(ctx, "grt_12345678"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Revoke(ctx, "gra_12345678"); err == nil || strings.Contains(err.Error(), "test-node-token") {
		t.Fatalf("unsafe refusal: %v", err)
	}
	if _, err := c.Answer(ctx, "../../etc", true, nil, 0); err == nil {
		t.Fatal("unsafe id accepted")
	}
	if _, err := c.Request(ctx, "lnk_other", []string{"admin"}, 3600, "", ""); err == nil {
		t.Fatal("unknown scope accepted")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 5 {
		t.Fatalf("calls: %v", calls)
	}
	if calls[0] != "POST /api/v1/link/self/access-requests" || calls[2] != "POST /api/v1/link/self/access-requests/req_12345678/answer" {
		t.Fatalf("routes: %v", calls)
	}
}

func TestRequestResolvesOwnMachineName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/link/self/send/targets" {
			_, _ = w.Write([]byte(`{"machines":[{"id":"lnk_01J8Z4M2Q0R7T9YV3K6N8P1W2Z","name":"Desktop"}]}`))
			return
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["target"] != "lnk_01J8Z4M2Q0R7T9YV3K6N8P1W2Z" || body["duration_seconds"] != nil {
			t.Errorf("request did not resolve name and forever: %v", body)
		}
		_, _ = w.Write([]byte(`{"request":{"id":"req_12345678"}}`))
	}))
	defer server.Close()
	c := &Client{Origin: server.URL, Tokens: testTokens{}}
	if _, err := c.Request(context.Background(), "desktop", []string{"send"}, 0, "", ""); err != nil {
		t.Fatal(err)
	}
}
