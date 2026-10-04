package controlplane

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/api/dto"
	"github.com/openagentsinc/bahia/internal/domain"
)

func TestPolicyCommandPublisherPublishesCanonicalCreateAndEvaluateRequests(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 1}
	signer, err := NewPrivateKeySigner(nostr.Generate().Hex())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	publisher := NewPolicyCommandPublisher(capture, signer)
	envID := uuid.New()
	artifactID := uuid.New()
	serviceID := uuid.New()

	enabled := true
	create, err := publisher.PublishPolicyCreateRequest(ctx, PolicyMutationCommand{Name: "require-sbom", EnvironmentID: &envID, Rules: []domain.PolicyRule{{Type: domain.RuleRequireSBOM}}, Enforcement: string(domain.PolicyEnforcementBlock), Enabled: &enabled, IdempotencyKey: "policy-create:require-sbom", AgentID: "agent-7"})
	if err != nil {
		t.Fatalf("publish policy create: %v", err)
	}
	if create.RequestKind != KindContextVMMessage || create.StatusKind != KindNIP38Status || create.ResultKind != KindContextVMMessage || create.ReadModelKinds["policy_registry"] != KindCASControlState || create.PublishedRelays != 1 {
		t.Fatalf("unexpected create receipt: %#v", create)
	}
	createParams := assertContextVMCommand(t, capture.events[0], ContextVMMethodPolicyCreate)
	if createParams["name"] != "require-sbom" || createParams["enabled"] != true {
		t.Fatalf("unexpected create params: %#v", createParams)
	}
	assertReactorTag(t, capture.events[0].Tags, "environment", envID.String())
	assertReactorTag(t, capture.events[0].Tags, "d", "policy-create:require-sbom")
	assertReactorTag(t, capture.events[0].Tags, "agent", "agent-7")

	evaluate, err := publisher.PublishPolicyEvaluateRequest(ctx, PolicyMutationCommand{ArtifactID: artifactID, EnvironmentID: &envID, ServiceID: &serviceID, IdempotencyKey: "policy-evaluate:artifact"})
	if err != nil {
		t.Fatalf("publish policy evaluate: %v", err)
	}
	if evaluate.RequestKind != KindContextVMMessage || evaluate.ReadModelKinds != nil {
		t.Fatalf("unexpected evaluate receipt: %#v", evaluate)
	}
	evalParams := assertContextVMCommand(t, capture.events[1], ContextVMMethodPolicyEvaluate)
	if evalParams["artifact_id"] != artifactID.String() || evalParams["environment_id"] != envID.String() || evalParams["service_id"] != serviceID.String() {
		t.Fatalf("unexpected evaluate params: %#v", evalParams)
	}
}

func TestToolApprovalCommandPublisherPublishesCanonicalApprovalResponse(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 2}
	signer, err := NewPrivateKeySigner(nostr.Generate().Hex())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	publisher := NewToolApprovalCommandPublisher(capture, signer)
	intentID := uuid.New()

	receipt, err := publisher.PublishToolApprovalResponse(ctx, ToolApprovalCommand{IntentID: intentID, Action: "approve", Reason: "operator reviewed", IdempotencyKey: "tool-approval:1"})
	if err != nil {
		t.Fatalf("publish tool approval: %v", err)
	}
	if receipt.RequestKind != KindContextVMMessage || receipt.ResultKind != KindContextVMMessage || receipt.PublishedRelays != 2 || receipt.IntentID != intentID.String() || receipt.Action != "approve" || receipt.DTag != "tool-approval:1" {
		t.Fatalf("unexpected tool approval receipt: %#v", receipt)
	}
	payload := assertContextVMCommand(t, capture.events[0], ContextVMMethodToolApprovalResponse)
	if payload["intent_id"] != intentID.String() || payload["action"] != "approve" || payload["reason"] != "operator reviewed" {
		t.Fatalf("unexpected tool approval payload: %#v", payload)
	}
	assertReactorTag(t, capture.events[0].Tags, "intent", intentID.String())
	assertReactorTag(t, capture.events[0].Tags, "d", "tool-approval:1")
}

