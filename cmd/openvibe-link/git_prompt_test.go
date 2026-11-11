package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/OpenVibers/OpenVibe.Link/internal/gitrelay"
)

// fakeDesktop is a desktop prompt the test answers with: avail says a prompt
// could be shown, answer is Approve/Deny (nil: could not show it after all),
// and block holds the answer open until closed or the context ends.
type fakeDesktop struct {
	calls  *int
	avail  bool
	answer *bool
	block  chan struct{}
	title  *string
	body   *string
	giveUp *time.Duration
}

func (f *fakeDesktop) available() bool { return f.avail }

func (f *fakeDesktop) ask(ctx context.Context, title, body string, giveUp time.Duration) *bool {
	*f.calls++
	if f.title != nil {
		*f.title, *f.body, *f.giveUp = title, body, giveUp
	}
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return nil
		}
	}
	return f.answer
}

func showDesktop(calls *int, answer *bool) *fakeDesktop {
	return &fakeDesktop{calls: calls, avail: true, answer: answer}
}

func cloneReq() gitrelay.CloneRequest {
	return gitrelay.CloneRequest{ID: "clr_01JQZ8M4T7Y2N5K3B6V9C1X0DA", Repo: "openvibers/demo",
		ExpiresAt: time.Now().Add(90 * time.Second), RequestedBy: "sam"}
}

// The desktop's Approve decides even when the piped input would refuse: a pipe
// is not the person.
func TestDesktopApproveDecides(t *testing.T) {
	var calls int
	var out bytes.Buffer
	approve, _ := cloneApproverWithDesktop(t.TempDir(), false, strings.NewReader(""), &out, showDesktop(&calls, boolp(true)))
	got, err := approve(context.Background(), cloneReq())
	if err != nil || !got.Approve {
		t.Fatalf("approve=%v err=%v", got.Approve, err)
	}
	if calls != 1 {
		t.Fatalf("desktop calls=%d, want 1", calls)
	}
}

// The desktop's Deny refuses; nothing else is asked.
func TestDesktopDenyRefuses(t *testing.T) {
	approve, _ := cloneApproverWithDesktop(t.TempDir(), false, strings.NewReader("y\n"), new(bytes.Buffer), showDesktop(new(int), boolp(false)))
	got, err := approve(context.Background(), cloneReq())
	if err != nil || got.Approve {
		t.Fatalf("approve=%v err=%v (a Deny must refuse)", got.Approve, err)
	}
}

// With no desktop the piped input still decides, as before.
func TestNoDesktopFallsBackToTerminal(t *testing.T) {
	desktop := &fakeDesktop{calls: new(int)}
	for in, want := range map[string]bool{"y\n": true, "n\n": false} {
		var out bytes.Buffer
		approve, _ := cloneApproverWithDesktop(t.TempDir(), false, strings.NewReader(in), &out, desktop)
		got, err := approve(context.Background(), cloneReq())
		if err != nil || got.Approve != want {
			t.Fatalf("%q: approve=%v err=%v want %v", in, got.Approve, err, want)
		}
	}
	if *desktop.calls != 0 {
		t.Fatalf("desktop calls=%d, want 0: an unavailable desktop asks nothing", *desktop.calls)
	}
}

// The desktop prompt sees what it approves: the repository, the folder and who asked.
func TestDesktopSeesWhatItApproves(t *testing.T) {
	var calls int
	var title, body string
	var giveUp time.Duration
	desktop := showDesktop(&calls, boolp(true))
	desktop.title, desktop.body, desktop.giveUp = &title, &body, &giveUp
	var out bytes.Buffer
	approve, _ := cloneApproverWithDesktop(t.TempDir(), false, strings.NewReader(""), &out, desktop)
	if _, err := approve(context.Background(), cloneReq()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"openvibers/demo", "demo", "sam"} {
		if !strings.Contains(title+" "+body, want) {
			t.Fatalf("prompt %q %q does not say %q", title, body, want)
		}
	}
	if giveUp <= 0 || giveUp > 90*time.Second {
		t.Fatalf("giveUp=%v, want the time left on the request", giveUp)
	}
}

