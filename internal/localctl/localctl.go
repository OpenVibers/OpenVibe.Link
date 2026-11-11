// Package localctl is the running Link's local control socket: `openvibe-link stop`, `resume`, `quit`, `status`, `plugins`,
// `approvals`, `approve`, `deny` and `tray` talk to it. It is a Unix domain socket (Windows 10+ supports them too) in the state directory with mode
// 0600, so only the account running the Link and root can use it. One JSON request per connection, one JSON answer.
package localctl

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"time"
)

// Commands.
const (
	CmdStatus = "status"
	CmdStop   = "stop"
	CmdResume = "resume"
	// CmdQuit ends the running Link cleanly: every actuator stops and nothing is latched (stop latches).
	CmdQuit    = "quit"
	CmdPlugins = "plugins"
	// The computer plugin's approvals (control level "ask"): list what waits, approve or deny one by its id.
	CmdApprovals = "approvals"
	CmdApprove   = "approve"
	CmdDeny      = "deny"
	// CmdTray: `openvibe-link tray` shows the approvals (On) or has quit; the computer plugin's own desktop prompts
	// stay quiet while it does.
	CmdTray = "tray"
	// CmdFirstRun carries the guided first run's telemetry events (Data) to the running Link, which sends them on the
	// device link: the installer starts the service before the guide runs, and a second device connection would
	// replace it.
	CmdFirstRun = "first_run"
	// CmdPanel answers where OpenVibe Link's own window is: its loopback URL with the per-run token in the fragment.
	// It goes to the tray (or `openvibe-link panel`), which runs as the Link's owner, never into a status line.
	CmdPanel     = "panel"
	CmdPartyHost = "party_host"
	CmdPartyStop = "party_stop"
	// CmdMCP runs one named MCP tool through the same local authorization path as the CLI.
	CmdMCP    = "mcp"
	CmdAccess = "access"
)

type Request struct {
	Cmd   string          `json:"cmd"`
	ID    string          `json:"id,omitempty"`    // approve and deny: the locally minted approval nonce (never the cloud command ID)
	Agent string          `json:"agent,omitempty"` // MCP: which local CLI launched the server
	On    bool            `json:"on,omitempty"`    // tray: the tray is running
	Data  json.RawMessage `json:"data,omitempty"`  // first_run: the telemetry events to report
}

type Response struct {
	OK    bool            `json:"ok"`
	Error string          `json:"error,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// Handler answers one request.
type Handler func(Request) Response

// Serve listens on path until ctx ends.
func Serve(ctx context.Context, path string, h Handler) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// A socket left by a crashed Link: remove it if nobody answers.
	if _, err := os.Stat(path); err == nil {
		if c, err := net.DialTimeout("unix", path, 200*time.Millisecond); err == nil {
			c.Close()
			return errors.New("another openvibe-link is already running (its control socket answers)")
		}
		_ = os.Remove(path)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	_ = os.Chmod(path, 0o600)
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	defer os.Remove(path)
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			continue
		}
		go func(c net.Conn) {
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(5 * time.Second))
			var req Request
			if err := json.NewDecoder(c).Decode(&req); err != nil {
				_ = json.NewEncoder(c).Encode(Response{Error: "bad request"})
				return
			}
			if req.Cmd == CmdMCP {
				// agent_ask may wait for a remote agent's reply. The socket is local and
				// the MCP server still caps that wait at 120 seconds.
				_ = c.SetDeadline(time.Now().Add(123 * time.Second))
			}
			_ = json.NewEncoder(c).Encode(h(req))
		}(c)
	}
}

// Call sends one request to a running Link.
func Call(path string, req Request, timeout time.Duration) (Response, error) {
	var resp Response
	c, err := net.DialTimeout("unix", path, timeout)
	if err != nil {
		return resp, err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(timeout))
	if err := json.NewEncoder(c).Encode(req); err != nil {
		return resp, err
	}
	if err := json.NewDecoder(c).Decode(&resp); err != nil {
		return resp, err
	}
	if resp.Error != "" {
		return resp, errors.New(resp.Error)
	}
	if !resp.OK {
		return resp, errors.New("local control request failed")
	}
	return resp, nil
}
