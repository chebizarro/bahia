package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

const f74aReceiptFamily = "f74a-import-receipt-v1"

func f74aMigrationTrustSet(cfg config.NostrConfig, logger *zap.Logger) *controlplane.TrustSet {
	return controlplane.NewTrustSet(cfg.AuthorizedPubkeys, logger, controlplane.WithBootstrapOwners(cfg.BootstrapOwners))
}

// The receipt is stored in the outbox control-record bucket, which is not
// pruned with settled events. A cursor can advance only after the callback has
// persisted quorum acceptance for the same signed event ID.
type f74aReceipt struct {
	EventID    string `json:"event_id"`
	Accepted   bool   `json:"accepted"`
	Failed     bool   `json:"failed,omitempty"`
	Deleted    bool   `json:"deleted"`
	OCKVersion int    `json:"ock_version,omitempty"`
	SourceHash string `json:"source_hash,omitempty"`
}

type f74aReceiptStore interface {
	GetControlRecord(string, string) ([]byte, error)
	UpdateControlRecord(string, string, func([]byte) ([]byte, error)) ([]byte, error)
}

type f74aDeliveryLedger struct {
	store  f74aReceiptStore
	author gonostr.PubKey
}

type f74aSourceHashKey struct{}

func f74aSourceHash(item any) (string, error) {
	// Runtime observation metadata and other SQL-only columns are deliberately
	// absent from the canonical event, and the projector ignores observed_at
	// when deduplicating. Hash only its stable published fields so SQL-only and
	// timestamp-only updates do not invalidate an accepted relay projection.
	if obs, ok := item.(*domain.RuntimeObservation); ok {
		item = map[string]any{
			"id": obs.ID.String(), "service_id": obs.ServiceID.String(), "environment_id": obs.EnvironmentID.String(),
			"observed_image_digest": obs.ObservedImageDigest, "observed_container_id": obs.ObservedContainerID,
			"health_status": obs.HealthStatus,
		}
	}
	raw, err := json.Marshal(item)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// The UUID-coordinate tombstone publishes only the row ID and SBOM tag.
// Semantic package fields have their own v2 coordinate and delivery proof.
func f74aLegacyTombstoneIdentity(pkg *domain.SBOMPackage) map[string]any {
	return map[string]any{"id": pkg.ID, "sbom_id": pkg.SBOMID}
}

func f74aRecordContext(ctx context.Context, item any) (context.Context, error) {
	hash, err := f74aSourceHash(item)
	if err != nil {
		return nil, err
	}
	return context.WithValue(ctx, f74aSourceHashKey{}, hash), nil
}

func f74aEventTag(ev gonostr.Event, key string) string {
	for _, tag := range ev.Tags {
		if len(tag) >= 2 && tag[0] == key {
			return tag[1]
		}
	}
	return ""
}
func f74aReceiptKey(author gonostr.PubKey, kind int, d string) string {
	return fmt.Sprintf("%s:%d:%s", author.Hex(), kind, d)
}
func (l f74aDeliveryLedger) mutate(ev gonostr.Event, accepted bool, stage bool, sourceHash string) error {
	if ev.PubKey != l.author || int(ev.Kind) != nostradapter.KindCASControlState {
		return nil
	}
	d := f74aEventTag(ev, "d")
	if d == "" {
		return nil
	}
	kind, err := strconv.Atoi(f74aEventTag(ev, "legacy_kind"))
	if err != nil {
		return nil
	}
	switch kind {
	case nostradapter.KindLLMReleaseRegistry, nostradapter.KindArtifactSignatureRegistry, nostradapter.KindArtifactSBOMRegistry, nostradapter.KindSBOMPackageRegistry, nostradapter.KindRuntimeObservationState, nostradapter.KindOrgKeyEnvelope:
	default:
		return nil
	}
	id := fmt.Sprintf("%x", ev.ID[:])
	_, err = l.store.UpdateControlRecord(f74aReceiptFamily, f74aReceiptKey(l.author, kind, d), func(raw []byte) ([]byte, error) {
		var current f74aReceipt
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &current); err != nil {
				return nil, err
			}
		}
		if !stage && current.EventID != "" && current.EventID != id {
			return raw, nil
		}
		if stage && current.EventID == id {
			return raw, nil
		}
		next := f74aReceipt{EventID: id, Accepted: accepted, Failed: !stage && !accepted, Deleted: f74aEventTag(ev, "deleted") == "true", SourceHash: current.SourceHash}
		if stage && kind != nostradapter.KindOrgKeyEnvelope {
			next.SourceHash = sourceHash
		}
		if kind == nostradapter.KindLLMReleaseRegistry {
			_, version, err := controlplane.VersionFromEnvelope(ev.Content)
			if err != nil {
				return nil, fmt.Errorf("decode release OCK version: %w", err)
			}
			next.OCKVersion = version
		}
		return json.Marshal(next)
	})
	return err
}
func (l f74aDeliveryLedger) stage(ev gonostr.Event) error { return l.stageWithHash(ev, "") }
func (l f74aDeliveryLedger) stageWithHash(ev gonostr.Event, hash string) error {
	return l.mutate(ev, false, true, hash)
}
func (l f74aDeliveryLedger) accepted(ev gonostr.Event) error  { return l.mutate(ev, true, false, "") }
func (l f74aDeliveryLedger) abandoned(ev gonostr.Event) error { return l.mutate(ev, false, false, "") }
func (l f74aDeliveryLedger) receipt(kind int, d string) (f74aReceipt, bool, error) {
	raw, err := l.store.GetControlRecord(f74aReceiptFamily, f74aReceiptKey(l.author, kind, d))
	if err != nil {
		return f74aReceipt{}, false, err
	}
	if len(raw) == 0 {
		return f74aReceipt{}, false, nil
	}
	var rec f74aReceipt
	if err := json.Unmarshal(raw, &rec); err != nil {
		return f74aReceipt{}, false, err
	}
	return rec, true, nil
}

