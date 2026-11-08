package link

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestConnectTokenRequestsLinkAudience(t *testing.T) {
	_, _, creds := networkPair(t)
	var audience string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		audience = r.Form.Get("audience")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"test-node-token","token_type":"Bearer","expires_in":300}`))
	}))
	defer server.Close()
	creds.Network = server.URL
	source := NewTokenSourceFor(creds, server.Client(), "test", ConnectAudience)
	if _, err := source.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	if audience != "openvibe.link" {
		t.Fatalf("requested audience = %q, want openvibe.link", audience)
	}
}
