package stream

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/OpenVibers/OpenVibe.Node/internal/credentials"
)

const (
	testTicket = "wpt_0123456789abcdefghijklmnopqrstuvwxyzABCDEFG"
	testID     = "ags_01J8Z4M2Q0R7T9YV3K6N8P1W2X"
)

type recorded struct {
	method, path, auth, ctype string
	body                      []byte
}

func liveFake(t *testing.T, h func(w http.ResponseWriter, r *http.Request, rec recorded)) (*Client, *[]recorded) {
	t.Helper()
	var mu sync.Mutex
	var got []recorded
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rec := recorded{method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization"), ctype: r.Header.Get("Content-Type"), body: b}
		mu.Lock()
		got = append(got, rec)
		mu.Unlock()
		h(w, r, rec)
	}))
	t.Cleanup(srv.Close)
	c, err := NewClient(ClientOptions{BaseURL: srv.URL, Token: func(context.Context) (credentials.Secret, error) {
		return credentials.NewSecret("node-token"), nil
	}, UserAgent: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return c, &got
}

func TestCreateParsesPublishAndNeverLogsTheTicket(t *testing.T) {
	c, got := liveFake(t, func(w http.ResponseWriter, r *http.Request, rec recorded) {
		if rec.method != "POST" || rec.path != "/api/streams/agent" {
			t.Errorf("request %s %s", rec.method, rec.path)
		}
		if rec.auth != "Bearer node-token" {
			t.Errorf("create auth = %q", rec.auth)
		}
		var req CreateRequest
		if err := json.Unmarshal(rec.body, &req); err != nil {
			t.Error(err)
		}
		if req.Title != "fix the tests" || req.Task.Source != "codes" || req.Visibility != "private" {
			t.Errorf("create body = %+v", req)
		}
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"stream":{"id":"`+testID+`","title":"fix the tests","state":"live"},
			"publish":{"whip_url":"https://whip.openvibe.live/whip/agent/`+testID+`","ticket":"`+testTicket+`","expires_at":"2026-11-06T16:00:00Z"}}`)
	})
	res, err := c.Create(context.Background(), CreateRequest{Title: "fix the tests", Visibility: "private",
		Task: Task{Source: "codes", Ref: "job_1", Title: "fix"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stream.ID != testID || res.Publish.WHIPURL == "" {
		t.Fatalf("create = %+v", res)
	}
	if res.Publish.Ticket.Reveal() != testTicket {
		t.Fatalf("ticket = %v", res.Publish.Ticket)
	}
	if s := res.Publish.Ticket.String(); strings.Contains(s, "wpt_") {
		t.Fatalf("Secret prints its value: %q", s)
	}
	// The ticket appears only in the wire answer; nothing this package hands out in an error or status does.
	b, _ := json.Marshal(res)
	if strings.Contains(string(b), "wpt_") {
		t.Fatalf("the ticket reached JSON: %s", b)
	}
	_ = got
}

func TestTicketCallsUseTheTicketAsBearer(t *testing.T) {
	c, got := liveFake(t, func(w http.ResponseWriter, r *http.Request, rec recorded) {
		switch {
		case strings.HasSuffix(rec.path, "/state"):
			io.WriteString(w, `{"viewers":2,"encode":true,"curtain":{"up":false},"ends_at":"2026-11-06T16:00:00Z"}`)
		case rec.method == "POST" && strings.HasSuffix(rec.path, "/ticket"):
			io.WriteString(w, `{"whip_url":"https://whip.openvibe.live/whip/agent/`+testID+`","ticket":"`+testTicket+`","expires_at":"2026-11-06T17:00:00Z"}`)
		case rec.method == "DELETE":
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(rec.path, "/curtain"):
			if string(rec.body) != `{"reason":"manual","up":true}` {
				t.Errorf("curtain body = %s", rec.body)
			}
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(rec.path, "/frame"):
			if rec.ctype != "image/jpeg" || len(rec.body) < 4 {
				t.Errorf("frame = %q %d bytes", rec.ctype, len(rec.body))
			}
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(rec.path, "/end"):
			w.WriteHeader(http.StatusNoContent)
		}
	})
	ctx := context.Background()
	tick := credentials.NewSecret(testTicket)
	if st, err := c.State(ctx, testID, tick); err != nil || st.Viewers != 2 || !st.Encode {
		t.Fatalf("state = %+v %v", st, err)
	}
	if p, err := c.Renew(ctx, testID, tick); err != nil || p.Ticket.Reveal() != testTicket {
		t.Fatalf("renew = %+v %v", p, err)
	}
	if err := c.Revoke(ctx, testID, tick); err != nil {
		t.Fatal(err)
	}
	if err := c.Curtain(ctx, testID, tick, true, "manual"); err != nil {
		t.Fatal(err)
	}
	jpeg := []byte{0xff, 0xd8, 0xff, 0xe0, 0, 0, 0, 0}
	if err := c.PostFrame(ctx, testID, tick, jpeg); err != nil {
		t.Fatal(err)
	}
	if err := c.End(ctx, testID, tick); err != nil {
		t.Fatal(err)
	}
	for _, r := range *got {
		if r.path == "/api/streams/agent" {
			continue
		}
		if r.auth != "Bearer "+testTicket {
			t.Errorf("%s %s auth = %q", r.method, r.path, r.auth)
		}
	}
}

func TestTicketErrors(t *testing.T) {
	c, _ := liveFake(t, func(w http.ResponseWriter, r *http.Request, rec recorded) {
		switch {
		case strings.HasSuffix(rec.path, "/state"):
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"code":"invalid_ticket"}`)
		case strings.HasSuffix(rec.path, "/end"):
			w.WriteHeader(http.StatusConflict)
			io.WriteString(w, `{"code":"already_publishing"}`)
		case strings.HasSuffix(rec.path, "/frame"):
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"code":"invalid_frame","detail":"ticket `+testTicket+`"}`)
		}
	})
	ctx := context.Background()
	tick := credentials.NewSecret(testTicket)
	if _, err := c.State(ctx, testID, tick); !errors.Is(err, ErrTicketInvalid) {
		t.Fatalf("state: %v", err)
	}
	if err := c.End(ctx, testID, tick); !errors.Is(err, ErrAlreadyPublishing) {
		t.Fatalf("end: %v", err)
	}
	err := c.PostFrame(ctx, testID, tick, []byte{0xff, 0xd8, 0xff, 0xe0})
	if err == nil || strings.Contains(err.Error(), "wpt_") {
		t.Fatalf("frame error leaks a ticket: %v", err)
	}
}

func TestCreateValidation(t *testing.T) {
	c, _ := liveFake(t, func(w http.ResponseWriter, r *http.Request, rec recorded) {
		t.Error("invalid request reached Live")
	})
	bad := []CreateRequest{
		{Title: "", Task: Task{Source: "codes", Ref: "r", Title: "t"}, Visibility: "private"},
		{Title: "t", Task: Task{Source: "other", Ref: "r", Title: "t"}, Visibility: "private"},
		{Title: "t", Task: Task{Source: "codes", Ref: "r", Title: "t"}, Visibility: "friends"},
		{Title: "t", Task: Task{Source: "codes", Ref: "r", Title: "t"}, Visibility: "private", AllowSites: []string{"not a url"}},
	}
	for i, r := range bad {
		if _, err := c.Create(context.Background(), r); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
	if err := c.Curtain(context.Background(), testID, credentials.NewSecret(testTicket), true, "because"); err == nil {
		t.Error("an unknown curtain reason was accepted")
	}
	if err := c.PostFrame(context.Background(), testID, credentials.NewSecret(testTicket), make([]byte, MaxFrameBytes+1)); err == nil {
		t.Error("an oversized frame was accepted")
	}
}
