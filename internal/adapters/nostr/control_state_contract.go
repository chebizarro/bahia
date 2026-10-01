package nostr

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

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

// Service and environment registry records ----------------------------------
//
// The service-registry and environment-registry cp-state coordinates have two
// writers until Phase 3 removes the dual write: the projector, and the
// relay-first registry (internal/service.RelayFirstRegistry), which publishes
// before it writes the local cache. Both build the record here, on the
// envelope controlStateEnvelope derives, so for one entity state they emit one
// tag set and one content serialization (bahia-irsry.41, audit B-5). Before,
// the relay-first record had no t or legacy_kind tag, so #t consumers missed
// it, and its content differed (org_id, repository, timestamp precision), so
// the coordinate alternated between two shapes.

// serviceRegistryRecord returns the family tags and content of a service's
// registry record. svc must already carry the read normalization readers see
// (RegistryService.GetService/ListServices), which is what both writers pass.
func serviceRegistryRecord(svc *domain.Service, deleted bool) (gonostr.Tags, string) {
	content := map[string]any{
		"deleted": deleted,
		"id":      svc.ID.String(),
	}
	putRecordTime(content, "updated_at", svc.UpdatedAt)
	tags := gonostr.Tags{}
	if !deleted {
		if svc.OrgID != uuid.Nil {
			content["org_id"] = svc.OrgID.String()
		}
		content["name"] = svc.Name
		content["repo_url"] = svc.RepoURL
		if svc.Repository != nil {
			content["repository"] = svc.Repository
		}
		content["artifact_repo"] = svc.ArtifactRepo
		content["default_branch"] = svc.DefaultBranch
		content["runtime_type"] = string(svc.RuntimeType)
		putRecordTime(content, "created_at", svc.CreatedAt)
		tags = append(tags,
			gonostr.Tag{"name", svc.Name},
			gonostr.Tag{"runtime", string(svc.RuntimeType)},
		)
	}
	contentJSON, _ := json.Marshal(content)
	return tags, string(contentJSON)
}

// environmentRegistryRecord returns the family tags and content of an
// environment's registry record. The deployment units are the environment's
// implicit default unit; explicit units are not part of the record yet.
func environmentRegistryRecord(env *domain.Environment, deleted bool) (gonostr.Tags, string) {
	snapshot := *env
	domain.NormalizeEnvironmentTargeting(&snapshot)
	content := map[string]any{
		"deleted": deleted,
		"id":      snapshot.ID.String(),
	}
	putRecordTime(content, "updated_at", snapshot.UpdatedAt)
	tags := gonostr.Tags{}
	if !deleted {
		if snapshot.OrgID != uuid.Nil {
			content["org_id"] = snapshot.OrgID.String()
		}
		content["name"] = snapshot.Name
		content["loom_worker_selector"] = recordObject(snapshot.LoomWorkerSelector)
		content["runtime_config"] = recordObject(snapshot.RuntimeConfig)
		content["protected"] = snapshot.Protected
		content["deploy_strategy"] = string(snapshot.DeployStrategy)
		content["targeting"] = snapshot.Targeting
		content["deployment_units"] = []map[string]any{{"key": snapshot.Targeting.DefaultUnitKey, "implicit": true}}
		content["reconcile_mode"] = string(snapshot.Targeting.DefaultReconcileMode)
		putRecordTime(content, "created_at", snapshot.CreatedAt)
		tags = append(tags,
			gonostr.Tag{"name", snapshot.Name},
			gonostr.Tag{"protected", fmt.Sprintf("%t", snapshot.Protected)},
			gonostr.Tag{"unit", snapshot.Targeting.DefaultUnitKey},
			gonostr.Tag{"reconcile_mode", string(snapshot.Targeting.DefaultReconcileMode)},
		)
	}
	contentJSON, _ := json.Marshal(content)
	return tags, string(contentJSON)
}

// putRecordTime stores a registry timestamp at full precision. updated_at is
// the revision clients send back as expected_updated_at, so it must survive
// the round trip exactly; a zero time is omitted rather than written as "",
// which a time.Time decoder rejects.
func putRecordTime(content map[string]any, key string, t time.Time) {
	if t.IsZero() {
		return
	}
	content[key] = t.UTC().Format(time.RFC3339Nano)
}

// recordObject makes a nil and an empty free-form map serialize alike, so a
// write intent (nil) and the cached row ({}) produce the same record.
func recordObject(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

// RelayAcceptPublisher publishes one signed event and reports how many relays
// accepted it. RelayPool implements it.
type RelayAcceptPublisher interface {
	Publish(ctx context.Context, ev gonostr.Event) (int, error)
}

// RelayFirstStatePublisher is the relay-first registry's writer of service
// and environment registry records (service.RelayFirstStatePublisher). It
// builds each record with the projector's builders and signs it under the
// projector's per-coordinate lock, created_at floor and fingerprint memory:
// a later projection of the same state is not signed again, and any later
// event on the coordinate (from either writer) is strictly newer.
//
// Unlike the projector it publishes synchronously to relays instead of through
// the outbox, and fails unless a relay accepted the event: the caller writes
// its cache only after that, and a rejected write must leave nothing queued
// for later delivery.
type RelayFirstStatePublisher struct {
	projector *Projector
	relays    RelayAcceptPublisher
}

// NewRelayFirstStatePublisher returns the relay-first writer that shares
// projector's coordinate state and publishes to relays (the control-plane
// relays the projector targets).
func NewRelayFirstStatePublisher(projector *Projector, relays RelayAcceptPublisher) *RelayFirstStatePublisher {
	return &RelayFirstStatePublisher{projector: projector, relays: relays}
}

// PublishServiceRegistry publishes svc's service-registry record (or its
// tombstone when deleted).
func (r *RelayFirstStatePublisher) PublishServiceRegistry(ctx context.Context, svc *domain.Service, deleted bool) error {
	if svc == nil {
		return fmt.Errorf("service is nil")
	}
	tags, content := serviceRegistryRecord(svc, deleted)
	return r.publish(ctx, KindServiceRegistry, svc.ID.String(), deleted, tags, content)
}

// PublishEnvironmentRegistry publishes env's environment-registry record (or
// its tombstone when deleted).
func (r *RelayFirstStatePublisher) PublishEnvironmentRegistry(ctx context.Context, env *domain.Environment, deleted bool) error {
	if env == nil {
		return fmt.Errorf("environment is nil")
	}
	tags, content := environmentRegistryRecord(env, deleted)
	return r.publish(ctx, KindEnvironmentRegistry, env.ID.String(), deleted, tags, content)
}

func (r *RelayFirstStatePublisher) publish(ctx context.Context, legacyKind int, id string, deleted bool, tags gonostr.Tags, content string) error {
	if r == nil || r.projector == nil {
		return fmt.Errorf("relay-first state publisher is not configured")
	}
	if r.relays == nil {
		return fmt.Errorf("relay-first state publisher has no relays")
	}
	wireKind, baseTags := controlStateEnvelope(legacyKind, id, deleted)
	return r.projector.publishSignedRelayFirst(ctx, wireKind, append(baseTags, tags...), content, r.relays)
}
