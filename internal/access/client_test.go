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
	if len(calls) != 4 {
		t.Fatalf("calls: %v", calls)
	}
	if calls[0] != "POST /api/v1/link/self/access-requests" || calls[2] != "POST /api/v1/link/self/access-requests/req_12345678/answer" {
		t.Fatalf("routes: %v", calls)
	}
}
