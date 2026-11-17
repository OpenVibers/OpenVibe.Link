package specs

import (
	"reflect"
	"testing"
)

// TestLANAddresses: a machine reports where it is on its own networks (plan §AD.15) — private IPv4, the overlay range and
// unique-local IPv6 on interfaces that are up — and never a public address, loopback, link-local, an interface that is
// down, or a container or VM bridge only it can reach.
func TestLANAddresses(t *testing.T) {
	got := lanFrom([]iface{
		{Name: "lo", Up: true, Loop: true, Addrs: []string{"127.0.0.1/8", "::1/128"}},
		{Name: "enp3s0", Up: true, Addrs: []string{"192.168.1.20/24", "203.0.113.7/24", "fe80::1/64", "fd12:3456::20/64", "2001:db8::20/64"}},
		{Name: "wlp2s0", Up: false, Addrs: []string{"192.168.1.21/24"}},
		{Name: "docker0", Up: true, Addrs: []string{"172.17.0.1/16"}},
		{Name: "br-1a2b3c", Up: true, Addrs: []string{"172.18.0.1/16"}},
		{Name: "virbr0", Up: true, Addrs: []string{"192.168.122.1/24"}},
		{Name: "tailscale0", Up: true, Addrs: []string{"100.101.102.103/32"}},
		{Name: "wg0", Up: true, Addrs: []string{"10.8.0.2/24", "10.8.0.2/24"}},
		{Name: "eth1", Up: true, Addrs: []string{"::ffff:172.20.1.5/112"}},
	})
	want := []string{"10.8.0.2", "100.101.102.103", "172.20.1.5", "192.168.1.20", "fd12:3456::20"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lanFrom = %v, want %v", got, want)
	}
	if lanFrom([]iface{{Name: "eth0", Up: true, Addrs: []string{"203.0.113.7/24"}}}) != nil {
		t.Fatal("a machine with only a public address reports no LAN")
	}
	if n := len(lanFrom([]iface{{Name: "eth0", Up: true, Addrs: []string{"10.0.0.1/8", "10.0.0.2/8", "10.0.0.3/8", "10.0.0.4/8", "10.0.0.5/8", "10.0.0.6/8", "10.0.0.7/8", "10.0.0.8/8", "10.0.0.9/8", "10.0.1.1/8"}}})); n != maxLAN {
		t.Fatalf("at most %d, got %d", maxLAN, n)
	}
}
