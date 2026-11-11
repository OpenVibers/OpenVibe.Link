package fakenetwork

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/OpenVibers/OpenVibe.Link/internal/access"
)

const fakeOwner = "usr_01J8Z4M2Q0R7T9YV3K6N8P1W2X"

type fakeAccessRequest struct {
	ID              string   `json:"id"`
	FromPrincipal   string   `json:"from_principal"`
	ToPrincipal     string   `json:"to_principal"`
	FromOwner       string   `json:"from_owner"`
	ToOwner         string   `json:"to_owner"`
	FromAgent       string   `json:"from_agent,omitempty"`
	FromMachineName string   `json:"from_machine_name,omitempty"`
	FromOwnerName   string   `json:"from_owner_name,omitempty"`
	FromAgentName   string   `json:"from_agent_name,omitempty"`
	Scopes          []string `json:"scopes"`
	Reason          string   `json:"reason,omitempty"`
	Status          string   `json:"status"`
	RequestedAt     string   `json:"requested_at"`
	ExpiresAt       string   `json:"expires_at"`
	DurationSeconds int64    `json:"duration_seconds"`
}

type fakeAccessGrant struct {
	ID            string   `json:"id"`
	RequestID     string   `json:"request_id"`
	FromPrincipal string   `json:"from_principal"`
	ToPrincipal   string   `json:"to_principal"`
	Scopes        []string `json:"scopes"`
	ApprovedBy    string   `json:"approved_by"`
	CreatedAt     string   `json:"created_at"`
	ExpiresAt     *string  `json:"expires_at"`
	RevokedAt     *string  `json:"revoked_at"`
}

// GrantActive is fake Bot's live Network authorization on each relayed frame.
func (s *Server) GrantActive(id, from, to, scope string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.grants[id]
	if g == nil || g.FromPrincipal != from || g.ToPrincipal != to || g.RevokedAt != nil {
		return false
	}
	if g.ExpiresAt != nil {
		at, err := time.Parse(time.RFC3339, *g.ExpiresAt)
		if err != nil || !at.After(time.Now()) {
			return false
		}
	}
	for _, v := range g.Scopes {
		if v == scope {
			return true
		}
	}
	return false
}

func (s *Server) registerAccessRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/.well-known/jwks", s.jwks)
	mux.HandleFunc("/api/v1/link/self/access-requests", s.accessRequests)
	mux.HandleFunc("/api/v1/link/self/access-requests/", s.accessRequestAction)
	mux.HandleFunc("/api/v1/link/self/access-grants", s.accessGrants)
	mux.HandleFunc("/api/v1/link/self/access-grants/", s.accessGrantAction)
	mux.HandleFunc("/api/v1/link/self/machines", s.accessMachines)
}

func (s *Server) accessPrincipal(r *http.Request) string {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tokenAudience[token] != "openvibe.network" {
		return ""
	}
	return s.tokenOwners[token]
}

func (s *Server) jwks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(405)
		return
	}
	if s.signing == nil {
		w.WriteHeader(503)
		return
	}
	pub := s.signing.PublicKey
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{
		"kid": "fake-network-1", "kty": "RSA", "alg": "RS256", "use": "sig",
		"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1}),
	}}})
}

