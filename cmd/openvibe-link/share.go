package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/OpenVibers/OpenVibe.Link/internal/connect"
	"github.com/OpenVibers/OpenVibe.Link/internal/credentials"
	"github.com/OpenVibers/OpenVibe.Link/internal/link"
)

// shareArgs is `openvibe-link share …` (OpenVibe Connect, Website Connect v0).
type shareArgs struct {
	key    string
	port   int
	server string
	share  string // shr_…: this computer serves it as OpenVibe Link, no key (CON-4)
}

const shareUsage = `usage:
  openvibe-link share <port>                        serve the share this computer was picked for on that port, until Ctrl-C
  openvibe-link share add <port>                    keep that share up from the Link service
  openvibe-link share --share shr_… [--port N]      serve one by its id, until Ctrl-C (no key)
  openvibe-link share add --share shr_… [--port N]  keep one by its id up from the Link service
  openvibe-link share mine                          the shares this computer was picked for
  openvibe-link share --key ovc_… --port N          serve a share with its connect key, until Ctrl-C
  openvibe-link share add --key ovc_… --port N      keep it up from the Link service (restart the service to start it)
  openvibe-link share list                          the shares the service keeps up
  openvibe-link share remove <PORT|KEY|ID|ADDRESS>  stop keeping one up

Make a share at https://connect.openvibe.link; its address is https://<label>--<you>.openvibe.link. On its page, pick this computer under "Or serve it from one of your
computers" to serve it with --share and no key (the computer must be added on openvibe.bot/machines). Without --port,
the Link serves the port the share's page names now, and keeps that one. The Link only ever connects to localhost on
the share's port; every visitor signs in with OpenVibe first.`

// connectLinkToken buys this machine's node token for OpenVibe Connect (audience openvibe.link). A machine
// removed on OpenVibe stops its shares (connect.ErrStopped) instead of trying forever.
func connectLinkToken(creds *credentials.Credentials) func(context.Context) (string, error) {
	ts := link.NewTokenSourceFor(creds, nil, "openvibe-link/"+version, link.ConnectAudience)
	return func(ctx context.Context) (string, error) {
		tok, err := ts.Token(ctx)
		if errors.Is(err, link.ErrLinkRevoked) {
			return "", fmt.Errorf("%w: %v", connect.ErrStopped, err)
		}
		if err != nil {
			return "", err
		}
		return tok.Reveal(), nil
	}
}

// nodeShare is a share this computer serves by id: the machine's token, and the port (the one given, else the
// share's page's now).
func nodeShare(ctx context.Context, g *globals, a shareArgs) (connect.Share, func(context.Context) (string, error), error) {
	if !connect.ShareIDRe.MatchString(a.share) {
		return connect.Share{}, nil, errors.New("--share is the share's shr_… id, from its page on connect.openvibe.link")
	}
	creds, err := credentials.Load(g.paths.CredentialFile(), nil)
	if err != nil || !creds.NetworkPaired() {
		return connect.Share{}, nil, errors.New("this computer is not added to OpenVibe yet: add it on openvibe.bot/machines, then pick it on the share's page")
	}
	tokens := connectLinkToken(creds)
	s := connect.Share{ID: a.share, Port: a.port, Server: a.server}
	if s.Port == 0 {
		tok, err := tokens(ctx)
		if err != nil {
			return connect.Share{}, nil, err
		}
		mine, err := connect.LinkShares(ctx, nil, a.server, tok, "openvibe-link/"+version)
		if err != nil {
			return connect.Share{}, nil, err
		}
		for _, m := range mine {
			if m.ID == a.share {
				s.Port, s.Name = m.LocalPort, m.Host
			}
		}
		if s.Port == 0 {
			return connect.Share{}, nil, fmt.Errorf("this computer does not serve %s: pick it on the share's page on connect.openvibe.link", a.share)
		}
	}
	return s, tokens, nil
}

// shareByPort finds the one share the owner picked this computer for that names local port a.port (CON-4). It is the
// `openvibe-link share <port>` form: the machine's own node token buys the tunnel token, so there is no key.
func shareByPort(ctx context.Context, g *globals, a shareArgs) (connect.Share, func(context.Context) (string, error), error) {
	if a.port < 1 || a.port > 65535 {
		return connect.Share{}, nil, fmt.Errorf("port %d is not a port", a.port)
	}
	creds, err := credentials.Load(g.paths.CredentialFile(), nil)
	if err != nil || !creds.NetworkPaired() {
		return connect.Share{}, nil, errors.New("this computer is not added to OpenVibe yet: add it on openvibe.bot/machines, then pick it on the share's page")
	}
	tokens := connectLinkToken(creds)
	tok, err := tokens(ctx)
	if err != nil {
		return connect.Share{}, nil, err
	}
	mine, err := connect.LinkShares(ctx, nil, a.server, tok, "openvibe-link/"+version)
	if err != nil {
		return connect.Share{}, nil, err
	}
	var found []connect.LinkShare
	for _, m := range mine {
		if m.LocalPort == a.port {
			found = append(found, m)
		}
	}
	switch len(found) {
	case 0:
		return connect.Share{}, nil, fmt.Errorf("this computer does not serve localhost:%d: pick it on the share's page on connect.openvibe.link", a.port)
	case 1:
		return connect.Share{ID: found[0].ID, Port: found[0].LocalPort, Server: a.server, Name: found[0].Host}, tokens, nil
	default:
		return connect.Share{}, nil, fmt.Errorf("%d shares use localhost:%d; name one with --share shr_…", len(found), a.port)
	}
}

