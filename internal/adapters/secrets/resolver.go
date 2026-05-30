package secrets

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
)

// Resolver resolves stored Bahia service secrets into plaintext values.
type Resolver struct {
	repo      repository.SecretRepository
	encryptor *Encryptor
}

func NewResolver(repo repository.SecretRepository, encryptor *Encryptor) *Resolver {
	return &Resolver{repo: repo, encryptor: encryptor}
}

func (r *Resolver) ResolveSecret(ctx context.Context, ref string) (string, error) {
	value, _, err := r.ResolveSecretWithAudit(ctx, ref, domain.SecretResolveOptions{})
	return value, err
}

// ResolveSecretWithAudit returns the plaintext value and a safe audit manifest.
// The manifest contains version/access metadata only; it never contains the
// plaintext value or encrypted payload.
func (r *Resolver) ResolveSecretWithAudit(ctx context.Context, ref string, opts domain.SecretResolveOptions) (string, domain.SecretAccessManifest, error) {
	manifest, err := emptyManifest(opts)
	if err != nil {
		return "", manifest, err
	}
	if r == nil || r.repo == nil || r.encryptor == nil {
		return "", manifest, fmt.Errorf("secret resolver is not configured")
	}
	id, err := uuid.Parse(strings.TrimSpace(ref))
	if err != nil {
		return "", manifest, fmt.Errorf("secret ref must be a secret UUID: %w", err)
	}
	secret, err := r.repo.GetByID(ctx, id)
	if err != nil {
		return "", manifest, fmt.Errorf("get secret %s: %w", id, err)
	}
	if secret == nil {
		return "", manifest, fmt.Errorf("secret %s not found", id)
	}
	version, err := r.repo.GetCurrentVersion(ctx, id)
	if err != nil {
		return "", manifest, fmt.Errorf("get current secret version %s: %w", id, err)
	}
	if version == nil {
		return "", manifest, fmt.Errorf("secret %s has no versioned payload", id)
	}

	accessedAt := time.Now().UTC()
	manifest = domain.SecretAccessManifest{
		SecretID:      secret.ID,
		VersionID:     version.ID,
		Version:       version.Version,
		ServiceID:     secret.ServiceID,
		EnvironmentID: secret.EnvironmentID,
		Name:          secret.Name,
		Operation:     normalizeSecretAccessOperation(opts.Operation),
		Outcome:       domain.SecretAccessOutcomeSuccess,
		AccessedAt:    accessedAt,
	}

	value, decryptErr := r.encryptor.Decrypt(version.EncryptedValue, version.EncryptionMethod)
	audit := &domain.SecretAccessAudit{
		SecretID:      secret.ID,
		VersionID:     version.ID,
		Version:       version.Version,
		ServiceID:     secret.ServiceID,
		EnvironmentID: secret.EnvironmentID,
		Operation:     manifest.Operation,
		Outcome:       domain.SecretAccessOutcomeSuccess,
		Actor:         opts.Actor,
		Reason:        opts.Reason,
		RequestID:     opts.RequestID,
		AccessedAt:    accessedAt,
	}
	if decryptErr != nil {
		manifest.Outcome = domain.SecretAccessOutcomeFailure
		audit.Outcome = domain.SecretAccessOutcomeFailure
		audit.Error = decryptErr.Error()
	}
	if auditErr := r.repo.RecordSecretAccessAudit(ctx, audit); auditErr != nil {
		return "", manifest, auditErr
	}
	if decryptErr != nil {
		return "", manifest, fmt.Errorf("decrypt secret %s version %d: %w", id, version.Version, decryptErr)
	}
	return value, manifest, nil
}

func emptyManifest(opts domain.SecretResolveOptions) (domain.SecretAccessManifest, error) {
	return domain.SecretAccessManifest{Operation: normalizeSecretAccessOperation(opts.Operation)}, nil
}

func normalizeSecretAccessOperation(op domain.SecretAccessOperation) domain.SecretAccessOperation {
	if op == "" {
		return domain.SecretAccessOperationResolve
	}
	return op
}
