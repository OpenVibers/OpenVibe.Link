package main

// `openvibe-link exchange serve --model <id>`: serve a model that runs on this machine (Ollama, llama.cpp, vLLM …)
// as a seller offer on OpenVibe.Exchange. It listens on a local OpenAI-compatible port, forwards ONE model to the
// local server that actually runs it, and registers this machine's public https address through the Exchange's
// existing endpoint-seller flow (POST /api/v1/exchange/seller/offers) with the person's Network developer key. The
// Exchange reaches the public URL only through its egress guard and verifies the endpoint with canaries exactly as
// it verifies an OpenVibe Link machine; on Ctrl-C the offer is removed again.
//
// The local listener does not authenticate the Exchange: the Exchange's endpoint flow builds a fresh request and
// sends no buyer identity by design. Keep --public-url behind the person's own tunnel/reverse proxy, and bind
// --listen to loopback (the default) so only that tunnel reaches it.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const (
	exchangeDefaultURL      = "https://api.openvibe.exchange"
	exchangeDefaultUpstream = "http://127.0.0.1:11434/v1"
	exchangeDefaultListen   = "127.0.0.1:11435"
	exchangeMaxBody         = 2 * 1024 * 1024
	exchangeMaxAnswer       = 8 * 1024 * 1024
	exchangeTokenEnv        = "OPENVIBE_DEVELOPER_KEY"
)

const exchangeUsage = `usage:
  openvibe-link exchange serve --model <id> [flags]

Serve a local model as an OpenVibe.Exchange seller offer through the endpoint-seller flow.

  --model <id>            the model id buyers ask for (required), e.g. qwen3:32b
  --upstream URL          the local OpenAI-compatible server (default http://127.0.0.1:11434/v1, Ollama)
  --upstream-model <id>   the model name the upstream knows, when it differs from --model
  --listen ADDR           the local address to serve (default 127.0.0.1:11435)
  --public-url URL        the https base URL the Exchange calls (required unless --no-register), e.g.
                          https://gpu.example.com/v1; /chat/completions is appended
  --price-in N            credits per million input tokens (default 0: volunteer)
  --price-out N           credits per million output tokens (default 0)
  --price N               one price for both sides, unless --price-in/--price-out is given
  --concurrency N         calls in flight at once, 1-8 (default 2)
  --hours-start HH:MM     when the offer opens, UTC by default (default 00:00)
  --hours-end HH:MM       when it closes (default 23:59)
  --tz ZONE               the time zone of the hours (default UTC)
  --audience WHO          everyone, followers or group (default everyone)
  --group ID              the group, when --audience group
  --name NAME             a name for the offer
  --token-env NAME        where the person's Network developer key lives (default OPENVIBE_DEVELOPER_KEY)
  --exchange URL          the Exchange API origin (default https://api.openvibe.exchange)
  --no-register           serve locally only, without registering an offer

The command prints the local address and the registered offer id, then serves until Ctrl-C. It removes the offer on
exit. The developer key is read from the environment and is never printed.`

type exchangeServeOptions struct {
	Model         string
	Upstream      string
	UpstreamModel string
	Listen        string
	PublicURL     string
	Price         float64
	PriceIn       float64
	PriceOut      float64
	Concurrency   int
	HoursStart    string
	HoursEnd      string
	TZ            string
	Audience      string
	Group         string
	Name          string
	TokenEnv      string
	ExchangeURL   string
	NoRegister    bool
	UpstreamToken string // a local upstream's own key, from --upstream-token-env (never printed)

	// Ready is the tests' seam: called once the listener is up and the offer registered (empty when not registered).
	Ready func(localAddr, offerID string)
	// Client is the tests' seam for the Exchange and the upstream; nil uses a bounded default.
	Client *http.Client
}

type exchangeOfferAnswer struct {
	Offer struct {
		ID string `json:"id"`
	} `json:"offer"`
}

