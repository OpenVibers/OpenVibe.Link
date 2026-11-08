// Package panel serves OpenVibe Link's own window: the static UI embedded here and the small JSON API it polls, on a
// loopback-only HTTP server with a per-run token.
//
// The UI is the one designed in internal/panel/ui (see its README for the JSON contract). Every /panel/api/* request
// must carry the token as X-Link-Panel and, when it sends an Origin, send this server's own origin; a request without
// the token or from another origin is refused. Static files need no token. No CORS header is ever written.
package panel

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/OpenVibers/OpenVibe.Node/internal/firstrun"
)

//go:embed ui/index.html ui/panel.css ui/panel.js
var assets embed.FS

// Assets is the UI as it ships in the binary: index.html, panel.css and panel.js. testdata and the README are not
// embedded (they are documentation and fixtures, not something to serve).
func Assets() fs.FS { return assets }

// ErrNotImplemented is what a handler returns for a request this version of Link cannot serve.
var ErrNotImplemented = errors.New("not implemented in this version of Link")

// Machine is what the window's header shows.
type Machine struct {
	// ID is the machine's own id (known after pairing), so "New task on this computer" can preselect it.
	ID       string `json:"id,omitempty"`
	Name     string `json:"name"`
	OS       string `json:"os,omitempty"`
	Online   bool   `json:"online"`
	Paused   bool   `json:"paused"`
	NeedsYou int    `json:"needs_you,omitempty"`
}

// Running is one thing the machine is doing: an agent, a desk, a share or a model.
type Running struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Title     string    `json:"title"`
	Detail    string    `json:"detail,omitempty"`
	StartedAt time.Time `json:"started_at,omitempty"`
}

// Capability is one permission switch: what this computer allows, for whom.
type Capability struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Who  string `json:"who,omitempty"`
	On   bool   `json:"on"`
	// Confirm is what the window asks, in plain words, before turning this switch on (Terminal and Desktop).
	Confirm string `json:"confirm,omitempty"`
}

