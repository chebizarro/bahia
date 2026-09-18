package service

import (
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestRuntimePromotionCreatesSpan(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(t.Context())
	})

	orgID, serviceID, environmentID := uuid.New(), uuid.New(), uuid.New()
	releaseRepo := newMemoryAgentReleaseRepo()
	services := memoryServiceRepo{values: map[uuid.UUID]domain.Service{
		serviceID: {ID: serviceID, OrgID: orgID, Name: "agent-scout"},
	}}
	svc := NewAgentRuntimePromotionService(
		NewAgentRuntimeReleaseService(releaseRepo, services),
		fakeRuntimeSubscriptionSource{subs: []RuntimePromotionSubscription{promotionTestSubscription(serviceID, environmentID)}},
		&recordingPromotionIntentSink{},
	)
	if _, err := svc.PromoteSubscribedSouls(t.Context(), RuntimePromotionRequest{
		Source: promotionTestSource(orgID), Release: promotionTestAcceptedRelease(),
	}); err != nil {
		t.Fatalf("PromoteSubscribedSouls: %v", err)
	}
	spans := recorder.Ended()
	if len(spans) != 1 || spans[0].Name() != "bahia.release.promotion" {
		t.Fatalf("ended spans = %#v, want one promotion span", spans)
	}
}
