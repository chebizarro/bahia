package nostr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// F74aCanonicalPublisher writes the read-only MCP families through the shared
// cp-state signer and outbox. Package records are indexed individually: an SBOM
// with thousands of packages never becomes one oversized Nostr frame.
type F74aCanonicalPublisher struct {
	projector *Projector
	encryptor ConfidentialStateEncryptor
	marker    F74aBackfillDirtyMarker
}

// F74aBackfillDirtyMarker triggers a one-time repair pass on the next startup
// if a post-commit projection could not enter the signed outbox.
type F74aBackfillDirtyMarker interface {
	PutControlRecord(family, id string, value []byte) error
}

func NewF74aCanonicalPublisher(projector *Projector, encryptor ConfidentialStateEncryptor, marker ...F74aBackfillDirtyMarker) *F74aCanonicalPublisher {
	p := &F74aCanonicalPublisher{projector: projector, encryptor: encryptor}
	if len(marker) > 0 {
		p.marker = marker[0]
	}
	return p
}

func (p *F74aCanonicalPublisher) MarkBackfillDirty() error {
	if p == nil || p.marker == nil {
		return nil
	}
	return p.marker.PutControlRecord("bootstrap", "f74a-canonical-v1", []byte("dirty"))
}

// Keep content below both NIP-44's plaintext ceiling and common relay frame
// limits, leaving room for tags, signatures and the encrypted envelope.
const f74aMaxContent = 60000
const f74aMaxConfidentialPlaintext = 40000

func (p *F74aCanonicalPublisher) publish(ctx context.Context, kind int, d string, deleted, confidential bool, tags gonostr.Tags, value any, entity string, id *uuid.UUID) (err error) {
	if p != nil && p.marker != nil {
		defer func() {
			if err != nil {
				err = errors.Join(err, p.MarkBackfillDirty())
			}
		}()
	}
	if p == nil || p.projector == nil || !p.projector.Enabled() {
		return nil
	}
	content, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", entity, err)
	}
	if len(content) > f74aMaxContent {
		return fmt.Errorf("%s record exceeds %d-byte cp-state content limit", entity, f74aMaxContent)
	}
	if confidential {
		if len(content) > f74aMaxConfidentialPlaintext {
			return fmt.Errorf("%s record exceeds %d-byte confidential plaintext limit", entity, f74aMaxConfidentialPlaintext)
		}
		if p.encryptor == nil {
			return fmt.Errorf("confidential encryptor not configured for %s", entity)
		}
		family := cpStateFamilies[kind]
		sealed, err := p.encryptor.EncryptConfidential(ctx, kinds.FleetOCKScope, content, kind, d, family.topic, nil)
		if err != nil {
			return fmt.Errorf("encrypt %s: %w", entity, err)
		}
		content = []byte(sealed)
		if len(content) > f74aMaxContent {
			return fmt.Errorf("%s encrypted record exceeds %d-byte cp-state content limit", entity, f74aMaxContent)
		}
	}
	return p.projector.publishControlState(ctx, kind, d, deleted, tags, string(content), entity, id)
}

