package doctor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kibanbuild/dojo/internal/lab"
	"github.com/kibanbuild/dojo/internal/sshconfig"
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

var sshErrNoKey = sshconfig.ErrNoKey

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
	if !Ready([]Result{{Group: GroupMachine, Status: OK}, {Group: GroupMachine, Status: Warn}}) {
		t.Error("warnings alone should be ready")
	}
	if Ready([]Result{{Group: GroupMachine, Status: OK}, {Group: GroupMachine, Status: Fail}}) {
		t.Error("a machine failure should not be ready")
	}
	if !Ready([]Result{{Group: GroupMachine, Status: OK}, {Group: GroupKey, Status: Fail}, {Group: GroupRepo, Status: Fail}}) {
		t.Error("a failing key or repo group must not block a good machine")
	}
	if !Ready(Evaluate(good, "lima", nil)) {
		t.Error("a good machine with no SSH key is still ready")
	}
}

func TestResultIDsAndGroups(t *testing.T) {
	m := with(good, func(m *Machine) {
		m.HasSSHKeygen, m.SSHKeyPath = true, "/home/ana/.ssh/id_ed25519"
		m.GitSigningFormat, m.GitSigningKey, m.GitSignCommits = "ssh", "/home/ana/.ssh/id_ed25519.pub", "true"
		m.SigningKeyIsSSHKey = true
	})
	want := []struct {
		id    string
		group Group
	}{
		{"memory", GroupMachine}, {"disk", GroupMachine}, {"cpu", GroupMachine}, {"virtualization", GroupMachine},
		{"vm_manager", GroupMachine}, {"git", GroupMachine}, {"git_identity", GroupMachine},
		{"ssh_key", GroupKey}, {"ssh_keygen", GroupKey}, {"git_signing", GroupKey}, {"editor", GroupMachine},
	}
	rs := Evaluate(m, "lima", nil)
	if len(rs) != len(want) {
		t.Fatalf("got %d results, want %d:\n%s", len(rs), len(want), textsOf(rs))
	}
	for i, w := range want {
		if rs[i].ID != w.id || rs[i].Group != w.group {
			t.Errorf("result %d = %s/%s, want %s/%s", i, rs[i].ID, rs[i].Group, w.id, w.group)
		}
	}
}

