package secrets

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
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
	if r == nil || r.repo == nil || r.encryptor == nil {
		return "", fmt.Errorf("secret resolver is not configured")
	}
	id, err := uuid.Parse(strings.TrimSpace(ref))
	if err != nil {
		return "", fmt.Errorf("secret ref must be a secret UUID: %w", err)
	}
	secret, err := r.repo.GetByID(ctx, id)
	if err != nil {
		return "", fmt.Errorf("get secret %s: %w", id, err)
	}
	if secret == nil {
		return "", fmt.Errorf("secret %s not found", id)
	}
	value, err := r.encryptor.Decrypt(secret.EncryptedValue, secret.EncryptionMethod)
	if err != nil {
		return "", fmt.Errorf("decrypt secret %s: %w", id, err)
	}
	return value, nil
}
