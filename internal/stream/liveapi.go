// Package stream is Node's part of Agent Streams S1: the desk stream. It publishes a desk's screen to OpenVibe.Live
// over WHIP with a publish ticket (wpt_…), polls the stream's state, sends snapshots while nobody watches, renews the
// ticket at three hours, blanks the picture behind the curtain, and measures glass-to-glass latency with a QR probe.
//
// The design is plan-state-2026-10-22/AGENT-STREAMS-S1-DESIGN-2026-11-06.md. Tickets and tokens are credentials.Secret:
// they go on the wire and nowhere else, never into a log line, an error or `openvibe-link status`.
package stream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/OpenVibers/OpenVibe.Link/internal/voice"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/OpenVibers/OpenVibe.Link/internal/credentials"
)

// DefaultLiveURL is where the agent-stream API lives.
const DefaultLiveURL = "https://openvibe.live"

// ErrTicketInvalid is Live refusing a wpt_ (401 invalid_ticket): it is wrong, expired, revoked or names another stream.
var ErrTicketInvalid = errors.New("the publish ticket was refused (invalid_ticket)")

// ErrAlreadyPublishing is a second WHIP session for a stream that already has one (409 already_publishing).
var ErrAlreadyPublishing = errors.New("this stream already has a WHIP session (already_publishing)")

var (
	idRe        = regexp.MustCompile(`^ags_[0-9A-HJKMNP-TV-Z]{26}$`)
	ticketRe    = regexp.MustCompile(`wpt_[A-Za-z0-9_-]{43}`)
	viewTickRe  = regexp.MustCompile(`wvt_[A-Za-z0-9_-]{43}`)
	bearerAnyRe = regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/-]{8,}`)
)

// Task is what the agent is doing and where that task lives (live.agent-stream@1 task).
type Task struct {
	Source string `json:"source"` // codes, actor, desk or node
	Ref    string `json:"ref"`
	Title  string `json:"title"`
	URL    string `json:"url,omitempty"`
}

// CreateRequest is the body of POST /api/streams/agent.
type CreateRequest struct {
	Title      string   `json:"title"`
	Task       Task     `json:"task"`
	Visibility string   `json:"visibility"`
	AllowSites []string `json:"allow_sites,omitempty"`
}

// Stream is live.agent-stream@1, the fields Node needs.
type Stream struct {
	ID            string  `json:"id"`
	Owner         string  `json:"owner"`
	Channel       string  `json:"channel"`
	Title         string  `json:"title"`
	Task          Task    `json:"task"`
	Visibility    string  `json:"visibility"`
	Mode          string  `json:"mode"`
	State         string  `json:"state"`
	Viewers       int     `json:"viewers"`
	WorkedSeconds int64   `json:"worked_seconds"`
	PageURL       string  `json:"page_url"`
	Label         string  `json:"label"`
	StartedAt     *string `json:"started_at"`
	EndedAt       *string `json:"ended_at"`
}

// Publish is live.agent-stream-publish@1: where the desk publishes and with what.
type Publish struct {
	WHIPURL   string             `json:"whip_url"`
	Ticket    credentials.Secret `json:"-"`
	ExpiresAt time.Time          `json:"-"`
}

// publishWire is Publish on the wire; the ticket is read once and wrapped in a Secret.
type publishWire struct {
	WHIPURL   string `json:"whip_url"`
	Ticket    string `json:"ticket"`
	ExpiresAt string `json:"expires_at"`
}

// Curtain is the desk's privacy screen as Live records it.
type Curtain struct {
	Up     bool       `json:"up"`
	Since  *time.Time `json:"since,omitempty"`
	Reason string     `json:"reason,omitempty"`
}

// State is live.agent-stream-state@1, what the desk reads every ten seconds.
type State struct {
	Viewers int        `json:"viewers"`
	Encode  bool       `json:"encode"`
	Curtain Curtain    `json:"curtain"`
	EndsAt  *time.Time `json:"ends_at"`

	// Lines are the lines the agent said (S4): the desk speaks them, one at a time, in its own voice.
	Lines []voice.Line `json:"lines"`
	// Voice is the voice the agent speaks in: the mode, the personality, the voice and the mute.
	Voice voice.State `json:"voice"`
}

// VoiceCurtain is the curtain as the voice sees it: the password-field reason is the one that silences the voice.
func VoiceCurtain(c Curtain) voice.Curtain {
	return voice.Curtain{Up: c.Up, Reason: c.Reason}
}

// CreateResult is the 201 answer of POST /api/streams/agent.
type CreateResult struct {
	Stream  Stream  `json:"stream"`
	Publish Publish `json:"publish"`
}

// Client talks to Live's agent-stream API. Token buys the node token that POST /api/streams/agent authenticates with;
// every other call uses the stream's publish ticket.
type Client struct {
	base  string
	token func(context.Context) (credentials.Secret, error)
	http  *http.Client
	ua    string
}

// ClientOptions configure a Client.
type ClientOptions struct {
	BaseURL    string
	Token      func(context.Context) (credentials.Secret, error)
	HTTPClient *http.Client
	UserAgent  string
}

// NewClient returns a client for base (DefaultLiveURL when empty). Plain http is accepted only for a loopback host,
// which is what tests use.
func NewClient(o ClientOptions) (*Client, error) {
	base := strings.TrimSuffix(o.BaseURL, "/")
	if base == "" {
		base = DefaultLiveURL
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil {
		return nil, errors.New("stream: the Live URL is not an origin")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Hostname())) {
		return nil, errors.New("stream: the Live URL must be https")
	}
	if o.Token == nil {
		return nil, errors.New("stream: no node token source (pair this machine with OpenVibe.Network)")
	}
	c := &Client{base: base, token: o.Token, http: o.HTTPClient, ua: o.UserAgent}
	if c.http == nil {
		c.http = &http.Client{Timeout: 20 * time.Second}
	}
	if c.ua == "" {
		c.ua = "openvibe-link"
	}
	return c, nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Create calls POST /api/streams/agent with the machine's node token (audience openvibe.live). It is the only call
// that carries a token; the answer's ticket is stored in a Secret and never logged.
func (c *Client) Create(ctx context.Context, req CreateRequest) (CreateResult, error) {
	var out CreateResult
	if err := validateCreate(req); err != nil {
		return out, err
	}
	tok, err := c.token(ctx)
	if err != nil {
		return out, err
	}
	var wire struct {
		Stream  Stream      `json:"stream"`
		Publish publishWire `json:"publish"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/streams/agent", tok, req, &wire); err != nil {
		return out, err
	}
	pub, err := parsePublish(wire.Publish)
	if err != nil {
		return out, err
	}
	out.Stream, out.Publish = wire.Stream, pub
	return out, nil
}

