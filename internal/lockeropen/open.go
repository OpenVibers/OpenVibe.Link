// Package lockeropen opens the people lock emitted by openvibe-sdk/locker for Link Send recipients.
// Keep the byte format and vectors aligned with OpenVibe.SDK's locker implementation.
package lockeropen

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"
)

const lockerVersion = "locker.envelope.v1"
const suite = "AES-256-GCM"
const chunkSize = 1 << 20
const maxBytes = 8 << 20

type JWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
	D   string `json:"d"`
}
type Subject struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}
type peopleSlot struct {
	Subject      Subject `json:"subject"`
	Algorithm    string  `json:"algorithm"`
	RecipientKey string  `json:"recipient_key"`
	Ephemeral    JWK     `json:"ephemeral_public_key"`
	Salt         string  `json:"salt"`
	Nonce        string  `json:"nonce"`
	Wrapped      string  `json:"wrapped_key"`
}
type header struct {
	Version    string `json:"version"`
	Suite      string `json:"suite"`
	ChunkSize  int    `json:"chunk_size"`
	PaddedSize int    `json:"padded_size"`
	NonceBase  string `json:"nonce_base"`
	Lock       string `json:"lock"`
	Slots      struct {
		People []peopleSlot `json:"people"`
	} `json:"slots"`
}

// Open validates and opens an SDK Locker people envelope addressed to the Node's Link Send key.
func Open(envelope []byte, private JWK, recipient Subject) ([]byte, error) {
	if len(envelope) < 8 || len(envelope) > maxBytes || string(envelope[:4]) != "OVLK" {
		return nil, errors.New("invalid locker envelope")
	}
	headerLen := int(binary.BigEndian.Uint32(envelope[4:8]))
	if headerLen < 2 || headerLen > 65536 || 8+headerLen > len(envelope) {
		return nil, errors.New("invalid locker header")
	}
	var h header
	if json.Unmarshal(envelope[8:8+headerLen], &h) != nil {
		return nil, errors.New("invalid locker header")
	}
	if h.Version != lockerVersion || h.Suite != suite || h.ChunkSize != chunkSize || h.Lock != "people" || h.PaddedSize < 256 || h.PaddedSize > maxBytes {
		return nil, errors.New("unsupported locker envelope")
	}
	contentKey, err := unwrap(h.Slots.People, private, recipient)
	if err != nil {
		return nil, err
	}
	defer wipe(contentKey)
	block, err := aes.NewCipher(contentKey)
	if err != nil {
		return nil, errors.New("invalid locker key")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.New("invalid locker cipher")
	}
	core := map[string]any{"version": h.Version, "suite": h.Suite, "chunk_size": h.ChunkSize, "padded_size": h.PaddedSize, "nonce_base": h.NonceBase, "lock": h.Lock}
	aadCore, _ := json.Marshal(core)
	nonceBase, err := base64.StdEncoding.DecodeString(h.NonceBase)
	if err != nil || len(nonceBase) != 8 {
		return nil, errors.New("invalid locker nonce")
	}
	data, offset := envelope, 8+headerLen
	counter := uint32(0)
	expectedChunks := (h.PaddedSize + chunkSize - 1) / chunkSize
	plain := make([]byte, 0, h.PaddedSize)
	defer wipe(plain)
	var expected int64 = -1
	finalSeen := false
	for offset < len(data) {
		if int(counter) >= expectedChunks {
			return nil, errors.New("too many locker chunks")
		}
		if offset+17 > len(data) {
			return nil, errors.New("truncated locker chunk")
		}
		n := int(binary.BigEndian.Uint32(data[offset : offset+4]))
		flags := data[offset+4]
		nonce := data[offset+5 : offset+17]
		offset += 17
		if n < gcm.Overhead() || n > chunkSize+gcm.Overhead() || offset+n > len(data) {
			return nil, errors.New("invalid locker chunk size")
		}
		wantNonce := make([]byte, 12)
		copy(wantNonce, nonceBase)
		binary.BigEndian.PutUint32(wantNonce[8:], counter)
		isFinal := int(counter) == expectedChunks-1
		if !equal(nonce, wantNonce) || flags&^byte(1) != 0 || (flags&1 == 1) != isFinal {
			return nil, errors.New("invalid locker chunk metadata")
		}
		aad := make([]byte, len(aadCore)+5)
		copy(aad, aadCore)
		binary.BigEndian.PutUint32(aad[len(aadCore):], counter)
		aad[len(aadCore)+4] = flags
		part, e := gcm.Open(nil, nonce, data[offset:offset+n], aad)
		if e != nil {
			return nil, errors.New("locker envelope authentication failed")
		}
		offset += n
		if counter == 0 {
			if len(part) < 8 {
				return nil, errors.New("invalid locker length")
			}
			v := binary.BigEndian.Uint64(part[:8])
			if v > uint64(h.PaddedSize-8) || v > math.MaxInt32 {
				wipe(part)
				return nil, errors.New("invalid locker length")
			}
			expected = int64(v)
			part = part[8:]
		}
		plain = append(plain, part...)
		wipe(part)
		counter++
		if flags&1 != 0 {
			finalSeen = true
			if offset != len(data) {
				return nil, errors.New("trailing locker data")
			}
			break
		}
	}
	if !finalSeen || int(counter) != expectedChunks || expected < 0 || len(plain) != h.PaddedSize-8 {
		return nil, errors.New("truncated locker content")
	}
	result := append([]byte(nil), plain[:expected]...)
	return result, nil
}

