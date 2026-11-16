package doctor

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"runtime"
	"syscall"
)

func hostFacts(_ context.Context, m *Machine) {
	m.FreeDiskBytes = freeDisk()
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		m.MemoryBytes = parseMemInfo(string(b))
	}
	f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	switch {
	case err == nil:
		_ = f.Close()
		m.Virtualization, m.VirtualizationKnown = true, true
	case errors.Is(err, fs.ErrPermission):
		m.KVMDenied = true
	case errors.Is(err, fs.ErrNotExist) && (runtime.GOARCH == "amd64" || runtime.GOARCH == "386"):
		if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
			if parseCPUFlags(string(b)) {
				m.KVMMissing = true
			} else {
				m.VirtualizationKnown = true
			}
		}
	}
}

func freeDisk() uint64 {
	home, err := os.UserHomeDir()
	if err != nil {
		return 0
	}
	var st syscall.Statfs_t
	if syscall.Statfs(home, &st) != nil || st.Bsize <= 0 {
		return 0
	}
	return st.Bavail * uint64(st.Bsize)
}