// State calls GET /api/streams/agent/:id/state with the ticket.
func (c *Client) State(ctx context.Context, id string, ticket credentials.Secret) (State, error) {
	var out State
	if err := checkID(id); err != nil {
		return out, err
	}
	err := c.do(ctx, http.MethodGet, "/api/streams/agent/"+id+"/state", ticket, nil, &out)
	return out, err
}

// Renew calls POST /api/streams/agent/:id/ticket with the ticket and returns the new one. The desk renews at 3 h.
func (c *Client) Renew(ctx context.Context, id string, ticket credentials.Secret) (Publish, error) {
	var out Publish
	if err := checkID(id); err != nil {
		return out, err
	}
	var wire publishWire
	if err := c.do(ctx, http.MethodPost, "/api/streams/agent/"+id+"/ticket", ticket, nil, &wire); err != nil {
		return out, err
	}
	return parsePublish(wire)
}

// Revoke calls DELETE /api/streams/agent/:id/ticket: the kill switch. Live ends the WHIP session at once.
func (c *Client) Revoke(ctx context.Context, id string, ticket credentials.Secret) error {
	if err := checkID(id); err != nil {
		return err
	}
	return c.do(ctx, http.MethodDelete, "/api/streams/agent/"+id+"/ticket", ticket, nil, nil)
}

// Curtain reports the desk's privacy screen: POST /api/streams/agent/:id/curtain {up, reason}.
func (c *Client) Curtain(ctx context.Context, id string, ticket credentials.Secret, up bool, reason string) error {
	if err := checkID(id); err != nil {
		return err
	}
	if !validReason(reason) {
		return fmt.Errorf("stream: curtain reason %q is not one of password_field, secret_file, env_print, manual, site, detector", reason)
	}
	return c.do(ctx, http.MethodPost, "/api/streams/agent/"+id+"/curtain", ticket,
		map[string]any{"up": up, "reason": reason}, nil)
}

// PostFrame sends a snapshot (JPEG or WebP, at most 200 KB): PUT /api/streams/agent/:id/frame.
func (c *Client) PostFrame(ctx context.Context, id string, ticket credentials.Secret, frame []byte) error {
	if err := checkID(id); err != nil {
		return err
	}
	if len(frame) == 0 || len(frame) > MaxFrameBytes {
		return fmt.Errorf("stream: a snapshot is 1 to %d bytes (got %d)", MaxFrameBytes, len(frame))
	}
	typ := http.DetectContentType(frame)
	if typ != "image/jpeg" && typ != "image/webp" {
		return fmt.Errorf("stream: a snapshot must be JPEG or WebP (got %s)", typ)
	}
	return c.doRaw(ctx, http.MethodPut, "/api/streams/agent/"+id+"/frame", ticket, typ, frame, nil)
}

