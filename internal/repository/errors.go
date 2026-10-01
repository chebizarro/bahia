package repository

import "errors"

var (
	// ErrNotFound is returned when a requested resource does not exist.
	ErrNotFound = errors.New("resource not found")
	// ErrConflict is returned when optimistic concurrency rejects a stale update.
	ErrConflict = errors.New("repository conflict")
	// ErrStaleRevision identifies a retryable resource-revision precondition conflict.
	ErrStaleRevision = errors.New("stale resource revision")
	// ErrStaleWrite is returned when an older observation loses a freshness guard.
	ErrStaleWrite = errors.New("stale write")
	// ErrAlreadyExists is returned when a create names an id that is already
	// stored. Ids may be client-minted (bahia-irsry.35), so callers resolve it
	// by comparing content: same content is an idempotent retry, different
	// content is domain.ErrEntityIDConflict.
	ErrAlreadyExists = errors.New("resource already exists")
)
