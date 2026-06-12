package nostr

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
)

// ---------------------------------------------------------------------------
// Backward-compatible payload enrichment tests (Item 8 — bahia-zu2p.8.7)
//
// These tests verify that:
//   - Legacy (pre-enrichment) payloads decode without error
//   - New enriched payloads include enrichment fields
//   - Decoders tolerate missing optional enrichment fields
//   - Decoders tolerate unknown future fields (forward compat)
//   - Event kinds and d-tag semantics are unchanged
//   - Replaceable event identity (pubkey+kind+d) is preserved
// ---------------------------------------------------------------------------

// --- Golden payload fixtures ------------------------------------------------

// goldenLegacy returns a map of kind → (dtag, legacy JSON content, tags) for
// pre-enrichment payloads that existing consumers already process.
func goldenLegacyPayloads() map[int]goldenPayload {
	now := "2026-05-01T00:00:00Z"
	return map[int]goldenPayload{
		KindServiceState: {
			dtag:    "svc-1:env-1",
			tags:    gonostr.Tags{{"d", "svc-1:env-1"}, {"service", "svc-1"}, {"environment", "env-1"}, {"drift_status", "unknown"}},
			content: fmt.Sprintf(`{"deleted":false,"service_id":"svc-1","environment_id":"env-1","drift_status":"unknown","updated_at":"%s"}`, now),
		},
		KindDeploymentIntentRegistry: {
			dtag:    "intent-legacy-1",
			tags:    gonostr.Tags{{"d", "intent-legacy-1"}, {"intent", "intent-legacy-1"}, {"status", "deploying"}},
			content: fmt.Sprintf(`{"deleted":false,"id":"intent-legacy-1","service_id":"svc-1","environment_id":"env-1","artifact_id":"art-1","status":"deploying","updated_at":"%s"}`, now),
		},
		KindDeploymentRunRegistry: {
			dtag:    "run-legacy-1",
			tags:    gonostr.Tags{{"d", "run-legacy-1"}, {"run", "run-legacy-1"}, {"status", "succeeded"}},
			content: fmt.Sprintf(`{"deleted":false,"id":"run-legacy-1","deployment_intent_id":"intent-1","status":"succeeded","updated_at":"%s"}`, now),
		},
		KindServiceRegistry: {
			dtag:    "svc-legacy-1",
			tags:    gonostr.Tags{{"d", "svc-legacy-1"}, {"deleted", "false"}},
			content: fmt.Sprintf(`{"deleted":false,"id":"svc-legacy-1","name":"api","created_at":"%s","updated_at":"%s"}`, now, now),
		},
		KindEnvironmentRegistry: {
			dtag:    "env-legacy-1",
			tags:    gonostr.Tags{{"d", "env-legacy-1"}, {"deleted", "false"}},
			content: fmt.Sprintf(`{"deleted":false,"id":"env-legacy-1","name":"prod","protected":true,"created_at":"%s","updated_at":"%s"}`, now, now),
		},
		KindBuildRegistry: {
			dtag:    "build-legacy-1",
			tags:    gonostr.Tags{{"d", "build-legacy-1"}, {"deleted", "false"}},
			content: fmt.Sprintf(`{"deleted":false,"id":"build-legacy-1","service_id":"svc-1","git_sha":"abc","status":"succeeded","created_at":"%s"}`, now),
		},
		KindArtifactRegistry: {
			dtag:    "art-legacy-1",
			tags:    gonostr.Tags{{"d", "art-legacy-1"}, {"deleted", "false"}},
			content: fmt.Sprintf(`{"deleted":false,"id":"art-legacy-1","build_id":"build-1","service_id":"svc-1","image_repo":"registry.test/api","image_tag":"v1","created_at":"%s"}`, now),
		},
		KindPolicyRegistry: {
			dtag:    "policy-legacy-1",
			tags:    gonostr.Tags{{"d", "policy-legacy-1"}, {"deleted", "false"}},
			content: fmt.Sprintf(`{"deleted":false,"id":"policy-legacy-1","name":"prod approvals","enabled":true,"created_at":"%s","updated_at":"%s"}`, now, now),
		},
	}
}

