package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/OpenVibers/OpenVibe.Link/internal/access"
	"github.com/OpenVibers/OpenVibe.Link/internal/localctl"
	"github.com/OpenVibers/OpenVibe.Link/internal/mono"
	"github.com/OpenVibers/OpenVibe.Link/internal/protocol"
	"github.com/OpenVibers/OpenVibe.Link/internal/send"
)

func (n *Agent) agentResultAck(taskID, failure string) {
	n.mu.Lock()
	ch := n.agentResults[taskID]
	n.mu.Unlock()
	if ch != nil {
		select {
		case ch <- failure:
		default:
		}
	}
}

func (n *Agent) failAgentResults(detail string) {
	n.mu.Lock()
	waiters := make([]chan string, 0, len(n.agentResults))
	for id, ch := range n.agentResults {
		waiters = append(waiters, ch)
		delete(n.agentResults, id)
	}
	n.mu.Unlock()
	for _, ch := range waiters {
		select {
		case ch <- detail:
		default:
		}
	}
}

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
	n.mu.Lock()
	sessions := make([]*machineRelay, 0)
	for _, session := range n.relaySessions {
		if session.Claims.ID == frame.GrantID {
			sessions = append(sessions, session)
		}
	}
	n.mu.Unlock()
	for _, session := range sessions {
		n.closeRelay(session, "revoked")
	}
}

