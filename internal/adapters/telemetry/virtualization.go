package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// Measurement names, modes and every label are closed vocabularies. Resource IDs,
// provider stderr, digests, endpoints and paths never become metric dimensions.
type virtualizationInstrument struct {
	mode      string
	unit      string
	gauge     metric.Float64Gauge
	counter   metric.Float64Counter
	histogram metric.Float64Histogram
}

var virtualizationInstruments = newVirtualizationInstruments()

func newVirtualizationInstruments() map[string]virtualizationInstrument {
	definitions := map[string][2]string{
		"observation_timestamp_seconds": {"gauge", "s"},
		"quota_headroom":                {"gauge", "1"},
		"usage":                         {"gauge", "1"},
		"plane_probe_duration_seconds":  {"histogram", "s"},
		"inventory":                     {"gauge", "{resource}"}, "inventory_freshness_seconds": {"gauge", "s"}, "inventory_failures_total": {"counter", "{failure}"},
		"desired_observed": {"gauge", "{resource}"}, "capacity": {"gauge", "1"}, "quota_rejections_total": {"counter", "{rejection}"},
		"operations_total": {"counter", "{operation}"}, "operation_duration_seconds": {"histogram", "s"},
		"guest_agent_probes_total": {"counter", "{probe}"}, "guest_agent_duration_seconds": {"histogram", "s"}, "guest_agent_freshness_seconds": {"gauge", "s"},
		"console_streams": {"gauge", "{stream}"}, "console_bytes_total": {"counter", "By"}, "console_failures_total": {"counter", "{failure}"},
		"checkpoints_total": {"counter", "{operation}"}, "checkpoint_duration_seconds": {"histogram", "s"}, "checkpoint_bytes": {"gauge", "By"}, "checkpoint_age_seconds": {"gauge", "s"}, "checkpoint_verification_failures_total": {"counter", "{failure}"},
		"orphans": {"gauge", "{resource}"}, "plane_probes_total": {"counter", "{probe}"}, "plane_probe_freshness_seconds": {"gauge", "s"}, "plane_drift": {"gauge", "{plane}"}, "plane_capabilities": {"gauge", "{capability}"}, "plane_capability_retractions_total": {"counter", "{capability}"},
	}
	meter := otel.Meter("github.com/openagentsinc/bahia/virtualization")
	out := make(map[string]virtualizationInstrument, len(definitions))
	for name, d := range definitions {
		v := virtualizationInstrument{mode: d[0], unit: d[1]}
		switch v.mode {
		case "gauge":
			v.gauge, _ = meter.Float64Gauge("bahia.virtualization."+name, metric.WithUnit(v.unit))
		case "counter":
			v.counter, _ = meter.Float64Counter("bahia.virtualization."+name, metric.WithUnit(v.unit))
		case "histogram":
			v.histogram, _ = meter.Float64Histogram("bahia.virtualization."+name, metric.WithUnit(v.unit))
		}
		out[name] = v
	}
	return out
}

type VirtualizationLabels struct {
	Provider       string
	LifecycleClass string
	Desired        string
	State          string
	Drift          string
	GuestHealth    string
	Operation      string
	Result         string
	Reason         string
	Dimension      string
}

func vmLabel(v string, allowed ...string) string {
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	return "unknown"
}
func (l VirtualizationLabels) bounded() VirtualizationLabels {
	l.Provider = vmLabel(l.Provider, "libvirt", "firecracker", "loom")
	l.LifecycleClass = vmLabel(l.LifecycleClass, "persistent_vm", "loom_firecracker_job_microvm", "loom_qemu_job_domain")
	l.Desired = vmLabel(l.Desired, "running", "stopped", "enabled", "disabled")
	l.State = vmLabel(l.State, "absent", "stopped", "running", "paused", "failed", "owned", "orphan", "foreign", "available", "unavailable")
	l.Drift = vmLabel(l.Drift, "in_sync", "drifted")
	l.GuestHealth = vmLabel(l.GuestHealth, "not_configured", "starting", "healthy", "unhealthy")
	l.Operation = vmLabel(l.Operation, "define", "adopt", "start", "graceful_stop", "reboot", "checkpoint", "export", "clone", "restore", "delete", "inspect", "probe", "reconcile", "approve", "cancel")
	l.Result = vmLabel(l.Result, "success", "failure", "unconfirmed", "rejected", "stale", "protocol_mismatch", "truncated", "disconnected")
	l.Reason = vmLabel(l.Reason, "none", "invalid", "conflict", "unavailable", "unsupported", "foreign_resource", "approval_required", "quota_exceeded", "completion_unconfirmed", "integrity_failure")
	l.Dimension = vmLabel(l.Dimension, "vcpu_allocated", "vcpu_reserved", "vcpu_available", "memory_allocated", "memory_reserved", "memory_available", "storage_allocated", "storage_reserved", "storage_available", "disk", "nvram", "swtpm", "rootfs", "kernel", "package", "config", "image", "cpu_ratio", "memory_used", "disk_used", "disk_read", "disk_written", "cpu", "memory", "storage")
	return l
}
func (l VirtualizationLabels) attributes() []attribute.KeyValue {
	return []attribute.KeyValue{attribute.String("provider", l.Provider), attribute.String("lifecycle_class", l.LifecycleClass), attribute.String("desired", l.Desired), attribute.String("state", l.State), attribute.String("drift", l.Drift), attribute.String("guest_health", l.GuestHealth), attribute.String("operation", l.Operation), attribute.String("result", l.Result), attribute.String("reason", l.Reason), attribute.String("dimension", l.Dimension)}
}

