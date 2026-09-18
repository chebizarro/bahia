package reconcile

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/telemetry"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.uber.org/zap"
)

// rollbackTraceID is the release-spine trace the regressing release belongs to.
const rollbackTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"

func rollbackSpanAttribute(span sdktrace.ReadOnlySpan, key string) (string, bool) {
	for _, attr := range span.Attributes() {
		if string(attr.Key) == key {
			return attr.Value.AsString(), true
		}
	}
	return "", false
}

// runTracedRollback drives the real CD-5 attributable-regression rollback with
// an in-memory runtime and returns the spans it produced. No relay, registry,
// or container runtime is involved.
func runTracedRollback(t *testing.T, metadata map[string]any) []sdktrace.ReadOnlySpan {
	t.Helper()

	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(t.Context())
	})

	serviceID, envID := uuid.New(), uuid.New()
	currentArtifactID, priorArtifactID, intentID := uuid.New(), uuid.New(), uuid.New()
	now := time.Now().UTC()

	svcRepo := &mockServiceRepo{services: map[uuid.UUID]*domain.Service{
		serviceID: {ID: serviceID, Name: "web"},
	}}
	envRepo := &mockEnvironmentRepo{envs: map[uuid.UUID]*domain.Environment{
		envID: {ID: envID, Name: "prod", RuntimeConfig: map[string]any{"auto_remediation": map[string]any{
			"enabled": true, "cooldown_seconds": float64(0), "on_health_failure": "rollback",
		}}},
	}}
	artifactRepo := &mockArtifactRepo{artifacts: map[uuid.UUID]*domain.Artifact{
		currentArtifactID: {ID: currentArtifactID, ServiceID: serviceID, ImageRepo: "registry.example/web", ImageDigest: rollbackRegressingDigest},
		priorArtifactID:   {ID: priorArtifactID, ServiceID: serviceID, ImageRepo: "registry.example/web", ImageDigest: rollbackPriorDigest},
	}}
	stateRepo := &mockStateRepo{states: map[string]*domain.EnvironmentServiceState{
		stateMapKey(serviceID, envID): {
			ServiceID: serviceID, EnvironmentID: envID,
			DesiredArtifactID: &currentArtifactID, DesiredIntentID: &intentID,
		},
	}}
	intentRepo := &mockIntentRepo{intents: map[uuid.UUID]*domain.DeploymentIntent{
		intentID: {
			ID: intentID, ServiceID: serviceID, EnvironmentID: envID,
			ArtifactID:          currentArtifactID,
			SourceKind:          domain.SourceKindAutoPromote,
			PriorArtifactDigest: rollbackPriorDigest,
			Status:              domain.IntentStatusDeployed,
			Metadata:            metadata,
			CreatedAt:           now,
		},
	}}
	rt := &mockRuntime{observeDigest: "sha256:broken"}
	r := NewRemediator(svcRepo, envRepo, artifactRepo, stateRepo, rt, &mockPublisher{}, zap.NewNop(),
		WithRemediationIntentHistory(intentRepo))

	if err := r.OnHealthFailure(t.Context(), serviceID, envID); err != nil {
		t.Fatalf("OnHealthFailure() error = %v", err)
	}
	rt.mu.Lock()
	deployed := len(rt.deployed)
	rt.mu.Unlock()
	if deployed != 1 {
		t.Fatalf("deployments = %d, want the rollback deployment", deployed)
	}
	return recorder.Ended()
}

const (
	rollbackPriorDigest      = "sha256:prior"
	rollbackRegressingDigest = "sha256:regressing"
)

func findSpan(spans []sdktrace.ReadOnlySpan, name string) sdktrace.ReadOnlySpan {
	for _, span := range spans {
		if span.Name() == name {
			return span
		}
	}
	return nil
}

// TestRollbackContinuesReleaseTraceAndRecordsDigests is the rollback half of
// the release-spine stitch: the rollback rejoins the trace of the release it is
// undoing (carried on the promotion intent) and records both the regressing
// digest and the digest it restores.
func TestRollbackContinuesReleaseTraceAndRecordsDigests(t *testing.T) {
	spans := runTracedRollback(t, map[string]any{
		"promotion_source": "hiveci_release",
		"image_digest":     rollbackRegressingDigest,
		"traceparent":      "00-" + rollbackTraceID + "-00f067aa0ba902b7-01",
		"tracestate":       "loom=worker-01",
	})

	span := findSpan(spans, "bahia.release.rollback")
	if span == nil {
		t.Fatalf("ended spans = %#v, want a bahia.release.rollback span", spans)
	}
	if got := span.SpanContext().TraceID().String(); got != rollbackTraceID {
		t.Fatalf("rollback trace id = %s, want %s (rollback did not rejoin the release trace)", got, rollbackTraceID)
	}
	if parent := span.Parent(); !parent.IsRemote() || parent.SpanID().String() != "00f067aa0ba902b7" {
		t.Fatalf("rollback parent = %#v, want the promoted release's remote span context", parent)
	}
	if got, ok := rollbackSpanAttribute(span, telemetry.OCIManifestDigestAttribute); !ok || got != rollbackRegressingDigest {
		t.Fatalf("%s = %q (present=%v), want the regressing digest %s",
			telemetry.OCIManifestDigestAttribute, got, ok, rollbackRegressingDigest)
	}
	if got, ok := rollbackSpanAttribute(span, telemetry.OCIManifestDigestRestoredAttribute); !ok || got != rollbackPriorDigest {
		t.Fatalf("%s = %q (present=%v), want the prior digest %s",
			telemetry.OCIManifestDigestRestoredAttribute, got, ok, rollbackPriorDigest)
	}
	if got, ok := rollbackSpanAttribute(span, "outcome"); !ok || got != "success" {
		t.Fatalf("rollback outcome = %q (present=%v), want success", got, ok)
	}
}

// TestRollbackWithoutTraceContextStillRecordsPriorDigest keeps the change
// additive for intents promoted before this instrumentation existed.
func TestRollbackWithoutTraceContextStillRecordsPriorDigest(t *testing.T) {
	spans := runTracedRollback(t, map[string]any{"image_digest": rollbackRegressingDigest})

	span := findSpan(spans, "bahia.release.rollback")
	if span == nil {
		t.Fatalf("ended spans = %#v, want a bahia.release.rollback span", spans)
	}
	if span.Parent().IsValid() {
		t.Fatalf("rollback parent = %#v, want a root span when no context was carried", span.Parent())
	}
	if got, ok := rollbackSpanAttribute(span, telemetry.OCIManifestDigestRestoredAttribute); !ok || got != rollbackPriorDigest {
		t.Fatalf("%s = %q (present=%v), want the prior digest %s",
			telemetry.OCIManifestDigestRestoredAttribute, got, ok, rollbackPriorDigest)
	}
}