func (l f74aDeliveryLedger) prove(ctx context.Context, phase string, item any) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	var kind int
	var d string
	deleted := phase == "legacy_packages"
	switch phase {
	case "releases":
		x := item.(*domain.LLMRelease)
		kind = nostradapter.KindLLMReleaseRegistry
		d = "llm:release:" + x.ID.String()
	case "signatures":
		x := item.(*domain.ArtifactSignature)
		kind = nostradapter.KindArtifactSignatureRegistry
		d = "artifact:signature:" + x.ID.String()
	case "sboms":
		x := item.(*domain.ArtifactSBOM)
		kind = nostradapter.KindArtifactSBOMRegistry
		d = "artifact:sbom:" + x.ID.String()
	case "semantic_packages":
		x := item.(*domain.SBOMPackage)
		kind = nostradapter.KindSBOMPackageRegistry
		d = nostradapter.SBOMPackageDTag(x)
	case "legacy_packages":
		x := item.(*domain.SBOMPackage)
		kind = nostradapter.KindSBOMPackageRegistry
		d = "artifact:sbom-package:" + x.ID.String()
	case "observations":
		x := item.(*domain.RuntimeObservation)
		kind = nostradapter.KindRuntimeObservationState
		d = nostradapter.RuntimeObservationDTag(x.ServiceID, x.EnvironmentID)
	default:
		return false, fmt.Errorf("unknown F74a proof phase %q", phase)
	}
	rec, found, err := l.receipt(kind, d)
	if err != nil || !found {
		return false, err
	}
	hashInput := item
	if deleted {
		hashInput = f74aLegacyTombstoneIdentity(item.(*domain.SBOMPackage))
	}
	hash, err := f74aSourceHash(hashInput)
	if err != nil {
		return false, err
	}
	if rec.SourceHash != hash || rec.Deleted != deleted {
		return false, nil
	}
	return rec.Accepted, nil
}

// f74aTrackedPublisher journals the signed coordinate before handing it to
// the real publisher; an old accepted receipt cannot satisfy a newer pending
// replacement on the same coordinate.
type f74aTrackedPublisher struct {
	*nostradapter.Publisher
	ledger f74aDeliveryLedger
}

func (p f74aTrackedPublisher) PublishProjection(ctx context.Context, ev gonostr.Event, entityType string, entityID *uuid.UUID) error {
	hash, _ := ctx.Value(f74aSourceHashKey{}).(string)
	if err := p.ledger.stageWithHash(ev, hash); err != nil {
		return err
	}
	return p.Publisher.PublishProjection(ctx, ev, entityType, entityID)
}

