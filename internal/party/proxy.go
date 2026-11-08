package party

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Access struct {
	Active  bool   `json:"active"`
	Member  bool   `json:"member"`
	Holder  string `json:"holder"`
	Members []struct {
		Subject  string `json:"subject"`
		Username string `json:"username"`
	} `json:"members"`
}

type AccessFunc func(context.Context, string) (Access, error)

type nekoSession struct {
	cookie, id string
	login      []byte
	checked    time.Time
}

// Proxy exposes neko only through Link's loopback port. Connect injects the
// signed-in subject after its gate; the proxy checks current Chat membership
// before forwarding any HTTP request or WebSocket upgrade.
type Proxy struct {
	plan     Plan
	neko     *url.URL
	access   AccessFunc
	client   *http.Client
	mu       sync.Mutex
	sessions map[string]nekoSession
	holder   string
}

func NewProxy(plan Plan, nekoPort int, access AccessFunc) *Proxy {
	u, _ := url.Parse("http://127.0.0.1:" + itoa(nekoPort))
	return &Proxy{plan: plan, neko: u, access: access, client: &http.Client{Timeout: 6 * time.Second}, sessions: map[string]nekoSession{}}
}

func itoa(n int) string { return strconv.Itoa(n) }

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		http.Error(w, "Local connection required", http.StatusForbidden)
		return
	}
	subject := r.Header.Get("X-Openvibe-Subject")
	if !regexp.MustCompile(`^usr_[0-9A-HJKMNP-TV-Z]{26}$`).MatchString(subject) {
		http.Error(w, "Sign in to the party", http.StatusUnauthorized)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	a, err := p.access(ctx, subject)
	cancel()
	if err != nil || !a.Active || !a.Member {
		http.Error(w, "This party is unavailable", http.StatusForbidden)
		return
	}
	// Neko's own control UI is locked; only Chat's lease may move the host.
	if (strings.HasPrefix(r.URL.Path, "/api/oauth") && !(r.URL.Path == "/api/oauth/config" && r.Method == http.MethodGet)) ||
		(strings.HasPrefix(r.URL.Path, "/api/room/control") && r.Method != http.MethodGet) {
		http.Error(w, "The party remote controls this desktop", http.StatusForbidden)
		return
	}
	display := subject
	for _, member := range a.Members {
		if member.Subject == subject && member.Username != "" {
			display = member.Username
			break
		}
	}
	s, err := p.session(r.Context(), subject, display)
	if err != nil {
		http.Error(w, "Desktop sign-in failed", http.StatusBadGateway)
		return
	}
	p.syncControl(r.Context(), a.Holder)
	if r.URL.Path == "/api/login" {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(s.login)
		return
	}
	proxy := httputil.NewSingleHostReverseProxy(p.neko)
	proxy.Director = func(out *http.Request) {
		out.URL.Scheme, out.URL.Host = p.neko.Scheme, p.neko.Host
		if out.URL.Path == "/ws" {
			q := out.URL.Query()
			q.Del("password")
			q.Del("username")
			out.URL.RawQuery = q.Encode()
		}
		out.Host = p.neko.Host
		out.Header.Del("Authorization")
		out.Header.Del("Cookie")
		out.Header.Del("X-Openvibe-Subject")
		out.Header.Del("X-Openvibe-User")
		out.Header.Set("Cookie", s.cookie)
	}
	proxy.ModifyResponse = func(res *http.Response) error { res.Header.Del("Set-Cookie"); return nil }
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		wsCtx, wsCancel := context.WithCancel(r.Context())
		defer wsCancel()
		go p.watchMembership(wsCtx, wsCancel, subject)
		r = r.WithContext(wsCtx)
	}
	proxy.ServeHTTP(w, r)
}

func (p *Proxy) watchMembership(ctx context.Context, cancel context.CancelFunc, subject string) {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		check, done := context.WithTimeout(ctx, 4*time.Second)
		a, err := p.access(check, subject)
		done()
		if err != nil || !a.Active || !a.Member {
			cancel()
			return
		}
		p.syncControl(ctx, a.Holder)
	}
}

func (p *Proxy) session(ctx context.Context, subject, display string) (nekoSession, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if s, ok := p.sessions[subject]; ok {
		if time.Since(s.checked) < 5*time.Second {
			return s, nil
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.neko.String()+"/api/whoami", nil)
		if err == nil {
			req.Header.Set("Cookie", s.cookie)
			if res, e := p.client.Do(req); e == nil {
				_ = res.Body.Close()
				if res.StatusCode == http.StatusOK {
					s.checked = time.Now()
					p.sessions[subject] = s
					return s, nil
				}
			}
		}
		delete(p.sessions, subject)
		if p.holder == subject {
			p.holder = ""
		}
	}
	body, _ := json.Marshal(map[string]string{"username": display, "password": p.plan.MemberPassword})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.neko.String()+"/api/login", bytes.NewReader(body))
	if err != nil {
		return nekoSession{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := p.client.Do(req)
	if err != nil {
		return nekoSession{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nekoSession{}, errors.New("neko refused member sign-in")
	}
	var out struct {
		ID string `json:"id"`
	}
	login, err := io.ReadAll(io.LimitReader(res.Body, 4096))
	if err != nil {
		return nekoSession{}, err
	}
	if err := json.Unmarshal(login, &out); err != nil {
		return nekoSession{}, err
	}
	for _, c := range res.Cookies() {
		if c.Name == "NEKO_SESSION" && c.Value != "" && out.ID != "" {
			s := nekoSession{cookie: c.Name + "=" + c.Value, id: out.ID, login: login, checked: time.Now()}
			p.sessions[subject] = s
			return s, nil
		}
	}
	return nekoSession{}, errors.New("neko did not set a session cookie")
}

func (p *Proxy) admin(ctx context.Context, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.neko.String()+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.plan.APIToken)
	res, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	if res.StatusCode != http.StatusNoContent {
		return errors.New("neko refused control change")
	}
	return nil
}

func (p *Proxy) syncControl(ctx context.Context, holder string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if holder == p.holder {
		return
	}
	controlCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	if err := p.admin(controlCtx, "/api/room/control/reset"); err != nil {
		return
	}
	p.holder = ""
	if holder == "" {
		return
	}
	s, ok := p.sessions[holder]
	if !ok {
		return
	}
	if err := p.admin(controlCtx, "/api/room/control/give/"+url.PathEscape(s.id)); err == nil {
		p.holder = holder
	}
}
