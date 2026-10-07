package domain

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// Entity identity.
//
// An entity's id is fixed by whoever authors its create intent, not by
// Postgres. The id is an RFC 9562 UUID in canonical lowercase form. New
// writers mint UUIDv7 (time-ordered, 74 random bits); a create intent may also
// carry a UUIDv4, which is what compatibility rows and crypto.randomUUID produce.
// The id is the addressable coordinate's entity segment, so every existing
// `<prefix>:<uuid>` (or bare `<uuid>`) coordinate stays valid unchanged.
// Uniqueness of natural keys such as (org, name) is a constraint the
// authoritative index enforces; it is never the identity. See
// docs/event-spec.md "Entity identity and coordinates" and
// docs/architecture/entity-identity.md.

var (
	// ErrInvalidEntityID rejects a client-supplied id that is not a
	// canonical RFC 9562 UUIDv4/v7.
	ErrInvalidEntityID = errors.New("invalid entity id")
	// ErrEntityIDConflict reports a create whose id already names an entity
	// with different content. Retrying with the same id and the same content
	// is idempotent; different content needs a new id.
	ErrEntityIDConflict = errors.New("entity id already exists with different content")
)

// EntityIDConflictError is the typed form of ErrEntityIDConflict.
type EntityIDConflictError struct {
	Entity string
	ID     uuid.UUID
}

func (e *EntityIDConflictError) Error() string {
	return fmt.Sprintf("%s id %s already exists with different content; retry with the original content or mint a new id", e.Entity, e.ID)
}

func (e *EntityIDConflictError) Unwrap() error { return ErrEntityIDConflict }

// NewEntityID mints a new entity id (UUIDv7). Servers use it only when a
// create intent carries no id.
func NewEntityID() uuid.UUID {
	return uuid.Must(uuid.NewV7())
}

// ParseClientEntityID validates a client-minted entity id carried in a create
// intent. It must be the canonical 36-character lowercase form (the exact
// bytes that become the coordinate's d segment), use the RFC 9562 variant, be
// version 4 or 7, and not be the nil or max UUID. Name-based (v3/v5) and
// MAC/clock (v1/v6) ids are rejected because they are predictable and would let
// a third party pre-claim another author's coordinate.
func ParseClientEntityID(raw string) (uuid.UUID, error) {
	if len(raw) != 36 {
		return uuid.Nil, fmt.Errorf("%w: %q is not a canonical 36-character UUID", ErrInvalidEntityID, raw)
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: %v", ErrInvalidEntityID, err)
	}
	if id.String() != raw {
		return uuid.Nil, fmt.Errorf("%w: %q must be lowercase canonical form", ErrInvalidEntityID, raw)
	}
	if id == uuid.Nil || id == uuid.Max {
		return uuid.Nil, fmt.Errorf("%w: nil and max UUIDs are reserved", ErrInvalidEntityID)
	}
	if id.Variant() != uuid.RFC4122 {
		return uuid.Nil, fmt.Errorf("%w: %q does not use the RFC 9562 variant", ErrInvalidEntityID, raw)
	}
	if v := id.Version(); v != 4 && v != 7 {
		return uuid.Nil, fmt.Errorf("%w: version %d is not accepted (use UUIDv7, or v4)", ErrInvalidEntityID, v)
	}
	return id, nil
}

// ResolveCreateEntityID returns the client-supplied id when raw is non-empty,
// otherwise a freshly minted one. supplied reports which case applied.
func ResolveCreateEntityID(raw string) (id uuid.UUID, supplied bool, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return NewEntityID(), false, nil
	}
	id, err = ParseClientEntityID(raw)
	if err != nil {
		return uuid.Nil, true, err
	}
	return id, true, nil
}
