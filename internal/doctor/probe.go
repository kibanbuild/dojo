package doctor

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const probeTimeout = 10 * time.Second

var errTimedOut = errors.New("timed out")

// runCmd runs the command mk builds under its own timeout; a command that
// does not answer in time returns errTimedOut.
func runCmd(ctx context.Context, mk func(context.Context) *exec.Cmd) (string, error) {
	tctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	out, err := mk(tctx).Output()
	if tctx.Err() != nil {
		return "", errTimedOut
	}
	return string(out), err
}

// Probe reads this machine's facts.
func Probe(ctx context.Context) Machine {
	m := Machine{OS: runtime.GOOS, CPUs: runtime.NumCPU()}
	hostFacts(ctx, &m)
	if _, err := exec.LookPath("git"); err == nil {
		probeGit(ctx, &m)
	}
	_, err := exec.LookPath("code")
	m.HasVSCode = err == nil
	return m
}

// probeGit confirms git runs (macOS ships a shim that only prompts to install)
// and reads the global identity.
func probeGit(ctx context.Context, m *Machine) {
	_, err := runCmd(ctx, func(c context.Context) *exec.Cmd { return exec.CommandContext(c, "git", "--version") })
	if errors.Is(err, errTimedOut) {
		m.GitUnknown = true
		return
	}
	if err != nil {
		return
	}
	m.HasGit = true
	out, err := runCmd(ctx, func(c context.Context) *exec.Cmd {
		return exec.CommandContext(c, "git", "config", "--global", "--get-regexp", `^user\.(name|email)$`)
	})
	if errors.Is(err, errTimedOut) {
		m.HasGit, m.GitUnknown = false, true
		return
	}
	m.GitName, m.GitEmail = parseGitIdentity(out)
}

func parseGitIdentity(s string) (name, email string) {
	for _, line := range strings.Split(s, "\n") {
		key, val, _ := strings.Cut(strings.TrimSpace(line), " ")
		switch key {
		case "user.name":
			name = strings.TrimSpace(val)
		case "user.email":
			email = strings.TrimSpace(val)
		}
	}
	return name, email
}

// parseMemInfo returns MemTotal from /proc/meminfo text, in bytes.
func parseMemInfo(s string) uint64 {
	for _, line := range strings.Split(s, "\n") {
		f := strings.Fields(line)
		if len(f) >= 3 && f[0] == "MemTotal:" && f[2] == "kB" {
			kb, err := strconv.ParseUint(f[1], 10, 64)
			if err != nil {
				return 0
			}
			return kb * 1024
		}
	}
	return 0
}

// parseCPUFlags reports whether /proc/cpuinfo text lists vmx (Intel) or svm (AMD).
func parseCPUFlags(s string) bool {
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "flags") && (strings.Contains(line, " vmx") || strings.Contains(line, " svm")) {
			return true
		}
	}
	return false
}

// parseWindowsFacts reads the three lines the Windows probe script prints, in
// order: total RAM, enabled virtualisation, free bytes on the home drive. An
// empty or malformed line leaves its fact unknown without shifting the others.
func parseWindowsFacts(s string, m *Machine) {
	lines := strings.Split(strings.ReplaceAll(s, "\r", ""), "\n")
	field := func(i int) string {
		if i < len(lines) {
			return strings.TrimSpace(lines[i])
		}
		return ""
	}
	m.MemoryBytes, _ = strconv.ParseUint(field(0), 10, 64)
	v, err := strconv.ParseBool(field(1))
	m.Virtualization, m.VirtualizationKnown = v, err == nil
	m.FreeDiskBytes, _ = strconv.ParseUint(field(2), 10, 64)
}
