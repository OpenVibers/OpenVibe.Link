package send

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/OpenVibers/OpenVibe.Node/internal/credentials"
)

type Tokens interface {
	Token(context.Context) (credentials.Secret, error)
	Invalidate()
}

var itemID = regexp.MustCompile(`^snd_[a-f0-9]{32}$`)

type Client struct {
	BaseURL string
	Tokens  Tokens
	HTTP    *http.Client
	Agent   string
}

type Machine struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Online    bool   `json:"online"`
	PublicKey JWK    `json:"public_key"`
}

// Item is one waiting transfer as the inbox lists it: who sent it and how big it is. The ciphertext is not in the
// list (it is polled every few seconds); Accept fetches it once.
type Item struct {
	ID         string    `json:"id"`
	Sender     string    `json:"sender_node"`
	SenderName string    `json:"sender_name"`
	Bytes      int64     `json:"bytes"`
	CreatedAt  time.Time `json:"created_at"`
	ExpiresAt  time.Time `json:"expires_at"`
}

func (c *Client) Register(ctx context.Context, key JWK) error {
	return c.do(ctx, http.MethodPut, "/api/v1/node/self/send/key", map[string]any{"public_key": Public(key)}, nil)
}
func (c *Client) Targets(ctx context.Context) ([]Machine, error) {
	var out struct {
		Machines []Machine `json:"machines"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/node/self/send/targets", nil, &out)
	return out.Machines, err
}
func (c *Client) Send(ctx context.Context, target, envelope string) error {
	return c.do(ctx, http.MethodPost, "/api/v1/node/self/send", map[string]string{"target_node": target, "envelope": envelope}, nil)
}
func (c *Client) Inbox(ctx context.Context) ([]Item, error) {
	var out struct {
		Items []Item `json:"items"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/node/self/send/inbox", nil, &out)
	return out.Items, err
}
func (c *Client) Accept(ctx context.Context, id string) (string, error) {
	if !itemID.MatchString(id) {
		return "", fmt.Errorf("this transfer is no longer available")
	}
	var out struct {
		Envelope string `json:"envelope"`
	}
	err := c.do(ctx, http.MethodPost, "/api/v1/node/self/send/"+id+"/accept", map[string]string{}, &out)
	return out.Envelope, err
}
func (c *Client) Decline(ctx context.Context, id string) error {
	if !itemID.MatchString(id) {
		return fmt.Errorf("this transfer is no longer available")
	}
	return c.do(ctx, http.MethodPost, "/api/v1/node/self/send/"+id+"/decline", map[string]string{}, nil)
}
func (c *Client) Complete(ctx context.Context, id string) error {
	if !itemID.MatchString(id) {
		return fmt.Errorf("this transfer is no longer available")
	}
	return c.do(ctx, http.MethodPost, "/api/v1/node/self/send/"+id+"/complete", map[string]string{}, nil)
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	if c == nil || c.Tokens == nil || c.BaseURL == "" {
		return fmt.Errorf("this machine is not paired for Link Send")
	}
	tok, err := c.Tokens.Token(ctx)
	if err != nil {
		return err
	}
	var data io.Reader
	if body != nil {
		b, e := json.Marshal(body)
		if e != nil {
			return e
		}
		data = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, data)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok.Reveal())
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Agent != "" {
		req.Header.Set("User-Agent", c.Agent)
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 45 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("OpenVibe could not be reached")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		var problem struct {
			Detail string `json:"detail"`
		}
		_ = json.Unmarshal(b, &problem)
		if problem.Detail == "" {
			problem.Detail = "the transfer was refused"
		}
		return fmt.Errorf("%s", problem.Detail)
	}
	if out == nil {
		_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return err
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 40<<20)).Decode(out); err != nil {
		return fmt.Errorf("OpenVibe returned an unreadable transfer")
	}
	return nil
}