func TestWireChecks(t *testing.T) {
	rs := Evaluate(with(good, func(m *Machine) {
		m.HasSSHKeygen, m.SSHKeyPath = true, "/home/ana/.ssh/id_ed25519"
	}), "lima", nil)
	wire := WireChecks(rs, "/home/ana")
	if len(wire) != len(rs) {
		t.Fatalf("got %d wire checks, want %d", len(wire), len(rs))
	}
	seen := map[string]WireCheck{}
	for _, w := range wire {
		seen[w.ID] = w
		for _, s := range []string{w.Text, w.Fix} {
			if strings.Contains(s, "Ana") || strings.Contains(s, "ana@x.dev") || strings.Contains(s, "/home/ana") {
				t.Errorf("%s leaks personal data: %q", w.ID, s)
			}
		}
	}
	if g := seen["git_identity"]; g.Text != "git identity is set" || g.Status != "ok" {
		t.Errorf("git_identity = %+v", g)
	}
	if g := seen["git_signing"]; g.Status != "fail" || !strings.Contains(g.Fix, "~/.ssh/id_ed25519.pub") {
		t.Errorf("git_signing = %+v", g)
	}
	other := WireChecks(Evaluate(with(good, func(m *Machine) {
		m.HasSSHKeygen, m.SSHKeyPath = true, "/home/ana/.ssh/id_ed25519"
		m.GitSigningFormat, m.GitSigningKey, m.GitSignCommits = "ssh", "/mnt/secret/team/other.pub", "true"
	}), "lima", errors.New("open /home/ana/.lima: denied")), "/home/ana")
	for _, w := range other {
		if strings.Contains(w.Text, "/mnt/secret") || strings.Contains(w.Text, "/home/ana") {
			t.Errorf("%s leaks a path: %q", w.ID, w.Text)
		}
		if w.ID == "git_signing" && (w.Status != "warn" || !strings.Contains(w.Text, "other.pub")) {
			t.Errorf("git_signing = %+v", w)
		}
		if w.ID == "vm_manager" && !strings.Contains(w.Text, "open ~/.lima: denied") {
			t.Errorf("vm_manager = %+v", w)
		}
	}
	if got := tilde("/home/anabel/x /home/ana/y /home/ana", "/home/ana"); got != "/home/anabel/x ~/y ~" {
		t.Errorf("tilde boundary = %q", got)
	}
	for _, home := range []string{"", "/", `C:`, `C:\`} {
		if got := tilde("/a C:/b", home); got != "/a C:/b" {
			t.Errorf("home %q changed the text: %q", home, got)
		}
	}
	long := WireChecks([]Result{{ID: "memory", Status: Warn, Text: strings.Repeat("é", 300), Fix: strings.Repeat("x", 700)}}, "")
	if n := len([]rune(long[0].Text)); n != 200 {
		t.Errorf("text runes = %d, want 200", n)
	}
	if n := len(long[0].Fix); n != 600 {
		t.Errorf("fix len = %d, want 600", n)
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

func TestParseGitConfig(t *testing.T) {
	cfg := parseGitConfig("user.name Ana Pérez\nuser.email ana@x.dev\ngpg.format ssh\nuser.name Ana P\n")
	if cfg["user.name"] != "Ana P" || cfg["user.email"] != "ana@x.dev" || cfg["gpg.format"] != "ssh" {
		t.Errorf("got %v", cfg)
	}
	if cfg = parseGitConfig(""); len(cfg) != 0 {
		t.Errorf("empty config: %v", cfg)
	}
}

func TestParseGitVersion(t *testing.T) {
	for in, want := range map[string][2]int{
		"git version 2.39.5 (Apple Git-154)\n": {2, 39},
		"git version 2.45.1.windows.1\n":       {2, 45},
		"git version 2.34\n":                   {2, 34},
		"":                                     {0, 0},
		"git version two.x\n":                  {0, 0},
		"something else 2.1.0":                 {0, 0},
	} {
		if a, b := parseGitVersion(in); a != want[0] || b != want[1] {
			t.Errorf("parseGitVersion(%q) = %d.%d, want %d.%d", in, a, b, want[0], want[1])
		}
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
	if !reflect.DeepEqual(m, Machine{}) {
		t.Errorf("empty output: %+v", m)
	}
}

func textsOf(rs []Result) string {
	var b strings.Builder
	for _, r := range rs {
		b.WriteString(r.Text + " | " + r.Fix + "\n")
	}
	return b.String()
}

func TestSSHChecks(t *testing.T) {
	withKey := func(m *Machine) { m.HasSSHKeygen, m.SSHKeyPath = true, "/home/ana/.ssh/id_ed25519" }
	signing := func(m *Machine) {
		withKey(m)
		m.GitSigningFormat, m.GitSigningKey, m.GitSignCommits = "ssh", "/home/ana/.ssh/id_ed25519.pub", "true"
	}
	tests := []struct {
		name string
		m    Machine
		text string
		want Status
	}{
		{"key found", with(good, withKey), "SSH key found: id_ed25519", OK},
		{"no key", with(good, func(m *Machine) { m.HasSSHKeygen = true; m.SSHKeyErr = fmt.Errorf("x: %w", sshErrNoKey) }), "no SSH key found", Fail},
		{"unusable chosen key", with(good, func(m *Machine) { m.HasSSHKeygen = true; m.SSHKeyErr = errors.New("permission denied") }), "could not use an SSH key: permission denied", Fail},
		{"no ssh-keygen", with(good, func(m *Machine) { m.HasSSHKeygen = false }), "`ssh-keygen` not found", Warn},
		{"signing not configured", with(good, withKey), "not set up to sign", Fail},
		{"format is gpg", with(good, func(m *Machine) { signing(m); m.GitSigningFormat = "openpgp" }), "not set up to sign", Fail},
		{"gpgsign off", with(good, func(m *Machine) { signing(m); m.GitSignCommits = "false" }), "not set up to sign", Fail},
		{"signing with dojo's key", with(good, func(m *Machine) { signing(m); m.SigningKeyIsSSHKey = true }), "configured to sign commits with id_ed25519", OK},
		{"signing with another key", with(good, signing), "not with the key dojo uses", Warn},
		{"signing, no key to compare", with(good, func(m *Machine) { signing(m); m.SSHKeyPath = "" }), "configured to sign commits with an SSH key", OK},
		{"git too old", with(good, func(m *Machine) { signing(m); m.GitMajor, m.GitMinor = 2, 30 }), "too old", Fail},
		{"git 2.34 is enough", with(good, func(m *Machine) { signing(m); m.SigningKeyIsSSHKey = true; m.GitMajor, m.GitMinor = 2, 34 }), "configured to sign", OK},
		{"git version unknown is not too old", with(good, func(m *Machine) { signing(m); m.SigningKeyIsSSHKey = true }), "configured to sign", OK},
	}
	for _, tt := range tests {
		rs := Evaluate(tt.m, "lima", nil)
		got, ok := statusOf(rs, tt.text)
		if !ok || got != tt.want {
			t.Errorf("%s: status %v (found %v), want %v\n%s", tt.name, got, ok, tt.want, textsOf(rs))
		}
	}
}

func TestSigningChecksSkippedWithoutGit(t *testing.T) {
	rs := Evaluate(with(good, func(m *Machine) { m.HasGit, m.HasSSHKeygen = false, true }), "lima", nil)
	if strings.Contains(textsOf(rs), "commits") {
		t.Errorf("signing reported without git:\n%s", textsOf(rs))
	}
}

func TestSSHAdviceMatchesTheMachine(t *testing.T) {
	fix := func(m Machine, text string) string {
		for _, r := range Evaluate(m, "lima", nil) {
			if strings.Contains(r.Text, text) {
				return r.Fix
			}
		}
		t.Fatalf("no result containing %q", text)
		return ""
	}
	m := with(good, func(m *Machine) { m.HasSSHKeygen, m.SSHKeyPath = true, `C:\Users\Ana\.ssh\id_ed25519` })
	got := fix(m, "not set up to sign")
	if !strings.Contains(got, `user.signingkey "C:\Users\Ana\.ssh\id_ed25519.pub"`) || strings.Contains(got, "&&") || strings.Contains(got, `\\`) {
		t.Errorf("advice must use the found key, one command per line, no doubled backslashes:\n%s", got)
	}
	nokey := with(good, func(m *Machine) { m.HasSSHKeygen = true })
	if got := fix(nokey, "not set up to sign"); !strings.Contains(got, "create an SSH key first") {
		t.Errorf("no key found, advice should say to create one:\n%s", got)
	}
	for goos, want := range map[string]string{"windows": "Optional features", "darwin": "ships with macOS", "linux": "apt install openssh-client"} {
		if got := fix(with(good, func(m *Machine) { m.OS = goos }), "`ssh-keygen` not found"); !strings.Contains(got, want) {
			t.Errorf("%s keygen advice = %q, want %q", goos, got, want)
		}
	}
	for goos, want := range map[string]string{"darwin": "brew install git", "windows": "git-scm.com", "linux": "package manager"} {
		old := with(good, func(m *Machine) {
			m.OS, m.HasSSHKeygen, m.GitMajor, m.GitMinor = goos, true, 2, 30
			m.GitSigningFormat, m.GitSigningKey, m.GitSignCommits = "ssh", "k", "true"
		})
		if got := fix(old, "too old"); !strings.Contains(got, want) {
			t.Errorf("%s git advice = %q, want %q", goos, got, want)
		}
	}
}

func writeKey(t *testing.T, dir, name, pub string) string {
	t.Helper()
	priv := filepath.Join(dir, name)
	for p, body := range map[string]string{priv: "PRIVATE", priv + ".pub": pub + "\n"} {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return priv
}

func TestProbeSSH(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DOJO_SSH_DIR", dir)
	pub := "ssh-ed25519 AAAAC3Nza me@x"
	priv := writeKey(t, dir, "id_ed25519", pub)
	other := writeKey(t, dir, "id_other", "ssh-ed25519 BBBB other@x")

	tests := []struct {
		name, chosen, signing string
		wantMatch             bool
	}{
		{"pub path", "", priv + ".pub", true},
		{"private path", "", priv, true},
		{"literal key", "", pub, true},
		{"key:: literal", "", "key::" + pub + " comment", true},
		{"other key's path", "", other + ".pub", false},
		{"other literal", "", "ssh-ed25519 BBBB other@x", false},
		{"missing file", "", filepath.Join(dir, "nope.pub"), false},
		{"unset", "", "", false},
		{"chosen key differs from signing key", other, priv + ".pub", false},
	}
	for _, tt := range tests {
		m := Machine{GitSigningKey: tt.signing}
		ProbeSSH(&m, tt.chosen)
		if m.SSHKeyErr != nil || m.SigningKeyIsSSHKey != tt.wantMatch {
			t.Errorf("%s: err=%v match=%v, want match=%v", tt.name, m.SSHKeyErr, m.SigningKeyIsSSHKey, tt.wantMatch)
		}
	}
	var m Machine
	ProbeSSH(&m, "")
	if m.SSHKeyPath != priv {
		t.Errorf("default key = %q, want %q", m.SSHKeyPath, priv)
	}
}

func TestProbeSSHExpandsHomeInSigningKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOJO_SSH_DIR", dir)
	writeKey(t, dir, "id_ed25519", "ssh-ed25519 AAAA me@x")
	m := Machine{GitSigningKey: "~/.ssh/id_ed25519.pub"}
	ProbeSSH(&m, "")
	if !m.SigningKeyIsSSHKey {
		t.Errorf("~/ path not matched: %+v", m)
	}
}

func TestProbeSSHFailures(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DOJO_SSH_DIR", dir)
	var m Machine
	ProbeSSH(&m, "")
	if !errors.Is(m.SSHKeyErr, sshErrNoKey) || m.SSHKeyPath != "" {
		t.Errorf("empty dir: %+v", m)
	}
	writeKey(t, dir, "id_bad", "not a key")
	m = Machine{}
	ProbeSSH(&m, filepath.Join(dir, "id_bad"))
	if m.SSHKeyErr == nil || errors.Is(m.SSHKeyErr, sshErrNoKey) || m.SSHKeyPath != "" || m.SigningKeyIsSSHKey {
		t.Errorf("bad chosen key must be an error, not 'no key': %+v", m)
	}
}
