package party

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAssignmentAsksChatWithTheMachineToken(t *testing.T) {
	var path, auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"assignment":{"party":"party-0123abcd","hold":"ph_0123456789abcdef","title":"Friday film"}}`)
	}))
	defer srv.Close()
	c := Client{PartyURL: srv.URL, ChatToken: func(context.Context) (string, error) { return "node-token", nil }}
	got, err := c.Assignment(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if path != "/api/chat/parties/desktop/assignment" || auth != "Bearer node-token" {
		t.Fatalf("asked %q with %q", path, auth)
	}
	if got == nil || got.Party != "party-0123abcd" || got.Hold != "ph_0123456789abcdef" || got.Title != "Friday film" {
		t.Fatalf("assignment = %#v", got)
	}
}

func TestAssignmentIsNilWhenThePoolHoldsNothing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"assignment":null}`)
	}))
	defer srv.Close()
	c := Client{PartyURL: srv.URL, ChatToken: func(context.Context) (string, error) { return "node-token", nil }}
	got, err := c.Assignment(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("assignment = %#v, want none", got)
	}
}
