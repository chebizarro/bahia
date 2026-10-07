package domain

import "time"

// Registry revisions.
//
// A registry row's updated_at is its revision: clients send it back as
// expected_updated_at, and the relay-first writer signs it into the
// cp-state record before Postgres stores the row. Postgres timestamptz keeps
// microseconds, so a revision minted with Go's nanosecond clock would be
// published with digits the stored row not has, and the token a client
// read from the relay would never match the database. Every revision is
// therefore minted, compared and published at RevisionPrecision.

// RevisionPrecision is the precision Postgres stores timestamptz values at.
const RevisionPrecision = time.Microsecond

// NormalizeRevisionTime returns t in UTC at RevisionPrecision, the value the
// database will hold for it. The zero time stays zero.
func NormalizeRevisionTime(t time.Time) time.Time {
	if t.IsZero() {
		return t
	}
	return t.UTC().Truncate(RevisionPrecision)
}

// NewRevisionTime mints a revision for "now" at RevisionPrecision.
func NewRevisionTime() time.Time {
	return NormalizeRevisionTime(time.Now())
}

// NextRevisionTime mints a revision strictly after prev, so an update always
// moves the token even when the clock has not advanced past (or lags behind)
// the stored revision.
func NextRevisionTime(prev time.Time) time.Time {
	next := NewRevisionTime()
	if prev = NormalizeRevisionTime(prev); !next.After(prev) {
		return prev.Add(RevisionPrecision)
	}
	return next
}

// SameRevision reports whether two revision tokens name the same stored
// revision, ignoring precision the database does not keep.
func SameRevision(a, b time.Time) bool {
	return NormalizeRevisionTime(a).Equal(NormalizeRevisionTime(b))
}

// StampCreateRevision fixes a create's created_at/updated_at before anything
// is published or written: unset values become "now" (updated_at defaults to
// created_at), and set ones are kept at RevisionPrecision. The registry calls
// it before the relay-first publish and the repositories keep the result, so
// the signed record and the stored row carry the same timestamps.
func StampCreateRevision(createdAt, updatedAt *time.Time) {
	if createdAt.IsZero() {
		*createdAt = NewRevisionTime()
	}
	*createdAt = NormalizeRevisionTime(*createdAt)
	if updatedAt.IsZero() {
		*updatedAt = *createdAt
	}
	*updatedAt = NormalizeRevisionTime(*updatedAt)
}
