package reconcile

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"go.uber.org/zap"
)

const (
	dnsEventZoneSynced           events.EventType = "dns.zone_synced"
	dnsEventRecordChanged        events.EventType = "dns.record_changed"
	dnsEventDriftDetected        events.EventType = "dns.drift_detected"
	dnsEventEndpointRegistered   events.EventType = "dns.endpoint_registered"
	dnsEventEndpointDeregistered events.EventType = "dns.endpoint_deregistered"
	dnsReconcileDebounceWindow                    = 5 * time.Second
	dnsReconcileTriggerBuffer                     = 16
)

// DNSBackend is the narrow zone-snapshot backend interface used by the Phase 0 reconciler.
type DNSBackend interface {
	ListRecords(ctx context.Context, zone domain.DNSZone) ([]domain.DNSRecord, error)
	SyncZone(ctx context.Context, zone domain.DNSZone, records []domain.DNSRecord) error
}

// DNSBackendResolver resolves configured backend references to DNS backends.
type DNSBackendResolver interface {
	Resolve(ref string) (DNSBackend, bool)
}

// DNSReconciler compares projected DNS records with backend snapshots and syncs drift.
type DNSReconciler struct {
	projector *DNSProjector
	zones     []domain.DNSZone
	resolver  DNSBackendResolver
	interval  time.Duration
	logger    *zap.Logger
	publisher events.Publisher
	triggerCh chan struct{}
	debounce  time.Duration
	runMu     sync.Mutex
}

func NewDNSReconciler(projector *DNSProjector, zones []domain.DNSZone, resolver DNSBackendResolver, interval time.Duration, logger *zap.Logger, publisher ...events.Publisher) *DNSReconciler {
	if interval <= 0 {
		interval = time.Minute
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	var pub events.Publisher
	if len(publisher) > 0 {
		pub = publisher[0]
	}
	return &DNSReconciler{projector: projector, zones: append([]domain.DNSZone(nil), zones...), resolver: resolver, interval: interval, logger: logger, publisher: pub, triggerCh: make(chan struct{}, dnsReconcileTriggerBuffer), debounce: dnsReconcileDebounceWindow}
}

func (r *DNSReconciler) Name() string { return "dns-reconciler" }

func (r *DNSReconciler) SetPublisher(publisher events.Publisher) {
	r.publisher = publisher
}

func (r *DNSReconciler) TriggerReconcile() {
	select {
	case r.triggerCh <- struct{}{}:
	default:
	}
}

func (r *DNSReconciler) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		if err := r.ReconcileOnce(ctx); err != nil && ctx.Err() == nil {
			r.logger.Warn("DNS reconcile failed", zap.Error(err))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		case <-r.triggerCh:
			if !r.waitForDebounce(ctx) {
				return nil
			}
		}
	}
}

func (r *DNSReconciler) waitForDebounce(ctx context.Context) bool {
	debounce := r.debounce
	if debounce <= 0 {
		return true
	}
	timer := time.NewTimer(debounce)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-r.triggerCh:
			continue
		case <-timer.C:
			return true
		}
	}
}