// goldenEnrichedPayloads returns payloads that include the new enrichment
// fields added by the desired-state metadata work.
func goldenEnrichedPayloads() map[int]goldenPayload {
	now := "2026-05-26T00:00:00Z"
	return map[int]goldenPayload{
		KindServiceState: {
			dtag:    "svc-1:env-1",
			tags:    gonostr.Tags{{"d", "svc-1:env-1"}, {"service", "svc-1"}, {"environment", "env-1"}, {"drift_status", "in_sync"}, {"desired_hash", "sha256:enriched"}},
			content: fmt.Sprintf(`{"deleted":false,"service_id":"svc-1","environment_id":"env-1","drift_status":"in_sync","desired_hash":"sha256:enriched","renderer":"compose","target":"api-prod","updated_at":"%s"}`, now),
			enrichedFields: map[string]string{
				"desired_hash": "sha256:enriched",
				"renderer":     "compose",
				"target":       "api-prod",
			},
		},
		KindDeploymentIntentRegistry: {
			dtag:    "intent-enriched-1",
			tags:    gonostr.Tags{{"d", "intent-enriched-1"}, {"intent", "intent-enriched-1"}, {"status", "deploying"}, {"desired_hash", "sha256:intent-enriched"}},
			content: fmt.Sprintf(`{"deleted":false,"id":"intent-enriched-1","service_id":"svc-1","environment_id":"env-1","artifact_id":"art-1","status":"deploying","desired_hash":"sha256:intent-enriched","renderer":"docker","target":"api-prod","updated_at":"%s"}`, now),
			enrichedFields: map[string]string{
				"desired_hash": "sha256:intent-enriched",
				"renderer":     "docker",
				"target":       "api-prod",
			},
		},
		KindDeploymentRunRegistry: {
			dtag: "run-enriched-1",
			tags: gonostr.Tags{{"d", "run-enriched-1"}, {"run", "run-enriched-1"}, {"status", "succeeded"}, {"renderer", "compose"}},
			content: fmt.Sprintf(`{"deleted":false,"id":"run-enriched-1","deployment_intent_id":"intent-1","status":"succeeded",`+
				`"renderer":"compose","desired_hash":"sha256:run-hash","revision_hash":"sha256:rev","target":"api-prod",`+
				`"apply_summary":"recreated 1 container","observation_id":"obs-1","updated_at":"%s"}`, now),
			enrichedFields: map[string]string{
				"renderer":       "compose",
				"desired_hash":   "sha256:run-hash",
				"revision_hash":  "sha256:rev",
				"target":         "api-prod",
				"apply_summary":  "recreated 1 container",
				"observation_id": "obs-1",
			},
		},
	}
}

type goldenPayload struct {
	dtag           string
	tags           gonostr.Tags
	content        string
	enrichedFields map[string]string // key → expected value for enrichment fields
}

// --- Test: Legacy payloads decode without error -----------------------------

func TestBackwardCompat_LegacyPayloadsDecodeWithoutError(t *testing.T) {
	catalog := NewKindCatalog()
	for kind, gp := range goldenLegacyPayloads() {
		t.Run(fmt.Sprintf("kind_%d", kind), func(t *testing.T) {
			ev := &gonostr.Event{
				Kind:      canonicalKind(kind),
				CreatedAt: gonostr.Now(),
				Tags:      gp.tags,
				Content:   gp.content,
			}
			decoded := catalogDecode(t, catalog, ev)
			if decoded.Kind != kind {
				t.Fatalf("decoded kind = %d, want %d", decoded.Kind, kind)
			}
			if decoded.DTag != gp.dtag {
				t.Fatalf("decoded dtag = %q, want %q", decoded.DTag, gp.dtag)
			}
		})
	}
}

// --- Test: Enriched payloads decode with new fields -------------------------

func TestBackwardCompat_EnrichedPayloadsIncludeNewFields(t *testing.T) {
	catalog := NewKindCatalog()
	for kind, gp := range goldenEnrichedPayloads() {
		t.Run(fmt.Sprintf("kind_%d", kind), func(t *testing.T) {
			ev := &gonostr.Event{
				Kind:      canonicalKind(kind),
				CreatedAt: gonostr.Now(),
				Tags:      gp.tags,
				Content:   gp.content,
			}
			decoded := catalogDecode(t, catalog, ev)

			// Verify enriched fields via re-marshal of the decoded family struct.
			verifyEnrichedFields(t, kind, decoded, gp.enrichedFields)
		})
	}
}

