package send

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const lanTarget = "lnk_01JAB2C3D4E5F6G7H8J9K0MNPQ"

func startTestLAN(t *testing.T) (*LAN, string) {
	t.Helper()
	lan := NewLAN(lanTarget)
	if err := lan.Listen("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lan.Close() })
	secret := strings.Repeat("s", 32)
	if err := lan.SetSecret(base64.RawURLEncoding.EncodeToString([]byte(secret))); err != nil {
		t.Fatal(err)
	}
	return lan, secret
}

func lanBody(sender, secret, envelope string) []byte {
	at := time.Now().Unix()
	nonce := hex.EncodeToString(random(16))
	b, _ := json.Marshal(lanRequest{Sender: sender, Target: lanTarget, Envelope: envelope, At: at, Nonce: nonce,
		Mac: lanMAC([]byte(secret), sender, lanTarget, at, nonce, envelope)})
	return b
}

func postLAN(t *testing.T, url string, body []byte) int {
	t.Helper()
	resp, err := http.Post(url+lanPath, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// A machine refuses a sender that does not hold the owner's LAN secret, and it refuses a replayed transfer even when
// it does. The accepted envelope is the same ciphertext the sender made: the listener never sees a key.
func TestLANRefusesANonOwnerAndReplayedTransfer(t *testing.T) {
	lan, secret := startTestLAN(t)
	url := "http://" + lan.ln.Addr().String()
	envelope := base64.RawStdEncoding.EncodeToString([]byte("sealed bytes"))
	if code := postLAN(t, url, lanBody("lnk_01JAB2C3D4E5F6G7H8J9K0MNPZ", "not-the-owner-secret", envelope)); code != http.StatusForbidden {
		t.Fatalf("a sender without the owner secret got %d, want 403", code)
	}
	if got := len(lan.Items()); got != 0 {
		t.Fatalf("a refused sender put %d items in the inbox", got)
	}
	body := lanBody("lnk_01JAB2C3D4E5F6G7H8J9K0MNPZ", secret, envelope)
	if code := postLAN(t, url, body); code != http.StatusCreated {
		t.Fatalf("the owner's machine got %d, want 201", code)
	}
	if code := postLAN(t, url, body); code != http.StatusConflict {
		t.Fatalf("a replayed transfer got %d, want 409", code)
	}
	items := lan.Items()
	if len(items) != 1 || items[0].Envelope != envelope {
		t.Fatalf("stored items = %+v", items)
	}
	if got, err := lan.Accept(items[0].ID); err != nil || got != envelope {
		t.Fatalf("accept = %q, %v", got, err)
	}
}

// A sender on the same network hands the sealed item straight to the target: the relay is not called for the upload.
func TestSendUsesLANBeforeTheRelay(t *testing.T) {
	lan, secret := startTestLAN(t)
	relayCalls := 0
	network := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/link/self/send/targets":
			relayCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"machines": []map[string]any{{
				"id": lanTarget, "name": "Workshop", "online": true,
				"public_key": map[string]string{"kty": "EC", "crv": "P-256", "x": "x", "y": "y"},
				"lan":        []string{"http://" + lan.ln.Addr().String()},
			}}})
		case "/api/v1/link/self/send/lan-key":
			relayCalls++
			_ = json.NewEncoder(w).Encode(map[string]string{"key": base64.RawURLEncoding.EncodeToString([]byte(secret))})
		default:
			relayCalls += 100
			t.Errorf("the relay was asked to upload over %s %s", r.Method, r.URL.Path)
		}
	}))
	defer network.Close()
	c := &Client{BaseURL: network.URL, Tokens: staticToken("node-token"), HTTP: network.Client(),
		Self: "lnk_01JAB2C3D4E5F6G7H8J9K0MNPZ"}
	envelope := base64.RawStdEncoding.EncodeToString([]byte(strings.Repeat("sealed", 100)))
	if err := c.Send(context.Background(), lanTarget, envelope); err != nil {
		t.Fatal(err)
	}
	if relayCalls != 2 {
		t.Fatalf("relay calls = %d, want only the target list and the LAN secret", relayCalls)
	}
	items := lan.Items()
	if len(items) != 1 || items[0].Envelope != envelope {
		t.Fatalf("the direct transfer stored %+v", items)
	}
}
