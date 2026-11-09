package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/OpenVibers/OpenVibe.Link/internal/credentials"
	"github.com/OpenVibers/OpenVibe.Link/internal/firstrun"
	"github.com/OpenVibers/OpenVibe.Link/internal/link"
	"github.com/OpenVibers/OpenVibe.Link/internal/localctl"
	"github.com/OpenVibers/OpenVibe.Link/internal/protocol"
)

// The guided first run is what a bare `openvibe-link` does: pair (when needed), check the way to OpenVibe, run one
// harmless first job (this machine's OS, CPUs, memory and free disk) and report one event per step to Bot over the
// link the Node already uses. `openvibe-link first-run` is the same command, for scripts that want to say it.
const firstRunDocs = "https://github.com/OpenVibers/OpenVibe.Link/blob/main/docs/install.md"

// guideArgs are the flags a bare `openvibe-link` (or `openvibe-link first-run`) takes. They mirror the pair flags so
// a machine's installer command can be pasted at the prompt, and so scripts can pass the code and Network pairing.
type guideArgs struct {
	code, robot, name, server, network, pairing string
}

// parseGuideFlags parses the guide's own flags (global flags included) and returns the positional words.
func parseGuideFlags(args []string, g *globals, a *guideArgs, stderr io.Writer) ([]string, error) {
	fs := flag.NewFlagSet("openvibe-link", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&g.home, "home", "", "config and state directory")
	fs.StringVar(&g.logLevel, "log-level", "info", "log level")
	fs.StringVar(&a.code, "code", "", "the pairing code, instead of the prompt")
	fs.StringVar(&a.robot, "robot", "", "the rob_… id of the robot to pair with")
	fs.StringVar(&a.name, "name", "", "a name for this device on openvibe.bot; default: the hostname")
	fs.StringVar(&a.server, "server", "", "OpenVibe.Bot origin")
	fs.StringVar(&a.network, "network", "", "OpenVibe.Network origin: pair this machine there")
	fs.StringVar(&a.pairing, "pairing", "", "the pair_… id from the installer command (with --network)")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	return fs.Args(), nil
}

