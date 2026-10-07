package nostr

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"

	"go.uber.org/zap"
)

// DNSCanonicalPublisher publishes authoritative DNS state records through the
// shared builder and outbox.
//
// The reconciler calls this after each material reconcile, so each canonical
// record is published once per mutation.
type DNSCanonicalPublisher struct {
	projector *Projector // delegates to publishReplaceableJSON/publishReplaceableTombstone
	logger    *zap.Logger

	mu        sync.Mutex
	published map[string]dnsPublishedEndpoint // coordinate → last published
}

// NewDNSCanonicalPublisher creates a publisher that delegates to the projector's
// shared signing and outbox pipeline.
func NewDNSCanonicalPublisher(projector *Projector, logger *zap.Logger) *DNSCanonicalPublisher {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &DNSCanonicalPublisher{
		projector: projector,
		logger:    logger.Named("dns-canonical"),
		published: make(map[string]dnsPublishedEndpoint),
	}
}

// HydrateFromProjector copies the projector's DNS published cache into this
// publisher so tombstoning covers endpoints already in that cache.
func (p *DNSCanonicalPublisher) HydrateFromProjector() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.projector.dnsPublishMu.Lock()
	defer p.projector.dnsPublishMu.Unlock()
	for coord, ep := range p.projector.dnsPublished {
		if _, ok := p.published[coord]; !ok {
			p.published[coord] = ep
		}
	}
}

