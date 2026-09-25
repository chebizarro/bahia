package telemetry

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/repository"
	"math"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.uber.org/zap"
)

type vmTelemetryRows[T any] struct {
	repository.VirtualizationResources[T]
	rows []T
}

func (r vmTelemetryRows[T]) List(_ context.Context, _ uuid.UUID, limit, offset int) ([]T, error) {
	if offset >= len(r.rows) {
		return []T{}, nil
	}
	end := min(offset+limit, len(r.rows))
	return r.rows[offset:end], nil
}

type vmTelemetryRepo struct {
	repository.VirtualizationRepository
	host domain.VirtualizationHost
	vms  []domain.PersistentVMDeployment
}

func (r *vmTelemetryRepo) Hosts() repository.VirtualizationHostRepository {
	return vmTelemetryRows[domain.VirtualizationHost]{rows: []domain.VirtualizationHost{r.host}}
}
func (r *vmTelemetryRepo) Deployments() repository.PersistentVMDeploymentRepository {
	return vmTelemetryRows[domain.PersistentVMDeployment]{rows: r.vms}
}
func (r *vmTelemetryRepo) ExecutionPlanes() repository.ExecutionPlaneDeploymentRepository {
	return vmTelemetryRows[domain.ExecutionPlaneDeployment]{}
}
func (r *vmTelemetryRepo) Checkpoints() repository.VMCheckpointRepository {
	return vmTelemetryRows[domain.VMCheckpoint]{}
}
func (r *vmTelemetryRepo) ListReservations(context.Context, uuid.UUID, uuid.UUID) ([]domain.VMCapacityReservation, error) {
	return []domain.VMCapacityReservation{{LifecycleClass: domain.VMLifecyclePersistent, Capacity: domain.VMCapacity{VCPU: 2, MemoryBytes: 20, DiskBytes: 30}}}, nil
}

type vmTelemetryOrgs struct{ id uuid.UUID }

