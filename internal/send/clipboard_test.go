package send

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestClipboardCommandsReadAndWriteText(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the test uses a POSIX shell as the clipboard tool")
	}
	out := filepath.Join(t.TempDir(), "clipboard.txt")
	c := &commandClipboard{
		read:  [][]string{{"sh", "-c", "cat " + out}},
		write: [][]string{{"sh", "-c", "cat > " + out}},
		tool:  "the test clipboard",
	}
	if err := os.WriteFile(out, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := c.Read(context.Background())
	if err != nil || got != "first" {
		t.Fatalf("read = %q, %v", got, err)
	}
	if err := c.Write(context.Background(), "second"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil || string(b) != "second" {
		t.Fatalf("written = %q, %v", b, err)
	}
}

func TestClipboardWithoutAToolSaysSo(t *testing.T) {
	c := &commandClipboard{
		read:  [][]string{{"openvibe-link-no-such-clipboard-tool"}},
		write: [][]string{{"openvibe-link-no-such-clipboard-tool"}},
		tool:  "a clipboard tool",
	}
	if _, err := c.Read(context.Background()); err == nil {
		t.Fatal("read without a tool succeeded")
	}
	if err := c.Write(context.Background(), "x"); err == nil {
		t.Fatal("write without a tool succeeded")
	}
}
