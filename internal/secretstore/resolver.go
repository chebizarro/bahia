package secretstore

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

// ErrNoRecord: the secret has no current canonical record in the daemon's
// local event store (it was deleted, never existed, or the daemon has not
// caught up with the relays yet).
var ErrNoRecord = errors.New("secret has no canonical record")

// Records reads canonical secret records and the org of a service from the
// daemon's local event store (adapters/nostr.CanonicalSecretView).
type Records interface {
	// CurrentSecretRef is the secret's current canonical record, or nil.
	CurrentSecretRef(ctx context.Context, secretID uuid.UUID) (*domain.SecretRef, error)
	// ServiceOrgID is the org of the service's canonical record.
	ServiceOrgID(ctx context.Context, serviceID uuid.UUID) (string, error)
}

// Resolved is a secret's canonical record and its value. The caller owns
// Value and must Zeroize it.
type Resolved struct {
	Secret domain.SecretRef
	Value  *Value
}

// Resolver resolves Bahia secrets, named by their secret id, to values: the
// canonical record supplies the credential reference, the service's
// canonical org bounds the credential's scope, and the store delivers the
// pinned version. It never reads PostgreSQL and never falls back: a secret
// without a credential reference is a legacy value and is refused with
// domain.ErrLegacySecretValue.
type Resolver struct {
	store SecretStore

	mu      sync.RWMutex
	records Records
}

// NewResolver returns a resolver over store; a nil store makes every
// resolve fail with ErrNotConfigured.
func NewResolver(store SecretStore) *Resolver {
	return &Resolver{store: store}
}

// UseRecords attaches the canonical secret records. The daemon builds them
// after its consumers; until they are attached every resolve fails with
// ErrUnavailable.
func (r *Resolver) UseRecords(records Records) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = records
}

// Resolve resolves one secret.
func (r *Resolver) Resolve(ctx context.Context, secretID uuid.UUID) (Resolved, error) {
	secret, req, err := r.target(ctx, secretID)
	if err != nil {
		return Resolved{}, err
	}
	value, err := r.store.Resolve(ctx, req.Ref, req.Scope)
	if err != nil {
		return Resolved{}, secretError(secret, err)
	}
	return Resolved{Secret: secret, Value: value}, nil
}

// ResolveAll resolves secrets for one operation, all or nothing: on failure
// no value is returned and the error names the failing secret. Each distinct
// secret is resolved once; a repeated id gets its own copy of the value, in
// input order, so every Value can be zeroized independently.
func (r *Resolver) ResolveAll(ctx context.Context, secretIDs []uuid.UUID) ([]Resolved, error) {
	if len(secretIDs) == 0 {
		return nil, nil
	}
	first := make(map[uuid.UUID]int, len(secretIDs))
	secrets := make([]domain.SecretRef, 0, len(secretIDs))
	reqs := make([]Request, 0, len(secretIDs))
	for _, id := range secretIDs {
		if _, dup := first[id]; dup {
			continue
		}
		secret, req, err := r.target(ctx, id)
		if err != nil {
			return nil, err
		}
		first[id] = len(secrets)
		secrets = append(secrets, secret)
		reqs = append(reqs, req)
	}
	values, err := r.store.ResolveAll(ctx, reqs)
	if err != nil {
		var batch *BatchError
		if errors.As(err, &batch) && batch.Index >= 0 && batch.Index < len(secrets) {
			return nil, secretError(secrets[batch.Index], batch.Err)
		}
		return nil, err
	}
	if len(values) != len(reqs) {
		ZeroizeAll(values)
		return nil, fmt.Errorf("%w: the store returned %d values for %d secrets", ErrUnavailable, len(values), len(reqs))
	}
	out := make([]Resolved, len(secretIDs))
	used := make([]bool, len(values))
	for i, id := range secretIDs {
		j := first[id]
		value := values[j]
		if used[j] {
			value = NewValue(append([]byte(nil), values[j].Bytes()...), values[j].LeaseExpiresAt())
		}
		used[j] = true
		out[i] = Resolved{Secret: secrets[j], Value: value}
	}
	return out, nil
}

// ZeroizeResolved zeroizes every resolved value.
func ZeroizeResolved(resolved []Resolved) {
	for _, r := range resolved {
		if r.Value != nil {
			r.Value.Zeroize()
		}
	}
}

func (r *Resolver) target(ctx context.Context, secretID uuid.UUID) (domain.SecretRef, Request, error) {
	if r == nil || r.store == nil {
		return domain.SecretRef{}, Request{}, fmt.Errorf("secret %s: %w: secret values resolve only through secret_store (docs/architecture/secret-store.md)", secretID, ErrNotConfigured)
	}
	r.mu.RLock()
	records := r.records
	r.mu.RUnlock()
	if records == nil {
		return domain.SecretRef{}, Request{}, fmt.Errorf("secret %s: %w: canonical secret records are not loaded", secretID, ErrUnavailable)
	}
	secret, err := records.CurrentSecretRef(ctx, secretID)
	if err != nil {
		return domain.SecretRef{}, Request{}, fmt.Errorf("secret %s: read canonical record: %w", secretID, err)
	}
	if secret == nil {
		return domain.SecretRef{}, Request{}, fmt.Errorf("secret %s: %w (deleted, or not yet received from the relays)", secretID, ErrNoRecord)
	}
	if secret.CredentialRef == nil {
		return domain.SecretRef{}, Request{}, domain.LegacySecretValueError(secret.ID, secret.Name)
	}
	org, err := records.ServiceOrgID(ctx, secret.ServiceID)
	if err != nil {
		return domain.SecretRef{}, Request{}, fmt.Errorf("secret %s (%s): %w", secret.ID, secret.Name, err)
	}
	orgID, err := uuid.Parse(org)
	if err != nil || orgID == uuid.Nil {
		return domain.SecretRef{}, Request{}, fmt.Errorf("secret %s (%s): service %s has no organization", secret.ID, secret.Name, secret.ServiceID)
	}
	return *secret, Request{Ref: *secret.CredentialRef, Scope: Scope{OrgID: orgID, ServiceID: secret.ServiceID}}, nil
}

func secretError(secret domain.SecretRef, err error) error {
	return fmt.Errorf("secret %s (%s): %w", secret.ID, secret.Name, err)
}