// cmdFirstRun is the guided first run. It reads a pairing code from stdin when the machine is not paired yet and
// never asks again once a credential exists.
func cmdFirstRun(g *globals, a guideArgs, positional []string, stdin io.Reader, stdout io.Writer) error {
	if len(positional) > 1 {
		return errors.New("usage: openvibe-link [--code CODE] [--network URL --pairing pair_…] [--home DIR]")
	}
	if len(positional) == 1 && a.code == "" {
		a.code = positional[0]
	}
	if err := g.paths.Ensure(); err != nil {
		fmt.Fprintln(stdout, "The Node keeps its files where this account cannot write them. Run this with sudo:")
		fmt.Fprintln(stdout, "  sudo openvibe-link")
		return fmt.Errorf("cannot use %s: %w", g.paths.ConfigDir, err)
	}
	if err := writable(g.paths.StateDir); err != nil {
		fmt.Fprintln(stdout, "The Node's files belong to the service account. Run the first run with sudo:")
		fmt.Fprintln(stdout, "  sudo openvibe-link")
		return fmt.Errorf("cannot write to %s: %w (run with sudo)", g.paths.StateDir, err)
	}
	fmt.Fprintln(stdout, "OpenVibe Link first run")
	fmt.Fprintln(stdout, "======================")

	report, err := firstrun.Load(g.paths.FirstRunFile())
	if err != nil {
		return err
	}
	if !report.Has(firstrun.StepInstalled) {
		report.Upsert(firstrun.Step{Name: firstrun.StepInstalled, At: time.Now().UTC()})
	}

	creds, err := credentials.Load(g.paths.CredentialFile(), func(s string) { g.log.Warn(s) })
	if errors.Is(err, credentials.ErrNotPaired) {
		fmt.Fprintln(stdout)
		fmt.Fprintln(stdout, "Get a pairing code on openvibe.bot (machines: Add a machine), then enter it here.")
		fmt.Fprintln(stdout, "You can also paste the whole install command it shows.")
		line, rerr := readPrompt(stdin, stdout, "Pairing code: ")
		if rerr != nil {
			return rerr
		}
		pa, perr := parsePairingInput(line)
		if perr != nil {
			return perr
		}
		// Flags on the command line win over the pasted command, which is what a script expects.
		if a.code != "" {
			pa.code = a.code
		}
		if a.robot != "" {
			pa.robot = a.robot
		}
		if a.name != "" {
			pa.name = a.name
		}
		if a.server != "" {
			pa.server = a.server
		}
		if a.network != "" {
			pa.network = a.network
		}
		if a.pairing != "" {
			pa.pairing = a.pairing
		}
		creds, err = pairFromGuide(g, pa, stdout)
		if err != nil {
			fmt.Fprintln(stdout)
			fmt.Fprintln(stdout, "That did not pair. Codes work once and expire; get a fresh one at openvibe.bot/machines")
			fmt.Fprintln(stdout, "and run `openvibe-link` again.")
			return err
		}
		at := creds.PairedAt
		if at.IsZero() {
			at = time.Now().UTC()
		}
		report.Upsert(firstrun.Step{Name: firstrun.StepPaired, At: at})
		fmt.Fprintf(stdout, "Paired as %s.\n", creds.Label())
	} else if err != nil {
		return err
	} else {
		// A pairing done by the installer (or `openvibe-link pair`) already has its time; keep it for the funnel.
		if !report.Has(firstrun.StepPaired) {
			at := creds.PairedAt
			if at.IsZero() {
				at = time.Now().UTC()
			}
			report.Upsert(firstrun.Step{Name: firstrun.StepPaired, At: at})
		}
		// A Network pairing whose bind on Bot failed at pairing time binds now, so the events have a device to land on.
		if creds.NetworkPaired() && !creds.Bound() {
			cfg, cerr := loadConfig(g)
			if cerr != nil {
				return cerr
			}
			ts := link.NewTokenSource(creds, nil, "openvibe-link/"+version)
			if creds, err = bindOnStart(g, creds, ts, cfg.Server); err != nil {
				return err
			}
		}
		if err := report.Save(g.paths.FirstRunFile()); err != nil {
			g.log.Warn("could not save the first-run record", "err", err)
		}
		fmt.Fprintf(stdout, "Paired as %s.\n", creds.Label())
	}

	// (b) The self-check: both services the machine talks to.
	fmt.Fprintln(stdout)
	botURL := creds.Server
	if botURL == "" {
		if cfg, cerr := loadConfig(g); cerr == nil {
			botURL = cfg.Server
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	ok, reason := firstrun.Check(ctx, nil, botURL, creds.Network)
	cancel()
	report.Upsert(firstrun.Step{Name: firstrun.StepCheck, At: time.Now().UTC(), OK: ok, Reason: reason})
	if ok {
		if creds.Network != "" {
			fmt.Fprintln(stdout, "Self-check: OpenVibe.Bot and OpenVibe.Network answer.")
		} else {
			fmt.Fprintln(stdout, "Self-check: OpenVibe.Bot answers.")
		}
	} else {
		fmt.Fprintf(stdout, "Self-check: FAILED: %s.\n", reason)
		fmt.Fprintln(stdout, "The Node keeps trying in the background; check this machine's network and firewall.")
	}

	// (c) The first job: a harmless local report of this machine, sent as one event (never a path, user or address).
	info := firstrun.Gather(g.paths.StateDir)
	report.Upsert(firstrun.Step{Name: firstrun.StepFirstJob, At: time.Now().UTC(), Fields: info.Fields(version)})
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "First job: this machine")
	fmt.Fprintf(stdout, "  system   %s/%s, %d CPUs\n", info.OS, info.Arch, info.CPUs)
	if info.MemoryTotal > 0 {
		fmt.Fprintf(stdout, "  memory   %s free of %s\n", bytesText(info.MemoryFree), bytesText(info.MemoryTotal))
	}
	if info.DiskTotal > 0 {
		fmt.Fprintf(stdout, "  disk     %s free of %s\n", bytesText(info.DiskFree), bytesText(info.DiskTotal))
	}
	fmt.Fprintf(stdout, "  node     %s\n", version)

	if err := report.Save(g.paths.FirstRunFile()); err != nil {
		return err
	}
	events := report.Events(version, info.OS, info.Arch)
	if len(events) > 0 {
		if err := sendFirstRun(g, creds, events); err != nil {
			fmt.Fprintf(stdout, "Could not report this first run (%v). The report is kept; run `openvibe-link` again when the link is up.\n", err)
		} else {
			fmt.Fprintln(stdout, "Reported this first run to OpenVibe.Bot.")
		}
	}

	// (d) What it can do next, in one line, with the docs.
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Next: `openvibe-link status` shows this machine, `openvibe-link stop` stops everything, and")
	fmt.Fprintf(stdout, "your account lists it at https://openvibe.bot/machines. Docs: %s\n", firstRunDocs)
	return nil
}

// pairFromGuide redeems the code and stores the credential, the same two paths `openvibe-link pair` takes.
func pairFromGuide(g *globals, a pairArgs, stdout io.Writer) (*credentials.Credentials, error) {
	cfg, noConfig, err := preparePair(g, a)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ua := "openvibe-link/" + version
	var creds *credentials.Credentials
	if a.network != "" || a.pairing != "" {
		code, err := link.NormalizeCode(a.code)
		if err != nil {
			return nil, err
		}
		if a.pairing != "" && !link.PairingIDRe.MatchString(a.pairing) {
			return nil, errors.New("--pairing must be the pair_… id from the installer command on openvibe.bot")
		}
		network := strings.TrimSuffix(strings.TrimSpace(a.network), "/")
		if network == "" {
			return nil, errors.New("--pairing needs --network, the OpenVibe.Network URL from the installer command")
		}
		if creds, err = link.PairNetwork(ctx, nil, network, protocol.NodePairingRequest{Code: code, Pairing: a.pairing, Name: deviceName(a.name)}, ua); err != nil {
			return nil, err
		}
		creds.Server = strings.TrimSuffix(cfg.Server, "/")
		if err := saveCredentials(g, creds); err != nil {
			return nil, err
		}
		if bound, berr := link.Bind(ctx, nil, creds, link.NewTokenSource(creds, nil, ua), ua); berr != nil {
			if errors.Is(berr, link.ErrNodeRevoked) {
				return nil, berr
			}
			fmt.Fprintf(stdout, "Paired on %s; the device binds when the Node starts.\n", network)
		} else {
			if err := saveCredentials(g, bound); err != nil {
				return nil, err
			}
			creds = bound
		}
	} else {
		code, err := link.NormalizeCode(a.code)
		if err != nil {
			return nil, err
		}
		if a.robot != "" && !link.RobotRe.MatchString(a.robot) {
			return nil, fmt.Errorf("--robot must be a robot id like rob_01J8Z4M2Q0R7T9YV3K6N8P1W2X")
		}
		descs, _ := probeAll(cfg, g.paths, g.log)
		req := protocol.PairRequest{Robot: a.robot, Code: code, AgentVersion: version, DeviceKind: cfg.DeviceKind, Drivers: []string{},
			Capabilities: map[string]any{}, Name: deviceName(a.name)}
		names := make([]string, 0, len(descs))
		for n := range descs {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			req.Drivers = append(req.Drivers, descs[n].Driver)
			req.Capabilities[n] = descs[n].Capabilities
		}
		if creds, err = link.Pair(ctx, nil, cfg.Server, req); err != nil {
			return nil, err
		}
		if err := saveCredentials(g, creds); err != nil {
			return nil, err
		}
	}
	if err := finishPair(g, a, cfg, noConfig, stdout); err != nil {
		return nil, err
	}
	return creds, nil
}

// writable makes the guide fail with the sudo hint before it asks for a code, not after, when the state directory
// exists but belongs to the service account.
func writable(dir string) error {
	f, err := os.CreateTemp(dir, ".first-run-*")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}

// readPrompt writes prompt and reads one line; a line without a trailing newline at EOF is still a line.
func readPrompt(r io.Reader, w io.Writer, prompt string) (string, error) {
	if r == nil {
		return "", errors.New("no pairing code was entered")
	}
	fmt.Fprint(w, prompt)
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return "", errors.New("no pairing code was entered")
	}
	return line, nil
}

