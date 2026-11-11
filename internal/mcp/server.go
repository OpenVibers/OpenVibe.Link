// Package mcp serves Link's local machine and agent tools over MCP stdio.
package mcp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const Version = "2025-11-25"

type Caller interface {
	Call(name string, arguments json.RawMessage) (json.RawMessage, error)
}

type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func schema(properties map[string]any, required ...string) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}

var textField = map[string]any{"type": "string", "minLength": 1, "maxLength": 8000}
var idField = map[string]any{"type": "string", "minLength": 1, "maxLength": 128}
var scopeField = map[string]any{"type": "string", "enum": []string{"agent", "send", "screen", "desk", "computer", "worker"}}

var Tools = []Tool{
	{"agents_list", "List agents available to this person and their online status.", schema(map[string]any{})},
	{"agent_ask", "Ask an agent and wait for its answer for up to 120 seconds.", schema(map[string]any{"agent": idField, "text": textField, "wait_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": 120}}, "agent", "text")},
	{"agent_tell", "Send a message to an agent without waiting for an answer.", schema(map[string]any{"agent": idField, "text": textField, "thread": idField}, "agent", "text")},
	{"agent_inbox", "Read tasks addressed to this local agent.", schema(map[string]any{"agent": idField})},
	{"agent_reply", "Reply to a task in the local agent inbox.", schema(map[string]any{"task": idField, "text": textField}, "task", "text")},
	{"machines_list", "List machines and grants available to this machine.", schema(map[string]any{})},
	{"machine_request_access", "Ask another machine's owner for limited access.", schema(map[string]any{"machine": idField, "scopes": map[string]any{"type": "array", "minItems": 1, "items": scopeField}, "for_seconds": map[string]any{"type": "integer", "minimum": 60, "maximum": 2592000}, "reason": map[string]any{"type": "string", "maxLength": 500}}, "machine", "scopes")},
	{"machine_use", "Use a held grant for one operation on another machine.", schema(map[string]any{"machine": idField, "scope": scopeField, "action": idField, "data": map[string]any{"type": "object"}}, "machine", "scope", "action")},
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func answer(id json.RawMessage, value any, errCode int, errText string) any {
	if errCode != 0 {
		return map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": errCode, "message": errText}}
	}
	return map[string]any{"jsonrpc": "2.0", "id": id, "result": value}
}

// Serve reads one JSON-RPC message per line. Stdout is only the protocol.
func Serve(in io.Reader, out io.Writer, caller Caller) error {
	scan := bufio.NewScanner(in)
	scan.Buffer(make([]byte, 4096), 1<<20)
	enc := json.NewEncoder(out)
	for scan.Scan() {
		var req request
		if json.Unmarshal(scan.Bytes(), &req) != nil || req.JSONRPC != "2.0" {
			if err := enc.Encode(answer(json.RawMessage("null"), nil, -32700, "invalid JSON-RPC message")); err != nil {
				return err
			}
			continue
		}
		if len(req.ID) == 0 {
			continue
		} // notifications need no response
		var result any
		var code int
		var msg string
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": Version, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "openvibe-link", "version": "1"}}
		case "ping":
			result = map[string]any{}
		case "tools/list":
			result = map[string]any{"tools": Tools}
		case "tools/call":
			var p struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if json.Unmarshal(req.Params, &p) != nil {
				code, msg = -32602, "invalid tool parameters"
				break
			}
			if err := validate(p.Name, p.Arguments); err != nil {
				code, msg = -32602, err.Error()
				break
			}
			if caller == nil {
				code, msg = -32603, "local Link unavailable"
				break
			}
			data, err := caller.Call(p.Name, p.Arguments)
			if err != nil {
				result = map[string]any{"content": []any{map[string]any{"type": "text", "text": err.Error()}}, "isError": true}
			} else {
				result = map[string]any{"content": []any{map[string]any{"type": "text", "text": string(data)}}, "structuredContent": map[string]any{"data": json.RawMessage(data)}}
			}
		default:
			code, msg = -32601, "method not found"
		}
		if err := enc.Encode(answer(req.ID, result, code, msg)); err != nil {
			return err
		}
	}
	return scan.Err()
}

func validate(name string, raw json.RawMessage) error {
	var tool *Tool
	for i := range Tools {
		if Tools[i].Name == name {
			tool = &Tools[i]
			break
		}
	}
	if tool == nil {
		return errors.New("unknown tool")
	}
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	var args map[string]json.RawMessage
	if json.Unmarshal(raw, &args) != nil || args == nil {
		return errors.New("arguments must be an object")
	}
	properties := tool.InputSchema["properties"].(map[string]any)
	for _, required := range tool.InputSchema["required"].([]string) {
		if _, ok := args[required]; !ok {
			return fmt.Errorf("missing %s", required)
		}
	}
	for key, value := range args {
		property, ok := properties[key]
		if !ok {
			return fmt.Errorf("unknown argument %s", key)
		}
		definition := property.(map[string]any)
		switch definition["type"] {
		case "string":
			var s string
			if json.Unmarshal(value, &s) != nil {
				return fmt.Errorf("%s must be text", key)
			}
			if min, ok := definition["minLength"].(int); ok && len(strings.TrimSpace(s)) < min {
				return fmt.Errorf("%s is empty", key)
			}
			if max, ok := definition["maxLength"].(int); ok && len(s) > max {
				return fmt.Errorf("%s is too long", key)
			}
			if allowed, ok := definition["enum"].([]string); ok {
				found := false
				for _, v := range allowed {
					found = found || s == v
				}
				if !found {
					return fmt.Errorf("invalid %s", key)
				}
			}
		case "integer":
			var n int64
			if json.Unmarshal(value, &n) != nil {
				return fmt.Errorf("%s must be an integer", key)
			}
			if min, ok := definition["minimum"].(int); ok && n < int64(min) {
				return fmt.Errorf("%s is too small", key)
			}
			if max, ok := definition["maximum"].(int); ok && n > int64(max) {
				return fmt.Errorf("%s is too large", key)
			}
		case "array":
			var a []string
			if json.Unmarshal(value, &a) != nil {
				return fmt.Errorf("%s must be a list", key)
			}
			if len(a) == 0 {
				return fmt.Errorf("%s is empty", key)
			}
			seen := map[string]bool{}
			for _, s := range a {
				if !scoped(s) || seen[s] {
					return fmt.Errorf("invalid %s", key)
				}
				seen[s] = true
			}
		case "object":
			var object map[string]any
			if json.Unmarshal(value, &object) != nil || object == nil {
				return fmt.Errorf("%s must be an object", key)
			}
		}
	}
	return nil
}

func scoped(s string) bool {
	for _, v := range []string{"agent", "send", "screen", "desk", "computer", "worker"} {
		if s == v {
			return true
		}
	}
	return false
}