func cmdExchange(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "serve" {
		fmt.Fprintln(stderr, exchangeUsage)
		return 2
	}
	fs := flag.NewFlagSet("exchange serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var o exchangeServeOptions
	var upstreamTokenEnv string
	fs.StringVar(&o.Model, "model", "", "the model id buyers ask for")
	fs.StringVar(&o.Upstream, "upstream", exchangeDefaultUpstream, "the local OpenAI-compatible server")
	fs.StringVar(&o.UpstreamModel, "upstream-model", "", "the model name the upstream knows")
	fs.StringVar(&o.Listen, "listen", exchangeDefaultListen, "the local address to serve")
	fs.StringVar(&o.PublicURL, "public-url", "", "the public https base URL the Exchange calls")
	fs.Float64Var(&o.Price, "price", 0, "one price for both sides, credits per million tokens")
	fs.Float64Var(&o.PriceIn, "price-in", 0, "credits per million input tokens")
	fs.Float64Var(&o.PriceOut, "price-out", 0, "credits per million output tokens")
	fs.IntVar(&o.Concurrency, "concurrency", 2, "calls in flight at once (1-8)")
	fs.StringVar(&o.HoursStart, "hours-start", "00:00", "when the offer opens, HH:MM")
	fs.StringVar(&o.HoursEnd, "hours-end", "23:59", "when the offer closes, HH:MM")
	fs.StringVar(&o.TZ, "tz", "UTC", "the time zone of the hours")
	fs.StringVar(&o.Audience, "audience", "everyone", "everyone, followers or group")
	fs.StringVar(&o.Group, "group", "", "the group, when --audience group")
	fs.StringVar(&o.Name, "name", "", "a name for the offer")
	fs.StringVar(&o.TokenEnv, "token-env", exchangeTokenEnv, "where the Network developer key lives")
	fs.StringVar(&o.ExchangeURL, "exchange", exchangeDefaultURL, "the Exchange API origin")
	fs.BoolVar(&o.NoRegister, "no-register", false, "serve locally only")
	fs.StringVar(&upstreamTokenEnv, "upstream-token-env", "", "where the local upstream's key lives, when it needs one")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, exchangeUsage)
		return 2
	}
	if upstreamTokenEnv != "" {
		o.UpstreamToken = os.Getenv(upstreamTokenEnv)
		if o.UpstreamToken == "" {
			fmt.Fprintf(stderr, "error: %s is empty (the local upstream needs a key)\n", upstreamTokenEnv)
			return 1
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runExchangeServe(ctx, o, stdout); err != nil {
		if errors.Is(err, context.Canceled) {
			return 0
		}
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	return 0
}

func normalizeExchangeBase(raw, field string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("%s must be an http(s) URL", field)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("%s must be an http(s) URL", field)
	}
	if u.User != nil {
		return nil, fmt.Errorf("%s must not carry credentials", field)
	}
	return u, nil
}

// judgePublicURL applies the client side of the Exchange's own rule: https, a host name (never an IP literal or a
// private name), port 443 or 8443, no query or fragment. The Exchange judges it again; this fails fast and honestly.
func judgePublicURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, errors.New("--public-url must be an https URL")
	}
	if u.Scheme != "https" {
		return nil, errors.New("--public-url must be https")
	}
	if u.User != nil {
		return nil, errors.New("--public-url must not carry credentials")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("--public-url must not carry a query or fragment")
	}
	if p := u.Port(); p != "" && p != "443" && p != "8443" {
		return nil, errors.New("--public-url must use port 443 or 8443")
	}
	host := strings.ToLower(strings.Trim(u.Hostname(), "[]"))
	if host == "" {
		return nil, errors.New("--public-url must name a host")
	}
	if ip := net.ParseIP(host); ip != nil {
		return nil, errors.New("--public-url must name a host, not an IP address")
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") ||
		strings.HasSuffix(host, ".internal") || strings.HasSuffix(host, ".home.arpa") {
		return nil, errors.New("--public-url must be a public host name")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u, nil
}

func hhmm(s string) bool {
	if len(s) != 5 || s[2] != ':' {
		return false
	}
	for i, c := range s {
		if i == 2 {
			continue
		}
		if c < '0' || c > '9' {
			return false
		}
	}
	h := int(s[0]-'0')*10 + int(s[1]-'0')
	m := int(s[3]-'0')*10 + int(s[4]-'0')
	return h < 24 && m < 60
}

func (o *exchangeServeOptions) validate() error {
	if strings.TrimSpace(o.Model) == "" {
		return errors.New("--model is the model id buyers ask for (for example qwen3:32b)")
	}
	if o.UpstreamModel == "" {
		o.UpstreamModel = o.Model
	}
	if _, err := normalizeExchangeBase(o.Upstream, "--upstream"); err != nil {
		return err
	}
	o.Upstream = strings.TrimRight(o.Upstream, "/")
	if _, err := normalizeExchangeBase(o.ExchangeURL, "--exchange"); err != nil {
		return err
	}
	o.ExchangeURL = strings.TrimRight(o.ExchangeURL, "/")
	if _, err := net.ResolveTCPAddr("tcp", o.Listen); err != nil {
		return fmt.Errorf("--listen %q is not an address: %v", o.Listen, err)
	}
	if !o.NoRegister {
		if _, err := judgePublicURL(o.PublicURL); err != nil {
			return err
		}
		o.PublicURL = strings.TrimRight(o.PublicURL, "/")
		if strings.TrimSpace(os.Getenv(o.TokenEnv)) == "" {
			return fmt.Errorf("%s is not set: make a Network developer key and export it there (or use --no-register to serve locally only)", o.TokenEnv)
		}
	}
	if o.Price > 0 && o.PriceIn == 0 && o.PriceOut == 0 {
		o.PriceIn, o.PriceOut = o.Price, o.Price
	}
	if o.PriceIn < 0 || o.PriceOut < 0 {
		return errors.New("--price-in and --price-out cannot be negative")
	}
	if o.Concurrency < 1 || o.Concurrency > 8 {
		return errors.New("--concurrency is an integer from 1 to 8")
	}
	if !hhmm(o.HoursStart) || !hhmm(o.HoursEnd) {
		return errors.New("--hours-start and --hours-end are HH:MM")
	}
	if strings.TrimSpace(o.TZ) == "" || len(o.TZ) > 64 {
		return errors.New("--tz is a time zone name, at most 64 characters")
	}
	if o.Audience != "everyone" && o.Audience != "followers" && o.Audience != "group" {
		return errors.New("--audience is everyone, followers or group")
	}
	if o.Audience == "group" && strings.TrimSpace(o.Group) == "" {
		return errors.New("--audience group needs --group")
	}
	if len(o.Name) > 100 {
		return errors.New("--name is at most 100 characters")
	}
	return nil
}

func exchangeClient(o *exchangeServeOptions) *http.Client {
	if o.Client != nil {
		return o.Client
	}
	return &http.Client{Timeout: 60 * time.Second}
}

func exchangeToken(o *exchangeServeOptions) string { return strings.TrimSpace(os.Getenv(o.TokenEnv)) }

func registerExchangeOffer(ctx context.Context, o *exchangeServeOptions) (string, error) {
	price := map[string]float64{"in_per_million": o.PriceIn, "out_per_million": o.PriceOut}
	body := map[string]any{
		"base_url":    o.PublicURL,
		"models":      []string{o.Model},
		"price":       price,
		"hours":       map[string]string{"start": o.HoursStart, "end": o.HoursEnd, "tz": o.TZ},
		"concurrency": o.Concurrency,
		"audience":    o.Audience,
	}
	if o.Group != "" {
		body["group"] = o.Group
	}
	if o.Name != "" {
		body["name"] = o.Name
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.ExchangeURL+"/api/v1/exchange/seller/offers", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+exchangeToken(o))
	req.Header.Set("User-Agent", "openvibe-link/"+version)
	res, err := exchangeClient(o).Do(req)
	if err != nil {
		return "", fmt.Errorf("the Exchange could not be reached: %w", err)
	}
	defer res.Body.Close()
	answer, _ := io.ReadAll(io.LimitReader(res.Body, 64*1024))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("the Exchange answered HTTP %d: %s", res.StatusCode, exchangeDetail(answer))
	}
	var out exchangeOfferAnswer
	if err := json.Unmarshal(answer, &out); err != nil || out.Offer.ID == "" {
		return "", errors.New("the Exchange answered without an offer id")
	}
	return out.Offer.ID, nil
}

