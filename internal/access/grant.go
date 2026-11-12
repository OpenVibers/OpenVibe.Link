// Package access verifies machine grants independently of Bot's relay checks.
package access

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

var ErrGrant = errors.New("machine access grant refused")

var scopes = map[string]bool{"agent": true, "send": true, "screen": true, "desk": true, "computer": true, "worker": true}

// Claims are the Network-signed link.access-grant-token@1 claims. A grant id is
// required so a revocation can end a still-unexpired grant immediately.
type Claims struct {
	ID     string   `json:"grant_id"`
	JTI    string   `json:"jti"`
	Issuer string   `json:"iss"`
	From   string   `json:"sub"`
	To     string   `json:"aud"`
	Scopes []string `json:"scopes"`
	Expiry int64    `json:"exp"`
	Issued int64    `json:"iat"`
	Before int64    `json:"nbf,omitempty"`
}

// Verifier pins Network's signing keys to the configured HTTPS origin and
// caches them briefly. Revocations are kept until the JWT would expire.
type Verifier struct {
	Network string
	Machine string
	Client  *http.Client
	Now     func() time.Time

	mu      sync.Mutex
	keys    map[string]*rsa.PublicKey
	keysAt  time.Time
	revoked map[string]time.Time
}

func NewVerifier(network, machine string) *Verifier {
	return &Verifier{Network: strings.TrimRight(network, "/"), Machine: machine, Client: &http.Client{Timeout: 5 * time.Second}, Now: time.Now, revoked: map[string]time.Time{}}
}

func (v *Verifier) Revoke(id string, until time.Time) {
	if id == "" {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.revoked == nil {
		v.revoked = map[string]time.Time{}
	}
	v.revoked[id] = until
}

// Verify checks every use, including the requested scope and the sending
// machine. A malformed token or a key fetch failure never opens access.
func (v *Verifier) Verify(ctx context.Context, token, sender, scope string) (Claims, error) {
	if !scopes[scope] {
		return Claims{}, ErrGrant
	}
	return v.verify(ctx, token, sender, scope)
}

// VerifyAny checks a grant token without selecting one scope: Bot's relay.opened
// names the grant's scopes, and each later relay.frame selects one.
func (v *Verifier) VerifyAny(ctx context.Context, token, sender string) (Claims, error) {
	return v.verify(ctx, token, sender, "")
}

func (v *Verifier) verify(ctx context.Context, token, sender, scope string) (Claims, error) {
	var c Claims
	if token == "" || sender == "" || sender == v.Machine || v.Machine == "" {
		return c, ErrGrant
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || len(token) > 16<<10 {
		return c, ErrGrant
	}
	header, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(header) > 2048 {
		return c, ErrGrant
	}
	var h struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
		Typ string `json:"typ"`
	}
	if json.Unmarshal(header, &h) != nil || h.Alg != "RS256" || h.Kid == "" {
		return c, ErrGrant
	}
	key, err := v.key(ctx, h.Kid)
	if err != nil {
		return c, fmt.Errorf("%w: signing key unavailable", ErrGrant)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return c, ErrGrant
	}
	hash := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(key, crypto.SHA256, hash[:], sig) != nil {
		return c, ErrGrant
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(payload) > 8<<10 || json.Unmarshal(payload, &c) != nil {
		return Claims{}, ErrGrant
	}
	now := v.Now()
	if c.ID == "" || c.JTI == "" || c.From != sender || c.To != v.Machine || c.Expiry <= now.Unix() || c.Before > now.Unix() || c.Issued > now.Unix() ||
		(c.Issuer != v.Network && c.Issuer != strings.TrimPrefix(v.Network, "https://")) {
		return Claims{}, ErrGrant
	}
	found := scope == ""
	for _, s := range c.Scopes {
		if !scopes[s] {
			return Claims{}, ErrGrant
		}
		found = found || s == scope
	}
	if !found {
		return Claims{}, ErrGrant
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if until, ok := v.revoked[c.ID]; ok && (until.IsZero() || now.Before(until)) {
		return Claims{}, ErrGrant
	}
	return c, nil
}

func (v *Verifier) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	if v.keys != nil && v.Now().Sub(v.keysAt) < 5*time.Minute {
		key := v.keys[kid]
		v.mu.Unlock()
		if key != nil {
			return key, nil
		}
		// A signing-key rotation can introduce a new kid before the cache TTL.
	}
	v.mu.Unlock()
	u, parseErr := url.Parse(v.Network)
	if parseErr != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") ||
		(u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1"))) {
		return nil, ErrGrant
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.Network+"/api/.well-known/jwks", nil)
	if err != nil {
		return nil, err
	}
	client := v.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, ErrGrant
	}
	var doc struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			Alg string `json:"alg"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&doc) != nil {
		return nil, ErrGrant
	}
	keys := map[string]*rsa.PublicKey{}
	for _, item := range doc.Keys {
		if item.Kid == "" || item.Kty != "RSA" || item.Alg != "RS256" {
			continue
		}
		n, ne := base64.RawURLEncoding.DecodeString(item.N)
		e, ee := base64.RawURLEncoding.DecodeString(item.E)
		if ne != nil || ee != nil || len(n) < 256 || len(n) > 512 || len(e) == 0 || len(e) > 4 {
			continue
		}
		exponent := new(big.Int).SetBytes(e).Int64()
		if exponent < 3 || exponent > 1<<31-1 || exponent%2 == 0 {
			continue
		}
		keys[item.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(exponent)}
	}
	v.mu.Lock()
	v.keys, v.keysAt = keys, v.Now()
	key := keys[kid]
	v.mu.Unlock()
	if key == nil {
		return nil, ErrGrant
	}
	return key, nil
}
