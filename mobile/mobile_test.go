package mobile

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeNetwork and fakeBot are the two services a phone pairs through, reduced to the answers Pair reads.
func fakeNetwork(t *testing.T, principal, credential, ref string) (*httptest.Server, *[]string) {
	var codes []string
	n := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/link-pairing":
			var body struct {
				Code    string `json:"code"`
				Pairing string `json:"pairing"`
				Name    string `json:"name"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			codes = append(codes, body.Code)
			if body.Pairing != "" && body.Pairing != "pair_01JD2C3D4E5F6G7H8J9K0MNPQR" {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte(`{"code":"registry.invalid_pairing_request","detail":"bad pairing id"}`))
				return
			}
			if body.Code != "ABCD2345" {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"code":"registry.pairing_code_invalid","detail":"that is not the pairing code"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"principal": principal, "credential": credential,
				"paired_for": map[string]string{"service": "bot", "ref": ref},
			})
		case "/oauth/token":
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "node-token", "expires_in": 300})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(n.Close)
	return n, &codes
}

func fakeBot(t *testing.T) *httptest.Server {
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/devices/bind" || r.Header.Get("Authorization") != "Bearer node-token" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"device_id": "dev_phone"})
	}))
	t.Cleanup(b.Close)
	return b
}

func TestPairBindsThePhone(t *testing.T) {
	net, codes := fakeNetwork(t, "lnk_01234567890123456789012345", "node-secret", "phone:usr_01")
	bot := fakeBot(t)
	creds, err := Pair(net.URL, bot.URL, "abcd-2345", "Phone", "")
	if err != nil {
		t.Fatalf("Pair: %v", err)
	}
	if len(*codes) != 1 || (*codes)[0] != "ABCD2345" {
		t.Fatalf("the code sent was %v, want the normalized ABCD2345", *codes)
	}
	var p pairing
	if err := json.Unmarshal([]byte(creds), &p); err != nil {
		t.Fatalf("Pair returned no JSON: %v", err)
	}
	if p.DeviceID != "dev_phone" || p.Principal != "lnk_01234567890123456789012345" || p.LinkCredential != "node-secret" {
		t.Fatalf("pairing = %+v", p)
	}
	if p.Server != bot.URL || p.Network != net.URL {
		t.Fatalf("origins = %q %q", p.Server, p.Network)
	}
	if p.PairedFor == nil || p.PairedFor.Service != "bot" {
		t.Fatalf("paired_for = %+v", p.PairedFor)
	}
}

func TestPairRefusesAWrongCode(t *testing.T) {
	net, _ := fakeNetwork(t, "lnk_01234567890123456789012345", "node-secret", "phone:usr_01")
	bot := fakeBot(t)
	_, err := Pair(net.URL, bot.URL, "ZZZZ-9999", "Phone", "")
	if err == nil || !strings.Contains(err.Error(), "pairing code") {
		t.Fatalf("a wrong code gave %v", err)
	}
}

func TestPairRejectsAMalformedCode(t *testing.T) {
	if _, err := Pair("https://openvibe.network", "https://openvibe.bot", "nope", "Phone", ""); err == nil {
		t.Fatal("a malformed code was accepted")
	}
}

func TestInfoNamesThePhone(t *testing.T) {
	creds := `{"principal":"lnk_01234567890123456789012345","link_credential":"s","network":"https://openvibe.network","device_id":"dev_phone","server":"https://openvibe.bot"}`
	got, err := Info(creds)
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	var out struct {
		DeviceID string `json:"device_id"`
		Kind     string `json:"kind"`
		Bound    bool   `json:"bound"`
	}
	if err := json.Unmarshal([]byte(got), &out); err != nil {
		t.Fatal(err)
	}
	if out.DeviceID != "dev_phone" || out.Kind != "phone" || !out.Bound {
		t.Fatalf("info = %+v", out)
	}
}

func TestNewLinkNeedsAPairing(t *testing.T) {
	if _, err := NewLink(`{}`); err == nil {
		t.Fatal("an empty pairing made a link")
	}
	if _, err := NewLink(`not json`); err == nil {
		t.Fatal("invalid JSON made a link")
	}
}

func TestNewLinkUsesTheBotSocket(t *testing.T) {
	l, err := NewLink(`{"principal":"lnk_01234567890123456789012345","link_credential":"s","network":"https://openvibe.network","server":"https://openvibe.bot"}`)
	if err != nil {
		t.Fatalf("NewLink: %v", err)
	}
	l.Stop() // Stop before Start is harmless.
	if got := l.Status(); !strings.Contains(got, "connected") {
		t.Fatalf("status = %s", got)
	}
}

func TestPairCarriesThePairingIDFromTheQR(t *testing.T) {
	net, _ := fakeNetwork(t, "lnk_01234567890123456789012345", "node-secret", "phone:usr_01")
	bot := fakeBot(t)
	if _, err := Pair(net.URL, bot.URL, "abcd-2345", "Phone", "pair_01JD2C3D4E5F6G7H8J9K0MNPQR"); err != nil {
		t.Fatalf("Pair with a pairing id: %v", err)
	}
	if _, err := Pair(net.URL, bot.URL, "abcd-2345", "Phone", "not-a-pairing"); err == nil {
		t.Fatal("a malformed pairing id was accepted")
	}
}
