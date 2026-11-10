// Package gitrelay is Link's side of OpenVibe Git's "clone to my computer" and "your computer as a git host"
// (Git plan G3). Link connects out to OpenVibe.Git: there is no listening port and no inbound request, the same
// shape as Link's Connect relay. One link carries a clone request:
//
//	GET  <base>/api/v1/link/clone-requests/<clr_…>?token=<one-time>     the requested owner/name
//	POST <base>/api/v1/link/clone-requests/<clr_…>?token=<one-time>     approved, progress, done, refused, failed
//
// and another keeps a machine published, read-only:
//
//	POST <base>/api/v1/link/machines/<mch_…>/hello?token=<one-time>     the machine and the repos under the root
//	GET  <base>/api/v1/link/machines/<mch_…>/watch?token=<one-time>     one browsing request, or a timeout
//	POST <base>/api/v1/link/machines/<mch_…>/results?token=<one-time>   tree, file or log
//
// A token is in the link the page hands to the person; it is never logged. The page polls the same documents over
// its own signed-in session (contracts: git.clone-request@1, git.machine@1).
package gitrelay

import (
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
)

var (
	requestIDRe = regexp.MustCompile(`^clr_[0-9A-HJKMNP-TV-Z]{26}$`)
	machineIDRe = regexp.MustCompile(`^mch_[0-9A-HJKMNP-TV-Z]{26}$`)
	repoRe      = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,99}/[a-z0-9][a-z0-9._-]{0,99}$`)
)

// Endpoint is one link the page gave the person: the Git origin, what kind of relay it is, its id and token.
type Endpoint struct {
	Base  string // https://git.openvibe.codes (never a path)
	Kind  string // "clone" or "machine"
	ID    string // clr_… or mch_…
	Token string
}

// ParseEndpoint reads an https link from a repository page, or the `openvibe-link://clone?url=…` deep link that
// wraps one. Plain http is accepted for a loopback server only (tests and a local Git).
func ParseEndpoint(raw string) (Endpoint, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Endpoint{}, errors.New("the link is empty")
	}
	if strings.HasPrefix(raw, "openvibe-link://") {
		u, err := url.Parse(raw)
		if err != nil {
			return Endpoint{}, errors.New("not a valid openvibe-link:// link")
		}
		if strings.ToLower(u.Host) != "clone" {
			return Endpoint{}, fmt.Errorf("openvibe-link://%s is not a clone link", u.Host)
		}
		raw = u.Query().Get("url")
		if raw == "" {
			return Endpoint{}, errors.New("the openvibe-link:// clone link has no url")
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" {
		return Endpoint{}, errors.New("the link is not an https address with an id and token")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Hostname())) {
		return Endpoint{}, errors.New("refusing a link that is not https (plain http is for a loopback server only)")
	}
	token := u.Query().Get("token")
	if len(token) < 20 || len(token) > 200 || strings.ContainsAny(token, " \t\r\n") {
		return Endpoint{}, errors.New("the link has no usable token")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	// api/v1/link/clone-requests/<id> or api/v1/link/machines/<id>
	if len(parts) != 5 || parts[0] != "api" || parts[1] != "v1" || parts[2] != "link" {
		return Endpoint{}, errors.New("the link is not a Link relay address")
	}
	ep := Endpoint{Base: u.Scheme + "://" + u.Host, Token: token, ID: parts[4]}
	switch parts[3] {
	case "clone-requests":
		if !requestIDRe.MatchString(ep.ID) {
			return Endpoint{}, errors.New("the link has no clone request id (clr_…)")
		}
		ep.Kind = "clone"
	case "machines":
		if !machineIDRe.MatchString(ep.ID) {
			return Endpoint{}, errors.New("the link has no machine id (mch_…)")
		}
		ep.Kind = "machine"
	default:
		return Endpoint{}, errors.New("the link is not a Link relay address")
	}
	return ep, nil
}

func isLoopback(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || host == "[::1]"
}

// Progress is git's own line and the percentage it reported.
type Progress struct {
	Percent int    `json:"percent"`
	Line    string `json:"line"`
}

// CloneRequest is contracts/git/clone-request.v1.json.
type CloneRequest struct {
	ID        string    `json:"id"`
	Repo      string    `json:"repo"`
	State     string    `json:"state"`
	CloneURL  string    `json:"clone_url,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Progress  *Progress `json:"progress,omitempty"`
	Path      string    `json:"path,omitempty"`
	Reason    string    `json:"reason,omitempty"`
}

// RepoName is the folder name of a repository (the part after the slash), once the shape is checked.
func RepoName(repo string) (owner, name string, err error) {
	if !repoRe.MatchString(repo) {
		return "", "", fmt.Errorf("%q is not an owner/name repository", repo)
	}
	owner, name, _ = strings.Cut(repo, "/")
	if strings.HasSuffix(name, ".git") || name == "." || name == ".." || owner == "." || owner == ".." {
		return "", "", fmt.Errorf("%q is not an owner/name repository", repo)
	}
	return owner, name, nil
}

// httpError is a Git answer that was not 2xx; the body is a problem document, read for its detail only.
type httpError struct {
	Status int
	Code   string
	Detail string
}

func (e *httpError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("Git answered %d: %s", e.Status, e.Detail)
	}
	return fmt.Sprintf("Git answered %d", e.Status)
}

type problemDoc struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

func call(ctx context.Context, client *http.Client, method, target string, body any, out any) error {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, target, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "openvibe-link/gitrelay")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		var p problemDoc
		_ = json.Unmarshal(raw, &p)
		return &httpError{Status: resp.StatusCode, Code: p.Code, Detail: p.Detail}
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("Git's answer is not valid JSON: %w", err)
		}
	}
	return nil
}

func linkURL(ep Endpoint, suffix string) string {
	return ep.Base + "/api/v1/link/" + map[string]string{"clone": "clone-requests", "machine": "machines"}[ep.Kind] + "/" + ep.ID + suffix + "?token=" + url.QueryEscape(ep.Token)
}
