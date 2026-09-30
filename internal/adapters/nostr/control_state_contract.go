package nostr

import (
	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"

	"github.com/openagentsinc/bahia/internal/domain"
)

// The functions below expose the projector's own envelope and tag builders to
// producers outside this package that must emit byte-identical control-state
// records, such as the cmd/bahia-test-relay seed corpus. They add no policy:
// each one forwards to the builder the projector publishes with, so a change
// to the producer contract changes every caller at once.

// ControlStateEnvelope returns the wire kind and envelope tags (d, domain,
// schema, legacy_kind, deleted) the projector stamps on the record identified
// by (legacyKind, id). See controlStateEnvelope.
func ControlStateEnvelope(legacyKind int, id string, deleted bool) (wireKind int, tags gonostr.Tags) {
	return controlStateEnvelope(legacyKind, id, deleted)
}

// DNSEndpointTags returns the per-endpoint tags (family, health, dns, addr,
// the dns-endpoint t topic, npub/mesh, ...) the projector appends to a live
// DNS endpoint record's envelope.
func DNSEndpointTags(endpoint domain.DNSEndpoint) gonostr.Tags {
	return dnsEndpointTags(endpoint)
}

// DNSZoneDTag, DNSBackendDTag and DNSPolicyDTag return the record ids the
// projector uses for DNS zone, backend and policy state.
func DNSZoneDTag(name string) string { return dnsZoneDTag(name) }

func DNSBackendDTag(ref string) string { return dnsBackendDTag(ref) }

func DNSPolicyDTag(id uuid.UUID) string { return dnsPolicyDTag(id) }
