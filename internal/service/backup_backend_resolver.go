package service

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
)

// BackupCapability identifies one lifecycle operation a backup backend can honestly execute.
type BackupCapability string

const (
	BackupCapabilitySnapshotCreate BackupCapability = "snapshot_create"
	BackupCapabilitySnapshotVerify BackupCapability = "snapshot_verify"
	BackupCapabilityRestore        BackupCapability = "restore"
	BackupCapabilityRetention      BackupCapability = "retention"
	BackupCapabilityProbe          BackupCapability = "probe"
)

// BackendCapabilities declares the lifecycle operations a backend supports.
type BackendCapabilities struct {
	SnapshotCreate bool `json:"snapshot_create"`
	SnapshotVerify bool `json:"snapshot_verify"`
	Restore        bool `json:"restore"`
	Retention      bool `json:"retention"`
	Probe          bool `json:"probe"`
}

func (c BackendCapabilities) Supports(capability BackupCapability) bool {
	switch capability {
	case BackupCapabilitySnapshotCreate:
		return c.SnapshotCreate
	case BackupCapabilitySnapshotVerify:
		return c.SnapshotVerify
	case BackupCapabilityRestore:
		return c.Restore
	case BackupCapabilityRetention:
		return c.Retention
	case BackupCapabilityProbe:
		return c.Probe
	default:
		return false
	}
}

func (c BackendCapabilities) Missing(required ...BackupCapability) []BackupCapability {
	missing := make([]BackupCapability, 0)
	for _, capability := range required {
		if !c.Supports(capability) {
			missing = append(missing, capability)
		}
	}
	return missing
}

// BackupBackend is the base runtime boundary shared by all backup backend capabilities.
type BackupBackend interface {
	BackendKind() domain.BackupBackendKind
	Capabilities() BackendCapabilities
	Health(ctx context.Context, repo *domain.BackupRepository) error
}

// BackupSnapshotCreateBackend creates snapshots for backup runs.
type BackupSnapshotCreateBackend interface {
	CreateSnapshot(ctx context.Context, req BackupSnapshotRequest) (*BackupSnapshotResult, error)
}

// BackupSnapshotVerifyBackend verifies snapshots for backup runs.
type BackupSnapshotVerifyBackend interface {
	VerifySnapshot(ctx context.Context, req BackupVerifyRequest) (*BackupVerifyResult, error)
}

// BackupSnapshotBackend creates and verifies snapshots for backup runs.
type BackupSnapshotBackend interface {
	BackupSnapshotCreateBackend
	BackupSnapshotVerifyBackend
}

// BackupRestoreBackend restores a previously verified backend snapshot.
type BackupRestoreBackend interface {
	Restore(ctx context.Context, req BackupRestoreRequest) (*BackupRestoreResult, error)
}

// BackupRetentionBackend delegates retention enforcement to backend-native policy semantics.
type BackupRetentionBackend interface {
	EnforceRetention(ctx context.Context, req BackupRetentionRequest) (*BackupRetentionResult, error)
}

// BackupBackendResolver resolves backend capabilities by durable run backend kind.
type BackupBackendResolver interface {
	Resolve(kind domain.BackupBackendKind) (BackupBackend, bool)
	Capabilities(kind domain.BackupBackendKind) (BackendCapabilities, bool)
	Supports(kind domain.BackupBackendKind, capability BackupCapability) bool
	RequireCapabilities(kind domain.BackupBackendKind, required ...BackupCapability) (BackendCapabilities, error)
}

type StaticBackupBackendResolver struct {
	backends map[domain.BackupBackendKind]BackupBackend
}

func NewStaticBackupBackendResolver(backends ...BackupBackend) (*StaticBackupBackendResolver, error) {
	resolver := &StaticBackupBackendResolver{backends: map[domain.BackupBackendKind]BackupBackend{}}
	for _, backend := range backends {
		if backend == nil {
			continue
		}
		kind := backend.BackendKind()
		if !kind.IsValid() {
			return nil, fmt.Errorf("%w: backup backend kind %q is not valid", ErrBackupBackendConfiguration, kind)
		}
		if _, exists := resolver.backends[kind]; exists {
			return nil, fmt.Errorf("%w: duplicate backup backend registration for %q", ErrBackupBackendConfiguration, kind)
		}
		if err := validateBackendCapabilityDeclaration(kind, backend); err != nil {
			return nil, err
		}
		resolver.backends[kind] = backend
	}
	return resolver, nil
}

