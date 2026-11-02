package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/OpenVibers/OpenVibe.Node/internal/connect"
)

// shareArgs is `openvibe-node share …` (OpenVibe Connect, Website Connect v0).
type shareArgs struct {
	key    string
	port   int
	server string
}

const shareUsage = `usage:
  openvibe-node share --key ovc_… --port N      serve the share now, until Ctrl-C
  openvibe-node share add --key ovc_… --port N  keep it up from the Node service (restart the service to start it)
  openvibe-node share list                      the shares the service keeps up
  openvibe-node share remove <PORT|KEY|ADDRESS> stop keeping one up

Make a share and its key at https://connect.openvibe.website. The Node only ever connects to localhost on the
share's port; every visitor signs in with OpenVibe first.`

func cmdShare(g *globals, positional []string, a shareArgs, stdout, stderr io.Writer) error {
	sub := ""
	if len(positional) > 0 {
		sub = positional[0]
	}
	if a.key == "" {
		a.key = os.Getenv("OV_CONNECT_KEY")
	}
	switch sub {
	case "", "run":
		if a.key == "" || a.port == 0 {
			fmt.Fprintln(stderr, shareUsage)
			return errUsage
		}
		return shareNow(g, a, stdout)
	case "add":
		if a.key == "" || a.port == 0 {
			fmt.Fprintln(stderr, shareUsage)
			return errUsage
		}
		if !strings.HasPrefix(a.key, "ovc_") {
			return errors.New("the key is the share's ovc_… connect key, from its page on connect.openvibe.website")
		}
		if err := g.paths.Ensure(); err != nil {
			return err
		}
		if err := connect.Add(g.paths.ConfigDir, connect.Share{Key: a.key, Port: a.port, Server: a.server}); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Saved: localhost:%d (key %s). Restart the Node service to start it: the service keeps every saved share up.\n", a.port, connect.Redact(a.key))
		return nil
	case "list":
		shares, err := connect.Load(g.paths.ConfigDir)
		if err != nil {
			return err
		}
		if len(shares) == 0 {
			fmt.Fprintln(stdout, "No saved shares. Add one with: openvibe-node share add --key ovc_… --port N")
			return nil
		}
		for _, s := range shares {
			name := s.Name
			if name == "" {
				name = "(address shown once it connects)"
			}
			fmt.Fprintf(stdout, "localhost:%d  %s  key %s\n", s.Port, name, connect.Redact(s.Key))
		}
		return nil
	case "remove":
		if len(positional) != 2 {
			fmt.Fprintln(stderr, shareUsage)
			return errUsage
		}
		n, err := connect.Remove(g.paths.ConfigDir, positional[1])
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("no saved share matches %q", positional[1])
		}
		fmt.Fprintf(stdout, "Removed %d. Restart the Node service to stop serving it.\n", n)
		return nil
	default:
		fmt.Fprintln(stderr, shareUsage)
		return errUsage
	}
}

// shareNow serves one share in the foreground, with a line per request, until Ctrl-C or the server stops it.
func shareNow(g *globals, a shareArgs, stdout io.Writer) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := connect.Run(ctx, connect.Share{Key: a.key, Port: a.port, Server: a.server}, connect.Options{
		Log:     g.log,
		Version: version,
		OnReady: func(r connect.Ready) {
			fmt.Fprintf(stdout, "Connected. %s → localhost:%d (only the people you let in can open it). Ctrl-C to stop.\n", r.URL, a.port)
		},
		OnRequest: func(method, path string, status int, d time.Duration) {
			if len(path) > 120 {
				path = path[:120]
			}
			if status == 0 {
				fmt.Fprintf(stdout, "%s %s → nothing answered on port %d\n", method, path, a.port)
				return
			}
			fmt.Fprintf(stdout, "%s %s → %d (%d ms)\n", method, path, status, d.Milliseconds())
		},
	})
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// errUsage: the usage was printed; exit 2 without another line.
var errUsage = errors.New("usage")
