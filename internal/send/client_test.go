package send

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/OpenVibers/OpenVibe.Node/internal/credentials"
)

type staticToken string

func (s staticToken) Token(context.Context) (credentials.Secret, error) {
	return credentials.NewSecret(string(s)), nil
}
func (staticToken) Invalidate() {}

func TestClientUsesNodeTokenAndOnlyPublishesPublicReceiveKey(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer node-token" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		if r.URL.Path != "/api/v1/node/self/send/key" || r.Method != http.MethodPut {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		var body struct {
			PublicKey JWK `json:"public_key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.PublicKey.D != "" || body.PublicKey.X == "" {
			t.Errorf("registered key includes private data or is empty: %#v", body.PublicKey)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"node_id":"nod_test","key_thumbprint":"thumb"}`))
	}))
	defer server.Close()
	key, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{BaseURL: server.URL, Tokens: staticToken("node-token"), HTTP: server.Client()}
	if err := c.Register(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("made %d calls", calls)
	}
}
