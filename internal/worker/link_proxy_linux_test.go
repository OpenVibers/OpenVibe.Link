//go:build linux

package worker

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/OpenVibers/OpenVibe.Node/internal/config"
)

func TestLinkProxyFirewallOnlyAllowsItsOwnSocket(t *testing.T) {
	e := &egressNet{n: 7, host: netip.MustParseAddr("169.254.240.29"), job: netip.MustParseAddr("169.254.240.30")}
	rules := e.proxyRules(32123)
	for _, required := range []string{`iifname "ovw7" ip saddr 169.254.240.30 ip daddr 169.254.240.29 tcp dport 32123 meta mark set 0x4f564547`, `iifname "ovw7" reject with icmpx admin-prohibited`, `hook forward priority -20`} {
		if !strings.Contains(rules, required) {
			t.Fatalf("missing restriction: %s", required)
		}
	}
	base := ruleset(config.WorkerConfig{Egress: config.EgressPublic}, []string{"eth0"})
	if !strings.Contains(base, "meta mark 0x4f564547 accept") {
		t.Fatal("desk cannot reach its proxy")
	}
	if strings.Contains(rules, "udp dport") || strings.Contains(rules, "ip6 daddr") {
		t.Fatal("direct DNS/WebRTC/IPv6 allowed")
	}
	if _, err := e.allowProxy("127.0.0.1:32123"); err == nil {
		t.Fatal("another gateway accepted")
	}
}
