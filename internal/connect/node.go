package connect

// A share served by this computer itself, as OpenVibe Node (CON-4, ADR-057 decision 4). The share's owner picks the
// computer on the share's page; there is no connect key. Before every connection the Node buys its node token (subject
// node:nod_…, audience openvibe.link) and asks Connect for a tunnel token for that one share:
//
//	POST https://connect.openvibe.website/api/v1/node/shares/<shr_…>/tunnel-token   Authorization: Bearer <node token>
//	→ {"tunnel_token":"ovt_…","expires_in":60,"share":{"id","host","url","local_port","mode"}}
//
// then says hello with both, {"t":"hello","v":1,"share":"shr_…","node_token":…,"tunnel":"ovt_…",…}, and sends
// {"t":"reauth","node_token":…} every ReauthEvery so the tunnel outlives its 5-minute token. Neither token is logged.
//
// The port stays this computer's: the Node serves the port it was given (or the one the share had when it was added)
// even if the share's page says another later, so Connect can never point it at a different local service.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// ShareIDRe matches a share id on OpenVibe Connect (shr_ + a ULID).
var ShareIDRe = regexp.MustCompile(`^shr_[0-9A-HJKMNP-TV-Z]{26}$`)

// DefaultReauthEvery is how often a computer's tunnel sends a fresh node token (they last 300 s; Connect allows 30 s
// past the expiry, and the Node's token cache replaces a token 30 s before it).
const DefaultReauthEvery = 45 * time.Second

// NodeShare is one share this computer serves, as Connect lists it.
type NodeShare struct {
	ID        string `json:"id"`
	Host      string `json:"host"`
	URL       string `json:"url"`
	LocalPort int    `json:"local_port"`
	Mode      string `json:"mode"`
}

type problem struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// apiURL turns the tunnel URL (wss://host/tunnel) into Connect's API URL for path; plain ws only to a loopback host.
func apiURL(server, path string) (string, error) {
	if server == "" {
		server = DefaultServer
	}
	u, err := url.Parse(server)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("connect: %q is not a tunnel URL", server)
	}
	switch u.Scheme {
	case "wss":
		u.Scheme = "https"
	case "ws":
		h := u.Hostname()
		ip := net.ParseIP(h)
		if h != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return "", fmt.Errorf("connect: refusing to talk to %s over plain http", u.Host)
		}
		u.Scheme = "http"
	default:
		return "", fmt.Errorf("connect: %q is not a ws:// or wss:// URL", server)
	}
	u.Path, u.RawQuery = path, ""
	return u.String(), nil
}

func nodeCall(ctx context.Context, client *http.Client, method, server, path, nodeToken, userAgent string, out any) (int, error) {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	target, err := apiURL(server, path)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+nodeToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("connect: %s: %w", path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode/100 != 2 {
		var p problem
		_ = json.Unmarshal(raw, &p)
		msg := p.Detail
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		return resp.StatusCode, fmt.Errorf("connect: HTTP %d %s: %s", resp.StatusCode, p.Code, msg)
	}
	return resp.StatusCode, json.Unmarshal(raw, out)
}

// NodeShares lists the open shares bound to this computer (GET /api/v1/node/shares).
func NodeShares(ctx context.Context, client *http.Client, server, nodeToken, userAgent string) ([]NodeShare, error) {
	var out struct {
		Shares []NodeShare `json:"shares"`
	}
	if _, err := nodeCall(ctx, client, http.MethodGet, server, "/api/v1/node/shares", nodeToken, userAgent, &out); err != nil {
		return nil, err
	}
	return out.Shares, nil
}

// tunnelToken asks for one hello's tunnel token. A share this computer does not serve (404) or a closed one (410)
// stops it; anything else is tried again.
func tunnelToken(ctx context.Context, o Options, server, shareID, nodeToken string) (string, NodeShare, error) {
	var out struct {
		Token string    `json:"tunnel_token"`
		Share NodeShare `json:"share"`
	}
	status, err := nodeCall(ctx, o.HTTPClient, http.MethodPost, server, "/api/v1/node/shares/"+url.PathEscape(shareID)+"/tunnel-token", nodeToken, "openvibe-node/"+o.Version, &out)
	if err != nil {
		if status == http.StatusNotFound || status == http.StatusGone {
			return "", NodeShare{}, fmt.Errorf("%w: %v", ErrStopped, err)
		}
		return "", NodeShare{}, err
	}
	if !strings.HasPrefix(out.Token, "ovt_") {
		return "", NodeShare{}, errors.New("connect: no tunnel token in the answer")
	}
	return out.Token, out.Share, nil
}
