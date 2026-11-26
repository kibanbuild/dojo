//go:build windows

package cli

import "os/exec"

func detach(*exec.Cmd) {}
