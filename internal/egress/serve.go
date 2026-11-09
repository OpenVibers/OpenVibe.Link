package egress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/OpenVibers/OpenVibe.Link/internal/connect"
	"github.com/OpenVibers/OpenVibe.Link/internal/credentials"
	"github.com/OpenVibers/OpenVibe.Link/internal/link"
)

// Serve exposes no public listening port. Connect owns the authenticated incoming byte pipe.
func Serve(ctx context.Context, creds *credentials.Credentials, stateDir string) error {
	if creds == nil || !creds.NetworkPaired() {
		return nil
	}
	budget, err := NewBudget(filepath.Join(stateDir, "link-egress-usage.json"))
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	client := NewClient(creds)
	server := &http.Server{Handler: &Endpoint{Client: client, Link: creds.Principal, Budget: budget, Pause: Paused}, ReadHeaderTimeout: 10 * time.Second, MaxHeaderBytes: 8 << 10}
	go server.Serve(listener)
	defer server.Close()
	networkToken, err := client.Tokens.Token(ctx)
	if err != nil {
		return errors.New("computer sign-in unavailable")
	}
	connectTokens := link.NewTokenSourceFor(creds, nil, "openvibe-link/egress", link.ConnectAudience)
	token, err := connectTokens.Token(ctx)
	if err != nil {
		return errors.New("computer sign-in unavailable")
	}
	data, _ := json.Marshal(map[string]any{"port": listener.Addr().(*net.TCPAddr).Port, "network_token": networkToken.Reveal()})
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://connect.openvibe.link/api/v1/link/egress-share", bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer "+token.Reveal())
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		return errors.New("Link connection unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return errors.New("Link connection refused")
	}
	var result struct {
		Share connect.LinkShare `json:"share"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 16<<10)).Decode(&result); err != nil {
		return errors.New("invalid Link connection")
	}
	var online atomic.Bool
	heartbeat := func() {
		if !online.Load() {
			return
		}
		relay := "wss://" + result.Share.Host + "/__ov_egress"
		_ = client.Call(ctx, "PUT", "/api/v1/link/self/egress/heartbeat", map[string]string{"relay_url": relay}, nil, true)
	}
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				heartbeat()
			}
		}
	}()
	return connect.Run(ctx, connect.Share{ID: result.Share.ID, Port: listener.Addr().(*net.TCPAddr).Port}, connect.Options{
		LocalHost: "127.0.0.1", LinkToken: func(ctx context.Context) (string, error) { t, e := connectTokens.Token(ctx); return t.Reveal(), e },
		OnReady: func(connect.Ready) { online.Store(true); go heartbeat() }, OnDisconnect: func() { online.Store(false) },
	})
}

// A transient service failure retries without taking down the person's Link. Errors contain no response bodies.
func Run(ctx context.Context, creds *credentials.Credentials, stateDir string) {
	for {
		_ = Serve(ctx, creds, stateDir)
		if creds == nil || !creds.NetworkPaired() {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(15 * time.Second):
		}
	}
}

func IsCloudDesk(name string) bool { return strings.HasPrefix(name, "desk-s") && len(name) == 22 }
