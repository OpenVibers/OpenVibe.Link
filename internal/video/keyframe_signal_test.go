//go:build !windows

package video

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A source whose child takes a keyframe signal (ov-desk stream, Link v0.75) passes RequestKeyframe on to it: here a
// child that writes two access units, notes every SIGUSR1 it gets, and keeps running.
func TestCommandSourceSignalsItsChildForAKeyframe(t *testing.T) {
	dir := t.TempDir()
	enc, err := NewPCMEncoder(32, 32)
	if err != nil {
		t.Fatal(err)
	}
	h264 := filepath.Join(dir, "one.h264")
	if err := os.WriteFile(h264, enc.Encode(NewFrame(32, 32), true, nil), 0o644); err != nil {
		t.Fatal(err)
	}
	got := filepath.Join(dir, "signals")
	script := filepath.Join(dir, "child.sh")
	// Two access units: the parser hands one on when the next one begins.
	body := "#!/bin/sh\ntrap 'echo keyframe >> \"" + got + "\"' USR1\ncat '" + h264 + "' '" + h264 + "'\nn=0\nwhile [ $n -lt 100 ]; do sleep 0.05; n=$((n+1)); done\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	src := &CommandSource{Argv: []string{script}, KeyframeSignal: syscall.SIGUSR1}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	samples := make(chan Sample, 4)
	go func() { _ = src.Run(ctx, func(s Sample) { samples <- s }) }()
	select {
	case <-samples:
	case <-time.After(5 * time.Second):
		t.Fatal("the child wrote nothing")
	}
	src.RequestKeyframe()
	deadline := time.Now().Add(3 * time.Second)
	for {
		b, _ := os.ReadFile(got)
		if strings.Contains(string(b), "keyframe") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the child was not asked for a keyframe")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Without a signal it asks nothing (rpicam-vid, ffmpeg).
	(&CommandSource{Argv: []string{script}}).RequestKeyframe()
}
