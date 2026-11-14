//go:build linux

package specs

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// root is where the Linux reader finds /proc, /sys, /etc and /dev (tests point it at a fixture).
var root = "/"

func readOS(m *Machine) {
	p := func(rel string) string { return filepath.Join(root, rel) }
	// The distribution, from os-release.
	if b, err := os.ReadFile(p("etc/os-release")); err == nil {
		kv := map[string]string{}
		for _, l := range strings.Split(string(b), "\n") {
			if k, v, ok := strings.Cut(l, "="); ok {
				kv[k] = strings.Trim(v, `"'`)
			}
		}
		m.OSName, m.OSVersion = kv["NAME"], kv["VERSION_ID"]
	}
	if m.OSName == "" {
		m.OSName = "Linux"
	}
	if root == "/" {
		var u unix.Utsname
		if unix.Uname(&u) == nil {
			m.Kernel = unix.ByteSliceToString(u.Release[:])
		}
	} else if b, err := os.ReadFile(p("proc/sys/kernel/osrelease")); err == nil {
		m.Kernel = strings.TrimSpace(string(b))
	}
	// The CPU: its model and its physical cores (distinct physical id × core id), threads from the processor lines.
	if b, err := os.ReadFile(p("proc/cpuinfo")); err == nil {
		cores := map[string]bool{}
		threads, phys := 0, ""
		for _, l := range strings.Split(string(b), "\n") {
			k, v, ok := strings.Cut(l, ":")
			if !ok {
				continue
			}
			k, v = strings.TrimSpace(k), strings.TrimSpace(v)
			switch k {
			case "processor":
				threads++
			case "model name", "Model", "cpu model":
				if m.CPU.Model == "" {
					m.CPU.Model = v
				}
			case "physical id":
				phys = v
			case "core id":
				cores[phys+"/"+v] = true
			}
		}
		if threads > 0 {
			m.CPU.Threads = threads
		}
		if len(cores) > 0 {
			m.CPU.Cores = len(cores)
		}
	}
	if b, err := os.ReadFile(p("proc/meminfo")); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if f := strings.Fields(l); len(f) >= 2 && f[0] == "MemTotal:" {
				if kb, err := strconv.ParseInt(f[1], 10, 64); err == nil {
					m.MemoryMB = kb / 1024
				}
			}
		}
	}
	m.GPUs = linuxGPUs(p)
	// Virtualization: /dev/kvm for microVMs; the hypervisor flag and the DMI vendor for a guest; WSL and containers.
	if _, err := os.Stat(p("dev/kvm")); err == nil {
		m.Virtualization.KVM = true
	}
	if b, err := os.ReadFile(p("proc/cpuinfo")); err == nil && strings.Contains(string(b), " hypervisor") {
		m.Virtualization.Guest = true
	}
	if b, err := os.ReadFile(p("proc/version")); err == nil && strings.Contains(strings.ToLower(string(b)), "microsoft") {
		m.Virtualization.WSL, m.Virtualization.Guest = true, true
	}
	for _, f := range []string{".dockerenv", "run/.containerenv"} {
		if _, err := os.Stat(p(f)); err == nil {
			m.Virtualization.Container = true
		}
	}
	switch {
	case os.Getenv("WAYLAND_DISPLAY") != "":
		m.Display = "wayland"
	case os.Getenv("DISPLAY") != "":
		m.Display = "x11"
	default:
		m.Display = "none"
	}
}

// linuxGPUs reads the display adapters from /sys (vendor, driver, AMD's VRAM), named by lspci and sized by nvidia-smi
// where those tools exist.
func linuxGPUs(p func(string) string) []GPU {
	var out []GPU
	cards, _ := filepath.Glob(p("sys/class/drm/card[0-9]*"))
	seen := map[string]bool{}
	for _, c := range cards {
		if strings.Contains(filepath.Base(c), "-") { // card0-HDMI-A-1: a connector, not a card
			continue
		}
		dev := filepath.Join(c, "device")
		real, err := filepath.EvalSymlinks(dev)
		if err != nil || seen[real] {
			continue
		}
		seen[real] = true
		vendor, _ := os.ReadFile(filepath.Join(dev, "vendor"))
		g := GPU{Vendor: vendorOf(string(vendor))}
		if b, err := os.ReadFile(filepath.Join(dev, "uevent")); err == nil {
			for _, l := range strings.Split(string(b), "\n") {
				if d, ok := strings.CutPrefix(l, "DRIVER="); ok {
					g.Model = d
				}
			}
		}
		if b, err := os.ReadFile(filepath.Join(dev, "mem_info_vram_total")); err == nil {
			if n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); err == nil {
				g.VRAMMB = n >> 20
			}
		}
		if g.Vendor != "" || g.Model != "" {
			out = append(out, g)
		}
	}
	if root != "/" {
		return out
	}
	// Names: lspci's VGA and 3D controllers, in the same order as the cards (best effort).
	if s := run("lspci", "-mm"); s != "" {
		var names []GPU
		for _, l := range strings.Split(s, "\n") {
			f := quoted(l)
			if len(f) >= 4 && (strings.Contains(f[1], "VGA") || strings.Contains(f[1], "3D") || strings.Contains(f[1], "Display")) {
				names = append(names, GPU{Vendor: shortVendor(f[2]), Model: f[3]})
			}
		}
		if len(names) == len(out) {
			for i := range out {
				if out[i].Vendor == "" {
					out[i].Vendor = names[i].Vendor
				}
				out[i].Model = names[i].Model
			}
		} else if len(out) == 0 {
			out = names
		}
	}
	// NVIDIA's own tool names and sizes its cards.
	if s := run("nvidia-smi", "--query-gpu=name,memory.total", "--format=csv,noheader,nounits"); s != "" {
		var nv []GPU
		for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
			name, mem, ok := strings.Cut(l, ",")
			if !ok {
				continue
			}
			mb, _ := strconv.ParseInt(strings.TrimSpace(mem), 10, 64)
			nv = append(nv, GPU{Vendor: "NVIDIA", Model: strings.TrimSpace(name), VRAMMB: mb})
		}
		kept := out[:0]
		for _, g := range out {
			if g.Vendor != "NVIDIA" {
				kept = append(kept, g)
			}
		}
		out = append(kept, nv...)
	}
	return out
}

// quoted splits one lspci -mm line into its fields (each in double quotes, or bare).
func quoted(l string) []string {
	var out []string
	for l = strings.TrimSpace(l); l != ""; l = strings.TrimSpace(l) {
		if l[0] == '"' {
			end := strings.IndexByte(l[1:], '"')
			if end < 0 {
				return out
			}
			out = append(out, l[1:end+1])
			l = l[end+2:]
			continue
		}
		end := strings.IndexByte(l, ' ')
		if end < 0 {
			return append(out, l)
		}
		out = append(out, l[:end])
		l = l[end:]
	}
	return out
}

func shortVendor(s string) string {
	l := strings.ToLower(s)
	switch {
	case strings.Contains(l, "nvidia"):
		return "NVIDIA"
	case strings.Contains(l, "amd") || strings.Contains(l, "ati"):
		return "AMD"
	case strings.Contains(l, "intel"):
		return "Intel"
	}
	return s
}
