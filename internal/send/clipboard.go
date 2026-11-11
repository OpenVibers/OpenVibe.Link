package send

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"time"
)

// Clipboard is this computer's text clipboard. Link only ever reads and writes text: an image or a file copied
// on one machine stays there.
type Clipboard interface {
	Read(ctx context.Context) (string, error)
	Write(ctx context.Context, text string) error
}

// commandClipboard uses the clipboard tool the desktop already has, so Link adds no dependency and no C library.
type commandClipboard struct {
	read  [][]string
	write [][]string
	tool  string
}

func (c *commandClipboard) Read(parent context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	for _, argv := range c.read {
		if _, err := exec.LookPath(argv[0]); err != nil {
			continue
		}
		out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).Output()
		if err == nil {
			return string(out), nil
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
	}
	return "", errors.New(c.tool + " is not available on this computer")
}

func (c *commandClipboard) Write(parent context.Context, text string) error {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	for _, argv := range c.write {
		if _, err := exec.LookPath(argv[0]); err != nil {
			continue
		}
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Stdin = bytes.NewReader([]byte(text))
		if err := cmd.Run(); err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return errors.New(c.tool + " is not available on this computer")
}
