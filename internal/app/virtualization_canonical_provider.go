package app

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/google/uuid"
	runtimeAdapter "github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/adapters/runtime/vm"
	"github.com/openagentsinc/bahia/internal/adapters/runtime/vm/firecracker"
	"github.com/openagentsinc/bahia/internal/adapters/runtime/vm/libvirt"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// buildCanonicalVirtualizationProvider binds a canonical host to installation
// configuration. It never reads an SQL host row or uses the old operator list
// for intent authorization; IntentProcessor's relay-derived TrustSet does that.
func buildCanonicalVirtualizationProvider(ctx context.Context, cfg config.VirtualizationConfig, events repository.NostrEventRepository, host domain.VirtualizationHost, logger *zap.Logger) (domain.PersistentVMProvider, error) {
	if !cfg.PersistentVM.Enabled || cfg.PersistentVM.HostID != host.ID {
		return nil, fmt.Errorf("bahia-4f10q.1: canonical host has no configured persistent provider")
	}
	policy := &virtualizationPolicy{config: cfg, events: events}
	configured, err := policy.host(&host)
	if err != nil {
		return nil, err
	}
	if host.ExecutionLocation != domain.VMExecutionLocal {
		return nil, fmt.Errorf("bahia-4f10q.1: remote provider is unsupported")
	}
	p := cfg.PersistentVM
	switch host.Provider {
	case domain.VMProviderLibvirt:
		if (p.LibvirtURI != libvirt.DefaultURI && p.LibvirtURI != "qemu:///session") || !filepath.IsAbs(p.EventSocket) {
			return nil, domain.ErrInvalidValue
		}
	case domain.VMProviderFirecracker:
		if !filepath.IsAbs(p.FirecrackerBinary) || filepath.Base(p.FirecrackerBinary) != "firecracker" {
			return nil, domain.ErrInvalidValue
		}
	default:
		return nil, domain.ErrInvalidValue
	}
	networks := map[uuid.UUID]libvirt.NetworkBinding{}
	for _, n := range configured.Networks {
		networks[n.Ref] = libvirt.NetworkBinding{Mode: n.Mode, Name: n.Name}
	}
	return runtimeAdapter.NewPersistentVMProvider(runtimeAdapter.PersistentVMConfig{
		Provider: vm.PersistentConfig{Host: host, StoragePoolRef: p.StoragePoolRef, StateDir: p.StateDir,
			VerifyImage: func(ctx context.Context, h domain.VirtualizationHost, image domain.VMImage) error {
				return policy.VerifyPlaneProvenance(ctx, &h, image.ManifestDigest, image.Provenance)
			}},
		ImageRoot:   p.ImageRoot,
		Libvirt:     libvirt.Config{URI: p.LibvirtURI, EventSocket: p.EventSocket, Networks: networks},
		Firecracker: firecracker.Config{Binary: p.FirecrackerBinary},
	}, logger)
}