func unwrap(slots []peopleSlot, key JWK, recipient Subject) ([]byte, error) {
	if key.Kty != "EC" || key.Crv != "P-256" {
		return nil, errors.New("invalid Link Send private key")
	}
	d, err := base64.RawURLEncoding.DecodeString(key.D)
	if err != nil || len(d) != 32 {
		return nil, errors.New("invalid Link Send private key")
	}
	priv, err := ecdh.P256().NewPrivateKey(d)
	if err != nil {
		return nil, errors.New("invalid Link Send private key")
	}
	pubJSON, _ := json.Marshal(map[string]string{"crv": "P-256", "kty": "EC", "x": key.X, "y": key.Y})
	thumb := sha256.Sum256(pubJSON)
	recipientKey := base64.RawURLEncoding.EncodeToString(thumb[:])
	for _, slot := range slots {
		if slot.Subject != recipient || slot.RecipientKey != recipientKey || slot.Algorithm != "ECDH-P256/HKDF-SHA256/AES-256-GCM" {
			continue
		}
		x, ex := base64.RawURLEncoding.DecodeString(slot.Ephemeral.X)
		y, ey := base64.RawURLEncoding.DecodeString(slot.Ephemeral.Y)
		if ex != nil || ey != nil || slot.Ephemeral.Kty != "EC" || slot.Ephemeral.Crv != "P-256" || len(x) != 32 || len(y) != 32 {
			continue
		}
		point := elliptic.Marshal(elliptic.P256(), newInt(x), newInt(y))
		pub, e := ecdh.P256().NewPublicKey(point)
		if e != nil {
			continue
		}
		shared, e := priv.ECDH(pub)
		if e != nil {
			continue
		}
		salt, e1 := base64.StdEncoding.DecodeString(slot.Salt)
		nonce, e2 := base64.StdEncoding.DecodeString(slot.Nonce)
		wrapped, e3 := base64.StdEncoding.DecodeString(slot.Wrapped)
		if e1 != nil || e2 != nil || e3 != nil || len(nonce) != 12 || len(wrapped) != 48 {
			wipe(shared)
			continue
		}
		info := []byte(fmt.Sprintf("openvibe-locker people-slot v1|%s:%s|%s", recipient.Type, recipient.ID, recipientKey))
		wrapKey, e := hkdf.Key(sha256.New, shared, salt, string(info), 32)
		wipe(shared)
		if e != nil {
			continue
		}
		ephemeralJSON, _ := json.Marshal(map[string]string{"crv": "P-256", "kty": "EC", "x": slot.Ephemeral.X, "y": slot.Ephemeral.Y})
		aad := []byte(fmt.Sprintf("openvibe-locker people-slot v1|%s:%s|%s|%s|%s", recipient.Type, recipient.ID, recipientKey, ephemeralJSON, base64.StdEncoding.EncodeToString(salt)))
		block, _ := aes.NewCipher(wrapKey)
		gcm, _ := cipher.NewGCM(block)
		content, e := gcm.Open(nil, nonce, wrapped, aad)
		wipe(wrapKey)
		if e == nil && len(content) == 32 {
			return content, nil
		}
		wipe(content)
	}
	return nil, errors.New("locker envelope is not addressed to this machine")
}

func newInt(b []byte) *big.Int { return new(big.Int).SetBytes(b) }
func equal(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var x byte
	for i := range a {
		x |= a[i] ^ b[i]
	}
	return x == 0
}
func wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// DecodeEnvelope accepts the base64 form sent in the JSON grant.
func DecodeEnvelope(s string) ([]byte, error) {
	if strings.ContainsAny(s, "\r\n\t ") || len(s) > maxBytes*2 {
		return nil, errors.New("invalid locker envelope encoding")
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) > maxBytes {
		return nil, errors.New("invalid locker envelope encoding")
	}
	return b, nil
}
