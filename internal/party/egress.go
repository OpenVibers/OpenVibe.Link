package party

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"time"
)

var deniedNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("224.0.0.0/4"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("fc00::/7"), netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("64:ff9b::/96"),
}

func publicAddress(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, block := range deniedNetworks {
		if block.Contains(ip) {
			return false
		}
	}
	return true
}

type addressResolver func(context.Context, string) ([]net.IPAddr, error)
type addressDialer func(context.Context, string, string) (net.Conn, error)

func checkedDial(ctx context.Context, address string, resolve addressResolver, dial addressDialer, denied ...netip.Addr) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, errors.New("invalid proxy destination")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 || host == "" {
		return nil, errors.New("invalid proxy port")
	}
	var ips []net.IPAddr
	if literal, err := netip.ParseAddr(host); err == nil {
		ips = []net.IPAddr{{IP: net.IP(literal.AsSlice())}}
	} else {
		ips, err = resolve(ctx, host)
		if err != nil {
			return nil, errors.New("proxy DNS lookup failed")
		}
	}
	if len(ips) == 0 {
		return nil, errors.New("proxy destination has no address")
	}
	// Reject the entire answer if any address is private. Dial only the checked
	// address: a second name lookup would reintroduce DNS rebinding.
	for _, item := range ips {
		ip, ok := netip.AddrFromSlice(item.IP)
		if !ok || !publicAddress(ip) {
			return nil, errors.New("private proxy destination refused")
		}
		for _, blocked := range denied {
			if ip.Unmap() == blocked.Unmap() {
				return nil, errors.New("bridge gateway refused")
			}
		}
	}
	return dial(ctx, "tcp", net.JoinHostPort(ips[0].IP.String(), port))
}

func NewEgressProxy(gateway string) http.Handler {
	resolver := net.DefaultResolver.LookupIPAddr
	dialer := (&net.Dialer{Timeout: 10 * time.Second}).DialContext
	address, _ := netip.ParseAddr(gateway)
	return egressProxy(resolver, dialer, address)
}

func egressProxy(resolve addressResolver, dial addressDialer, denied ...netip.Addr) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			upstream, err := checkedDial(r.Context(), r.Host, resolve, dial, denied...)
			if err != nil {
				http.Error(w, err.Error(), http.StatusForbidden)
				return
			}
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				_ = upstream.Close()
				http.Error(w, "CONNECT unavailable", http.StatusInternalServerError)
				return
			}
			client, _, err := hijacker.Hijack()
			if err != nil {
				_ = upstream.Close()
				return
			}
			_, _ = client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
			go func() { _, _ = io.Copy(upstream, client); _ = upstream.Close() }()
			_, _ = io.Copy(client, upstream)
			_ = client.Close()
			return
		}
		if r.URL == nil || r.URL.Scheme != "http" || r.URL.Hostname() == "" || r.URL.User != nil {
			http.Error(w, "HTTP proxy requires an absolute HTTP URL", http.StatusBadRequest)
			return
		}
		port := r.URL.Port()
		if port == "" {
			port = "80"
		}
		address := net.JoinHostPort(r.URL.Hostname(), port)
		upstream, err := checkedDial(r.Context(), address, resolve, dial, denied...)
		if err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		defer upstream.Close()
		out := r.Clone(r.Context())
		out.RequestURI = ""
		out.URL.Scheme, out.URL.Host = "", ""
		out.Header.Del("Proxy-Connection")
		out.Header.Del("Proxy-Authorization")
		if err = out.Write(upstream); err != nil {
			http.Error(w, "upstream request failed", http.StatusBadGateway)
			return
		}
		response, err := http.ReadResponse(bufio.NewReader(upstream), out)
		if err != nil {
			http.Error(w, "upstream response failed", http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		for key, values := range response.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	})
}

func policyFile(address string) (string, error) {
	if _, _, err := net.SplitHostPort(address); err != nil {
		return "", err
	}
	policy := map[string]any{
		"ProxySettings": map[string]string{
			"ProxyMode":       "fixed_servers",
			"ProxyServer":     "http=" + address + ";https=" + address,
			"ProxyBypassList": "<-loopback>",
		},
		"URLBlocklist": []string{"file://*", "chrome://settings", "chrome://settings/*", "chrome://flags/*"},
	}
	data, err := json.Marshal(policy)
	if err != nil {
		return "", err
	}
	file, err := os.CreateTemp("", "openvibe-party-policy-*.json")
	if err != nil {
		return "", err
	}
	defer file.Close()
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(data)
	}
	if err != nil {
		_ = os.Remove(file.Name())
		return "", fmt.Errorf("write Chrome policy: %w", err)
	}
	return file.Name(), nil
}
