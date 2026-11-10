package agent

import (
	"context"
	"time"

	"github.com/OpenVibers/OpenVibe.Link/internal/party"
)

// partyPoolInterval is how often a pool machine asks Chat what to host. The pool sweeps idle holds every minute, so
// five seconds is prompt without a busy poll; Chat answers null for a machine it is not holding a party for.
const partyPoolInterval = 5 * time.Second

// pollPartyPool offers this machine to OpenVibe's own computer pool (openvibe.party/pool): the owner registered its
// principal there, and the Link asks Chat what is held for it. It starts the same host path as
// `openvibe-link party host <party>`, one party at a time; the host loop ends when the party ends and the next poll
// picks up the next one. Off unless party_pool.enabled is set in the Link's config file.
func (n *Agent) pollPartyPool(ctx context.Context) {
	if n.chatTokens == nil || n.opt.Creds == nil {
		return
	}
	partyURL := n.opt.Config.PartyPool.PartyURL
	if partyURL == "" {
		partyURL = "https://openvibe.party"
	}
	client := party.Client{
		PartyURL: partyURL,
		ChatToken: func(ctx context.Context) (string, error) {
			t, err := n.chatTokens.Token(ctx)
			if err != nil {
				return "", err
			}
			return t.Reveal(), nil
		},
	}
	n.offerPartyPool(ctx, client)
	tick := time.NewTicker(partyPoolInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			n.offerPartyPool(ctx, client)
		}
	}
}

// offerPartyPool starts the next assignment unless this Link is already hosting a party. A failure (Chat down, the
// machine not registered, no container runtime) is logged and the poll tries again; it never affects the machine's
// other work.
func (n *Agent) offerPartyPool(ctx context.Context, client party.Client) {
	n.mu.Lock()
	busy := n.partyID != ""
	n.mu.Unlock()
	if busy || ctx.Err() != nil {
		return
	}
	// The owner's stop is the owner's: no pool party starts while it is latched.
	if st := n.latch.State(); st.Stopped() {
		return
	}
	assignment, err := client.Assignment(ctx)
	if err != nil {
		n.log.Debug("party pool: no assignment", "err", err)
		return
	}
	if assignment == nil || assignment.Party == "" {
		return
	}
	if err := n.startParty(assignment.Party); err != nil {
		n.log.Warn("party pool: the party could not start", "party", assignment.Party, "err", err)
		return
	}
	n.log.Info("party pool: hosting", "party", assignment.Party)
}
