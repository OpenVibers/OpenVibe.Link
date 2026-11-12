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
		if _, err = fmt.Fprintf(out, "Asked %s for: %s (%s). Waiting for their owner…\n", args[1], scopeWords(strings.Split(opt.Scope, ",")), accessTime(seconds)); err != nil {
			return err
		}
		id := responseID(value)
		if id == "" {
			return errors.New("Network did not return the request id")
		}
		return waitAccessAnswer(g, id, strings.Split(opt.Scope, ","), out)
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
	for i := 1; i < len(parts); i++ {
		parts[i] = strings.ToLower(parts[i][:1]) + parts[i][1:]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}

func accessTime(seconds int64) string {
	if seconds == 0 {
		return "until you take it back"
	}
	if seconds%86400 == 0 {
		if seconds == 86400 {
			return "1 day"
		}
		return fmt.Sprintf("%d days", seconds/86400)
	}
	if seconds%3600 == 0 {
		if seconds == 3600 {
			return "1 hour"
		}
		return fmt.Sprintf("%d hours", seconds/3600)
	}
	if seconds%60 == 0 {
		if seconds == 60 {
			return "1 minute"
		}
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
	fromName, _ := row["from_machine_name"].(string)
	fromOwner, _ := row["from_owner_name"].(string)
	toName, _ := row["to_machine_name"].(string)
	if fromName != "" {
		from = fromName
		if fromOwner != "" {
			from = fromOwner + "'s " + fromName
		}
	}
	if toName != "" {
		to = toName
	}
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
		when = " · " + accessLeft(expiry)
	} else if pending, _ := row["pending_until"].(string); pending != "" {
		when = " · waiting"
	} else if value, ok := row["expires_at"]; ok && value == nil {
		when = " · until you take it back"
	}
	return name + " · " + scopeWords(scopes) + when + " · " + id
}

func responseID(value any) string {
	root, _ := value.(map[string]any)
	for _, key := range []string{"id", "request_id", "grant_id"} {
		if id, _ := root[key].(string); id != "" {
			return id
		}
	}
	for _, key := range []string{"request", "grant"} {
		if row, _ := root[key].(map[string]any); row != nil {
			for _, idKey := range []string{"id", "request_id", "grant_id"} {
				if id, _ := row[idKey].(string); id != "" {
					return id
				}
			}
		}
	}
	return ""
}

type accessListRow struct {
	ID          string   `json:"id"`
	RequestID   string   `json:"request_id"`
	Status      string   `json:"status"`
	Scopes      []string `json:"scopes"`
	ExpiresAt   any      `json:"expires_at"`
	PendingTill string   `json:"pending_until"`
	RequestedAt string   `json:"requested_at"`
	AskedAt     string   `json:"asked_at"`
}

type accessListAnswer struct {
	Requests struct {
		Requests []accessListRow `json:"requests"`
	} `json:"requests"`
	Grants struct {
		Grants []accessListRow `json:"grants"`
	} `json:"grants"`
}

// waitAccessAnswer follows one request until its owner answers it, as the
// design's `access request` output promises. A person may take up to ten
// minutes; a script can pass --json to return at once.
func waitAccessAnswer(g *globals, id string, scopes []string, out io.Writer) error {
	deadline := time.Now().Add(10 * time.Minute)
	for {
		raw, err := localctl.Call(g.paths.SocketPath(), localctl.Request{Cmd: localctl.CmdAccess, ID: "list"}, 20*time.Second)
		if err != nil {
			return err
		}
		var answer accessListAnswer
		if len(raw.Data) > 0 && json.Unmarshal(raw.Data, &answer) == nil {
			for _, request := range answer.Requests.Requests {
				if request.ID != id {
					continue
				}
				switch request.Status {
				case "pending":
				case "denied":
					_, err = fmt.Fprintln(out, "Denied.")
					return err
				case "expired":
					_, err = fmt.Fprintln(out, "No answer in 10 minutes; ask again later.")
					return err
				case "approved":
					// The grant below carries the answer's scopes and time.
				}
			}
			for _, grant := range answer.Grants.Grants {
				if grant.RequestID != id {
					continue
				}
				if grant.Scopes != nil && !sameScopes(grant.Scopes, scopes) {
					_, err = fmt.Fprintf(out, "Allowed, narrowed to: %s.\n", scopeWords(grant.Scopes))
					return err
				}
				switch expiry := grant.ExpiresAt.(type) {
				case nil:
					_, err = fmt.Fprintln(out, "Allowed until you take it back.")
				case string:
					at, parseErr := time.Parse(time.RFC3339, expiry)
					if parseErr != nil {
						_, err = fmt.Fprintln(out, "Allowed.")
					} else {
						_, err = fmt.Fprintf(out, "Allowed until %s.\n", at.Local().Format("15:04"))
					}
				default:
					_, err = fmt.Fprintln(out, "Allowed.")
				}
				return err
			}
		}
		if !time.Now().Before(deadline) {
			_, err = fmt.Fprintln(out, "No answer in 10 minutes; ask again later.")
			return err
		}
		time.Sleep(2 * time.Second)
	}
}

func sameScopes(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]bool{}
	for _, s := range a {
		seen[s] = true
	}
	for _, s := range b {
		if !seen[s] {
			return false
		}
	}
	return true
}

func accessLeft(expiry string) string {
	at, err := time.Parse(time.RFC3339, expiry)
	if err != nil {
		return "until " + expiry
	}
	left := time.Until(at)
	if left <= 0 {
		return "ended"
	}
	if left < time.Minute {
		left = time.Minute
	}
	left = left.Round(time.Minute)
	days := int(left / (24 * time.Hour))
	hours := int((left % (24 * time.Hour)) / time.Hour)
	minutes := int((left % time.Hour) / time.Minute)
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh left", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm left", hours, minutes)
	default:
		return fmt.Sprintf("%dm left", minutes)
	}
}

func validScope(s string) bool {
	switch s {
	case "agent", "send", "screen", "desk", "computer", "worker":
		return true
	}
	return false
}
