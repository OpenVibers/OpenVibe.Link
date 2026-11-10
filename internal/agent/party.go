package agent

import (
	"context"
	"errors"
	"io"
	"regexp"
	"time"

	"github.com/OpenVibers/OpenVibe.Link/internal/connect"
	"github.com/OpenVibers/OpenVibe.Link/internal/link"
	"github.com/OpenVibers/OpenVibe.Link/internal/party"
)

var validPartyID = regexp.MustCompile(`^party-[a-f0-9]{8}$`)

// startParty runs the same host path as the CLI, tied to the Link service's
// lifetime. The window lists it and can cancel it without stopping other work.
func (n *Agent) startParty(id string) error {
	if !validPartyID.MatchString(id) {
		return errors.New("invalid party id")
	}
	if n.ctx == nil || n.opt.Creds == nil || !n.opt.Creds.NetworkPaired() {
		return errors.New("pair this computer with OpenVibe Link before hosting")
	}
	if _, err := party.FindRuntime(); err != nil {
		return err
	}
	n.mu.Lock()
	if n.partyCancel != nil {
		n.mu.Unlock()
		return errors.New("this Link is already hosting a party desktop")
	}
	ctx, cancel := context.WithCancel(n.ctx)
	done := make(chan struct{})
	n.partyID, n.partyCancel, n.partyDone = id, cancel, done
	n.mu.Unlock()

	chatTokens := link.NewTokenSourceFor(n.opt.Creds, nil, "openvibe-link/"+n.opt.Version, "openvibe.chat")
	connectTokens := link.NewTokenSourceFor(n.opt.Creds, nil, "openvibe-link/"+n.opt.Version, link.ConnectAudience)
	partyURL := n.opt.Config.PartyPool.PartyURL
	if partyURL == "" {
		partyURL = "https://openvibe.party"
	}
	client := party.Client{PartyURL: partyURL, ConnectServer: connect.DefaultServer, PartyID: id,
		ChatToken: func(ctx context.Context) (string, error) {
			t, err := chatTokens.Token(ctx)
			if err != nil {
				return "", err
			}
			return t.Reveal(), nil
		},
		ConnectToken: func(ctx context.Context) (string, error) {
			t, err := connectTokens.Token(ctx)
			if err != nil {
				return "", err
			}
			return t.Reveal(), nil
		}}
	go func() {
		defer close(done)
		if err := party.Host(ctx, client, n.opt.Version, io.Discard); err != nil && ctx.Err() == nil {
			n.log.Warn("party desktop stopped", "party", id, "err", err)
			n.addActivity("Party desktop could not start")
		}
		cancel()
		n.mu.Lock()
		if n.partyDone == done {
			n.partyID, n.partyCancel, n.partyDone = "", nil, nil
		}
		n.mu.Unlock()
	}()
	return nil
}

func (n *Agent) stopParty(id string) error {
	n.mu.Lock()
	if n.partyCancel == nil || n.partyID != id {
		n.mu.Unlock()
		return errors.New("that party is not hosted here")
	}
	cancel, done := n.partyCancel, n.partyDone
	n.mu.Unlock()
	cancel()
	select {
	case <-done:
		n.addActivity("Stopped the party desktop")
		return nil
	case <-time.After(55 * time.Second):
		return errors.New("party desktop is still stopping")
	}
}
