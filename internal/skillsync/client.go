package skillsync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// DefaultEndpoint is where the skills hub lives: OpenVibe.Codes.
const DefaultEndpoint = "https://openvibe.codes"

// DefaultTokenEnv is the environment variable holding the person's OpenVibe
// access token (the same one OpenVibe Memory sync uses).
const DefaultTokenEnv = "OPENVIBE_TOKEN"

// MaxResponseBytes bounds the manifest a hostile or broken hub can make the
// Link read: one skill is at most 128 KiB and a person syncs a handful.
const MaxResponseBytes = 4 << 20

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// Client reads one person's sync manifest from OpenVibe.Codes.
type Client struct {
	base  string
	token string
	http  *http.Client
}

// NewClient checks the endpoint and keeps the token for the Authorization
// header only; it is never logged or written.
func NewClient(endpoint, token string) (*Client, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1"))) {
		return nil, fmt.Errorf("the skills endpoint must be an https origin (or localhost in development)")
	}
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("the access token is empty")
	}
	return &Client{base: strings.TrimRight(endpoint, "/"), token: token, http: &http.Client{Timeout: 20 * time.Second}}, nil
}

// Manifest reads what this person chose to sync plus this project's picks. An
// empty project is the person's own scope.
func (c *Client) Manifest(ctx context.Context, project string) ([]Skill, error) {
	u := c.base + "/api/v1/me/skills"
	if project != "" {
		u += "?project=" + url.QueryEscape(project)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("the skills hub could not be reached: %w", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, MaxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxResponseBytes {
		return nil, fmt.Errorf("the skills hub sent more than %d bytes; refusing it", MaxResponseBytes)
	}
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("the skills hub refused the access token (%d): set %s to a current OpenVibe access token", res.StatusCode, DefaultTokenEnv)
	}
	if res.StatusCode != http.StatusOK {
		detail := strings.TrimSpace(string(raw))
		if len(detail) > 200 {
			detail = detail[:200]
		}
		return nil, fmt.Errorf("the skills hub answered %d: %s", res.StatusCode, detail)
	}
	var body struct {
		Skills []Skill `json:"skills"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("the skills hub answered a body that is not a manifest: %w", err)
	}
	for i := range body.Skills {
		s := &body.Skills[i]
		if !slugPattern.MatchString(s.Slug) {
			return nil, fmt.Errorf("the hub sent a skill with an unsafe folder name %q; refusing the whole manifest", s.Slug)
		}
		if s.Kind == "" {
			return nil, fmt.Errorf("the hub sent skill %s without a kind", s.Slug)
		}
		if len([]rune(s.Body)) > MaxBodyRunes {
			s.Body = string([]rune(s.Body)[:MaxBodyRunes])
		}
	}
	return body.Skills, nil
}
