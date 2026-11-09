// Package egress carries one person's cloud browsing through their own paired Link.
package egress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/OpenVibers/OpenVibe.Link/internal/credentials"
	"github.com/OpenVibers/OpenVibe.Link/internal/link"
)

type Tokens interface {
	Token(context.Context) (credentials.Secret, error)
}
type Client struct {
	Base   string
	Tokens Tokens
	HTTP   *http.Client
}
type Plan struct {
	Mode       string   `json:"mode"`
	Session    string   `json:"session_id"`
	Credential string   `json:"credential"`
	Device     string   `json:"device"`
	DeviceName string   `json:"device_name"`
	Relay      string   `json:"relay_url"`
	Enabled    bool     `json:"egress_via_link"`
	Offline    string   `json:"offline_choice"`
	Cap        int64    `json:"monthly_bytes"`
	Excluded   []string `json:"excluded_sites"`
}
type Grant struct {
	Session string `json:"session_id"`
	Link    string `json:"link_id"`
	Expires int64  `json:"expires_at"`
	Cap     int64  `json:"monthly_bytes"`
	Enabled bool   `json:"egress_via_link"`
}

func NewClient(c *credentials.Credentials) *Client {
	return &Client{Base: strings.TrimRight(c.Network, "/"), Tokens: link.NewTokenSourceFor(c, nil, "openvibe-link/egress", "openvibe.network")}
}
func (c *Client) Call(ctx context.Context, method, path string, body, out any, authenticated bool) error {
	var data io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		data = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, data)
	if err != nil {
		return errors.New("browsing connection unavailable")
	}
	req.Header.Set("Content-Type", "application/json")
	if authenticated {
		token, err := c.Tokens.Token(ctx)
		if err != nil {
			return errors.New("computer sign-in unavailable")
		}
		req.Header.Set("Authorization", "Bearer "+token.Reveal())
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("browsing connection unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return errors.New("browsing connection refused")
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 32<<10)).Decode(out)
}
func (c *Client) Open(ctx context.Context, session, credential, node string) (Grant, error) {
	var grant Grant
	err := c.Call(ctx, "POST", "/api/v1/link-egress/open", map[string]string{"session_id": session, "credential": credential, "link_id": node}, &grant, false)
	if err == nil && (grant.Session != session || grant.Link != node || !grant.Enabled || grant.Expires <= time.Now().UnixMilli()) {
		err = errors.New("browsing connection refused")
	}
	return grant, err
}
func (c *Client) Desk(ctx context.Context, desk string) (Plan, error) {
	var p Plan
	err := c.Call(ctx, "GET", "/api/v1/link/self/egress/desks/"+url.PathEscape(desk), nil, &p, true)
	if err != nil {
		return p, err
	}
	if p.Mode == "wait" {
		return p, errors.New("waiting for your computer")
	}
	if p.Mode == "direct" {
		return p, nil
	}
	u, err := url.Parse(p.Relay)
	if err != nil || p.Mode != "link" || u.Scheme != "wss" || !strings.HasSuffix(u.Hostname(), ".openvibe.link") || u.Port() != "" || u.User != nil || u.Path != "/__ov_egress" || u.RawQuery != "" || len(p.Credential) != 43 {
		return p, errors.New("invalid browsing connection")
	}
	return p, nil
}
