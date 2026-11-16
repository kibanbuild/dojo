package lab

import (
	"strings"
	"testing"
)

const limaCfg = `# This SSH config file can be passed to 'ssh -F'.
Host lima-dojo
  IdentityFile "/Users/x/.lima/_config/user"
  StrictHostKeyChecking no
  BatchMode yes
  User x
  Hostname 127.0.0.1
  Port 53272
  ControlMaster auto
  ControlPath "/Users/x/.lima/dojo/ssh.sock"
  ControlPersist yes
`

func TestLimaToDojo(t *testing.T) {
	got := limaToDojo(limaCfg, "/Users/x/.ssh/id_ed25519")
	for _, want := range []string{"Host dojo\n", `IdentityFile "/Users/x/.ssh/id_ed25519"`, "Port 53272", "IdentitiesOnly yes", "StrictHostKeyChecking no"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, gone := range []string{"lima-dojo", ".lima/_config/user", "BatchMode", "can be passed", "Control"} {
		if strings.Contains(got, gone) {
			t.Errorf("%q should be gone:\n%s", gone, got)
		}
	}
}

func TestAuthorizeScriptEncodesKey(t *testing.T) {
	script, err := authorizeScript([]byte("ssh-ed25519 AAAA me@x; rm -rf /\n"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(script), "rm -rf") {
		t.Errorf("key text reached the script unencoded:\n%s", script)
	}
}

func TestAuthorizeScriptRejectsNonKeys(t *testing.T) {
	for _, bad := range []string{"", "hello", "ssh-ed25519 AAAA\nrm -rf /", "-----BEGIN OPENSSH PRIVATE KEY-----"} {
		if _, err := authorizeScript([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestAuthorizeScriptTerminatesLastLine(t *testing.T) {
	script, err := authorizeScript([]byte("ssh-ed25519 AAAA"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(script)
	nl := strings.Index(s, "tail -c1")
	app := strings.Index(s, "grep -qxF")
	if nl < 0 || app < 0 || nl > app {
		t.Errorf("newline guard must come before the append:\n%s", s)
	}
}

func TestLimaToDojoKeywordForms(t *testing.T) {
	cfg := "Host lima-dojo\n\tIdentityFile\t\"/lima/key\"\n  BatchMode=yes\n  ControlPath=/x/sock\n\tControlMaster\tauto\n  Port 22\n"
	got := limaToDojo(cfg, "/k/id")
	for _, gone := range []string{"/lima/key", "BatchMode", "Control"} {
		if strings.Contains(got, gone) {
			t.Errorf("%q should be gone:\n%s", gone, got)
		}
	}
	if strings.Count(got, "IdentityFile") != 1 || !strings.Contains(got, "Port 22") {
		t.Errorf("unexpected output:\n%s", got)
	}
}
