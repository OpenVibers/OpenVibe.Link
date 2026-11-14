//go:build linux

package specs

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLinuxReading reads a fixture system: a distribution, a 4-core 8-thread CPU, 16 GB, an Intel and an AMD card (with
// its VRAM), KVM, a guest's hypervisor flag; and Read fills in the rest without failing.
func TestLinuxReading(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("etc/os-release", "NAME=\"Ubuntu\"\nVERSION_ID=\"26.04\"\nPRETTY_NAME=\"Ubuntu 26.04 LTS\"\n")
	write("proc/sys/kernel/osrelease", "7.0.0-34-generic\n")
	cpu := ""
	for i := 0; i < 8; i++ {
		cpu += "processor\t: " + string(rune('0'+i)) + "\nmodel name\t: Intel(R) Core(TM) i7-7700 CPU @ 3.60GHz\nphysical id\t: 0\ncore id\t\t: " + string(rune('0'+i%4)) + "\nflags\t\t: fpu vme hypervisor\n\n"
	}
	write("proc/cpuinfo", cpu)
	write("proc/meminfo", "MemTotal:       16314884 kB\nMemFree:  100 kB\n")
	write("proc/version", "Linux version 7.0.0-34-generic\n")
	write("dev/kvm", "")
	for card, body := range map[string][2]string{"card0": {"0x8086", "DRIVER=i915"}, "card1": {"0x1002", "DRIVER=amdgpu"}} {
		dev := filepath.Join("sys/devices/pci0000:00", card)
		write(filepath.Join(dev, "vendor"), body[0]+"\n")
		write(filepath.Join(dev, "uevent"), body[1]+"\nPCI_ID=1\n")
		if card == "card1" {
			write(filepath.Join(dev, "mem_info_vram_total"), "4294967296\n")
		}
		if err := os.MkdirAll(filepath.Join(dir, "sys/class/drm", card), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(dir, dev), filepath.Join(dir, "sys/class/drm", card, "device")); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "sys/class/drm/card0-HDMI-A-1"), 0o755); err != nil { // a connector, not a card
		t.Fatal(err)
	}
	old := root
	root = dir
	defer func() { root = old }()

	m := Read(dir)
	if m.OSName != "Ubuntu" || m.OSVersion != "26.04" || m.Kernel != "7.0.0-34-generic" {
		t.Fatalf("os: %q %q %q", m.OSName, m.OSVersion, m.Kernel)
	}
	if m.CPU.Model != "Intel(R) Core(TM) i7-7700 CPU @ 3.60GHz" || m.CPU.Cores != 4 || m.CPU.Threads != 8 {
		t.Fatalf("cpu: %+v", m.CPU)
	}
	if m.MemoryMB != 15932 {
		t.Fatalf("memory: %d", m.MemoryMB)
	}
	if len(m.GPUs) != 2 || m.GPUs[0].Vendor != "AMD" || m.GPUs[0].VRAMMB != 4096 || m.GPUs[1].Vendor != "Intel" {
		t.Fatalf("gpus (largest first): %+v", m.GPUs)
	}
	if !m.Virtualization.KVM || !m.Virtualization.Guest || m.Virtualization.WSL {
		t.Fatalf("virtualization: %+v", m.Virtualization)
	}
	if m.DiskTotalGB <= 0 || m.DiskFreeGB <= 0 || m.Arch == "" {
		t.Fatalf("disk and arch: %+v", m)
	}
}

// TestNothingToRead: an empty root still answers, with the generic name and the thread count Go knows.
func TestNothingToRead(t *testing.T) {
	old := root
	root = t.TempDir()
	defer func() { root = old }()
	m := Read("")
	if m.OSName != "Linux" || m.CPU.Threads < 1 || len(m.GPUs) != 0 || m.DiskTotalGB != 0 {
		t.Fatalf("%+v", m)
	}
}

func TestClipAndVendors(t *testing.T) {
	if got := clip("  a\x1b[31mb\n  ", 10); got != "a[31mb" {
		t.Fatalf("clip: %q", got)
	}
	if vendorOf("0x10DE") != "NVIDIA" || vendorOf("0x1002") != "AMD" || vendorOf("0xffff") != "" {
		t.Fatal("vendors")
	}
	if f := quoted(`00:02.0 "VGA compatible controller" "Intel Corporation" "HD Graphics 630" -r04 "Dell" "Device 07a1"`); len(f) < 4 || f[3] != "HD Graphics 630" {
		t.Fatalf("lspci: %q", f)
	}
}
