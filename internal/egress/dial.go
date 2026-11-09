package egress

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"time"
)

var blocked = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"), netip.MustParsePrefix("2001::/32"), netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("2001:db8::/32"),
}

func Public(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.Zone() != "" {
		return false
	}
	for _, p := range blocked {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

type DialFunc func(context.Context, string) (net.Conn, error)

func PublicDial(ctx context.Context, address string) (net.Conn, error) {
	return checkedDial(ctx, address, net.DefaultResolver.LookupIPAddr, (&net.Dialer{Timeout: 10 * time.Second}).DialContext)
}
func checkedDial(ctx context.Context, address string, resolve func(context.Context, string) ([]net.IPAddr, error), dial func(context.Context, string, string) (net.Conn, error)) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	p, err := strconv.Atoi(port)
	if err != nil || (p != 80 && p != 443) {
		return nil, errors.New("only web ports are allowed")
	}
	// Resolve on the person's Link and pin the checked address for the actual dial.
	ips, err := resolve(ctx, host)
	if err != nil || len(ips) == 0 {
		return nil, errors.New("site address unavailable")
	}
	for _, ip := range ips {
		a, ok := netip.AddrFromSlice(ip.IP)
		if !ok || !Public(a) {
			return nil, errors.New("private site address refused")
		}
	}
	return dial(ctx, "tcp", net.JoinHostPort(ips[0].IP.String(), port))
}
