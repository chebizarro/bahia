package identityrecovery

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/secretstore"
)

// SecretRegistrySource inventories the canonical, OCK-decrypted secret view.
// It never resolves a Signet value or consults PostgreSQL.
type SecretRegistrySource struct {
	View interface {
		StoreBackedSecrets(context.Context) ([]domain.SecretRef, error)
		ServiceOrgID(context.Context, uuid.UUID) (string, error)
	}
}

func (s SecretRegistrySource) CredentialRefs(ctx context.Context) ([]SecretRef, error) {
	if s.View == nil {
		return nil, ErrAdapterNotRegistered
	}
	secrets, err := s.View.StoreBackedSecrets(ctx)
	if err != nil {
		return nil, err
	}
	refs := make([]SecretRef, 0, len(secrets))
	for _, secret := range secrets {
		if secret.CredentialRef == nil || secret.ServiceID == uuid.Nil || secret.Name == "" {
			return nil, errors.New("canonical secret reference incomplete")
		}
		if err := secretstore.ValidateRef(*secret.CredentialRef); err != nil {
			return nil, errors.New("canonical secret reference invalid")
		}
		org, err := s.View.ServiceOrgID(ctx, secret.ServiceID)
		if err != nil {
			return nil, err
		}
		orgID, err := uuid.Parse(org)
		if err != nil || orgID == uuid.Nil {
			return nil, errors.New("canonical service organization invalid")
		}
		label := orgID.String() + "/" + secret.ServiceID.String() + "/" + secret.Name
		refs = append(refs, SecretRef{Provider: secret.CredentialRef.Store, Label: label, Credential: *secret.CredentialRef})
	}
	return refs, nil
}
