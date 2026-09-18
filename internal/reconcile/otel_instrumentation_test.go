package reconcile

import (
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.uber.org/zap"
)

func TestReconcileCycleCreatesSpan(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(t.Context())
	})

	reconciler := NewReconciler(
		nil, nil, nil, nil, &mockObservationRepo{}, &mockStateRepo{states: map[string]*domain.EnvironmentServiceState{}},
		nil, &mockPublisher{}, time.Minute, zap.NewNop(),
	)
	reconciler.reconcileAll(t.Context())
	spans := recorder.Ended()
	if len(spans) != 1 || spans[0].Name() != "bahia.reconcile" {
		t.Fatalf("ended spans = %#v, want one reconcile span", spans)
	}
}
