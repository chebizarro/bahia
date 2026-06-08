package controlplane

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/domain"
)

func TestServiceCommandPublisherPublishesCanonicalServiceCreateRequest(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 2}
	signer, err := NewPrivateKeySigner(nostr.GeneratePrivateKey())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	publisher := NewServiceCommandPublisher(capture, signer)

	receipt, err := publisher.PublishServiceCreateRequest(ctx, ServiceCreateCommand{Name: "payments-api", RepoURL: "https://example.invalid/payments.git", ArtifactRepo: "registry.example/payments", RuntimeType: "compose", IdempotencyKey: "service-create:payments-api"})
	if err != nil {
		t.Fatalf("publish service create: %v", err)
	}
	if receipt.RequestKind != KindContextVMMessage || receipt.StatusKind != KindNIP38Status || receipt.ResultKind != KindContextVMMessage || receipt.PublishedRelays != 2 {
		t.Fatalf("unexpected receipt: %#v", receipt)
	}
	if receipt.ServiceName != "payments-api" || receipt.IdempotencyKey != "service-create:payments-api" || receipt.RegistryKind != KindCASControlState {
		t.Fatalf("missing service receipt metadata: %#v", receipt)
	}
	if len(capture.events) != 1 {
		t.Fatalf("published events=%d, want 1", len(capture.events))
	}
	params := assertContextVMCommand(t, capture.events[0], ContextVMMethodServiceCreate)
	if params["name"] != "payments-api" || params["artifact_repo"] != "registry.example/payments" {
		t.Fatalf("unexpected service create params: %#v", params)
	}
	assertReactorTag(t, capture.events[0].Tags, "service", "payments-api")
	assertReactorTag(t, capture.events[0].Tags, "d", "service-create:payments-api")
}

func TestPolicyCommandPublisherPublishesCanonicalPolicyCreateUpdateDeleteEvaluateRequests(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 1}
	signer, err := NewPrivateKeySigner(nostr.GeneratePrivateKey())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	publisher := NewPolicyCommandPublisher(capture, signer)
	envID := uuid.New()
	policyID := uuid.New()
	artifactID := uuid.New()
	serviceID := uuid.New()

	enabled := true
	create, err := publisher.PublishPolicyCreateRequest(ctx, PolicyMutationCommand{Name: "require-sbom", EnvironmentID: &envID, Rules: []domain.PolicyRule{{Type: domain.RuleRequireSBOM}}, Enforcement: string(domain.PolicyEnforcementBlock), Enabled: &enabled, IdempotencyKey: "policy-create:require-sbom"})
	if err != nil {
		t.Fatalf("publish policy create: %v", err)
	}
	if create.RequestKind != KindPolicyCreate || create.ResultKind != KindContextVMMessage || create.ReadModelKinds["policy_registry"] != KindCASControlState || create.PublishedRelays != 1 {
		t.Fatalf("unexpected create receipt: %#v", create)
	}
	var createParams map[string]any
	if err := json.Unmarshal([]byte(capture.events[0].Content), &createParams); err != nil {
		t.Fatalf("decode create params: %v", err)
	}
	if createParams["name"] != "require-sbom" || createParams["enabled"] != true {
		t.Fatalf("unexpected create params: %#v", createParams)
	}
	assertReactorTag(t, capture.events[0].Tags, "environment", envID.String())
	assertReactorTag(t, capture.events[0].Tags, "d", "policy-create:require-sbom")

	_, err = publisher.PublishPolicyUpdateRequest(ctx, PolicyMutationCommand{ID: policyID, Enforcement: string(domain.PolicyEnforcementWarn), IdempotencyKey: "policy-update:require-sbom"})
	if err != nil {
		t.Fatalf("publish policy update: %v", err)
	}
	if capture.events[1].Kind != KindPolicyUpdate {
		t.Fatalf("update kind = %d, want %d", capture.events[1].Kind, KindPolicyUpdate)
	}
	var updateParams map[string]any
	if err := json.Unmarshal([]byte(capture.events[1].Content), &updateParams); err != nil {
		t.Fatalf("decode update params: %v", err)
	}
	if updateParams["id"] != policyID.String() || updateParams["enforcement"] != string(domain.PolicyEnforcementWarn) {
		t.Fatalf("unexpected update params: %#v", updateParams)
	}
	if _, ok := updateParams["enabled"]; ok {
		t.Fatalf("partial update must not emit omitted enabled flag: %#v", updateParams)
	}
	if _, ok := updateParams["name"]; ok {
		t.Fatalf("partial update must not require or emit omitted name: %#v", updateParams)
	}
	assertReactorTag(t, capture.events[1].Tags, "policy", policyID.String())

	_, err = publisher.PublishPolicyDeleteRequest(ctx, PolicyMutationCommand{ID: policyID, IdempotencyKey: "policy-delete:require-sbom"})
	if err != nil {
		t.Fatalf("publish policy delete: %v", err)
	}
	if capture.events[2].Kind != KindPolicyDelete {
		t.Fatalf("delete kind = %d, want %d", capture.events[2].Kind, KindPolicyDelete)
	}
	var deleteParams map[string]any
	if err := json.Unmarshal([]byte(capture.events[2].Content), &deleteParams); err != nil {
		t.Fatalf("decode delete params: %v", err)
	}
	if deleteParams["id"] != policyID.String() {
		t.Fatalf("unexpected delete params: %#v", deleteParams)
	}

	evaluate, err := publisher.PublishPolicyEvaluateRequest(ctx, PolicyMutationCommand{ArtifactID: artifactID, EnvironmentID: &envID, ServiceID: &serviceID, IdempotencyKey: "policy-evaluate:artifact"})
	if err != nil {
		t.Fatalf("publish policy evaluate: %v", err)
	}
	if evaluate.RequestKind != KindPolicyEvaluate || evaluate.ReadModelKinds != nil {
		t.Fatalf("unexpected evaluate receipt: %#v", evaluate)
	}
	var evalParams map[string]any
	if err := json.Unmarshal([]byte(capture.events[3].Content), &evalParams); err != nil {
		t.Fatalf("decode evaluate params: %v", err)
	}
	if evalParams["artifact_id"] != artifactID.String() || evalParams["environment_id"] != envID.String() || evalParams["service_id"] != serviceID.String() {
		t.Fatalf("unexpected evaluate params: %#v", evalParams)
	}
}