var _ nostradapter.ProjectionPublisher = f74aTrackedPublisher{}

// The source-row digest flows only through this explicit import boundary. It
// binds the signed receipt to the row observed by the bounded SQL page.
type f74aRecordPublisher struct{ inner service.F74aBackfillPublisher }

func (p f74aRecordPublisher) PublishLLMRelease(ctx context.Context, x *domain.LLMRelease) error {
	c, err := f74aRecordContext(ctx, x)
	if err != nil {
		return err
	}
	return p.inner.PublishLLMRelease(c, x)
}
func (p f74aRecordPublisher) PublishArtifactSignature(ctx context.Context, x *domain.ArtifactSignature) error {
	c, err := f74aRecordContext(ctx, x)
	if err != nil {
		return err
	}
	return p.inner.PublishArtifactSignature(c, x)
}
func (p f74aRecordPublisher) PublishArtifactSBOM(ctx context.Context, x *domain.ArtifactSBOM) error {
	c, err := f74aRecordContext(ctx, x)
	if err != nil {
		return err
	}
	return p.inner.PublishArtifactSBOM(c, x)
}
func (p f74aRecordPublisher) PublishSBOMPackage(ctx context.Context, x *domain.SBOMPackage) error {
	c, err := f74aRecordContext(ctx, x)
	if err != nil {
		return err
	}
	return p.inner.PublishSBOMPackage(c, x)
}
func (p f74aRecordPublisher) PublishLegacySBOMPackageTombstone(ctx context.Context, x *domain.SBOMPackage) error {
	c, err := f74aRecordContext(ctx, f74aLegacyTombstoneIdentity(x))
	if err != nil {
		return err
	}
	return p.inner.PublishLegacySBOMPackageTombstone(c, x)
}
func (p f74aRecordPublisher) PublishRuntimeObservation(ctx context.Context, x *domain.RuntimeObservation) error {
	c, err := f74aRecordContext(ctx, x)
	if err != nil {
		return err
	}
	return p.inner.PublishRuntimeObservation(c, x)
}

var _ service.F74aBackfillPublisher = f74aRecordPublisher{}

const f74aOCKManifestFamily = "f74a-import-ock-v1"

func f74aOCKManifestID(author gonostr.PubKey) string { return "fleet:" + author.Hex() }

type f74aOCKManifest struct {
	Version     int      `json:"version"`
	KeyHash     string   `json:"key_hash"`
	Recipients  []string `json:"recipients"`
	Coordinates []string `json:"coordinates"`
}

func f74aDistinctOCKManifestEntries(m f74aOCKManifest) error {
	for _, entries := range [][]string{m.Recipients, m.Coordinates} {
		seen := make(map[string]struct{}, len(entries))
		for _, entry := range entries {
			if entry == "" {
				return errors.New("fleet OCK manifest contains an empty recipient or coordinate")
			}
			if _, duplicate := seen[entry]; duplicate {
				return fmt.Errorf("fleet OCK manifest duplicates recipient or coordinate %q", entry)
			}
			seen[entry] = struct{}{}
		}
	}
	return nil
}

type f74aOCKManifestStore interface {
	f74aReceiptStore
	PutControlRecord(string, string, []byte) error
}

// OCKManager distributes sequentially to service first, then the configured
// fleet member source. This wrapper records each random-handle envelope; if a
// recipient wrap fails before publish, count validation rejects the manifest.
type f74aEnvelopePublisher struct {
	inner       *nostradapter.Projector
	mu          sync.Mutex
	coordinates []string
	failures    []error
}

