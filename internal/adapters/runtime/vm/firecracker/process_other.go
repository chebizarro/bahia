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

func (unsupportedProcessManager) Kill(VMMIdentity, string) error {
	return fmt.Errorf("firecracker process signals require Linux")
}
func (unsupportedProcessManager) InspectProcess(context.Context, VMMIdentity, string) (bool, error) {
	return false, fmt.Errorf("firecracker process inspection requires Linux")
}
func (unsupportedProcessManager) WatchExit(context.Context, VMMIdentity, string) (ProcessExit, error) {
	return nil, fmt.Errorf("firecracker process exit events require Linux")
}

var _ PersistentProcessManager = unsupportedProcessManager{}
