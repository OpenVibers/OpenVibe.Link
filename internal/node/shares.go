package node

// OpenVibe Connect shares, kept in the Node's own process so Link's window can start and end one while it runs. The
// saved list is the same one `openvibe-node share add|list|remove` uses.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"

	"github.com/OpenVibers/OpenVibe.Node/internal/connect"
	"github.com/OpenVibers/OpenVibe.Node/internal/link"
	"github.com/OpenVibers/OpenVibe.Node/internal/panel"
)

// shareOptions builds the Connect client: a Network-paired machine buys its own node token for a share it serves.
func (n *Node) shareOptions() connect.Options {
	o := connect.Options{Log: n.log, Version: n.opt.Version}
	if n.opt.Creds != nil && n.opt.Creds.NetworkPaired() {
		ts := link.NewTokenSourceFor(n.opt.Creds, nil, "openvibe-node/"+n.opt.Version, link.ConnectAudience)
		o.NodeToken = func(ctx context.Context) (string, error) {
			tok, err := ts.Token(ctx)
			if errors.Is(err, link.ErrNodeRevoked) {
				return "", fmt.Errorf("%w: %v", connect.ErrStopped, err)
			}
			if err != nil {
				return "", err
			}
			return tok.Reveal(), nil
		}
	}
	return o
}

// shareSet keeps every saved share connected, and can add or remove one at runtime.
type shareSet struct {
	ctx  context.Context
	dir  string
	opts connect.Options
	mu   sync.Mutex
	// running is one cancel per port this process serves.
	running map[int]context.CancelFunc
}

func newShareSet(ctx context.Context, dir string, opts connect.Options) *shareSet {
	return &shareSet{ctx: ctx, dir: dir, opts: opts, running: map[int]context.CancelFunc{}}
}

// StartAll connects every saved share, as `openvibe-node run` always did.
func (s *shareSet) StartAll() {
	shares, err := connect.Load(s.dir)
	if err != nil {
		if s.opts.Log != nil {
			s.opts.Log.Warn("connect: saved shares unreadable", "err", err)
		}
		return
	}
	for _, sh := range shares {
		s.start(sh)
	}
}

func (s *shareSet) start(sh connect.Share) {
	s.mu.Lock()
	if _, ok := s.running[sh.Port]; ok {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.running[sh.Port] = cancel
	s.mu.Unlock()
	go func() {
		err := connect.Run(ctx, sh, s.opts)
		s.mu.Lock()
		if cancel, ok := s.running[sh.Port]; ok {
			cancel()
			delete(s.running, sh.Port)
		}
		s.mu.Unlock()
		if errors.Is(err, connect.ErrStopped) && s.opts.Log != nil {
			s.opts.Log.Warn("connect: share stopped; remove it with `openvibe-node share remove`", "port", sh.Port, "share", sh.Label())
		}
	}()
}

// add serves a port this computer was picked for on OpenVibe Connect.
func (s *shareSet) add(port int) (panel.Share, error) {
	if port < 1 || port > 65535 {
		return panel.Share{}, errors.New("port is 1 to 65535")
	}
	if s.opts.NodeToken == nil {
		return panel.Share{}, errors.New("this computer is not paired with OpenVibe: add it on openvibe.bot/machines first")
	}
	s.mu.Lock()
	_, running := s.running[port]
	s.mu.Unlock()
	if running {
		return panel.Share{}, fmt.Errorf("port %d is already shared", port)
	}
	tok, err := s.opts.NodeToken(s.ctx)
	if err != nil {
		return panel.Share{}, err
	}
	mine, err := connect.NodeShares(s.ctx, nil, connect.DefaultServer, tok, "openvibe-node/"+s.opts.Version)
	if err != nil {
		return panel.Share{}, err
	}
	var found *connect.NodeShare
	for i := range mine {
		if mine[i].LocalPort != port {
			continue
		}
		if found != nil {
			return panel.Share{}, fmt.Errorf("Connect has more than one share for this computer on port %d", port)
		}
		found = &mine[i]
	}
	if found == nil {
		return panel.Share{}, fmt.Errorf("Connect has no share for this computer on port %d: pick this computer on the share's page first", port)
	}
	sh := connect.Share{ID: found.ID, Port: port, Server: connect.DefaultServer, Name: found.Host}
	if err := connect.Add(s.dir, sh); err != nil {
		return panel.Share{}, err
	}
	s.start(sh)
	return panel.Share{Port: port, URL: found.URL}, nil
}

// remove ends a share and forgets it from the saved list.
func (s *shareSet) remove(port int) error {
	s.mu.Lock()
	cancel := s.running[port]
	delete(s.running, port)
	s.mu.Unlock()
	n, err := connect.Remove(s.dir, strconv.Itoa(port))
	if err != nil {
		return err
	}
	if cancel != nil {
		cancel()
	}
	if cancel == nil && n == 0 {
		return fmt.Errorf("port %d is not shared", port)
	}
	return nil
}

// shareAdd is the window's POST /panel/api/shares.
func (n *Node) shareAdd(port int) (panel.Share, error) {
	if n.shares == nil {
		return panel.Share{}, errors.New("this Node is not keeping shares up")
	}
	sh, err := n.shares.add(port)
	if err != nil {
		return panel.Share{}, err
	}
	n.addActivity(fmt.Sprintf("Port %d shared through OpenVibe Connect", port))
	return sh, nil
}

// shareRemove is the window's DELETE /panel/api/shares/:port.
func (n *Node) shareRemove(port int) error {
	if n.shares == nil {
		return errors.New("this Node is not keeping shares up")
	}
	if err := n.shares.remove(port); err != nil {
		return err
	}
	n.addActivity(fmt.Sprintf("Port %d is no longer shared", port))
	return nil
}