func TestArtifactCommandPublisherPublishesCanonicalArtifactRegisterRequest(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 1}
	signer, err := NewPrivateKeySigner(nostr.Generate().Hex())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	publisher := NewArtifactCommandPublisher(capture, signer)
	buildID := uuid.New()
	serviceID := uuid.New()
	digest := "sha256:" + strings.Repeat("a", 64)
	size := int64(4096)

	receipt, err := publisher.PublishArtifactRegisterRequest(ctx, ArtifactRegisterCommand{BuildID: buildID, ServiceID: serviceID, ImageRepo: "registry.example/payments", ImageTag: "v1.2.3", ImageDigest: digest, SizeBytes: &size, Metadata: map[string]any{"source": "ci"}, IdempotencyKey: "artifact-register:payments:v1.2.3"})
	if err != nil {
		t.Fatalf("publish artifact register: %v", err)
	}
	if receipt.RequestKind != KindContextVMMessage || receipt.ResultKind != KindContextVMMessage || receipt.RegistryKind != KindCASControlState || receipt.PublishedRelays != 1 || receipt.Status != "submitted" {
		t.Fatalf("unexpected artifact receipt: %#v", receipt)
	}
	if receipt.IdempotencyKey != "artifact-register:payments:v1.2.3" || receipt.BuildID != buildID.String() || receipt.ServiceID != serviceID.String() || receipt.ImageDigest != digest {
		t.Fatalf("missing artifact receipt metadata: %#v", receipt)
	}
	params := assertContextVMCommand(t, capture.events[0], ContextVMMethodArtifactRegister)
	assertReactorTag(t, capture.events[0].Tags, "service", serviceID.String())
	assertReactorTag(t, capture.events[0].Tags, "build", buildID.String())
	assertReactorTag(t, capture.events[0].Tags, "digest", digest)
	assertReactorTag(t, capture.events[0].Tags, "d", "artifact-register:payments:v1.2.3")

	// The params must decode cleanly into the strict artifact/register handler DTO.
	rawParams, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("re-encode params: %v", err)
	}
	var dtoParams dto.RegisterArtifactRequest
	if err := decodeStrictContextVMParams(rawParams, &dtoParams); err != nil {
		t.Fatalf("artifact/register handler would reject publisher params: %v", err)
	}
	if dtoParams.BuildID != buildID || dtoParams.ServiceID != serviceID || dtoParams.ImageDigest != digest || dtoParams.ScanStatus != string(domain.ScanStatusUnknown) || dtoParams.SizeBytes == nil || *dtoParams.SizeBytes != size {
		t.Fatalf("unexpected decoded artifact params: %#v", dtoParams)
	}
}

// TestMCPCommandPublishersNeverSignLegacyRequestKinds guards bahia-n5uin: the
// policy, artifact, and tool-approval publishers must only emit ContextVM 25910.
func TestMCPCommandPublishersNeverSignLegacyRequestKinds(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 1}
	signer, err := NewPrivateKeySigner(nostr.Generate().Hex())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	envID := uuid.New()
	enabled := true
	policies := NewPolicyCommandPublisher(capture, signer)
	if _, err := policies.PublishPolicyCreateRequest(ctx, PolicyMutationCommand{Name: "p", Rules: []domain.PolicyRule{{Type: domain.RuleRequireSBOM}}, Enabled: &enabled}); err != nil {
		t.Fatalf("policy create: %v", err)
	}
	if _, err := policies.PublishPolicyEvaluateRequest(ctx, PolicyMutationCommand{ArtifactID: uuid.New(), EnvironmentID: &envID}); err != nil {
		t.Fatalf("policy evaluate: %v", err)
	}
	if _, err := NewArtifactCommandPublisher(capture, signer).PublishArtifactRegisterRequest(ctx, ArtifactRegisterCommand{BuildID: uuid.New(), ServiceID: uuid.New(), ImageRepo: "r", ImageTag: "t", ImageDigest: "sha256:" + strings.Repeat("b", 64)}); err != nil {
		t.Fatalf("artifact register: %v", err)
	}
	if _, err := NewToolApprovalCommandPublisher(capture, signer).PublishToolApprovalResponse(ctx, ToolApprovalCommand{IntentID: uuid.New(), Action: "reject", Reason: "no"}); err != nil {
		t.Fatalf("tool approval: %v", err)
	}
	legacy := map[nostr.Kind]bool{KindArtifactRegister: true, KindPolicyCreate: true, KindPolicyUpdate: true, KindPolicyDelete: true, KindPolicyEvaluate: true, KindToolApprovalResponse: true}
	if len(capture.events) != 4 {
		t.Fatalf("published events=%d, want 4", len(capture.events))
	}
	for i, ev := range capture.events {
		if legacy[ev.Kind] || ev.Kind != KindContextVMMessage {
			t.Fatalf("event %d kind = %d, want ContextVM %d", i, ev.Kind, KindContextVMMessage)
		}
	}
}

func TestPolicyCommandPublisherFailsWhenNoRelayAccepts(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 0}
	signer, err := NewPrivateKeySigner(nostr.Generate().Hex())
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