// Publish*State accepts deleted=true for a tombstone on the live coordinate.
// The normal mutation interfaces call these with deleted=false; delete writers
// use the same method rather than constructing a second envelope.
func (p *F74aCanonicalPublisher) PublishLLMRelease(ctx context.Context, release *domain.LLMRelease) error {
	return p.PublishLLMReleaseState(ctx, release, false)
}
func (p *F74aCanonicalPublisher) PublishLLMReleaseState(ctx context.Context, release *domain.LLMRelease, deleted bool) error {
	if release == nil || release.ID == uuid.Nil {
		return fmt.Errorf("LLM release ID is required")
	}
	value := any(release)
	if deleted {
		value = map[string]any{"id": release.ID, "deleted": true}
	}
	return p.publish(ctx, KindLLMReleaseRegistry, "llm:release:"+release.ID.String(), deleted, true,
		gonostr.Tags{{"route_id", release.RouteID.String()}}, value, "llm_release.projection", &release.ID)
}
func (p *F74aCanonicalPublisher) PublishArtifactSignature(ctx context.Context, sig *domain.ArtifactSignature) error {
	return p.PublishArtifactSignatureState(ctx, sig, false)
}
func (p *F74aCanonicalPublisher) PublishArtifactSignatureState(ctx context.Context, sig *domain.ArtifactSignature, deleted bool) error {
	if sig == nil || sig.ID == uuid.Nil {
		return fmt.Errorf("signature ID is required")
	}
	value := any(sig)
	if deleted {
		value = map[string]any{"id": sig.ID, "deleted": true}
	}
	return p.publish(ctx, KindArtifactSignatureRegistry, "artifact:signature:"+sig.ID.String(), deleted, false,
		gonostr.Tags{{"artifact_id", sig.ArtifactID.String()}}, value, "artifact_signature.projection", &sig.ID)
}
func (p *F74aCanonicalPublisher) PublishArtifactSBOM(ctx context.Context, sbom *domain.ArtifactSBOM) error {
	return p.PublishArtifactSBOMState(ctx, sbom, false)
}
func (p *F74aCanonicalPublisher) PublishArtifactSBOMState(ctx context.Context, sbom *domain.ArtifactSBOM, deleted bool) error {
	if sbom == nil || sbom.ID == uuid.Nil {
		return fmt.Errorf("SBOM ID is required")
	}
	value := any(sbom)
	if deleted {
		value = map[string]any{"id": sbom.ID, "deleted": true}
	}
	return p.publish(ctx, KindArtifactSBOMRegistry, "artifact:sbom:"+sbom.ID.String(), deleted, false,
		gonostr.Tags{{"artifact_id", sbom.ArtifactID.String()}}, value, "artifact_sbom.projection", &sbom.ID)
}
func (p *F74aCanonicalPublisher) PublishSBOMPackage(ctx context.Context, pkg *domain.SBOMPackage) error {
	return p.PublishSBOMPackageState(ctx, pkg, false)
}
func (p *F74aCanonicalPublisher) PublishSBOMPackageState(ctx context.Context, pkg *domain.SBOMPackage, deleted bool) error {
	if pkg == nil || pkg.ID == uuid.Nil {
		return fmt.Errorf("SBOM package ID is required")
	}
	value := any(pkg)
	if deleted {
		value = map[string]any{"id": pkg.ID, "deleted": true}
	}
	return p.publish(ctx, KindSBOMPackageRegistry, "artifact:sbom-package:"+pkg.ID.String(), deleted, false,
		gonostr.Tags{{"sbom_id", pkg.SBOMID.String()}}, value, "sbom_package.projection", &pkg.ID)
}
func (p *F74aCanonicalPublisher) PublishRuntimeObservation(ctx context.Context, obs *domain.RuntimeObservation) error {
	return p.PublishRuntimeObservationState(ctx, obs, false)
}
func (p *F74aCanonicalPublisher) PublishRuntimeObservationState(ctx context.Context, obs *domain.RuntimeObservation, deleted bool) error {
	if obs == nil || obs.ServiceID == uuid.Nil || obs.EnvironmentID == uuid.Nil {
		return fmt.Errorf("runtime observation service and environment IDs are required")
	}
	// Arbitrary metadata can contain secrets and is not projected.
	record := map[string]any{
		"id": obs.ID.String(), "service_id": obs.ServiceID.String(), "environment_id": obs.EnvironmentID.String(),
		"observed_image_digest": obs.ObservedImageDigest, "observed_container_id": obs.ObservedContainerID,
		"health_status": obs.HealthStatus, "observed_at": obs.ObservedAt.UTC().Format(time.RFC3339Nano),
	}
	if deleted {
		record = map[string]any{"service_id": obs.ServiceID.String(), "environment_id": obs.EnvironmentID.String(), "deleted": true}
	}
	d := "runtime:observation:" + obs.ServiceID.String() + ":" + obs.EnvironmentID.String()
	return p.publish(ctx, KindRuntimeObservationState, d, deleted, false,
		gonostr.Tags{{"service_id", obs.ServiceID.String()}, {"environment_id", obs.EnvironmentID.String()}},
		record, "runtime_observation.projection", &obs.ID)
}
