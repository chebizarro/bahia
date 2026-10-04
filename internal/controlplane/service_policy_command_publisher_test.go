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