type virtualizationMetricKey struct {
	Name   string
	Labels VirtualizationLabels
}
type virtualizationMetricValue struct {
	Value float64
	Count uint64
}

// RecordVirtualization records aggregates supplied by C/D, not per-ID gauges.
// Gauge inputs must be the full count for the bounded label set (including zero
// when removed). Capacity vCPU values are cores; memory/storage values are bytes.
func RecordVirtualization(ctx context.Context, name string, labels VirtualizationLabels, value float64) error {
	instrument, ok := virtualizationInstruments[name]
	if !ok || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return domain.ErrInvalidValue
	}
	labels = labels.bounded()
	attrs := metric.WithAttributes(labels.attributes()...)
	switch instrument.mode {
	case "gauge":
		instrument.gauge.Record(ctx, value, attrs)
	case "counter":
		instrument.counter.Add(ctx, value, attrs)
	case "histogram":
		instrument.histogram.Record(ctx, value, attrs)
	}
	if m := activeMetrics.Load(); m != nil {
		m.recordVirtualization(name, labels, value, instrument.mode)
	}
	return nil
}
func (m *Metrics) recordVirtualization(name string, labels VirtualizationLabels, value float64, mode string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.virtualization == nil {
		m.virtualization = map[virtualizationMetricKey]virtualizationMetricValue{}
	}
	key := virtualizationMetricKey{name, labels.bounded()}
	v := m.virtualization[key]
	if mode == "gauge" {
		v.Value = value
	} else {
		v.Value += value
	}
	v.Count++
	m.virtualization[key] = v
}