func verifyEnrichedFields(t *testing.T, kind int, decoded *DecodedProjectionEvent, expected map[string]string) {
	t.Helper()
	switch kind {
	case KindServiceState:
		if decoded.State == nil {
			t.Fatal("decoded state is nil")
		}
		for field, want := range expected {
			switch field {
			case "desired_hash":
				if decoded.State.DesiredHash != want {
					t.Fatalf("State.DesiredHash = %q, want %q", decoded.State.DesiredHash, want)
				}
			case "renderer":
				if decoded.State.Renderer != want {
					t.Fatalf("State.Renderer = %q, want %q", decoded.State.Renderer, want)
				}
			case "target":
				if decoded.State.Target != want {
					t.Fatalf("State.Target = %q, want %q", decoded.State.Target, want)
				}
			}
		}
	case KindDeploymentIntentRegistry:
		if decoded.Intent == nil {
			t.Fatal("decoded intent is nil")
		}
		for field, want := range expected {
			switch field {
			case "desired_hash":
				if decoded.Intent.DesiredHash != want {
					t.Fatalf("Intent.DesiredHash = %q, want %q", decoded.Intent.DesiredHash, want)
				}
			case "renderer":
				if decoded.Intent.Renderer != want {
					t.Fatalf("Intent.Renderer = %q, want %q", decoded.Intent.Renderer, want)
				}
			case "target":
				if decoded.Intent.Target != want {
					t.Fatalf("Intent.Target = %q, want %q", decoded.Intent.Target, want)
				}
			}
		}
	case KindDeploymentRunRegistry:
		if decoded.Run == nil {
			t.Fatal("decoded run is nil")
		}
		for field, want := range expected {
			switch field {
			case "renderer":
				if decoded.Run.Renderer != want {
					t.Fatalf("Run.Renderer = %q, want %q", decoded.Run.Renderer, want)
				}
			case "desired_hash":
				if decoded.Run.DesiredHash != want {
					t.Fatalf("Run.DesiredHash = %q, want %q", decoded.Run.DesiredHash, want)
				}
			case "revision_hash":
				if decoded.Run.RevisionHash != want {
					t.Fatalf("Run.RevisionHash = %q, want %q", decoded.Run.RevisionHash, want)
				}
			case "target":
				if decoded.Run.Target != want {
					t.Fatalf("Run.Target = %q, want %q", decoded.Run.Target, want)
				}
			case "apply_summary":
				if decoded.Run.ApplySummary != want {
					t.Fatalf("Run.ApplySummary = %q, want %q", decoded.Run.ApplySummary, want)
				}
			case "observation_id":
				if decoded.Run.ObservationID != want {
					t.Fatalf("Run.ObservationID = %q, want %q", decoded.Run.ObservationID, want)
				}
			}
		}
	default:
		t.Fatalf("verifyEnrichedFields: unhandled kind %d", kind)
	}
}

// --- Test: Decoders tolerate missing optional enrichment fields --------------