func (r *DNSReconciler) ReconcileOnce(ctx context.Context) error {
	r.runMu.Lock()
	defer r.runMu.Unlock()

	recordsByZone, err := r.projector.ProjectZoneRecords(ctx)
	if err != nil {
		return err
	}
	changed := 0
	unchanged := 0
	for _, zone := range r.zones {
		backend, ok := r.resolver.Resolve(zone.BackendRef)
		if !ok {
			r.logger.Warn("DNS backend not found", zap.String("zone", zone.Name), zap.String("backend_ref", zone.BackendRef))
			continue
		}
		desired := cloneDNSRecords(recordsByZone[zone.Name])
		sortDNSRecords(desired)
		actual, err := backend.ListRecords(ctx, zone)
		if err != nil {
			r.logger.Warn("DNS backend list records failed", zap.String("zone", zone.Name), zap.Error(err))
			continue
		}
		actual = cloneDNSRecords(actual)
		sortDNSRecords(actual)

		diff := diffDNSRecords(actual, desired)
		if diff.empty() {
			unchanged++
			r.logger.Info("DNS zone unchanged", zap.String("zone", zone.Name), zap.Int("records", len(desired)))
			continue
		}

		r.emitDriftDetected(ctx, zone, diff)
		r.emitRecordChanges(ctx, zone, diff)
		r.emitEndpointDeltas(ctx, zone, diff)
		if err := backend.SyncZone(ctx, zone, desired); err != nil {
			r.logger.Warn("DNS backend sync zone failed", zap.String("zone", zone.Name), zap.Error(err))
			continue
		}
		changed++
		r.emitZoneSynced(ctx, zone, len(actual), len(desired), diff)
		r.logger.Info("DNS zone synced", zap.String("zone", zone.Name), zap.Int("desired_records", len(desired)), zap.Int("actual_records", len(actual)), zap.Int("added_records", len(diff.added)), zap.Int("deleted_records", len(diff.deleted)), zap.Int("updated_records", len(diff.updated)))
	}
	r.logger.Info("DNS reconcile completed", zap.Int("changed_zones", changed), zap.Int("unchanged_zones", unchanged))
	return nil
}

func cloneDNSRecords(records []domain.DNSRecord) []domain.DNSRecord {
	if len(records) == 0 {
		return []domain.DNSRecord{}
	}
	return append([]domain.DNSRecord(nil), records...)
}

type dnsRecordUpdate struct {
	old domain.DNSRecord
	new domain.DNSRecord
}

type dnsRecordDiff struct {
	added                   []domain.DNSRecord
	deleted                 []domain.DNSRecord
	updated                 []dnsRecordUpdate
	registeredCoordinates   []string
	deregisteredCoordinates []string
}

func (d dnsRecordDiff) empty() bool {
	return len(d.added) == 0 && len(d.deleted) == 0 && len(d.updated) == 0 && len(d.registeredCoordinates) == 0 && len(d.deregisteredCoordinates) == 0
}

func diffDNSRecords(actual, desired []domain.DNSRecord) dnsRecordDiff {
	actualByKey := dnsRecordMap(actual)
	desiredByKey := dnsRecordMap(desired)
	diff := dnsRecordDiff{}
	for key, desiredRecord := range desiredByKey {
		actualRecord, ok := actualByKey[key]
		if !ok {
			diff.added = append(diff.added, desiredRecord)
			continue
		}
		if actualRecord.Value != desiredRecord.Value || actualRecord.TTL != desiredRecord.TTL {
			diff.updated = append(diff.updated, dnsRecordUpdate{old: actualRecord, new: desiredRecord})
		}
	}
	for key, actualRecord := range actualByKey {
		if _, ok := desiredByKey[key]; !ok {
			diff.deleted = append(diff.deleted, actualRecord)
		}
	}
	actualCoordinates := dnsCoordinateSet(actual)
	desiredCoordinates := dnsCoordinateSet(desired)
	for coordinate := range desiredCoordinates {
		if _, ok := actualCoordinates[coordinate]; !ok {
			diff.registeredCoordinates = append(diff.registeredCoordinates, coordinate)
		}
	}
	for coordinate := range actualCoordinates {
		if _, ok := desiredCoordinates[coordinate]; !ok {
			diff.deregisteredCoordinates = append(diff.deregisteredCoordinates, coordinate)
		}
	}
	sortDNSDiff(&diff)
	return diff
}

func dnsRecordMap(records []domain.DNSRecord) map[string]domain.DNSRecord {
	out := make(map[string]domain.DNSRecord, len(records))
	for _, record := range records {
		out[dnsRecordKey(record)] = record
	}
	return out
}

func dnsRecordKey(record domain.DNSRecord) string {
	return record.FQDN + "\x00" + string(record.Type)
}

