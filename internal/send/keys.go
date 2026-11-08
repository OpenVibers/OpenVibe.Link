package send

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
)

// JWK is the P-256 receive key shape used by openvibe-sdk/locker. The private fields stay in the Node's 0600 config
// file and are returned only to its token-protected loopback panel so the user can open an accepted envelope.
type JWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
	D   string `json:"d,omitempty"`
}

func LoadOrCreate(dir string) (JWK, error) {
	path := filepath.Join(dir, "link-send-key.json")
	if b, err := os.ReadFile(path); err == nil {
		info, statErr := os.Lstat(path)
		if statErr != nil || !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0) {
			return JWK{}, errors.New("the saved Link receive key must be a regular file accessible only to this account")
		}
		var key JWK
		if json.Unmarshal(b, &key) != nil || !valid(key) {
			return JWK{}, errors.New("the saved Link receive key is invalid")
		}
		return key, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return JWK{}, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return JWK{}, err
	}
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return JWK{}, err
	}
	key := JWK{Kty: "EC", Crv: "P-256", X: b64(private.X), Y: b64(private.Y), D: b64(private.D)}
	b, err := json.Marshal(key)
	if err != nil {
		return JWK{}, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return LoadOrCreate(dir)
		}
		return JWK{}, err
	}
	if _, err = f.Write(append(b, '\n')); err != nil {
		_ = f.Close()
		return JWK{}, err
	}
	if err = f.Close(); err != nil {
		return JWK{}, err
	}
	return key, nil
}

func Public(key JWK) JWK { key.D = ""; return key }

func valid(key JWK) bool {
	if key.Kty != "EC" || key.Crv != "P-256" || key.D == "" {
		return false
	}
	x, errX := unb64(key.X)
	y, errY := unb64(key.Y)
	d, errD := unb64(key.D)
	curve := elliptic.P256()
	if errX != nil || errY != nil || errD != nil || len(x) != 32 || len(y) != 32 || len(d) != 32 {
		return false
	}
	return curve.IsOnCurve(new(big.Int).SetBytes(x), new(big.Int).SetBytes(y))
}

func b64(n *big.Int) string {
	raw := make([]byte, 32)
	n.FillBytes(raw)
	return base64.RawURLEncoding.EncodeToString(raw)
}
func unb64(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }
