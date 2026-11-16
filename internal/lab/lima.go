package lab

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/kibanbuild/dojo/internal/sshconfig"
)

// Lima runs the lab with Lima (https://lima-vm.io) on macOS and Linux.
type Lima struct {
	Bin string
}

func (l *Lima) Name() string { return "lima" }

func (l *Lima) Available() error {
	if _, err := exec.LookPath(l.Bin); err != nil {
		return fmt.Errorf("lima is %w: see https://lima-vm.io/docs/installation/", ErrNotInstalled)
	}
	return nil
}

func (l *Lima) Exists(ctx context.Context) (bool, error) {
	out, err := l.output(ctx, "list", "--format", "{{.Name}}")
	if err != nil {
		return false, err
	}
	for _, name := range strings.Fields(string(out)) {
		if name == Instance {
			return true, nil
		}
	}
	return false, nil
}

func (l *Lima) Recreate(ctx context.Context) error {
	if err := l.Delete(ctx); err != nil {
		return err
	}
	// --plain: no host folders, no port forwarding, no containerd. The lab sees nothing of the host.
	_, err := l.output(ctx, "start", "--tty=false", "--name="+Instance, "--plain",
		"--cpus=2", "--memory=2", "--disk=10", "template:ubuntu-lts")
	return err
}

func (l *Lima) Run(ctx context.Context, script []byte) ([]byte, int, error) {
	cmd := exec.CommandContext(ctx, l.Bin, "shell", "--workdir", "/", Instance, "sudo", "bash", "-s") //nolint:gosec // Bin is a constant or caller-controlled, never learner input
	cmd.Stdin = bytes.NewReader(script)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return out, exit.ExitCode(), nil
	}
	if err != nil {
		return out, -1, err
	}
	return out, 0, nil
}

func (l *Lima) Reboot(ctx context.Context) error {
	if _, err := l.output(ctx, "stop", Instance); err != nil {
		return err
	}
	_, err := l.output(ctx, "start", "--tty=false", Instance)
	return err
}

func (l *Lima) Shell(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, l.Bin, "shell", "--workdir", "/", Instance, "bash", "-lc", "cd && exec bash -l") //nolint:gosec // Bin is a constant or caller-controlled, never learner input
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// AuthorizeKey runs as the lab user (no sudo) so the key lands in their own home.
func (l *Lima) AuthorizeKey(ctx context.Context, pub []byte) error {
	script, err := authorizeScript(pub)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, l.Bin, "shell", "--workdir", "/", Instance, "bash", "-s") //nolint:gosec // Bin is a constant or caller-controlled, never learner input
	cmd.Stdin = bytes.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("authorizing your SSH key: %w\n%s", err, out)
	}
	return nil
}

func (l *Lima) SSHConfig(ctx context.Context, identityFile string) (string, error) {
	if strings.ContainsAny(identityFile, "\"\r\n%") || strings.Contains(identityFile, "${") {
		return "", fmt.Errorf("the SSH key path %q has a quote, line break, %% or ${ in it, which ssh would misread", identityFile)
	}
	dir, err := l.output(ctx, "list", Instance, "--format", "{{.Dir}}")
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(filepath.Join(strings.TrimSpace(string(dir)), "ssh.config"))
	if err != nil {
		return "", err
	}
	return limaToDojo(string(raw), identityFile), nil
}

func (l *Lima) Delete(ctx context.Context) error {
	exists, err := l.Exists(ctx)
	if err != nil || !exists {
		return err
	}
	_, err = l.output(ctx, "delete", "--force", Instance)
	return err
}

// output runs limactl and returns stdout; on failure the error carries stderr.
func (l *Lima) output(ctx context.Context, args ...string) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, l.Bin, args...) //nolint:gosec // Bin is a constant or caller-controlled, never learner input
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("limactl %s: %w\n%s", args[0], err, lastLines(stderr.String(), 15))
	}
	return stdout.Bytes(), nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// authorizeScript appends pub to the lab user's authorized_keys once. The key
// travels base64-encoded and is only ever expanded inside double quotes, so
// nothing in it is interpreted by the shell.
func authorizeScript(pub []byte) ([]byte, error) {
	pub = bytes.TrimSpace(pub)
	if err := sshconfig.ValidatePublicKey(pub); err != nil {
		return nil, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "set -e\nK=$(echo %s | base64 -d)\n", base64.StdEncoding.EncodeToString(pub))
	b.WriteString("umask 077\nmkdir -p \"$HOME/.ssh\" && touch \"$HOME/.ssh/authorized_keys\"\n")
	b.WriteString("[ ! -s \"$HOME/.ssh/authorized_keys\" ] || [ -z \"$(tail -c1 \"$HOME/.ssh/authorized_keys\")\" ] || echo >> \"$HOME/.ssh/authorized_keys\"\n")
	b.WriteString("grep -qxF -- \"$K\" \"$HOME/.ssh/authorized_keys\" || printf '%s\\n' \"$K\" >> \"$HOME/.ssh/authorized_keys\"\n")
	b.WriteString("chmod 700 \"$HOME/.ssh\" && chmod 600 \"$HOME/.ssh/authorized_keys\"\n")
	return []byte(b.String()), nil
}

// limaToDojo renames Lima's generated host to "dojo" and logs in with the learner's
// own key instead of Lima's. Connection sharing is dropped: a master opened with
// Lima's key would let any key through.
func limaToDojo(cfg, identityFile string) string {
	var b strings.Builder
	b.WriteString("# Managed by dojo: rewritten on every dojo start and dojo check.\n")
	for _, line := range strings.Split(cfg, "\n") {
		trimmed := strings.TrimSpace(line)
		keyword, _, _ := strings.Cut(strings.NewReplacer("=", " ", "\t", " ").Replace(trimmed), " ")
		switch strings.ToLower(keyword) {
		case "", "batchmode", "controlmaster", "controlpath", "controlpersist", "identityfile", "identitiesonly":
			continue
		case "host":
			b.WriteString("Host " + Instance + "\n")
			b.WriteString("  IdentityFile \"" + filepath.ToSlash(identityFile) + "\"\n  IdentitiesOnly yes\n")
		default:
			if strings.HasPrefix(trimmed, "#") {
				continue
			}
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}
