package controlplane

import (
	"context"
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

func TestPolicyCommandPublisherPublishesCanonicalPolicyCreateUpdateDeleteRequests(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 1}
	signer, err := NewPrivateKeySigner(nostr.GeneratePrivateKey())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	publisher := NewPolicyCommandPublisher(capture, signer)
	envID := uuid.New()
	policyID := uuid.New()

	enabled := true
	create, err := publisher.PublishPolicyCreateRequest(ctx, PolicyMutationCommand{Name: "require-sbom", EnvironmentID: &envID, Rules: []domain.PolicyRule{{Type: domain.RuleRequireSBOM}}, Enforcement: string(domain.PolicyEnforcementBlock), Enabled: &enabled, IdempotencyKey: "policy-create:require-sbom"})
	if err != nil {
		t.Fatalf("publish policy create: %v", err)
	}
	if create.RequestKind != KindContextVMMessage || create.StatusKind != KindNIP38Status || create.ResultKind != KindContextVMMessage || create.ReadModelKinds["policy_registry"] != KindCASControlState {
		t.Fatalf("unexpected create receipt: %#v", create)
	}
	createParams := assertContextVMCommand(t, capture.events[0], ContextVMMethodPolicyCreate)
	if createParams["name"] != "require-sbom" || createParams["enabled"] != true {
		t.Fatalf("unexpected create params: %#v", createParams)
	}
	assertReactorTag(t, capture.events[0].Tags, "environment", envID.String())

	_, err = publisher.PublishPolicyUpdateRequest(ctx, PolicyMutationCommand{ID: policyID, Enforcement: string(domain.PolicyEnforcementWarn), IdempotencyKey: "policy-update:require-sbom"})
	if err != nil {
		t.Fatalf("publish policy update: %v", err)
	}
	updateParams := assertContextVMCommand(t, capture.events[1], ContextVMMethodPolicyUpdate)
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
	deleteParams := assertContextVMCommand(t, capture.events[2], ContextVMMethodPolicyDelete)
	if deleteParams["id"] != policyID.String() {
		t.Fatalf("unexpected delete params: %#v", deleteParams)
	}
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