// End calls POST /api/streams/agent/:id/end.
func (c *Client) End(ctx context.Context, id string, ticket credentials.Secret) error {
	if err := checkID(id); err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, "/api/streams/agent/"+id+"/end", ticket, nil, nil)
}

// MaxFrameBytes is the snapshot cap in the design.
const MaxFrameBytes = 200 * 1024

func validateCreate(r CreateRequest) error {
	if strings.TrimSpace(r.Title) == "" || len(r.Title) > 200 {
		return errors.New("stream: title is 1 to 200 characters")
	}
	switch r.Task.Source {
	case "codes", "actor", "desk", "node":
	default:
		return errors.New("stream: task.source is codes, actor, desk or node")
	}
	if r.Task.Ref == "" || len(r.Task.Ref) > 200 || r.Task.Title == "" || len(r.Task.Title) > 200 {
		return errors.New("stream: task.ref and task.title are required (at most 200 characters)")
	}
	if r.Task.URL != "" {
		if u, err := url.Parse(r.Task.URL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return errors.New("stream: task.url must be an http(s) URL")
		}
	}
	switch r.Visibility {
	case "private", "unlisted", "public":
	default:
		return errors.New("stream: visibility is private, unlisted or public")
	}
	if len(r.AllowSites) > 50 {
		return errors.New("stream: allow_sites carries at most 50 hosts")
	}
	for _, s := range r.AllowSites {
		if u, err := url.Parse(s); err != nil || u.Host == "" {
			return fmt.Errorf("stream: allow_sites entry %q is not a URL", s)
		}
	}
	return nil
}

func checkID(id string) error {
	if !idRe.MatchString(id) {
		return fmt.Errorf("stream: %q is not an agent-stream id (ags_…)", id)
	}
	return nil
}

func validReason(r string) bool {
	switch r {
	case "password_field", "secret_file", "env_print", "manual", "site", "detector":
		return true
	}
	return false
}

func parsePublish(w publishWire) (Publish, error) {
	var out Publish
	if w.WHIPURL == "" || w.Ticket == "" || w.ExpiresAt == "" {
		return out, errors.New("stream: the publish part is missing whip_url, ticket or expires_at")
	}
	u, err := url.Parse(w.WHIPURL)
	if err != nil || u.Host == "" || u.User != nil {
		return out, errors.New("stream: the publish whip_url is not a URL")
	}
	if !ticketRe.MatchString(w.Ticket) || !strings.HasPrefix(w.Ticket, "wpt_") {
		return out, errors.New("stream: the publish ticket is not a wpt_…")
	}
	exp, err := time.Parse(time.RFC3339, w.ExpiresAt)
	if err != nil {
		return out, errors.New("stream: expires_at is not RFC 3339")
	}
	out.WHIPURL, out.Ticket, out.ExpiresAt = w.WHIPURL, credentials.NewSecret(w.Ticket), exp
	return out, nil
}

func (c *Client) do(ctx context.Context, method, path string, auth credentials.Secret, body any, out any) error {
	var raw []byte
	typ := ""
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		raw, typ = b, "application/json"
	}
	return c.doRaw(ctx, method, path, auth, typ, raw, out)
}

func (c *Client) doRaw(ctx context.Context, method, path string, auth credentials.Secret, typ string, body []byte, out any) error {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return err
	}
	if typ != "" {
		req.Header.Set("Content-Type", typ)
	}
	// A ticket is sent only to the configured Live origin, never after a redirect to another host.
	req.Header.Set("User-Agent", c.ua)
	if !auth.IsZero() {
		req.Header.Set("Authorization", "Bearer "+auth.Reveal())
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return errors.New(redact(err.Error()))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		var pe struct {
			Code   string `json:"code"`
			Error  string `json:"error"`
			Detail string `json:"detail"`
		}
		_ = json.Unmarshal(raw, &pe)
		code := pe.Code
		if code == "" {
			code = pe.Error
		}
		switch {
		case resp.StatusCode == http.StatusUnauthorized && (code == "invalid_ticket" || code == ""):
			return ErrTicketInvalid
		case resp.StatusCode == http.StatusConflict && code == "already_publishing":
			return ErrAlreadyPublishing
		}
		msg := strings.TrimSpace(code + " " + pe.Detail)
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
			if len(msg) > 200 {
				msg = msg[:200]
			}
		}
		return fmt.Errorf("stream: Live answered HTTP %d %s", resp.StatusCode, redact(msg))
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return errors.New("stream: Live's answer is not valid JSON")
		}
	}
	return nil
}

// redact removes anything that looks like a publish or view ticket, and any bearer value, from a message that may be
// logged or returned in an error.
func redact(s string) string {
	s = ticketRe.ReplaceAllString(s, "[ticket]")
	s = viewTickRe.ReplaceAllString(s, "[view-ticket]")
	s = bearerAnyRe.ReplaceAllString(s, "Bearer [redacted]")
	return s
}
