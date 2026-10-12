// Package secretstore is Bahia's seam to the external store that holds secret
// values (docs/architecture/secret-store.md). Bahia keeps only references
// (domain.SecretCredentialRef) in canonical records; values are fetched at use
// time under a short store-issued lease, held in memory and zeroized. Every
// failure is fail-closed: callers receive one of the sentinels below and never
// a fallback value.
package secretstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

// BackendSignet is the Signet generic secret store, the only backend.
const BackendSignet = "signet"

// Sentinels. Implementations wrap them with the store's own reason code;
// callers test with errors.Is.
var (
	// ErrNotConfigured: no secret store is configured for this daemon.
	ErrNotConfigured = errors.New("secret store is not configured")
	// ErrUnavailable: the store did not answer, refused for an internal
	// reason, or is rate limiting. Retrying later may succeed.
	ErrUnavailable = errors.New("secret store is unavailable")
	// ErrInvalidRef: the reference is malformed or names another backend.
	ErrInvalidRef = errors.New("invalid secret store reference")
	// ErrNotFound: the store has no such credential.
	ErrNotFound = errors.New("credential not found in the secret store")
	// ErrRevoked: the credential was revoked.
	ErrRevoked = errors.New("credential is revoked")
	// ErrExpired: the credential is past its expiry.
	ErrExpired = errors.New("credential is expired")
	// ErrDenied: the store refused this daemon access to the credential.
	ErrDenied = errors.New("secret store denied access")
	// ErrVersionMismatch: the credential's active version is not the
	// version the reference pins (it was rotated, or the reference is stale).
	ErrVersionMismatch = errors.New("credential version does not match the reference")
)

// Status is a credential's lifecycle state in the store.
type Status string

const (
	StatusActive  Status = "active"
	StatusExpired Status = "expired"
	StatusRevoked Status = "revoked"
)

// Metadata is payload-free credential metadata. Version is the credential's
// active version.
type Metadata struct {
	CredentialID string
	Owner        string
	Type         string
	Label        string
	Version      int
	Status       Status
	CreatedAt    time.Time
	RotatedAt    time.Time
	ExpiresAt    time.Time
	RevokedAt    time.Time
}

// Scope is the tenant a credential must belong to. One store owner holds
// every tenant's credentials, so the store releases a credential only when
// its label is in the scope's namespace, "<org-id>/<service-id>/<name>"
// (LabelPrefix). Labels are set by whoever imports the credential into the
// store, which requires store administrator rights, not Bahia org roles.
type Scope struct {
	OrgID     uuid.UUID
	ServiceID uuid.UUID
}

// LabelPrefix is the label namespace of the scope's credentials.
func (s Scope) LabelPrefix() string {
	return s.OrgID.String() + "/" + s.ServiceID.String() + "/"
}

// Admits reports whether label names a credential of this scope.
func (s Scope) Admits(label string) bool {
	prefix := s.LabelPrefix()
	return s.OrgID != uuid.Nil && s.ServiceID != uuid.Nil && len(label) > len(prefix) && strings.HasPrefix(label, prefix)
}

// SecretStore resolves and inspects externally held secret values.
// Implementations enforce scope on every call and fail closed with
// ErrDenied for a credential outside it.
type SecretStore interface {
	// Resolve fetches the value ref pins under a short store-issued lease.
	// It fails with ErrVersionMismatch unless the delivered value is exactly
	// ref.Version. The caller owns the Value and must Zeroize it.
	Resolve(ctx context.Context, ref domain.SecretCredentialRef, scope Scope) (*Value, error)
	// ResolveAll resolves several references for one operation, all or
	// nothing: on any failure it zeroizes every value already resolved and
	// returns a *BatchError naming the failing request. Its round trips are
	// paced as one declared outbound operation instead of being refused by
	// the burst budget. The caller owns the Values and must Zeroize them.
	ResolveAll(ctx context.Context, reqs []Request) ([]*Value, error)
	// Inspect returns metadata for one credential of scope.
	Inspect(ctx context.Context, credentialID string, scope Scope) (Metadata, error)
	// Health performs an authenticated, payload-free round trip to the store.
	Health(ctx context.Context) error
}

// Request is one reference to resolve and the scope it must belong to.
type Request struct {
	Ref   domain.SecretCredentialRef
	Scope Scope
}

// BatchError is the failure of request Index of a ResolveAll.
type BatchError struct {
	Index int
	Err   error
}

func (e *BatchError) Error() string { return fmt.Sprintf("request %d: %v", e.Index, e.Err) }
func (e *BatchError) Unwrap() error { return e.Err }

// ZeroizeAll zeroizes every non-nil value.
func ZeroizeAll(values []*Value) {
	for _, v := range values {
		if v != nil {
			v.Zeroize()
		}
	}
}

// ValidateRef checks the backend-independent shape of a reference.
func ValidateRef(ref domain.SecretCredentialRef) error {
	if ref.Store != BackendSignet {
		return fmt.Errorf("%w: store %q is not supported (want %q)", ErrInvalidRef, ref.Store, BackendSignet)
	}
	id := ref.CredentialID
	if id == "" || len(id) > 128 || strings.TrimSpace(id) != id || strings.ContainsAny(id, " \t\r\n") {
		return fmt.Errorf("%w: credential_id must be a non-empty token of at most 128 characters", ErrInvalidRef)
	}
	if ref.Version < 1 {
		return fmt.Errorf("%w: version must be >= 1", ErrInvalidRef)
	}
	return nil
}

// Value is a resolved secret value. It holds the only copy Bahia controls;
// Zeroize overwrites it. Formatting a Value never prints the secret.
type Value struct {
	mu             sync.Mutex
	data           []byte
	leaseExpiresAt time.Time
}

// NewValue takes ownership of data.
func NewValue(data []byte, leaseExpiresAt time.Time) *Value {
	return &Value{data: data, leaseExpiresAt: leaseExpiresAt}
}

// Bytes returns the value, or nil after Zeroize. The slice aliases the
// Value's buffer: callers must not retain it past Zeroize.
func (v *Value) Bytes() []byte {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.data
}

// LeaseExpiresAt is when the store's delivery lease ends. Callers use the
// value within the operation that resolved it and resolve again afterwards;
// they never keep it past this time.
func (v *Value) LeaseExpiresAt() time.Time { return v.leaseExpiresAt }

// Zeroize overwrites the value and drops it. It is idempotent.
func (v *Value) Zeroize() {
	v.mu.Lock()
	defer v.mu.Unlock()
	clear(v.data)
	v.data = nil
}

// String redacts the value.
func (v *Value) String() string { return "secretstore.Value(redacted)" }

// GoString redacts the value under %#v.
func (v *Value) GoString() string { return v.String() }
