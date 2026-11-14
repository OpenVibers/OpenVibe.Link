package main

import (
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/OpenVibers/OpenVibe.Link/internal/deskdisplay"
)

const deskDisplayUsage = `usage: openvibe-link desk-display WIDTH HEIGHT APPS-JSON

Runs a lent desktop from inside its sandbox (the computer plugin starts it under openvibe-link desk-sandbox, Link v0.66):
its X display, then each app ([["wm", [...]], ["browser", [...]], ["terminal", [...]]]), then answers the plugin on fds
3 and 4, one JSON line each way: shot (a JPEG made here), xdotool (mouse and keys done in-process), logs, stop.
`

// cmdDeskDisplay is the `openvibe-link desk-display` command: the Go helper of a lent desktop (sandbox_desk.py's HELPER is its fallback).
func cmdDeskDisplay(args []string, stderr io.Writer) int {
	if len(args) != 3 {
		fmt.Fprint(stderr, deskDisplayUsage)
		return 2
	}
	w, e1 := strconv.Atoi(args[0])
	h, e2 := strconv.Atoi(args[1])
	if e1 != nil || e2 != nil || w < 320 || w > 7680 || h < 240 || h > 4320 {
		fmt.Fprint(stderr, deskDisplayUsage)
		return 2
	}
	apps, err := deskdisplay.ParseApps(args[2])
	if err != nil {
		fmt.Fprintln(stderr, "desk-display:", err)
		return 2
	}
	in, out := os.NewFile(3, "desk-in"), os.NewFile(4, "desk-out")
	if in == nil || out == nil {
		fmt.Fprintln(stderr, "desk-display: fds 3 and 4 are the plugin's pipe")
		return 2
	}
	if err := deskdisplay.Serve(w, h, apps, in, out); err != nil {
		fmt.Fprintln(stderr, "desk-display:", err)
		return 1
	}
	return 0
}
