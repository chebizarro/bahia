package app

import (
	"context"
	"fiatjaf.com/nostr"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/loom"
	runtimeAdapter "github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/adapters/runtime/vm"
	"github.com/openagentsinc/bahia/internal/adapters/runtime/vm/firecracker"
	"github.com/openagentsinc/bahia/internal/adapters/runtime/vm/libvirt"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

type virtualizationPlaneSigner struct{ nostr.Signer }

func (s virtualizationPlaneSigner) Sign(ctx context.Context, e *nostr.Event) error {
	return s.SignEvent(ctx, e)
}

type virtualizationConfigurationDependencies struct {
	events       repository.NostrEventRepository
	secrets      repository.SecretRepository
	resolver     service.VMAuditedSecretResolver
	services     repository.ServiceRepository
	environments repository.EnvironmentRepository
	units        repository.DeploymentUnitRepository
	workers      repository.WorkerRepository
	pool         loom.PlaneRelayPool
	signer       nostr.Signer
	logger       *zap.Logger
}

func configureVirtualization(ctx context.Context, cfg config.VirtualizationConfig, deps *VirtualizationDependencies, d virtualizationConfigurationDependencies) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if deps.Repository == nil || deps.RBAC == nil || deps.Store == nil || deps.Publisher == nil || deps.Bus == nil || deps.Organizations == nil || deps.CanonicalAuthor == "" {
		return nil
	}
	if !cfg.PersistentVM.Enabled && len(cfg.PlaneEndpoints) == 0 {
		return nil
	}
	policy := &virtualizationPolicy{config: cfg, rbac: deps.RBAC, events: d.events, secrets: d.secrets, services: d.services}
	services := &VirtualizationServices{Logger: d.logger, policy: policy, Workers: d.workers}
	if cfg.PersistentVM.Enabled {
		var configured *config.VirtualizationHostPolicy
		for _, h := range cfg.Hosts {
			if h.HostID == cfg.PersistentVM.HostID {
				configured = &h
				break
			}
		}
		if configured == nil || d.events == nil || d.secrets == nil || d.resolver == nil {
			return nil
		}
		host, err := deps.Repository.Hosts().Get(ctx, configured.OrgID, configured.HostID)
		if err != nil {
			return err
		}
		if _, err = policy.host(host); err != nil {
			return err
		}
		p := cfg.PersistentVM
		if host.ExecutionLocation != domain.VMExecutionLocal {
			return &domain.VMProviderError{Code: domain.VMErrorUnsupported}
		}
		switch host.Provider {
		case domain.VMProviderLibvirt:
			if (p.LibvirtURI != libvirt.DefaultURI && p.LibvirtURI != "qemu:///session") || !filepath.IsAbs(p.EventSocket) {
				return domain.ErrInvalidValue
			}
		case domain.VMProviderFirecracker:
			if !filepath.IsAbs(p.FirecrackerBinary) || filepath.Base(p.FirecrackerBinary) != "firecracker" {
				return domain.ErrInvalidValue
			}
		default:
			return domain.ErrInvalidValue
		}
		networks := map[uuid.UUID]libvirt.NetworkBinding{}
		for _, n := range configured.Networks {
			networks[n.Ref] = libvirt.NetworkBinding{Mode: n.Mode, Name: n.Name}
		}
		provider, err := runtimeAdapter.NewPersistentVMProvider(runtimeAdapter.PersistentVMConfig{
			Provider: vm.PersistentConfig{Host: *host, StoragePoolRef: p.StoragePoolRef, StateDir: p.StateDir, VerifyImage: func(ctx context.Context, h domain.VirtualizationHost, image domain.VMImage) error {
				current, err := deps.Repository.Hosts().Get(ctx, h.OrgID, h.ID)
				if err != nil {
					return err
				}
				return policy.VerifyPlaneProvenance(ctx, current, image.ManifestDigest, image.Provenance)
			}}, ImageRoot: p.ImageRoot,
			Libvirt:     libvirt.Config{URI: p.LibvirtURI, EventSocket: p.EventSocket, Networks: networks},
			Firecracker: firecracker.Config{Binary: p.FirecrackerBinary},
		}, d.logger)
		if err != nil {
			return err
		}
		bootstrap, err := service.NewVMBootstrapService(d.secrets, d.resolver, deps.RBAC)
		if err != nil {
			return err
		}
		services.Provider, services.Host, services.Bootstrap = provider, host, bootstrap
		services.VMConfig = service.PersistentVMServiceConfig{Permissions: deps.RBAC, Operator: policy.operator, Services: d.services, Environments: d.environments, Units: d.units}
		services.Principal = func(ctx context.Context, org uuid.UUID) (*auth.Principal, error) {
			p := &auth.Principal{Subject: cfg.ReconcilePubkey, PubKey: cfg.ReconcilePubkey, Method: auth.MethodSystem}
			if err := policy.AuthorizeExecutionPlane(ctx, p, org); err != nil {
				return nil, err
			}
			return p, nil
		}
	}
	if len(cfg.PlaneEndpoints) > 0 && d.pool != nil && d.signer != nil && d.events != nil && d.workers != nil {
		for _, e := range cfg.PlaneEndpoints {
			services.Endpoints = append(services.Endpoints, domain.ExecutionPlaneEndpoint{HostID: e.HostID, EndpointRef: e.EndpointRef, Author: e.Author})
		}
		client, err := loom.NewPlaneClient(d.pool, virtualizationPlaneSigner{d.signer}, services.Endpoints)
		if err != nil {
			return err
		}
		services.PlaneClient, services.PlanePolicy = client, policy
	}
	deps.Services = services
	return nil
}
