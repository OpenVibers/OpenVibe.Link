package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/OpenVibers/OpenVibe.Node/internal/config"
	"github.com/OpenVibers/OpenVibe.Node/internal/connect"
	"github.com/OpenVibers/OpenVibe.Node/internal/credentials"
	"github.com/OpenVibers/OpenVibe.Node/internal/link"
	"github.com/OpenVibers/OpenVibe.Node/internal/localctl"
	"github.com/OpenVibers/OpenVibe.Node/internal/party"
)

func cmdParty(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "host" {
		return errors.New("usage: openvibe-link party host <party-id> [--home DIR]")
	}
	fs := flag.NewFlagSet("party host", flag.ContinueOnError)
	fs.SetOutput(stderr)
	home := fs.String("home", "", "Link state directory")
	partyURL := fs.String("party-url", "https://openvibe.party", "party site (local testing only)")
	connectServer := fs.String("connect-server", connect.DefaultServer, "Connect tunnel")
	// A party id comes before flags in the published command form.
	if len(args) < 2 {
		return errors.New("usage: openvibe-link party host <party-id> [--home DIR]")
	}
	id := args[1]
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	if len(fs.Args()) != 0 {
		return errors.New("unexpected party arguments")
	}
	paths := config.DefaultPaths(*home)
	if *partyURL == "https://openvibe.party" && *connectServer == connect.DefaultServer {
		if response, err := localctl.Call(paths.SocketPath(), localctl.Request{Cmd: localctl.CmdPartyHost, ID: id}, 2*time.Second); err == nil {
			if !response.OK {
				return errors.New(response.Error)
			}
			fmt.Fprintln(stdout, "Starting the shared computer. Open Link to see and stop it.")
			return nil
		}
	}
	creds, err := credentials.Load(paths.CredentialFile(), nil)
	if err != nil || !creds.NetworkPaired() {
		return errors.New("pair this computer with OpenVibe Link before hosting a party")
	}
	chatSource := link.NewTokenSourceFor(creds, nil, "openvibe-node/"+version, "openvibe.chat")
	connectSource := link.NewTokenSourceFor(creds, nil, "openvibe-node/"+version, link.ConnectAudience)
	client := party.Client{PartyURL: *partyURL, ConnectServer: *connectServer, PartyID: id,
		ChatToken: func(ctx context.Context) (string, error) {
			t, err := chatSource.Token(ctx)
			if err != nil {
				return "", err
			}
			return t.Reveal(), nil
		},
		ConnectToken: func(ctx context.Context) (string, error) {
			t, err := connectSource.Token(ctx)
			if err != nil {
				return "", err
			}
			return t.Reveal(), nil
		}}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return party.Host(ctx, client, version, stdout)
}
