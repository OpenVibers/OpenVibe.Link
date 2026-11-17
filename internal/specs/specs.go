// Package specs reads what a machine is (Link v0.66, 2026-11-14: the owner wants every operating system supported and
// "aware of specs"): its operating system and version, CPU, memory, GPUs, free disk and the virtualization it offers.
// OpenVibe sizes what it asks of a machine by it (desks, jobs, models, lent desktops in a microVM or a sandbox), placement
// prefers the machines that fit, and the owner's machine card says what each one is.
//
// Everything is best effort and bounded: a probe that cannot answer leaves its field empty, a slow tool (lspci,
// nvidia-smi, system_profiler) gets ProbeTimeout, and Read never fails. Nothing personal is read: no serial numbers,
// no user names, no host names beyond what the Link already reports. Addresses are only the machine's own private ones
// (lan.go), never a public one.
package specs

import (
	"context"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"
)

// ProbeTimeout bounds each external tool a reader runs.
var ProbeTimeout = 4 * time.Second

// CPU is the processor: its model as the system names it, its physical cores and its hardware threads.
type CPU struct {
	Model   string `json:"model,omitempty"`
	Cores   int    `json:"cores,omitempty"`
	Threads int    `json:"threads"`
}

// GPU is one graphics adapter: its maker, its model and its own memory (0 when unknown or shared).
type GPU struct {
	Vendor string `json:"vendor,omitempty"`
	Model  string `json:"model"`
	VRAMMB int64  `json:"vram_mb,omitempty"`
}

// Virtualization is what the machine can run isolated work in, beyond the Link's own sandbox: KVM (Linux microVMs:
// Firecracker, Cloud Hypervisor), Apple's Virtualization framework (macOS VMs), Hyper-V or Windows Sandbox, WSL; and
// whether the Link itself runs inside a VM or a container.
type Virtualization struct {
	KVM       bool `json:"kvm,omitempty"`
	AppleVZ   bool `json:"apple_vz,omitempty"`
	HyperV    bool `json:"hyperv,omitempty"`
	WSL       bool `json:"wsl,omitempty"`
	Guest     bool `json:"guest,omitempty"`     // the Link runs inside a virtual machine
	Container bool `json:"container,omitempty"` // the Link runs inside a container
}

// Machine is the whole reading, sent with the Link's status as `machine`.
type Machine struct {
	OSName         string         `json:"os_name,omitempty"`    // "Ubuntu", "macOS", "Windows 11 Pro"
	OSVersion      string         `json:"os_version,omitempty"` // "26.04", "16.1", "10.0.26200"
	Kernel         string         `json:"kernel,omitempty"`
	Arch           string         `json:"arch"`
	Model          string         `json:"model,omitempty"` // a Mac's model ("Mac16,10"); empty elsewhere
	CPU            CPU            `json:"cpu"`
	MemoryMB       int64          `json:"memory_mb,omitempty"`
	GPUs           []GPU          `json:"gpus,omitempty"`
	DiskTotalGB    float64        `json:"disk_total_gb,omitempty"` // the volume of the Link's state directory
	DiskFreeGB     float64        `json:"disk_free_gb,omitempty"`
	Virtualization Virtualization `json:"virtualization"`
	Display        string         `json:"display,omitempty"` // x11, wayland, quartz, windows or none (a service with no screen)
	LAN            []string       `json:"lan,omitempty"`     // its addresses on its own networks (lan.go): where Computer finds it
}

// Read is the machine now. dataDir is the volume whose space is reported (the Link's state directory).
func Read(dataDir string) Machine {
	m := Machine{Arch: runtime.GOARCH, CPU: CPU{Threads: runtime.NumCPU()}}
	readOS(&m)
	m.LAN = lanAddresses()
	if dataDir != "" {
		m.DiskTotalGB, m.DiskFreeGB = diskGB(dataDir)
	}
	sort.SliceStable(m.GPUs, func(i, j int) bool { return m.GPUs[i].VRAMMB > m.GPUs[j].VRAMMB })
	if len(m.GPUs) > 8 {
		m.GPUs = m.GPUs[:8]
	}
	for i := range m.GPUs {
		m.GPUs[i].Model = clip(m.GPUs[i].Model, 120)
		m.GPUs[i].Vendor = clip(m.GPUs[i].Vendor, 40)
	}
	m.OSName, m.OSVersion, m.Kernel, m.Model = clip(m.OSName, 80), clip(m.OSVersion, 40), clip(m.Kernel, 80), clip(m.Model, 60)
	m.CPU.Model = clip(strings.Join(strings.Fields(m.CPU.Model), " "), 120)
	return m
}

// run is one bounded external probe: its stdout, or "" when it is missing, fails or is slow.
func run(name string, args ...string) string {
	path, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), ProbeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, args...).Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// vendorOf names a PCI vendor id.
func vendorOf(id string) string {
	switch strings.ToLower(strings.TrimPrefix(strings.TrimSpace(id), "0x")) {
	case "10de":
		return "NVIDIA"
	case "1002", "1022":
		return "AMD"
	case "8086":
		return "Intel"
	case "106b":
		return "Apple"
	case "1af4":
		return "Virtio"
	case "15ad":
		return "VMware"
	case "1414":
		return "Microsoft"
	case "1a03":
		return "ASPEED"
	}
	return ""
}

func clip(s string, n int) string {
	s = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s))
	if len(s) > n {
		return s[:n]
	}
	return s
}

func round1(f float64) float64 { return float64(int64(f*10+0.5)) / 10 }
