//go:build !windows

package cli

import (
	"bytes"
	"context"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestDetachedCommandHasNoControllingTerminal(t *testing.T) {
	if _, err := exec.LookPath("ps"); err != nil {
		t.Skip("ps is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", "ps -o tty= -p $$")
	detach(cmd)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	if tty := strings.TrimSpace(string(out)); !strings.HasPrefix(tty, "?") {
		t.Errorf("tty = %q, want none", tty)
	}
}

func TestDetachedCancelKillsTheChildrenToo(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", "sleep 30 & echo $!; wait")
	cmd.WaitDelay = time.Second
	var out bytes.Buffer
	cmd.Stdout = &out // a copying pipe the grandchild holds open: the WaitDelay path
	detach(cmd)
	_ = cmd.Run()
	pid, err := strconv.Atoi(strings.TrimSpace(out.String()))
	if err != nil {
		t.Fatalf("grandchild pid %q: %v", out.String(), err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	deadline := time.Now().Add(3 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("grandchild %d still runs after the cancel", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
