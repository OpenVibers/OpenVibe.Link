package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

// A clone approval reaches the person two ways: the y/N in the terminal that runs
// `git connect` (or `clone`), and a prompt on the desktop with Approve and Deny —
// the same pairing the computer plugin uses when the tray is not showing it
// (a notification with buttons on Linux, a dialog on macOS). Either answer wins;
// the prompt is taken down once the person answers anywhere. Only an explicit
// approval approves: a dismissed prompt, a closed input and anything but y are no.

// errCloneExpired is an approval that waited past the request's two minutes.
var errCloneExpired = errors.New("the request expired after two minutes; ask the page again")

// errNobodyToAsk is an approval with no terminal to read and no desktop to ask on.
var errNobodyToAsk = errors.New("there is no terminal to ask on and no desktop prompt here; pass --yes to approve from a script")

// desktopPrompter shows one yes/no question on the desktop: the clone approval
// with Approve and Deny. available reports whether a prompt could be shown,
// without showing one. ask shows title and body and reports the person's
// answer: true is Approve, false is Deny or a dismissal (only an explicit
// approval approves). A nil answer means the prompt could not be shown after
// all, so the caller falls back to whatever else can ask. giveUp bounds the
// prompt: the time left on the request, 0 for none.
type desktopPrompter interface {
	available() bool
	ask(ctx context.Context, title, body string, giveUp time.Duration) *bool
}

// realDesktop is the desktop half of a clone approval: notify-send with Approve
// and Deny on Linux, an osascript dialog with Deny default on macOS. Elsewhere
// it is never available (Windows has the tray; without it the terminal asks).
type realDesktop struct{}

func (realDesktop) available() bool { return desktopFacility() }

func (realDesktop) ask(ctx context.Context, title, body string, giveUp time.Duration) *bool {
	switch runtime.GOOS {
	case "linux":
		return askNotifySend(ctx, title, body, giveUp)
	case "darwin":
		return askDialog(ctx, title, body, giveUp)
	}
	return nil
}

// desktopFacility is whether this computer could show a clone prompt right now:
// a graphical session with its helper present. ask rechecks before showing.
func desktopFacility() bool {
	switch runtime.GOOS {
	case "linux":
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return false
		}
		_, err := exec.LookPath("notify-send")
		return err == nil
	case "darwin":
		_, err := exec.LookPath("osascript")
		return err == nil
	}
	return false
}

// notifySendActions is whether this notify-send shows buttons and waits for one
// (libnotify 0.7.10+), asked once per process.
var notifySendActions = sync.OnceValue(func() bool {
	out, err := exec.Command("notify-send", "--help").Output()
	if err != nil {
		return false
	}
	return bytes.Contains(out, []byte("--action")) && bytes.Contains(out, []byte("--wait"))
})

// askNotifySend asks with a notification with Approve and Deny buttons. Without
// button support it leaves an informational notification and reports nil, so the
// terminal stays the decider; without a session to show it in, nil as well.
func askNotifySend(ctx context.Context, title, body string, giveUp time.Duration) *bool {
	if !notifySendActions() {
		_ = runDetached("notify-send", "--app-name=OpenVibe Link", "--urgency=critical", "--", title,
			body+"\nAnswer in the terminal that runs git connect.")
		return nil
	}
	argv := []string{"notify-send", "--app-name=OpenVibe Link", "--urgency=critical", "--wait",
		"--action=approve=Approve", "--action=deny=Deny"}
	if giveUp > 0 {
		argv = append(argv, "--expire-time="+itoa(int64(giveUp/time.Millisecond)))
	}
	argv = append(argv, "--", title, body)
	out, ok := runPrompt(ctx, argv[0], argv[1:]...)
	if !ok {
		return nil
	}
	switch strings.TrimSpace(out) {
	case "approve":
		return boolp(true)
	default:
		// Deny, a dismissal, or the notification expiring with the request: no.
		return boolp(false)
	}
}

// askDialog asks with an osascript dialog, Deny as its default button, giving up
// with the request. A dialog that never appeared (no GUI session) is nil.
func askDialog(ctx context.Context, title, body string, giveUp time.Duration) *bool {
	script := "display dialog " + osaQuote(body) + " with title " + osaQuote(title) +
		` buttons {"Deny", "Approve"} default button "Deny"`
	if giveUp > 0 {
		script += " giving up after " + itoa(int64((giveUp+time.Second-1)/time.Second))
	}
	script += " with icon caution"
	out, ok := runPrompt(ctx, "osascript", "-e", script)
	if !ok {
		return nil
	}
	if strings.Contains(out, "button returned:Approve") {
		return boolp(true)
	}
	// Deny, giving up, or closing the dialog: no.
	return boolp(false)
}

// osaQuote quotes AppleScript text the way the computer plugin does.
func osaQuote(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}

// runPrompt runs one desktop prompt and returns what it printed. False means it
// never asked (it would not start); ending the context takes a shown prompt down
// and is also false, so the caller falls back to whatever else can ask.
func runPrompt(ctx context.Context, name string, argv ...string) (string, bool) {
	cmd := exec.Command(name, argv...)
	cmd.Stdin = nil
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return "", false
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return "", false
		}
		return out.String(), true
	case <-ctx.Done():
		interruptPrompt(cmd, done)
		<-done
		return "", false
	}
}

// interruptPrompt takes a shown prompt down: SIGINT first (notify-send --wait
// withdraws its notification on it), then SIGKILL if it stays. done is the one
// waiter that reaps the process; this only signals.
func interruptPrompt(cmd *exec.Cmd, done <-chan error) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGINT)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = cmd.Process.Kill()
	}
}

// runDetached starts a fire-and-forget helper and reports whether it started.
func runDetached(name string, argv ...string) bool {
	cmd := exec.Command(name, argv...)
	cmd.Stdin = nil
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Start() == nil
}

func boolp(b bool) *bool { return &b }

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// isTerminal is whether input is the person's terminal (a character device),
// rather than a pipe, a file, or nothing.
func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}
