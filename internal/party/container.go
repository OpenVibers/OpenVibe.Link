// Package party runs one short-lived shared browser in a container. Nothing from
// the host is mounted into it; Connect reaches only its loopback HTTP port.
package party

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// Image is the pinned v3.1.6 multi-architecture index (amd64 and arm64).
const Image = "ghcr.io/m1k1o/neko/google-chrome@sha256:dce2a6286cfb7e747d7fb05fadd03b25bf2db155c4f71ae53e0f40be71837547"

var partyID = regexp.MustCompile(`^party-[a-f0-9]{8}$`)

type ICEServer struct {
	URLs       any    `json:"urls"`
	Username   string `json:"username,omitempty"`
	Credential string `json:"credential,omitempty"`
}

type Plan struct {
	PartyID        string
	Port           int
	ICEServers     []ICEServer
	MemberPassword string
	AdminPassword  string
	APIToken       string
}

// NewPlan generates independent credentials for this run. The caller obtains
// fresh TURN credentials before constructing it.
func NewPlan(id string, port int, ice []ICEServer) (Plan, error) {
	if !partyID.MatchString(id) || port < 1 || port > 65535 {
		return Plan{}, errors.New("party id or loopback port is invalid")
	}
	if len(ice) == 0 {
		return Plan{}, errors.New("a party desktop needs TURN servers")
	}
	hasTurnTLS := false
	for _, server := range ice {
		urls, ok := server.URLs.(string)
		if !ok {
			return Plan{}, errors.New("ICE URL must be a string")
		}
		if strings.HasPrefix(urls, "turns:") && strings.Contains(urls, ":443") && server.Username != "" && server.Credential != "" {
			hasTurnTLS = true
		}
	}
	if !hasTurnTLS {
		return Plan{}, errors.New("a party desktop needs authenticated TURN over TLS on port 443")
	}
	member, err := random()
	if err != nil {
		return Plan{}, err
	}
	admin, err := random()
	if err != nil {
		return Plan{}, err
	}
	api, err := random()
	if err != nil {
		return Plan{}, err
	}
	return Plan{PartyID: id, Port: port, ICEServers: ice, MemberPassword: member, AdminPassword: admin, APIToken: api}, nil
}

func random() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (p Plan) Name() string { return "openvibe-" + p.PartyID }

// RunArgs contain no credential. The runtime receives values in its process
// environment, while Docker/Podman receive only environment variable names.
func (p Plan) RunArgs() []string {
	return []string{"run", "-d", "--rm", "--name", p.Name(), "--network", "bridge",
		"--security-opt=no-new-privileges", "--pids-limit=512",
		"--cpus=2", "--memory=3g", "--shm-size=2g",
		"-p", fmt.Sprintf("127.0.0.1:%d:8080", p.Port), Image}
}

func (p Plan) EnvFile() (string, error) {
	ice, err := json.Marshal(p.ICEServers)
	if err != nil {
		return "", err
	}
	return "NEKO_MEMBER_MULTIUSER_USER_PASSWORD=" + p.MemberPassword + "\n" +
		"NEKO_MEMBER_MULTIUSER_ADMIN_PASSWORD=" + p.AdminPassword + "\n" +
		"NEKO_SESSION_API_TOKEN=" + p.APIToken + "\n" +
		"NEKO_SESSION_COOKIE_ENABLED=true\n" +
		"NEKO_SESSION_COOKIE_SECURE=true\n" +
		"NEKO_SESSION_LOCKED_CONTROLS=true\n" +
		"NEKO_SESSION_IMPLICIT_HOSTING=false\n" +
		"NEKO_WEBRTC_ICELITE=false\n" +
		"NEKO_WEBRTC_ICESERVERS_FRONTEND=" + string(ice) + "\n" +
		"NEKO_WEBRTC_ICESERVERS_BACKEND=" + string(ice) + "\n", nil
}

type Runtime interface {
	Run(ctx context.Context, stdin string, args ...string) error
}

type CLIRuntime struct{ Binary string }

func FindRuntime() (CLIRuntime, error) {
	for _, name := range []string{"docker", "podman"} {
		if path, err := exec.LookPath(name); err == nil {
			return CLIRuntime{Binary: path}, nil
		}
	}
	return CLIRuntime{}, errors.New("install Docker or Podman to host a party desktop")
}

func (r CLIRuntime) Run(ctx context.Context, stdin string, args ...string) error {
	if stdin != "" {
		for _, line := range strings.Split(strings.TrimSpace(stdin), "\n") {
			key, _, ok := strings.Cut(line, "=")
			if !ok || key == "" {
				return errors.New("invalid container environment")
			}
			args = append(args[:len(args)-1], append([]string{"--env", key}, args[len(args)-1:]...)...)
		}
	}
	cmd := exec.CommandContext(ctx, r.Binary, args...)
	cmd.Env = os.Environ()
	if stdin != "" {
		cmd.Env = append(cmd.Env, strings.Split(strings.TrimSpace(stdin), "\n")...)
	}
	// Never return runtime output: a failed runtime may echo environment values.
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("container runtime %s failed: %w", args[0], err)
	}
	return nil
}

// Start pulls exactly the pinned image, then starts the container. A failed run
// cleans up by its validated name. Stop is idempotent and also runs on cancellation.
func Start(ctx context.Context, r Runtime, p Plan) (func(context.Context) error, error) {
	if !partyID.MatchString(p.PartyID) {
		return nil, errors.New("invalid party id")
	}
	if err := r.Run(ctx, "", "pull", Image); err != nil {
		return nil, err
	}
	env, err := p.EnvFile()
	if err != nil {
		return nil, err
	}
	if err := r.Run(ctx, env, p.RunArgs()...); err != nil {
		_ = r.Run(context.Background(), "", "rm", "-f", p.Name())
		return nil, err
	}
	return func(stopCtx context.Context) error { return r.Run(stopCtx, "", "stop", "--time", "10", p.Name()) }, nil
}

// FreePort asks the kernel for an unused loopback port. Docker's bind is the
// final ownership check; a race fails closed and Start removes the container.
func FreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func WaitReady(ctx context.Context, port int) error {
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	client := &http.Client{Timeout: time.Second}
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/", port), nil)
		if err == nil {
			if res, e := client.Do(req); e == nil {
				_ = res.Body.Close()
				if res.StatusCode == http.StatusOK {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}
