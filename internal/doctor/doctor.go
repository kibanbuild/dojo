// Package doctor checks whether this machine can run the Dojo labs.
package doctor

import (
	"errors"
	"fmt"
	"strings"

	"github.com/kibanyadev/dojo/internal/lab"
)

// Status of one check.
type Status int

const (
	OK Status = iota
	Warn
	Fail
	// Unknown is a check that could not be judged: GitHub unreachable or limiting requests.
	Unknown
)

// String is the status as the doctor.run event spells it.
func (s Status) String() string {
	switch s {
	case OK:
		return "ok"
	case Warn:
		return "warn"
	case Unknown:
		return "unknown"
	default:
		return "fail"
	}
}

// Group says what a result gates: only machine results decide Ready.
type Group string

const (
	GroupMachine Group = "machine"
	GroupKey     Group = "key"
	GroupRepo    Group = "repo"
	GroupAdvice  Group = "advisory"
)

// Result is one line of the report.
type Result struct {
	// ID is the check id of the wire contract (memory, disk, ssh_key ...).
	ID     string
	Group  Group
	Status Status
	Text   string
	Fix    string
}

func res(id string, g Group, s Status, text, fix string) Result {
	return Result{ID: id, Group: g, Status: s, Text: text, Fix: fix}
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
	// GitMajor and GitMinor are 0 when the git version could not be read.
	GitMajor, GitMinor int
	// GitSigningFormat, GitSigningKey and GitSignCommits are the global git settings, as written.
	GitSigningFormat, GitSigningKey, GitSignCommits string
	HasVSCode                                       bool
	HasSSHKeygen                                    bool
	// SSHKeyPath is the private key dojo uses; empty when none was found or the chosen one is unusable.
	SSHKeyPath string
	// SSHKeyErr says why no key could be used.
	SSHKeyErr error
	// SigningKeyIsSSHKey is set when git's signing key is the file or literal key at SSHKeyPath.
	SigningKeyIsSSHKey bool
	// SigningPub is the public key git signs with (the dojo key when git names none); empty when unknown.
	SigningPub string
	// CloneOverSSH is set when the practice clone's origin, or the default clone URL, is SSH.
	CloneOverSSH bool
	// SSHOfferedKeys are the public keys ssh offers for github.com; only read when CloneOverSSH.
	SSHOfferedKeys []string
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
	add := func(id string, g Group, s Status, text, fix string) { rs = append(rs, res(id, g, s, text, fix)) }
	const mach = GroupMachine

	switch {
	case m.MemoryBytes == 0:
		add("memory", mach, Warn, "could not read the amount of RAM", "")
	case m.MemoryBytes < minMemory:
		add("memory", mach, Fail, fmt.Sprintf("%s GB RAM, the labs need at least 8 GB", gb(m.MemoryBytes)), "the labs need at least 8 GB of RAM on this computer")
	case m.MemoryBytes < recMemory:
		add("memory", mach, Warn, fmt.Sprintf("%s GB RAM: enough, close other apps while a lab runs", gb(m.MemoryBytes)), "")
	default:
		add("memory", mach, OK, fmt.Sprintf("%s GB RAM", gb(m.MemoryBytes)), "")
	}

	switch {
	case m.FreeDiskBytes == 0:
		add("disk", mach, Warn, "could not read the free disk space", "")
	case m.FreeDiskBytes < minDisk:
		add("disk", mach, Fail, fmt.Sprintf("%s GB free disk, the labs need at least 15 GB", gb(m.FreeDiskBytes)), "free some space and run dojo doctor again")
	case m.FreeDiskBytes < recDisk:
		add("disk", mach, Warn, fmt.Sprintf("%s GB free disk: enough for now, 30 GB is comfortable", gb(m.FreeDiskBytes)), "")
	default:
		add("disk", mach, OK, fmt.Sprintf("%s GB free disk", gb(m.FreeDiskBytes)), "")
	}

	switch {
	case m.CPUs < minCPUs:
		add("cpu", mach, Fail, fmt.Sprintf("%d CPU core, the labs need 2", m.CPUs), "the labs need at least 2 CPU cores on this computer")
	case m.CPUs < recCPUs:
		add("cpu", mach, Warn, fmt.Sprintf("%d CPU cores: enough, labs will be a bit slow", m.CPUs), "")
	default:
		add("cpu", mach, OK, fmt.Sprintf("%d CPU cores", m.CPUs), "")
	}

	switch {
	case m.KVMDenied:
		add("virtualization", mach, Warn, "/dev/kvm exists but your user cannot open it", "add yourself to the kvm group (sudo usermod -aG kvm $USER), then log out and back in")
	case m.KVMMissing:
		add("virtualization", mach, Fail, "the CPU supports virtualisation but /dev/kvm is missing", "load the kvm module (sudo modprobe kvm_intel or kvm_amd), or enable virtualisation in the BIOS/UEFI")
	case !m.VirtualizationKnown:
		add("virtualization", mach, Warn, "could not tell whether CPU virtualisation is enabled", "if the lab fails to start, check VT-x/AMD-V in your BIOS")
	case !m.Virtualization:
		add("virtualization", mach, Fail, "CPU virtualisation is disabled", "enable VT-x (Intel) or AMD-V/SVM (AMD) in your BIOS/UEFI settings")
	default:
		add("virtualization", mach, OK, "CPU virtualisation is enabled", "")
	}

	switch {
	case m.OS == "windows":
		add("vm_manager", mach, Fail, "Windows labs are not supported yet", "Windows support (WSL2) is coming; for now the labs run on macOS and Linux")
	case errors.Is(backendErr, lab.ErrNotInstalled):
		add("vm_manager", mach, Fail, fmt.Sprintf("VM manager (%s) not ready: %v", backendName, backendErr), "install Lima: https://lima-vm.io/docs/installation/ (macOS: brew install lima)")
	case backendErr != nil:
		add("vm_manager", mach, Fail, fmt.Sprintf("VM manager (%s) not ready: %v", backendName, backendErr), "")
	default:
		add("vm_manager", mach, OK, fmt.Sprintf("VM manager ready (%s)", backendName), "")
	}

	switch {
	case m.GitUnknown:
		add("git", mach, Warn, "could not check git: it did not answer in time", "")
	case !m.HasGit:
		add("git", mach, Fail, "git is not installed", "install it: https://git-scm.com/downloads")
	default:
		add("git", mach, OK, "git is installed", "")
		if m.GitName == "" || m.GitEmail == "" {
			add("git_identity", mach, Warn, "git has no name or email yet",
				`git config --global user.name "Your Name" && git config --global user.email "you@example.com"`)
		} else {
			add("git_identity", mach, OK, "git identity: "+m.GitName+" <"+m.GitEmail+">", "")
		}
	}
	rs = append(rs, sshChecks(m)...)
	if !m.HasVSCode {
		add("editor", mach, Warn, "`code` command not found (optional, any editor works)", "install VS Code and its shell command: https://code.visualstudio.com")
	} else {
		add("editor", mach, OK, "`code` command found", "")
	}
	return rs
}

