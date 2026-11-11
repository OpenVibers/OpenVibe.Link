package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/OpenVibers/OpenVibe.Link/internal/localctl"
)

type accessArgs struct {
	Scope, For, Reason string
	JSON               bool
}

func accessDuration(raw string) (int64, error) {
	if raw == "" {
		raw = "1h"
	}
	if raw == "forever" {
		return 0, nil
	}
	if strings.HasSuffix(raw, "d") {
		var d int64
		if _, err := fmt.Sscanf(raw, "%dd", &d); err == nil && d >= 1 && d <= 30 {
			return d * 86400, nil
		}
		return 0, errors.New("--for must be 1m through 30d, or forever for same-owner agent/send")
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < time.Minute || d > 30*24*time.Hour {
		return 0, errors.New("--for must be 1m through 30d, or forever for same-owner agent/send")
	}
	return int64(d.Seconds()), nil
}

func cmdAccess(g *globals, args []string, opt accessArgs, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: openvibe-link access request|list|approve|deny|revoke")
	}
	data := map[string]any{}
	action := args[0]
	switch action {
	case "request":
		if len(args) != 2 {
			return errors.New("usage: openvibe-link access request <machine> --scope agent,send [--for 1h] [--reason text]")
		}
		if opt.Scope == "" {
			return errors.New("--scope is required")
		}
		scopes := strings.Split(opt.Scope, ",")
		seen := map[string]bool{}
		for _, scope := range scopes {
			if !validScope(scope) || seen[scope] {
				return errors.New("--scope must list distinct agent,send,screen,desk,computer,worker")
			}
			seen[scope] = true
		}
		seconds, err := accessDuration(opt.For)
		if err != nil {
			return err
		}
		if seconds == 0 && (len(scopes) > 2 || !seen["agent"] && !seen["send"] || seen["screen"] || seen["desk"] || seen["computer"] || seen["worker"]) {
			return errors.New("forever is only for agent and send")
		}
		if len(opt.Reason) > 500 {
			return errors.New("reason is too long")
		}
		data = map[string]any{"machine": args[1], "scopes": scopes, "duration_seconds": seconds, "reason": opt.Reason}
	case "list":
		if len(args) != 1 {
			return errors.New("usage: openvibe-link access list")
		}
	case "approve", "deny", "revoke":
		if len(args) != 2 {
			return fmt.Errorf("usage: openvibe-link access %s <id>", action)
		}
		data["id"] = args[1]
		if action == "approve" {
			if opt.Scope != "" {
				scopes := strings.Split(opt.Scope, ",")
				for _, s := range scopes {
					if !validScope(s) {
						return errors.New("invalid scope")
					}
				}
				data["scopes"] = scopes
			}
			if opt.For != "" {
				seconds, err := accessDuration(opt.For)
				if err != nil {
					return err
				}
				if seconds == 0 {
					return errors.New("an answer may only shorten the requested time")
				}
				data["duration_seconds"] = seconds
			}
		}
	default:
		return errors.New("usage: openvibe-link access request|list|approve|deny|revoke")
	}
	raw, _ := json.Marshal(data)
	resp, err := localctl.Call(g.paths.SocketPath(), localctl.Request{Cmd: localctl.CmdAccess, ID: action, Data: raw}, 20*time.Second)
	if err != nil {
		return err
	}
	var value any
	if len(resp.Data) > 0 {
		if err := json.Unmarshal(resp.Data, &value); err != nil {
			return err
		}
	}
	if opt.JSON {
		b, _ := json.MarshalIndent(value, "", "  ")
		_, err = fmt.Fprintln(out, string(b))
		return err
	}
	if action == "list" {
		return printAccessList(out, value)
	}
	if action == "request" {
		seconds, _ := accessDuration(opt.For)
		_, err = fmt.Fprintf(out, "Asked %s for: %s (%s). Waiting for their owner…\n", args[1], scopeWords(strings.Split(opt.Scope, ",")), accessTime(seconds))
		return err
	}
	if action == "approve" {
		_, err = fmt.Fprintln(out, "Allowed.")
		return err
	}
	if action == "deny" {
		_, err = fmt.Fprintln(out, "Denied.")
		return err
	}
	if action == "revoke" {
		_, err = fmt.Fprintln(out, "Access ended.")
		return err
	}
	label := map[string]string{"request": "Access requested", "approve": "Access approved", "deny": "Access denied", "revoke": "Access revoked"}[action]
	if m, ok := value.(map[string]any); ok {
		for _, key := range []string{"id", "request_id", "grant_id"} {
			if id, _ := m[key].(string); id != "" {
				label += ": " + id
				break
			}
		}
		if status, _ := m["status"].(string); status != "" {
			label += " (" + status + ")"
		}
	}
	_, err = fmt.Fprintln(out, label)
	return err
}

