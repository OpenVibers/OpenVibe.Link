package send

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestGrantedLANRequiresLiveSendGrant(t *testing.T) {
	lan := NewLAN("lnk_target")
	secret := []byte("same-owner-lan-secret-123456")
	if err := lan.SetSecret(base64.RawURLEncoding.EncodeToString(secret)); err != nil {
		t.Fatal(err)
	}
	var allowed atomic.Bool
	allowed.Store(true)
	lan.SetGrantCheck(func(_ context.Context, token, sender string) error {
		if allowed.Load() && token == "signed-grant" && sender == "lnk_sender" {
			return nil
		}
		return ErrGrantTest{}
	})
	server := httptest.NewServer(lan.handler())
	defer server.Close()
	post := func(token string) int {
		t.Helper()
		at := time.Now().Unix()
		nonce := base64.RawURLEncoding.EncodeToString(random(16))
		envelope := "sealed-file"
		body, _ := json.Marshal(lanRequest{Sender: "lnk_sender", Target: "lnk_target", Envelope: envelope, At: at, Nonce: nonce,
			Mac: lanMAC(secret, "lnk_sender", "lnk_target", at, nonce, envelope), GrantToken: token})
		resp, err := http.Post(server.URL+lanGrantPath, "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	if got := post(""); got != http.StatusForbidden {
		t.Fatalf("missing grant: %d", got)
	}
	if got := post("signed-grant"); got != http.StatusCreated {
		t.Fatalf("live grant: %d", got)
	}
	allowed.Store(false)
	if got := post("signed-grant"); got != http.StatusForbidden {
		t.Fatalf("revoked grant: %d", got)
	}
	if len(lan.Items()) != 1 {
		t.Fatal("refused transfer reached the inbox")
	}
}

type ErrGrantTest struct{}

func (ErrGrantTest) Error() string { return "grant refused" }
