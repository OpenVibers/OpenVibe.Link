package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// `openvibe-link exchange serve`: the seller offer is registered through the endpoint-seller flow with the person's
// developer key, the local model is served without its own auth, the model the buyer names is rewritten to the one the
// local upstream knows, and Ctrl-C removes the offer again.
func TestExchangeServeRegistersForwardsAndRemoves(t *testing.T) {
	var mu sync.Mutex
	var registered map[string]any
	var registerAuth, removedPath, removedAuth string
	exchange := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/exchange/seller/offers":
			registerAuth = r.Header.Get("Authorization")
			json.NewDecoder(r.Body).Decode(&registered)
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{"offer":{"id":"ep_test"}}`)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/v1/exchange/seller/offers/"):
			removedPath = r.URL.Path
			removedAuth = r.Header.Get("Authorization")
			io.WriteString(w, `{"id":"ep_test","removed":true}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"detail":"not found"}`)
		}
	}))
	defer exchange.Close()

	var upstreamModel string
	var upstreamStream bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("the upstream path: %s", r.URL.Path)
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		upstreamModel, _ = body["model"].(string)
		upstreamStream, _ = body["stream"].(bool)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"chatcmpl_1","choices":[{"index":0,"message":{"role":"assistant","content":"local answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`)
	}))
	defer upstream.Close()

	t.Setenv("OV_TEST_EXCHANGE_TOKEN", "dev-token")
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- runExchangeServe(ctx, exchangeServeOptions{
			Model: "qwen3:32b", Upstream: upstream.URL, UpstreamModel: "qwen3-upstream",
			Listen: "127.0.0.1:0", PublicURL: "https://seller.example/v1",
			PriceIn: 10, PriceOut: 30, Concurrency: 2, HoursStart: "00:00", HoursEnd: "23:59", TZ: "UTC",
			Audience: "everyone", Name: "test box", TokenEnv: "OV_TEST_EXCHANGE_TOKEN", ExchangeURL: exchange.URL,
			Ready: func(addr, _ string) { ready <- addr },
		}, io.Discard)
	}()

	var addr string
	select {
	case addr = <-ready:
	case err := <-done:
		t.Fatalf("serve stopped before it was ready: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not become ready")
	}

	// The model list is the one model this node sells.
	res, err := http.Get("http://" + addr + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	var models struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	json.NewDecoder(res.Body).Decode(&models)
	res.Body.Close()
	if len(models.Data) != 1 || models.Data[0].ID != "qwen3:32b" {
		t.Fatalf("models: %+v", models)
	}

	// One completion: the buyer's model is accepted, rewritten for the upstream, and stream is forced off.
	res, err = http.Post("http://"+addr+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"qwen3:32b","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	answer, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.Contains(string(answer), "local answer") {
		t.Fatalf("answer %d %s", res.StatusCode, answer)
	}
	if upstreamModel != "qwen3-upstream" {
		t.Fatalf("upstream model %q", upstreamModel)
	}
	if upstreamStream {
		t.Fatal("stream was not forced off")
	}

	// A model this node does not sell is refused before the upstream sees it.
	res, err = http.Post("http://"+addr+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"other/model","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("another model: %d", res.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not stop")
	}

	mu.Lock()
	defer mu.Unlock()
	if registerAuth != "Bearer dev-token" {
		t.Fatalf("registration auth: %q", registerAuth)
	}
	if removedPath != "/api/v1/exchange/seller/offers/ep_test" || removedAuth != "Bearer dev-token" {
		t.Fatalf("removal %s %q", removedPath, removedAuth)
	}
	if registered["base_url"] != "https://seller.example/v1" {
		t.Fatalf("base_url: %v", registered["base_url"])
	}
	if m, _ := registered["models"].([]any); len(m) != 1 || m[0] != "qwen3:32b" {
		t.Fatalf("models: %v", registered["models"])
	}
	price, _ := registered["price"].(map[string]any)
	if price["in_per_million"] != float64(10) || price["out_per_million"] != float64(30) {
		t.Fatalf("price: %v", registered["price"])
	}
	if registered["concurrency"] != float64(2) || registered["audience"] != "everyone" {
		t.Fatalf("offer: %v", registered)
	}
	hours, _ := registered["hours"].(map[string]any)
	if hours["start"] != "00:00" || hours["end"] != "23:59" || hours["tz"] != "UTC" {
		t.Fatalf("hours: %v", registered["hours"])
	}
}

func TestExchangeServeRefusesAPrivateOrIPPublicURL(t *testing.T) {
	t.Setenv("OV_TEST_EXCHANGE_TOKEN", "dev-token")
	for _, pub := range []string{"http://seller.example/v1", "https://127.0.0.1/v1", "https://[::1]/v1", "https://localhost/v1", "https://seller.example:8080/v1"} {
		err := runExchangeServe(context.Background(), exchangeServeOptions{
			Model: "qwen3:32b", Upstream: "http://127.0.0.1:11434/v1", Listen: "127.0.0.1:0",
			PublicURL: pub, Concurrency: 2, HoursStart: "00:00", HoursEnd: "23:59", TZ: "UTC",
			Audience: "everyone", TokenEnv: "OV_TEST_EXCHANGE_TOKEN", ExchangeURL: "http://127.0.0.1:1",
		}, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "public-url") {
			t.Fatalf("%s: %v", pub, err)
		}
	}
}

func TestExchangeServeRefusesARefusedDeveloperKey(t *testing.T) {
	exchange := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"detail":"the developer key was refused"}`)
	}))
	defer exchange.Close()
	t.Setenv("OV_TEST_EXCHANGE_TOKEN", "wrong-token")
	err := runExchangeServe(context.Background(), exchangeServeOptions{
		Model: "qwen3:32b", Upstream: "http://127.0.0.1:11434/v1", Listen: "127.0.0.1:0",
		PublicURL: "https://seller.example/v1", Concurrency: 2, HoursStart: "00:00", HoursEnd: "23:59", TZ: "UTC",
		Audience: "everyone", TokenEnv: "OV_TEST_EXCHANGE_TOKEN", ExchangeURL: exchange.URL,
	}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") || !strings.Contains(err.Error(), "developer key was refused") {
		t.Fatalf("a refused key: %v", err)
	}
	if strings.Contains(err.Error(), "wrong-token") {
		t.Fatal("the token is in the error")
	}
}

func TestExchangeUsage(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"exchange"}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "exchange serve --model") {
		t.Fatalf("usage: %d %q", code, errb.String())
	}
	if code := run([]string{"exchange", "serve"}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "--model") {
		t.Fatalf("no model: %d %q", code, errb.String())
	}
}
