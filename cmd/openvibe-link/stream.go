package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/OpenVibers/OpenVibe.Link/internal/credentials"
	"github.com/OpenVibers/OpenVibe.Link/internal/stream"
)

// cmdStream is `openvibe-link stream --probe`: it publishes a QR of the millisecond clock over WHIP, so a headless
// viewer can report the glass-to-glass latency. The ticket comes from the environment or stdin, never from argv.
func cmdStream(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("stream", flag.ContinueOnError)
	fs.SetOutput(stderr)
	probe := fs.Bool("probe", false, "publish the QR millisecond clock (the latency probe)")
	whip := fs.String("whip-url", "", "the stream's WHIP URL (https://whip.openvibe.live/whip/agent/ags_…)")
	seconds := fs.Int("seconds", stream.ProbeDefaultSeconds, "how long to publish (seconds)")
	size := fs.String("size", "640x360", "picture size, WxH")
	fps := fs.Int("fps", stream.ProbeDefaultFPS, "frames per second")
	loopback := fs.Bool("loopback", false, "offer loopback ICE candidates (a WHIP endpoint on this machine)")
	ticketStdin := fs.Bool("ticket-stdin", false, "read the publish ticket from one line on stdin")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "error: stream takes no arguments")
		return 2
	}
	if !*probe {
		fmt.Fprintln(stderr, "usage: openvibe-link stream --probe --whip-url URL [--seconds N] [--size WxH] [--json]")
		fmt.Fprintln(stderr, "the publish ticket is read from OPENVIBE_STREAM_TICKET, or with --ticket-stdin from stdin (never argv)")
		return 2
	}
	width, height, err := parseSize(*size)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 2
	}
	ticket, err := probeTicket(stdin, *ticketStdin)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	n, err := stream.Probe(ctx, stream.ProbeOptions{WHIPURL: *whip, Ticket: ticket, Seconds: *seconds,
		Width: width, Height: height, FPS: *fps, Loopback: *loopback})
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	if *asJSON {
		b, _ := json.Marshal(map[string]any{"frames": n, "seconds": *seconds, "size": fmt.Sprintf("%dx%d", width, height),
			"whip_url": *whip})
		fmt.Fprintln(stdout, string(b))
		return 0
	}
	fmt.Fprintf(stdout, "probe: published %d frames in %ds at %dx%d\n", n, *seconds, width, height)
	fmt.Fprintln(stdout, "the latency is measured by scripts/stream-viewer.py against the same stream; it reports p50/p95/max")
	return 0
}

// probeTicket reads the publish ticket. It is a secret: the environment or stdin, never a flag that `ps` can show.
func probeTicket(stdin io.Reader, fromStdin bool) (credentials.Secret, error) {
	t := strings.TrimSpace(os.Getenv("OPENVIBE_STREAM_TICKET"))
	if t == "" && fromStdin && stdin != nil {
		// Read at most one line, so a pipe cannot hang the probe.
		buf := make([]byte, 256)
		n, _ := stdin.Read(buf)
		t = strings.TrimSpace(string(buf[:n]))
	}
	if t == "" {
		return credentials.Secret{}, errors.New("set OPENVIBE_STREAM_TICKET to the stream's wpt_… ticket (or pass --ticket-stdin)")
	}
	if !strings.HasPrefix(t, "wpt_") || len(t) < 8 {
		return credentials.Secret{}, errors.New("the publish ticket must be a wpt_… value (OPENVIBE_STREAM_TICKET)")
	}
	return credentials.NewSecret(t), nil
}

func parseSize(s string) (int, int, error) {
	var w, h int
	if _, err := fmt.Sscanf(s, "%dx%d", &w, &h); err != nil || w < 160 || h < 90 || w > 3840 || h > 2160 {
		return 0, 0, fmt.Errorf("size %q is WxH between 160x90 and 3840x2160", s)
	}
	return w &^ 15, h &^ 15, nil
}
