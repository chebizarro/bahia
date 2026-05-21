package reconcile

import (
	"context"
	"reflect"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
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
}

func NewDNSReconciler(projector *DNSProjector, zones []domain.DNSZone, resolver DNSBackendResolver, interval time.Duration, logger *zap.Logger) *DNSReconciler {
	if interval <= 0 {
		interval = time.Minute
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &DNSReconciler{projector: projector, zones: append([]domain.DNSZone(nil), zones...), resolver: resolver, interval: interval, logger: logger}
}

func (r *DNSReconciler) Name() string { return "dns-reconciler" }

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
		}
	}
}

func (r *DNSReconciler) ReconcileOnce(ctx context.Context) error {
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
		if reflect.DeepEqual(actual, desired) {
			unchanged++
			r.logger.Info("DNS zone unchanged", zap.String("zone", zone.Name), zap.Int("records", len(desired)))
			continue
		}
		if err := backend.SyncZone(ctx, zone, desired); err != nil {
			r.logger.Warn("DNS backend sync zone failed", zap.String("zone", zone.Name), zap.Error(err))
			continue
		}
		changed++
		r.logger.Info("DNS zone synced", zap.String("zone", zone.Name), zap.Int("desired_records", len(desired)), zap.Int("actual_records", len(actual)))
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
