//go:build !linux

package firecracker

import (
	"context"
	"fmt"
	"runtime"
)

// newOSProcessManager returns a stub on non-Linux hosts so the package
// compiles (and its unit tests run) everywhere; launching real VMMs
// requires Linux/KVM.
func newOSProcessManager() ProcessManager {
	return unsupportedProcessManager{}
}

type unsupportedProcessManager struct{}

func (unsupportedProcessManager) Start(context.Context, StartVMMRequest) (VMMIdentity, error) {
	return VMMIdentity{}, fmt.Errorf("firecracker VMM processes require a linux host (running on %s)", runtime.GOOS)
}

func (unsupportedProcessManager) Alive(VMMIdentity, string) bool { return false }

func (unsupportedProcessManager) Kill(VMMIdentity, string) error { return nil }
