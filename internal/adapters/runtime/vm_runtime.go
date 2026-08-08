package runtime

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/runtime/vm"
	vmlibvirt "github.com/openagentsinc/bahia/internal/adapters/runtime/vm/libvirt"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// newVMQEMURuntime wires the shared VM runtime core to the libvirt/QEMU
// hypervisor driver.
func newVMQEMURuntime(cfg RuntimeConfig, logger *zap.Logger) (Runtime, error) {
	uri := strings.TrimSpace(cfg.VM.LibvirtURI)
	if uri == "" {
		uri = vmlibvirt.DefaultURI
	}
	driver := vmlibvirt.New(vmlibvirt.Config{
		URI:          uri,
		InstancesDir: vm.InstancesDir(strings.TrimSpace(cfg.VM.StateDir)),
	}, logger)
	core, err := vm.NewRuntime(vm.Config{
		RuntimeType:    domain.RuntimeTypeVMQEMU,
		StateDir:       strings.TrimSpace(cfg.VM.StateDir),
		ImageRoot:      strings.TrimSpace(cfg.VM.ImageRoot),
		VsockGuestPort: cfg.VM.VsockGuestPort,
		VCPUs:          cfg.VM.VCPUs,
		MemoryMB:       cfg.VM.MemoryMB,
		NetworkProfile: strings.TrimSpace(cfg.VM.NetworkProfile),
	}, driver, logger)
	if err != nil {
		return nil, err
	}
	return &vmRuntimeAdapter{core: core}, nil
}

// vmRuntimeAdapter adapts the vm package's core (which cannot import this
// package without an import cycle) to the Runtime and LifecycleRuntime
// interfaces by translating the option and log types.
type vmRuntimeAdapter struct {
	core *vm.Runtime
}

func (a *vmRuntimeAdapter) Type() domain.RuntimeType {
	return a.core.Type()
}

func (a *vmRuntimeAdapter) Observe(ctx context.Context, serviceID, envID uuid.UUID, serviceName string) (*domain.RuntimeObservation, error) {
	return a.core.Observe(ctx, serviceID, envID, serviceName)
}

func (a *vmRuntimeAdapter) Deploy(ctx context.Context, serviceName, image string, opts DeployOptions) error {
	return a.core.Deploy(ctx, serviceName, image, vm.DeployOptions{
		Environment: opts.Environment,
		Labels:      opts.Labels,
		Ports:       opts.Ports,
		Volumes:     opts.Volumes,
		Restart:     opts.Restart,
		Command:     opts.Command,
		Entrypoint:  opts.Entrypoint,
		WorkingDir:  opts.WorkingDir,
		NetworkMode: opts.NetworkMode,
		PullAlways:  opts.PullAlways,
	})
}

func (a *vmRuntimeAdapter) Undeploy(ctx context.Context, serviceName string) error {
	return a.core.Undeploy(ctx, serviceName)
}

func (a *vmRuntimeAdapter) StreamLogs(ctx context.Context, serviceName string, opts LogOptions) (<-chan LogEntry, error) {
	inner, err := a.core.StreamLogs(ctx, serviceName, vm.LogOptions{Tail: opts.Tail, Follow: opts.Follow})
	if err != nil {
		return nil, err
	}
	out := make(chan LogEntry, 64)
	go func() {
		defer close(out)
		for entry := range inner {
			select {
			case out <- LogEntry{Timestamp: entry.Timestamp, Stream: entry.Stream, Message: entry.Message}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

func (a *vmRuntimeAdapter) Restart(ctx context.Context, targetName string) error {
	return a.core.Restart(ctx, targetName)
}

func (a *vmRuntimeAdapter) Stop(ctx context.Context, targetName string) error {
	return a.core.Stop(ctx, targetName)
}

var (
	_ Runtime          = (*vmRuntimeAdapter)(nil)
	_ LifecycleRuntime = (*vmRuntimeAdapter)(nil)
)
