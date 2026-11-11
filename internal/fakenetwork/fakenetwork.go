// Package fakenetwork is an in-process stand-in for OpenVibe.Network's node-principal pairing, for tests: POST
// /api/v1/link-pairing redeems a one-time code for a node principal and its credential, and POST /oauth/token
// (client_credentials) sells node tokens for that credential (server/registry/node-principals.js,
// server/auth/oauth-routes.js). Each token it mints is registered on the fake Bot as a credential of DeviceID, so the
// Bot fake accepts it on /device and POST /api/v1/devices/bind as Bot accepts a node token.
package fakenetwork

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	"github.com/OpenVibers/OpenVibe.Link/internal/fakebot"
	"github.com/OpenVibers/OpenVibe.Link/internal/protocol"
)

// Principal and PairingID are what a redeemed code answers with; DeviceID is the device the fake Bot binds it to.
const (
	Principal = "lnk_01J8Z4M2Q0R7T9YV3K6N8P1W2X"
	PairingID = "pair_01J8Z4M2Q0R7T9YV3K6N8P1W2Y"
	DeviceID  = "dev_net01"
)

// Server is the fake Network.
type Server struct {
	HTTP *httptest.Server
	// TTL is the expires_in of every token (seconds; default 300).
	TTL int

	bot *fakebot.Server

	mu            sync.Mutex
	health        int             // GET /api/health status; 200 unless a test sets it
	codes         map[string]bool // normalized code → live
	credential    string          // the node credential, once paired
	revoked       bool
	tokens        []string
	pairings      []protocol.LinkPairingRequest
	tokenForms    []map[string]string
	machines      map[string]fakeMachine
	codeMachines  map[string]fakeMachine
	tokenOwners   map[string]string
	tokenAudience map[string]string
	requests      map[string]*fakeAccessRequest
	grants        map[string]*fakeAccessGrant
	agents        map[string]*fakeAgent
	agentTasks    map[string]*fakeAgentTask
	signing       *rsa.PrivateKey
}

type fakeMachine struct{ Principal, DeviceID, Credential string }

// New starts the fake Network; bot (may be nil) learns every token it mints.
func New(bot *fakebot.Server) *Server {
	s := &Server{TTL: 300, bot: bot, codes: map[string]bool{}, health: http.StatusOK,
		machines: map[string]fakeMachine{}, codeMachines: map[string]fakeMachine{}, tokenOwners: map[string]string{}, tokenAudience: map[string]string{},
		requests: map[string]*fakeAccessRequest{}, grants: map[string]*fakeAccessGrant{}, agents: map[string]*fakeAgent{}, agentTasks: map[string]*fakeAgentTask{}}
	s.signing, _ = rsa.GenerateKey(rand.Reader, 2048)
	mux := http.NewServeMux()
	mux.HandleFunc(protocol.LinkPairingPath, s.pair)
	mux.HandleFunc(protocol.TokenPath, s.token)
	s.registerAccessRoutes(mux)
	s.registerAgentRoutes(mux)
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		status := s.health
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	s.HTTP = httptest.NewServer(mux)
	if bot != nil {
		bot.SetRelayAuthority(s.URL(), s.GrantActive)
	}
	return s
}

func (s *Server) Close()      { s.HTTP.Close() }
func (s *Server) URL() string { return s.HTTP.URL }

// SetHealth makes the fake /api/health answer with status (the guide's self-check reads it).
func (s *Server) SetHealth(status int) {
	s.mu.Lock()
	s.health = status
	s.mu.Unlock()
}

// AddCode makes a one-time code (normalized, without the dash) live for PairingID.
func (s *Server) AddCode(code string) {
	s.mu.Lock()
	s.codes[code] = true
	s.mu.Unlock()
}

// AddMachine pairs a second fake Link with the same Network and Bot.
func (s *Server) AddMachine(code, principal, deviceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.codes[code] = true
	s.codeMachines[code] = fakeMachine{Principal: principal, DeviceID: deviceID}
	if s.bot != nil {
		s.bot.BindMachine(principal, deviceID)
	}
}

// Revoke makes /oauth/token refuse the node credential (401 invalid_client), as a revoked principal is.
func (s *Server) Revoke() {
	s.mu.Lock()
	s.revoked = true
	s.mu.Unlock()
}

// Credential is the node credential the last pairing handed out.
func (s *Server) Credential() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.credential
}

// Tokens are the node tokens minted so far, in order.
func (s *Server) Tokens() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.tokens...)
}

// Pairings are the node-pairing request bodies received.
func (s *Server) Pairings() []protocol.LinkPairingRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]protocol.LinkPairingRequest(nil), s.pairings...)
}

// TokenForms are the /oauth/token form bodies received (client_secret included).
func (s *Server) TokenForms() []map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]string(nil), s.tokenForms...)
}

func random() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func problem(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "code": code, "title": http.StatusText(status), "detail": code})
}

func (s *Server) pair(w http.ResponseWriter, r *http.Request) {
	var req protocol.LinkPairingRequest
	if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&req) != nil {
		problem(w, http.StatusBadRequest, "registry.invalid_pairing_request")
		return
	}
	code := strings.ToUpper(strings.ReplaceAll(req.Code, "-", ""))
	s.mu.Lock()
	s.pairings = append(s.pairings, req)
	ok := s.codes[code] && (req.Pairing == "" || req.Pairing == PairingID)
	machine, defined := s.codeMachines[code]
	if !defined {
		machine = fakeMachine{Principal: Principal, DeviceID: DeviceID}
	}
	if ok {
		delete(s.codes, code)
		s.credential, s.revoked = random(), false
		machine.Credential = s.credential
		s.machines[machine.Principal] = machine
	}
	cred := s.credential
	s.mu.Unlock()
	if !ok {
		problem(w, http.StatusForbidden, "registry.pairing_code_invalid")
		return
	}
	if s.bot != nil {
		s.bot.BindMachine(machine.Principal, machine.DeviceID)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(protocol.LinkPairingResponse{Principal: machine.Principal, LinkID: "n-01j8z4m2q0r7t9yv3k6n8p1w2x",
		HomeCell: "eu-1", Credential: cred, TokenEndpoint: s.HTTP.URL + protocol.TokenPath,
		PairedFor: &protocol.PairedFor{Service: "bot", Ref: fakebot.RobotID}})
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.ParseForm() != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	form := map[string]string{}
	for k := range r.PostForm {
		form[k] = r.PostForm.Get(k)
	}
	s.mu.Lock()
	s.tokenForms = append(s.tokenForms, form)
	machine, exists := s.machines[form["client_id"]]
	good := !s.revoked && exists && machine.Credential != "" && form["grant_type"] == "client_credentials" &&
		form["client_secret"] == machine.Credential
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if !good {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(protocol.OAuthError{Error: "invalid_client", Description: "Invalid client credentials"})
		return
	}
	if form["audience"] != "openvibe.bot" && form["audience"] != "openvibe.network" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(protocol.OAuthError{Error: "invalid_scope", Description: "a node token is for openvibe.network or openvibe.bot"})
		return
	}
	tok := "eyJ" + random()
	s.mu.Lock()
	s.tokens = append(s.tokens, tok)
	s.tokenOwners[tok] = machine.Principal
	s.tokenAudience[tok] = form["audience"]
	s.mu.Unlock()
	if s.bot != nil && form["audience"] == "openvibe.bot" {
		s.bot.AddCredential(tok, machine.DeviceID)
	}
	_ = json.NewEncoder(w).Encode(protocol.TokenResponse{AccessToken: tok, TokenType: "Bearer", ExpiresIn: s.TTL})
}
