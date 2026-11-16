// Package doctor checks whether this machine can run the Dojo labs.
package doctor

import (
	"errors"
	"fmt"

	"github.com/kibanbuild/dojo/internal/lab"
)

// Status of one check.
type Status int

const (
	OK Status = iota
	Warn
	Fail
)

// Result is one line of the report.
type Result struct {
	Status Status
	Text   string
	Fix    string
}

// Machine is what the checks need to know about the host.
type Machine struct {
	OS             string
	MemoryBytes    uint64
	FreeDiskBytes  uint64
	CPUs           int
	Virtualization bool
	// VirtualizationKnown is false when the probe could not tell.
	VirtualizationKnown bool
	// KVMDenied is set on Linux when /dev/kvm exists but this user cannot open it.
	KVMDenied bool
	// KVMMissing is set on Linux x86 when the CPU supports virtualisation but /dev/kvm does not exist.
	KVMMissing bool
	HasGit     bool
	// GitUnknown is set when git did not answer in time.
	GitUnknown        bool
	GitName, GitEmail string
	HasVSCode         bool
}

const gib = 1 << 30

// gb formats bytes as GB rounded down to a tenth, so a shown value never
// reaches a threshold the machine has not.
func gb(b uint64) string {
	t := b * 10 / gib
	if t%10 == 0 {
		return fmt.Sprint(t / 10)
	}
	return fmt.Sprintf("%d.%d", t/10, t%10)
}

// Thresholds, from the lab VM's footprint: 2 CPUs, 2 GiB RAM, 10 GiB disk.
const (
	minMemory = 7.5 * gib // "8 GB" machines report a little less
	recMemory = 15 * gib
	minDisk   = 15 * gib
	recDisk   = 30 * gib
	minCPUs   = 2
	recCPUs   = 4
)

// Evaluate turns machine facts and the VM manager's state into a report.
func Evaluate(m Machine, backendName string, backendErr error) []Result {
	var rs []Result

	switch {
	case m.MemoryBytes == 0:
		rs = append(rs, Result{Warn, "could not read the amount of RAM", ""})
	case m.MemoryBytes < minMemory:
		rs = append(rs, Result{Fail, fmt.Sprintf("%s GB RAM, the labs need at least 8 GB", gb(m.MemoryBytes)), "the labs need at least 8 GB of RAM on this computer"})
	case m.MemoryBytes < recMemory:
		rs = append(rs, Result{Warn, fmt.Sprintf("%s GB RAM: enough, close other apps while a lab runs", gb(m.MemoryBytes)), ""})
	default:
		rs = append(rs, Result{OK, fmt.Sprintf("%s GB RAM", gb(m.MemoryBytes)), ""})
	}

	switch {
	case m.FreeDiskBytes == 0:
		rs = append(rs, Result{Warn, "could not read the free disk space", ""})
	case m.FreeDiskBytes < minDisk:
		rs = append(rs, Result{Fail, fmt.Sprintf("%s GB free disk, the labs need at least 15 GB", gb(m.FreeDiskBytes)), "free some space and run dojo doctor again"})
	case m.FreeDiskBytes < recDisk:
		rs = append(rs, Result{Warn, fmt.Sprintf("%s GB free disk: enough for now, 30 GB is comfortable", gb(m.FreeDiskBytes)), ""})
	default:
		rs = append(rs, Result{OK, fmt.Sprintf("%s GB free disk", gb(m.FreeDiskBytes)), ""})
	}

	switch {
	case m.CPUs < minCPUs:
		rs = append(rs, Result{Fail, fmt.Sprintf("%d CPU core, the labs need 2", m.CPUs), "the labs need at least 2 CPU cores on this computer"})
	case m.CPUs < recCPUs:
		rs = append(rs, Result{Warn, fmt.Sprintf("%d CPU cores: enough, labs will be a bit slow", m.CPUs), ""})
	default:
		rs = append(rs, Result{OK, fmt.Sprintf("%d CPU cores", m.CPUs), ""})
	}

	switch {
	case m.KVMDenied:
		rs = append(rs, Result{Warn, "/dev/kvm exists but your user cannot open it", "add yourself to the kvm group (sudo usermod -aG kvm $USER), then log out and back in"})
	case m.KVMMissing:
		rs = append(rs, Result{Fail, "the CPU supports virtualisation but /dev/kvm is missing", "load the kvm module (sudo modprobe kvm_intel or kvm_amd), or enable virtualisation in the BIOS/UEFI"})
	case !m.VirtualizationKnown:
		rs = append(rs, Result{Warn, "could not tell whether CPU virtualisation is enabled", "if the lab fails to start, check VT-x/AMD-V in your BIOS"})
	case !m.Virtualization:
		rs = append(rs, Result{Fail, "CPU virtualisation is disabled", "enable VT-x (Intel) or AMD-V/SVM (AMD) in your BIOS/UEFI settings"})
	default:
		rs = append(rs, Result{OK, "CPU virtualisation is enabled", ""})
	}

	switch {
	case m.OS == "windows":
		rs = append(rs, Result{Fail, "Windows labs are not supported yet", "Windows support (WSL2) is coming; for now the labs run on macOS and Linux"})
	case errors.Is(backendErr, lab.ErrNotInstalled):
		rs = append(rs, Result{Fail, fmt.Sprintf("VM manager (%s) not ready: %v", backendName, backendErr), "install Lima: https://lima-vm.io/docs/installation/ (macOS: brew install lima)"})
	case backendErr != nil:
		rs = append(rs, Result{Fail, fmt.Sprintf("VM manager (%s) not ready: %v", backendName, backendErr), ""})
	default:
		rs = append(rs, Result{OK, fmt.Sprintf("VM manager ready (%s)", backendName), ""})
	}

	switch {
	case m.GitUnknown:
		rs = append(rs, Result{Warn, "could not check git: it did not answer in time", ""})
	case !m.HasGit:
		rs = append(rs, Result{Fail, "git is not installed", "install it: https://git-scm.com/downloads"})
	default:
		rs = append(rs, Result{OK, "git is installed", ""})
		if m.GitName == "" || m.GitEmail == "" {
			rs = append(rs, Result{Warn, "git has no name or email yet",
				`git config --global user.name "Your Name" && git config --global user.email "you@example.com"`})
		} else {
			rs = append(rs, Result{OK, "git identity: " + m.GitName + " <" + m.GitEmail + ">", ""})
		}
	}
	if !m.HasVSCode {
		rs = append(rs, Result{Warn, "`code` command not found (optional, any editor works)", "install VS Code and its shell command: https://code.visualstudio.com"})
	} else {
		rs = append(rs, Result{OK, "`code` command found", ""})
	}
	return rs
}

// Ready reports whether no check failed.
func Ready(rs []Result) bool {
	for _, r := range rs {
		if r.Status == Fail {
			return false
		}
	}
	return true
}