func removeExchangeOffer(ctx context.Context, o *exchangeServeOptions, id string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, o.ExchangeURL+"/api/v1/exchange/seller/offers/"+url.PathEscape(id), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+exchangeToken(o))
	req.Header.Set("User-Agent", "openvibe-link/"+version)
	res, err := exchangeClient(o).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	io.Copy(io.Discard, io.LimitReader(res.Body, 64*1024))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("the Exchange answered HTTP %d to the removal", res.StatusCode)
	}
	return nil
}

// exchangeDetail is the problem document's sentence, never the whole body and never a token (which the Exchange never
// sends anyway).
func exchangeDetail(body []byte) string {
	var p struct {
		Detail string `json:"detail"`
		Title  string `json:"title"`
		Code   string `json:"code"`
	}
	if json.Unmarshal(body, &p) == nil {
		for _, s := range []string{p.Detail, p.Title, p.Code} {
			if strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return strings.TrimSpace(string(body))[:min(len(strings.TrimSpace(string(body))), 300)]
}

func exchangeModels(o *exchangeServeOptions) []byte {
	out, _ := json.Marshal(map[string]any{
		"object": "list",
		"data": []map[string]any{{
			"id": o.Model, "object": "model", "created": time.Now().Unix(), "owned_by": "openvibe-link",
		}},
	})
	return out
}

func exchangeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": message, "type": "invalid_request_error"}})
}

func exchangeUpstreamPath(base string) string {
	u, err := url.Parse(base)
	if err != nil {
		return base + "/chat/completions"
	}
	if strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/v1") {
		u.Path = strings.TrimRight(u.Path, "/") + "/chat/completions"
	} else {
		u.Path = strings.TrimRight(u.Path, "/") + "/v1/chat/completions"
	}
	return u.String()
}

