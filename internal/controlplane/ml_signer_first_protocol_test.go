package controlplane

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
)

func TestMLProtocolNamespaces(t *testing.T) {
	legacyCommandResultKinds := []int{
		KindMLRecipeRunRequest,
		KindMLInferenceDeployRequest,
		KindMLInferenceDeploymentApproval,
		KindMLInferenceRollbackRequest,
		KindMLModelImportRequest,
		KindMLRecipeRunResult,
		KindMLInferenceDeployResult,
		KindMLInferenceApprovalResult,
		KindMLInferenceRollbackResult,
		KindMLModelImportResult,
	}
	for i, kind := range legacyCommandResultKinds {
		want := 38390 + i
		if kind != want {
			t.Fatalf("AI/ML legacy command/result kind[%d]=%d, want %d", i, kind, want)
		}
		if kind >= 5000 && kind <= 7000 {
			// This preserves the historical AI/ML namespace separation from the retired
			// legacy DVM allocation. Loom, Hive-CI, and SoulFactory are explicit fleet-local exceptions.
			t.Fatalf("AI/ML command/result kind %d unexpectedly entered the retired legacy DVM allocation", kind)
		}
	}

	legacyReadModelKinds := []int{
		kinds.MLModelRegistry,
		kinds.MLModelVersionRegistry,
		kinds.MLDatasetRegistry,
		kinds.MLRecipeRegistry,
		kinds.MLRecipeRunState,
		kinds.MLInferenceEndpointRegistry,
		kinds.MLInferenceEndpointState,
		kinds.MLEvaluationExperimentState,
		kinds.MLArtifactProvenanceGraph,
		kinds.MLRuntimeCapabilityProfile,
	}
	for i, kind := range legacyReadModelKinds {
		want := 31980 + i
		if kind != want {
			t.Fatalf("AI/ML legacy read-model kind[%d]=%d, want %d", i, kind, want)
		}
	}

}

func TestMLSignerFirstRequestSubscriptionsAreScopedCanonicalContextVM(t *testing.T) {
	signer, err := NewPrivateKeySigner(nostr.Generate().Hex())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	operatorPubkey := testNostrPubKeyHexFromPrivateKey(t, nostr.Generate().Hex())
	adoptionPubkey := testNostrPubKeyHexFromPrivateKey(t, nostr.Generate().Hex())
	runtimePubkey := testNostrPubKeyHexFromPrivateKey(t, nostr.Generate().Hex())
	reactor := NewReactor(Config{
		AuthorizedPubkeys:              []string{operatorPubkey},
		AdoptionAuthorizedPubkeys:      []string{adoptionPubkey},
		DirectRuntimeAuthorizedPubkeys: []string{runtimePubkey, operatorPubkey},
	}, nil, nil, signer, nil)

	since := nostr.Timestamp(123)
	filters := reactor.buildRequestSubscriptionFilters(since)
	if len(filters) != 1 {
		t.Fatalf("filters=%d, want one scoped ContextVM subscription", len(filters))
	}
	filter := filters[0]
	wantKinds := []nostr.Kind{KindContextVMMessage, KindContextVMGiftWrap, KindContextVMEphemeralWrap, KindArtifactRegister}
	if !sameNostrKindSet(filter.Kinds, wantKinds) {
		t.Fatalf("request subscription kinds=%v, want canonical ContextVM kinds plus signed artifact registration %v", filter.Kinds, wantKinds)
	}
	for _, legacyKind := range []nostr.Kind{KindMLRecipeRunRequest, KindMLInferenceDeployRequest, KindMLModelImportRequest, KindMLInferenceDeployResult, KindMLModelImportResult} {
		if containsNostrKind(filter.Kinds, legacyKind) {
			t.Fatalf("runtime request subscription revived legacy ML kind %d in %v", legacyKind, filter.Kinds)
		}
	}
	wantAuthors := []string{operatorPubkey, adoptionPubkey, runtimePubkey}
	if !samePubKeyHexSet(filter.Authors, wantAuthors) {
		t.Fatalf("request subscription authors=%v, want scoped operators %v", filter.Authors, wantAuthors)
	}
	if filter.Since != since {
		t.Fatalf("request subscription since=%v, want %d", filter.Since, since)
	}
}