func machineGrant(raw json.RawMessage, self, machine, scope string) string {
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
		return ""
	}
	for _, grant := range list.Grants {
		if grant.From != self || grant.To != machine || grant.ID == "" || grant.Revoked != nil {
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
				return grant.ID
			}
		}
	}
	return ""
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
		if n.link == nil {
			return localctl.Response{Error: "the Link is not connected to OpenVibe.Bot"}
		}
		if strings.TrimSpace(args.Text) == "" || len(args.Text) > 8000 {
			return localctl.Response{Error: "a reply needs up to 8000 characters"}
		}
		result, _ := json.Marshal(map[string]any{"parts": []any{map[string]any{"type": "text", "text": args.Text}}})
		// Bot records the answer on Network; a node token may not post agent
		// task results directly, and Bot checks the task was delivered here.
		waiter := make(chan string, 1)
		n.mu.Lock()
		n.agentResults[args.Task] = waiter
		n.mu.Unlock()
		defer func() {
			n.mu.Lock()
			delete(n.agentResults, args.Task)
			n.mu.Unlock()
		}()
		if err := n.link.Send(protocol.AgentTaskResult{TaskID: args.Task, Status: "completed", Result: result}); err != nil {
			return localctl.Response{Error: err.Error()}
		}
		select {
		case failure := <-waiter:
			if failure != "" {
				return localctl.Response{Error: failure}
			}
		case <-time.After(10 * time.Second):
			return localctl.Response{Error: "Bot did not record the answer in time"}
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
			// Network's node-self directory route is newer than this Link. A
			// local machine still lists the CLIs it has registered.
			return responseJSON(map[string]any{"agents": n.localAgentEntries()})
		}
		return localctl.Response{OK: true, Data: publicData(data)}
	case "machines_list":
		if n.accessClient == nil {
			return localctl.Response{Error: "pair this machine through Network first"}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		grants, err := n.accessClient.Grants(ctx)
		if err != nil {
			return localctl.Response{Error: err.Error()}
		}
		machines, machineErr := n.accessClient.Machines(ctx)
		if machineErr != nil {
			machines = json.RawMessage(`{"machines":[]}`)
		}
		var machineList struct {
			Machines []any `json:"machines"`
		}
		var grantList struct {
			Grants []any `json:"grants"`
		}
		_ = json.Unmarshal(machines, &machineList)
		_ = json.Unmarshal(grants, &grantList)
		if machineList.Machines == nil {
			machineList.Machines = []any{}
		}
		if grantList.Grants == nil {
			grantList.Grants = []any{}
		}
		return responseJSON(map[string]any{"machines": stripPrivate(machineList.Machines), "grants": stripPrivate(grantList.Grants)})
	case "machine_request_access":
		if n.accessClient == nil {
			return localctl.Response{Error: "pair this machine through Network first"}
		}
		// The request names the agent when "Let my agents talk" has registered
		// this CLI. Without it the machine itself still asks, so a local agent
		// can request access before the owner turns agent messages on.
		agentID := ""
		if local, err := n.localAgentFor(caller); err == nil {
			agentID = local.ID
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
		data, err := n.accessClient.Request(ctx, args.Machine, args.Scopes, args.ForSeconds, args.Reason, agentID)
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

type relayRequest struct {
	ID     string          `json:"id"`
	Action string          `json:"action"`
	Level  string          `json:"level,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`
}

type relayReply struct {
	ID    string          `json:"id"`
	OK    bool            `json:"ok"`
	Error string          `json:"error,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

type relayOpened struct {
	Frame protocol.RelayOpened
	Err   error
}

// machineRelay is one relay.opened session Bot keeps open between two Links.
// Opener is true on the machine that asked; Peer is the other machine.
type machineRelay struct {
	ID        string
	Peer      string
	Token     string
	Claims    access.Claims
	Scopes    map[string]bool
	Expires   time.Time
	Opener    bool
	CheckedAt time.Time
	WaitingID string
}

type relaySource struct {
	to, scope, relay, bodyID string
}

func validRelayID(id string) bool {
	if len(id) < 12 || len(id) > 84 || !strings.HasPrefix(id, "rly_") {
		return false
	}
	for _, c := range id[4:] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

func validOperationID(id string) bool {
	if len(id) != 36 || !strings.HasPrefix(id, "mrl_") {
		return false
	}
	for _, c := range id[4:] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
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
	if !strings.HasPrefix(args.Machine, "lnk_") {
		return localctl.Response{Error: "invalid machine id"}
	}
	if len(args.Data) == 0 {
		args.Data = json.RawMessage(`{}`)
	}
	if args.Scope == "computer" {
		var object map[string]any
		if json.Unmarshal(args.Data, &object) != nil || object == nil {
			return localctl.Response{Error: "a computer action must be a JSON object"}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	grants, err := n.accessClient.Grants(ctx)
	cancel()
	if err != nil {
		return localctl.Response{Error: err.Error()}
	}
	grantID := machineGrant(grants, n.opt.Creds.Principal, args.Machine, args.Scope)
	if grantID == "" {
		return localctl.Response{Error: "no active grant for this machine and scope"}
	}
	ctx, cancel = context.WithTimeout(context.Background(), 8*time.Second)
	tokenAnswer, err := n.accessClient.GrantToken(ctx, grantID)
	if err != nil {
		cancel()
		return localctl.Response{Error: err.Error()}
	}
	var tokenRow struct {
		GrantToken string `json:"grant_token"`
	}
	if json.Unmarshal(tokenAnswer, &tokenRow) != nil || tokenRow.GrantToken == "" {
		cancel()
		return localctl.Response{Error: "Network did not issue the grant token"}
	}
	token := tokenRow.GrantToken
	claims, err := access.NewVerifier(n.opt.Creds.Network, args.Machine).Verify(ctx, token, n.opt.Creds.Principal, args.Scope)
	cancel()
	if err != nil || claims.ID != grantID {
		return localctl.Response{Error: "Network issued an invalid grant token"}
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
	bodyID := access.NewRelayID()
	if bodyID == "" {
		return localctl.Response{Error: "could not create a relay id"}
	}
	if args.Scope == "worker" && args.Action == "run" {
		var job protocol.Job
		if json.Unmarshal(args.Data, &job) != nil || !protocol.ValidJobID(job.ID) {
			return localctl.Response{Error: "invalid worker job"}
		}
	}
	body := map[string]any{"id": bodyID, "action": args.Action, "data": args.Data}
	if args.Scope == "computer" {
		level := requestedComputerLevel(args.Data)
		body["level"] = level
	}
	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return localctl.Response{Error: "could not encode the relay body"}
	}
	if len(bodyJSON) > 256<<10 {
		return localctl.Response{Error: "the relay body is larger than 256 KiB; send files on the LAN or use a smaller item"}
	}
	// Bot opens one relay per operation: relay.open, relay.opened to both
	// machines, then relay.frame each way. The receiving Link verifies the same
	// token and checks the scope again.
	opened := make(chan relayOpened, 1)
	n.mu.Lock()
	if n.relayOpening[args.Machine] != nil {
		n.mu.Unlock()
		return localctl.Response{Error: "another relay to this machine is opening"}
	}
	n.relayOpening[args.Machine] = opened
	n.mu.Unlock()
	defer func() {
		n.mu.Lock()
		if n.relayOpening[args.Machine] == opened {
			delete(n.relayOpening, args.Machine)
		}
		n.mu.Unlock()
	}()
	if err := n.link.Send(protocol.RelayOpen{To: args.Machine, GrantToken: token}); err != nil {
		return localctl.Response{Error: err.Error()}
	}
	var session *machineRelay
	select {
	case answer := <-opened:
		if answer.Err != nil {
			return localctl.Response{Error: answer.Err.Error()}
		}
		n.mu.Lock()
		session = n.relaySessions[answer.Frame.Relay]
		n.mu.Unlock()
	case <-time.After(15 * time.Second):
		return localctl.Response{Error: "the other machine did not open the relay in time"}
	}
	if session == nil {
		return localctl.Response{Error: "the relay opened without a verified grant"}
	}
	wait := make(chan relayReply, 1)
	n.mu.Lock()
	n.relayWaiting[bodyID] = wait
	if current := n.relaySessions[session.ID]; current != nil {
		current.WaitingID = bodyID
	}
	n.mu.Unlock()
	defer func() {
		n.mu.Lock()
		delete(n.relayWaiting, bodyID)
		n.mu.Unlock()
	}()
	if err := n.link.Send(protocol.RelayFrame{Relay: session.ID, Scope: args.Scope, Body: bodyJSON}); err != nil {
		n.closeRelay(session, "send failed")
		return localctl.Response{Error: err.Error()}
	}
	select {
	case reply := <-wait:
		n.closeRelay(session, "done")
		if !reply.OK {
			return localctl.Response{Error: reply.Error}
		}
		n.addActivity("Used " + args.Scope + " on machine " + args.Machine)
		return responseJSON(map[string]any{"id": bodyID, "machine": args.Machine, "scope": args.Scope, "result": publicJSON(reply.Data)})
	case <-time.After(120 * time.Second):
		n.closeRelay(session, "timeout")
		return localctl.Response{Error: "machine did not answer within 120 seconds"}
	}
}

func requestedComputerLevel(raw json.RawMessage) string {
	var object struct {
		Level string `json:"level"`
	}
	_ = json.Unmarshal(raw, &object)
	switch object.Level {
	case "observe", "ask", "trusted", "full":
		return object.Level
	}
	return "ask"
}

func computerRank(level string) int {
	switch level {
	case "observe":
		return 0
	case "ask":
		return 1
	case "trusted":
		return 2
	case "full":
		return 3
	}
	return -1
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

func (n *Agent) receiveRelayOpened(frame protocol.RelayOpened) {
	self := ""
	if n.opt.Creds != nil {
		self = n.opt.Creds.Principal
	}
	refuse := func(reason string) {
		if n.link != nil {
			_ = n.link.Send(protocol.RelayClose{Relay: frame.Relay})
		}
		n.mu.Lock()
		if ch := n.relayOpening[frame.To]; ch != nil && frame.From == self {
			select {
			case ch <- relayOpened{Err: errors.New(reason)}:
			default:
			}
		}
		n.mu.Unlock()
	}
	if self == "" || n.accessClient == nil || !validRelayID(frame.Relay) || frame.GrantToken == "" || len(frame.Scopes) == 0 ||
		frame.From == "" || frame.To == "" || frame.From == frame.To || (frame.From != self && frame.To != self) {
		refuse("invalid relay opening")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	claims, err := access.NewVerifier(n.opt.Creds.Network, frame.To).Verify(ctx, frame.GrantToken, frame.From, frame.Scopes[0])
	cancel()
	if err != nil || claims.ID == "" || claims.From != frame.From || claims.To != frame.To || claims.Expiry <= time.Now().Unix() {
		refuse("the relay grant was not valid")
		return
	}
	scopes := map[string]bool{}
	for _, scope := range claims.Scopes {
		if !access.ValidScope(scope) {
			refuse("the relay grant names an unknown scope")
			return
		}
		scopes[scope] = true
	}
	peer := frame.From
	if frame.From == self {
		peer = frame.To
	}
	session := &machineRelay{ID: frame.Relay, Peer: peer, Token: frame.GrantToken, Claims: claims,
		Scopes: scopes, Expires: time.Unix(claims.Expiry, 0), Opener: frame.From == self}
	n.mu.Lock()
	if n.relaySessions[frame.Relay] != nil {
		n.mu.Unlock()
		refuse("the relay is already open")
		return
	}
	n.relaySessions[frame.Relay] = session
	var ch chan relayOpened
	if session.Opener {
		ch = n.relayOpening[frame.To]
	}
	n.mu.Unlock()
	if ch != nil {
		select {
		case ch <- relayOpened{Frame: frame}:
		default:
		}
	}
}

func (n *Agent) receiveRelayFrame(frame protocol.RelayFrame) {
	n.mu.Lock()
	session := n.relaySessions[frame.Relay]
	n.mu.Unlock()
	if session == nil || frame.From != session.Peer || !session.Scopes[frame.Scope] || len(frame.Body) == 0 || len(frame.Body) > 500<<10 {
		if session != nil {
			n.closeRelay(session, "invalid frame")
		}
		return
	}
	if time.Now().After(session.Expires) {
		n.closeRelay(session, "expired")
		return
	}
	if time.Since(session.CheckedAt) > 5*time.Second {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		grants, err := n.accessClient.Grants(ctx)
		cancel()
		if err != nil || !grantStillActive(grants, session.Claims.ID, session.Claims.From, session.Claims.To, frame.Scope) {
			n.closeRelay(session, "revoked")
			return
		}
		session.CheckedAt = time.Now()
	}
	if session.Opener {
		var reply relayReply
		if json.Unmarshal(frame.Body, &reply) != nil || reply.ID == "" {
			return
		}
		n.mu.Lock()
		ch := n.relayWaiting[reply.ID]
		n.mu.Unlock()
		if ch != nil {
			select {
			case ch <- reply:
			default:
			}
		}
		return
	}
	var request relayRequest
	if json.Unmarshal(frame.Body, &request) != nil || !validOperationID(request.ID) || !validRelayAction(frame.Scope, request.Action) {
		n.sendRelayReply(session, frame.Scope, request.ID, nil, errors.New("invalid machine operation"))
		return
	}
	if frame.Scope == "computer" {
		own := n.computerLevel()
		if request.Level != "" && (computerRank(request.Level) < 0 || computerRank(request.Level) > computerRank(own)) {
			n.sendRelayReply(session, frame.Scope, request.ID, nil, errors.New("this computer does not allow that level"))
			return
		}
	}
	immediate, data, err := n.dispatchRelay(session, frame.Scope, request)
	if immediate {
		n.sendRelayReply(session, frame.Scope, request.ID, data, err)
	} else if err != nil {
		n.sendRelayReply(session, frame.Scope, request.ID, nil, err)
	}
}

// dispatchRelay runs one operation through the local feature that already
// serves this machine. immediate is false when the feature answers later
// through relayResult (a command ack, a job exit).
func (n *Agent) dispatchRelay(session *machineRelay, scope string, request relayRequest) (immediate bool, data any, err error) {
	if n.capabilityOff(relayCapability(scope, request.Action)) {
		return true, nil, errors.New("this capability is off on the target machine")
	}
	n.addActivity("Machine " + session.Peer + " used " + scope + " (grant " + session.Claims.ID + ")")
	switch scope {
	case "agent":
		if !n.opt.Config.AgentsTalk {
			return true, nil, errors.New("Let my agents talk is off on this machine")
		}
		err = n.receiveAgentTask(protocol.AgentTaskFrame{Task: request.Data})
		return true, map[string]any{"queued": err == nil}, err
	case "send":
		var offer struct {
			Envelope string `json:"envelope"`
		}
		if json.Unmarshal(request.Data, &offer) != nil || len(offer.Envelope) > 900000 {
			return true, nil, errors.New("invalid sealed item")
		}
		n.mu.Lock()
		lan := n.grantedSend
		n.mu.Unlock()
		if lan == nil {
			return true, nil, errors.New("Link Send is off on this machine")
		}
		id, err := lan.ReceiveGranted(session.Peer, offer.Envelope)
		return true, map[string]any{"id": id}, err
	case "worker":
		if request.Action == "cancel" || request.Action == "status" {
			var body struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(request.Data, &body) != nil || !protocol.ValidJobID(body.ID) {
				return true, nil, errors.New("invalid job id")
			}
			n.mu.Lock()
			owner := n.remoteJobs[body.ID]
			n.mu.Unlock()
			if owner != session.Peer {
				return true, nil, errors.New("job is not owned by this machine")
			}
			if request.Action == "status" {
				result, ok := n.jobs.answer(body.ID)
				if !ok {
					return true, nil, errors.New("job not found")
				}
				return true, result, nil
			}
			n.jobCancel(protocol.JobCancel{ID: body.ID})
			return true, map[string]any{"cancelled": true}, nil
		}
		var job protocol.Job
		if json.Unmarshal(request.Data, &job) != nil || !protocol.ValidJobID(job.ID) {
			return true, nil, errors.New("invalid job")
		}
		source := relaySource{to: session.Peer, scope: scope, relay: session.ID, bodyID: request.ID}
		if err := n.jobRelayed(protocol.JobRequest{Job: job}, source); err != nil {
			return true, nil, err
		}
		return false, nil, nil
	default:
		if !json.Valid(request.Data) {
			return true, nil, errors.New("invalid command data")
		}
		n.mu.Lock()
		n.relaySources[request.ID] = relaySource{to: session.Peer, scope: scope, relay: session.ID, bodyID: request.ID}
		n.mu.Unlock()
		n.command(protocol.Command{ID: request.ID, Kind: request.Action, Value: request.Data, Operator: &protocol.Operator{Subject: session.Peer, Role: "machine"}}, time.Now().UnixMilli(), 0, mono.Now())
		return false, nil, nil
	}
}

func (n *Agent) sendRelayReply(session *machineRelay, scope, bodyID string, data any, err error) {
	if n.link == nil || session == nil {
		return
	}
	reply := relayReply{ID: bodyID, OK: err == nil}
	if err != nil {
		reply.Error = err.Error()
	} else if data != nil {
		reply.Data, _ = json.Marshal(data)
	}
	body, marshalErr := json.Marshal(reply)
	if marshalErr != nil {
		return
	}
	_ = n.link.Send(protocol.RelayFrame{Relay: session.ID, Scope: scope, Body: body})
}

func (n *Agent) closeRelay(session *machineRelay, reason string) {
	if session == nil {
		return
	}
	n.mu.Lock()
	delete(n.relaySessions, session.ID)
	bodyID := session.WaitingID
	n.mu.Unlock()
	if bodyID != "" {
		n.mu.Lock()
		ch := n.relayWaiting[bodyID]
		n.mu.Unlock()
		if ch != nil {
			select {
			case ch <- relayReply{ID: bodyID, Error: "the relay closed: " + reason}:
			default:
			}
		}
	}
	if n.link != nil {
		_ = n.link.Send(protocol.RelayClose{Relay: session.ID})
	}
}

func (n *Agent) receiveRelayClosed(frame protocol.RelayClosed) {
	n.mu.Lock()
	session := n.relaySessions[frame.Relay]
	delete(n.relaySessions, frame.Relay)
	n.mu.Unlock()
	if session == nil {
		return
	}
	reason := frame.Reason
	if reason == "" {
		reason = "closed"
	}
	if session.WaitingID != "" {
		n.mu.Lock()
		ch := n.relayWaiting[session.WaitingID]
		n.mu.Unlock()
		if ch != nil {
			select {
			case ch <- relayReply{ID: session.WaitingID, Error: "the relay closed: " + reason}:
			default:
			}
		}
	}
	if session.Opener {
		n.mu.Lock()
		ch := n.relayOpening[session.Peer]
		n.mu.Unlock()
		if ch != nil {
			select {
			case ch <- relayOpened{Err: errors.New("the relay closed: " + reason)}:
			default:
			}
		}
	}
}

func (n *Agent) failRelayWaiters(detail string) {
	n.mu.Lock()
	openings := make([]chan relayOpened, 0, len(n.relayOpening))
	for key, ch := range n.relayOpening {
		openings = append(openings, ch)
		delete(n.relayOpening, key)
	}
	waiters := map[string]chan relayReply{}
	for id, ch := range n.relayWaiting {
		waiters[id] = ch
	}
	n.mu.Unlock()
	for _, ch := range openings {
		select {
		case ch <- relayOpened{Err: errors.New(detail)}:
		default:
		}
	}
	for id, ch := range waiters {
		select {
		case ch <- relayReply{ID: id, Error: detail}:
		default:
		}
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
		if _, refused := m.(protocol.Nack); refused {
			delete(n.remoteJobs, id)
		}
	}
	n.mu.Unlock()
	if source.to == "" {
		// A relayed worker's ack already closed its relay. Its later job_exit
		// must not go to Bot as a job frame for a job Bot never started.
		if _, exited := m.(protocol.JobExit); exited {
			n.mu.Lock()
			_, remote := n.remoteJobs[id]
			delete(n.remoteJobs, id)
			n.mu.Unlock()
			if remote {
				return true
			}
		}
		return false
	}
	reply := relayReply{ID: source.bodyID, OK: true}
	if nk, ok := m.(protocol.Nack); ok {
		reply.OK = false
		reply.Error = nk.Message
	} else {
		reply.Data, _ = json.Marshal(m)
	}
	body, err := json.Marshal(reply)
	if err != nil {
		return true
	}
	if n.link != nil {
		_ = n.link.Send(protocol.RelayFrame{Relay: source.relay, Scope: source.scope, Body: body})
	}
	return true
}

type localAgent struct{ ID, Owner, Name string }

func loadLocalAgents(dir string) map[string]localAgent {
	entries := map[string]localAgent{}
	raw, err := os.ReadFile(filepath.Join(dir, "agent-directory.json"))
	if err != nil || len(raw) > 64<<10 || json.Unmarshal(raw, &entries) != nil || entries == nil {
		return map[string]localAgent{}
	}
	for cli, entry := range entries {
		if cli == "" || !strings.HasPrefix(entry.ID, "agt_") || !strings.HasPrefix(entry.Owner, "usr_") {
			delete(entries, cli)
		}
	}
	return entries
}

func saveLocalAgents(dir string, entries map[string]localAgent) error {
	path := filepath.Join(dir, "agent-directory.json")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".agent-directory-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (n *Agent) localAgentEntries() []map[string]any {
	n.mu.Lock()
	defer n.mu.Unlock()
	list := make([]map[string]any, 0, len(n.localAgents))
	machine := ""
	if n.opt.Creds != nil {
		machine = n.opt.Creds.Principal
	}
	for cli, entry := range n.localAgents {
		name := entry.Name
		if name == "" {
			name = cli
		}
		list = append(list, map[string]any{"id": entry.ID, "owner": entry.Owner, "kind": "link-local",
			"name": name, "machine_id": machine, "status": "online", "allow_others": false})
	}
	sort.Slice(list, func(i, j int) bool { return fmt.Sprint(list[i]["name"]) < fmt.Sprint(list[j]["name"]) })
	return list
}

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
		n.mu.Lock()
		known := n.localAgents[cli.ID]
		n.mu.Unlock()
		if known.ID != "" {
			entry["id"] = known.ID
		}
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
		n.localAgents[cli.ID] = localAgent{ID: result.ID, Owner: result.Owner, Name: cli.Name}
		copy := make(map[string]localAgent, len(n.localAgents))
		for key, value := range n.localAgents {
			copy[key] = value
		}
		n.mu.Unlock()
		if err := saveLocalAgents(n.opt.Paths.StateDir, copy); err != nil {
			n.log.Warn("agent directory id not saved", "err", err)
		}
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
		args.Thread = access.NewThreadID()
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
		Task   struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"task"`
	}
	if json.Unmarshal(data, &created) != nil {
		return localctl.Response{Error: "Network did not return a task id"}
	}
	if created.ID == "" {
		created.ID = created.Task.ID
		created.Status = created.Task.Status
	}
	if created.ID == "" {
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
			Task   struct {
				Status string `json:"status"`
			} `json:"task"`
		}
		if json.Unmarshal(data, &result) != nil {
			return localctl.Response{Error: "Network returned an invalid task"}
		}
		if result.Status == "" {
			result.Status = result.Task.Status
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
	// Network currently returns asked_at/pending_until; link.access-request@1
	// names them requested_at/expires_at. Accept either so Link follows the
	// contract and the first Network build alike.
	AskedAt         string `json:"asked_at"`
	RequestedAt     string `json:"requested_at"`
	PendingUntil    string `json:"pending_until"`
	ExpiresAt       string `json:"expires_at"`
	DurationSeconds int64  `json:"duration_seconds"`
}

func (r incomingAccessRequest) askedAt() string {
	if r.AskedAt != "" {
		return r.AskedAt
	}
	return r.RequestedAt
}

func (r incomingAccessRequest) expiresAt() string {
	if r.PendingUntil != "" {
		return r.PendingUntil
	}
	return r.ExpiresAt
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
		if request.DurationSeconds%86400 == 0 {
			days := request.DurationSeconds / 86400
			duration = fmt.Sprintf("%d days", days)
			if days == 1 {
				duration = "1 day"
			}
		} else if request.DurationSeconds%3600 == 0 {
			hours := request.DurationSeconds / 3600
			duration = fmt.Sprintf("%d hours", hours)
			if hours == 1 {
				duration = "1 hour"
			}
		}
	}
	body := phrase + ", for " + duration + "."
	if request.DurationSeconds == 0 {
		body = phrase + ", until you take it back."
	}
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
		at, _ := time.Parse(time.RFC3339, request.askedAt())
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
