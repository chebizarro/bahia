package controlplane

import (
	"context"
	"fmt"
	"strings"

	"fiatjaf.com/nostr"
	canonicalnostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

// ArtifactRegisterCommand describes a signer-first artifact registration request.
type ArtifactRegisterCommand struct {
	BuildID           uuid.UUID
	ServiceID         uuid.UUID
	ImageRepo         string
	ImageTag          string
	ImageDigest       string
	ManifestMediaType string
	SizeBytes         *int64
	SBOMURL           string
	SignatureRef      string
	ScanStatus        domain.ScanStatus
	Metadata          map[string]any
	IdempotencyKey    string
	AgentID           string
}

// ArtifactCommandReceipt is the correlation handle for artifact registration events.
type ArtifactCommandReceipt struct {
	RequestEventID  string         `json:"request_event_id"`
	RequestPubkey   string         `json:"request_pubkey"`
	RequestKind     int            `json:"request_kind"`
	ResultKind      int            `json:"result_kind"`
	RegistryKind    int            `json:"registry_kind"`
	DTag            string         `json:"d_tag,omitempty"`
	IdempotencyKey  string         `json:"idempotency_key,omitempty"`
	Status          string         `json:"status"`
	Error           string         `json:"error,omitempty"`
	PublishedRelays int            `json:"published_relays"`
	BuildID         string         `json:"build_id,omitempty"`
	ServiceID       string         `json:"service_id,omitempty"`
	ImageDigest     string         `json:"image_digest,omitempty"`
	RelayOutcomes   []RelayOutcome `json:"relay_outcomes,omitempty"`
}

// RelayOutcome records the relay OK acceptance flag and message/reason.
type RelayOutcome struct {
	RelayURL string `json:"relay_url,omitempty"`
	Accepted bool   `json:"accepted"`
	Reason   string `json:"reason,omitempty"`
	Error    string `json:"error,omitempty"`
}

// ArtifactCommandPublisher emits signed ContextVM kind 25910 artifact/register
// requests. Legacy request kind 5985 is a migration input only and is never
// published.
type ArtifactCommandPublisher struct {
	publisher NostrEventPublisher
	signer    canonicalnostr.Signer
}

func NewArtifactCommandPublisher(publisher NostrEventPublisher, signer canonicalnostr.Signer) *ArtifactCommandPublisher {
	return &ArtifactCommandPublisher{publisher: publisher, signer: signer}
}

func (p *ArtifactCommandPublisher) PublishArtifactRegisterRequest(ctx context.Context, cmd ArtifactRegisterCommand) (*ArtifactCommandReceipt, error) {
	if p == nil || p.publisher == nil {
		return nil, fmt.Errorf("artifact command publisher is not configured")
	}
	if cmd.BuildID == uuid.Nil {
		return nil, fmt.Errorf("build_id is required")
	}
	if cmd.ServiceID == uuid.Nil {
		return nil, fmt.Errorf("service_id is required")
	}
	if err := domain.ValidateRequiredString(cmd.ImageRepo, "image_repo"); err != nil {
		return nil, err
	}
	if err := domain.ValidateRequiredString(cmd.ImageTag, "image_tag"); err != nil {
		return nil, err
	}
	if err := domain.ValidateImageDigest(cmd.ImageDigest); err != nil {
		return nil, err
	}
	if cmd.ScanStatus == "" {
		cmd.ScanStatus = domain.ScanStatusUnknown
	}
	if err := domain.ValidateScanStatus(cmd.ScanStatus); err != nil {
		return nil, err
	}
	content := map[string]any{
		"build_id":     cmd.BuildID.String(),
		"service_id":   cmd.ServiceID.String(),
		"image_repo":   cmd.ImageRepo,
		"image_tag":    cmd.ImageTag,
		"image_digest": cmd.ImageDigest,
		"scan_status":  string(cmd.ScanStatus),
	}
	if cmd.ManifestMediaType != "" {
		content["manifest_media_type"] = cmd.ManifestMediaType
	}
	if cmd.SizeBytes != nil {
		content["size_bytes"] = *cmd.SizeBytes
	}
	if cmd.SBOMURL != "" {
		content["sbom_url"] = cmd.SBOMURL
	}
	if cmd.SignatureRef != "" {
		content["signature_ref"] = cmd.SignatureRef
	}
	if len(cmd.Metadata) > 0 {
		content["metadata"] = cmd.Metadata
	}
	dTag := strings.TrimSpace(cmd.IdempotencyKey)
	if dTag == "" {
		dTag = "artifact-register:" + uuid.NewString()
	}
	tags := nostr.Tags{{"service", cmd.ServiceID.String()}, {"build", cmd.BuildID.String()}, {"digest", cmd.ImageDigest}}
	ev, published, dTag, err := publishContextVMCommand(ctx, p.publisher, p.signer, ContextVMMethodArtifactRegister, dTag, cmd.AgentID, tags, content, "artifact register")
	if ev == nil {
		return nil, err
	}
	receipt := &ArtifactCommandReceipt{RequestEventID: ev.ID.Hex(), RequestPubkey: ev.PubKey.Hex(), RequestKind: int(ev.Kind), ResultKind: KindContextVMMessage, RegistryKind: KindCASControlState, DTag: dTag, IdempotencyKey: dTag, Status: "submitted", PublishedRelays: published, BuildID: cmd.BuildID.String(), ServiceID: cmd.ServiceID.String(), ImageDigest: cmd.ImageDigest}
	if err != nil {
		if published > 0 {
			receipt.Status = "error"
			receipt.Error = err.Error()
			return receipt, nil
		}
		return nil, err
	}
	return receipt, nil
}
