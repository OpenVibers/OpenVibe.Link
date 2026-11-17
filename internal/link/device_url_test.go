package link

import "testing"

// The default registry's device socket is openvibe.computer's (plan §AD.15); any other server keeps its own host.
func TestDeviceURLMachinesOnComputer(t *testing.T) {
	for _, c := range []struct{ server, want string }{
		{"https://openvibe.bot", "wss://openvibe.computer/device"},
		{"https://OpenVibe.Bot/", "wss://openvibe.computer/device"},
		{"https://openvibe.bot:8443", "wss://openvibe.bot:8443/device"},
		{"https://bot.example.test", "wss://bot.example.test/device"},
		{"http://127.0.0.1:4630", "ws://127.0.0.1:4630/device"},
		{"wss://openvibe.computer", "wss://openvibe.computer/device"},
	} {
		got, err := DeviceURL(c.server)
		if err != nil || got != c.want {
			t.Fatalf("DeviceURL(%q) = %q, %v; want %q", c.server, got, err, c.want)
		}
	}
}