// Share is one port put on the web through OpenVibe Connect.
type Share struct {
	Port      int       `json:"port"`
	URL       string    `json:"url,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}

// Device is one of the person's machines.
type Device struct {
	Name   string `json:"name"`
	Kind   string `json:"kind,omitempty"`
	Online bool   `json:"online"`
	Here   bool   `json:"here,omitempty"`
}

// Model is one local model offered by a GPU.
type Model struct {
	Name   string `json:"name"`
	TPS    int    `json:"tps,omitempty"`
	Loaded bool   `json:"loaded,omitempty"`
}

// GPU is the machine's graphics card and the models it serves.
type GPU struct {
	Name   string  `json:"name"`
	VRAMGB int     `json:"vram_gb,omitempty"`
	Models []Model `json:"models,omitempty"`
}

// Earnings is what this machine earned today.
type Earnings struct {
	Coins int `json:"coins"`
}

// Activity is one thing that happened on this machine, as the window lists it.
type Activity struct {
	At   time.Time `json:"at"`
	Text string    `json:"text"`
}

// CodesAgent is one coding-agent CLI on this computer: ready, needs signing in (Hint is the command), or missing.
type CodesAgent struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	State   string `json:"state"`
	Version string `json:"version,omitempty"`
	Hint    string `json:"hint,omitempty"`
}

// CodesTask is one Codes task that runs, or ran, on this computer.
type CodesTask struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Folder    string    `json:"folder,omitempty"`
	Agent     string    `json:"agent,omitempty"`
	State     string    `json:"state"`
	StartedAt time.Time `json:"started_at,omitempty"`
	URL       string    `json:"url,omitempty"`
}

// Codes is what the window's Codes section reads.
type Codes struct {
	Agents []CodesAgent `json:"agents,omitempty"`
	Tasks  []CodesTask  `json:"tasks,omitempty"`
}

// State is GET /panel/api/state. Every key is optional: a section with nothing in it is left out.
type State struct {
	Machine       *Machine     `json:"machine,omitempty"`
	Running       []Running    `json:"running,omitempty"`
	Capabilities  []Capability `json:"capabilities,omitempty"`
	Shares        []Share      `json:"shares,omitempty"`
	Devices       []Device     `json:"devices,omitempty"`
	GPU           *GPU         `json:"gpu,omitempty"`
	EarningsToday *Earnings    `json:"earnings_today,omitempty"`
	Activity      []Activity   `json:"activity,omitempty"`
	// Codes is the desktop app's Codes module (§13.1): the agent CLIs on this computer and the Codes tasks here.
	Codes *Codes `json:"codes,omitempty"`
	// FirstRun is true until the person has answered "What's this computer for?" or skipped it.
	FirstRun bool `json:"first_run,omitempty"`
}

// FirstRunAnswer is the body of POST /panel/api/first-run: the uses the person picked, or a skip that only stops the
// question.
type FirstRunAnswer struct {
	Uses []string `json:"uses"`
	Skip bool     `json:"skip"`
}

// SendMachine and IncomingSend are the Link Send payloads shown in the window. Envelope is ciphertext only.
type SendMachine struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Online    bool   `json:"online"`
	PublicKey any    `json:"public_key"`
}
type IncomingSend struct {
	ID        string    `json:"id"`
	Sender    string    `json:"sender_node,omitempty"`
	Envelope  string    `json:"envelope"`
	CreatedAt time.Time `json:"created_at"`
}

// Options are what the daemon gives the panel: its state, and the actions the window can ask for.
type Options struct {
	// Token is the per-run secret the page sends as X-Link-Panel. Required.
	Token string
	// State answers GET /panel/api/state.
	State func() State
	// Pause is the local kill switch, the same one the tray's Stop everything and Resume use.
	Pause func(paused bool) error
	// Stop stops one running thing by the id State used.
	Stop func(id string) error
	// SetCapability turns one capability switch on or off.
	SetCapability func(id string, on bool) error
	// Share shares a local port through Connect; Unshare ends that share.
	Share   func(port int) (Share, error)
	Unshare func(port int) error
	// FirstRun records the answer to "What's this computer for?" and turns on what each use says. A skip only stops
	// the question.
	FirstRun func(uses []string, skip bool) error
	// CodesStop stops the local Codes task with this id the way Codes' own stop does. It returns an error when the
	// task is not running on this computer.
	CodesStop    func(id string) error
	SendKey      func() any
	SendTargets  func() ([]SendMachine, error)
	SendFile     func(target, envelope string) error
	SendInbox    func() ([]IncomingSend, error)
	SendAccept   func(id string) (string, error)
	SendDecline  func(id string) error
	SendComplete func(id string) error
	Log          *slog.Logger
}

// Server is one running panel server.
type Server struct {
	ln     net.Listener
	origin string
	token  []byte
	opt    Options
}

// NewToken is a per-run panel token: 32 random bytes, base64url without padding.
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Listen binds the panel on 127.0.0.1 at a free port. It never binds another address.
func Listen(opt Options) (*Server, error) {
	if len(opt.Token) < 16 {
		return nil, errors.New("panel: a token of at least 16 characters is required")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	return &Server{ln: ln, origin: "http://" + ln.Addr().String(), token: []byte(opt.Token), opt: opt}, nil
}

// Addr is the address it listens on (127.0.0.1:port).
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Origin is the panel's own origin (http://127.0.0.1:port).
func (s *Server) Origin() string { return s.origin }

// URL is where to open the window: the static page with the token in its fragment, which browsers never send.
func (s *Server) URL() string { return s.origin + "/panel/#t=" + s.opt.Token }

// Serve answers requests until Close.
func (s *Server) Serve() error {
	err := http.Serve(s.ln, s.Handler())
	if errors.Is(err, net.ErrClosed) || errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Close stops the server.
func (s *Server) Close() error { return s.ln.Close() }

// Handler is the panel's routes: /panel/ for the files, /panel/api/ for the JSON.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/panel/api/", s.api())
	mux.Handle("/panel/", s.files())
	return mux
}

// files serves the three UI files. No token is needed (they are public code), and nothing else in the folder is
// reachable.
func (s *Server) files() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeJSON(w, http.StatusMethodNotAllowed, errBody("only GET and HEAD"))
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/panel/")
		typ := ""
		switch name {
		case "", "index.html":
			name, typ = "index.html", "text/html; charset=utf-8"
		case "panel.css":
			typ = "text/css; charset=utf-8"
		case "panel.js":
			typ = "text/javascript; charset=utf-8"
		default:
			http.NotFound(w, r)
			return
		}
		b, err := assets.ReadFile("ui/" + name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", typ)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(b)
	})
}

// api checks the token and origin before every call, then dispatches.
func (s *Server) api() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			writeJSON(w, http.StatusForbidden, errBody("forbidden"))
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 40<<20)
		path := strings.TrimPrefix(r.URL.Path, "/panel/api/")
		switch {
		case path == "state" && r.Method == http.MethodGet:
			st := State{}
			if s.opt.State != nil {
				st = s.opt.State()
			}
			writeJSON(w, http.StatusOK, st)
		case path == "send/key" && r.Method == http.MethodGet:
			if s.opt.SendKey == nil {
				writeJSON(w, http.StatusServiceUnavailable, errBody("receiving is not available on this machine"))
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"private_key": s.opt.SendKey()})
		case path == "send/targets" && r.Method == http.MethodGet:
			if s.opt.SendTargets == nil {
				writeJSON(w, http.StatusServiceUnavailable, errBody("sending is not available on this machine"))
				return
			}
			items, err := s.opt.SendTargets()
			if err != nil {
				writeJSON(w, http.StatusServiceUnavailable, errBody(err.Error()))
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"machines": items})
		case path == "send/inbox" && r.Method == http.MethodGet:
			if s.opt.SendInbox == nil {
				writeJSON(w, http.StatusServiceUnavailable, errBody("receiving is not available on this machine"))
				return
			}
			items, err := s.opt.SendInbox()
			if err != nil {
				writeJSON(w, http.StatusServiceUnavailable, errBody(err.Error()))
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"items": items})
		case path == "pause" && r.Method == http.MethodPost:
			var body struct {
				Paused *bool `json:"paused"`
			}
			if !decode(w, r, &body) || body.Paused == nil {
				writeJSON(w, http.StatusBadRequest, errBody("paused is true or false"))
				return
			}
			if s.opt.Pause == nil {
				writeJSON(w, http.StatusServiceUnavailable, errBody("the panel cannot pause this Node"))
				return
			}
			if err := s.opt.Pause(*body.Paused); err != nil {
				writeJSON(w, http.StatusConflict, errBody(err.Error()))
				return
			}
			writeJSON(w, http.StatusOK, map[string]bool{"paused": *body.Paused})
		case strings.HasPrefix(path, "running/") && strings.HasSuffix(path, "/stop") && r.Method == http.MethodPost:
			id := strings.TrimSuffix(strings.TrimPrefix(path, "running/"), "/stop")
			if id == "" || strings.Contains(id, "/") {
				http.NotFound(w, r)
				return
			}
			if s.opt.Stop == nil {
				writeJSON(w, http.StatusServiceUnavailable, errBody("the panel cannot stop this"))
				return
			}
			if err := s.opt.Stop(id); err != nil {
				writeJSON(w, http.StatusConflict, errBody(err.Error()))
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"stopped": id})
		case strings.HasPrefix(path, "codes/tasks/") && strings.HasSuffix(path, "/stop") && r.Method == http.MethodPost:
			id := strings.TrimSuffix(strings.TrimPrefix(path, "codes/tasks/"), "/stop")
			if id == "" || strings.Contains(id, "/") {
				http.NotFound(w, r)
				return
			}
			if s.opt.CodesStop == nil {
				writeJSON(w, http.StatusServiceUnavailable, errBody("the panel cannot stop this task"))
				return
			}
			if err := s.opt.CodesStop(id); err != nil {
				writeJSON(w, http.StatusConflict, errBody(err.Error()))
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"stopped": id})
		case strings.HasPrefix(path, "capabilities/") && r.Method == http.MethodPost:
			id := strings.TrimPrefix(path, "capabilities/")
			if id == "" || strings.Contains(id, "/") {
				http.NotFound(w, r)
				return
			}
			var body struct {
				On *bool `json:"on"`
			}
			if !decode(w, r, &body) || body.On == nil {
				writeJSON(w, http.StatusBadRequest, errBody("on is true or false"))
				return
			}
			if s.opt.SetCapability == nil {
				writeJSON(w, http.StatusServiceUnavailable, errBody("the panel cannot change this switch"))
				return
			}
			if err := s.opt.SetCapability(id, *body.On); err != nil {
				writeJSON(w, http.StatusConflict, errBody(err.Error()))
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"id": id, "on": *body.On})
		case path == "shares" && r.Method == http.MethodPost:
			var body struct {
				Port int `json:"port"`
			}
			if !decode(w, r, &body) || body.Port < 1 || body.Port > 65535 {
				writeJSON(w, http.StatusBadRequest, errBody("port is 1 to 65535"))
				return
			}
			if s.opt.Share == nil {
				writeJSON(w, http.StatusServiceUnavailable, errBody("the panel cannot share a port"))
				return
			}
			sh, err := s.opt.Share(body.Port)
			if err != nil {
				writeJSON(w, http.StatusConflict, errBody(err.Error()))
				return
			}
			writeJSON(w, http.StatusOK, sh)
		case strings.HasPrefix(path, "shares/") && r.Method == http.MethodDelete:
			port, err := strconv.Atoi(strings.TrimPrefix(path, "shares/"))
			if err != nil || port < 1 || port > 65535 {
				http.NotFound(w, r)
				return
			}
			if s.opt.Unshare == nil {
				writeJSON(w, http.StatusServiceUnavailable, errBody("the panel cannot end a share"))
				return
			}
			if err := s.opt.Unshare(port); err != nil {
				writeJSON(w, http.StatusConflict, errBody(err.Error()))
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case path == "first-run" && r.Method == http.MethodPost:
			var body FirstRunAnswer
			if !decode(w, r, &body) {
				writeJSON(w, http.StatusBadRequest, errBody("uses is a list of codes, browser, models, earn or vibe, and skip is true or false"))
				return
			}
			if !body.Skip {
				for _, use := range body.Uses {
					if !firstrun.ValidUse(use) {
						writeJSON(w, http.StatusBadRequest, errBody("no use "+strconv.Quote(use)+": uses are codes, browser, models, earn and vibe"))
						return
					}
				}
			}
			if s.opt.FirstRun == nil {
				writeJSON(w, http.StatusServiceUnavailable, errBody("the panel cannot record this answer"))
				return
			}
			if err := s.opt.FirstRun(body.Uses, body.Skip); err != nil {
				writeJSON(w, http.StatusConflict, errBody(err.Error()))
				return
			}
			writeJSON(w, http.StatusOK, map[string]bool{"first_run": false})
		case path == "files" && r.Method == http.MethodPost:
			var body struct {
				Target   string `json:"target_node"`
				Envelope string `json:"envelope"`
			}
			if !decode(w, r, &body) || body.Target == "" || body.Envelope == "" {
				writeJSON(w, http.StatusBadRequest, errBody("choose a machine and provide an encrypted envelope"))
				return
			}
			if s.opt.SendFile == nil {
				writeJSON(w, http.StatusServiceUnavailable, errBody("sending is not available on this machine"))
				return
			}
			if err := s.opt.SendFile(body.Target, body.Envelope); err != nil {
				writeJSON(w, http.StatusConflict, errBody(err.Error()))
				return
			}
			writeJSON(w, http.StatusCreated, map[string]bool{"sent": true})
		case strings.HasPrefix(path, "files/") && strings.HasSuffix(path, "/accept") && r.Method == http.MethodPost:
			id := strings.TrimSuffix(strings.TrimPrefix(path, "files/"), "/accept")
			if s.opt.SendAccept == nil {
				writeJSON(w, http.StatusServiceUnavailable, errBody("receiving is not available on this machine"))
				return
			}
			envelope, err := s.opt.SendAccept(id)
			if err != nil {
				writeJSON(w, http.StatusConflict, errBody(err.Error()))
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"envelope": envelope})
		case strings.HasPrefix(path, "files/") && strings.HasSuffix(path, "/decline") && r.Method == http.MethodPost:
			id := strings.TrimSuffix(strings.TrimPrefix(path, "files/"), "/decline")
			if s.opt.SendDecline == nil {
				writeJSON(w, http.StatusServiceUnavailable, errBody("receiving is not available on this machine"))
				return
			}
			if err := s.opt.SendDecline(id); err != nil {
				writeJSON(w, http.StatusConflict, errBody(err.Error()))
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case strings.HasPrefix(path, "files/") && strings.HasSuffix(path, "/complete") && r.Method == http.MethodPost:
			id := strings.TrimSuffix(strings.TrimPrefix(path, "files/"), "/complete")
			if s.opt.SendComplete == nil {
				writeJSON(w, http.StatusServiceUnavailable, errBody("receiving is not available on this machine"))
				return
			}
			if err := s.opt.SendComplete(id); err != nil {
				writeJSON(w, http.StatusConflict, errBody(err.Error()))
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})
}

// authorized: the token must match, and an Origin, when present, must be this server's own.
func (s *Server) authorized(r *http.Request) bool {
	got := []byte(r.Header.Get("X-Link-Panel"))
	if subtle.ConstantTimeCompare(got, s.token) != 1 {
		return false
	}
	if o := r.Header.Get("Origin"); o != "" && o != s.origin {
		return false
	}
	return true
}

func decode(w http.ResponseWriter, r *http.Request, out any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, 40<<20))
	dec.DisallowUnknownFields()
	return dec.Decode(out) == nil
}

func errBody(msg string) map[string]string { return map[string]string{"error": msg} }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
