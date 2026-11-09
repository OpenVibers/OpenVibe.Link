package egress

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Unknown OS readings do not invent a metered/battery state. Known readings pause immediately.
func Paused() bool {
	if runtime.GOOS == "linux" {
		paths, _ := filepath.Glob("/sys/class/power_supply/*/status")
		for _, p := range paths {
			b, _ := os.ReadFile(p)
			if strings.TrimSpace(string(b)) == "Discharging" {
				return true
			}
		}
		out := probe("nmcli", "-t", "-f", "GENERAL.METERED", "device", "show")
		for _, line := range strings.Split(out, "\n") {
			_, v, _ := strings.Cut(line, ":")
			if strings.HasPrefix(strings.TrimSpace(v), "yes") {
				return true
			}
		}
	}
	if runtime.GOOS == "darwin" {
		return strings.Contains(probe("pmset", "-g", "batt"), "'Battery Power'")
	}
	if runtime.GOOS == "windows" {
		return strings.TrimSpace(probe("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", `Add-Type -AssemblyName System.Windows.Forms; if ([System.Windows.Forms.SystemInformation]::PowerStatus.PowerLineStatus -eq 'Offline') {'pause'; exit}; $p = [Windows.Networking.Connectivity.NetworkInformation, Windows.Networking.Connectivity, ContentType=WindowsRuntime]::GetInternetConnectionProfile(); if ($p) {$c=$p.GetConnectionCost(); if ($c.NetworkCostType -eq 'Fixed' -or $c.NetworkCostType -eq 'Variable' -or $c.Roaming -or $c.OverDataLimit) {'pause'}}`)) == "pause"
	}
	return false
}
func probe(name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	b, _ := exec.CommandContext(ctx, name, args...).Output()
	return string(b)
}
