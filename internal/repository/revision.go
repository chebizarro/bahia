package repository

import (
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

// Registry revisions (bahia-irsry.53). The registry mints a row's
// created_at/updated_at before the relay-first writer signs its cp-state
// record, so the repository must store those values rather than stamp its
// own; otherwise the projection of the stored row differs from the signed
// record and is signed a second time.

// updateRevisionArgs returns the requested and fallback revisions for
// revisionAssignment: the caller's pre-minted revision (when set), and a
// fresh one.
func updateRevisionArgs(requested time.Time) (time.Time, time.Time) {
	fallback := domain.NewRevisionTime()
	requested = domain.NormalizeRevisionTime(requested)
	if requested.IsZero() {
		requested = fallback
	}
	return requested, fallback
}

// revisionAssignment is the updated_at SET expression of a registry update,
// given the placeholders of updateRevisionArgs' two values. A requested
// revision newer than the stored one is kept verbatim (it is the one the
// relay-first record carries); any other request (a stale row written back,
// or a clock behind the stored revision) still moves the revision forward,
// so optimistic-concurrency tokens never repeat. Updates RETURN the stored
// value so the caller holds the exact revision.
func revisionAssignment(requested, fallback string) string {
	return "updated_at = CASE WHEN " + requested + "::timestamptz > updated_at THEN " + requested +
		"::timestamptz ELSE GREATEST(" + fallback + "::timestamptz, updated_at + interval '1 microsecond') END"
}
