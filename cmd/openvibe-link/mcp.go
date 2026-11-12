package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/OpenVibers/OpenVibe.Link/internal/localctl"
	"github.com/OpenVibers/OpenVibe.Link/internal/mcp"
)

type socketMCP struct{ path, agent string }

func (s socketMCP) Call(name string, args json.RawMessage) (json.RawMessage, error) {
	timeout := 10 * time.Second
	if name == "agent_ask" || name == "machine_use" {
		timeout = 125 * time.Second
	}
	r, err := localctl.Call(s.path, localctl.Request{Cmd: localctl.CmdMCP, ID: name, Agent: s.agent, Data: args}, timeout)
	if err != nil {
		return nil, err
	}
	if len(r.Data) == 0 {
		return json.RawMessage(`{}`), nil
	}
	return r.Data, nil
}

func mcpCommand(g *globals, args []string, agent string, in io.Reader, out io.Writer) error {
	if len(args) == 0 {
		return mcp.Serve(in, out, socketMCP{path: g.paths.SocketPath(), agent: agent})
	}
	if len(args) == 2 && args[0] == "--print-config" {
		return printMCPConfig(g, args[1], out)
	}
	if len(args) == 2 && args[0] == "install" {
		return installMCP(g, args[1], in, out)
	}
	return errors.New("usage: openvibe-link mcp [--print-config claude|codex|muse|opencode | install claude|codex|muse|opencode]")
}

func mcpArgs(g *globals, cli string) (string, []string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", nil, err
	}
	local := map[string]string{"claude": "claude-code", "codex": "codex", "muse": "muse", "opencode": "opencode"}[cli]
	args := []string{"mcp", "--agent", local}
	if g.home != "" {
		args = append(args, "--home", g.home)
	}
	return exe, args, nil
}

func mcpSnippet(g *globals, cli string) (string, error) {
	exe, args, err := mcpArgs(g, cli)
	if err != nil {
		return "", err
	}
	quoted, _ := json.Marshal(exe)
	argv, _ := json.Marshal(args)
	switch cli {
	case "claude":
		return fmt.Sprintf(`{"mcpServers":{"openvibe-link":{"command":%s,"args":%s}}}`, quoted, argv), nil
	case "codex":
		var b strings.Builder
		fmt.Fprintf(&b, "[mcp_servers.openvibe-link]\ncommand = %s\nargs = %s\n", quoted, argv)
		return b.String(), nil
	case "muse":
		return fmt.Sprintf(`{"mcp_servers":{"openvibe-link":{"transport":"stdio","command":%s,"args":%s,"mode":"optional"}}}`, quoted, argv), nil
	case "opencode":
		command, _ := json.Marshal(append([]string{exe}, args...))
		return fmt.Sprintf(`{"mcp":{"servers":{"openvibe-link":{"type":"local","command":%s}}}}`, command), nil
	default:
		return "", fmt.Errorf("unknown CLI %q", cli)
	}
}

func printMCPConfig(g *globals, cli string, out io.Writer) error {
	s, err := mcpSnippet(g, cli)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, s)
	return err
}

// installMCP is an explicit offer: the person must answer yes before a CLI
// config is touched. For three CLIs, their own `mcp add` does the edit. Muse
// has no add command, so its JSON settings are patched with a safe file swap.
func installMCP(g *globals, cli string, in io.Reader, out io.Writer) error {
	if _, err := mcpSnippet(g, cli); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "Add OpenVibe Link's MCP tools to %s for this account? [y/N] ", cli); err != nil {
		return err
	}
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && err != io.EOF {
		return err
	}
	if strings.ToLower(strings.TrimSpace(line)) != "y" {
		_, _ = fmt.Fprintln(out, "Nothing changed.")
		return nil
	}
	exe, args, err := mcpArgs(g, cli)
	if err != nil {
		return err
	}
	var command *exec.Cmd
	switch cli {
	case "claude":
		command = exec.Command("claude", append([]string{"mcp", "add", "--transport", "stdio", "--scope", "user", "openvibe-link", "--", exe}, args...)...)
	case "codex":
		command = exec.Command("codex", append([]string{"mcp", "add", "openvibe-link", "--", exe}, args...)...)
	case "opencode":
		command = exec.Command("opencode", append([]string{"mcp", "add", "openvibe-link", "--", exe}, args...)...)
	case "muse":
		return installMuseMCP(exe, args, out)
	}
	command.Stdout, command.Stderr = out, out
	if err := command.Run(); err != nil {
		return fmt.Errorf("%s did not add the MCP server: %w", cli, err)
	}
	return nil
}

func installMuseMCP(exe string, args []string, out io.Writer) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Join(home, ".config", "muse", "settings.json")
	settings := map[string]json.RawMessage{}
	if raw, err := os.ReadFile(path); err == nil {
		if json.Unmarshal(raw, &settings) != nil || settings == nil {
			return errors.New("Muse settings are not a JSON object")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	servers := map[string]json.RawMessage{}
	if raw := settings["mcp_servers"]; len(raw) > 0 && (json.Unmarshal(raw, &servers) != nil || servers == nil) {
		return errors.New("Muse mcp_servers is not an object")
	}
	if _, exists := servers["openvibe-link"]; exists {
		return errors.New("Muse already has an openvibe-link MCP server")
	}
	entry, _ := json.Marshal(map[string]any{"transport": "stdio", "command": exe, "args": args, "mode": "optional"})
	servers["openvibe-link"] = entry
	settings["mcp_servers"], _ = json.Marshal(servers)
	updated, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".settings-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(updated, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, "Added OpenVibe Link to Muse settings.")
	return err
}
