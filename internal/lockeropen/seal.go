// Seal writes the people lock emitted by openvibe-sdk/locker. The format is the SDK's: every byte here matches
// what the SDK would write for the same key, salt, nonce and content (seal_test.go checks the shared vector).
package lockeropen

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"math/bits"
)

const (
	minPaddedSize = 256
	finalFlag     = byte(1)
	saltBytes     = 16
	keyBytes      = 32
	nonceBaseSize = 8
	nonceSize     = 12
)

// SealOptions fixes the values a test vector needs to be the same bytes every time. A caller sealing a real item
// leaves them empty and gets fresh random ones.
type SealOptions struct {
	ContentKey []byte
	NonceBase  []byte
	// EphemeralPrivate is the envelope's one ephemeral P-256 private key as a 32-byte scalar.
	EphemeralPrivate []byte
	Salt             []byte
	WrapNonce        []byte
	// Random is the source for whatever was not given above. Nil means crypto/rand.
	Random io.Reader
}

// Seal builds a people-locked envelope addressed to one recipient. It is the sender's half of Open: a machine
// that holds the private key for public opens it, and nobody else can.
func Seal(content []byte, recipient Subject, public JWK, opt SealOptions) ([]byte, error) {
	if public.Kty != "EC" || public.Crv != "P-256" || public.X == "" || public.Y == "" {
		return nil, errors.New("the receiving machine has no P-256 key")
	}
	if len(content) > maxBytes {
		return nil, errors.New("the item is too large to seal")
	}
	rnd := opt.Random
	if rnd == nil {
		rnd = rand.Reader
	}
	read := func(n int, given []byte) ([]byte, error) {
		if given != nil {
			if len(given) != n {
				return nil, errors.New("invalid seal value")
			}
			return given, nil
		}
		b := make([]byte, n)
		if _, err := io.ReadFull(rnd, b); err != nil {
			return nil, err
		}
		return b, nil
	}
	contentKey, err := read(keyBytes, opt.ContentKey)
	if err != nil {
		return nil, err
	}
	nonceBase, err := read(nonceBaseSize, opt.NonceBase)
	if err != nil {
		return nil, err
	}
	salt, err := read(saltBytes, opt.Salt)
	if err != nil {
		return nil, err
	}
	wrapNonce, err := read(nonceSize, opt.WrapNonce)
	if err != nil {
		return nil, err
	}
	ephemeral, err := ephemeralKey(opt.EphemeralPrivate, rnd)
	if err != nil {
		return nil, err
	}
	recipientKey, err := thumbprint(public)
	if err != nil {
		return nil, err
	}
	shared, err := ephemeral.ECDH(recipientPublic(public))
	if err != nil {
		return nil, errors.New("the receiving machine has no usable P-256 key")
	}
	defer wipe(shared)
	ephemeralJWK := ephemeralPublicJwk(ephemeral)
	ephemeralJSON, _ := json.Marshal(map[string]string{"crv": "P-256", "kty": "EC", "x": ephemeralJWK.X, "y": ephemeralJWK.Y})
	info := []byte(fmt.Sprintf("openvibe-locker people-slot v1|%s:%s|%s", recipient.Type, recipient.ID, recipientKey))
	wrapKey, err := hkdf.Key(sha256.New, shared, salt, string(info), keyBytes)
	if err != nil {
		return nil, err
	}
	defer wipe(wrapKey)
	aad := []byte(fmt.Sprintf("openvibe-locker people-slot v1|%s:%s|%s|%s|%s", recipient.Type, recipient.ID, recipientKey, ephemeralJSON, base64.StdEncoding.EncodeToString(salt)))
	block, err := aes.NewCipher(wrapKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	wrapped := gcm.Seal(nil, wrapNonce, contentKey, aad)

	padded := paddedSize(len(content))
	header := map[string]any{
		"version":     lockerVersion,
		"suite":       suite,
		"chunk_size":  chunkSize,
		"padded_size": padded,
		"nonce_base":  base64.StdEncoding.EncodeToString(nonceBase),
		"lock":        "people",
		"slots": map[string]any{"people": []any{map[string]any{
			"subject":              map[string]string{"type": recipient.Type, "id": recipient.ID},
			"algorithm":            "ECDH-P256/HKDF-SHA256/AES-256-GCM",
			"recipient_key":        recipientKey,
			"ephemeral_public_key": map[string]string{"kty": "EC", "crv": "P-256", "x": ephemeralJWK.X, "y": ephemeralJWK.Y},
			"salt":                 base64.StdEncoding.EncodeToString(salt),
			"nonce":                base64.StdEncoding.EncodeToString(wrapNonce),
			"wrapped_key":          base64.StdEncoding.EncodeToString(wrapped),
		}}},
	}
	headerBytes, err := json.Marshal(header)
	if err != nil {
		return nil, err
	}
	core, err := json.Marshal(map[string]any{
		"version":     lockerVersion,
		"suite":       suite,
		"chunk_size":  chunkSize,
		"padded_size": padded,
		"nonce_base":  base64.StdEncoding.EncodeToString(nonceBase),
		"lock":        "people",
	})
	if err != nil {
		return nil, err
	}
	plain := make([]byte, padded)
	binary.BigEndian.PutUint64(plain[:8], uint64(len(content)))
	copy(plain[8:], content)
	defer wipe(plain)

	block, err = aes.NewCipher(contentKey)
	if err != nil {
		return nil, err
	}
	gcm, err = cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, 8+len(headerBytes)+len(plain)+gcm.Overhead())
	out = append(out, "OVLK"...)
	out = binary.BigEndian.AppendUint32(out, uint32(len(headerBytes)))
	out = append(out, headerBytes...)
	count := (padded + chunkSize - 1) / chunkSize
	for i := 0; i < count; i++ {
		start := i * chunkSize
		end := start + chunkSize
		if end > padded {
			end = padded
		}
		flags := byte(0)
		if i == count-1 {
			flags = finalFlag
		}
		nonce := make([]byte, nonceSize)
		copy(nonce, nonceBase)
		binary.BigEndian.PutUint32(nonce[8:], uint32(i))
		chunkAad := make([]byte, len(core)+5)
		copy(chunkAad, core)
		binary.BigEndian.PutUint32(chunkAad[len(core):], uint32(i))
		chunkAad[len(core)+4] = flags
		ciphertext := gcm.Seal(nil, nonce, plain[start:end], chunkAad)
		out = binary.BigEndian.AppendUint32(out, uint32(len(ciphertext)))
		out = append(out, flags)
		out = append(out, nonce...)
		out = append(out, ciphertext...)
	}
	return out, nil
}

