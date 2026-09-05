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
)

func TestMLSignerFirstProtocolNamespacesAndCanonicalPublishing(t *testing.T) {
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
		KindMLModelRegistry,
		KindMLModelVersionRegistry,
		KindMLDatasetRegistry,
		KindMLRecipeRegistry,
		KindMLRecipeRunState,
		KindMLInferenceEndpointRegistry,
		KindMLInferenceEndpointState,
		KindMLEvaluationExperimentState,
		KindMLArtifactProvenanceGraph,
		KindMLRuntimeCapabilityProfile,
	}
	for i, kind := range legacyReadModelKinds {
		want := 31980 + i
		if kind != want {
			t.Fatalf("AI/ML legacy read-model kind[%d]=%d, want %d", i, kind, want)
		}
	}

	ctx := context.Background()
	capture := &captureNostrPublisher{published: 2}
	signer, err := NewPrivateKeySigner(nostr.Generate().Hex())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	publisher := NewMLCommandPublisher(capture, signer)

	receipt, err := publisher.PublishMLInferenceDeployRequest(ctx, MLCommandPayload{
		IdempotencyKey: "deploy:qwen-prod",
		Content: map[string]any{
			"endpoint":           "endpoint:qwen:prod",
			"model_version":      "model-version:qwen:v1",
			"runtime_preference": "vllm",
			"placement":          map[string]any{"accelerator": "gpu_nvidia_cuda"},
		},
		Tags: map[string]string{"runtime": "vllm", "accelerator": "gpu_nvidia_cuda"},
	})
	if err != nil {
		t.Fatalf("publish deploy: %v", err)
	}
	if receipt.RequestKind != KindContextVMMessage || receipt.ResultKind != KindContextVMMessage {
		t.Fatalf("production ML receipt must use ContextVM request/result kinds, got %#v", receipt)
	}
	if receipt.DTag != "deploy:qwen-prod" || receipt.PublishedRelays != 2 || receipt.Status != "submitted" {
		t.Fatalf("unexpected receipt status/correlation: %#v", receipt)
	}
	for name, kind := range receipt.ReadModelKinds {
		if kind != KindCASControlState {
			t.Fatalf("production ML read-model hint %s=%d, want canonical control state %d", name, kind, KindCASControlState)
		}
	}
	if len(capture.events) != 1 {
		t.Fatalf("published events=%d, want 1", len(capture.events))
	}
	ev := capture.events[0]
	if ev.Kind != KindContextVMMessage {
		t.Fatalf("production ML command event kind=%d, want ContextVM %d", ev.Kind, KindContextVMMessage)
	}
	assertNoLegacyStatusResultEvents(t, capture.events)
	params := assertContextVMCommand(t, ev, "ml/inference-deploy")
	assertReactorTag(t, ev.Tags, "d", "deploy:qwen-prod")
	assertReactorTag(t, ev.Tags, "endpoint", "endpoint:qwen:prod")
	assertReactorTag(t, ev.Tags, "environment", "prod")
	assertReactorTag(t, ev.Tags, "model_version", "model-version:qwen:v1")
	assertReactorTag(t, ev.Tags, "runtime", "vllm")
	assertReactorTag(t, ev.Tags, "accelerator", "gpu_nvidia_cuda")
	meta, ok := params["_meta"].(map[string]any)
	if !ok || meta["progressToken"] != "deploy:qwen-prod" {
		t.Fatalf("ContextVM params missing progressToken correlation: %#v", params)
	}
	var rpc ContextVMJSONRPCRequest
	if err := json.Unmarshal([]byte(ev.Content), &rpc); err != nil {
		t.Fatalf("decode ContextVM command: %v", err)
	}
	var rpcID string
	if err := json.Unmarshal(rpc.ID, &rpcID); err != nil || rpcID != "deploy:qwen-prod" {
		t.Fatalf("ContextVM request id=%s err=%v, want d-tag", string(rpc.ID), err)
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
	for _, forbidden := range []string{"fetch('/api/v1/ml", "fetch(\"/api/v1/ml", "setTimeout(", "setInterval(", "sleep("} {
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
		{"Nostr publish bridge", `async\s+function\s+publishMLCommand\s*\([^)]*\)\s*\{[^}]*publishCommand\s*\(`},
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
