package lockeropen

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenSDKLinkSendVector(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "sdk-link-send-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Recipient Subject `json:"recipient"`
		Private   JWK     `json:"private_jwk"`
		Envelope  string  `json:"envelope_base64"`
		Expected  any     `json:"expected"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	envelope, err := base64.StdEncoding.DecodeString(v.Envelope)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := Open(envelope, v.Private, v.Recipient)
	if err != nil {
		t.Fatal(err)
	}
	var got any
	if err := json.Unmarshal(plain, &got); err != nil {
		t.Fatal(err)
	}
	if !jsonEqual(got, v.Expected) {
		t.Fatalf("opened payload differs from SDK vector")
	}
	wipe(plain)
}

func TestOpenRejectsWrongKeyAndTampering(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "sdk-link-send-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Recipient Subject `json:"recipient"`
		Private   JWK     `json:"private_jwk"`
		Envelope  string  `json:"envelope_base64"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	envelope, err := base64.StdEncoding.DecodeString(v.Envelope)
	if err != nil {
		t.Fatal(err)
	}
	wrong := v.Private
	wrong.D = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if _, err := Open(envelope, wrong, v.Recipient); err == nil {
		t.Fatal("wrong private key opened envelope")
	}
	corrupt := append([]byte(nil), envelope...)
	corrupt[len(corrupt)-1] ^= 1
	if _, err := Open(corrupt, v.Private, v.Recipient); err == nil {
		t.Fatal("tampered envelope opened")
	}
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
