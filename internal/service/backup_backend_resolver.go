package service

import (
	"context"
	"fmt"
	"sort"

	"github.com/openagentsinc/bahia/internal/domain"
)

// BackupBackend is the base runtime boundary shared by all backup backend capabilities.
type BackupBackend interface {
	BackendKind() domain.BackupBackendKind
	Health(ctx context.Context, repo *domain.BackupRepository) error
}

// BackupSnapshotBackend creates and verifies snapshots for backup runs.
type BackupSnapshotBackend interface {
	CreateSnapshot(ctx context.Context, req BackupSnapshotRequest) (*BackupSnapshotResult, error)
	VerifySnapshot(ctx context.Context, req BackupVerifyRequest) (*BackupVerifyResult, error)
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

func (r *StaticBackupBackendResolver) Resolve(kind domain.BackupBackendKind) (BackupBackend, bool) {
	if r == nil || len(r.backends) == 0 {
		return nil, false
	}
	backend, ok := r.backends[kind]
	return backend, ok
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
