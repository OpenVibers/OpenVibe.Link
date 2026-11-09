//go:build linux

package worker

import (
	"fmt"
	"net"
	"strconv"
)

// Each desk has one host-side proxy socket. Every other packet from its veth is refused,
// including DNS, QUIC, IPv6 and WebRTC UDP. The proxy alone can dial the relay or an explicitly excluded site.
// A separate table survives another worker's refresh of openvibe_worker.
func (e *egressNet) proxyRules(port int) string {
	name := "ov_link_" + strconv.Itoa(e.n)
	return fmt.Sprintf(`table inet %s
delete table inet %s
table inet %s {
 chain mark_proxy {
  type filter hook prerouting priority -100; policy accept;
  iifname "%s" ip saddr %s ip daddr %s tcp dport %d meta mark set 0x4f564547
 }
 chain no_forward {
  type filter hook forward priority -20; policy accept;
  iifname "%s" reject with icmpx admin-prohibited
 }
}
`, name, name, name, e.hostIf(), e.job, e.host, port, e.hostIf())
}
func (e *egressNet) allowProxy(address string) (func(), error) {
	host, p, err := net.SplitHostPort(address)
	if err != nil || host != e.host.String() {
		return nil, fmt.Errorf("desk proxy must bind its own gateway")
	}
	port, err := strconv.Atoi(p)
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("invalid desk proxy port")
	}
	if err = runTool(e.proxyRules(port), e.tools.nft, "-f", "-"); err != nil {
		return nil, err
	}
	return func() { _ = runTool("", e.tools.nft, "delete", "table", "inet", "ov_link_"+strconv.Itoa(e.n)) }, nil
}
