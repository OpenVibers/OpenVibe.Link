package protocol

import "encoding/json"

// Machine-to-machine relay frames, as OpenVibe.Bot speaks them
// (server/realtime.js: relay.open → relay.opened, relay.frame both ways,
// relay.close → relay.closed). Bot verifies the grant token on open and again
// on every frame; the receiving Link verifies it too and caps each scope at its
// own local level.
const (
	TypeRelayOpen   = "relay.open"
	TypeRelayOpened = "relay.opened"
	TypeRelayFrame  = "relay.frame"
	TypeRelayClose  = "relay.close"
	TypeRelayClosed = "relay.closed"

	TypeAgentTask          = "agent.task"
	TypeAgentTaskResult    = "agent.task_result"
	TypeAgentTaskResultAck = "agent.task_result_ack"
	TypeAccessRevoked      = "access.revoked"
)

// RelayOpen asks Bot for a relay to To under a Network-signed grant token.
type RelayOpen struct {
	To         string `json:"to"`
	GrantToken string `json:"grant_token"`
}

func (RelayOpen) MessageType() string { return TypeRelayOpen }

// RelayOpened is Bot's answer to both machines: the relay id, the grant's
// scopes and the same token, so each Link checks Network's signature itself.
type RelayOpened struct {
	Relay      string   `json:"relay"`
	To         string   `json:"to"`
	GrantToken string   `json:"grant_token"`
	Scopes     []string `json:"scopes"`
	ExpiresAt  string   `json:"expires_at,omitempty"`
	From       string   `json:"from"`
}

func (RelayOpened) MessageType() string { return TypeRelayOpened }

// RelayFrame carries one operation or its answer. From is filled in by Bot.
type RelayFrame struct {
	Relay string          `json:"relay"`
	From  string          `json:"from,omitempty"`
	Scope string          `json:"scope"`
	Body  json.RawMessage `json:"body"`
}

func (RelayFrame) MessageType() string { return TypeRelayFrame }

// RelayClose asks Bot to end a relay when either side is done with it.
type RelayClose struct {
	Relay string `json:"relay"`
}

func (RelayClose) MessageType() string { return TypeRelayClose }

// RelayClosed tells both machines a relay ended, with Bot's reason (closed,
// expired or revoked).
type RelayClosed struct {
	Relay  string `json:"relay"`
	Reason string `json:"reason,omitempty"`
}

func (RelayClosed) MessageType() string { return TypeRelayClosed }

// AgentTaskFrame is Network's A2A task delivered by Bot. Network has already
// checked the sender, the receiving agent and any machine grant.
type AgentTaskFrame struct {
	Task json.RawMessage `json:"task"`
}

func (AgentTaskFrame) MessageType() string { return TypeAgentTask }

// AgentTaskResult answers a delivered task; Bot records it on Network, so only
// the machine the task was delivered to may send one.
type AgentTaskResult struct {
	TaskID string          `json:"task_id"`
	Status string          `json:"status"`
	Result json.RawMessage `json:"result,omitempty"`
}

func (AgentTaskResult) MessageType() string { return TypeAgentTaskResult }

type AgentTaskResultAck struct {
	TaskID string `json:"task_id"`
	Status string `json:"status"`
}

func (AgentTaskResultAck) MessageType() string { return TypeAgentTaskResultAck }

// AccessRevoked is kept for a Network/Bot build that closes a grant with this
// frame instead of relay.closed; it ends locally cached access at once.
type AccessRevoked struct {
	GrantID string `json:"grant_id"`
}

func (AccessRevoked) MessageType() string { return TypeAccessRevoked }