// PublishEndpoints publishes DNS endpoint state for all current endpoints and
// tombstones endpoints that have been removed since the last call.
func (p *DNSCanonicalPublisher) PublishEndpoints(ctx context.Context, endpoints []domain.DNSEndpoint) (int, int, error) {
	if p.projector == nil || !p.projector.Enabled() {
		return 0, 0, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	current := make(map[string]dnsPublishedEndpoint, len(endpoints))
	desired := make(map[string]struct{}, len(endpoints))
	var failures []string
	published := 0

	for i := range endpoints {
		endpoint := endpoints[i]
		if err := domain.ValidateDNSEndpoint(&endpoint); err != nil {
			failures = append(failures, fmt.Sprintf("validate endpoint[%d]: %v", i, err))
			p.logger.Warn("skip invalid DNS endpoint", zap.Int("index", i), zap.Error(err))
			continue
		}
		if _, exists := desired[endpoint.Coordinate]; exists {
			failures = append(failures, fmt.Sprintf("duplicate coordinate %q", endpoint.Coordinate))
			continue
		}
		desired[endpoint.Coordinate] = struct{}{}
		payload := dnsEndpointPayload(endpoint)
		if previous, ok := p.published[endpoint.Coordinate]; ok && previous.Payload == payload {
			current[endpoint.Coordinate] = previous
			continue
		}
		tags := dnsEndpointTags(endpoint)
		if err := p.projector.publishReplaceableJSON(ctx, int(kinds.CPStateFamilyDNSEndpoint), endpoint.Coordinate, tags, endpoint, "dns_endpoint.projection", &endpoint.ID); err != nil {
			failures = append(failures, fmt.Sprintf("publish %s: %v", endpoint.Coordinate, err))
			p.logger.Warn("publish DNS endpoint failed", zap.String("coordinate", endpoint.Coordinate), zap.Error(err))
			continue
		}
		current[endpoint.Coordinate] = dnsPublishedEndpoint{FQDN: endpoint.FQDN, Payload: payload}
		published++
	}

	// Build next published set.
	nextPublished := make(map[string]dnsPublishedEndpoint, len(current))
	for coord, ep := range current {
		nextPublished[coord] = ep
	}

	tombstones := 0
	for coordinate, previous := range p.published {
		if _, stillCurrent := current[coordinate]; stillCurrent {
			continue
		}
		if _, stillDesired := desired[coordinate]; stillDesired {
			nextPublished[coordinate] = previous
			continue
		}
		// Endpoint was removed — tombstone it.
		now := time.Now().UTC()
		content := map[string]any{"deleted": true, "coordinate": coordinate, "fqdn": previous.FQDN, "updated_at": formatTime(now)}
		tags := gonostr.Tags{{"t", "bahia"}}
		if fqdn := strings.TrimSpace(previous.FQDN); fqdn != "" {
			tags = append(tags, gonostr.Tag{"dns", fqdn})
		}
		if err := p.projector.publishReplaceableTombstone(ctx, int(kinds.CPStateFamilyDNSEndpoint), coordinate, tags, content, "dns_endpoint.projection", nil); err != nil {
			failures = append(failures, fmt.Sprintf("tombstone %s: %v", coordinate, err))
			p.logger.Warn("publish DNS endpoint tombstone failed", zap.String("coordinate", coordinate), zap.Error(err))
			nextPublished[coordinate] = previous
			continue
		}
		tombstones++
	}
	p.published = nextPublished

	if len(failures) > 0 {
		return published, tombstones, fmt.Errorf("DNS endpoint publish completed with %d failure(s): %s", len(failures), strings.Join(failures, "; "))
	}
	return published, tombstones, nil
}

func dnsEndpointPayload(endpoint domain.DNSEndpoint) string {
	endpoint.MaterializedAt = time.Time{}
	data, _ := json.Marshal(endpoint)
	return string(data)
}

func (p *DNSCanonicalPublisher) PublishEndpoint(ctx context.Context, endpoint domain.DNSEndpoint) error {
	if p.projector == nil || !p.projector.Enabled() {
		return nil
	}
	if err := domain.ValidateDNSEndpoint(&endpoint); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.projector.publishReplaceableJSON(ctx, int(kinds.CPStateFamilyDNSEndpoint), endpoint.Coordinate, dnsEndpointTags(endpoint), endpoint, "dns_endpoint.projection", &endpoint.ID); err != nil {
		return err
	}
	p.published[endpoint.Coordinate] = dnsPublishedEndpoint{FQDN: endpoint.FQDN, Payload: dnsEndpointPayload(endpoint)}
	return nil
}

func (p *DNSCanonicalPublisher) PublishEndpointTombstone(ctx context.Context, endpoint domain.DNSEndpoint) error {
	if p.projector == nil || !p.projector.Enabled() {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	content := map[string]any{"deleted": true, "coordinate": endpoint.Coordinate, "fqdn": endpoint.FQDN, "updated_at": formatTime(time.Now().UTC())}
	if err := p.projector.publishReplaceableTombstone(ctx, int(kinds.CPStateFamilyDNSEndpoint), endpoint.Coordinate,
		gonostr.Tags{{"dns", endpoint.FQDN}, {"t", "bahia"}}, content, "dns_endpoint.projection", &endpoint.ID); err != nil {
		return err
	}
	delete(p.published, endpoint.Coordinate)
	return nil
}

// PublishZone publishes a canonical DNS zone state record.
func (p *DNSCanonicalPublisher) PublishZone(ctx context.Context, zone domain.DNSZone) error {
	if p.projector == nil || !p.projector.Enabled() {
		return nil
	}
	if zone.UpdatedAt.IsZero() {
		zone.UpdatedAt = time.Now().UTC()
	}
	content := map[string]any{
		"deleted":                   false,
		"name":                      zone.Name,
		"visibility":                string(zone.Visibility),
		"backend_ref":               zone.BackendRef,
		"ttl":                       zone.TTL,
		"authoritative":             zone.Authoritative,
		"allow_empty_authoritative": zone.AllowEmptyAuthoritative,
		"updated_at":                formatTime(zone.UpdatedAt),
	}
	tags := gonostr.Tags{
		{"zone", zone.Name},
		{"backend", zone.BackendRef},
		{"visibility", string(zone.Visibility)},
		{"t", "bahia"},
	}
	return p.projector.publishReplaceableJSON(ctx, int(kinds.CPStateFamilyDNSZone), dnsZoneDTag(zone.Name), tags, content, "dns_zone.projection", nil)
}

// PublishZoneTombstone marks a zone as deleted.
func (p *DNSCanonicalPublisher) PublishZoneTombstone(ctx context.Context, zoneName string) error {
	if p.projector == nil || !p.projector.Enabled() {
		return nil
	}
	content := map[string]any{"deleted": true, "name": zoneName, "updated_at": formatTime(time.Now().UTC())}
	tags := gonostr.Tags{{"zone", zoneName}, {"t", "bahia"}}
	return p.projector.publishReplaceableTombstone(ctx, int(kinds.CPStateFamilyDNSZone), dnsZoneDTag(zoneName), tags, content, "dns_zone.projection", nil)
}

// PublishBackend publishes a canonical DNS backend state record.
func (p *DNSCanonicalPublisher) PublishBackend(ctx context.Context, backend domain.DNSBackendState) error {
	if p.projector == nil || !p.projector.Enabled() {
		return nil
	}
	if backend.UpdatedAt.IsZero() {
		backend.UpdatedAt = time.Now().UTC()
	}
	tags := gonostr.Tags{
		{"backend", backend.Ref},
		{"type", string(backend.Type)},
		{"health", string(backend.Health)},
		{"t", "bahia"},
	}
	content := map[string]any{
		"deleted":      false,
		"ref":          backend.Ref,
		"type":         string(backend.Type),
		"health":       string(backend.Health),
		"metadata":     backend.Metadata,
		"last_sync_at": backend.LastSyncAt,
		"updated_at":   formatTime(backend.UpdatedAt),
	}
	if len(backend.ZoneRefs) > 0 {
		zoneNames := make([]string, len(backend.ZoneRefs))
		for i, z := range backend.ZoneRefs {
			zoneNames[i] = z
		}
		content["zones"] = zoneNames
	}
	return p.projector.publishReplaceableJSON(ctx, int(kinds.CPStateFamilyDNSBackend), dnsBackendDTag(backend.Ref), tags, content, "dns_backend.projection", nil)
}

func (p *DNSCanonicalPublisher) PublishBackendTombstone(ctx context.Context, ref string) error {
	if p.projector == nil || !p.projector.Enabled() {
		return nil
	}
	return p.projector.publishReplaceableTombstone(ctx, int(kinds.CPStateFamilyDNSBackend), dnsBackendDTag(ref),
		gonostr.Tags{{"backend", ref}, {"t", "bahia"}},
		map[string]any{"deleted": true, "ref": ref, "updated_at": formatTime(time.Now().UTC())}, "dns_backend.projection", nil)
}

// PublishPolicy publishes a canonical DNS policy state record.
func (p *DNSCanonicalPublisher) PublishPolicy(ctx context.Context, policy domain.DNSPolicy) error {
	if p.projector == nil || !p.projector.Enabled() {
		return nil
	}
	if policy.UpdatedAt.IsZero() {
		policy.UpdatedAt = time.Now().UTC()
	}
	tags := gonostr.Tags{
		{"policy", policy.ID.String()},
		{"t", "bahia"},
	}
	if policy.Name != "" {
		tags = append(tags, gonostr.Tag{"name", policy.Name})
	}
	rulesJSON, _ := json.Marshal(policy.Rules)
	content := map[string]any{
		"deleted":        false,
		"id":             policy.ID.String(),
		"name":           policy.Name,
		"enabled":        policy.Enabled,
		"zone_id":        policy.ZoneID,
		"environment_id": policy.EnvironmentID,
		"metadata":       policy.Metadata,
		"created_at":     formatTime(policy.CreatedAt),
		"rules":          json.RawMessage(rulesJSON),
		"updated_at":     formatTime(policy.UpdatedAt),
	}
	return p.projector.publishReplaceableJSON(ctx, int(kinds.CPStateFamilyDNSPolicy), dnsPolicyDTag(policy.ID), tags, content, "dns_policy.projection", &policy.ID)
}

func (p *DNSCanonicalPublisher) PublishPolicyTombstone(ctx context.Context, id uuid.UUID) error {
	if p.projector == nil || !p.projector.Enabled() {
		return nil
	}
	return p.projector.publishReplaceableTombstone(ctx, int(kinds.CPStateFamilyDNSPolicy), dnsPolicyDTag(id),
		gonostr.Tags{{"policy", id.String()}, {"t", "bahia"}},
		map[string]any{"deleted": true, "id": id.String(), "updated_at": formatTime(time.Now().UTC())}, "dns_policy.projection", &id)
}

// HydrateFromStore loads published DNS endpoint coordinates from the event
// store so tombstoning works correctly after a daemon restart. It is
// idempotent: only fills gaps not already present in memory.
func (p *DNSCanonicalPublisher) HydrateFromStore(ctx context.Context) error {
	if p.projector == nil || p.projector.history == nil {
		return nil
	}
	servicePubkey := ""
	if p.projector.privateKey != "" {
		var err error
		servicePubkey, err = publicKeyHexFromPrivateKeyHex(p.projector.privateKey)
		if err != nil {
			return fmt.Errorf("derive service pubkey for DNS hydration: %w", err)
		}
	}
	records, err := p.projector.liveRetainedControlState(ctx, int(kinds.CPStateFamilyDNSEndpoint), servicePubkey)
	if err != nil {
		return fmt.Errorf("hydrate DNS endpoint cache: %w", err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for d, record := range records {
		if _, ok := p.published[d]; !ok {
			p.published[d] = dnsPublishedEndpoint{FQDN: record.field("dns", "fqdn")}
		}
	}
	p.logger.Info("hydrated DNS endpoint cache from store", zap.Int("loaded", len(records)), zap.Int("total", len(p.published)))
	return nil
}

// PublishZoneSync publishes a zone sync event carrying the full set of DNS
// records for a zone. The agent subscribes to these events instead of
// receiving ContextVM RPC pushes. The created_at timestamp serves as
// the serial; the event ID is the tie-breaker for equal serials.
func (p *DNSCanonicalPublisher) PublishZoneSync(ctx context.Context, zone domain.DNSZone, records []domain.DNSRecord) error {
	if p.projector == nil || !p.projector.Enabled() {
		return nil
	}
	content := map[string]any{
		"zone":    zone,
		"records": records,
	}
	tags := gonostr.Tags{
		{"zone", zone.Name},
		{"t", "dns-zone-sync"},
		{"t", "bahia"},
	}
	if zone.BackendRef != "" {
		tags = append(tags, gonostr.Tag{"backend", zone.BackendRef})
	}
	dTag := "zone-sync:" + zone.Name
	return p.projector.publishReplaceableJSON(ctx, int(kinds.CPStateFamilyDNSZone), dTag, tags, content, "dns_zone_sync.projection", nil)
}
