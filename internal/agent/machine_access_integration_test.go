package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/OpenVibers/OpenVibe.Link/internal/config"
	"github.com/OpenVibers/OpenVibe.Link/internal/fakebot"
	"github.com/OpenVibers/OpenVibe.Link/internal/fakenetwork"
	"github.com/OpenVibers/OpenVibe.Link/internal/link"
	"github.com/OpenVibers/OpenVibe.Link/internal/localctl"
	"github.com/OpenVibers/OpenVibe.Link/internal/panel"
	"github.com/OpenVibers/OpenVibe.Link/internal/protocol"
)

func TestTwoLinksRequestApproveUseRevoke(t *testing.T) {
	bot := fakebot.New()
	t.Cleanup(bot.Close)
	network := fakenetwork.New(bot)
	t.Cleanup(network.Close)
	network.AddCode("ACCESS11")
	const other = "lnk_01J8Z4M2Q0R7T9YV3K6N8P1W2Z"
	network.AddMachine("ACCESS22", other, "dev_net02")
	start := func(code string) (*Agent, config.Paths, context.CancelFunc) {
		t.Helper()
		creds, err := link.PairNetwork(context.Background(), nil, network.URL(), protocol.LinkPairingRequest{Code: code}, "test")
		if err != nil {
			t.Fatal(err)
		}
		creds.Server = bot.URL()
		creds, err = link.Bind(context.Background(), nil, creds, link.NewTokenSource(creds, nil, "test"), "test")
		if err != nil {
			t.Fatal(err)
		}
		home, err := os.MkdirTemp("", "ova")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(home) })
		paths := config.HomePaths(home)
		cfg := config.Default()
		cfg.Plugins = nil
		cfg.AgentsTalk = true
		cfg.Video.Source = "off"
		n, err := New(Options{Config: cfg, Paths: paths, Creds: creds, Version: "test", NoShares: true, HeartbeatInterval: 100 * time.Millisecond, LinkBackoffMin: 50 * time.Millisecond,
			Log: slog.New(slog.NewTextHandler(io.Discard, nil)), CodesAgents: func() []panel.CodesAgent { return []panel.CodesAgent{{ID: "codex", Name: "Codex", State: "ready"}} }})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		go func() { _ = n.Run(ctx) }()
		t.Cleanup(cancel)
		if _, err := bot.NextConn(5 * time.Second); err != nil {
			t.Fatal(err)
		}
		return n, paths, cancel
	}
	a, ap, _ := start("ACCESS11")
	b, bp, _ := start("ACCESS22")
	a.registerLocalAgentsOnce(context.Background())
	b.registerLocalAgentsOnce(context.Background())
	call := func(paths config.Paths, req localctl.Request) (json.RawMessage, error) {
		t.Helper()
		r, err := localctl.Call(paths.SocketPath(), req, 10*time.Second)
		return r.Data, err
	}
	request, err := call(ap, localctl.Request{Cmd: localctl.CmdAccess, ID: "request", Data: json.RawMessage(`{"machine":"` + other + `","scopes":["send"],"duration_seconds":3600,"reason":"test two links"}`)})
	if err != nil {
		t.Fatal(err)
	}
	var requested struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(request, &requested) != nil || requested.ID == "" {
		t.Fatalf("request: %s", request)
	}
	b.refreshAccessRequests(context.Background())
	var approval Approval
	for _, item := range b.Approvals() {
		if item.Plugin == "machine-access" && item.CommandID == requested.ID {
			approval = item
			break
		}
	}
	if approval.ID == "" {
		t.Fatal("target Link did not show tray approval")
	}
	if approval.Title != "Alex's laptop asks for access" || approval.Summary != `Send you files, for 1 hour. "test two links"` {
		t.Fatalf("tray words: %q / %q", approval.Title, approval.Summary)
	}
	if _, err = call(bp, localctl.Request{Cmd: localctl.CmdApprove, ID: approval.ID}); err != nil {
		t.Fatal(err)
	}
	use := json.RawMessage(`{"machine":"` + other + `","scope":"send","action":"offer","data":{"envelope":"sealed-test-item"}}`)
	result, err := call(ap, localctl.Request{Cmd: localctl.CmdMCP, ID: "machine_use", Data: use})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result), `"scope":"send"`) {
		t.Fatalf("machine_use: %s", result)
	}
	b.mu.Lock()
	lan := b.grantedSend
	b.mu.Unlock()
	if lan == nil || len(lan.Items()) != 1 || lan.Items()[0].Sender != fakenetwork.Principal {
		t.Fatal("sealed item did not reach the target inbox")
	}
	grants, err := a.accessClient.Grants(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Grants []struct {
			ID string `json:"id"`
		} `json:"grants"`
	}
	if json.Unmarshal(grants, &list) != nil || len(list.Grants) != 1 {
		t.Fatal("grant missing")
	}
	if _, err = call(bp, localctl.Request{Cmd: localctl.CmdAccess, ID: "revoke", Data: json.RawMessage(`{"id":"` + list.Grants[0].ID + `"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err = call(ap, localctl.Request{Cmd: localctl.CmdMCP, ID: "machine_use", Data: use}); err == nil || !strings.Contains(err.Error(), "no active grant") {
		t.Fatalf("revoke did not stop use: %v", err)
	}
	if listed, listErr := call(ap, localctl.Request{Cmd: localctl.CmdMCP, ID: "agents_list", Agent: "codex", Data: json.RawMessage(`{}`)}); listErr != nil || !strings.Contains(string(listed), "Codex") {
		t.Fatalf("agents_list: %s %v", listed, listErr)
	}
	if listed, listErr := call(ap, localctl.Request{Cmd: localctl.CmdMCP, ID: "machines_list", Agent: "codex", Data: json.RawMessage(`{}`)}); listErr != nil || !strings.Contains(string(listed), other) {
		t.Fatalf("machines_list: %s %v", listed, listErr)
	}
	b.mu.Lock()
	remoteAgent := b.localAgents["codex"].ID
	b.mu.Unlock()
	if remoteAgent == "" {
		t.Fatal("local CLI did not register")
	}
	_, err = call(ap, localctl.Request{Cmd: localctl.CmdMCP, ID: "agent_tell", Agent: "codex", Data: json.RawMessage(`{"agent":"` + remoteAgent + `","text":"hello"}`)})
	if err != nil {
		t.Fatal(err)
	}
	var inbox json.RawMessage
	for until := time.Now().Add(2 * time.Second); time.Now().Before(until); time.Sleep(20 * time.Millisecond) {
		inbox, err = call(bp, localctl.Request{Cmd: localctl.CmdMCP, ID: "agent_inbox", Agent: "codex", Data: json.RawMessage(`{}`)})
		if err == nil && strings.Contains(string(inbox), "hello") {
			break
		}
	}
	if !strings.Contains(string(inbox), "hello") {
		t.Fatalf("agent task not in inbox: %s %v", inbox, err)
	}
	var found struct {
		Tasks []struct {
			Task struct {
				ID string `json:"id"`
			} `json:"task"`
		} `json:"tasks"`
	}
	if json.Unmarshal(inbox, &found) != nil || len(found.Tasks) != 1 {
		t.Fatalf("inbox: %s", inbox)
	}
	if _, err = call(bp, localctl.Request{Cmd: localctl.CmdMCP, ID: "agent_reply", Agent: "codex", Data: json.RawMessage(`{"task":"` + found.Tasks[0].Task.ID + `","text":"received"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err = call(bp, localctl.Request{Cmd: localctl.CmdMCP, ID: "agent_inbox", Agent: "unregistered", Data: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("unregistered CLI read inbox")
	}
	askDone := make(chan error, 1)
	go func() {
		answer, askErr := call(ap, localctl.Request{Cmd: localctl.CmdMCP, ID: "agent_ask", Agent: "codex", Data: json.RawMessage(`{"agent":"` + remoteAgent + `","text":"status?","wait_seconds":4}`)})
		if askErr == nil && !strings.Contains(string(answer), "all clear") {
			askErr = errors.New("agent_ask did not return the reply")
		}
		askDone <- askErr
	}()
	var nextID string
	for until := time.Now().Add(3 * time.Second); time.Now().Before(until); time.Sleep(20 * time.Millisecond) {
		raw, readErr := call(bp, localctl.Request{Cmd: localctl.CmdMCP, ID: "agent_inbox", Agent: "codex", Data: json.RawMessage(`{}`)})
		if readErr != nil {
			t.Fatal(readErr)
		}
		var entries struct {
			Tasks []struct {
				Status string `json:"status"`
				Task   struct {
					ID string `json:"id"`
				} `json:"task"`
			} `json:"tasks"`
		}
		if json.Unmarshal(raw, &entries) != nil {
			t.Fatal("invalid inbox")
		}
		for _, entry := range entries.Tasks {
			if entry.Status == "pending" {
				nextID = entry.Task.ID
			}
		}
		if nextID != "" {
			break
		}
	}
	if nextID == "" {
		t.Fatal("agent_ask task did not arrive")
	}
	if _, err = call(bp, localctl.Request{Cmd: localctl.CmdMCP, ID: "agent_reply", Agent: "codex", Data: json.RawMessage(`{"task":"` + nextID + `","text":"all clear"}`)}); err != nil {
		t.Fatal(err)
	}
	if err = <-askDone; err != nil {
		t.Fatal(err)
	}
	requestedByAgent, err := call(ap, localctl.Request{Cmd: localctl.CmdMCP, ID: "machine_request_access", Agent: "codex", Data: json.RawMessage(`{"machine":"` + other + `","scopes":["worker"],"for_seconds":3600}`)})
	if err != nil || !strings.Contains(string(requestedByAgent), `"from_agent"`) {
		t.Fatalf("machine_request_access: %s %v", requestedByAgent, err)
	}
	t.Log("request -> target tray approval -> granted send use -> revoke -> use refused")
	t.Log("agents_list -> agent_tell -> agent_inbox -> agent_reply -> agent_ask; machine_request_access; unregistered inbox refused")
}
