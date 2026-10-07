package controlplane

import (
	"strconv"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// workerCPStateEnvelope builds the canonical cp-state tags every worker record
// carries (bahia-irsry.9.2): kind 30900, schema bahia.cp-state.v1, the family's
// CPStateFamily discriminator in legacy_kind, deleted, and the family's
// single-letter t topic so relays can index the REQ. Live records and
// tombstones for one d share this envelope, so a tombstone replaces the live
// event on the same (kind, author, d) coordinate and stays on the same topic.
func workerCPStateEnvelope(family kinds.CPStateFamily, topic, d string, deleted bool) nostr.Tags {
	return nostr.Tags{
		{kinds.CASControlStateTagD, d},
		{kinds.CASControlStateTagDomain, kinds.WorkerDomain},
		{kinds.CASControlStateTagSchema, kinds.CASControlStateSchema},
		{kinds.CASControlStateTagLegacyKind, family.TagValue()},
		{kinds.CASControlStateTagDeleted, strconv.FormatBool(deleted)},
		{"t", topic},
	}
}