// Ready reports whether no machine check failed. Key and repo results never
// block: they fail while a step is undone, and the page walks the learner through them.
func Ready(rs []Result) bool {
	for _, r := range rs {
		if r.Group == GroupMachine && r.Status == Fail {
			return false
		}
	}
	return true
}

// WireCheck is one entry of the doctor.run event.
type WireCheck struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Text   string `json:"text"`
	Fix    string `json:"fix"`
}

const (
	wireTextMax = 200
	wireFixMax  = 600
)

// WireChecks turns a report into the doctor.run payload: no git name or
// email, the home directory as "~", text and fix cut to the contract's limits.
func WireChecks(rs []Result, home string) []WireCheck {
	out := make([]WireCheck, 0, len(rs))
	for _, r := range rs {
		text := r.Text
		if r.ID == "git_identity" && r.Status == OK {
			text = "git identity is set"
		}
		out = append(out, WireCheck{
			ID:     r.ID,
			Status: r.Status.String(),
			Text:   cutRunes(tilde(text, home), wireTextMax),
			Fix:    cutRunes(tilde(r.Fix, home), wireFixMax),
		})
	}
	return out
}

// tilde replaces home with "~" only where it ends at a path boundary, so
// /home/ana leaves /home/anabel alone. A root home is never replaced.
func tilde(s, home string) string {
	home = strings.TrimRight(home, `/\`)
	if home == "" || (len(home) == 2 && home[1] == ':') {
		return s
	}
	var b strings.Builder
	for {
		i := strings.Index(s, home)
		if i < 0 {
			break
		}
		end := i + len(home)
		b.WriteString(s[:i])
		if end == len(s) || s[end] == '/' || s[end] == '\\' || s[end] == '"' || s[end] == ' ' || s[end] == '\n' {
			b.WriteString("~")
		} else {
			b.WriteString(home)
		}
		s = s[end:]
	}
	b.WriteString(s)
	return b.String()
}

func cutRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
