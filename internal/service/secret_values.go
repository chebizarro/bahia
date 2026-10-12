package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/secretstore"
)

// SecretValues resolves Bahia secrets by id through the secret store
// (secretstore.Resolver): one declared operation per call, all or nothing.
type SecretValues interface {
	ResolveAll(ctx context.Context, secretIDs []uuid.UUID) ([]secretstore.Resolved, error)
}

// resolveSecretValues resolves the PostgreSQL-indexed secrets rows, in
// order. A row that still holds a value Bahia sealed itself is refused with
// domain.ErrLegacySecretValue before anything is resolved, and a canonical
// record that no longer matches its index row (another service or name) is
// refused rather than served. The caller zeroizes the result.
func resolveSecretValues(ctx context.Context, values SecretValues, rows []domain.ServiceSecret) ([]secretstore.Resolved, error) {
	ids := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		if row.EncryptionMethod != domain.EncryptionStoreRef {
			return nil, domain.LegacySecretValueError(row.ID, row.Name)
		}
		ids = append(ids, row.ID)
	}
	if values == nil {
		return nil, fmt.Errorf("%w: secret values resolve only through secret_store", secretstore.ErrNotConfigured)
	}
	resolved, err := values.ResolveAll(ctx, ids)
	if err != nil {
		return nil, err
	}
	if len(resolved) != len(rows) {
		secretstore.ZeroizeResolved(resolved)
		return nil, fmt.Errorf("secret store resolved %d of %d secrets", len(resolved), len(rows))
	}
	for i, r := range resolved {
		if r.Secret.ID != rows[i].ID || r.Secret.ServiceID != rows[i].ServiceID || r.Secret.Name != rows[i].Name {
			secretstore.ZeroizeResolved(resolved)
			return nil, fmt.Errorf("secret %s (%s): the PostgreSQL index disagrees with the canonical record; it is re-indexed after the next warm start", rows[i].ID, rows[i].Name)
		}
	}
	return resolved, nil
}

// resolvedSecretManifest is the audit manifest of one resolved secret. A
// store-backed secret has no PostgreSQL version row: Version is the
// credential version the canonical record pins.
func resolvedSecretManifest(row domain.ServiceSecret, r secretstore.Resolved, op domain.SecretAccessOperation) domain.SecretAccessManifest {
	return domain.SecretAccessManifest{
		SecretID:      row.ID,
		Version:       r.Secret.CredentialRef.Version,
		ServiceID:     row.ServiceID,
		EnvironmentID: row.EnvironmentID,
		Name:          row.Name,
		Operation:     op,
		Outcome:       domain.SecretAccessOutcomeSuccess,
		AccessedAt:    time.Now().UTC(),
	}
}
