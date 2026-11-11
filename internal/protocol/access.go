package protocol

import "encoding/json"

const (
	TypeAgentTask     = "agent.task"
	TypeAccessRevoked = "access.revoked"
	TypeMachineRelay  = "machine.relay"
	TypeMachineReply  = "machine.reply"
)

// AgentTaskFrame carries an A2A task from Network through Bot. A task from a
// different machine carries that machine's signed agent grant as well.
type AgentTaskFrame struct {
	Task        json.RawMessage `json:"task"`
	FromMachine string          `json:"from_machine,omitempty"`
	GrantToken  string          `json:"grant_token,omitempty"`
}

func (AgentTaskFrame) MessageType() string { return TypeAgentTask }

// AccessRevoked is Bot's immediate signal that a grant must stop locally.
type AccessRevoked struct {
	GrantID string `json:"grant_id"`
}

func (AccessRevoked) MessageType() string { return TypeAccessRevoked }

// MachineRelay asks this Link to use a named local feature for the sender.
// Bot checks the grant first, then Link independently verifies its token.
type MachineRelay struct {
	ID         string          `json:"id"`
	From       string          `json:"from"`
	To         string          `json:"to"`
	Scope      string          `json:"scope"`
	GrantToken string          `json:"grant_token"`
	Action     string          `json:"action"`
	Data       json.RawMessage `json:"data,omitempty"`
}

func (MachineRelay) MessageType() string { return TypeMachineRelay }

type MachineReply struct {
	ID    string          `json:"id"`
	From  string          `json:"from"`
	To    string          `json:"to"`
	OK    bool            `json:"ok"`
	Error string          `json:"error,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

func (MachineReply) MessageType() string { return TypeMachineReply }
