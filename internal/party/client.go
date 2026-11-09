package party

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type TokenSource func(context.Context) (string, error)

type Client struct {
	PartyURL      string
	ConnectServer string
	PartyID       string
	ChatToken     TokenSource
	ConnectToken  TokenSource
	HTTP          *http.Client
}

type Claim struct {
	ICEServers []ICEServer `json:"ice_servers"`
	ShareGrant string      `json:"share_grant"`
}

type Share struct {
	ID   string `json:"id"`
	URL  string `json:"url"`
	Port int    `json:"local_port"`
}

func (c Client) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func (c Client) base(chat bool) (string, error) {
	if chat {
		base := c.PartyURL
		if base == "" {
			base = "https://openvibe.party"
		}
		u, err := url.Parse(base)
		if err != nil || u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && loopback(u.Hostname()))) {
			return "", errors.New("party URL must be HTTPS or loopback HTTP")
		}
		return strings.TrimSuffix(u.String(), "/"), nil
	}
	base := c.ConnectServer
	if base == "" {
		base = "wss://connect.openvibe.link/tunnel"
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return "", errors.New("invalid Connect URL")
	}
	if u.Scheme == "wss" {
		u.Scheme = "https"
	} else if u.Scheme == "ws" && loopback(u.Hostname()) {
		u.Scheme = "http"
	} else {
		return "", errors.New("Connect requires TLS")
	}
	u.Path, u.RawQuery, u.Fragment = "", "", ""
	return strings.TrimSuffix(u.String(), "/"), nil
}

func loopback(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || (ip != nil && ip.IsLoopback())
}

func (c Client) call(ctx context.Context, chat bool, method, path string, input, output any) error {
	base, err := c.base(chat)
	if err != nil {
		return err
	}
	var body io.Reader
	if input != nil {
		b, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, body)
	if err != nil {
		return err
	}
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	var source TokenSource = c.ConnectToken
	if chat {
		source = c.ChatToken
	}
	if source == nil {
		return errors.New("machine token unavailable")
	}
	token, err := source(ctx)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := c.client().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("party API %s %s returned %d", method, path, res.StatusCode)
	}
	if output != nil {
		return json.NewDecoder(io.LimitReader(res.Body, 32*1024)).Decode(output)
	}
	return nil
}

func (c Client) chatPath(suffix string) string {
	return "/api/chat/parties/" + url.PathEscape(c.PartyID) + "/desktop" + suffix
}

func (c Client) Claim(ctx context.Context, port int) (Claim, error) {
	var out Claim
	err := c.call(ctx, true, http.MethodPost, c.chatPath("/claim"), map[string]int{"port": port}, &out)
	return out, err
}

func (c Client) Access(ctx context.Context, subject string) (Access, error) {
	var out Access
	err := c.call(ctx, true, http.MethodGet, c.chatPath("/access")+"?subject="+url.QueryEscape(subject), nil, &out)
	return out, err
}

func (c Client) CreateShare(ctx context.Context, grant string) (Share, error) {
	var out struct {
		Share Share `json:"share"`
	}
	err := c.call(ctx, false, http.MethodPost, "/api/v1/link/party-shares", map[string]string{"grant": grant}, &out)
	return out.Share, err
}

func (c Client) Ready(ctx context.Context, share Share) error {
	return c.call(ctx, true, http.MethodPost, c.chatPath("/ready"), map[string]string{"share_id": share.ID, "url": share.URL}, nil)
}

func (c Client) Stopped(ctx context.Context) error {
	return c.call(ctx, true, http.MethodPost, c.chatPath("/stopped"), nil, nil)
}

func (c Client) SyncMembers(ctx context.Context, share Share, access Access) error {
	members := make([]string, 0, len(access.Members))
	for _, m := range access.Members {
		if m.Username != "" {
			members = append(members, m.Username)
		}
	}
	return c.call(ctx, false, http.MethodPut, "/api/v1/link/shares/"+url.PathEscape(share.ID)+"/party-members", map[string]any{"members": members}, nil)
}

func (c Client) CloseShare(ctx context.Context, share Share) error {
	return c.call(ctx, false, http.MethodDelete, "/api/v1/link/shares/"+url.PathEscape(share.ID)+"/party", nil, nil)
}
