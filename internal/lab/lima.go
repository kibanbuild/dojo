package lab

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Lima runs the lab with Lima (https://lima-vm.io) on macOS and Linux.
type Lima struct {
	Bin string
}

func (l *Lima) Name() string { return "lima" }

func (l *Lima) Available() error {
	if _, err := exec.LookPath(l.Bin); err != nil {
		return fmt.Errorf("lima is not installed: see https://lima-vm.io/docs/installation/")
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
