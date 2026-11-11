package access

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGrantVerification(t *testing.T) {
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pub := private.PublicKey
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/.well-known/jwks" {
			t.Errorf("path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{
			"kid": "network-1", "kty": "RSA", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1}),
		}}})
	}))
	defer server.Close()
	v := NewVerifier(server.URL, "lnk_target")
	now := time.Unix(2000000000, 0)
	v.Now = func() time.Time { return now }
	sign := func(c Claims) string {
		t.Helper()
		h, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "network-1"})
		p, _ := json.Marshal(c)
		body := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(p)
		digest := sha256.Sum256([]byte(body))
		sig, err := rsa.SignPKCS1v15(rand.Reader, private, crypto.SHA256, digest[:])
		if err != nil {
			t.Fatal(err)
		}
		return body + "." + base64.RawURLEncoding.EncodeToString(sig)
	}
	c := Claims{ID: "gra_12345678", JTI: "jti_12345678", Issuer: server.URL, From: "lnk_sender", To: "lnk_target", Scopes: []string{"agent", "send"}, Issued: now.Unix(), Expiry: now.Add(time.Hour).Unix()}
	token := sign(c)
	if _, err := v.Verify(context.Background(), token, c.From, "send"); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, token, sender, scope string }{
		{"wrong sender", token, "lnk_other", "send"},
		{"scope", token, c.From, "computer"},
		{"unknown scope", token, c.From, "admin"},
		{"tamper", token + "x", c.From, "send"},
		{"expired", sign(Claims{ID: c.ID, JTI: c.JTI, Issuer: c.Issuer, From: c.From, To: c.To, Scopes: c.Scopes, Issued: c.Issued, Expiry: now.Unix() - 1}), c.From, "send"},
		{"other audience", sign(Claims{ID: c.ID, JTI: c.JTI, Issuer: c.Issuer, From: c.From, To: "lnk_other", Scopes: c.Scopes, Issued: c.Issued, Expiry: c.Expiry}), c.From, "send"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := v.Verify(context.Background(), test.token, test.sender, test.scope); err == nil {
				t.Fatal("grant accepted")
			}
		})
	}
	v.Revoke(c.ID, now.Add(time.Hour))
	if _, err := v.Verify(context.Background(), token, c.From, "send"); err == nil {
		t.Fatal("revoked grant accepted")
	}
}
