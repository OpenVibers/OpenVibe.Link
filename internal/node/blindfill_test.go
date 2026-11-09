package node

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OpenVibers/OpenVibe.Node/internal/config"
	"github.com/OpenVibers/OpenVibe.Node/internal/credentials"
	"github.com/OpenVibers/OpenVibe.Node/internal/send"
)

func blindFillTestNode(t *testing.T) (*Node, string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "lockeropen", "testdata", "sdk-link-send-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Private   send.JWK `json:"private_jwk"`
		Envelope  string   `json:"envelope_base64"`
		Recipient struct {
			ID string `json:"id"`
		} `json:"recipient"`
		Expected map[string]any `json:"expected"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	key, _ := json.Marshal(v.Private)
	if err := os.WriteFile(filepath.Join(dir, "link-send-key.json"), key, 0o600); err != nil {
		t.Fatal(err)
	}
	n := &Node{opt: Options{Creds: &credentials.Credentials{Principal: v.Recipient.ID}, Paths: config.Paths{ConfigDir: dir}}}
	raw, _ := json.Marshal(map[string]any{"action": "blind_fill", "request_id": v.Expected["request_id"], "envelope": v.Envelope})
	return n, string(raw)
}

func TestPrepareBlindFillOpensSDKGrantAndClearsPayload(t *testing.T) {
	n, raw := blindFillTestNode(t)
	plugin, clear, err := n.prepareBlindFillAt([]byte(raw), time.UnixMilli(1893455970000))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(plugin, &got); err != nil {
		t.Fatal(err)
	}
	if got["action"] != "blind_fill" || got["username"] != "vector-user" || got["password"] != "vector-password" || got["code"] != "123456" || got["submit"] != true {
		t.Fatal("SDK grant did not open as expected")
	}
	clear()
	if strings.Trim(string(plugin), "\x00") != "" {
		t.Fatal("plaintext plugin payload was not cleared")
	}
}

func TestPrepareBlindFillBindsRequestAndRecipient(t *testing.T) {
	n, raw := blindFillTestNode(t)
	for _, mutate := range []func(map[string]any){
		func(v map[string]any) { v["request_id"] = "different" },
		func(v map[string]any) { v["envelope"] = "not-base64" },
	} {
		var v map[string]any
		_ = json.Unmarshal([]byte(raw), &v)
		mutate(v)
		b, _ := json.Marshal(v)
		if _, _, err := n.prepareBlindFill(b); err == nil {
			t.Fatal("mismatched grant was accepted")
		}
	}
	if _, _, err := n.prepareBlindFillAt([]byte(raw), time.UnixMilli(1893456000001)); err == nil {
		t.Fatal("expired grant was accepted")
	}
	for _, bad := range []string{"http://example.org", "https://example.org/path", "https://example.org/", "https://EXAMPLE.org"} {
		if _, ok := exactHTTPOrigin(bad); ok {
			t.Fatalf("invalid origin was accepted: %s", bad)
		}
	}
}
