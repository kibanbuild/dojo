//go:build !windows

package cli

import (
	"os/exec"
	"syscall"
)

// detach starts the command in its own session, so it has no controlling terminal to prompt on,
// and makes a cancel kill the whole group, so a child such as ssh-keygen is reaped with git.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