func TestBackwardCompat_DecodersTolerateMissingOptionalFields(t *testing.T) {
	catalog := NewKindCatalog()

	// State without desired_hash, renderer, target
	t.Run("state_missing_enrichment", func(t *testing.T) {
		ev := &gonostr.Event{
			Kind:      canonicalKind(KindServiceState),
			CreatedAt: gonostr.Now(),
			Tags:      gonostr.Tags{{"d", "svc:env"}},
			Content:   `{"deleted":false,"service_id":"svc","environment_id":"env","drift_status":"unknown","updated_at":"2026-01-01T00:00:00Z"}`,
		}
		decoded := catalogDecode(t, catalog, ev)
		if decoded.State.DesiredHash != "" {
			t.Fatalf("expected empty desired_hash, got %q", decoded.State.DesiredHash)
		}
		if decoded.State.Renderer != "" {
			t.Fatalf("expected empty renderer, got %q", decoded.State.Renderer)
		}
		if decoded.State.Target != "" {
			t.Fatalf("expected empty target, got %q", decoded.State.Target)
		}
	})

	// Intent without desired_hash, renderer, target
	t.Run("intent_missing_enrichment", func(t *testing.T) {
		ev := &gonostr.Event{
			Kind:      canonicalKind(KindDeploymentIntentRegistry),
			CreatedAt: gonostr.Now(),
			Tags:      gonostr.Tags{{"d", "intent-bare"}},
			Content:   `{"deleted":false,"id":"intent-bare","service_id":"svc","environment_id":"env","artifact_id":"art","status":"approved","updated_at":"2026-01-01T00:00:00Z"}`,
		}
		decoded := catalogDecode(t, catalog, ev)
		if decoded.Intent.DesiredHash != "" {
			t.Fatalf("expected empty desired_hash, got %q", decoded.Intent.DesiredHash)
		}
		if decoded.Intent.Renderer != "" {
			t.Fatalf("expected empty renderer, got %q", decoded.Intent.Renderer)
		}
		if decoded.Intent.Target != "" {
			t.Fatalf("expected empty target, got %q", decoded.Intent.Target)
		}
	})

	// Run without renderer, desired_hash, revision_hash, target, apply_summary, observation_id
	t.Run("run_missing_enrichment", func(t *testing.T) {
		ev := &gonostr.Event{
			Kind:      canonicalKind(KindDeploymentRunRegistry),
			CreatedAt: gonostr.Now(),
			Tags:      gonostr.Tags{{"d", "run-bare"}},
			Content:   `{"deleted":false,"id":"run-bare","deployment_intent_id":"intent-1","status":"succeeded","updated_at":"2026-01-01T00:00:00Z"}`,
		}
		decoded := catalogDecode(t, catalog, ev)
		if decoded.Run.Renderer != "" {
			t.Fatalf("expected empty renderer, got %q", decoded.Run.Renderer)
		}
		if decoded.Run.DesiredHash != "" {
			t.Fatalf("expected empty desired_hash, got %q", decoded.Run.DesiredHash)
		}
		if decoded.Run.RevisionHash != "" {
			t.Fatalf("expected empty revision_hash, got %q", decoded.Run.RevisionHash)
		}
		if decoded.Run.Target != "" {
			t.Fatalf("expected empty target, got %q", decoded.Run.Target)
		}
		if decoded.Run.ApplySummary != "" {
			t.Fatalf("expected empty apply_summary, got %q", decoded.Run.ApplySummary)
		}
		if decoded.Run.ObservationID != "" {
			t.Fatalf("expected empty observation_id, got %q", decoded.Run.ObservationID)
		}
	})
}

// --- Test: Decoders tolerate unknown future fields (forward compat) ----------