func (p *f74aEnvelopePublisher) PublishKeyEnvelope(ctx context.Context, d, content string) error {
	p.mu.Lock()
	p.coordinates = append(p.coordinates, d)
	p.mu.Unlock()
	err := p.inner.PublishKeyEnvelope(ctx, d, content)
	if err != nil {
		p.mu.Lock()
		p.failures = append(p.failures, err)
		p.mu.Unlock()
	}
	return err
}
func (p *f74aEnvelopePublisher) snapshot() ([]string, []error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.coordinates...), append([]error(nil), p.failures...)
}
func f74aRecipients(ctx context.Context, servicePubkey string, members *controlplane.TrustSetMemberSource) ([]string, error) {
	rest, err := members.OrgMemberPubkeys(ctx, kinds.FleetOCKScope)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{servicePubkey: true}
	out := []string{servicePubkey}
	for _, pk := range rest {
		if pk != "" && !seen[pk] {
			seen[pk] = true
			out = append(out, pk)
		}
	}
	slices.Sort(out[1:])
	return out, nil
}
func f74aLoadOCKManifest(store f74aOCKManifestStore, author gonostr.PubKey) (f74aOCKManifest, error) {
	raw, err := store.GetControlRecord(f74aOCKManifestFamily, f74aOCKManifestID(author))
	if err != nil || len(raw) == 0 {
		return f74aOCKManifest{}, err
	}
	var m f74aOCKManifest
	err = json.Unmarshal(raw, &m)
	return m, err
}
func f74aPrepareOCK(ctx context.Context, store f74aOCKManifestStore, manager *controlplane.OCKManager, wraps *f74aEnvelopePublisher, recipients []string, author gonostr.PubKey) (f74aOCKManifest, error) {
	m, err := f74aLoadOCKManifest(store, author)
	if err != nil {
		return m, err
	}
	if m.Version > 0 {
		if err := f74aDistinctOCKManifestEntries(m); err != nil {
			return m, err
		}
	}
	if m.Version > 0 && m.KeyHash != "" && slices.Equal(m.Recipients, recipients) && len(m.Coordinates) == len(recipients) {
		refused := false
		ledger := f74aDeliveryLedger{store: store, author: author}
		for _, d := range m.Coordinates {
			receipt, found, err := ledger.receipt(nostradapter.KindOrgKeyEnvelope, d)
			if err != nil {
				return m, err
			}
			if found && receipt.Failed {
				refused = true
				break
			}
		}
		if !refused {
			return m, nil
		}
	}
	key, err := manager.RotateKey(ctx, kinds.FleetOCKScope)
	if err != nil {
		return m, err
	}
	coords, failures := wraps.snapshot()
	if len(failures) > 0 {
		return m, fmt.Errorf("fleet OCK envelope publication failed: %w", errors.Join(failures...))
	}
	if len(coords) != len(recipients) {
		return m, fmt.Errorf("fleet OCK wrapped %d of %d recipients", len(coords), len(recipients))
	}
	prefix := fmt.Sprintf("org-key:%s:v%d:", kinds.FleetOCKScope, key.Version)
	for _, d := range coords {
		if !strings.HasPrefix(d, prefix) {
			return m, fmt.Errorf("unexpected fleet OCK envelope coordinate %q", d)
		}
	}
	sum := sha256.Sum256(key.Key[:])
	m = f74aOCKManifest{Version: key.Version, KeyHash: hex.EncodeToString(sum[:]), Recipients: recipients, Coordinates: coords}
	if err := f74aDistinctOCKManifestEntries(m); err != nil {
		return m, err
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return m, err
	}
	return m, store.PutControlRecord(f74aOCKManifestFamily, f74aOCKManifestID(author), raw)
}
func (l f74aDeliveryLedger) proveOCK(ctx context.Context, store f74aOCKManifestStore, version int) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	m, err := f74aLoadOCKManifest(store, l.author)
	if err != nil {
		return false, err
	}
	if m.Version != version || m.KeyHash == "" || len(m.Coordinates) == 0 || len(m.Coordinates) != len(m.Recipients) {
		return false, nil
	}
	if err := f74aDistinctOCKManifestEntries(m); err != nil {
		return false, nil
	}
	prefix := fmt.Sprintf("org-key:%s:v%d:", kinds.FleetOCKScope, version)
	for _, d := range m.Coordinates {
		if !strings.HasPrefix(d, prefix) {
			return false, nil
		}
		rec, found, err := l.receipt(nostradapter.KindOrgKeyEnvelope, d)
		if err != nil {
			return false, err
		}
		if !found || !rec.Accepted || rec.Deleted {
			return false, nil
		}
	}
	return true, nil
}
