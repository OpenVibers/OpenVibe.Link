package send

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestReceiveKeyPersistsPrivatelyAndPublishesOnlyPublicFields(t *testing.T) {
	dir := t.TempDir()
	key, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if key.Kty != "EC" || key.Crv != "P-256" || key.D == "" {
		t.Fatalf("bad private key: %#v", key)
	}
	info, err := os.Stat(filepath.Join(dir, "link-send-key.json"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("key mode = %o, want 600", info.Mode().Perm())
	}
	public := Public(key)
	if public.D != "" || public.X != key.X || public.Y != key.Y {
		t.Fatalf("bad public projection: %#v", public)
	}
	loaded, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded != key {
		t.Fatal("receive key changed across restart")
	}
}

func TestReceiveKeyRejectsMalformedSavedKey(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "link-send-key.json"), []byte(`{"kty":"RSA"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(dir); err == nil {
		t.Fatal("accepted a malformed saved key")
	}
}

func TestReceiveKeyRejectsBroadPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows file modes do not expose POSIX group and other permissions")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "link-send-key.json"), []byte(`{"kty":"EC","crv":"P-256","x":"x","y":"y","d":"d"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(dir); err == nil {
		t.Fatal("accepted a receive key file readable by other accounts")
	}
}
