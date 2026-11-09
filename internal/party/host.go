package party

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/OpenVibers/OpenVibe.Link/internal/connect"
)

func Host(ctx context.Context, client Client, version string, stdout io.Writer) error {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	proxyPort := listener.Addr().(*net.TCPAddr).Port
	nekoPort, err := FreePort()
	if err != nil {
		return err
	}
	for nekoPort == proxyPort {
		nekoPort, err = FreePort()
		if err != nil {
			return err
		}
	}
	runtime, err := FindRuntime()
	if err != nil {
		return err
	}
	claim, err := client.Claim(ctx, proxyPort)
	if err != nil {
		return err
	}
	defer func() {
		clean, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = client.Stopped(clean)
	}()
	plan, err := NewPlan(client.PartyID, nekoPort, claim.ICEServers)
	if err != nil {
		return err
	}
	stop, err := Start(ctx, runtime, plan)
	if err != nil {
		return err
	}
	defer func() {
		clean, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = stop(clean)
	}()
	readyCtx, readyCancel := context.WithTimeout(ctx, 45*time.Second)
	err = WaitReady(readyCtx, nekoPort)
	readyCancel()
	if err != nil {
		return fmt.Errorf("shared browser did not start: %w", err)
	}
	proxy := NewProxy(plan, nekoPort, client.Access)
	server := &http.Server{Handler: proxy, ReadHeaderTimeout: 8 * time.Second}
	go func() { _ = server.Serve(listener) }()
	defer func() {
		clean, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(clean)
	}()
	share, err := client.CreateShare(ctx, claim.ShareGrant)
	if err != nil {
		return err
	}
	defer func() {
		clean, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = client.CloseShare(clean, share)
	}()
	access, err := client.Access(ctx, "")
	if err != nil || !access.Active {
		return errors.New("party ended before the desktop was ready")
	}
	if err := client.SyncMembers(ctx, share, access); err != nil {
		return err
	}
	tunnelCtx, stopTunnel := context.WithCancel(ctx)
	defer stopTunnel()
	connected := make(chan struct{}, 1)
	tunnelErr := make(chan error, 1)
	go func() {
		tunnelErr <- connect.Run(tunnelCtx, connect.Share{ID: share.ID, Port: proxyPort, Server: client.ConnectServer}, connect.Options{
			Version: version, LinkToken: client.ConnectToken, OnReady: func(connect.Ready) {
				select {
				case connected <- struct{}{}:
				default:
				}
			},
		})
	}()
	select {
	case <-connected:
	case err := <-tunnelErr:
		return err
	case <-time.After(40 * time.Second):
		return errors.New("Connect did not open the party share")
	case <-ctx.Done():
		return nil
	}
	if err := client.Ready(ctx, share); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Hosting %s at %s. Stop with Ctrl-C or end the party.\n", client.PartyID, share.URL)
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	lastMembers := ""
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-tunnelErr:
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		case <-tick.C:
			check, cancel := context.WithTimeout(ctx, 10*time.Second)
			a, err := client.Access(check, "")
			if err == nil && !a.Active {
				cancel()
				return nil
			}
			if err == nil {
				var names []string
				for _, m := range a.Members {
					names = append(names, m.Username)
				}
				key := strings.Join(names, "\x00")
				if key != lastMembers && client.SyncMembers(check, share, a) == nil {
					lastMembers = key
				}
			}
			cancel()
		}
	}
}