// Addressed reports whether an envelope names this machine's public key in a people slot, so Link can refuse an
// item encrypted for another machine before it asks the person anything.
func Addressed(envelope []byte, public JWK) bool {
	h, _, err := readHeader(envelope)
	if err != nil {
		return false
	}
	want, err := thumbprint(public)
	if err != nil {
		return false
	}
	for _, slot := range h.Slots.People {
		if slot.RecipientKey == want && slot.Algorithm == "ECDH-P256/HKDF-SHA256/AES-256-GCM" {
			return true
		}
	}
	return false
}

func readHeader(envelope []byte) (header, int, error) {
	var h header
	if len(envelope) < 8 || len(envelope) > maxBytes || string(envelope[:4]) != "OVLK" {
		return h, 0, errors.New("invalid locker envelope")
	}
	headerLen := int(binary.BigEndian.Uint32(envelope[4:8]))
	if headerLen < 2 || headerLen > 65536 || 8+headerLen > len(envelope) {
		return h, 0, errors.New("invalid locker header")
	}
	if json.Unmarshal(envelope[8:8+headerLen], &h) != nil {
		return h, 0, errors.New("invalid locker header")
	}
	return h, headerLen, nil
}

func ephemeralKey(scalar []byte, rnd io.Reader) (*ecdh.PrivateKey, error) {
	if scalar != nil {
		if len(scalar) != 32 {
			return nil, errors.New("invalid ephemeral key")
		}
		return ecdh.P256().NewPrivateKey(scalar)
	}
	return ecdh.P256().GenerateKey(rnd)
}

func recipientPublic(public JWK) *ecdh.PublicKey {
	x, errX := base64.RawURLEncoding.DecodeString(public.X)
	y, errY := base64.RawURLEncoding.DecodeString(public.Y)
	if errX != nil || errY != nil || len(x) != 32 || len(y) != 32 {
		return nil
	}
	key, err := ecdh.P256().NewPublicKey(elliptic.Marshal(elliptic.P256(), new(big.Int).SetBytes(x), new(big.Int).SetBytes(y)))
	if err != nil {
		return nil
	}
	return key
}

func ephemeralPublicJwk(key *ecdh.PrivateKey) JWK {
	pub := key.PublicKey().Bytes() // uncompressed point: 0x04 || X || Y
	return JWK{
		Kty: "EC", Crv: "P-256",
		X: base64.RawURLEncoding.EncodeToString(pub[1:33]),
		Y: base64.RawURLEncoding.EncodeToString(pub[33:]),
	}
}

func thumbprint(public JWK) (string, error) {
	jwk, err := json.Marshal(map[string]string{"crv": "P-256", "kty": "EC", "x": public.X, "y": public.Y})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(jwk)
	return base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

func paddedSize(size int) int {
	p := padme(8 + size)
	if p < minPaddedSize {
		return minPaddedSize
	}
	return p
}

// padme is Padmé's grid (the SDK's padme.js): round up to the next size the band allows.
func padme(size int) int {
	if size <= 0 {
		return 0
	}
	if size == 1 {
		return 1
	}
	band := bits.Len(uint(size)) - 1
	steps := bits.Len(uint(band))
	step := 1 << (band - steps)
	return ((size + step - 1) / step) * step
}