// An expired request asks nothing anywhere: no desktop call, no terminal read.
func TestExpiredRequestAsksNothing(t *testing.T) {
	var calls int
	approve, _ := cloneApproverWithDesktop(t.TempDir(), false, strings.NewReader("y\n"), new(bytes.Buffer), showDesktop(&calls, boolp(true)))
	req := cloneReq()
	req.ExpiresAt = time.Now().Add(-time.Second)
	if _, err := approve(context.Background(), req); !errors.Is(err, errCloneExpired) {
		t.Fatalf("err=%v, want the expiry", err)
	}
	if calls != 0 {
		t.Fatalf("desktop calls=%d, want 0", calls)
	}
}

// No terminal and no desktop is an error naming both, not a silent refusal.
func TestNoTerminalNoDesktopErrors(t *testing.T) {
	approve, _ := cloneApproverWithDesktop(t.TempDir(), false, nil, new(bytes.Buffer), &fakeDesktop{calls: new(int)})
	if _, err := approve(context.Background(), cloneReq()); err == nil || !strings.Contains(err.Error(), "no terminal") {
		t.Fatalf("err=%v, want the no-one-to-ask error", err)
	}
}

// No terminal but a desktop: the desktop alone decides (the deep link's shape).
func TestDesktopAloneDecides(t *testing.T) {
	approve, _ := cloneApproverWithDesktop(t.TempDir(), false, nil, new(bytes.Buffer), showDesktop(new(int), boolp(true)))
	got, err := approve(context.Background(), cloneReq())
	if err != nil || !got.Approve {
		t.Fatalf("approve=%v err=%v", got.Approve, err)
	}
}

// A cancelled run stops asking and reports the cancellation.
func TestCancelledRunStopsAsking(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	approve, _ := cloneApproverWithDesktop(t.TempDir(), false, strings.NewReader(""), new(bytes.Buffer),
		&fakeDesktop{calls: new(int), avail: true, answer: boolp(true), block: block})
	ctx, stop := context.WithCancel(context.Background())
	stop()
	if _, err := approve(ctx, cloneReq()); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v, want context.Canceled", err)
	}
}

// funcDesktop is a desktop prompt from one function, for answers fakeDesktop
// cannot give.
type funcDesktop struct {
	avail bool
	askFn func(ctx context.Context, title, body string, giveUp time.Duration) *bool
}

func (f *funcDesktop) available() bool { return f.avail }

func (f *funcDesktop) ask(ctx context.Context, title, body string, giveUp time.Duration) *bool {
	return f.askFn(ctx, title, body, giveUp)
}

// The terminal's answer takes a held-open desktop prompt down with it: the
// prompt below only ends when its context ends.
func TestTerminalAnswerTakesDesktopDown(t *testing.T) {
	null, err := os.OpenFile(os.DevNull, os.O_RDONLY, 0)
	if err != nil {
		t.Skip("no null device")
	}
	defer null.Close()
	if !isTerminal(null) {
		t.Skip("no terminal-like file here")
	}
	down := make(chan struct{})
	desktop := &funcDesktop{avail: true, askFn: func(ctx context.Context, _ string, _ string, _ time.Duration) *bool {
		<-ctx.Done()
		close(down)
		return nil
	}}
	var out bytes.Buffer
	approve, _ := cloneApproverWithDesktop(t.TempDir(), false, null, &out, desktop)
	got, err := approve(context.Background(), cloneReq())
	if err != nil || got.Approve {
		t.Fatalf("approve=%v err=%v (a closed input refuses)", got.Approve, err)
	}
	select {
	case <-down:
	case <-time.After(5 * time.Second):
		t.Fatal("the desktop prompt was not taken down after the terminal answered")
	}
}

// The non-empty folder's second answer can also come from the desktop.
func TestOverwriteSecondAnswerDesktop(t *testing.T) {
	for _, want := range []bool{true, false} {
		_, overwrite := cloneApproverWithDesktop(t.TempDir(), false, strings.NewReader(""), new(bytes.Buffer), showDesktop(new(int), boolp(want)))
		got, err := overwrite(context.Background(), cloneReq(), "/tmp/demo")
		if err != nil || got != want {
			t.Fatalf("overwrite=%v err=%v want %v", got, err, want)
		}
	}
}

func TestOsaQuote(t *testing.T) {
	if got := osaQuote(`say "hi" \ bye`); got != `"say \"hi\" \\ bye"` {
		t.Fatalf("quote=%q", got)
	}
}