func dnsCoordinateSet(records []domain.DNSRecord) map[string]struct{} {
	out := make(map[string]struct{})
	for _, record := range records {
		if record.SourceCoordinate != "" {
			out[record.SourceCoordinate] = struct{}{}
		}
	}
	return out
}

func sortDNSDiff(diff *dnsRecordDiff) {
	sortDNSRecords(diff.added)
	sortDNSRecords(diff.deleted)
	sort.Slice(diff.updated, func(i, j int) bool { return dnsRecordKey(diff.updated[i].new) < dnsRecordKey(diff.updated[j].new) })
	sort.Strings(diff.registeredCoordinates)
	sort.Strings(diff.deregisteredCoordinates)
}

func (r *DNSReconciler) emitDriftDetected(ctx context.Context, zone domain.DNSZone, diff dnsRecordDiff) {
	r.publish(ctx, dnsEventDriftDetected, zone.Name, map[string]any{
		"zone":          zone.Name,
		"backend_ref":   zone.BackendRef,
		"added_count":   len(diff.added),
		"deleted_count": len(diff.deleted),
		"updated_count": len(diff.updated),
	})
}

func (r *DNSReconciler) emitRecordChanges(ctx context.Context, zone domain.DNSZone, diff dnsRecordDiff) {
	for _, record := range diff.added {
		r.publish(ctx, dnsEventRecordChanged, dnsRecordKey(record), dnsRecordChangePayload(zone, "add", domain.DNSRecord{}, record))
	}
	for _, record := range diff.deleted {
		r.publish(ctx, dnsEventRecordChanged, dnsRecordKey(record), dnsRecordChangePayload(zone, "delete", record, domain.DNSRecord{}))
	}
	for _, update := range diff.updated {
		r.publish(ctx, dnsEventRecordChanged, dnsRecordKey(update.new), dnsRecordChangePayload(zone, "update", update.old, update.new))
	}
}

func (r *DNSReconciler) emitEndpointDeltas(ctx context.Context, zone domain.DNSZone, diff dnsRecordDiff) {
	for _, coordinate := range diff.registeredCoordinates {
		r.publish(ctx, dnsEventEndpointRegistered, coordinate, map[string]any{"zone": zone.Name, "backend_ref": zone.BackendRef, "source_coordinate": coordinate})
	}
	for _, coordinate := range diff.deregisteredCoordinates {
		r.publish(ctx, dnsEventEndpointDeregistered, coordinate, map[string]any{"zone": zone.Name, "backend_ref": zone.BackendRef, "source_coordinate": coordinate})
	}
}

func (r *DNSReconciler) emitZoneSynced(ctx context.Context, zone domain.DNSZone, actualCount, desiredCount int, diff dnsRecordDiff) {
	r.publish(ctx, dnsEventZoneSynced, zone.Name, map[string]any{
		"zone":          zone.Name,
		"backend_ref":   zone.BackendRef,
		"actual_count":  actualCount,
		"desired_count": desiredCount,
		"added_count":   len(diff.added),
		"deleted_count": len(diff.deleted),
		"updated_count": len(diff.updated),
	})
}

func dnsRecordChangePayload(zone domain.DNSZone, operation string, oldRecord, newRecord domain.DNSRecord) map[string]any {
	record := newRecord
	if operation == "delete" {
		record = oldRecord
	}
	return map[string]any{
		"zone":              zone.Name,
		"backend_ref":       zone.BackendRef,
		"operation":         operation,
		"fqdn":              record.FQDN,
		"record_type":       string(record.Type),
		"old_value":         oldRecord.Value,
		"new_value":         newRecord.Value,
		"ttl":               record.TTL,
		"old_ttl":           oldRecord.TTL,
		"new_ttl":           newRecord.TTL,
		"source_coordinate": record.SourceCoordinate,
	}
}

func (r *DNSReconciler) publish(ctx context.Context, eventType events.EventType, entityID string, data map[string]any) {
	if r.publisher == nil {
		return
	}
	r.publisher.Publish(ctx, events.Event{Type: eventType, EntityID: entityID, Data: data})
}
