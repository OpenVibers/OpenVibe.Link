package lockeropen

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"math/big"
	"testing"
)

// The shared SDK vector: fixed keys, fixed bytes, and the envelope hash the SDK's own test checks. If Go and the
// SDK ever write different bytes for the same input, this fails.
const (
	vectorContent    = "The quick brown fox jumps over the lazy dog."
	vectorEnvelopeID = "a07abdad22e7400831299bb6c363c50241c816bb49c361e555f90bd148690b47"
)

var vectorPublic = JWK{
	Kty: "EC", Crv: "P-256",
	X: "pNngpTSE3X3C_CGzfpAtenjliLPFZ9arBVNtI0ctPXw",
	Y: "fXZT2JcDDiXEb6xH3LUrqlPakM3GQwe3aqbV_1vhVLI",
	D: "cG9YgmeoPeaI8lXZRpijV5--2sC4rYWSqWbyGCYKPu0",
}
var vectorSubject = Subject{Type: "user", ID: "usr_01JAB2C3D4E5F6G7H8J9K0MNPQ"}

// fixedRandom is the SDK's vectors.fixedRandom: the same xorshift32 stream, so the same salt and key bytes.
type fixedRandom struct{ state uint32 }

func (f *fixedRandom) Read(p []byte) (int, error) {
	for i := range p {
		f.state ^= f.state << 13
		f.state ^= f.state >> 17
		f.state ^= f.state << 5
		p[i] = byte(f.state)
	}
	return len(p), nil
}

func TestSealMatchesTheSDKVector(t *testing.T) {
	private, err := base64.RawURLEncoding.DecodeString(vectorPublic.D)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := Seal([]byte(vectorContent), vectorSubject, vectorPublic, SealOptions{
		EphemeralPrivate: private,
		Random:           &fixedRandom{state: 0x1a2b3c4d},
	})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(envelope)
	if got := hex.EncodeToString(sum[:]); got != vectorEnvelopeID {
		t.Fatalf("sealed envelope %s, the SDK writes %s", got, vectorEnvelopeID)
	}
	opened, err := Open(envelope, vectorPublic, vectorSubject)
	if err != nil {
		t.Fatal(err)
	}
	if string(opened) != vectorContent {
		t.Fatalf("opened %q", opened)
	}
}

func TestSealRoundTripForALinkMachine(t *testing.T) {
	key := testKey(t)
	recipient := Subject{Type: "link", ID: "lnk_01JAB2C3D4E5F6G7H8J9K0MNPQ"}
	content := []byte(`{"name":"clipboard.txt"}` + "\x00hello from the other machine")
	envelope, err := Seal(content, recipient, key, SealOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !Addressed(envelope, key) {
		t.Fatal("the envelope does not name its recipient")
	}
	if Addressed(envelope, testKey(t)) {
		t.Fatal("the envelope names a key it was not sealed for")
	}
	opened, err := Open(envelope, key, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if string(opened) != string(content) {
		t.Fatalf("opened %q", opened)
	}
	if _, err := Open(envelope, key, Subject{Type: "link", ID: "lnk_01JAB2C3D4E5F6G7H8J9K0MNPR"}); err == nil {
		t.Fatal("another machine's subject opened the item")
	}
}

func testKey(t *testing.T) JWK {
	t.Helper()
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	b64 := func(n *big.Int) string {
		raw := make([]byte, 32)
		n.FillBytes(raw)
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	return JWK{Kty: "EC", Crv: "P-256", X: b64(private.X), Y: b64(private.Y), D: b64(private.D)}
}
