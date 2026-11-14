//go:build darwin

package specs

import (
	"encoding/json"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func readOS(m *Machine) {
	m.OSName, m.Display = "macOS", "quartz"
	if v, err := unix.Sysctl("kern.osproductversion"); err == nil {
		m.OSVersion = v
	}
	if v, err := unix.Sysctl("kern.osrelease"); err == nil {
		m.Kernel = "Darwin " + v
	}
	if v, err := unix.Sysctl("hw.model"); err == nil {
		m.Model = v
	}
	if v, err := unix.Sysctl("machdep.cpu.brand_string"); err == nil {
		m.CPU.Model = v
	}
	if n, err := unix.SysctlUint32("hw.physicalcpu"); err == nil {
		m.CPU.Cores = int(n)
	}
	if n, err := unix.SysctlUint32("hw.logicalcpu"); err == nil && n > 0 {
		m.CPU.Threads = int(n)
	}
	if n, err := unix.SysctlUint64("hw.memsize"); err == nil {
		m.MemoryMB = int64(n >> 20)
	}
	// Apple's Virtualization framework: macOS 11 and later on Apple silicon, or an Intel Mac with the hypervisor.
	major, _ := strconv.Atoi(strings.SplitN(m.OSVersion, ".", 2)[0])
	hv, _ := unix.SysctlUint32("kern.hv_support")
	m.Virtualization.AppleVZ = major >= 11 && (m.Arch == "arm64" || hv == 1)
	if v, err := unix.SysctlUint32("kern.hv_vmm_present"); err == nil && v == 1 {
		m.Virtualization.Guest = true
	}
	m.GPUs = macGPUs(m)
}

// macGPUs reads system_profiler's displays: each adapter's model and VRAM; Apple silicon shares the machine's memory.
func macGPUs(m *Machine) []GPU {
	s := run("system_profiler", "-json", "SPDisplaysDataType")
	if s == "" {
		return nil
	}
	var doc struct {
		Displays []map[string]any `json:"SPDisplaysDataType"`
	}
	if json.Unmarshal([]byte(s), &doc) != nil {
		return nil
	}
	var out []GPU
	for _, d := range doc.Displays {
		g := GPU{}
		if v, ok := d["sppci_model"].(string); ok {
			g.Model = v
		} else if v, ok := d["_name"].(string); ok {
			g.Model = v
		}
		if v, ok := d["spdisplays_vendor"].(string); ok {
			g.Vendor = strings.TrimPrefix(strings.TrimPrefix(v, "sppci_vendor_"), "spdisplays_vendor_")
		}
		for _, k := range []string{"spdisplays_vram", "spdisplays_vram_shared", "sppci_vram"} {
			if v, ok := d[k].(string); ok {
				g.VRAMMB = vramMB(v)
				break
			}
		}
		if g.Vendor == "" && strings.HasPrefix(g.Model, "Apple") {
			g.Vendor = "Apple"
		}
		if g.Model != "" {
			out = append(out, g)
		}
	}
	return out
}

// vramMB reads "8 GB", "1536 MB".
func vramMB(s string) int64 {
	f := strings.Fields(s)
	if len(f) < 2 {
		return 0
	}
	n, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return 0
	}
	if strings.HasPrefix(strings.ToUpper(f[1]), "G") {
		n *= 1024
	}
	return int64(n)
}
