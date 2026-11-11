package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/OpenVibers/OpenVibe.Link/internal/access"
	"github.com/OpenVibers/OpenVibe.Link/internal/localctl"
	"github.com/OpenVibers/OpenVibe.Link/internal/mono"
	"github.com/OpenVibers/OpenVibe.Link/internal/protocol"
	"github.com/OpenVibers/OpenVibe.Link/internal/send"
)

func (n *Agent) receiveAgentTask(frame protocol.AgentTaskFrame) error {
	if !n.opt.Config.AgentsTalk {
		return errors.New("this machine has Let my agents talk turned off")
	}
	if n.accessInbox == nil || n.opt.Creds == nil || !n.opt.Creds.NetworkPaired() {
		return errors.New("agent inbox unavailable")
	}
	if len(frame.Task) == 0 || len(frame.Task) > 200<<10 {
		return errors.New("agent task is missing or too large")
	}
	if frame.FromMachine != "" {
		if n.grantVerifier == nil {
			return access.ErrGrant
		}
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		claims, err := n.grantVerifier.Verify(ctx, frame.GrantToken, frame.FromMachine, "agent")
		if err == nil && n.accessClient != nil {
			var grants json.RawMessage
			grants, err = n.accessClient.Grants(ctx)
			if err == nil && !grantStillActive(grants, claims.ID, frame.FromMachine, n.opt.Creds.Principal, "agent") {
				err = access.ErrGrant
			}
		}
		cancel()
		if err != nil {
			return err
		}
	}
	var task access.Task
	if json.Unmarshal(frame.Task, &task) != nil {
		return errors.New("invalid agent task")
	}
	n.mu.Lock()
	registered := false
	for _, local := range n.localAgents {
		if local.ID == task.To {
			registered = true
			break
		}
	}
	n.mu.Unlock()
	if !registered {
		return errors.New("task is not addressed to an agent registered on this machine")
	}
	task.FromMachine = frame.FromMachine
	if err := n.accessInbox.Add(task); err != nil {
		return err
	}
	n.addActivity(fmt.Sprintf("Agent task %s from %s on behalf of %s arrived", task.ID, task.From, task.OnBehalfOf))
	return nil
}

func (n *Agent) revokeAccess(frame protocol.AccessRevoked) {
	if n.grantVerifier != nil && frame.GrantID != "" {
		n.grantVerifier.Revoke(frame.GrantID, time.Now().Add(31*24*time.Hour))
		n.addActivity("Machine access grant " + frame.GrantID + " was revoked")
	}
}

func responseJSON(value any) localctl.Response {
	b, err := json.Marshal(value)
	if err != nil {
		return localctl.Response{Error: "could not encode the answer"}
	}
	return localctl.Response{OK: true, Data: b}
}

// A grant token is used only inside Link. Neither `access list --json` nor an
// MCP tool answer may return a bearer token to a model or a terminal log.
func publicJSON(raw json.RawMessage) any {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return map[string]any{}
	}
	return stripPrivate(value)
}

func publicData(raw json.RawMessage) json.RawMessage {
	b, _ := json.Marshal(publicJSON(raw))
	return b
}

func stripPrivate(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "credential") {
				continue
			}
			out[key] = stripPrivate(item)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = stripPrivate(item)
		}
		return out
	default:
		return value
	}
}