// parsePairingInput takes a code, or the whole installer command from openvibe.bot/machines (curl … sh -s -- --network
// URL --pairing pair_… --code CODE --worker). Reading the command is how a machine paired through Network gets the
// network and pairing id from one prompt.
func parsePairingInput(line string) (pairArgs, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return pairArgs{}, errors.New("no pairing code was entered")
	}
	fields := strings.Fields(line)
	if len(fields) == 1 && !strings.HasPrefix(fields[0], "-") {
		return pairArgs{code: fields[0]}, nil
	}
	var a pairArgs
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		key, val, hasVal := strings.Cut(f, "=")
		set := func(dst *string) {
			if hasVal {
				*dst = val
			} else if i+1 < len(fields) {
				i++
				*dst = fields[i]
			}
		}
		switch key {
		case "--code":
			set(&a.code)
		case "--network":
			set(&a.network)
		case "--pairing":
			set(&a.pairing)
		case "--robot":
			set(&a.robot)
		case "--name":
			set(&a.name)
		case "--server":
			set(&a.server)
		}
	}
	if a.code == "" {
		// `openvibe-link pair ABCD-1234` pasted without --code.
		for i := 0; i+1 < len(fields); i++ {
			if fields[i] == "pair" && !strings.HasPrefix(fields[i+1], "-") {
				a.code = fields[i+1]
				break
			}
		}
	}
	if a.code == "" {
		return pairArgs{}, errors.New("that is neither a pairing code (like ABCD-1234) nor an install command with --code")
	}
	return a, nil
}

