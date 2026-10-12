package secretstore

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

// Auditor records secret access; repository.PgSecretRepository implements
// it. It is optional: without PostgreSQL, Signet's own audit of every
// delivery remains.
type Auditor interface {
	RecordSecretAccessAudit(ctx context.Context, audit *domain.SecretAccessAudit) error
}

// TextResolver serves consumers whose APIs take secret text (HTTP headers,
// Git credentials, registry passwords): it resolves a secret reference (a
// secret UUID, as configuration and records carry it), copies the value
// into a Go string, zeroizes the store's buffer and records the access.
// Go strings cannot be overwritten; callers keep the text only for the
// operation, or for the life of the process for a process-held credential.
type TextResolver struct {
	resolver     *Resolver
	audit        Auditor
	onAuditError func(secretID uuid.UUID, err error)
}

// NewTextResolver returns a text resolver over resolver; audit may be nil.
// onAuditError, if set, is told about an access row that could not be
// written (the resolve still succeeds).
func NewTextResolver(resolver *Resolver, audit Auditor, onAuditError func(secretID uuid.UUID, err error)) *TextResolver {
	return &TextResolver{resolver: resolver, audit: audit, onAuditError: onAuditError}
}

// ResolveSecret resolves ref to text.
func (t *TextResolver) ResolveSecret(ctx context.Context, ref string) (string, error) {
	value, _, err := t.ResolveSecretWithAudit(ctx, ref, domain.SecretResolveOptions{})
	return value, err
}

// ResolveSecretWithAudit resolves ref to text and returns its access
// manifest (never the value).
func (t *TextResolver) ResolveSecretWithAudit(ctx context.Context, ref string, opts domain.SecretResolveOptions) (string, domain.SecretAccessManifest, error) {
	values, manifests, err := t.ResolveSecretsWithAudit(ctx, []string{ref}, opts)
	if err != nil {
		return "", domain.SecretAccessManifest{Operation: operation(opts)}, err
	}
	return values[0], manifests[0], nil
}

// ResolveSecretsWithAudit resolves refs for one operation (one paced
// batch), all or nothing.
func (t *TextResolver) ResolveSecretsWithAudit(ctx context.Context, refs []string, opts domain.SecretResolveOptions) ([]string, []domain.SecretAccessManifest, error) {
	held, err := t.resolve(ctx, refs, opts)
	if err != nil {
		return nil, nil, err
	}
	return held.Values, held.Manifests, nil
}

// Held is a batch of secret texts a process may keep until ValidUntil, the
// earliest store lease of the batch; it then resolves them again, which
// also notices a revoked, expired or re-pointed credential.
type Held struct {
	Values     []string
	Manifests  []domain.SecretAccessManifest
	Pins       []domain.SecretCredentialRef
	ValidUntil time.Time
}

// ResolveHeld resolves refs as one batch for a process-held credential.
func (t *TextResolver) ResolveHeld(ctx context.Context, refs []string, opts domain.SecretResolveOptions) (Held, error) {
	return t.resolve(ctx, refs, opts)
}

// ResolveSecrets resolves refs as one batch; the values may be held until
// validUntil (package backends).
func (t *TextResolver) ResolveSecrets(ctx context.Context, refs []string) ([]string, time.Time, error) {
	held, err := t.resolve(ctx, refs, domain.SecretResolveOptions{Actor: "bahia", Reason: "package backend credentials"})
	if err != nil {
		return nil, time.Time{}, err
	}
	return held.Values, held.ValidUntil, nil
}

