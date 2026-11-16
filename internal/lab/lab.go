// Package lab runs the learner's practice machine: a local Linux VM the CLI
// can create, script as root, reboot and hand over as an interactive shell.
package lab

import (
	"context"
	"errors"
	"fmt"
)

// ErrNotInstalled is wrapped by Available when the VM manager is simply absent.
var ErrNotInstalled = errors.New("not installed")

// Instance is the name of the lab VM on every backend.
const Instance = "dojo"

// Backend drives one VM manager (Lima for now).
type Backend interface {
	Name() string
	// Available reports whether the VM manager is installed.
	Available() error
	// Recreate deletes any existing lab VM and starts a fresh one.
	Recreate(ctx context.Context) error
	// Exists reports whether the lab VM exists.
	Exists(ctx context.Context) (bool, error)
	// Run executes a bash script as root and returns its combined output and exit code.
	Run(ctx context.Context, script []byte) (out []byte, code int, err error)
	// Reboot stops and starts the VM.
	Reboot(ctx context.Context) error
	// Shell attaches the terminal to an interactive login shell in the VM.
	Shell(ctx context.Context) error
	// Delete removes the VM.
	Delete(ctx context.Context) error
}

// DefaultName is the backend used when none is configured.
func DefaultName() string {
	return "lima"
}

// New returns the backend with the given name.
func New(name string) (Backend, error) {
	switch name {
	case "lima":
		return &Lima{Bin: "limactl"}, nil
	default:
		return nil, fmt.Errorf("unknown backend %q (known: lima)", name)
	}
}