// exchangeChat forwards one completion to the local upstream. Only the registered model is served; the request is
// rebuilt around the model, the conversation, the output limit and the temperature, never the buyer's identity.
func exchangeChat(o *exchangeServeOptions, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		exchangeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, exchangeMaxBody+1))
	if err != nil {
		exchangeError(w, http.StatusBadRequest, "the request body could not be read")
		return
	}
	if len(raw) > exchangeMaxBody {
		exchangeError(w, http.StatusRequestEntityTooLarge, "the request body is too large")
		return
	}
	var asked struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.Unmarshal(raw, &asked); err != nil {
		exchangeError(w, http.StatusBadRequest, "the request is not JSON")
		return
	}
	if asked.Model != "" && asked.Model != o.Model && asked.Model != o.UpstreamModel {
		exchangeError(w, http.StatusNotFound, fmt.Sprintf("this node serves %s, not %s", o.Model, asked.Model))
		return
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil || body == nil {
		exchangeError(w, http.StatusBadRequest, "the request is not a JSON object")
		return
	}
	body["model"] = o.UpstreamModel
	body["stream"] = false // the endpoint-seller flow reads one answer, never a stream
	forwarded, _ := json.Marshal(body)

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, exchangeUpstreamPath(o.Upstream), bytes.NewReader(forwarded))
	if err != nil {
		exchangeError(w, http.StatusBadGateway, "the local model could not be called")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if o.UpstreamToken != "" {
		req.Header.Set("Authorization", "Bearer "+o.UpstreamToken)
	}
	res, err := exchangeClient(o).Do(req)
	if err != nil {
		exchangeError(w, http.StatusBadGateway, "the local model could not be reached")
		return
	}
	defer res.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(res.Body, exchangeMaxAnswer+1))
	if err != nil || len(answer) > exchangeMaxAnswer {
		exchangeError(w, http.StatusBadGateway, "the local model's answer could not be read")
		return
	}
	contentType := res.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(res.StatusCode)
	w.Write(answer)
}

func exchangeMux(o *exchangeServeOptions) *http.ServeMux {
	mux := http.NewServeMux()
	models := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			exchangeError(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(exchangeModels(o))
	}
	chat := func(w http.ResponseWriter, r *http.Request) { exchangeChat(o, w, r) }
	mux.HandleFunc("/v1/models", models)
	mux.HandleFunc("/v1/chat/completions", chat)
	// A --public-url without /v1 gets /chat/completions appended by the Exchange; accept that too.
	mux.HandleFunc("/models", models)
	mux.HandleFunc("/chat/completions", chat)
	return mux
}

// runExchangeServe is the command without the signal handling, so a test can cancel its context.
func runExchangeServe(ctx context.Context, o exchangeServeOptions, stdout io.Writer) error {
	if err := o.validate(); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", o.Listen)
	if err != nil {
		return fmt.Errorf("--listen %s: %w", o.Listen, err)
	}
	server := &http.Server{Handler: exchangeMux(&o), ReadHeaderTimeout: 10 * time.Second}
	served := make(chan error, 1)
	go func() { served <- server.Serve(ln) }()

	offerID := ""
	if !o.NoRegister {
		registerCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		offerID, err = registerExchangeOffer(registerCtx, &o)
		cancel()
		if err != nil {
			server.Close()
			return err
		}
	}
	fmt.Fprintf(stdout, "Serving %s on http://%s (upstream %s)\n", o.Model, ln.Addr().String(), o.Upstream)
	if offerID != "" {
		fmt.Fprintf(stdout, "Registered %s on the Exchange (%s) at %s\n", o.Model, o.ExchangeURL, o.PublicURL)
		fmt.Fprintf(stdout, "Offer %s; it is removed when this command stops.\n", offerID)
	}
	if o.Ready != nil {
		o.Ready(ln.Addr().String(), offerID)
	}

	select {
	case <-ctx.Done():
	case err := <-served:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server.Shutdown(shutdownCtx)
	if offerID != "" {
		removeCtx, cancelRemove := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancelRemove()
		if err := removeExchangeOffer(removeCtx, &o, offerID); err != nil {
			fmt.Fprintf(stdout, "The offer %s could not be removed: %v\n", offerID, err)
		} else {
			fmt.Fprintf(stdout, "Removed offer %s\n", offerID)
		}
	}
	return nil
}
