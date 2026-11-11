package send

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPackedItemKeepsItsNameKindAndBytes(t *testing.T) {
	envelope := PackKind("photo (1).png", "image/png", KindClipboard, []byte{0x89, 'P', 'N', 'G'})
	got, err := Unpack(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "photo (1).png" || got.Mime != "image/png" || got.Kind != KindClipboard {
		t.Fatalf("content = %#v", got.Content)
	}
	if string(got.Data) != "\x89PNG" {
		t.Fatalf("data = %q", got.Data)
	}
	if plain, err := Unpack(Pack("note.txt", "text/plain", []byte("hello"))); err != nil || plain.Kind != KindFile {
		t.Fatalf("a v1 item without a kind = %#v, %v", plain, err)
	}
}

func TestUnpackRefusesAnUnknownKindOrBrokenFrame(t *testing.T) {
	bad := Pack("note.txt", "text/plain", []byte("hello"))
	bad[0], bad[1], bad[2], bad[3] = 0, 0, 0, 200
	if _, err := Unpack(bad); err == nil {
		t.Fatal("accepted a frame longer than the item")
	}
	unknown := []byte(`{"name":"x","kind":"bogus"}`)
	frame := make([]byte, 4+len(unknown))
	frame[0], frame[1], frame[2], frame[3] = 0, 0, 0, byte(len(unknown))
	copy(frame[4:], unknown)
	if _, err := Unpack(frame); err == nil {
		t.Fatal("accepted an unknown kind")
	}
}

func TestSafeNameIsOnePathSegment(t *testing.T) {
	cases := map[string]string{
		"../../etc/passwd":     "_.._etc_passwd",
		`C:\Users\me\file.txt`: "C__Users_me_file.txt",
		".hidden":              "hidden",
		"  ":                   "received-file",
		"a\x00b.txt":           "a_b.txt",
	}
	for in, want := range cases {
		if got := SafeName(in); got != want {
			t.Fatalf("SafeName(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("x", 300)
	if got := SafeName(long); len([]rune(got)) != 180 {
		t.Fatalf("a long name = %d runes, want 180", len([]rune(got)))
	}
}

func TestSaveNeverReplacesAFile(t *testing.T) {
	dir := t.TempDir()
	first, err := Save(dir, "report.pdf", []byte("first"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := Save(dir, "report.pdf", []byte("second"))
	if err != nil {
		t.Fatal(err)
	}
	if first == second || filepath.Base(second) != "report (2).pdf" {
		t.Fatalf("paths = %q, %q", first, second)
	}
	b, err := os.ReadFile(second)
	if err != nil || string(b) != "second" {
		t.Fatalf("second file = %q, %v", b, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(second)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("saved mode = %v, %v", info.Mode().Perm(), err)
		}
	}
}

func TestSaveRefusesAnUnwritableDownloadsFolder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the test uses a file where a folder should be")
	}
	dir := t.TempDir()
	blocked := filepath.Join(dir, "not-a-folder")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Save(blocked, "file.txt", []byte("data")); err == nil {
		t.Fatal("saved into a path that is a file")
	}
}