func (t *TextResolver) resolve(ctx context.Context, refs []string, opts domain.SecretResolveOptions) (Held, error) {
	if t == nil {
		return Held{}, fmt.Errorf("%w: secret values resolve only through secret_store", ErrNotConfigured)
	}
	ids := make([]uuid.UUID, 0, len(refs))
	for _, ref := range refs {
		id, err := uuid.Parse(strings.TrimSpace(ref))
		if err != nil || id == uuid.Nil {
			return Held{}, fmt.Errorf("secret ref must be a secret UUID")
		}
		ids = append(ids, id)
	}
	var resolved []Resolved
	var err error
	if len(ids) == 1 {
		var one Resolved
		one, err = t.resolver.Resolve(ctx, ids[0])
		resolved = []Resolved{one}
	} else {
		resolved, err = t.resolver.ResolveAll(ctx, ids)
	}
	if err != nil {
		return Held{}, err
	}
	defer ZeroizeResolved(resolved)
	accessedAt := time.Now().UTC()
	held := Held{
		Values:    make([]string, 0, len(resolved)),
		Manifests: make([]domain.SecretAccessManifest, 0, len(resolved)),
		Pins:      make([]domain.SecretCredentialRef, 0, len(resolved)),
	}
	for _, r := range resolved {
		manifest := domain.SecretAccessManifest{
			SecretID:      r.Secret.ID,
			Version:       r.Secret.CredentialRef.Version,
			RecordVersion: r.Secret.Version,
			ServiceID:     r.Secret.ServiceID,
			EnvironmentID: r.Secret.EnvironmentID,
			Name:          r.Secret.Name,
			Operation:     operation(opts),
			Outcome:       domain.SecretAccessOutcomeSuccess,
			AccessedAt:    accessedAt,
		}
		t.recordAudit(ctx, manifest, opts)
		held.Values = append(held.Values, string(r.Value.Bytes()))
		held.Manifests = append(held.Manifests, manifest)
		held.Pins = append(held.Pins, *r.Secret.CredentialRef)
		if lease := r.Value.LeaseExpiresAt(); held.ValidUntil.IsZero() || lease.Before(held.ValidUntil) {
			held.ValidUntil = lease
		}
	}
	return held, nil
}

// recordAudit writes the access row when PostgreSQL is configured. Its
// failure does not fail the resolve: the secret may not be indexed yet (the
// audit table references the index row and the service row), PostgreSQL is
// optional, and Signet's audit chain records every delivery regardless.
func (t *TextResolver) recordAudit(ctx context.Context, manifest domain.SecretAccessManifest, opts domain.SecretResolveOptions) {
	if t.audit == nil {
		return
	}
	if err := t.audit.RecordSecretAccessAudit(ctx, &domain.SecretAccessAudit{
		SecretID: manifest.SecretID, Version: manifest.Version, ServiceID: manifest.ServiceID,
		EnvironmentID: manifest.EnvironmentID, Operation: manifest.Operation, Outcome: manifest.Outcome,
		Actor: opts.Actor, Reason: opts.Reason, RequestID: opts.RequestID, AccessedAt: manifest.AccessedAt,
	}); err != nil && t.onAuditError != nil {
		t.onAuditError(manifest.SecretID, err)
	}
}

// CurrentReference is the credential reference a secret's canonical record
// pins now, read from the local event store without contacting the store.
// Holders of a process-held credential compare it to decide whether the
// secret was re-pointed.
func (t *TextResolver) CurrentReference(ctx context.Context, ref string) (domain.SecretCredentialRef, error) {
	if t == nil {
		return domain.SecretCredentialRef{}, fmt.Errorf("%w: secret values resolve only through secret_store", ErrNotConfigured)
	}
	id, err := uuid.Parse(strings.TrimSpace(ref))
	if err != nil || id == uuid.Nil {
		return domain.SecretCredentialRef{}, fmt.Errorf("secret ref must be a secret UUID")
	}
	_, req, err := t.resolver.target(ctx, id)
	if err != nil {
		return domain.SecretCredentialRef{}, err
	}
	return req.Ref, nil
}

func operation(opts domain.SecretResolveOptions) domain.SecretAccessOperation {
	if opts.Operation == "" {
		return domain.SecretAccessOperationResolve
	}
	return opts.Operation
}