func scopeWords(scopes []string) string {
	words := map[string]string{"agent": "Message your agents", "send": "Send you files", "screen": "Watch the screen", "desk": "Use a private desk", "computer": "Control this computer", "worker": "Run jobs in the sandbox"}
	var parts []string
	for _, s := range scopes {
		if w := words[s]; w != "" {
			parts = append(parts, w)
		}
	}
	if len(parts) == 0 {
		return "No access"
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}

func accessTime(seconds int64) string {
	if seconds == 0 {
		return "until you take it back"
	}
	if seconds%86400 == 0 {
		return fmt.Sprintf("%d days", seconds/86400)
	}
	if seconds%3600 == 0 {
		if seconds == 3600 {
			return "1 hour"
		}
		return fmt.Sprintf("%d hours", seconds/3600)
	}
	if seconds%60 == 0 {
		return fmt.Sprintf("%d minutes", seconds/60)
	}
	return (time.Duration(seconds) * time.Second).String()
}

func printAccessList(out io.Writer, value any) error {
	root, _ := value.(map[string]any)
	groups := map[string][]string{"Waiting": {}, "Allowed": {}, "Ended": {}}
	if reqs, _ := root["requests"].(map[string]any); reqs != nil {
		if rows, _ := reqs["requests"].([]any); rows != nil {
			for _, raw := range rows {
				row, _ := raw.(map[string]any)
				if row == nil {
					continue
				}
				status, _ := row["status"].(string)
				group := "Ended"
				if status == "pending" {
					group = "Waiting"
				} else if status == "approved" {
					continue
				}
				groups[group] = append(groups[group], formatAccessRow(row))
			}
		}
	}
	if grants, _ := root["grants"].(map[string]any); grants != nil {
		if rows, _ := grants["grants"].([]any); rows != nil {
			for _, raw := range rows {
				row, _ := raw.(map[string]any)
				if row == nil {
					continue
				}
				group := "Allowed"
				if row["revoked_at"] != nil {
					group = "Ended"
				} else if expiry, _ := row["expires_at"].(string); expiry != "" {
					at, err := time.Parse(time.RFC3339, expiry)
					if err != nil || !at.After(time.Now()) {
						group = "Ended"
					}
				}
				groups[group] = append(groups[group], formatAccessRow(row))
			}
		}
	}
	for _, group := range []string{"Waiting", "Allowed", "Ended"} {
		if _, err := fmt.Fprintln(out, group); err != nil {
			return err
		}
		if len(groups[group]) == 0 {
			if _, err := fmt.Fprintln(out, "  None"); err != nil {
				return err
			}
			continue
		}
		for _, row := range groups[group] {
			if _, err := fmt.Fprintln(out, "  "+row); err != nil {
				return err
			}
		}
	}
	return nil
}

func formatAccessRow(row map[string]any) string {
	from, _ := row["from_principal"].(string)
	to, _ := row["to_principal"].(string)
	name := from + " → " + to
	var scopes []string
	if list, _ := row["scopes"].([]any); list != nil {
		for _, raw := range list {
			if s, _ := raw.(string); s != "" {
				scopes = append(scopes, s)
			}
		}
	}
	id, _ := row["id"].(string)
	when := ""
	if expiry, _ := row["expires_at"].(string); expiry != "" {
		when = " · until " + expiry
	}
	return name + " · " + scopeWords(scopes) + when + " · " + id
}

func validScope(s string) bool {
	switch s {
	case "agent", "send", "screen", "desk", "computer", "worker":
		return true
	}
	return false
}
