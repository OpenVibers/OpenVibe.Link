package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/OpenVibers/OpenVibe.Link/internal/localctl"
)

// cmdPanel prints where Link's own window is on this machine: a loopback URL with this run's token in the fragment.
// The tray's "Open Link" opens the same address.
func cmdPanel(g *globals, stdout io.Writer) error {
	resp, err := localctl.Call(g.paths.SocketPath(), localctl.Request{Cmd: localctl.CmdPanel}, 2*time.Second)
	if err != nil {
		return errors.New("the Link is not running: start it, then open Open Link from the tray or run this again")
	}
	var v struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(resp.Data, &v); err != nil || v.URL == "" {
		return errors.New("the Link did not answer where Link's window is")
	}
	fmt.Fprintln(stdout, v.URL)
	return nil
}
