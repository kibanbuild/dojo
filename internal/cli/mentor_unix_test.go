//go:build !windows

package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kibanyadev/dojo/internal/mentor"
)

// The script really runs under bash, with getent and chown faked, and leaves the two files.
func TestMentorScriptWritesTheFiles(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home", "ana.guest") // uid 501 and an odd home, as on a macOS Lima lab
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	fakes := map[string]string{
		"getent": "#!/bin/sh\n[ \"$2\" = ana ] && printf 'ana:x:501:1000:Ana B:" + home + ":/bin/bash\\n'\n",
		"id":     "#!/bin/sh\necho labgroup\n",
		"chown":  "#!/bin/sh\necho \"$@\" > \"" + filepath.Join(dir, "chown.log") + "\"\n",
	}
	for name, body := range fakes {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o700); err != nil { //nolint:gosec // G306: a test stub must be executable
			t.Fatal(err)
		}
	}
	cmd := exec.Command("bash", "-c", string(mentorScript())) //nolint:gosec // G204: the script under test
	cmd.Env = append(os.Environ(), "SUDO_USER=ana", "PATH="+bin+":"+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("script: %v\n%s", err, out)
	}
	agents, err := os.ReadFile(filepath.Join(home, "AGENTS.md")) //nolint:gosec // G304: a path under t.TempDir
	if err != nil || string(agents) != mentor.AgentsMD() {
		t.Errorf("AGENTS.md = %q, %v", agents, err)
	}
	claude, err := os.ReadFile(filepath.Join(home, "CLAUDE.md")) //nolint:gosec // G304: a path under t.TempDir
	if err != nil || string(claude) != mentor.ClaudeMD {
		t.Errorf("CLAUDE.md = %q, %v", claude, err)
	}
	log, _ := os.ReadFile(filepath.Join(dir, "chown.log")) //nolint:gosec // G304: a path under t.TempDir
	if !strings.HasPrefix(string(log), "ana:labgroup ") {
		t.Errorf("chown args = %q, want the lab user", log)
	}
}

func TestMentorScriptFailures(t *testing.T) {
	for name, tc := range map[string]struct{ env, getent, want string }{
		"no sudo user":    {"", "exit 0", "no lab user:"},
		"root":            {"SUDO_USER=root", "exit 0", "no lab user:"},
		"unknown user":    {"SUDO_USER=ghost", "exit 0", "no lab user home found for ghost"},
		"home is missing": {"SUDO_USER=ana", "printf 'ana:x:501:1000::/nonexistent:/bin/bash\\n'", "no lab user home found for ana"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "getent"), []byte("#!/bin/sh\n"+tc.getent+"\n"), 0o700); err != nil { //nolint:gosec // G306: a test stub must be executable
				t.Fatal(err)
			}
			cmd := exec.Command("bash", "-c", string(mentorScript())) //nolint:gosec // G204: the script under test
			cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "SUDO_USER=")
			if tc.env != "" {
				cmd.Env = append(cmd.Env, tc.env)
			}
			out, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(out), tc.want) {
				t.Errorf("err = %v, out = %s, want %q", err, out, tc.want)
			}
		})
	}
}