func (n *Agent) mcpControl(name, caller string, raw json.RawMessage) localctl.Response {
	if !n.opt.Config.AgentsTalk && (name == "agent_inbox" || name == "agent_reply") {
		return localctl.Response{Error: "Let my agents talk is off on this machine"}
	}
	switch name {
	case "agent_inbox":
		var args struct {
			Agent string `json:"agent"`
		}
		if json.Unmarshal(raw, &args) != nil {
			return localctl.Response{Error: "invalid inbox arguments"}
		}
		local, err := n.localAgentFor(caller)
		if err != nil {
			return localctl.Response{Error: err.Error()}
		}
		if args.Agent != "" && args.Agent != local.ID {
			return localctl.Response{Error: "this CLI cannot read another agent's inbox"}
		}
		return responseJSON(map[string]any{"tasks": n.accessInbox.List(local.ID)})
	case "agent_reply":
		var args struct {
			Task string `json:"task"`
			Text string `json:"text"`
		}
		if json.Unmarshal(raw, &args) != nil {
			return localctl.Response{Error: "invalid reply arguments"}
		}
		local, err := n.localAgentFor(caller)
		if err != nil {
			return localctl.Response{Error: err.Error()}
		}
		var pending *access.InboxEntry
		for _, entry := range n.accessInbox.List(local.ID) {
			if entry.Task.ID == args.Task {
				copy := entry
				pending = &copy
				break
			}
		}
		if pending == nil || pending.Status != "pending" {
			return localctl.Response{Error: "task is not in this agent's inbox"}
		}
		if n.accessClient == nil {
			return localctl.Response{Error: "Network unavailable"}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		result := map[string]any{"id": args.Task, "thread": pending.Task.Thread, "updated_at": time.Now().UTC().Format(time.RFC3339), "status": "completed", "result": map[string]any{"role": "agent", "parts": []any{map[string]any{"type": "text", "text": args.Text}}}}
		if _, err := n.accessClient.TaskResult(ctx, args.Task, result); err != nil {
			return localctl.Response{Error: err.Error()}
		}
		entry, err := n.accessInbox.Reply(args.Task, args.Text)
		if err != nil {
			return localctl.Response{Error: err.Error()}
		}
		return responseJSON(map[string]any{"id": entry.Task.ID, "thread": entry.Task.Thread, "status": entry.Status})
	case "agents_list":
		if n.accessClient == nil {
			return localctl.Response{Error: "pair this machine through Network first"}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		data, err := n.accessClient.Agents(ctx)
		if err != nil {
			return localctl.Response{Error: err.Error()}
		}
		return localctl.Response{OK: true, Data: data}
	case "machines_list":
		if n.accessClient == nil {
			return localctl.Response{Error: "pair this machine through Network first"}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		machines, err := n.accessClient.Machines(ctx)
		if err != nil {
			return localctl.Response{Error: err.Error()}
		}
		grants, err := n.accessClient.Grants(ctx)
		if err != nil {
			return localctl.Response{Error: err.Error()}
		}
		return responseJSON(map[string]any{"machines": publicJSON(machines), "grants": publicJSON(grants)})
	case "machine_request_access":
		if n.accessClient == nil {
			return localctl.Response{Error: "pair this machine through Network first"}
		}
		local, err := n.localAgentFor(caller)
		if err != nil {
			return localctl.Response{Error: err.Error()}
		}
		var args struct {
			Machine    string   `json:"machine"`
			Scopes     []string `json:"scopes"`
			ForSeconds int64    `json:"for_seconds"`
			Reason     string   `json:"reason"`
		}
		if json.Unmarshal(raw, &args) != nil {
			return localctl.Response{Error: "invalid access request"}
		}
		if args.ForSeconds == 0 {
			args.ForSeconds = 3600
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		data, err := n.accessClient.Request(ctx, args.Machine, args.Scopes, args.ForSeconds, args.Reason, local.ID)
		if err != nil {
			return localctl.Response{Error: err.Error()}
		}
		return localctl.Response{OK: true, Data: publicData(data)}
	case "agent_tell", "agent_ask":
		return n.sendAgentTask(name, caller, raw)
	case "machine_use":
		return n.machineUse(raw)
	}
	return localctl.Response{Error: "unknown MCP tool"}
}

type relaySource struct{ to, relayID string }

func validRelayID(id string) bool {
	if len(id) != 36 || !strings.HasPrefix(id, "mrl_") {
		return false
	}
	for _, c := range id[4:] {
		if c < '0' || c > '9' {
			if c < 'a' || c > 'f' {
				return false
			}
		}
	}
	return true
}

func machineGrant(raw json.RawMessage, self, machine, scope string) string {
	var list struct {
		Grants []struct {
			ID      string   `json:"id"`
			From    string   `json:"from_principal"`
			To      string   `json:"to_principal"`
			Scopes  []string `json:"scopes"`
			Token   string   `json:"grant_token"`
			Revoked *string  `json:"revoked_at"`
			Expires *string  `json:"expires_at"`
		} `json:"grants"`
	}
	if json.Unmarshal(raw, &list) != nil {
		return ""
	}
	for _, grant := range list.Grants {
		if grant.From != self || grant.To != machine || grant.Token == "" || grant.Revoked != nil {
			continue
		}
		if grant.Expires != nil {
			at, err := time.Parse(time.RFC3339, *grant.Expires)
			if err != nil || !at.After(time.Now()) {
				continue
			}
		}
		for _, s := range grant.Scopes {
			if s == scope {
				return grant.Token
			}
		}
	}
	return ""
}

func (n *Agent) machineUse(raw json.RawMessage) localctl.Response {
	if n.accessClient == nil || n.link == nil || n.opt.Creds == nil {
		return localctl.Response{Error: "machine relay unavailable"}
	}
	var args struct {
		Machine string          `json:"machine"`
		Scope   string          `json:"scope"`
		Action  string          `json:"action"`
		Data    json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &args) != nil || args.Machine == "" || args.Machine == n.opt.Creds.Principal || !access.ValidScope(args.Scope) || !validRelayAction(args.Scope, args.Action) || len(args.Data) > 1<<20 {
		return localctl.Response{Error: "invalid machine use"}
	}
	if len(args.Data) == 0 {
		args.Data = json.RawMessage(`{}`)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	grants, err := n.accessClient.Grants(ctx)
	cancel()
	if err != nil {
		return localctl.Response{Error: err.Error()}
	}
	token := machineGrant(grants, n.opt.Creds.Principal, args.Machine, args.Scope)
	if token == "" {
		return localctl.Response{Error: "no active grant for this machine and scope"}
	}
	if args.Scope == "send" && args.Action == "offer" {
		var offer struct {
			Envelope string `json:"envelope"`
		}
		if json.Unmarshal(args.Data, &offer) != nil || offer.Envelope == "" || len(offer.Envelope) > 900000 {
			return localctl.Response{Error: "invalid sealed item"}
		}
		client := &send.Client{BaseURL: n.opt.Creds.Network, Tokens: n.accessClient.Tokens, Self: n.opt.Creds.Principal}
		lanCtx, lanCancel := context.WithTimeout(context.Background(), 3*time.Second)
		id, lanErr := client.SendGrantedLAN(lanCtx, args.Machine, offer.Envelope, token)
		lanCancel()
		if lanErr == nil {
			n.addActivity("Sent a sealed item to machine " + args.Machine + " on the LAN")
			return responseJSON(map[string]any{"machine": args.Machine, "scope": "send", "result": map[string]any{"id": id, "transport": "lan"}})
		}
	}
	id := access.NewRelayID()
	if args.Scope == "worker" && args.Action == "run" {
		var job protocol.Job
		if json.Unmarshal(args.Data, &job) != nil || !protocol.ValidJobID(job.ID) {
			return localctl.Response{Error: "invalid worker job"}
		}
	}
	if id == "" {
		return localctl.Response{Error: "could not create relay id"}
	}
	wait := make(chan protocol.MachineReply, 1)
	n.mu.Lock()
	n.relayWaiting[id] = wait
	n.relayTargets[id] = args.Machine
	n.mu.Unlock()
	defer func() { n.mu.Lock(); delete(n.relayWaiting, id); delete(n.relayTargets, id); n.mu.Unlock() }()
	frame := protocol.MachineRelay{ID: id, From: n.opt.Creds.Principal, To: args.Machine, Scope: args.Scope, GrantToken: token, Action: args.Action, Data: args.Data}
	if err := n.link.Send(frame); err != nil {
		return localctl.Response{Error: err.Error()}
	}
	select {
	case reply := <-wait:
		if !reply.OK {
			return localctl.Response{Error: reply.Error}
		}
		n.addActivity("Used " + args.Scope + " on machine " + args.Machine)
		return responseJSON(map[string]any{"id": id, "machine": args.Machine, "scope": args.Scope, "result": publicJSON(reply.Data)})
	case <-time.After(120 * time.Second):
		return localctl.Response{Error: "machine did not answer within 120 seconds"}
	}
}

func validRelayAction(scope, action string) bool {
	switch scope {
	case "agent":
		return action == "task"
	case "send":
		return action == "offer"
	case "screen":
		return action == protocol.KindScreen || action == protocol.KindScreenStream
	case "desk":
		return action == protocol.KindDesk || action == protocol.KindDeskStream
	case "computer":
		return action == protocol.KindExec || action == protocol.KindFS || action == protocol.KindInput || action == protocol.KindBrowser || action == protocol.KindProbe
	case "worker":
		return action == "run" || action == "cancel" || action == "status"
	}
	return false
}

func (n *Agent) receiveMachineReply(reply protocol.MachineReply) {
	if n.opt.Creds == nil || reply.To != n.opt.Creds.Principal {
		return
	}
	n.mu.Lock()
	ch := n.relayWaiting[reply.ID]
	target := n.relayTargets[reply.ID]
	n.mu.Unlock()
	if ch != nil && target == reply.From {
		select {
		case ch <- reply:
		default:
		}
	}
}

func (n *Agent) receiveMachineRelay(frame protocol.MachineRelay) {
	self := ""
	if n.opt.Creds != nil {
		self = n.opt.Creds.Principal
	}
	answer := func(data any, err error) {
		var result json.RawMessage
		if err == nil {
			result, _ = json.Marshal(data)
		}
		reply := protocol.MachineReply{ID: frame.ID, From: self, To: frame.From, OK: err == nil, Data: result}
		if err != nil {
			reply.Error = err.Error()
		}
		n.send(reply)
	}
	if self == "" || frame.To != self || frame.From == "" || !validRelayID(frame.ID) || !validRelayAction(frame.Scope, frame.Action) || len(frame.Data) > 1<<20 {
		answer(nil, access.ErrGrant)
		return
	}
	if n.grantVerifier == nil || n.accessClient == nil {
		answer(nil, access.ErrGrant)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	claims, err := n.grantVerifier.Verify(ctx, frame.GrantToken, frame.From, frame.Scope)
	if err == nil {
		var grants json.RawMessage
		grants, err = n.accessClient.Grants(ctx)
		if err == nil && !grantStillActive(grants, claims.ID, frame.From, frame.To, frame.Scope) {
			err = access.ErrGrant
		}
	}
	cancel()
	if err != nil {
		answer(nil, access.ErrGrant)
		return
	}
	if n.capabilityOff(relayCapability(frame.Scope, frame.Action)) {
		answer(nil, errors.New("this capability is off on the target machine"))
		return
	}
	n.addActivity("Machine " + frame.From + " used " + frame.Scope + " (grant " + claims.ID + ")")
	switch frame.Scope {
	case "agent":
		if !n.opt.Config.AgentsTalk {
			answer(nil, errors.New("Let my agents talk is off on this machine"))
			return
		}
		err = n.receiveAgentTask(protocol.AgentTaskFrame{Task: frame.Data, FromMachine: frame.From, GrantToken: frame.GrantToken})
		answer(map[string]any{"queued": err == nil}, err)
	case "send":
		var offer struct {
			Envelope string `json:"envelope"`
		}
		if json.Unmarshal(frame.Data, &offer) != nil || len(offer.Envelope) > 900000 {
			answer(nil, errors.New("invalid sealed item"))
			return
		}
		n.mu.Lock()
		lan := n.grantedSend
		n.mu.Unlock()
		if lan == nil {
			answer(nil, errors.New("Link Send is off on this machine"))
			return
		}
		id, err := lan.ReceiveGranted(frame.From, offer.Envelope)
		answer(map[string]any{"id": id}, err)
	case "worker":
		if frame.Action == "cancel" || frame.Action == "status" {
			var body struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(frame.Data, &body) != nil || !protocol.ValidJobID(body.ID) {
				answer(nil, errors.New("invalid job id"))
				return
			}
			if frame.Action == "status" {
				result, ok := n.jobs.answer(body.ID)
				if !ok {
					answer(nil, errors.New("job not found"))
					return
				}
				answer(result, nil)
				return
			}
			n.jobCancel(protocol.JobCancel{ID: body.ID})
			answer(map[string]any{"cancelled": true}, nil)
			return
		}
		var job protocol.Job
		if json.Unmarshal(frame.Data, &job) != nil || job.ID == "" {
			answer(nil, errors.New("invalid job"))
			return
		}
		if !protocol.ValidJobID(job.ID) {
			answer(nil, errors.New("invalid job id"))
			return
		}
		n.mu.Lock()
		n.relaySources[job.ID] = relaySource{to: frame.From, relayID: frame.ID}
		n.mu.Unlock()
		n.job(protocol.JobRequest{Job: job})
	default:
		var value json.RawMessage = frame.Data
		if !json.Valid(value) {
			answer(nil, errors.New("invalid command data"))
			return
		}
		n.mu.Lock()
		n.relaySources[frame.ID] = relaySource{to: frame.From, relayID: frame.ID}
		n.mu.Unlock()
		n.command(protocol.Command{ID: frame.ID, Kind: frame.Action, Value: value, Operator: &protocol.Operator{Subject: frame.From, Role: "machine"}}, time.Now().UnixMilli(), 0, mono.Now())
	}
}

func grantStillActive(raw json.RawMessage, id, from, to, scope string) bool {
	var list struct {
		Grants []struct {
			ID      string   `json:"id"`
			From    string   `json:"from_principal"`
			To      string   `json:"to_principal"`
			Scopes  []string `json:"scopes"`
			Revoked *string  `json:"revoked_at"`
			Expires *string  `json:"expires_at"`
		} `json:"grants"`
	}
	if json.Unmarshal(raw, &list) != nil {
		return false
	}
	for _, g := range list.Grants {
		if g.ID != id || g.From != from || g.To != to || g.Revoked != nil {
			continue
		}
		if g.Expires != nil {
			at, err := time.Parse(time.RFC3339, *g.Expires)
			if err != nil || !at.After(time.Now()) {
				continue
			}
		}
		for _, s := range g.Scopes {
			if s == scope {
				return true
			}
		}
	}
	return false
}

func relayCapability(scope, action string) string {
	switch scope {
	case "screen":
		return protocol.KindScreen
	case "desk":
		return protocol.KindDesk
	case "computer":
		return action
	}
	return ""
}

func (n *Agent) relayResult(m protocol.Message) bool {
	var id string
	switch v := m.(type) {
	case protocol.Ack:
		id = v.ID
	case protocol.AckData:
		id = v.ID
	case protocol.Nack:
		id = v.ID
	case protocol.JobExit:
		id = v.ID
	default:
		return false
	}
	n.mu.Lock()
	source := n.relaySources[id]
	if source.to != "" {
		delete(n.relaySources, id)
	}
	n.mu.Unlock()
	if source.to == "" {
		return false
	}
	raw, _ := json.Marshal(m)
	reply := protocol.MachineReply{ID: source.relayID, From: n.opt.Creds.Principal, To: source.to, OK: true, Data: raw}
	if nk, ok := m.(protocol.Nack); ok {
		reply.OK = false
		reply.Error = nk.Message
	}
	if n.link != nil {
		_ = n.link.Send(reply)
	}
	return true
}

type localAgent struct{ ID, Owner string }

func (n *Agent) localAgentFor(cli string) (localAgent, error) {
	if !n.opt.Config.AgentsTalk {
		return localAgent{}, errors.New("Let my agents talk is off on this machine")
	}
	n.mu.Lock()
	entry, ok := n.localAgents[cli]
	n.mu.Unlock()
	if cli == "" || !ok || entry.ID == "" || entry.Owner == "" {
		return localAgent{}, errors.New("this CLI is not registered in the agent directory yet")
	}
	return entry, nil
}

func (n *Agent) registerLocalAgents(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		n.registerLocalAgentsOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (n *Agent) registerLocalAgentsOnce(ctx context.Context) {
	if n.accessClient == nil || n.opt.Creds == nil || !n.opt.Config.AgentsTalk {
		return
	}
	for _, cli := range n.codesAgents.latest(n.probeCodesAgents) {
		if cli.State == "missing" {
			continue
		}
		status := "offline"
		if cli.State == "ready" {
			status = "online"
		}
		entry := map[string]any{"kind": "link-local", "local_id": cli.ID, "name": cli.Name, "machine_id": n.opt.Creds.Principal, "skills": []string{}, "status": status, "allow_others": false}
		callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		raw, err := n.accessClient.RegisterAgent(callCtx, entry)
		cancel()
		if err != nil {
			n.log.Debug("agent registration unavailable", "cli", cli.ID, "err", err)
			continue
		}
		var result struct {
			ID    string `json:"id"`
			Owner string `json:"owner"`
			Agent struct {
				ID    string `json:"id"`
				Owner string `json:"owner"`
			} `json:"agent"`
		}
		if json.Unmarshal(raw, &result) != nil {
			continue
		}
		if result.ID == "" {
			result.ID = result.Agent.ID
		}
		if result.Owner == "" {
			result.Owner = result.Agent.Owner
		}
		if !strings.HasPrefix(result.ID, "agt_") || result.Owner == "" {
			continue
		}
		n.mu.Lock()
		n.localAgents[cli.ID] = localAgent{ID: result.ID, Owner: result.Owner}
		n.mu.Unlock()
	}
}

func (n *Agent) sendAgentTask(name, caller string, raw json.RawMessage) localctl.Response {
	if n.accessClient == nil {
		return localctl.Response{Error: "pair this machine through Network first"}
	}
	local, err := n.localAgentFor(caller)
	if err != nil {
		return localctl.Response{Error: err.Error()}
	}
	var args struct {
		Agent       string `json:"agent"`
		Text        string `json:"text"`
		Thread      string `json:"thread"`
		WaitSeconds int    `json:"wait_seconds"`
	}
	if json.Unmarshal(raw, &args) != nil || len(args.Text) > 8000 || args.Agent == "" || args.Text == "" {
		return localctl.Response{Error: "invalid agent message"}
	}
	if name == "agent_ask" {
		if args.WaitSeconds == 0 {
			args.WaitSeconds = 30
		}
		if args.WaitSeconds < 1 || args.WaitSeconds > 120 {
			return localctl.Response{Error: "wait_seconds must be 1 to 120"}
		}
	}
	if args.Thread == "" {
		args.Thread = access.NewTaskID()
	}
	if args.Thread == "" {
		return localctl.Response{Error: "could not make a thread id"}
	}
	task := map[string]any{"to": args.Agent, "from": local.ID, "on_behalf_of": local.Owner, "thread": args.Thread, "message": map[string]any{"role": "user", "parts": []any{map[string]any{"type": "text", "text": args.Text}}}}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	data, err := n.accessClient.Task(ctx, args.Agent, task)
	cancel()
	if err != nil {
		return localctl.Response{Error: err.Error()}
	}
	if name == "agent_tell" {
		return localctl.Response{OK: true, Data: data}
	}
	var created struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if json.Unmarshal(data, &created) != nil || created.ID == "" {
		return localctl.Response{Error: "Network did not return a task id"}
	}
	if created.Status == "completed" || created.Status == "failed" || created.Status == "cancelled" {
		return localctl.Response{OK: true, Data: data}
	}
	deadline := time.Now().Add(time.Duration(args.WaitSeconds) * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		pollCtx, pollCancel := context.WithTimeout(context.Background(), 5*time.Second)
		data, err = n.accessClient.TaskStatus(pollCtx, created.ID)
		pollCancel()
		if err != nil {
			return localctl.Response{Error: err.Error()}
		}
		var result struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(data, &result) != nil {
			return localctl.Response{Error: "Network returned an invalid task"}
		}
		if result.Status == "completed" || result.Status == "failed" || result.Status == "cancelled" {
			return localctl.Response{OK: true, Data: data}
		}
	}
	return responseJSON(map[string]any{"id": created.ID, "status": "pending", "thread": args.Thread})
}

func (n *Agent) accessControl(action string, raw json.RawMessage) localctl.Response {
	if n.accessClient == nil {
		return localctl.Response{Error: "pair this machine through OpenVibe.Network first"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var args struct {
		Machine  string   `json:"machine"`
		Scopes   []string `json:"scopes"`
		Duration int64    `json:"duration_seconds"`
		Reason   string   `json:"reason"`
		ID       string   `json:"id"`
	}
	if len(raw) > 0 && json.Unmarshal(raw, &args) != nil {
		return localctl.Response{Error: "invalid access arguments"}
	}
	var data json.RawMessage
	var err error
	switch action {
	case "request":
		data, err = n.accessClient.Request(ctx, args.Machine, args.Scopes, args.Duration, args.Reason, "")
	case "list":
		var requests, grants json.RawMessage
		requests, err = n.accessClient.List(ctx)
		if err == nil {
			grants, err = n.accessClient.Grants(ctx)
		}
		if err == nil {
			return responseJSON(map[string]any{"requests": publicJSON(requests), "grants": publicJSON(grants)})
		}
	case "approve", "deny":
		data, err = n.accessClient.Answer(ctx, args.ID, action == "approve", args.Scopes, args.Duration)
	case "revoke":
		data, err = n.accessClient.Revoke(ctx, args.ID)
	default:
		return localctl.Response{Error: "unknown access action"}
	}
	if err != nil {
		return localctl.Response{Error: err.Error()}
	}
	if action != "list" {
		n.addActivity("Machine access " + action + " completed")
	}
	return localctl.Response{OK: true, Data: publicData(data)}
}

type incomingAccessRequest struct {
	ID              string   `json:"id"`
	FromPrincipal   string   `json:"from_principal"`
	ToPrincipal     string   `json:"to_principal"`
	FromOwner       string   `json:"from_owner"`
	FromAgent       string   `json:"from_agent"`
	FromMachineName string   `json:"from_machine_name"`
	FromOwnerName   string   `json:"from_owner_name"`
	FromAgentName   string   `json:"from_agent_name"`
	Scopes          []string `json:"scopes"`
	Reason          string   `json:"reason"`
	Status          string   `json:"status"`
	RequestedAt     string   `json:"requested_at"`
	ExpiresAt       string   `json:"expires_at"`
	DurationSeconds int64    `json:"duration_seconds"`
}

func accessRequestWords(request incomingAccessRequest) (string, string) {
	actor := request.FromPrincipal
	if request.FromOwnerName != "" && request.FromMachineName != "" {
		actor = request.FromOwnerName + "'s " + request.FromMachineName
	} else if request.FromMachineName != "" {
		actor = request.FromMachineName
	}
	if request.FromAgentName != "" {
		actor = request.FromAgentName + " on " + actor
	}
	words := map[string]string{"agent": "message your agents", "send": "send you files", "screen": "watch the screen", "desk": "use a private desk", "computer": "control this computer", "worker": "run jobs in the sandbox"}
	var parts []string
	for _, scope := range request.Scopes {
		if word := words[scope]; word != "" {
			parts = append(parts, word)
		}
	}
	if len(parts) == 0 {
		parts = []string{"request access"}
	}
	phrase := parts[0]
	if len(parts) > 1 {
		phrase = strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
	}
	phrase = strings.ToUpper(phrase[:1]) + phrase[1:]
	duration := "until you take it back"
	if request.DurationSeconds > 0 {
		duration = (time.Duration(request.DurationSeconds) * time.Second).String()
		if request.DurationSeconds == 3600 {
			duration = "1 hour"
		}
	}
	body := phrase + ", for " + duration + "."
	if request.Reason != "" {
		body += " " + strconv.Quote(request.Reason)
	}
	return actor + " asks for access", body
}

func (n *Agent) pollAccessRequests(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		n.refreshAccessRequests(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (n *Agent) refreshAccessRequests(ctx context.Context) {
	if n.accessClient == nil || n.opt.Creds == nil {
		return
	}
	callCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	raw, err := n.accessClient.List(callCtx)
	if err != nil {
		n.log.Debug("machine access requests unavailable", "err", err)
		return
	}
	var result struct {
		Requests []incomingAccessRequest `json:"requests"`
	}
	if json.Unmarshal(raw, &result) != nil {
		return
	}
	want := map[string]incomingAccessRequest{}
	for _, request := range result.Requests {
		if request.Status == "pending" && request.ToPrincipal == n.opt.Creds.Principal {
			want[request.ID] = request
		}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for id, a := range n.approvals {
		if a.Plugin == "machine-access" {
			if _, ok := want[id]; !ok {
				delete(n.approvals, id)
			}
		}
	}
	for id, request := range want {
		if _, ok := n.approvals[id]; ok || len(n.approvals) >= 64 {
			continue
		}
		nonce := access.NewTaskID()
		if nonce == "" {
			continue
		}
		title, summary := accessRequestWords(request)
		at, _ := time.Parse(time.RFC3339, request.RequestedAt)
		if at.IsZero() {
			at = time.Now()
		}
		n.approvals[id] = Approval{ID: nonce, CommandID: id, Plugin: "machine-access", Kind: "Machine access", Title: title, Summary: summary, At: at.UnixMilli()}
		n.appendActivityLocked("Machine access request from " + request.FromPrincipal + " is waiting")
	}
}

func (n *Agent) answerAccessApproval(a Approval, approved bool) error {
	if n.accessClient == nil {
		return errors.New("machine access unavailable")
	}
	n.mu.Lock()
	current, ok := n.approvals[a.CommandID]
	if !ok || current.ID != a.ID {
		n.mu.Unlock()
		return errors.New("that machine access request no longer waits")
	}
	if approved && n.drivingLocked() {
		n.mu.Unlock()
		return errors.New("the agent is using the mouse or keyboard; answer again in a moment")
	}
	n.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := n.accessClient.Answer(ctx, a.CommandID, approved, nil, 0); err != nil {
		return err
	}
	n.mu.Lock()
	if current, ok := n.approvals[a.CommandID]; ok && current.ID == a.ID {
		delete(n.approvals, a.CommandID)
	}
	n.mu.Unlock()
	verb := "denied"
	if approved {
		verb = "approved"
	}
	n.addActivity("You " + verb + " machine access request " + a.CommandID)
	return nil
}