// Called with Metrics.mu held by the existing metrics endpoint.
func (m *Metrics) renderVirtualization(w io.Writer) error {
	names := make([]string, 0, len(virtualizationInstruments))
	for n := range virtualizationInstruments {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		kind := virtualizationInstruments[name].mode
		if kind == "histogram" {
			kind = "summary"
		}
		if _, err := fmt.Fprintf(w, "# TYPE bahia_virtualization_%s %s\n", name, kind); err != nil {
			return err
		}
		for key, v := range m.virtualization {
			if key.Name != name {
				continue
			}
			parts := []string{}
			for _, a := range key.Labels.attributes() {
				parts = append(parts, fmt.Sprintf("%s=%q", a.Key, a.Value.AsString()))
			}
			labels := strings.Join(parts, ",")
			var err error
			if kind == "summary" {
				_, err = fmt.Fprintf(w, "bahia_virtualization_%s_sum{%s} %g\nbahia_virtualization_%s_count{%s} %d\n", name, labels, v.Value, name, labels, v.Count)
			} else {
				_, err = fmt.Fprintf(w, "bahia_virtualization_%s{%s} %g\n", name, labels, v.Value)
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// SanitizedVirtualizationError discards the entire underlying cause. Never pass
// a raw provider error to EndOperation: it records both text and exception data.
func SanitizedVirtualizationError(err error) error {
	if err == nil {
		return nil
	}
	var provider *domain.VMProviderError
	code := "unavailable"
	if errors.As(err, &provider) {
		code = vmLabel(string(provider.Code), "invalid", "conflict", "unavailable", "unsupported", "foreign_resource", "approval_required", "quota_exceeded", "completion_unconfirmed", "integrity_failure")
	}
	return errors.New(domain.SanitizeEvidence(code))
}
func EndVirtualizationOperation(ctx context.Context, span trace.Span, labels VirtualizationLabels, err error) {
	labels = labels.bounded()
	EndOperation(ctx, span, "virtualization."+labels.Operation, labels.Result, SanitizedVirtualizationError(err), labels.attributes()...)
}

// VirtualizationCollector refreshes tenant-fenced aggregates at startup and on
// committed change signals. It is not a polling loop. Operation/probe/console
// counters remain service-side measurements through RecordVirtualization.
type VirtualizationCollector struct {
	Repository    repository.VirtualizationRepository
	Organizations interface {
		List(context.Context) ([]domain.Organization, error)
	}
	mu        sync.Mutex
	previous  map[virtualizationMetricKey]float64
	closed    bool
	lifecycle context.Context
	cancel    context.CancelFunc
}

func NewVirtualizationCollector(repo repository.VirtualizationRepository, orgs interface {
	List(context.Context) ([]domain.Organization, error)
}) *VirtualizationCollector {
	ctx, cancel := context.WithCancel(context.Background())
	return &VirtualizationCollector{Repository: repo, Organizations: orgs, lifecycle: ctx, cancel: cancel}
}
func (c *VirtualizationCollector) Close() {
	if c.cancel != nil {
		c.cancel()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
}
func (c *VirtualizationCollector) Subscribe(bus events.ErrorSubscriber) {
	for _, typ := range []events.EventType{events.EventVirtualizationResourceChanged, events.EventVMOperationTransitioned, events.EventExecutionPlaneProbeChanged, events.EventVirtualizationProjectionGap} {
		bus.SubscribeWithError(typ, func(ctx context.Context, _ events.Event) error { return c.Collect(ctx) })
	}
}
func virtualizationRows[T any](ctx context.Context, r repository.VirtualizationResources[T], org uuid.UUID) ([]T, error) {
	out := []T{}
	for offset := 0; ; offset += 100 {
		rows, err := r.List(ctx, org, 100, offset)
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
		if len(rows) < 100 {
			return out, nil
		}
	}
}
func (c *VirtualizationCollector) Collect(ctx context.Context) error {
	if c.lifecycle != nil {
		bound, cancel := context.WithCancel(ctx)
		defer cancel()
		stop := context.AfterFunc(c.lifecycle, cancel)
		defer stop()
		ctx = bound
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return context.Canceled
	}
	if c.Repository == nil || c.Organizations == nil {
		return errors.New("virtualization metrics unavailable")
	}
	orgs, err := c.Organizations.List(ctx)
	if err != nil {
		return errors.New("virtualization metrics inventory unavailable")
	}
	values := map[virtualizationMetricKey]float64{}
	add := func(name string, l VirtualizationLabels, v float64) {
		if v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
			return
		}
		values[virtualizationMetricKey{name, l.bounded()}] += v
	}
	capacity := func(name string, l VirtualizationLabels, v domain.VMCapacity, suffix string) {
		for dimension, value := range map[string]int64{"vcpu": v.VCPU, "memory": v.MemoryBytes, "storage": v.DiskBytes} {
			l.Dimension = dimension + suffix
			add(name, l, float64(max(0, value)))
		}
	}
	maximum := func(name string, l VirtualizationLabels, v float64) {
		key := virtualizationMetricKey{name, l.bounded()}
		values[key] = max(values[key], v)
	}
	oldest := func(l VirtualizationLabels, at time.Time) {
		key := virtualizationMetricKey{"observation_timestamp_seconds", l.bounded()}
		v := float64(at.Unix())
		if old, ok := values[key]; !ok || v < old {
			values[key] = v
		}
	}
	now := time.Now()
	for _, org := range orgs {
		hosts, err := virtualizationRows(ctx, c.Repository.Hosts(), org.ID)
		if err != nil {
			return errors.New("virtualization host metrics unavailable")
		}
		for _, h := range hosts {
			l := VirtualizationLabels{Provider: string(h.Provider)}
			reservations, err := c.Repository.ListReservations(ctx, org.ID, h.ID)
			if err != nil {
				return errors.New("virtualization reservation metrics unavailable")
			}
			reserved := domain.VMCapacity{}
			for _, r := range reservations {
				reserved.VCPU += r.Capacity.VCPU
				reserved.MemoryBytes += r.Capacity.MemoryBytes
				reserved.DiskBytes += r.Capacity.DiskBytes
				cl := l
				cl.LifecycleClass = string(r.LifecycleClass)
				capacity("capacity", cl, r.Capacity, "_reserved")
			}
			capacity("quota_headroom", l, domain.VMCapacity{VCPU: h.Quota.VCPU - reserved.VCPU, MemoryBytes: h.Quota.MemoryBytes - reserved.MemoryBytes, DiskBytes: h.Quota.DiskBytes - reserved.DiskBytes}, "_available")
			if o := h.Observation; o != nil {
				l.State = string(o.Availability)
				maximum("inventory_freshness_seconds", l, max(0, now.Sub(o.ObservedAt).Seconds()))
				oldest(l, o.ObservedAt)
				if o.Availability == domain.VMObservationAvailable {
					capacity("capacity", l, o.Free, "_available")
				}
			}
		}
		vms, err := virtualizationRows(ctx, c.Repository.Deployments(), org.ID)
		if err != nil {
			return errors.New("virtualization VM metrics unavailable")
		}
		for _, v := range vms {
			l := VirtualizationLabels{Provider: string(v.Provider), LifecycleClass: string(v.LifecycleClass), Desired: string(v.DesiredPower)}
			capacity("capacity", l, v.Allocation, "_allocated")
			if o := v.Observation; o != nil {
				l.State = string(o.Availability)
				if o.RuntimeState != nil {
					l.State = string(*o.RuntimeState)
				}
				l.Drift = string(o.Drift)
				l.GuestHealth = string(o.GuestHealth)
				owned := VirtualizationLabels{Provider: string(v.Provider), LifecycleClass: string(v.LifecycleClass), State: string(o.Ownership)}
				add("inventory", owned, 1)
				if o.Ownership == domain.VMOrphan {
					add("orphans", owned, 1)
				}
				maximum("guest_agent_freshness_seconds", l, max(0, now.Sub(o.ObservedAt).Seconds()))
				oldest(l, o.ObservedAt)
				if u := o.Usage; u != nil {
					for dimension, value := range map[string]float64{"cpu_ratio": u.CPUUtilizationRatio, "memory_used": float64(u.MemoryUsedBytes), "disk_used": float64(u.DiskUsedBytes), "disk_read": float64(u.DiskReadBytes), "disk_written": float64(u.DiskWrittenBytes)} {
						ul := l
						ul.Dimension = dimension
						add("usage", ul, value)
					}
				}
			}
			add("desired_observed", l, 1)
		}
		planes, err := virtualizationRows(ctx, c.Repository.ExecutionPlanes(), org.ID)
		if err != nil {
			return errors.New("virtualization plane metrics unavailable")
		}
		for _, p := range planes {
			for _, class := range p.Desired.LifecycleClasses {
				l := VirtualizationLabels{Provider: "loom", LifecycleClass: string(class), Desired: string(p.Desired.State)}
				if o := p.Observation; o != nil {
					l.State = string(o.Availability)
					l.Drift = string(o.Drift)
					add("plane_drift", l, 1)
					if o.Probe != nil {
						maximum("plane_probe_freshness_seconds", l, max(0, now.Sub(o.Probe.ObservedAt).Seconds()))
						oldest(l, o.Probe.ObservedAt)
					}
				}
				add("desired_observed", l, 1)
			}
		}
		checkpoints, err := virtualizationRows(ctx, c.Repository.Checkpoints(), org.ID)
		if err != nil {
			return errors.New("virtualization checkpoint metrics unavailable")
		}
		for _, checkpoint := range checkpoints {
			if checkpoint.State == domain.VMArtifactDeleted {
				continue
			}
			l := VirtualizationLabels{LifecycleClass: string(checkpoint.LifecycleClass), Operation: "checkpoint"}
			maximum("checkpoint_age_seconds", l, max(0, now.Sub(checkpoint.CreatedAt).Seconds()))
			for _, component := range checkpoint.Components {
				l.Dimension = string(component.Kind)
				add("checkpoint_bytes", l, float64(component.SizeBytes))
			}
		}
	}
	// Explicit zeroes retract disappeared gauges without retaining identity labels.
	for key := range c.previous {
		if _, ok := values[key]; !ok {
			if err := RecordVirtualization(ctx, key.Name, key.Labels, 0); err != nil {
				return err
			}
		}
	}
	for key, value := range values {
		if err := RecordVirtualization(ctx, key.Name, key.Labels, value); err != nil {
			return err
		}
	}
	c.previous = values
	return nil
}

// VirtualizationEvidence emits only the bounded categories, then applies the
// common sanitizer as defense in depth. It never accepts a provider evidence blob.
func VirtualizationEvidence(labels VirtualizationLabels) string {
	data, _ := json.Marshal(labels.bounded())
	return domain.SanitizeEvidence(string(data))
}
