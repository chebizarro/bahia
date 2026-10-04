package nostr

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository/repositorytest"
	"go.uber.org/zap"
)

func TestF74aCanonicalFamiliesPublishAndTombstone(t *testing.T) {
	ctx := context.Background()
	sink := &captureProjectionPublisher{}
	projector := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, repositorytest.NewInMemoryNostrEventRepository(), zap.NewNop())
	enc := &mockConfidentialEncryptor{}
	pub := NewF74aCanonicalPublisher(projector, enc)
	release := &domain.LLMRelease{ID: uuid.New(), RouteID: uuid.New(), Version: "v1", CreatedAt: time.Now().UTC()}
	sig := &domain.ArtifactSignature{ID: uuid.New(), ArtifactID: uuid.New(), SignerIdentity: "builder@example.test", CreatedAt: time.Now().UTC()}
	sbom := &domain.ArtifactSBOM{ID: uuid.New(), ArtifactID: sig.ArtifactID, Format: domain.SBOMFormatSPDX, CreatedAt: time.Now().UTC()}
	pkg := &domain.SBOMPackage{ID: uuid.New(), SBOMID: sbom.ID, Name: "module"}
	obs := &domain.RuntimeObservation{ID: uuid.New(), ServiceID: uuid.New(), EnvironmentID: uuid.New(), ObservedAt: time.Now().UTC(), Metadata: map[string]any{"password": "do-not-project"}}
	cases := []struct {
		kind               int
		topic, d           string
		publish, tombstone func() error
	}{
		{KindLLMReleaseRegistry, kinds.CPStateTopicLLMRelease, "llm:release:" + release.ID.String(), func() error { return pub.PublishLLMRelease(ctx, release) }, func() error { return pub.PublishLLMReleaseState(ctx, release, true) }},
		{KindArtifactSignatureRegistry, kinds.CPStateTopicArtifactSignature, "artifact:signature:" + sig.ID.String(), func() error { return pub.PublishArtifactSignature(ctx, sig) }, func() error { return pub.PublishArtifactSignatureState(ctx, sig, true) }},
		{KindArtifactSBOMRegistry, kinds.CPStateTopicArtifactSBOM, "artifact:sbom:" + sbom.ID.String(), func() error { return pub.PublishArtifactSBOM(ctx, sbom) }, func() error { return pub.PublishArtifactSBOMState(ctx, sbom, true) }},
		{KindSBOMPackageRegistry, kinds.CPStateTopicSBOMPackage, "artifact:sbom-package:" + pkg.ID.String(), func() error { return pub.PublishSBOMPackage(ctx, pkg) }, func() error { return pub.PublishSBOMPackageState(ctx, pkg, true) }},
		{KindRuntimeObservationState, kinds.CPStateTopicRuntimeObservation, "runtime:observation:" + obs.ServiceID.String() + ":" + obs.EnvironmentID.String(), func() error { return pub.PublishRuntimeObservation(ctx, obs) }, func() error { return pub.PublishRuntimeObservationState(ctx, obs, true) }},
	}
	for _, tc := range cases {
		t.Run(strconv.Itoa(tc.kind), func(t *testing.T) {
			before := len(sink.byKind(KindCASControlState))
			if err := tc.publish(); err != nil {
				t.Fatal(err)
			}
			if err := tc.tombstone(); err != nil {
				t.Fatal(err)
			}
			events := sink.byKind(KindCASControlState)
			if len(events) != before+2 {
				t.Fatalf("got %d new records, want 2", len(events)-before)
			}
			live, dead := events[before], events[before+1]
			for _, ev := range []struct {
				tags        gonostr.Tags
				wantDeleted string
			}{{live.Tags, "false"}, {dead.Tags, "true"}} {
				if !hasTag(ev.tags, "d", tc.d) || !hasTag(ev.tags, "t", tc.topic) || !hasTag(ev.tags, "legacy_kind", strconv.Itoa(tc.kind)) || !hasTag(ev.tags, "deleted", ev.wantDeleted) {
					t.Fatalf("incorrect canonical envelope: %v", ev.tags)
				}
			}
			if live.Kind != KindCASControlState || dead.Kind != KindCASControlState {
				t.Fatal("not 30900")
			}
			if len(live.Content) > f74aMaxContent || len(dead.Content) > f74aMaxContent {
				t.Fatal("oversized event content")
			}
			if tc.kind == KindRuntimeObservationState && strings.Contains(live.Content, "do-not-project") {
				t.Fatal("runtime secret metadata leaked")
			}
		})
	}
	if enc.lastLegacyKind != KindLLMReleaseRegistry || enc.lastTopic != kinds.CPStateTopicLLMRelease {
		t.Fatal("LLM release was not confidential")
	}
}

func TestF74aPackageIndexAndSizeBounds(t *testing.T) {
	ctx := context.Background()
	sink := &captureProjectionPublisher{}
	projector := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, repositorytest.NewInMemoryNostrEventRepository(), zap.NewNop())
	pub := NewF74aCanonicalPublisher(projector, &mockConfidentialEncryptor{})
	sbomID := uuid.New()
	for i := 0; i < 64; i++ {
		pkg := &domain.SBOMPackage{ID: uuid.New(), SBOMID: sbomID, Name: fmt.Sprintf("pkg-%d", i)}
		if err := pub.PublishSBOMPackage(ctx, pkg); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(sink.byKind(KindCASControlState)); got != 64 {
		t.Fatalf("package index has %d events, want 64", got)
	}
	huge := &domain.SBOMPackage{ID: uuid.New(), SBOMID: sbomID, Name: strings.Repeat("x", f74aMaxContent)}
	if err := pub.PublishSBOMPackage(ctx, huge); err == nil {
		t.Fatal("oversized package accepted")
	}
	release := &domain.LLMRelease{ID: uuid.New(), RouteID: uuid.New(), Metadata: map[string]any{"blob": strings.Repeat("x", f74aMaxConfidentialPlaintext)}}
	if err := pub.PublishLLMRelease(ctx, release); err == nil {
		t.Fatal("oversized confidential release accepted")
	}
	if got := len(sink.byKind(KindCASControlState)); got != 64 {
		t.Fatalf("size rejection published %d events", got-64)
	}
}

type f74aDirtyMarker struct {
	value  string
	writes int
}

func (m *f74aDirtyMarker) PutControlRecord(_, _ string, value []byte) error {
	m.value = string(value)
	m.writes++
	return nil
}
func TestF74aPublishFailureSchedulesStartupRepair(t *testing.T) {
	sink := &captureProjectionPublisher{}
	projector := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, repositorytest.NewInMemoryNostrEventRepository(), zap.NewNop())
	marker := &f74aDirtyMarker{}
	pub := NewF74aCanonicalPublisher(projector, nil, marker)
	pkg := &domain.SBOMPackage{ID: uuid.New(), SBOMID: uuid.New(), Name: strings.Repeat("x", f74aMaxContent)}
	if err := pub.PublishSBOMPackage(context.Background(), pkg); err == nil {
		t.Fatal("expected frame bound rejection")
	}
	if marker.value != "dirty" || marker.writes != 1 {
		t.Fatalf("repair marker = %q, writes=%d", marker.value, marker.writes)
	}
	if len(sink.byKind(KindCASControlState)) != 0 {
		t.Fatal("oversized state entered outbox")
	}
}