func cmdShare(g *globals, positional []string, a shareArgs, stdout, stderr io.Writer) error {
	sub := ""
	if len(positional) > 0 {
		sub = positional[0]
	}
	if a.key == "" {
		a.key = os.Getenv("OV_CONNECT_KEY")
	}
	// `openvibe-link share <port>`: the share this computer was picked for, found by the local port its owner set on
	// the share's page (CON-4). The page gives this form because there is no key to copy.
	if a.share == "" && a.key == "" && a.port == 0 && len(positional) == 1 {
		if p, err := strconv.Atoi(positional[0]); err == nil && p > 0 && p < 65536 {
			a.port, positional, sub = p, nil, ""
		}
	}
	switch sub {
	case "", "run":
		if a.share != "" {
			return shareNow(g, a, stdout)
		}
		if a.key == "" && a.port != 0 {
			s, _, err := shareByPort(context.Background(), g, a)
			if err != nil {
				return err
			}
			a.share, a.port = s.ID, s.Port
			return shareNow(g, a, stdout)
		}
		if a.key == "" || a.port == 0 {
			fmt.Fprintln(stderr, shareUsage)
			return errUsage
		}
		return shareNow(g, a, stdout)
	case "mine":
		creds, err := credentials.Load(g.paths.CredentialFile(), nil)
		if err != nil || !creds.NetworkPaired() {
			return errors.New("this computer is not added to OpenVibe yet: add it on openvibe.bot/machines")
		}
		ctx := context.Background()
		tok, err := connectLinkToken(creds)(ctx)
		if err != nil {
			return err
		}
		mine, err := connect.LinkShares(ctx, nil, a.server, tok, "openvibe-link/"+version)
		if err != nil {
			return err
		}
		if len(mine) == 0 {
			fmt.Fprintln(stdout, "No shares picked this computer yet. On a share's page on connect.openvibe.link, pick it under \"Or serve it from one of your computers\".")
			return nil
		}
		for _, m := range mine {
			fmt.Fprintf(stdout, "%s  %s → localhost:%d\n", m.ID, m.URL, m.LocalPort)
		}
		return nil
	case "add":
		if a.share == "" && a.key == "" && a.port == 0 && len(positional) == 2 {
			p, err := strconv.Atoi(positional[1])
			if err != nil || p < 1 || p > 65535 {
				fmt.Fprintln(stderr, shareUsage)
				return errUsage
			}
			a.port = p
			s, _, err := shareByPort(context.Background(), g, a)
			if err != nil {
				return err
			}
			a.share, a.port = s.ID, s.Port
		}
		if a.share != "" {
			if err := g.paths.Ensure(); err != nil {
				return err
			}
			s, _, err := nodeShare(context.Background(), g, a)
			if err != nil {
				return err
			}
			if err := connect.Add(g.paths.ConfigDir, s); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "Saved: %s on localhost:%d, served by this computer (no key). Restart the Link service to start it.\n", s.ID, s.Port)
			return nil
		}
		if a.key == "" || a.port == 0 {
			fmt.Fprintln(stderr, shareUsage)
			return errUsage
		}
		if !strings.HasPrefix(a.key, "ovc_") {
			return errors.New("the key is the share's ovc_… connect key, from its page on connect.openvibe.link")
		}
		if err := g.paths.Ensure(); err != nil {
			return err
		}
		if err := connect.Add(g.paths.ConfigDir, connect.Share{Key: a.key, Port: a.port, Server: a.server}); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Saved: localhost:%d (key %s). Restart the Link service to start it: the service keeps every saved share up.\n", a.port, connect.Redact(a.key))
		return nil
	case "list":
		shares, err := connect.Load(g.paths.ConfigDir)
		if err != nil {
			return err
		}
		if len(shares) == 0 {
			fmt.Fprintln(stdout, "No saved shares. Add one with: openvibe-link share add --key ovc_… --port N")
			return nil
		}
		for _, s := range shares {
			name := s.Name
			if name == "" {
				name = "(address shown once it connects)"
			}
			if s.ID != "" {
				fmt.Fprintf(stdout, "localhost:%d  %s  share %s (this computer, no key)\n", s.Port, name, s.ID)
				continue
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
		fmt.Fprintf(stdout, "Removed %d. Restart the Link service to stop serving it.\n", n)
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
	share := connect.Share{Key: a.key, Port: a.port, Server: a.server}
	var linkToken func(context.Context) (string, error)
	if a.share != "" {
		s, tokens, err := nodeShare(ctx, g, a)
		if err != nil {
			return err
		}
		share, linkToken, a.port = s, tokens, s.Port
	}
	err := connect.Run(ctx, share, connect.Options{
		Log:       g.log,
		Version:   version,
		LinkToken: linkToken,
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
