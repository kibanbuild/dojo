package doctor

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

func hostFacts(ctx context.Context, m *Machine) {
	m.FreeDiskBytes = freeDisk()
	if out, err := runCmd(ctx, func(c context.Context) *exec.Cmd { return exec.CommandContext(c, "sysctl", "-n", "hw.memsize") }); err == nil {
		m.MemoryBytes, _ = strconv.ParseUint(strings.TrimSpace(out), 10, 64)
	}
	if out, err := runCmd(ctx, func(c context.Context) *exec.Cmd { return exec.CommandContext(c, "sysctl", "-n", "kern.hv_support") }); err == nil {
		v, _ := strconv.Atoi(strings.TrimSpace(out))
		m.Virtualization, m.VirtualizationKnown = v == 1, true
	}
}

func freeDisk() uint64 {
	home, err := os.UserHomeDir()
	if err != nil {
		return 0
	}
	var st syscall.Statfs_t
	if syscall.Statfs(home, &st) != nil {
		return 0
	}
	return st.Bavail * uint64(st.Bsize)
}