// sendFirstRun sends the events through the running Node when there is one (the installer starts it), and otherwise
// opens the device link for the few frames itself, so a manual install reports the same way.
func sendFirstRun(g *globals, creds *credentials.Credentials, events []protocol.Event) error {
	data, err := jsonTelemetry(events)
	if err != nil {
		return err
	}
	if resp, err := localctl.Call(g.paths.SocketPath(), localctl.Request{Cmd: localctl.CmdFirstRun, Data: data}, 5*time.Second); err == nil && resp.OK {
		return nil
	}
	return sendFirstRunDirect(creds, events)
}

func jsonTelemetry(events []protocol.Event) ([]byte, error) {
	return json.Marshal(protocol.Telemetry{Events: events})
}

func sendFirstRunDirect(creds *credentials.Credentials, events []protocol.Event) error {
	if creds.Server == "" {
		return errors.New("no OpenVibe.Bot address is stored")
	}
	u, err := link.DeviceURL(creds.Server)
	if err != nil {
		return err
	}
	h := &firstRunHandler{ready: make(chan struct{})}
	opts := link.Options{URL: u, Credential: creds.Credential, UserAgent: "openvibe-link/" + version,
		HeartbeatInterval: 200 * time.Millisecond, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if creds.NetworkPaired() {
		opts.Credential = credentials.Secret{}
		opts.Tokens = link.NewTokenSource(creds, nil, opts.UserAgent)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	l := link.New(opts, h)
	go l.Run(ctx)
	select {
	case <-h.ready:
	case <-ctx.Done():
		return errors.New("OpenVibe.Bot did not answer")
	}
	st := protocol.Status{Firmware: "openvibe-link-" + version, AgentVersion: version, Capabilities: map[string]any{},
		OS: runtime.GOOS, Arch: runtime.GOARCH, Drivers: []protocol.DriverStatus{}, Faults: []protocol.Fault{}}
	if err := l.Send(st); err != nil {
		return err
	}
	for _, e := range events {
		if err := l.Send(protocol.Telemetry{Events: []protocol.Event{e}}); err != nil {
			return err
		}
	}
	// The link's own heartbeats (100 ms after hello) come after these frames on the same socket; two of them prove
	// the frames were written and answered. The device link acks heartbeats itself, so this is the only signal.
	select {
	case <-time.After(2 * time.Second):
	case <-ctx.Done():
		return errors.New("OpenVibe.Bot did not answer")
	}
	return nil
}

// firstRunHandler only needs the config (the link is ready) and the next heartbeat ack (the frames went out).
type firstRunHandler struct {
	ready chan struct{}
	ack   chan struct{}
	once  sync.Once
}

func (h *firstRunHandler) Connected()         {}
func (h *firstRunHandler) Disconnected(error) {}
func (h *firstRunHandler) Frame(f protocol.Frame) {
	switch f.Msg.(type) {
	case protocol.Config:
		h.once.Do(func() { close(h.ready) })
	case protocol.HeartbeatAck:
		select {
		case h.ack <- struct{}{}:
		default:
		}
	}
}

func bytesText(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
