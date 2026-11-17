//go:build !windows

package video

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OpenVibers/OpenVibe.Link/internal/credentials"
)

func helper(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ov-desk")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// waitFor polls cond every 20 ms until it holds or 5 s pass.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// ov-desk publish (v0.77) speaks JSON lines: the status follows them, the key is in its environment and never in argv.
func TestNativePublisherFollowsTheHelper(t *testing.T) {
	dir := t.TempDir()
	seen := filepath.Join(dir, "seen")
	exe := helper(t, `echo "$OPENVIBE_WHIP_KEY|$*|$XAUTHORITY" > '`+seen+`'
echo '{"state":"connecting"}'
echo '{"state":"live"}'
echo '{"frames":42}'
cat > /dev/null
`)
	p := &NativePublisher{Argv: []string{exe, "publish", "-whip", "https://openre.stream/whip/x"}, Env: []string{"XAUTHORITY=/run/x"}, Key: credentials.NewSecret("pk_secret")}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	waitFor(t, "the helper's status never arrived", func() bool { return p.Frames() == 42 && p.State() == StateLive })
	b, _ := os.ReadFile(seen)
	if got := strings.TrimSpace(string(b)); got != "pk_secret|publish -whip https://openre.stream/whip/x|/run/x" {
		t.Fatalf("the helper saw %q", got)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v after its context ended", err)
	}
	if p.State() != StateOff {
		t.Fatalf("state %s after the end", p.State())
	}
}

// Exit 3 (no UDP path), an older ov-desk (no publish: exit 2 at once) and a helper that never speaks the protocol all
// hand the stream to the Go publisher.
func TestNativePublisherFallsBack(t *testing.T) {
	was := SayWithin
	SayWithin = 300 * time.Millisecond
	defer func() { SayWithin = was }()
	cases := map[string]string{
		"no route":      "echo '{\"state\":\"connecting\"}'\necho '{\"state\":\"failed\",\"error\":\"no route\"}'\nexit 3\n",
		"older ov-desk": "echo usage >&2\nexit 2\n",
		"says nothing":  "head -c 100000 /dev/zero | tr '\\0' 'g'\nexec cat > /dev/null\n",
	}
	for name, body := range cases {
		p := &NativePublisher{Argv: []string{helper(t, body), "publish"}, Key: credentials.NewSecret("k")}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := p.Run(ctx)
		cancel()
		if !errors.Is(err, ErrNativeUnavailable) {
			t.Fatalf("%s: Run = %v, want ErrNativeUnavailable", name, err)
		}
	}
}

// An error line never carries the key.
func TestNativePublisherScrubsTheKey(t *testing.T) {
	exe := helper(t, "echo '{\"state\":\"live\"}'\necho '{\"state\":\"retrying\",\"error\":\"401 for pk_secret\"}'\ncat > /dev/null\n")
	p := &NativePublisher{Argv: []string{exe, "publish"}, Key: credentials.NewSecret("pk_secret")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = p.Run(ctx) }()
	waitFor(t, "no error line", func() bool { return p.LastError() != "" })
	if strings.Contains(p.LastError(), "pk_secret") {
		t.Fatalf("the key is in %q", p.LastError())
	}
}