func TestToolApprovalCommandPublisherPublishesCanonicalApprovalResponse(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 2}
	signer, err := NewPrivateKeySigner(nostr.GeneratePrivateKey())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	publisher := NewToolApprovalCommandPublisher(capture, signer)
	intentID := uuid.New()

	receipt, err := publisher.PublishToolApprovalResponse(ctx, ToolApprovalCommand{IntentID: intentID, Action: "approve", Reason: "operator reviewed", IdempotencyKey: "tool-approval:1"})
	if err != nil {
		t.Fatalf("publish tool approval: %v", err)
	}
	if receipt.RequestKind != KindToolApprovalResponse || receipt.ResultKind != KindContextVMMessage || receipt.PublishedRelays != 2 || receipt.IntentID != intentID.String() || receipt.Action != "approve" {
		t.Fatalf("unexpected tool approval receipt: %#v", receipt)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(capture.events[0].Content), &payload); err != nil {
		t.Fatalf("decode tool approval payload: %v", err)
	}
	if payload["intent_id"] != intentID.String() || payload["action"] != "approve" || payload["reason"] != "operator reviewed" {
		t.Fatalf("unexpected tool approval payload: %#v", payload)
	}
	assertReactorTag(t, capture.events[0].Tags, "intent", intentID.String())
	assertReactorTag(t, capture.events[0].Tags, "d", "tool-approval:1")
}

func TestPolicyCommandPublisherFailsWhenNoRelayAccepts(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 0}
	signer, err := NewPrivateKeySigner(nostr.GeneratePrivateKey())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	publisher := NewPolicyCommandPublisher(capture, signer)

	enabled := true
	_, err = publisher.PublishPolicyCreateRequest(ctx, PolicyMutationCommand{Name: "require-sbom", Rules: []domain.PolicyRule{{Type: domain.RuleRequireSBOM}}, Enabled: &enabled})
	if err == nil {
		t.Fatalf("expected no relay acceptance error")
	}
}
