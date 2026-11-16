package doctor

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/kibanbuild/dojo/internal/lab"
)

func with(m Machine, f func(*Machine)) Machine { f(&m); return m }

func statusOf(rs []Result, text string) (Status, bool) {
	for _, r := range rs {
		if strings.Contains(r.Text, text) {
			return r.Status, true
		}
	}
	return 0, false
}

var good = Machine{
	OS: "linux", MemoryBytes: 16 * gib, FreeDiskBytes: 100 * gib, CPUs: 8,
	Virtualization: true, VirtualizationKnown: true,
	HasGit: true, GitName: "Ana", GitEmail: "ana@x.dev", HasVSCode: true,
}

func TestEvaluate(t *testing.T) {
	tests := []struct {
		name    string
		m       Machine
		backend error
		text    string
		want    Status
	}{
		{"plenty of RAM", good, nil, "RAM", OK},
		{"8 GB laptop reports 7.8", with(good, func(m *Machine) { m.MemoryBytes = 78 * gib / 10 }), nil, "RAM", Warn},
		{"4 GB is too little", with(good, func(m *Machine) { m.MemoryBytes = 4 * gib }), nil, "RAM", Fail},
		{"RAM unreadable", with(good, func(m *Machine) { m.MemoryBytes = 0 }), nil, "RAM", Warn},
		{"plenty of disk", good, nil, "free disk", OK},
		{"low disk", with(good, func(m *Machine) { m.FreeDiskBytes = 10 * gib }), nil, "free disk", Fail},
		{"tight disk", with(good, func(m *Machine) { m.FreeDiskBytes = 20 * gib }), nil, "free disk", Warn},
		{"disk unreadable", with(good, func(m *Machine) { m.FreeDiskBytes = 0 }), nil, "free disk", Warn},
		{"many cores", good, nil, "CPU cores", OK},
		{"two cores", with(good, func(m *Machine) { m.CPUs = 2 }), nil, "CPU cores", Warn},
		{"one core", with(good, func(m *Machine) { m.CPUs = 1 }), nil, "CPU core", Fail},
		{"virtualisation on", good, nil, "virtualisation", OK},
		{"virtualisation off", with(good, func(m *Machine) { m.Virtualization = false }), nil, "virtualisation", Fail},
		{"virtualisation unknown", with(good, func(m *Machine) { m.VirtualizationKnown = false }), nil, "virtualisation", Warn},
		{"no VM manager", good, errors.New("not installed"), "VM manager", Fail},
		{"VM manager ready", good, nil, "VM manager", OK},
		{"KVM present but denied", with(good, func(m *Machine) { m.KVMDenied = true; m.VirtualizationKnown = false }), nil, "/dev/kvm exists", Warn},
		{"CPU capable but /dev/kvm missing", with(good, func(m *Machine) { m.KVMMissing = true; m.Virtualization, m.VirtualizationKnown = false, false }), nil, "/dev/kvm is missing", Fail},
		{"git timed out", with(good, func(m *Machine) { m.HasGit, m.GitUnknown = false, true }), nil, "could not check git", Warn},
		{"windows is unsupported", with(good, func(m *Machine) { m.OS = "windows" }), nil, "Windows labs are not supported yet", Fail},
		{"no git", with(good, func(m *Machine) { m.HasGit = false }), nil, "git is not installed", Fail},
		{"git identity set", good, nil, "git identity: Ana <ana@x.dev>", OK},
		{"git identity missing", with(good, func(m *Machine) { m.GitEmail = "" }), nil, "no name or email", Warn},
		{"VS Code present", good, nil, "`code` command found", OK},
		{"VS Code optional", with(good, func(m *Machine) { m.HasVSCode = false }), nil, "`code` command not found", Warn},
	}
	for _, tt := range tests {
		got, ok := statusOf(Evaluate(tt.m, "lima", tt.backend), tt.text)
		if !ok || got != tt.want {
			t.Errorf("%s: status %v (found %v), want %v", tt.name, got, ok, tt.want)
		}
	}
}

func TestWindowsNeverRecommendsLima(t *testing.T) {
	m := with(good, func(m *Machine) { m.OS = "windows" })
	for _, r := range Evaluate(m, "lima", errors.New("limactl not found")) {
		if strings.Contains(r.Text+r.Fix, "Lima") || strings.Contains(r.Text+r.Fix, "lima") {
			t.Errorf("windows report mentions lima: %+v", r)
		}
	}
}

func TestReady(t *testing.T) {
	if !Ready(Evaluate(good, "lima", nil)) {
		t.Error("a good machine should be ready")
	}
	if !Ready([]Result{{Status: OK}, {Status: Warn}}) {
		t.Error("warnings alone should be ready")
	}
	if Ready([]Result{{Status: OK}, {Status: Fail}}) {
		t.Error("a failure should not be ready")
	}
}

