package access

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/OpenVibers/OpenVibe.Link/internal/credentials"
)

var routeID = regexp.MustCompile(`^[a-z]{3}_[A-Za-z0-9_-]{8,80}$`)

// Client uses this machine's Network node token for self access and agent
// routes. Network still decides who may request, answer, revoke and relay.
type Client struct {
	Origin string
	Tokens Tokens
	HTTP   *http.Client
}

type Tokens interface {
	Token(context.Context) (credentials.Secret, error)
	Invalidate()
}

func (c *Client) do(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	var none json.RawMessage
	u, err := url.Parse(c.Origin)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1"))) {
		return none, errors.New("machine access requires a Network HTTPS origin")
	}
	if c.Tokens == nil {
		return none, errors.New("machine is not paired through Network")
	}
	var content io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return none, err
		}
		content = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.Origin, "/")+path, content)
	if err != nil {
		return none, err
	}
	token, err := c.Tokens.Token(ctx)
	if err != nil {
		return none, err
	}
	req.Header.Set("Authorization", "Bearer "+token.Reveal())
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return none, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return none, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var problem struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(raw, &problem)
		if len(problem.Code) > 100 || strings.ContainsAny(problem.Code, " \n\r\t") {
			problem.Code = ""
		}
		return none, fmt.Errorf("Network refused machine access: HTTP %d %s", resp.StatusCode, problem.Code)
	}
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	if !json.Valid(raw) {
		return none, errors.New("Network returned invalid JSON")
	}
	return raw, nil
}

func (c *Client) Request(ctx context.Context, machine string, scopes []string, seconds int64, reason, agent string) (json.RawMessage, error) {
	if machine == "" || len(machine) > 160 || len(scopes) == 0 || seconds < 0 || seconds > 30*86400 || len(reason) > 500 {
		return nil, errors.New("invalid access request")
	}
	for _, s := range scopes {
		if !ValidScope(s) {
			return nil, errors.New("invalid access scope")
		}
		if seconds == 0 && s != "agent" && s != "send" {
			return nil, errors.New("forever is only for agent and send")
		}
	}
	if !strings.HasPrefix(machine, "lnk_") && !strings.HasPrefix(machine, "@") {
		known, err := c.Machines(ctx)
		if err != nil {
			return nil, err
		}
		var list struct {
			Machines []struct{ ID, Name string } `json:"machines"`
		}
		if json.Unmarshal(known, &list) != nil {
			return nil, errors.New("Network returned an invalid machine list")
		}
		found := ""
		for _, row := range list.Machines {
			if strings.EqualFold(row.Name, machine) {
				if found != "" {
					return nil, errors.New("machine name is ambiguous; use its id")
				}
				found = row.ID
			}
		}
		if found == "" {
			return nil, errors.New("machine name not found")
		}
		machine = found
	}
	var duration any = seconds
	if seconds == 0 {
		duration = nil
	}
	return c.do(ctx, http.MethodPost, "/api/v1/link/self/access-requests", map[string]any{"target": machine, "scopes": scopes, "duration_seconds": duration, "reason": reason, "from_agent": agent})
}

func ValidScope(s string) bool { return scopes[s] }

func (c *Client) List(ctx context.Context) (json.RawMessage, error) {
	return c.do(ctx, http.MethodGet, "/api/v1/link/self/access-requests", nil)
}
func (c *Client) Grants(ctx context.Context) (json.RawMessage, error) {
	return c.do(ctx, http.MethodGet, "/api/v1/link/self/access-grants", nil)
}
func (c *Client) GrantToken(ctx context.Context, id string) (json.RawMessage, error) {
	if !routeID.MatchString(id) {
		return nil, errors.New("invalid grant id")
	}
	return c.do(ctx, http.MethodPost, "/api/v1/link/self/access-grants/"+id+"/token", map[string]any{})
}
func (c *Client) Machines(ctx context.Context) (json.RawMessage, error) {
	return c.do(ctx, http.MethodGet, "/api/v1/link/self/send/targets", nil)
}
func (c *Client) Answer(ctx context.Context, id string, approve bool, scopes []string, seconds int64) (json.RawMessage, error) {
	if !routeID.MatchString(id) {
		return nil, errors.New("invalid request id")
	}
	body := map[string]any{"approve": approve}
	if len(scopes) > 0 {
		for _, s := range scopes {
			if !ValidScope(s) {
				return nil, errors.New("invalid narrowed scope")
			}
		}
		body["scopes"] = scopes
	}
	if seconds > 0 {
		if seconds > 30*86400 {
			return nil, errors.New("invalid narrowed duration")
		}
		body["duration_seconds"] = seconds
	}
	return c.do(ctx, http.MethodPost, "/api/v1/link/self/access-requests/"+id+"/answer", body)
}
func (c *Client) Revoke(ctx context.Context, id string) (json.RawMessage, error) {
	if !routeID.MatchString(id) {
		return nil, errors.New("invalid grant id")
	}
	return c.do(ctx, http.MethodPost, "/api/v1/link/self/access-grants/"+id+"/revoke", map[string]any{})
}
func (c *Client) Agents(ctx context.Context) (json.RawMessage, error) {
	return c.do(ctx, http.MethodGet, "/api/v1/link/self/agents", nil)
}
func (c *Client) RegisterAgent(ctx context.Context, entry any) (json.RawMessage, error) {
	return c.do(ctx, http.MethodPost, "/api/v1/agents/register", entry)
}
func (c *Client) Task(ctx context.Context, id string, task any) (json.RawMessage, error) {
	if !routeID.MatchString(id) {
		return nil, errors.New("invalid agent id")
	}
	return c.do(ctx, http.MethodPost, "/api/v1/agents/"+id+"/tasks", task)
}
func (c *Client) TaskStatus(ctx context.Context, id string) (json.RawMessage, error) {
	if !routeID.MatchString(id) {
		return nil, errors.New("invalid task id")
	}
	return c.do(ctx, http.MethodGet, "/api/v1/agents/tasks/"+id, nil)
}
