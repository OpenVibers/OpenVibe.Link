package video

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/OpenVibers/OpenVibe.Link/internal/credentials"
)

// NativePublisher runs `ov-desk publish` (Link v0.77): the screen captured, encoded and published over WHIP in one Rust
// process, so the frames never cross into Go. Raced against the pipe + pion path on the same screen and receiver, it
// uses less CPU and memory (docs/computer.md); the owner, 11-17: "we want to use rust if we get any gain at all".
//
// The publish key goes to the child in its environment (OPENVIBE_WHIP_KEY), never in argv. The child says how it goes
// on stdout, one JSON object a line ({"state":"live"}, {"frames":N}, {"state":"retrying","error":"…"}), and reconnects
// by itself; a child that dies is started again with backoff. Run returns ErrNativeUnavailable when the helper cannot
// do it here — exit 3 (no UDP path to the server: the Go publisher can relay through TURN), or an exit before it said
// anything (an ov-desk older than v0.77) — so the caller falls back to the pipe + pion path.
type NativePublisher struct {
	Argv []string // ov-desk publish -whip <url> …
	Env  []string // added to the Link's own environment (XAUTHORITY)
	Key  credentials.Secret
	Log  *slog.Logger

	state  atomic.Value // string
	frames atomic.Int64
	lastEr atomic.Value // string
}

// SayWithin is how long a first start may take to say its first status line.
var SayWithin = 5 * time.Second

// ErrNativeUnavailable says the native publisher cannot run here; the caller uses the Go publisher instead.
var ErrNativeUnavailable = errors.New("ov-desk publish is not available here")

func (p *NativePublisher) State() string {
	if s, ok := p.state.Load().(string); ok {
		return s
	}
	return StateOff
}
func (p *NativePublisher) Frames() int64 { return p.frames.Load() }
func (p *NativePublisher) LastError() string {
	if s, ok := p.lastEr.Load().(string); ok {
		return s
	}
	return ""
}

// Run publishes until ctx ends (nil) or the helper cannot run here (ErrNativeUnavailable).
func (p *NativePublisher) Run(ctx context.Context) error {
	log := p.Log
	if log == nil {
		log = slog.Default()
	}
	p.state.Store(StateConnecting)
	defer p.state.Store(StateOff)
	backoff := time.Second
	for first := true; ; first = false {
		said, code, err := p.once(ctx, first)
		if ctx.Err() != nil {
			return nil
		}
		if code == 3 || (first && !said) {
			msg := "exit " + itoa(code)
			if err != nil {
				msg = err.Error()
			}
			log.Info("screen stream: ov-desk publish cannot run here; the Go publisher takes over", "why", msg)
			return ErrNativeUnavailable
		}
		p.state.Store(StateRetrying)
		log.Warn("screen stream: ov-desk publish ended; starting it again", "exit", code, "in", backoff)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

// once runs the helper until it ends → whether it said anything, its exit code.
func (p *NativePublisher) once(parent context.Context, first bool) (said bool, code int, err error) {
	if len(p.Argv) == 0 {
		return false, -1, errors.New("no argv")
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	var heard atomic.Bool
	if first {
		// A helper that does not speak the protocol within a few seconds (not ov-desk publish) is not one.
		t := time.AfterFunc(SayWithin, func() {
			if !heard.Load() {
				cancel()
			}
		})
		defer t.Stop()
	}
	cmd := exec.CommandContext(ctx, p.Argv[0], p.Argv[1:]...)
	env := os.Environ()
	// Never inherit a key from the Link's own environment: only this stream's.
	kept := env[:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, "OPENVIBE_WHIP_KEY=") {
			kept = append(kept, kv)
		}
	}
	cmd.Env = append(append(kept, p.Env...), "OPENVIBE_WHIP_KEY="+p.Key.Reveal())
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) } // ov-desk says goodbye (DELETE) on SIGTERM
	cmd.WaitDelay = 5 * time.Second
	stdin, err := cmd.StdinPipe() // held open: the helper ends when it closes
	if err != nil {
		return false, -1, err
	}
	defer stdin.Close()
	out, err := cmd.StdoutPipe()
	if err != nil {
		return false, -1, err
	}
	cmd.Stderr = &limitedWriter{w: io.Discard, n: 4096}
	if err := cmd.Start(); err != nil {
		return false, -1, err
	}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 0, 4096), 64*1024)
	for sc.Scan() {
		var line struct {
			State  string `json:"state"`
			Error  string `json:"error"`
			Frames *int64 `json:"frames"`
		}
		if json.Unmarshal(sc.Bytes(), &line) != nil {
			continue
		}
		said = true
		heard.Store(true)
		switch line.State {
		case "connecting":
			p.state.Store(StateConnecting)
		case "live":
			p.state.Store(StateLive)
			p.lastEr.Store("")
		case "retrying", "failed":
			p.state.Store(StateRetrying)
			if line.Error != "" {
				p.lastEr.Store(scrub(line.Error, p.Key))
			}
		}
		if line.Frames != nil {
			p.frames.Store(*line.Frames)
		}
	}
	err = cmd.Wait()
	code = -1
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	return said, code, err
}

func itoa(i int) string {
	if i < 0 {
		return "-" + itoa(-i)
	}
	if i < 10 {
		return string(rune('0' + i))
	}
	return itoa(i/10) + string(rune('0'+i%10))
}
