package specs

import (
	"net"
	"net/netip"
	"sort"
	"strings"
)

// The machine's addresses on its own networks (plan §AD.15, 11-16: "the Link agent reports its LAN addresses in specs,
// so Computer always knows where a machine is"): private IPv4 (10/8, 172.16/12, 192.168/16), the shared range overlay
// networks use (100.64/10, Tailscale among them) and IPv6 unique-local addresses, on interfaces that are up. Public
// addresses are never read here (the network already sees the one it talks to), nor loopback, link-local, or the
// bridges containers and virtual machines make on this machine, which no other machine can reach.

// maxLAN bounds the list: a machine with more addresses than this is a router, and the first ones are enough.
const maxLAN = 8

// virtualPrefixes name interfaces that only this machine can reach.
var virtualPrefixes = []string{"docker", "br-", "veth", "virbr", "lxc", "lxd", "cni", "flannel", "vnet", "vmnet", "vboxnet", "podman", "kube", "cali", "tap", "dummy"}

var lanRanges = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("fc00::/7"),
}

// iface is what lanAddresses needs of an interface, so a test can hand it a fixture.
type iface struct {
	Name  string
	Up    bool
	Loop  bool
	Addrs []string // CIDR forms, as net.Interface.Addrs gives them
}

// isLAN reports whether a is an address another machine on one of this machine's networks could reach it at.
func isLAN(a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range lanRanges {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func isVirtual(name string) bool {
	n := strings.ToLower(name)
	for _, p := range virtualPrefixes {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return false
}

// lanFrom picks the LAN addresses out of a list of interfaces: IPv4 first, then IPv6, each sorted, at most maxLAN.
func lanFrom(list []iface) []string {
	seen := map[netip.Addr]bool{}
	var v4, v6 []netip.Addr
	for _, it := range list {
		if !it.Up || it.Loop || isVirtual(it.Name) {
			continue
		}
		for _, s := range it.Addrs {
			p, err := netip.ParsePrefix(s)
			var a netip.Addr
			if err == nil {
				a = p.Addr()
			} else if a, err = netip.ParseAddr(s); err != nil {
				continue
			}
			a = a.Unmap().WithZone("")
			if !isLAN(a) || seen[a] {
				continue
			}
			seen[a] = true
			if a.Is4() {
				v4 = append(v4, a)
			} else {
				v6 = append(v6, a)
			}
		}
	}
	sort.Slice(v4, func(i, j int) bool { return v4[i].Less(v4[j]) })
	sort.Slice(v6, func(i, j int) bool { return v6[i].Less(v6[j]) })
	out := make([]string, 0, len(v4)+len(v6))
	for _, a := range append(v4, v6...) {
		if len(out) == maxLAN {
			break
		}
		out = append(out, a.String())
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// lanAddresses reads this machine's interfaces now; nil when there are none (or they cannot be read).
func lanAddresses() []string {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	list := make([]iface, 0, len(ifs))
	for _, i := range ifs {
		it := iface{Name: i.Name, Up: i.Flags&net.FlagUp != 0, Loop: i.Flags&net.FlagLoopback != 0}
		if addrs, err := i.Addrs(); err == nil {
			for _, a := range addrs {
				it.Addrs = append(it.Addrs, a.String())
			}
		}
		list = append(list, it)
	}
	return lanFrom(list)
}
