package controlplane

import (
	"context"
	"encoding/json"
	"fmt"

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
}

// ArtifactCommandReceipt is the correlation handle for artifact registration events.
type ArtifactCommandReceipt struct {
	RequestEventID  string         `json:"request_event_id"`
	RequestPubkey   string         `json:"request_pubkey"`
	RequestKind     int            `json:"request_kind"`
	ResultKind      int            `json:"result_kind"`
	RegistryKind    int            `json:"registry_kind"`
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

// ArtifactCommandPublisher emits signed artifact registration events.
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
	body, err := json.Marshal(content)
	if err != nil {
		return nil, fmt.Errorf("marshal artifact register request: %w", err)
	}
	ev := &nostr.Event{Kind: KindArtifactRegister, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"service", cmd.ServiceID.String()}, {"build", cmd.BuildID.String()}, {"digest", cmd.ImageDigest}}, Content: string(body)}
	if err := SignGoNostrEvent(ctx, p.signer, ev); err != nil {
		return nil, fmt.Errorf("sign artifact register request: %w", err)
	}
	published, err := p.publisher.Publish(ctx, *ev)
	receipt := &ArtifactCommandReceipt{RequestEventID: ev.ID.Hex(), RequestPubkey: ev.PubKey.Hex(), RequestKind: KindArtifactRegister, ResultKind: KindActionResult, RegistryKind: KindArtifactRegistry, Status: "submitted", PublishedRelays: published, BuildID: cmd.BuildID.String(), ServiceID: cmd.ServiceID.String(), ImageDigest: cmd.ImageDigest}
	if err != nil {
		receipt.Status = "error"
		receipt.Error = err.Error()
		if published > 0 {
			return receipt, nil
		}
		return nil, err
	}
	if published == 0 {
		return nil, fmt.Errorf("publish artifact register request: no relay accepted the request")
	}
	return receipt, nil
}