func (o vmTelemetryOrgs) List(context.Context) ([]domain.Organization, error) {
	return []domain.Organization{{ID: o.id}}, nil
}
func TestVirtualizationCollectorAggregatesAndRetracts(t *testing.T) {
	m := NewMetrics()
	old := activeMetrics.Swap(m)
	defer activeMetrics.Store(old)
	org := uuid.New()
	repo := &vmTelemetryRepo{host: domain.VirtualizationHost{VirtualizationResourceMeta: domain.VirtualizationResourceMeta{ID: uuid.New(), OrgID: org}, Provider: domain.VMProviderLibvirt, Quota: domain.VMCapacity{VCPU: 10, MemoryBytes: 100, DiskBytes: 300}}, vms: []domain.PersistentVMDeployment{{LifecycleClass: domain.VMLifecyclePersistent, Provider: domain.VMProviderLibvirt, DesiredPower: domain.VMDesiredRunning, Allocation: domain.VMCapacity{VCPU: 2}, Observation: &domain.VMObservation{VMObservationStamp: domain.VMObservationStamp{ObservedAt: time.Now()}, Availability: domain.VMObservationUnavailable, Ownership: domain.VMOrphan, GuestHealth: domain.VMGuestUnknown, Drift: domain.VMDriftUnknown}}}}
	collector := NewVirtualizationCollector(repo, vmTelemetryOrgs{org})
	defer collector.Close()
	if err := collector.Collect(context.Background()); err != nil {
		t.Fatal(err)
	}
	totals := func(name string) float64 {
		var n float64
		for key, v := range m.virtualization {
			if key.Name == name {
				n += v.Value
			}
		}
		return n
	}
	if totals("desired_observed") != 1 || totals("orphans") != 1 {
		t.Fatal("missing durable aggregates")
	}
	found := false
	for key, v := range m.virtualization {
		if key.Name == "quota_headroom" && key.Labels.Dimension == "vcpu_available" {
			found = true
			if v.Value != 8 {
				t.Fatal(v.Value)
			}
		}
	}
	if !found {
		t.Fatal("missing quota headroom")
	}
	repo.vms = nil
	if err := collector.Collect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if totals("desired_observed") != 0 || totals("orphans") != 0 {
		t.Fatal("removed resources not retracted")
	}
	collector.Close()
	if err := collector.Collect(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestVirtualizationMetricsBoundedAndMirrored(t *testing.T) {
	ctx := context.Background()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	oldProvider := otel.GetMeterProvider()
	otel.SetMeterProvider(mp)
	defer otel.SetMeterProvider(oldProvider)
	defer func() {
		if err := mp.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	}()
	oldInstruments := virtualizationInstruments
	var err error
	virtualizationInstruments, err = newVirtualizationInstruments(mp)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { virtualizationInstruments = oldInstruments }()
	p := Setup(Config{}, zap.NewNop())
	defer func() {
		if err := p.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	}()
	labels := VirtualizationLabels{Provider: "password=sentinel", LifecycleClass: "/private/sentinel", Result: "success", Operation: "checkpoint"}
	for name := range virtualizationInstruments {
		if err := RecordVirtualization(ctx, name, labels, 2); err != nil {
			t.Fatal(err)
		}
	}
	if err := RecordVirtualization(ctx, "secret-metric", labels, 1); err == nil {
		t.Fatal("unknown instrument")
	}
	if err := RecordVirtualization(ctx, "capacity", labels, math.NaN()); err == nil {
		t.Fatal("NaN accepted")
	}
	w := httptest.NewRecorder()
	p.MetricsHandler()(w, httptest.NewRequest("GET", "/metrics", nil))
	text := w.Body.String()
	if strings.Contains(text, "sentinel") {
		t.Fatal("unbounded label")
	}
	for _, name := range []string{"desired_observed", "capacity", "quota_rejections_total", "guest_agent_probes_total", "console_bytes_total", "checkpoint_bytes", "orphans", "plane_probes_total", "plane_capabilities", "operation_duration_seconds_sum", "operation_duration_seconds_count"} {
		if !strings.Contains(text, "bahia_virtualization_"+name+"{") {
			t.Fatalf("missing %s", name)
		}
	}
	var data metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &data); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, scope := range data.ScopeMetrics {
		for _, m := range scope.Metrics {
			if strings.HasPrefix(m.Name, "bahia.virtualization.") {
				count++
			}
		}
	}
	if count != len(virtualizationInstruments) {
		t.Fatalf("OTel %d vs endpoint %d", count, len(virtualizationInstruments))
	}
}
func TestVirtualizationMetricsConcurrentAndSanitizedSpan(t *testing.T) {
	m := NewMetrics()
	old := activeMetrics.Swap(m)
	defer activeMetrics.Store(old)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				if err := RecordVirtualization(context.Background(), "operations_total", VirtualizationLabels{Operation: "start", Result: "success"}, 1); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	var total float64
	for _, v := range m.virtualization {
		total += v.Value
	}
	if total != 240 {
		t.Fatal(total)
	}
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	defer func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	ctx, span := provider.Tracer("test").Start(context.Background(), "vm")
	EndVirtualizationOperation(ctx, span, VirtualizationLabels{Operation: "delete", Reason: "/private/sentinel", Result: "failure"}, &domain.VMProviderError{Code: domain.VMErrorIntegrity, Cause: errors.New("password=sentinel")})
	for _, ended := range recorder.Ended() {
		if strings.Contains(ended.Status().Description, "sentinel") {
			t.Fatal("raw span error")
		}
		for _, event := range ended.Events() {
			for _, a := range event.Attributes {
				if strings.Contains(a.Value.AsString(), "sentinel") {
					t.Fatal("raw exception")
				}
			}
		}
	}
	if strings.Contains(VirtualizationEvidence(VirtualizationLabels{Reason: "token=sentinel"}), "sentinel") {
		t.Fatal("raw evidence")
	}
}
