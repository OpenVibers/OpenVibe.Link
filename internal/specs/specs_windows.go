//go:build windows

package specs

import (
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// memoryStatusEx is MEMORYSTATUSEX (GlobalMemoryStatusEx), which x/sys/windows does not wrap.
type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

var globalMemoryStatusEx = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")

func readOS(m *Machine) {
	m.Display = "windows"
	v := windows.RtlGetVersion()
	m.OSVersion = fmt.Sprintf("%d.%d.%d", v.MajorVersion, v.MinorVersion, v.BuildNumber)
	m.Kernel = "NT " + m.OSVersion
	m.OSName = "Windows"
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE); err == nil {
		if name, _, err := k.GetStringValue("ProductName"); err == nil && name != "" {
			// Windows 11 still says "Windows 10" here; build 22000 and later is 11.
			if v.BuildNumber >= 22000 {
				name = strings.Replace(name, "Windows 10", "Windows 11", 1)
			}
			m.OSName = name
		}
		k.Close()
	}
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `HARDWARE\DESCRIPTION\System\CentralProcessor\0`, registry.QUERY_VALUE); err == nil {
		if name, _, err := k.GetStringValue("ProcessorNameString"); err == nil {
			m.CPU.Model = name
		}
		k.Close()
	}
	var ms memoryStatusEx
	ms.Length = uint32(unsafe.Sizeof(ms))
	if r, _, _ := globalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&ms))); r != 0 {
		m.MemoryMB = int64(ms.TotalPhys >> 20)
	}
	m.GPUs = windowsGPUs()
	// Hyper-V's compute service (Hyper-V, Windows Sandbox, WSL 2) and WSL itself.
	m.Virtualization.HyperV = serviceExists("vmcompute")
	m.Virtualization.WSL = serviceExists("LxssManager") || serviceExists("WSLService")
	// A guest by its system model ("Virtual Machine", "VMware Virtual Platform"): HypervisorPresent is true on a Hyper-V
	// host too, so it cannot tell.
	if model := regString(`HARDWARE\DESCRIPTION\System\BIOS`, "SystemProductName"); strings.Contains(strings.ToLower(model), "virtual") {
		m.Virtualization.Guest = true
	}
}

// windowsGPUs reads the display adapters' class key: each one's name and dedicated memory.
func windowsGPUs() []GPU {
	const class = `SYSTEM\CurrentControlSet\Control\Class\{4d36e968-e325-11ce-bfc1-08002be10318}`
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, class, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return nil
	}
	defer k.Close()
	names, _ := k.ReadSubKeyNames(32)
	var out []GPU
	for _, n := range names {
		if len(n) != 4 { // 0000, 0001 …: the adapters; "Properties" and others are not
			continue
		}
		sk, err := registry.OpenKey(registry.LOCAL_MACHINE, class+`\`+n, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		desc, _, _ := sk.GetStringValue("DriverDesc")
		vendor, _, _ := sk.GetStringValue("ProviderName")
		g := GPU{Model: desc, Vendor: shortVendorName(vendor, desc)}
		if q, _, err := sk.GetIntegerValue("HardwareInformation.qwMemorySize"); err == nil {
			g.VRAMMB = int64(q >> 20)
		} else if b, _, err := sk.GetBinaryValue("HardwareInformation.MemorySize"); err == nil && len(b) >= 4 {
			g.VRAMMB = int64(uint32(b[0])|uint32(b[1])<<8|uint32(b[2])<<16|uint32(b[3])<<24) >> 20
		}
		sk.Close()
		if g.Model != "" && !strings.Contains(g.Model, "Basic Display") {
			out = append(out, g)
		}
	}
	return out
}

func shortVendorName(provider, desc string) string {
	l := strings.ToLower(provider + " " + desc)
	switch {
	case strings.Contains(l, "nvidia"):
		return "NVIDIA"
	case strings.Contains(l, "amd") || strings.Contains(l, "radeon") || strings.Contains(l, "advanced micro"):
		return "AMD"
	case strings.Contains(l, "intel"):
		return "Intel"
	case strings.Contains(l, "qualcomm") || strings.Contains(l, "adreno"):
		return "Qualcomm"
	}
	return ""
}

func serviceExists(name string) bool {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Services\`+name, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	k.Close()
	return true
}

func regString(path, value string) string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	s, _, _ := k.GetStringValue(value)
	return s
}
