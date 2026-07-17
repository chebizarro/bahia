package repository

import "errors"

var (
	// ErrNotFound is returned when a requested resource does not exist.
	ErrNotFound = errors.New("resource not found")
	// ErrConflict is returned when optimistic concurrency rejects a stale update.
	ErrConflict = errors.New("repository conflict")
	// ErrStaleWrite is returned when an older observation loses a freshness guard.
	ErrStaleWrite = errors.New("stale write")
)