func TestBackwardCompat_DecodersIgnoreUnknownFutureFields(t *testing.T) {
	catalog := NewKindCatalog()

	cases := []struct {
		name    string
		kind    int
		dtag    string
		content string
	}{
		{
			name: "state_with_future_field",
			kind: KindServiceState,
			dtag: "svc:env",
			content: `{"deleted":false,"service_id":"svc","environment_id":"env","drift_status":"in_sync",` +
				`"desired_hash":"sha256:abc","renderer":"compose","target":"api-prod",` +
				`"future_field_v2":"some-value","another_future":42,` +
				`"updated_at":"2026-05-26T00:00:00Z"}`,
		},
		{
			name: "intent_with_future_field",
			kind: KindDeploymentIntentRegistry,
			dtag: "intent-future",
			content: `{"deleted":false,"id":"intent-future","service_id":"svc","environment_id":"env","artifact_id":"art",` +
				`"status":"deploying","desired_hash":"sha256:xyz","renderer":"docker","target":"api",` +
				`"rollback_policy":"auto","canary_weight":10,` +
				`"updated_at":"2026-05-26T00:00:00Z"}`,
		},
		{
			name: "run_with_future_field",
			kind: KindDeploymentRunRegistry,
			dtag: "run-future",
			content: `{"deleted":false,"id":"run-future","deployment_intent_id":"intent-1","status":"succeeded",` +
				`"renderer":"compose","desired_hash":"sha256:h1","revision_hash":"sha256:r1",` +
				`"target":"api","apply_summary":"ok","observation_id":"obs-1",` +
				`"future_metrics":{"cpu":0.5,"mem":128},` +
				`"updated_at":"2026-05-26T00:00:00Z"}`,
		},
		{
			name: "service_with_future_field",
			kind: KindServiceRegistry,
			dtag: "svc-future",
			content: `{"deleted":false,"id":"svc-future","name":"api","runtime_type":"docker",` +
				`"future_capabilities":["gpu","tpu"],` +
				`"created_at":"2026-05-26T00:00:00Z","updated_at":"2026-05-26T00:00:00Z"}`,
		},
		{
			name: "environment_with_future_field",
			kind: KindEnvironmentRegistry,
			dtag: "env-future",
			content: `{"deleted":false,"id":"env-future","name":"prod","protected":true,` +
				`"future_scaling_config":{"min":1,"max":10},` +
				`"created_at":"2026-05-26T00:00:00Z","updated_at":"2026-05-26T00:00:00Z"}`,
		},
		{
			name: "build_with_future_field",
			kind: KindBuildRegistry,
			dtag: "build-future",
			content: `{"deleted":false,"id":"build-future","service_id":"svc-1","git_sha":"abc","status":"succeeded",` +
				`"provenance_attestation":"in-toto-v1",` +
				`"created_at":"2026-05-26T00:00:00Z"}`,
		},
		{
			name: "artifact_with_future_field",
			kind: KindArtifactRegistry,
			dtag: "art-future",
			content: `{"deleted":false,"id":"art-future","build_id":"build-1","service_id":"svc-1",` +
				`"image_repo":"registry.test/api","image_tag":"v2",` +
				`"vulnerability_count":0,` +
				`"created_at":"2026-05-26T00:00:00Z"}`,
		},
		{
			name: "policy_with_future_field",
			kind: KindPolicyRegistry,
			dtag: "policy-future",
			content: `{"deleted":false,"id":"policy-future","name":"approvals","enabled":true,` +
				`"auto_remediation":true,` +
				`"created_at":"2026-05-26T00:00:00Z","updated_at":"2026-05-26T00:00:00Z"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := &gonostr.Event{
				Kind:      canonicalKind(tc.kind),
				CreatedAt: gonostr.Now(),
				Tags:      gonostr.Tags{{"d", tc.dtag}},
				Content:   tc.content,
			}
			decoded := catalogDecode(t, catalog, ev)
			if decoded.Kind != tc.kind {
				t.Fatalf("kind = %d, want %d", decoded.Kind, tc.kind)
			}
			if decoded.DTag != tc.dtag {
				t.Fatalf("dtag = %q, want %q", decoded.DTag, tc.dtag)
			}
		})
	}
}

// --- Test: Event kind numbers are stable (regression guard) ------------------

func TestBackwardCompat_EventKindNumbersAreStable(t *testing.T) {
	// These kind numbers are published on relays and consumed by external
	// clients. Changing them is a breaking protocol change. This test locks
	// the values so accidental renames or refactors are caught.
	stable := map[string]int{
		"KindServiceState":             31961,
		"KindServiceRegistry":          31962,
		"KindEnvironmentRegistry":      31963,
		"KindArtifactRegistry":         31966,
		"KindDeploymentIntentRegistry": 31967,
		"KindDeploymentRunRegistry":    31968,
		"KindBuildRegistry":            31969,
		"KindPolicyRegistry":           31970,
		"KindWorkerState":              32000,
		"KindWorkerAssignmentState":    32001,
		"KindWorkerDrainStatus":        32002,
		"KindWorkerEligibilityPreview": 32003,
	}

	actual := map[string]int{
		"KindServiceState":             KindServiceState,
		"KindServiceRegistry":          KindServiceRegistry,
		"KindEnvironmentRegistry":      KindEnvironmentRegistry,
		"KindArtifactRegistry":         KindArtifactRegistry,
		"KindDeploymentIntentRegistry": KindDeploymentIntentRegistry,
		"KindDeploymentRunRegistry":    KindDeploymentRunRegistry,
		"KindBuildRegistry":            KindBuildRegistry,
		"KindPolicyRegistry":           KindPolicyRegistry,
		"KindWorkerState":              KindWorkerState,
		"KindWorkerAssignmentState":    KindWorkerAssignmentState,
		"KindWorkerDrainStatus":        KindWorkerDrainStatus,
		"KindWorkerEligibilityPreview": KindWorkerEligibilityPreview,
	}

	for name, want := range stable {
		got, ok := actual[name]
		if !ok {
			t.Fatalf("kind constant %s missing from actual map", name)
		}
		if got != want {
			t.Fatalf("%s = %d, want %d — changing kind numbers is a breaking protocol change", name, got, want)
		}
	}
}

// --- Test: Replaceable event d-tag semantics preserved ----------------------

func TestBackwardCompat_ReplaceableDTagSemantics(t *testing.T) {
	catalog := NewKindCatalog()
	now := time.Date(2026, 5, 26, 0, 0, 0, 0, time.UTC)

	// For replaceable events (kind 31xxx), identity = (pubkey, kind, d-tag).
	// Enrichment must not alter the d-tag derivation.
	cases := []struct {
		name     string
		kind     int
		tags     gonostr.Tags
		content  string
		wantDTag string
	}{
		{
			name:     "state_dtag_from_d_tag",
			kind:     KindServiceState,
			tags:     gonostr.Tags{{"d", "svc-1:env-1"}, {"service", "svc-1"}, {"environment", "env-1"}, {"desired_hash", "sha256:abc"}},
			content:  fmt.Sprintf(`{"deleted":false,"service_id":"svc-1","environment_id":"env-1","drift_status":"in_sync","desired_hash":"sha256:abc","renderer":"compose","target":"api-prod","updated_at":"%s"}`, now.Format(time.RFC3339)),
			wantDTag: "svc-1:env-1",
		},
		{
			name:     "state_dtag_falls_back_to_service_env_key",
			kind:     KindServiceState,
			tags:     gonostr.Tags{{"service", "svc-2"}, {"environment", "env-2"}},
			content:  fmt.Sprintf(`{"deleted":false,"service_id":"svc-2","environment_id":"env-2","drift_status":"unknown","updated_at":"%s"}`, now.Format(time.RFC3339)),
			wantDTag: "svc-2:env-2",
		},
		{
			name:     "intent_dtag_from_d_tag",
			kind:     KindDeploymentIntentRegistry,
			tags:     gonostr.Tags{{"d", "intent-abc"}, {"desired_hash", "sha256:xyz"}},
			content:  fmt.Sprintf(`{"deleted":false,"id":"intent-abc","service_id":"svc","environment_id":"env","artifact_id":"art","status":"deploying","desired_hash":"sha256:xyz","updated_at":"%s"}`, now.Format(time.RFC3339)),
			wantDTag: "intent-abc",
		},
		{
			name:     "intent_dtag_falls_back_to_id",
			kind:     KindDeploymentIntentRegistry,
			tags:     gonostr.Tags{},
			content:  fmt.Sprintf(`{"deleted":false,"id":"intent-fallback","service_id":"svc","environment_id":"env","artifact_id":"art","status":"deploying","updated_at":"%s"}`, now.Format(time.RFC3339)),
			wantDTag: "intent-fallback",
		},
		{
			name:     "run_dtag_from_d_tag",
			kind:     KindDeploymentRunRegistry,
			tags:     gonostr.Tags{{"d", "run-abc"}, {"renderer", "compose"}},
			content:  fmt.Sprintf(`{"deleted":false,"id":"run-abc","deployment_intent_id":"intent-1","status":"succeeded","renderer":"compose","updated_at":"%s"}`, now.Format(time.RFC3339)),
			wantDTag: "run-abc",
		},
		{
			name:     "run_dtag_falls_back_to_id",
			kind:     KindDeploymentRunRegistry,
			tags:     gonostr.Tags{},
			content:  fmt.Sprintf(`{"deleted":false,"id":"run-fallback","deployment_intent_id":"intent-1","status":"succeeded","updated_at":"%s"}`, now.Format(time.RFC3339)),
			wantDTag: "run-fallback",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := &gonostr.Event{
				Kind:      canonicalKind(tc.kind),
				CreatedAt: gonostr.Timestamp(now.Unix()),
				Tags:      tc.tags,
				Content:   tc.content,
			}
			decoded := catalogDecode(t, catalog, ev)
			if decoded.DTag != tc.wantDTag {
				t.Fatalf("d-tag = %q, want %q — enrichment must not alter replaceable identity", decoded.DTag, tc.wantDTag)
			}
		})
	}
}

// --- Test: Golden payload format round-trip ----------------------------------

// TestBackwardCompat_GoldenPayloadRoundTrip verifies that golden legacy and
// enriched payloads can be decoded and re-serialized without losing known
// fields. This catches struct tag regressions.
func TestBackwardCompat_GoldenPayloadRoundTrip(t *testing.T) {
	catalog := NewKindCatalog()

	t.Run("legacy_state_roundtrip", func(t *testing.T) {
		gp := goldenLegacyPayloads()[KindServiceState]
		ev := &gonostr.Event{Kind: canonicalKind(KindServiceState), CreatedAt: gonostr.Now(), Tags: gp.tags, Content: gp.content}
		decoded := catalogDecode(t, catalog, ev)
		reJSON, err := json.Marshal(decoded.State)
		if err != nil {
			t.Fatalf("re-marshal state: %v", err)
		}
		var roundtrip map[string]any
		if err := json.Unmarshal(reJSON, &roundtrip); err != nil {
			t.Fatalf("unmarshal roundtrip: %v", err)
		}
		// Core fields must survive the round-trip
		if roundtrip["service_id"] != "svc-1" {
			t.Fatalf("service_id lost in roundtrip: %v", roundtrip)
		}
		if roundtrip["environment_id"] != "env-1" {
			t.Fatalf("environment_id lost in roundtrip: %v", roundtrip)
		}
	})

	t.Run("enriched_state_roundtrip", func(t *testing.T) {
		gp := goldenEnrichedPayloads()[KindServiceState]
		ev := &gonostr.Event{Kind: canonicalKind(KindServiceState), CreatedAt: gonostr.Now(), Tags: gp.tags, Content: gp.content}
		decoded := catalogDecode(t, catalog, ev)
		reJSON, err := json.Marshal(decoded.State)
		if err != nil {
			t.Fatalf("re-marshal state: %v", err)
		}
		var roundtrip map[string]any
		if err := json.Unmarshal(reJSON, &roundtrip); err != nil {
			t.Fatalf("unmarshal roundtrip: %v", err)
		}
		if roundtrip["desired_hash"] != "sha256:enriched" {
			t.Fatalf("desired_hash lost in roundtrip: %v", roundtrip)
		}
		if roundtrip["renderer"] != "compose" {
			t.Fatalf("renderer lost in roundtrip: %v", roundtrip)
		}
		if roundtrip["target"] != "api-prod" {
			t.Fatalf("target lost in roundtrip: %v", roundtrip)
		}
	})

	t.Run("enriched_intent_roundtrip", func(t *testing.T) {
		gp := goldenEnrichedPayloads()[KindDeploymentIntentRegistry]
		ev := &gonostr.Event{Kind: canonicalKind(KindDeploymentIntentRegistry), CreatedAt: gonostr.Now(), Tags: gp.tags, Content: gp.content}
		decoded := catalogDecode(t, catalog, ev)
		reJSON, err := json.Marshal(decoded.Intent)
		if err != nil {
			t.Fatalf("re-marshal intent: %v", err)
		}
		var roundtrip map[string]any
		if err := json.Unmarshal(reJSON, &roundtrip); err != nil {
			t.Fatalf("unmarshal roundtrip: %v", err)
		}
		if roundtrip["desired_hash"] != "sha256:intent-enriched" {
			t.Fatalf("desired_hash lost: %v", roundtrip)
		}
		if roundtrip["renderer"] != "docker" {
			t.Fatalf("renderer lost: %v", roundtrip)
		}
	})

	t.Run("enriched_run_roundtrip", func(t *testing.T) {
		gp := goldenEnrichedPayloads()[KindDeploymentRunRegistry]
		ev := &gonostr.Event{Kind: canonicalKind(KindDeploymentRunRegistry), CreatedAt: gonostr.Now(), Tags: gp.tags, Content: gp.content}
		decoded := catalogDecode(t, catalog, ev)
		reJSON, err := json.Marshal(decoded.Run)
		if err != nil {
			t.Fatalf("re-marshal run: %v", err)
		}
		var roundtrip map[string]any
		if err := json.Unmarshal(reJSON, &roundtrip); err != nil {
			t.Fatalf("unmarshal roundtrip: %v", err)
		}
		for _, field := range []string{"renderer", "desired_hash", "revision_hash", "target", "apply_summary", "observation_id"} {
			if _, ok := roundtrip[field]; !ok {
				t.Fatalf("enriched field %q lost in roundtrip: %v", field, roundtrip)
			}
		}
	})
}

// --- Test: Enrichment is purely additive — legacy family assignment unchanged

func TestBackwardCompat_FamilyAssignmentUnchanged(t *testing.T) {
	catalog := NewKindCatalog()
	expectedFamilies := map[int]ProjectionFamily{
		KindServiceState:             FamilyState,
		KindServiceRegistry:          FamilyService,
		KindEnvironmentRegistry:      FamilyEnvironment,
		KindBuildRegistry:            FamilyBuild,
		KindArtifactRegistry:         FamilyArtifact,
		KindDeploymentIntentRegistry: FamilyIntent,
		KindDeploymentRunRegistry:    FamilyRun,
		KindPolicyRegistry:           FamilyPolicy,
	}

	for kind, gp := range goldenLegacyPayloads() {
		t.Run(fmt.Sprintf("legacy_kind_%d_family", kind), func(t *testing.T) {
			ev := &gonostr.Event{Kind: canonicalKind(kind), CreatedAt: gonostr.Now(), Tags: gp.tags, Content: gp.content}
			decoded := catalogDecode(t, catalog, ev)
			wantFamily := expectedFamilies[kind]
			if decoded.Family != wantFamily {
				t.Fatalf("family = %q, want %q", decoded.Family, wantFamily)
			}
		})
	}

	for kind, gp := range goldenEnrichedPayloads() {
		t.Run(fmt.Sprintf("enriched_kind_%d_family", kind), func(t *testing.T) {
			ev := &gonostr.Event{Kind: canonicalKind(kind), CreatedAt: gonostr.Now(), Tags: gp.tags, Content: gp.content}
			decoded := catalogDecode(t, catalog, ev)
			wantFamily := expectedFamilies[kind]
			if decoded.Family != wantFamily {
				t.Fatalf("family = %q, want %q — enrichment must not change family assignment", decoded.Family, wantFamily)
			}
		})
	}
}

// --- Test: Mixed legacy+enriched stream processed in sequence ----------------

func TestBackwardCompat_MixedLegacyAndEnrichedStream(t *testing.T) {
	catalog := NewKindCatalog()

	// Simulate a relay stream where old and new events arrive interleaved.
	// This is the real-world scenario: some projectors haven't upgraded yet
	// and publish legacy payloads, while upgraded ones publish enriched.
	legacyState := goldenLegacyPayloads()[KindServiceState]
	enrichedState := goldenEnrichedPayloads()[KindServiceState]

	events := []*gonostr.Event{
		{Kind: canonicalKind(KindServiceState), CreatedAt: gonostr.Timestamp(time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC).Unix()), Tags: legacyState.tags, Content: legacyState.content},
		{Kind: canonicalKind(KindServiceState), CreatedAt: gonostr.Timestamp(time.Date(2026, 5, 26, 0, 0, 0, 0, time.UTC).Unix()), Tags: enrichedState.tags, Content: enrichedState.content},
	}

	for i, ev := range events {
		decoded := catalogDecode(t, catalog, ev)
		if decoded.State == nil {
			t.Fatalf("event %d: decoded state is nil", i)
		}
		if decoded.DTag != legacyState.dtag {
			t.Fatalf("event %d: d-tag changed between legacy and enriched", i)
		}
		if decoded.Family != FamilyState {
			t.Fatalf("event %d: family = %q, want state", i, decoded.Family)
		}
	}

	// The second (enriched) event should carry the new fields
	enrichedDecoded := catalogDecode(t, catalog, events[1])
	if enrichedDecoded.State.DesiredHash != "sha256:enriched" {
		t.Fatalf("enriched event in mixed stream missing desired_hash")
	}

	// The first (legacy) event should have empty enrichment fields
	legacyDecoded := catalogDecode(t, catalog, events[0])
	if legacyDecoded.State.DesiredHash != "" {
		t.Fatalf("legacy event in mixed stream should have empty desired_hash, got %q", legacyDecoded.State.DesiredHash)
	}
}

// --- Test: Catalog version tracks enrichment ---------------------------------

func TestBackwardCompat_CatalogVersionIncludesItem8(t *testing.T) {
	catalog := NewKindCatalog()
	// The catalog version should reference item8, indicating enrichment support.
	if catalog.Version != "2026-05-26.item8" {
		t.Fatalf("catalog version = %q, expected to contain item8 marker for enrichment support", catalog.Version)
	}
}