func (s *Server) accessRequests(w http.ResponseWriter, r *http.Request) {
	principal := s.accessPrincipal(r)
	if principal == "" {
		problem(w, 401, "access.node_required")
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.mu.Lock()
		list := []fakeAccessRequest{}
		for _, v := range s.requests {
			if v.FromPrincipal == principal || v.ToPrincipal == principal {
				list = append(list, *v)
			}
		}
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"requests": list})
	case http.MethodPost:
		var body struct {
			Machine   string   `json:"machine"`
			Scopes    []string `json:"scopes"`
			Duration  int64    `json:"duration_seconds"`
			Reason    string   `json:"reason"`
			FromAgent string   `json:"from_agent"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Machine == principal || body.Duration < 0 || body.Duration > 30*86400 || len(body.Scopes) == 0 {
			problem(w, 400, "access.invalid_request")
			return
		}
		for _, scope := range body.Scopes {
			if !access.ValidScope(scope) {
				problem(w, 400, "access.invalid_scope")
				return
			}
			if body.Duration == 0 && scope != "agent" && scope != "send" {
				problem(w, 400, "access.forever_scope")
				return
			}
		}
		s.mu.Lock()
		_, ok := s.machines[body.Machine]
		if !ok {
			s.mu.Unlock()
			problem(w, 404, "access.machine_not_found")
			return
		}
		now := time.Now().UTC()
		req := &fakeAccessRequest{ID: "req_" + random()[:16], FromPrincipal: principal, ToPrincipal: body.Machine,
			FromOwner: fakeOwner, ToOwner: fakeOwner, FromAgent: body.FromAgent, Scopes: body.Scopes, Reason: body.Reason, Status: "pending",
			FromMachineName: "laptop", FromOwnerName: "Alex",
			RequestedAt: now.Format(time.RFC3339), ExpiresAt: now.Add(24 * time.Hour).Format(time.RFC3339), DurationSeconds: body.Duration}
		s.requests[req.ID] = req
		s.mu.Unlock()
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(req)
	default:
		w.WriteHeader(405)
	}
}

func (s *Server) accessRequestAction(w http.ResponseWriter, r *http.Request) {
	principal := s.accessPrincipal(r)
	if principal == "" {
		problem(w, 401, "access.node_required")
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/link/self/access-requests/"), "/")
	if r.Method != http.MethodPost || len(parts) != 2 || parts[1] != "answer" {
		w.WriteHeader(404)
		return
	}
	var body struct {
		Approved bool     `json:"approved"`
		Scopes   []string `json:"scopes"`
		Duration int64    `json:"duration_seconds"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		problem(w, 400, "access.invalid_answer")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	req := s.requests[parts[0]]
	if req == nil || req.ToPrincipal != principal || req.Status != "pending" {
		problem(w, 403, "access.not_pending")
		return
	}
	if !body.Approved {
		req.Status = "denied"
		_ = json.NewEncoder(w).Encode(req)
		return
	}
	if len(body.Scopes) == 0 {
		body.Scopes = req.Scopes
	}
	allowed := map[string]bool{}
	for _, scope := range req.Scopes {
		allowed[scope] = true
	}
	for _, scope := range body.Scopes {
		if !allowed[scope] {
			problem(w, 400, "access.cannot_widen")
			return
		}
	}
	if body.Duration == 0 {
		body.Duration = req.DurationSeconds
	}
	if body.Duration > 30*86400 || (req.DurationSeconds > 0 && body.Duration > req.DurationSeconds) {
		problem(w, 400, "access.cannot_extend")
		return
	}
	req.Status = "approved"
	now := time.Now().UTC()
	var expiry *string
	if body.Duration > 0 {
		value := now.Add(time.Duration(body.Duration) * time.Second).Format(time.RFC3339)
		expiry = &value
	}
	grant := &fakeAccessGrant{ID: "gra_" + random()[:16], RequestID: req.ID, FromPrincipal: req.FromPrincipal, ToPrincipal: req.ToPrincipal,
		Scopes: body.Scopes, ApprovedBy: fakeOwner, CreatedAt: now.Format(time.RFC3339), ExpiresAt: expiry}
	s.grants[grant.ID] = grant
	_ = json.NewEncoder(w).Encode(map[string]any{"id": req.ID, "status": req.Status, "grant_id": grant.ID})
}

func (s *Server) accessGrants(w http.ResponseWriter, r *http.Request) {
	principal := s.accessPrincipal(r)
	if principal == "" {
		problem(w, 401, "access.node_required")
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(405)
		return
	}
	s.mu.Lock()
	list := []map[string]any{}
	for _, g := range s.grants {
		if g.FromPrincipal != principal && g.ToPrincipal != principal {
			continue
		}
		raw, _ := json.Marshal(g)
		var row map[string]any
		_ = json.Unmarshal(raw, &row)
		if g.FromPrincipal == principal && g.RevokedAt == nil {
			row["grant_token"] = s.grantToken(g)
		}
		list = append(list, row)
	}
	s.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{"grants": list})
}

func (s *Server) grantToken(g *fakeAccessGrant) string {
	if s.signing == nil {
		return ""
	}
	until := time.Now().Add(time.Hour).Unix()
	if g.ExpiresAt != nil {
		if t, err := time.Parse(time.RFC3339, *g.ExpiresAt); err == nil {
			until = t.Unix()
		}
	}
	c := access.Claims{ID: g.ID, JTI: "jti_" + random()[:16], Issuer: s.URL(), From: g.FromPrincipal, To: g.ToPrincipal, Scopes: g.Scopes, Issued: time.Now().Unix(), Expiry: until}
	h, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "fake-network-1", "typ": "JWT"})
	p, _ := json.Marshal(c)
	message := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(p)
	hash := sha256.Sum256([]byte(message))
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.signing, crypto.SHA256, hash[:])
	if err != nil {
		return ""
	}
	return message + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func (s *Server) accessGrantAction(w http.ResponseWriter, r *http.Request) {
	principal := s.accessPrincipal(r)
	if principal == "" {
		problem(w, 401, "access.node_required")
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/link/self/access-grants/"), "/")
	if r.Method != http.MethodPost || len(parts) != 2 || parts[1] != "revoke" {
		w.WriteHeader(404)
		return
	}
	s.mu.Lock()
	g := s.grants[parts[0]]
	if g == nil || (g.FromPrincipal != principal && g.ToPrincipal != principal) {
		s.mu.Unlock()
		problem(w, 403, "access.not_allowed")
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	g.RevokedAt = &now
	id, to := g.ID, g.ToPrincipal
	s.mu.Unlock()
	if s.bot != nil {
		s.bot.RevokeGrant(id, to)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "status": "revoked"})
}

func (s *Server) accessMachines(w http.ResponseWriter, r *http.Request) {
	if s.accessPrincipal(r) == "" {
		problem(w, 401, "access.node_required")
		return
	}
	s.mu.Lock()
	list := []map[string]any{}
	for _, m := range s.machines {
		list = append(list, map[string]any{"id": m.Principal, "name": m.Principal})
	}
	s.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{"machines": list})
}