func MustBackupBackendResolver(backends ...BackupBackend) *StaticBackupBackendResolver {
	resolver, err := NewStaticBackupBackendResolver(backends...)
	if err != nil {
		panic(err)
	}
	return resolver
}

func validateBackendCapabilityDeclaration(kind domain.BackupBackendKind, backend BackupBackend) error {
	capabilities := backend.Capabilities()
	mismatches := make([]string, 0)
	check := func(capability BackupCapability, declared bool, implemented bool) {
		if declared != implemented {
			mismatches = append(mismatches, fmt.Sprintf("%s declared=%t implemented=%t", capability, declared, implemented))
		}
	}
	_, createsSnapshots := backend.(BackupSnapshotCreateBackend)
	_, verifiesSnapshots := backend.(BackupSnapshotVerifyBackend)
	_, restores := backend.(BackupRestoreBackend)
	_, enforcesRetention := backend.(BackupRetentionBackend)
	check(BackupCapabilitySnapshotCreate, capabilities.SnapshotCreate, createsSnapshots)
	check(BackupCapabilitySnapshotVerify, capabilities.SnapshotVerify, verifiesSnapshots)
	check(BackupCapabilityRestore, capabilities.Restore, restores)
	check(BackupCapabilityRetention, capabilities.Retention, enforcesRetention)
	check(BackupCapabilityProbe, capabilities.Probe, true)
	if len(mismatches) > 0 {
		return fmt.Errorf("%w: backup backend %q capability declaration is not truthful: %s", ErrBackupBackendConfiguration, kind, strings.Join(mismatches, "; "))
	}
	return nil
}

func (r *StaticBackupBackendResolver) Resolve(kind domain.BackupBackendKind) (BackupBackend, bool) {
	if r == nil || len(r.backends) == 0 {
		return nil, false
	}
	backend, ok := r.backends[kind]
	return backend, ok
}

func (r *StaticBackupBackendResolver) Capabilities(kind domain.BackupBackendKind) (BackendCapabilities, bool) {
	backend, ok := r.Resolve(kind)
	if !ok || backend == nil {
		return BackendCapabilities{}, false
	}
	return backend.Capabilities(), true
}

func (r *StaticBackupBackendResolver) Supports(kind domain.BackupBackendKind, capability BackupCapability) bool {
	capabilities, ok := r.Capabilities(kind)
	return ok && capabilities.Supports(capability)
}

func (r *StaticBackupBackendResolver) RequireCapabilities(kind domain.BackupBackendKind, required ...BackupCapability) (BackendCapabilities, error) {
	capabilities, ok := r.Capabilities(kind)
	if !ok {
		return BackendCapabilities{}, fmt.Errorf("%w: backup backend %q is not registered", ErrBackupBackendUnsupported, kind)
	}
	if missing := capabilities.Missing(required...); len(missing) > 0 {
		return capabilities, fmt.Errorf("%w: backup backend %q does not support required capabilities %v", ErrBackupBackendUnsupported, kind, missing)
	}
	return capabilities, nil
}

func (r *StaticBackupBackendResolver) Kinds() []domain.BackupBackendKind {
	if r == nil || len(r.backends) == 0 {
		return nil
	}
	kinds := make([]domain.BackupBackendKind, 0, len(r.backends))
	for kind := range r.backends {
		kinds = append(kinds, kind)
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })
	return kinds
}

// BackupRestoreRequest and BackupRestoreResult are restore execution contracts.
type BackupRestoreRequest struct {
	Run        *domain.BackupRestoreRun `json:"-"`
	SourceRun  *domain.BackupRun        `json:"-"`
	Recipe     *domain.BackupRecipe     `json:"-"`
	Repository *domain.BackupRepository `json:"-"`
	Policy     *domain.BackupPolicy     `json:"-"`
}

type BackupRestoreResult struct {
	Verified           bool                            `json:"verified"`
	VerificationStatus domain.BackupVerificationStatus `json:"verification_status"`
	Evidence           map[string]any                  `json:"evidence,omitempty"`
	Error              string                          `json:"error,omitempty"`
}

// BackupRetentionRequest and BackupRetentionResult are retention execution contracts.
type BackupRetentionRequest struct {
	Run        *domain.BackupRetentionRun `json:"-"`
	Repository *domain.BackupRepository   `json:"-"`
	Policy     *domain.BackupPolicy       `json:"-"`
}

type BackupRetentionResult struct {
	Evidence map[string]any `json:"evidence,omitempty"`
	Error    string         `json:"error,omitempty"`
}