func TestParseMemInfo(t *testing.T) {
	tests := []struct {
		in   string
		want uint64
	}{
		{"MemTotal:       16384000 kB\nMemFree: 1 kB\n", 16384000 * 1024},
		{"MemFree: 1 kB\n", 0},
		{"MemTotal: lots kB\n", 0},
		{"", 0},
	}
	for _, tt := range tests {
		if got := parseMemInfo(tt.in); got != tt.want {
			t.Errorf("parseMemInfo(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestParseWindowsFacts(t *testing.T) {
	var m Machine
	parseWindowsFacts("17179869184\r\nTrue\r\n53687091200\r\n", &m)
	if m.MemoryBytes != 16*gib || !m.Virtualization || !m.VirtualizationKnown || m.FreeDiskBytes != 50*gib {
		t.Errorf("full output: %+v", m)
	}
	m = Machine{}
	parseWindowsFacts("17179869184\nmaybe\n", &m)
	if m.MemoryBytes != 16*gib || m.VirtualizationKnown || m.FreeDiskBytes != 0 {
		t.Errorf("partial output: %+v", m)
	}
}

func TestParseGitIdentity(t *testing.T) {
	name, email := parseGitIdentity("user.name Ana Pérez\nuser.email ana@x.dev\n")
	if name != "Ana Pérez" || email != "ana@x.dev" {
		t.Errorf("got %q <%q>", name, email)
	}
	if name, email = parseGitIdentity(""); name != "" || email != "" {
		t.Errorf("empty config: %q <%q>", name, email)
	}
}

func TestBackendFixOnlyWhenNotInstalled(t *testing.T) {
	fixOf := func(err error) string {
		for _, r := range Evaluate(good, "lima", err) {
			if strings.Contains(r.Text, "VM manager") {
				return r.Fix
			}
		}
		return "missing"
	}
	if got := fixOf(fmt.Errorf("lima is %w: see x", lab.ErrNotInstalled)); !strings.Contains(got, "install Lima") {
		t.Errorf("not installed: fix %q", got)
	}
	if got := fixOf(errors.New(`unknown backend "bogus" (known: lima)`)); got != "" {
		t.Errorf("other error: fix %q, want none", got)
	}
}

func TestKVMDeniedFixNamesTheGroup(t *testing.T) {
	m := with(good, func(m *Machine) { m.KVMDenied = true })
	for _, r := range Evaluate(m, "lima", nil) {
		if strings.Contains(r.Text, "/dev/kvm") && !strings.Contains(r.Fix, "kvm group") {
			t.Errorf("fix %q", r.Fix)
		}
	}
	if strings.Contains(fmt.Sprint(Evaluate(m, "lima", nil)), "virtualisation is enabled") {
		t.Error("denied KVM reported as enabled")
	}
}

func TestDisplayedValuesNeverReachAThreshold(t *testing.T) {
	tests := []struct {
		name string
		m    Machine
		text string
		want Status
	}{
		{"14.99 GiB RAM", with(good, func(m *Machine) { m.MemoryBytes = 1499 * gib / 100 }), "14.9 GB RAM: enough", Warn},
		{"15 GiB RAM", with(good, func(m *Machine) { m.MemoryBytes = 15 * gib }), "15 GB RAM", OK},
		{"7.49 GiB RAM", with(good, func(m *Machine) { m.MemoryBytes = 749 * gib / 100 }), "7.4 GB RAM, the labs need at least 8 GB", Fail},
		{"7.5 GiB RAM", with(good, func(m *Machine) { m.MemoryBytes = 75 * gib / 10 }), "7.5 GB RAM: enough", Warn},
		{"14.99 GiB disk", with(good, func(m *Machine) { m.FreeDiskBytes = 1499 * gib / 100 }), "14.9 GB free disk, the labs need at least 15 GB", Fail},
		{"15 GiB disk", with(good, func(m *Machine) { m.FreeDiskBytes = 15 * gib }), "15 GB free disk: enough", Warn},
		{"29.99 GiB disk", with(good, func(m *Machine) { m.FreeDiskBytes = 2999 * gib / 100 }), "29.9 GB free disk: enough", Warn},
		{"30 GiB disk", with(good, func(m *Machine) { m.FreeDiskBytes = 30 * gib }), "30 GB free disk", OK},
	}
	for _, tt := range tests {
		got, ok := statusOf(Evaluate(tt.m, "lima", nil), tt.text)
		if !ok || got != tt.want {
			t.Errorf("%s: status %v (found %v), want %v", tt.name, got, ok, tt.want)
		}
	}
}

func TestParseCPUFlags(t *testing.T) {
	if !parseCPUFlags("processor: 0\nflags\t\t: fpu vmx sse\n") || !parseCPUFlags("flags : fpu svm\n") {
		t.Error("vmx/svm not detected")
	}
	if parseCPUFlags("flags : fpu sse\n") || parseCPUFlags("") {
		t.Error("flag-less cpu reported as virtualisation capable")
	}
}

func TestParseWindowsFactsKeepsPositions(t *testing.T) {
	var m Machine
	parseWindowsFacts("\r\nTrue\r\n53687091200\r\n", &m)
	if m.MemoryBytes != 0 || !m.Virtualization || !m.VirtualizationKnown || m.FreeDiskBytes != 50*gib {
		t.Errorf("empty RAM line shifted the others: %+v", m)
	}
	m = Machine{}
	parseWindowsFacts("17179869184\r\n\r\n\r\n", &m)
	if m.MemoryBytes != 16*gib || m.VirtualizationKnown || m.FreeDiskBytes != 0 {
		t.Errorf("empty virt and disk lines: %+v", m)
	}
	m = Machine{}
	parseWindowsFacts("", &m)
	if m != (Machine{}) {
		t.Errorf("empty output: %+v", m)
	}
}