func TestMLInjectedLegacyRequestsRequireCorrelationAndPublishCanonicalFailure(t *testing.T) {
	ctx := context.Background()
	requestKey := nostr.Generate().Hex()
	requestPubkey := testNostrPubKeyHexFromPrivateKey(t, requestKey)
	capture := &captureNostrPublisher{published: 1}
	signer, err := NewPrivateKeySigner(nostr.Generate().Hex())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	reactor := NewReactor(Config{AuthorizedPubkeys: []string{requestPubkey}}, nil, nil, signer, nil, WithControlPlanePublisher(capture))
	request := signedLLMRequest(t, requestKey, KindMLModelImportRequest, `{}`, nostr.Tags{{"model", "model:qwen"}})

	reactor.handleMLModelImportRequest(ctx, request)

	if len(capture.events) != 1 {
		t.Fatalf("published events=%d, want validation failure", len(capture.events))
	}
	result := capture.events[0]
	if result.Kind != KindContextVMMessage {
		t.Fatalf("ML validation failure kind=%d, want ContextVM %d", result.Kind, KindContextVMMessage)
	}
	assertNoLegacyStatusResultEvents(t, capture.events)
	assertReactorTag(t, result.Tags, "e", request.ID.Hex())
	assertReactorTag(t, result.Tags, "p", requestPubkey)
	assertReactorTag(t, result.Tags, "status", "failed")
	assertReactorTag(t, result.Tags, "result", "validation_error")
	assertReactorTag(t, result.Tags, "model", "model:qwen")
	var response ContextVMJSONRPCResponse
	if err := json.Unmarshal([]byte(result.Content), &response); err != nil {
		t.Fatalf("decode ContextVM response: %v", err)
	}
	if response.Error == nil || !strings.Contains(response.Error.Message, "d tag is required") {
		t.Fatalf("expected d-tag validation error, got %#v", response)
	}
}

func TestMLBrowserRouteAvoidsHTTPPollingForCompletion(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test file")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(filename), "../.."))
	page, err := os.ReadFile(filepath.Join(repoRoot, "web/src/routes/ml/+page.svelte"))
	if err != nil {
		t.Fatalf("read ML route: %v", err)
	}
	src := string(page)
	for _, forbidden := range []string{"fetch('/api/v1/ml", "fetch(\"/api/v1/ml", "setTimeout(", "setInterval(", "sleep(", "publishCommand(", "requestEncryptedResult("} {
		if strings.Contains(src, forbidden) {
			t.Fatalf("ML route contains forbidden HTTP polling/completion primitive %q", forbidden)
		}
	}
	for _, required := range []struct {
		name    string
		pattern string
	}{
		{"model import command", `publishMLCommand\(\s*['"]ml/model-import['"]\s*,`},
		{"inference deploy command", `publishMLCommand\(\s*['"]ml/inference-deploy['"]\s*,`},
		// Phase 4 W3e: the bridge publishes a signed 30900 intent, never a ContextVM command.
		{"signed-intent bridge", `async\s+function\s+publishMLCommand\s*\([^)]*\)\s*\{[^}]*publishIntent\s*\(`},
	} {
		matched, err := regexp.MatchString(required.pattern, src)
		if err != nil {
			t.Fatalf("invalid route assertion %s: %v", required.name, err)
		}
		if !matched {
			t.Fatalf("ML route missing executable signer-first path %q", required.name)
		}
	}
}

func containsNostrKind(values []nostr.Kind, want nostr.Kind) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func sameNostrKindSet(got, want []nostr.Kind) bool {
	if len(got) != len(want) {
		return false
	}
	for _, value := range want {
		if !containsNostrKind(got, value) {
			return false
		}
	}
	return true
}

func samePubKeyHexSet(got []nostr.PubKey, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := map[string]struct{}{}
	for _, value := range got {
		seen[value.Hex()] = struct{}{}
	}
	for _, value := range want {
		if _, ok := seen[value]; !ok {
			return false
		}
	}
	return true
}
