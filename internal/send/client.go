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

	"github.com/OpenVibers/OpenVibe.Link/internal/credentials"
)

type Tokens interface {
	Token(context.Context) (credentials.Secret, error)
	Invalidate()
}

var itemID = regexp.MustCompile(`^snd_[a-f0-9]{32}$`)
var uploadID = regexp.MustCompile(`^upl_[a-f0-9]{32}$`)

// uploadChunk is how much of the base64 envelope Link sends in one part. The relay accepts up to 2 MiB; 1 MiB keeps
// one request small enough to retry quickly on a home connection.
const uploadChunk = 1024 * 1024

type Client struct {
	BaseURL string
	Tokens  Tokens
	HTTP    *http.Client
	Agent   string
	// Self is this machine's principal (lnk_…), so a direct transfer can name its sender.
	Self string
}

type Machine struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Online    bool     `json:"online"`
	PublicKey JWK      `json:"public_key"`
	LAN       []string `json:"lan"`
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

func (c *Client) Register(ctx context.Context, key JWK, lan ...string) error {
	body := map[string]any{"public_key": Public(key)}
	if len(lan) > 0 {
		body["lan"] = lan
	}
	return c.do(ctx, http.MethodPut, "/api/v1/link/self/send/key", body, nil)
}

// LANKey is the owner's shared secret for direct transfers; every one of the owner's machines reads the same one.
func (c *Client) LANKey(ctx context.Context) (string, error) {
	var out struct {
		Key string `json:"key"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/link/self/send/lan-key", nil, &out)
	return out.Key, err
}
func (c *Client) Targets(ctx context.Context) ([]Machine, error) {
	var out struct {
		Machines []Machine `json:"machines"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/link/self/send/targets", nil, &out)
	return out.Machines, err
}

// Upload is the relay's view of a transfer that is arriving in parts: the part after the last one it stored, and how
// many bytes it holds. Resuming asks the relay for this instead of guessing from the failed request.
type Upload struct {
	ID       string `json:"id"`
	Bytes    int64  `json:"bytes"`
	Received int64  `json:"received"`
	Seq      int    `json:"seq"`
	ItemID   string `json:"item_id"`
}

// Send uploads the Locker envelope in parts and resumes from the relay's acknowledged part after a dropped
// connection. Finishing is idempotent on the relay, so a lost finish answer does not make a second item.
func (c *Client) Send(ctx context.Context, target, envelope string) error {
	if envelope == "" {
		return fmt.Errorf("the encrypted item is empty")
	}
	// Same network: hand it over directly, sealed as before. Any problem (off the LAN, a timeout, no shared secret)
	// falls through to the relay, which keeps the item until the receiver accepts it.
	if err := c.sendLAN(ctx, target, envelope); err == nil {
		return nil
	}
	upload, err := c.StartUpload(ctx, target, int64(len(envelope)))
	if err != nil {
		// A Link newer than the Network it talks to still works: the one-request form is the v1 relay.
		if single := c.do(ctx, http.MethodPost, "/api/v1/link/self/send", map[string]string{"target_node": target, "envelope": envelope}, nil); single == nil {
			return nil
		}
		return err
	}
	failures := 0
	for {
		for upload.Seq*uploadChunk < len(envelope) {
			from := upload.Seq * uploadChunk
			to := from + uploadChunk
			if to > len(envelope) {
				to = len(envelope)
			}
			next, err := c.UploadPart(ctx, upload.ID, upload.Seq, envelope[from:to])
			if err == nil {
				upload = next
				failures = 0
				continue
			}
			if ctx.Err() != nil {
				return fmt.Errorf("sending was stopped")
			}
			failures++
			if failures > 5 {
				return fmt.Errorf("the connection kept dropping; the parts already sent are kept for an hour, try again")
			}
			time.Sleep(time.Duration(failures) * 250 * time.Millisecond)
			status, statusErr := c.UploadStatus(ctx, upload.ID)
			if statusErr != nil {
				continue
			}
			upload = status
		}
		item, err := c.FinishUpload(ctx, upload.ID)
		if err == nil {
			upload.ItemID = item
			return nil
		}
		if ctx.Err() != nil {
			return fmt.Errorf("sending was stopped")
		}
		failures++
		if failures > 5 {
			return fmt.Errorf("the relay did not confirm the item; the parts are kept for an hour, try again")
		}
		status, statusErr := c.UploadStatus(ctx, upload.ID)
		if statusErr == nil {
			if status.ItemID != "" {
				return nil
			}
			upload = status
		}
		time.Sleep(time.Duration(failures) * 250 * time.Millisecond)
	}
}

func (c *Client) StartUpload(ctx context.Context, target string, bytes int64) (Upload, error) {
	var out Upload
	err := c.do(ctx, http.MethodPost, "/api/v1/link/self/send/upload", map[string]any{"target_node": target, "bytes": bytes}, &out)
	return out, err
}

func (c *Client) UploadPart(ctx context.Context, id string, seq int, chunk string) (Upload, error) {
	if !uploadID.MatchString(id) {
		return Upload{}, fmt.Errorf("this transfer is no longer available")
	}
	var out Upload
	err := c.do(ctx, http.MethodPut, fmt.Sprintf("/api/v1/link/self/send/upload/%s/%d", id, seq), map[string]string{"chunk": chunk}, &out)
	return out, err
}

func (c *Client) UploadStatus(ctx context.Context, id string) (Upload, error) {
	if !uploadID.MatchString(id) {
		return Upload{}, fmt.Errorf("this transfer is no longer available")
	}
	var out Upload
	err := c.do(ctx, http.MethodGet, "/api/v1/link/self/send/upload/"+id, nil, &out)
	return out, err
}

func (c *Client) FinishUpload(ctx context.Context, id string) (string, error) {
	if !uploadID.MatchString(id) {
		return "", fmt.Errorf("this transfer is no longer available")
	}
	var out struct {
		ID string `json:"id"`
	}
	err := c.do(ctx, http.MethodPost, "/api/v1/link/self/send/upload/"+id+"/finish", map[string]string{}, &out)
	return out.ID, err
}

// Outbox is the sender's list: what this machine sent and whether the receiving machine accepted or saved it.
type OutboxItem struct {
	ID         string    `json:"id"`
	Target     string    `json:"target_node"`
	TargetName string    `json:"target_name"`
	Bytes      int64     `json:"bytes"`
	State      string    `json:"state"`
	CreatedAt  time.Time `json:"created_at"`
	AcceptedAt time.Time `json:"accepted_at"`
	ExpiresAt  time.Time `json:"expires_at"`
}

func (c *Client) Outbox(ctx context.Context) ([]OutboxItem, error) {
	var out struct {
		Items []OutboxItem `json:"items"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/link/self/send/outbox", nil, &out)
	return out.Items, err
}
func (c *Client) Inbox(ctx context.Context) ([]Item, error) {
	var out struct {
		Items []Item `json:"items"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/link/self/send/inbox", nil, &out)
	return out.Items, err
}
func (c *Client) Accept(ctx context.Context, id string) (string, error) {
	if !itemID.MatchString(id) {
		return "", fmt.Errorf("this transfer is no longer available")
	}
	var out struct {
		Envelope string `json:"envelope"`
	}
	err := c.do(ctx, http.MethodPost, "/api/v1/link/self/send/"+id+"/accept", map[string]string{}, &out)
	return out.Envelope, err
}
func (c *Client) Decline(ctx context.Context, id string) error {
	if !itemID.MatchString(id) {
		return fmt.Errorf("this transfer is no longer available")
	}
	return c.do(ctx, http.MethodPost, "/api/v1/link/self/send/"+id+"/decline", map[string]string{}, nil)
}
func (c *Client) Complete(ctx context.Context, id string) error {
	if !itemID.MatchString(id) {
		return fmt.Errorf("this transfer is no longer available")
	}
	return c.do(ctx, http.MethodPost, "/api/v1/link/self/send/"+id+"/complete", map[string]string{}, nil)
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
