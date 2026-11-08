package party

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"
)

func TestEgressRefusesPrivateRanges(t *testing.T) {
	for _, value := range []string{
		"127.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.1.1", "100.64.0.1",
		"169.254.1.1", "224.0.0.1", "172.17.0.1", "::1", "fc00::1", "fe80::1",
		"::ffff:192.168.1.1", "64:ff9b::c0a8:101",
	} {
		if publicAddress(netip.MustParseAddr(value)) {
			t.Errorf("accepted private address %s", value)
		}
	}
	if !publicAddress(netip.MustParseAddr("8.8.8.8")) || !publicAddress(netip.MustParseAddr("2606:4700:4700::1111")) {
		t.Fatal("public web address was refused")
	}
}

func TestEgressResolvesOnceAndDialsCheckedIP(t *testing.T) {
	lookup := 0
	resolve := func(_ context.Context, host string) ([]net.IPAddr, error) {
		lookup++
		if host == "lan.example" {
			return []net.IPAddr{{IP: net.ParseIP("192.168.1.1")}}, nil
		}
		if host == "mixed.example" {
			return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}, {IP: net.ParseIP("10.0.0.1")}}, nil
		}
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
	}
	dialed := ""
	dial := func(_ context.Context, _, address string) (net.Conn, error) {
		dialed = address
		return nil, errors.New("test dial reached")
	}
	if _, err := checkedDial(context.Background(), "lan.example:443", resolve, dial); err == nil || dialed != "" {
		t.Fatal("private DNS answer reached dial")
	}
	if _, err := checkedDial(context.Background(), "mixed.example:443", resolve, dial); err == nil || dialed != "" {
		t.Fatal("mixed DNS answer reached dial")
	}
	if _, err := checkedDial(context.Background(), "public.example:443", resolve, dial); err == nil || dialed != "8.8.8.8:443" || lookup != 3 {
		t.Fatalf("public destination was not dialed at checked IP: %s, lookups %d", dialed, lookup)
	}
	dialed = ""
	if _, err := checkedDial(context.Background(), "public.example:443", resolve, dial, netip.MustParseAddr("8.8.8.8")); err == nil || dialed != "" {
		t.Fatal("bridge gateway bypassed its explicit block")
	}
}

func TestEgressHTTPRefusesPrivateDNSAndAllowsPublic(t *testing.T) {
	resolve := func(_ context.Context, host string) ([]net.IPAddr, error) {
		if host == "lan.example" {
			return []net.IPAddr{{IP: net.ParseIP("10.1.2.3")}}, nil
		}
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
	}
	dialed := ""
	dial := func(_ context.Context, _, address string) (net.Conn, error) {
		dialed = address
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			buffer := make([]byte, 4096)
			_, _ = server.Read(buffer)
			_, _ = io.WriteString(server, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
		}()
		return client, nil
	}
	proxy := egressProxy(resolve, dial)
	private := httptest.NewRecorder()
	proxy.ServeHTTP(private, httptest.NewRequest(http.MethodGet, "http://lan.example/", nil))
	if private.Code != http.StatusForbidden || dialed != "" {
		t.Fatal("private DNS destination was not refused")
	}
	public := httptest.NewRecorder()
	proxy.ServeHTTP(public, httptest.NewRequest(http.MethodGet, "http://public.example/", nil))
	if public.Code != http.StatusOK || public.Body.String() != "ok" || dialed != "8.8.8.8:80" {
		t.Fatal("public HTTP destination failed")
	}
}

func TestPolicyForcesProxyAndBlocksLocalPages(t *testing.T) {
	path, err := policyFile("172.17.0.1:45678")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var policy struct {
		ProxySettings map[string]string `json:"ProxySettings"`
		URLBlocklist  []string          `json:"URLBlocklist"`
		WebRTC        string            `json:"WebRtcIPHandling"`
	}
	if json.Unmarshal(data, &policy) != nil {
		t.Fatal("invalid policy JSON")
	}
	if policy.ProxySettings["ProxyMode"] != "fixed_servers" || !strings.Contains(policy.ProxySettings["ProxyServer"], "172.17.0.1:45678") || policy.ProxySettings["ProxyBypassList"] != "<-loopback>" {
		t.Fatal("proxy policy is incomplete")
	}
	if policy.WebRTC != "disable_non_proxied_udp" {
		t.Fatal("a page's WebRTC could send UDP past the proxy")
	}
	for _, blocked := range []string{"file://*", "chrome://settings", "chrome://settings/*"} {
		if !strings.Contains(strings.Join(policy.URLBlocklist, " "), blocked) {
			t.Errorf("missing URL block %s", blocked)
		}
	}
}

func TestContainerManagedPolicyLive(t *testing.T) {
	if os.Getenv("PARTY_DOCKER_PROOF") != "1" {
		t.Skip("set PARTY_DOCKER_PROOF=1 for the local Docker proof")
	}
	runtime, err := FindRuntime()
	if err != nil {
		t.Fatal(err)
	}
	plan, err := NewPlan("party-89abcdef", 18181, turn())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	stop, err := Start(ctx, runtime, plan)
	if err != nil {
		t.Fatal(err)
	}
	defer stop(context.Background())
	if err := runtime.Run(ctx, "", "exec", plan.Name(), "test", "-s", "/etc/opt/chrome/policies/managed/openvibe-party.json"); err != nil {
		t.Fatal("Chrome policy was not present in the container")
	}
	ready, cancelReady := context.WithTimeout(ctx, 45*time.Second)
	defer cancelReady()
	if err := WaitReady(ready, plan.Port); err != nil {
		t.Fatal(err)
	}
}
