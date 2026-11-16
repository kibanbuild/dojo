//go:build !darwin && !linux && !windows

package doctor

import "context"

func hostFacts(context.Context, *Machine) {}
