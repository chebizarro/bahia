package controlplane

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

const virtualizationReceiptFamily = "virtualization-intent-v1"

// ErrVirtualizationPending means the exact signed state is durably staged but
// has not yet obtained the configured relay OK quorum. No 30315 is final yet.
var ErrVirtualizationPending = errors.New("virtualization canonical publication pending")

// VirtualizationUncertainError never permits a second provider mutation. A
// prepared receipt without matching provider marker cannot distinguish a call
// that did not start from a call that changed power before writing its marker.
type VirtualizationUncertainError struct{ Coordinate string }

func (e *VirtualizationUncertainError) Error() string {
	return "bahia-4f10q.1: provider effect for " + e.Coordinate + " cannot be proven from its receipt and marker; manual reconciliation required"
}

// VirtualizationIntentDependencies is deliberately local-store/outbox based;
// there is no PostgreSQL journal or SQL row recovery input.
type VirtualizationIntentDependencies struct {
	Outbox          *localstore.Outbox
	Events          *localstore.Store
	Signer          nostr.Signer
	Publish         func(context.Context, nostr.Event) error
	Target          string
	Provider        domain.PersistentVMProvider
	ProviderForHost func(context.Context, domain.VirtualizationHost) (domain.PersistentVMProvider, error)
}

type virtualizationRuntime struct {
	VirtualizationIntentDependencies
	pubkey nostr.PubKey
}

// Outgoing and incoming App instances overlap during reload and share the
// local outbox file. Serialize their coordinate/receipt checks with effects.
var virtualizationIntentMu sync.Mutex

func newVirtualizationRuntime(d VirtualizationIntentDependencies) *virtualizationRuntime {
	return &virtualizationRuntime{VirtualizationIntentDependencies: d}
}

type virtualizationResourceRequest struct {
	ID                uuid.UUID                         `json:"id"`
	ResourceKind      domain.VirtualizationResourceKind `json:"resource_kind"`
	Resource          json.RawMessage                   `json:"resource,omitempty"`
	Deleted           bool                              `json:"deleted,omitempty"`
	ExpectedUpdatedAt string                            `json:"expected_updated_at,omitempty"`
}

type virtualizationCanonicalRecord struct {
	ID           uuid.UUID                         `json:"id"`
	OrgID        uuid.UUID                         `json:"org_id"`
	ResourceKind domain.VirtualizationResourceKind `json:"resource_kind"`
	UpdatedAt    time.Time                         `json:"updated_at"`
	Deleted      bool                              `json:"deleted"`
	Resource     json.RawMessage                   `json:"resource,omitempty"`
	IntentID     string                            `json:"intent_id"`
}

type virtualizationReceipt struct {
	Intent      nostr.Event                `json:"intent"`
	Coordinate  string                     `json:"coordinate"`
	ContentHash string                     `json:"content_hash"`
	Phase       string                     `json:"phase"`
	Effect      *virtualizationEffectInput `json:"effect,omitempty"`
	Canonical   *nostr.Event               `json:"canonical,omitempty"`
}

// The exact effect input is durably bound before Execute. VMProviderOperation
// cannot be marshaled by design, so its serializable constituents are stored.
type virtualizationEffectInput struct {
	Host      domain.VirtualizationHost     `json:"host"`
	Image     domain.VMImage                `json:"image"`
	VM        domain.PersistentVMDeployment `json:"vm"`
	Operation domain.VMOperation            `json:"operation"`
}

func virtualizationFamily(kind domain.VirtualizationResourceKind) (kinds.CPStateFamily, string, string, bool) {
	switch kind {
	case domain.VirtualizationHostResource:
		return kinds.CPStateFamilyVirtualizationHost, "virtualization-host:", kinds.CPStateTopicVirtualizationHost, true
	case domain.VMImageResource:
		return kinds.CPStateFamilyVirtualizationImage, "vm-image:", kinds.CPStateTopicVirtualizationImage, true
	case domain.PersistentVMResource:
		return kinds.CPStateFamilyVirtualizationVM, "persistent-vm:", kinds.CPStateTopicVirtualizationVM, true
	default:
		return 0, "", "", false
	}
}

func validateVirtualizationResourceIntent(intent *Intent) error {
	if intent == nil || intent.Event == nil || !intent.Event.CheckID() || !intent.Event.VerifySignature() {
		return fmt.Errorf("virtualization resource requires a signed event")
	}
	parsed, err := ParseIntent(intent.Event)
	if err != nil || parsed.Domain != kinds.VirtualizationDomain || parsed.Schema != kinds.VirtualizationIntentSchema ||
		parsed.OrgID == uuid.Nil || intent.Actor != intent.Event.PubKey.Hex() || intent.Domain != parsed.Domain ||
		intent.Op != parsed.Op || intent.Schema != parsed.Schema || intent.OrgID != parsed.OrgID ||
		intent.IntentID != parsed.IntentID || intent.Coordinate != parsed.Coordinate || !virtualizationSameContent(intent.Content, parsed.Content) ||
		!virtualizationSameRevision(intent.ExpectedUpdatedAt, parsed.ExpectedUpdatedAt) ||
		!virtualizationTagExactlyOnce(intent.Event.Tags, "d", intent.Coordinate) ||
		!virtualizationTagExactlyOnce(intent.Event.Tags, "domain", kinds.VirtualizationDomain) ||
		!virtualizationTagExactlyOnce(intent.Event.Tags, "op", intent.Op) ||
		!virtualizationTagExactlyOnce(intent.Event.Tags, "schema", kinds.VirtualizationIntentSchema) ||
		!virtualizationTagExactlyOnce(intent.Event.Tags, "org", intent.OrgID.String()) ||
		!virtualizationTagExactlyOnce(intent.Event.Tags, "intent_id", intent.IntentID) || !virtualizationTopicsValid(intent.Event.Tags) {
		return fmt.Errorf("virtualization resource envelope does not match signed intent")
	}
	intentID, err := uuid.Parse(intent.IntentID)
	if err != nil || intentID.Version() != 7 || len(intent.Event.Content) == 0 || len(intent.Event.Content) > 1<<20 {
		return fmt.Errorf("virtualization resource intent_id must be UUIDv7 and content bounded")
	}
	if intent.Op != "create" && intent.Op != "update" && intent.Op != "delete" {
		return fmt.Errorf("bahia-4f10q.1: unsupported virtualization op %q", intent.Op)
	}
	request, err := decodeVirtualizationResourceRequest(intent.Event.Content)
	if err != nil {
		return err
	}
	_, prefix, _, ok := virtualizationFamily(request.ResourceKind)
	if !ok {
		return fmt.Errorf("bahia-4f10q.1: unsupported virtualization resource kind %q", request.ResourceKind)
	}
	if request.ID == uuid.Nil || request.ID.Version() != 7 || intent.Coordinate != prefix+request.ID.String() {
		return fmt.Errorf("virtualization resource kind, UUIDv7 id, or coordinate is invalid")
	}
	if intent.Op == "delete" {
		if !request.Deleted || len(request.Resource) != 0 {
			return fmt.Errorf("virtualization delete requires a tombstone without resource")
		}
	} else if request.Deleted || len(request.Resource) == 0 || bytes.Equal(request.Resource, []byte("null")) {
		return fmt.Errorf("virtualization upsert requires complete desired resource")
	}
	return nil
}

func decodeVirtualizationResourceRequest(content string) (virtualizationResourceRequest, error) {
	var request virtualizationResourceRequest
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return request, fmt.Errorf("invalid virtualization resource content: %w", err)
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return request, fmt.Errorf("virtualization resource content has trailing data")
	}
	return request, nil
}

func (h *virtualizationRuntime) handle(ctx context.Context, intent *Intent) error {
	virtualizationIntentMu.Lock()
	defer virtualizationIntentMu.Unlock()
	if h.Outbox == nil || h.Events == nil || h.Signer == nil || h.Publish == nil || h.Target == "" {
		return fmt.Errorf("bahia-4f10q.1: virtualization canonical outbox is unavailable")
	}
	pubkey, err := h.Signer.GetPublicKey(ctx)
	if err != nil {
		return err
	}
	h.pubkey = pubkey
	receipt, err := h.loadReceipt(intent)
	if err != nil {
		return err
	}
	if receipt != nil {
		return h.resume(ctx, intent, receipt)
	}
	if err := h.checkCoordinateReceipts(intent); err != nil {
		return err
	}
	if intent.Op == "request" {
		return h.handleOperation(ctx, intent)
	}
	return h.handleResource(ctx, intent)
}

func (h *virtualizationRuntime) checkCoordinateReceipts(intent *Intent) error {
	records, err := h.Outbox.ListControlRecords(virtualizationReceiptFamily)
	if err != nil {
		return err
	}
	for _, raw := range records {
		var other virtualizationReceipt
		if err := json.Unmarshal(raw, &other); err != nil {
			return err
		}
		if other.Coordinate != intent.Coordinate {
			continue
		}
		if other.Canonical != nil && other.Canonical.PubKey != h.pubkey {
			return fmt.Errorf("bahia-4f10q.1: virtualization canonical signer changed; explicit operator reassertion required")
		}
		if intent.Op == "request" {
			return &intentStateConflictError{message: "vm-operation coordinate already bound to another intent_id"}
		}
		if other.Canonical == nil {
			return &VirtualizationUncertainError{Coordinate: intent.Coordinate}
		}
		if err := h.requireProof(*other.Canonical); err != nil {
			return err
		}
	}
	return nil
}

func (h *virtualizationRuntime) loadReceipt(intent *Intent) (*virtualizationReceipt, error) {
	raw, err := h.Outbox.GetControlRecord(virtualizationReceiptFamily, intent.IntentID)
	if err != nil || len(raw) == 0 {
		return nil, err
	}
	var receipt virtualizationReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return nil, err
	}
	if receipt.Intent.ID != intent.Event.ID || receipt.Coordinate != intent.Coordinate || receipt.ContentHash != intentContentHash(intent.Content) {
		return nil, &intentReplayConflictError{intentID: intent.IntentID}
	}
	return &receipt, nil
}

func (h *virtualizationRuntime) saveReceipt(r *virtualizationReceipt) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	intentID := virtualizationIntentID(r.Intent)
	if intentID == "" || !r.Intent.CheckID() || !r.Intent.VerifySignature() {
		return fmt.Errorf("bahia-4f10q.1: receipt requires its signed operator intent")
	}
	_, err = h.Outbox.UpdateControlRecord(virtualizationReceiptFamily, intentID, func(previous []byte) ([]byte, error) {
		if len(previous) == 0 {
			return raw, nil
		}
		var old virtualizationReceipt
		if err := json.Unmarshal(previous, &old); err != nil {
			return nil, err
		}
		if old.Intent.ID != r.Intent.ID || old.Coordinate != r.Coordinate || old.ContentHash != r.ContentHash ||
			(old.Effect != nil && !reflect.DeepEqual(old.Effect, r.Effect)) ||
			old.Canonical != nil && (r.Canonical == nil || old.Canonical.ID != r.Canonical.ID) {
			return nil, &intentReplayConflictError{intentID: intentID}
		}
		return raw, nil
	})
	return err
}

func virtualizationIntentID(ev nostr.Event) string {
	for _, tag := range ev.Tags {
		if len(tag) >= 2 && tag[0] == "intent_id" {
			return tag[1]
		}
	}
	return ""
}

func (h *virtualizationRuntime) newReceipt(intent *Intent) *virtualizationReceipt {
	return &virtualizationReceipt{Intent: *intent.Event, Coordinate: intent.Coordinate, ContentHash: intentContentHash(intent.Content)}
}

func (h *virtualizationRuntime) current(ctx context.Context, kind domain.VirtualizationResourceKind, id uuid.UUID) (*virtualizationCanonicalRecord, *nostr.Event, error) {
	family, prefix, topic, _ := virtualizationFamily(kind)
	coordinate := prefix + id.String()
	entry, found, err := h.Outbox.LatestByCoordinate(h.Target, nostr.Kind(kinds.CASControlState), h.pubkey, coordinate)
	if err != nil {
		return nil, nil, err
	}
	var event *nostr.Event
	choose := func(candidate nostr.Event) {
		if event == nil || candidate.CreatedAt > event.CreatedAt ||
			candidate.CreatedAt == event.CreatedAt && bytes.Compare(candidate.ID[:], event.ID[:]) < 0 {
			copy := candidate
			event = &copy
		}
	}
	if found {
		choose(entry.Event)
	}
	filter := nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)}, Authors: []nostr.PubKey{h.pubkey}, Tags: nostr.TagMap{"d": {coordinate}}, Limit: 1}
	for ev := range h.Events.QueryEvents(filter) {
		choose(ev)
		break
	}
	// A proof-bearing receipt is non-prunable. It bridges a crash between the
	// outbox quorum commit and the rebuildable event-cache write, and remains
	// a canonical head after the settled outbox entry is pruned.
	receipts, err := h.Outbox.ListControlRecords(virtualizationReceiptFamily)
	if err != nil {
		return nil, nil, err
	}
	for _, raw := range receipts {
		var receipt virtualizationReceipt
		if err := json.Unmarshal(raw, &receipt); err != nil {
			return nil, nil, err
		}
		if receipt.Coordinate == coordinate && receipt.Canonical != nil && receipt.Canonical.PubKey == h.pubkey {
			choose(*receipt.Canonical)
		}
	}
	if event == nil {
		return nil, nil, nil
	}
	if !event.CheckID() || !event.VerifySignature() || event.PubKey != h.pubkey ||
		!virtualizationTagExactlyOnce(event.Tags, "d", coordinate) ||
		!virtualizationTagExactlyOnce(event.Tags, "domain", kinds.VirtualizationDomain) ||
		!virtualizationTagExactlyOnce(event.Tags, "schema", kinds.CASControlStateSchema) ||
		!virtualizationTagExactlyOnce(event.Tags, "legacy_kind", family.TagValue()) ||
		!virtualizationTagExactlyOnce(event.Tags, "t", topic) {
		return nil, nil, fmt.Errorf("bahia-4f10q.1: malformed canonical virtualization record")
	}
	if err := h.requireProof(*event); err != nil {
		return nil, nil, err
	}
	var record virtualizationCanonicalRecord
	if err := json.Unmarshal([]byte(event.Content), &record); err != nil {
		return nil, nil, err
	}
	intentID, intentErr := uuid.Parse(record.IntentID)
	intentEventID := ""
	for _, tag := range event.Tags {
		if len(tag) >= 2 && tag[0] == "e" {
			intentEventID = tag[1]
		}
	}
	_, eventErr := nostr.IDFromHex(intentEventID)
	if record.ID != id || id.Version() != 7 || record.ResourceKind != kind || record.OrgID == uuid.Nil || record.UpdatedAt.IsZero() ||
		intentErr != nil || intentID.Version() != 7 || eventErr != nil || !virtualizationTagExactlyOnce(event.Tags, "e", intentEventID) || !virtualizationTagExactlyOnce(event.Tags, "org", record.OrgID.String()) ||
		!virtualizationTagExactlyOnce(event.Tags, "intent_id", record.IntentID) ||
		!virtualizationTagExactlyOnce(event.Tags, "deleted", fmt.Sprint(record.Deleted)) ||
		(record.Deleted && len(record.Resource) != 0) || (!record.Deleted && len(record.Resource) == 0) {
		return nil, nil, fmt.Errorf("bahia-4f10q.1: canonical virtualization identity mismatch")
	}
	return &record, event, nil
}

func (h *virtualizationRuntime) requireProof(ev nostr.Event) error {
	proof, found, err := h.Outbox.GetDeliveryProof(ev.ID)
	if err != nil {
		return err
	}
	if !found {
		entry, exists, err := h.Outbox.Get(ev.ID)
		if err != nil {
			return err
		}
		if exists && entry.State == localstore.OutboxFailed {
			return fmt.Errorf("bahia-4f10q.1: canonical outbox delivery abandoned for %s", ev.ID.Hex())
		}
		return ErrVirtualizationPending
	}
	if !proof.ValidFor(ev, h.Target) {
		return fmt.Errorf("bahia-4f10q.1: canonical relay OK proof is invalid")
	}
	return nil
}

func (h *virtualizationRuntime) makeCanonical(ctx context.Context, intent *Intent, record virtualizationCanonicalRecord, previous *nostr.Event) (nostr.Event, error) {
	var family kinds.CPStateFamily
	var topic string
	if record.ResourceKind == domain.VMOperationResource {
		family, topic = kinds.CPStateFamilyVirtualizationOperation, kinds.CPStateTopicVirtualizationOperation
	} else {
		family, _, topic, _ = virtualizationFamily(record.ResourceKind)
	}
	content, err := json.Marshal(record)
	if err != nil {
		return nostr.Event{}, err
	}
	created := nostr.Now()
	if previous != nil && created <= previous.CreatedAt {
		created = previous.CreatedAt + 1
	}
	ev := nostr.Event{Kind: nostr.Kind(kinds.CASControlState), CreatedAt: created, Content: string(content), Tags: nostr.Tags{
		{"d", intent.Coordinate}, {"domain", kinds.VirtualizationDomain}, {"schema", kinds.CASControlStateSchema},
		{"legacy_kind", family.TagValue()}, {"deleted", fmt.Sprint(record.Deleted)}, {"t", topic},
		{"org", record.OrgID.String()}, {"intent_id", intent.IntentID}, {"e", intent.Event.ID.Hex()},
	}}
	if err := SignNostrEvent(ctx, h.Signer, &ev); err != nil {
		return nostr.Event{}, err
	}
	return ev, nil
}

func (h *virtualizationRuntime) publishCanonical(ctx context.Context, intent *Intent, receipt *virtualizationReceipt) error {
	if receipt.Canonical == nil {
		return fmt.Errorf("bahia-4f10q.1: canonical event not staged")
	}
	if err := h.requireProof(*receipt.Canonical); err == nil {
		return nil
	} else if !errors.Is(err, ErrVirtualizationPending) {
		return err
	}
	if err := h.Publish(ctx, *receipt.Canonical); err != nil {
		if proofErr := h.requireProof(*receipt.Canonical); !errors.Is(proofErr, ErrVirtualizationPending) {
			return proofErr
		}
		return fmt.Errorf("%w: %v", ErrVirtualizationPending, err)
	}
	return h.requireProof(*receipt.Canonical)
}

func (h *virtualizationRuntime) resume(ctx context.Context, intent *Intent, receipt *virtualizationReceipt) error {
	if receipt.Canonical != nil {
		return h.publishCanonical(ctx, intent, receipt)
	}
	if receipt.Phase != "prepared" || receipt.Effect == nil {
		return fmt.Errorf("bahia-4f10q.1: incomplete virtualization receipt")
	}
	provider, err := h.provider(ctx, receipt.Effect.Host)
	if err != nil {
		return &VirtualizationUncertainError{Coordinate: intent.Coordinate}
	}
	observed, err := provider.Inspect(ctx, receipt.Effect.VM.Identity)
	if err != nil || observed == nil || observed.Marker == nil || observed.Marker.OperationID != receipt.Effect.Operation.ID ||
		observed.Marker.AppliedGeneration != receipt.Effect.VM.Generation ||
		!reflect.DeepEqual(observed.Marker.VMResourceIdentity, receipt.Effect.VM.Identity) ||
		observed.Ownership != domain.VMOwned || observed.Availability != domain.VMObservationAvailable || observed.Drift != domain.VMDriftInSync ||
		!virtualizationDefineMarkerMatches(receipt.Effect, observed.Marker) ||
		!virtualizationObservedEffect(receipt.Effect.Operation.Kind, observed) {
		return &VirtualizationUncertainError{Coordinate: intent.Coordinate}
	}
	return h.completeEffect(ctx, intent, receipt, observed)
}

func virtualizationObservedEffect(kind domain.VMOperationKind, observed *domain.VMObservation) bool {
	if observed == nil {
		return false
	}
	switch kind {
	case domain.VMOperationDefine:
		return observed.RuntimeState != nil && *observed.RuntimeState != domain.VMRuntimeAbsent
	case domain.VMOperationStart, domain.VMOperationReboot:
		return observed.RuntimeState != nil && *observed.RuntimeState == domain.VMRuntimeRunning
	case domain.VMOperationGracefulStop:
		return observed.RuntimeState != nil && *observed.RuntimeState == domain.VMRuntimeStopped
	default:
		return false
	}
}

func virtualizationDefineMarkerMatches(effect *virtualizationEffectInput, marker *domain.VMOwnershipMarker) bool {
	if effect.Operation.Kind != domain.VMOperationDefine {
		return true
	}
	return marker.ImageDigest == effect.Image.ManifestDigest && marker.ConfigDigest == effect.VM.ConfigDigest
}

func (h *virtualizationRuntime) completeEffect(ctx context.Context, intent *Intent, receipt *virtualizationReceipt, observation *domain.VMObservation) error {
	effect := receipt.Effect
	if intent.Op == "request" {
		completed := time.Now().UTC()
		op := effect.Operation
		op.Phase = domain.VMOperationSucceeded
		op.CompletedAt = &completed
		op.UpdatedAt = completed
		payload, err := json.Marshal(struct {
			Operation   domain.VMOperation    `json:"operation"`
			Observation *domain.VMObservation `json:"observation"`
		}{op, observation})
		if err != nil {
			return err
		}
		record := virtualizationCanonicalRecord{ID: op.ID, OrgID: op.OrgID, ResourceKind: domain.VMOperationResource, UpdatedAt: completed, Resource: payload, IntentID: intent.IntentID}
		ev, err := h.makeCanonical(ctx, intent, record, nil)
		if err != nil {
			return err
		}
		receipt.Canonical = &ev
	} else {
		request, err := decodeVirtualizationResourceRequest(intent.Event.Content)
		if err != nil {
			return err
		}
		old, previous, err := h.current(ctx, domain.PersistentVMResource, request.ID)
		if err != nil {
			return err
		}
		if intent.Op == "create" && old != nil {
			return &revisionConflictError{entityType: "persistent_vm", entityID: request.ID, actual: old.UpdatedAt}
		}
		if intent.Op == "update" && (old == nil || !intent.RevisionMatches(old.UpdatedAt)) {
			return &revisionConflictError{entityType: "persistent_vm", entityID: request.ID}
		}
		if old != nil && old.IntentID == intent.IntentID {
			return fmt.Errorf("bahia-4f10q.1: already applied intent has no receipt")
		}
		payload, err := json.Marshal(effect.VM)
		if err != nil {
			return err
		}
		record := virtualizationCanonicalRecord{ID: effect.VM.ID, OrgID: effect.VM.OrgID, ResourceKind: domain.PersistentVMResource, UpdatedAt: effect.VM.UpdatedAt, Resource: payload, IntentID: intent.IntentID}
		ev, err := h.makeCanonical(ctx, intent, record, previous)
		if err != nil {
			return err
		}
		receipt.Canonical = &ev
	}
	receipt.Phase = "signed"
	if err := h.saveReceipt(receipt); err != nil {
		return err
	}
	return h.publishCanonical(ctx, intent, receipt)
}

func (h *virtualizationRuntime) handleResource(ctx context.Context, intent *Intent) error {
	request, _ := decodeVirtualizationResourceRequest(intent.Event.Content)
	old, prior, err := h.current(ctx, request.ResourceKind, request.ID)
	if err != nil {
		return err
	}
	if old != nil && old.OrgID != intent.OrgID {
		return fmt.Errorf("virtualization resource belongs to another organization")
	}
	if old == nil {
		if intent.Op == "delete" || intent.ExpectedUpdatedAt != nil {
			return &revisionConflictError{entityType: "virtualization", entityID: request.ID}
		}
	} else if old.Deleted || intent.ExpectedUpdatedAt == nil || !intent.RevisionMatches(old.UpdatedAt) {
		return &revisionConflictError{entityType: "virtualization", entityID: request.ID, actual: old.UpdatedAt}
	}
	if intent.Op == "create" && old != nil {
		return &revisionConflictError{entityType: "virtualization", entityID: request.ID, actual: old.UpdatedAt}
	}
	if intent.Op == "update" && old == nil {
		return &revisionConflictError{entityType: "virtualization", entityID: request.ID}
	}
	if old != nil && intent.Op == "update" && request.ResourceKind != domain.PersistentVMResource {
		if err := h.ensureNoPendingReferencedEffects(request.ResourceKind, request.ID); err != nil {
			return err
		}
	}
	updated := time.Now().UTC().Truncate(time.Microsecond)
	if old != nil && !updated.After(old.UpdatedAt) {
		updated = old.UpdatedAt.Add(time.Microsecond)
	}
	record := virtualizationCanonicalRecord{ID: request.ID, OrgID: intent.OrgID, ResourceKind: request.ResourceKind, UpdatedAt: updated, Deleted: intent.Op == "delete", IntentID: intent.IntentID}
	if !record.Deleted {
		resource, effect, err := h.validateDesired(ctx, intent, request, old, updated)
		if err != nil {
			return err
		}
		record.Resource = resource
		if effect != nil {
			return h.executeEffect(ctx, intent, effect)
		}
	} else if err := h.validateRetirement(ctx, request.ResourceKind, request.ID, old); err != nil {
		return err
	}
	ev, err := h.makeCanonical(ctx, intent, record, prior)
	if err != nil {
		return err
	}
	receipt := h.newReceipt(intent)
	receipt.Phase = "signed"
	receipt.Canonical = &ev
	if err := h.saveReceipt(receipt); err != nil {
		return err
	}
	return h.publishCanonical(ctx, intent, receipt)
}

func (h *virtualizationRuntime) ensureNoPendingReferencedEffects(kind domain.VirtualizationResourceKind, id uuid.UUID) error {
	receipts, err := h.Outbox.ListControlRecords(virtualizationReceiptFamily)
	if err != nil {
		return err
	}
	for _, raw := range receipts {
		var r virtualizationReceipt
		if err := json.Unmarshal(raw, &r); err != nil {
			return err
		}
		if r.Effect == nil || (kind != domain.VirtualizationHostResource || r.Effect.VM.HostID != id) &&
			(kind != domain.VMImageResource || r.Effect.VM.ImageID != id) {
			continue
		}
		if r.Canonical == nil {
			return &VirtualizationUncertainError{Coordinate: r.Coordinate}
		}
		if err := h.requireProof(*r.Canonical); err != nil {
			return err
		}
	}
	return nil
}

func (h *virtualizationRuntime) validateRetirement(ctx context.Context, kind domain.VirtualizationResourceKind, id uuid.UUID, old *virtualizationCanonicalRecord) error {
	if old == nil || old.Deleted {
		return fmt.Errorf("bahia-4f10q.1: only a live canonical virtualization resource can be retired")
	}
	if kind == domain.PersistentVMResource {
		var vm domain.PersistentVMDeployment
		if err := json.Unmarshal(old.Resource, &vm); err != nil {
			return err
		}
		if err := h.ensureNoPendingVMOperations(id); err != nil {
			return err
		}
		host, _, err := h.resolveReferences(ctx, vm.HostID, vm.ImageID, vm.OrgID)
		if err != nil {
			return err
		}
		provider, err := h.provider(ctx, host)
		if err != nil {
			return err
		}
		observed, err := provider.Inspect(ctx, vm.Identity)
		if err != nil || observed == nil || observed.RuntimeState == nil || *observed.RuntimeState != domain.VMRuntimeAbsent {
			return fmt.Errorf("bahia-4f10q.1: VM delete tombstone requires provider-proven absence; provider deletion is unsupported")
		}
		return nil
	}
	// HOST/IMAGE can be retired only when the canonical local view has no
	// live VM reference. A pending VM receipt is also a reference.
	receipts, err := h.Outbox.ListControlRecords(virtualizationReceiptFamily)
	if err != nil {
		return err
	}
	for _, raw := range receipts {
		var r virtualizationReceipt
		if err := json.Unmarshal(raw, &r); err != nil {
			return err
		}
		if r.Effect != nil && (kind == domain.VirtualizationHostResource && r.Effect.VM.HostID == id || kind == domain.VMImageResource && r.Effect.VM.ImageID == id) {
			return fmt.Errorf("bahia-4f10q.1: resource has a VM effect receipt; retirement requires explicit operator reconciliation")
		}
	}
	filter := nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)}, Authors: []nostr.PubKey{h.pubkey}, Tags: nostr.TagMap{"t": {kinds.CPStateTopicVirtualizationVM}}}
	for ev := range h.Events.QueryEvents(filter) {
		if err := h.requireProof(ev); err != nil {
			return err
		}
		var vmRecord virtualizationCanonicalRecord
		if err := json.Unmarshal([]byte(ev.Content), &vmRecord); err != nil {
			return err
		}
		if vmRecord.Deleted {
			continue
		}
		var vm domain.PersistentVMDeployment
		if err := json.Unmarshal(vmRecord.Resource, &vm); err != nil {
			return err
		}
		if kind == domain.VirtualizationHostResource && vm.HostID == id || kind == domain.VMImageResource && vm.ImageID == id {
			return fmt.Errorf("bahia-4f10q.1: resource is referenced by a canonical VM")
		}
	}
	return nil
}

func (h *virtualizationRuntime) ensureNoPendingVMOperations(id uuid.UUID) error {
	receipts, err := h.Outbox.ListControlRecords(virtualizationReceiptFamily)
	if err != nil {
		return err
	}
	for _, raw := range receipts {
		var r virtualizationReceipt
		if err := json.Unmarshal(raw, &r); err != nil {
			return err
		}
		if r.Effect == nil || r.Effect.VM.ID != id || !strings.HasPrefix(r.Coordinate, "vm-operation:") {
			continue
		}
		if r.Canonical == nil {
			return &VirtualizationUncertainError{Coordinate: r.Coordinate}
		}
		if err := h.requireProof(*r.Canonical); err != nil {
			return err
		}
	}
	return nil
}

func (h *virtualizationRuntime) ensureVMResourceSettled(id uuid.UUID) error {
	receipts, err := h.Outbox.ListControlRecords(virtualizationReceiptFamily)
	if err != nil {
		return err
	}
	coordinate := "persistent-vm:" + id.String()
	for _, raw := range receipts {
		var r virtualizationReceipt
		if err := json.Unmarshal(raw, &r); err != nil {
			return err
		}
		if r.Coordinate != coordinate {
			continue
		}
		if r.Canonical == nil {
			return &VirtualizationUncertainError{Coordinate: coordinate}
		}
		if err := h.requireProof(*r.Canonical); err != nil {
			return err
		}
	}
	return nil
}

func (h *virtualizationRuntime) validateDesired(ctx context.Context, intent *Intent, request virtualizationResourceRequest, old *virtualizationCanonicalRecord, updated time.Time) (json.RawMessage, *virtualizationEffectInput, error) {
	decode := func(dst any) error {
		d := json.NewDecoder(bytes.NewReader(request.Resource))
		d.DisallowUnknownFields()
		if err := d.Decode(dst); err != nil {
			return err
		}
		var tail any
		if d.Decode(&tail) != io.EOF {
			return fmt.Errorf("trailing resource content")
		}
		return nil
	}
	meta := domain.VirtualizationResourceMeta{SchemaVersion: 1, ID: request.ID, OrgID: intent.OrgID, Generation: 1, CreatedBy: intent.Actor, CreatedAt: updated, UpdatedAt: updated}
	if old != nil {
		var previous domain.VirtualizationResourceMeta
		if err := json.Unmarshal(old.Resource, &previous); err != nil {
			return nil, nil, err
		}
		if previous.ID != request.ID || previous.OrgID != intent.OrgID || previous.Generation < 1 {
			return nil, nil, fmt.Errorf("bahia-4f10q.1: malformed canonical resource metadata")
		}
		meta.Generation = previous.Generation + 1
		meta.CreatedAt = previous.CreatedAt
		meta.CreatedBy = previous.CreatedBy
	}
	marshal := func(v any) (json.RawMessage, error) { return json.Marshal(v) }
	switch request.ResourceKind {
	case domain.VirtualizationHostResource:
		var v domain.VirtualizationHost
		if err := decode(&v); err != nil {
			return nil, nil, err
		}
		if v.VirtualizationResourceMeta != (domain.VirtualizationResourceMeta{}) || v.Observation != nil || v.ObservationCursor != nil {
			return nil, nil, fmt.Errorf("bahia-4f10q.1: host metadata/observation is service owned")
		}
		if v.ExecutionLocation != domain.VMExecutionLocal || !virtualizationPersistentOnly(v.LifecycleClasses) {
			return nil, nil, fmt.Errorf("bahia-4f10q.1: only local persistent virtualization hosts are supported")
		}
		if old != nil {
			var previous domain.VirtualizationHost
			if err := json.Unmarshal(old.Resource, &previous); err != nil {
				return nil, nil, err
			}
			if v.InstallationID != previous.InstallationID || v.Provider != previous.Provider || v.ExecutionLocation != previous.ExecutionLocation || v.TrustPolicyRef != previous.TrustPolicyRef || v.ManagementEndpointRef != previous.ManagementEndpointRef {
				return nil, nil, fmt.Errorf("bahia-4f10q.1: host provider identity and trust binding are immutable")
			}
		}
		v.VirtualizationResourceMeta = meta
		if err := domain.ValidateVirtualizationHost(&v); err != nil {
			return nil, nil, err
		}
		raw, err := marshal(v)
		return raw, nil, err
	case domain.VMImageResource:
		var v domain.VMImage
		if err := decode(&v); err != nil {
			return nil, nil, err
		}
		if v.VirtualizationResourceMeta != (domain.VirtualizationResourceMeta{}) {
			return nil, nil, fmt.Errorf("bahia-4f10q.1: image metadata is service owned")
		}
		if !virtualizationPersistentOnly(v.LifecycleClasses) {
			return nil, nil, fmt.Errorf("bahia-4f10q.1: Loom image lifecycle is unsupported")
		}
		if old != nil {
			var previous domain.VMImage
			if err := json.Unmarshal(old.Resource, &previous); err != nil {
				return nil, nil, err
			}
			if v.ManifestDigest != previous.ManifestDigest || v.ReleaseRef != previous.ReleaseRef || v.Format != previous.Format || v.Architecture != previous.Architecture || v.OS != previous.OS || v.Firmware != previous.Firmware || !reflect.DeepEqual(v.Components, previous.Components) || !reflect.DeepEqual(v.Provenance, previous.Provenance) {
				return nil, nil, fmt.Errorf("bahia-4f10q.1: image artifact identity and provenance are immutable")
			}
		}
		v.VirtualizationResourceMeta = meta
		if err := domain.ValidateVMImage(&v); err != nil {
			return nil, nil, err
		}
		raw, err := marshal(v)
		return raw, nil, err
	case domain.PersistentVMResource:
		var v domain.PersistentVMDeployment
		if err := decode(&v); err != nil {
			return nil, nil, err
		}
		if v.VirtualizationResourceMeta != (domain.VirtualizationResourceMeta{}) || v.Observation != nil || v.ObservationCursor != nil || v.BootstrapApplied || len(v.Bootstrap) > 0 || v.ServiceID != nil || v.EnvironmentID != nil || v.DeploymentUnitID != nil || v.DesiredPower != domain.VMDesiredStopped {
			return nil, nil, fmt.Errorf("bahia-4f10q.1: VM bootstrap, Loom/service binding, observations and running desired state are unsupported")
		}
		if old != nil {
			if err := h.ensureNoPendingVMOperations(request.ID); err != nil {
				return nil, nil, err
			}
			var previous domain.PersistentVMDeployment
			if err := json.Unmarshal(old.Resource, &previous); err != nil {
				return nil, nil, err
			}
			if !reflect.DeepEqual(v.Identity, previous.Identity) || v.HostID != previous.HostID || v.Provider != previous.Provider || v.StoragePoolRef != previous.StoragePoolRef {
				return nil, nil, fmt.Errorf("bahia-4f10q.1: VM provider identity, host and storage pool are immutable")
			}
		}
		v.VirtualizationResourceMeta = meta
		if err := domain.ValidatePersistentVMDeployment(&v); err != nil {
			return nil, nil, err
		}
		host, image, err := h.resolveReferences(ctx, v.HostID, v.ImageID, intent.OrgID)
		if err != nil {
			return nil, nil, err
		}
		if err := domain.ValidateVMDeploymentReferences(&v, &host, &image); err != nil {
			return nil, nil, err
		}
		if h.Provider == nil && h.ProviderForHost == nil {
			return nil, nil, fmt.Errorf("bahia-4f10q.1: persistent VM provider is not configured")
		}
		op := h.providerOperation(intent, v, domain.VMOperationDefine, updated)
		effect := &virtualizationEffectInput{Host: host, Image: image, VM: v, Operation: op}
		raw, err := marshal(v)
		return raw, effect, err
	}
	return nil, nil, fmt.Errorf("bahia-4f10q.1: unsupported resource kind")
}

func virtualizationPersistentOnly(classes []domain.VMLifecycleClass) bool {
	if len(classes) != 1 {
		return false
	}
	return classes[0] == domain.VMLifecyclePersistent
}

func (h *virtualizationRuntime) resolveReferences(ctx context.Context, hostID, imageID, org uuid.UUID) (domain.VirtualizationHost, domain.VMImage, error) {
	var host domain.VirtualizationHost
	var image domain.VMImage
	hr, _, err := h.current(ctx, domain.VirtualizationHostResource, hostID)
	if err != nil {
		return host, image, err
	}
	ir, _, err := h.current(ctx, domain.VMImageResource, imageID)
	if err != nil {
		return host, image, err
	}
	if hr == nil || ir == nil || hr.Deleted || ir.Deleted || hr.OrgID != org || ir.OrgID != org {
		return host, image, fmt.Errorf("bahia-4f10q.1: canonical host/image reference missing")
	}
	if err := json.Unmarshal(hr.Resource, &host); err != nil {
		return host, image, err
	}
	if err := json.Unmarshal(ir.Resource, &image); err != nil {
		return host, image, err
	}
	return host, image, nil
}

func (h *virtualizationRuntime) providerOperation(intent *Intent, vm domain.PersistentVMDeployment, kind domain.VMOperationKind, at time.Time) domain.VMOperation {
	id := uuid.MustParse(intent.IntentID)
	if intent.Op == "request" {
		var req virtualizationOperationRequest
		_ = json.Unmarshal([]byte(intent.Event.Content), &req)
		id = req.OperationID
	}
	hash := sha256.Sum256([]byte(intent.Event.Content))
	deadline := at.Add(2 * time.Minute)
	return domain.VMOperation{VirtualizationResourceMeta: domain.VirtualizationResourceMeta{SchemaVersion: 1, ID: id, OrgID: intent.OrgID, Generation: 1, CreatedBy: intent.Actor, CreatedAt: at, UpdatedAt: at}, LifecycleClass: domain.VMLifecyclePersistent, ResourceID: vm.ID, ResourceGeneration: vm.Generation, ExpectedGeneration: vm.Generation, IdempotencyKey: intent.IntentID, RequestHash: fmt.Sprintf("sha256:%x", hash), Actor: intent.Actor, Reason: "operator-signed virtualization intent", Kind: kind, Phase: domain.VMOperationExecuting, RequiredTier: domain.VMApprovalOperator, ProviderCorrelationID: id, Deadline: deadline}
}

func (h *virtualizationRuntime) handleOperation(ctx context.Context, intent *Intent) error {
	var request virtualizationOperationRequest
	if err := json.Unmarshal([]byte(intent.Event.Content), &request); err != nil {
		return err
	}
	if err := h.operationCoordinateVacant(intent.Coordinate); err != nil {
		return err
	}
	record, _, err := h.current(ctx, domain.PersistentVMResource, request.ResourceID)
	if err != nil {
		return err
	}
	if record == nil || record.Deleted || record.OrgID != intent.OrgID {
		return fmt.Errorf("bahia-4f10q.1: vm-operation target has no canonical VM record")
	}
	if err := h.ensureNoPendingVMOperations(request.ResourceID); err != nil {
		return err
	}
	if err := h.ensureVMResourceSettled(request.ResourceID); err != nil {
		return err
	}
	var vm domain.PersistentVMDeployment
	if err := json.Unmarshal(record.Resource, &vm); err != nil {
		return err
	}
	if vm.Generation != request.ExpectedGeneration {
		return &revisionConflictError{entityType: "persistent_vm", entityID: vm.ID, actual: record.UpdatedAt}
	}
	if intent.ExpectedUpdatedAt != nil && !intent.RevisionMatches(record.UpdatedAt) {
		return &revisionConflictError{entityType: "persistent_vm", entityID: vm.ID, actual: record.UpdatedAt}
	}
	host, image, err := h.resolveReferences(ctx, vm.HostID, vm.ImageID, intent.OrgID)
	if err != nil {
		return err
	}
	if err := domain.ValidateVMDeploymentReferences(&vm, &host, &image); err != nil {
		return err
	}
	if h.Provider == nil && h.ProviderForHost == nil {
		return fmt.Errorf("bahia-4f10q.1: persistent VM provider is not configured")
	}
	at := time.Now().UTC()
	op := h.providerOperation(intent, vm, request.Action, at)
	op.Reason = request.Reason
	op.ExpectedGeneration = request.ExpectedGeneration
	return h.executeEffect(ctx, intent, &virtualizationEffectInput{Host: host, Image: image, VM: vm, Operation: op})
}

func (h *virtualizationRuntime) operationCoordinateVacant(coordinate string) error {
	entry, found, err := h.Outbox.LatestByCoordinate(h.Target, nostr.Kind(kinds.CASControlState), h.pubkey, coordinate)
	if err != nil {
		return err
	}
	if found {
		if err := h.requireProof(entry.Event); err != nil {
			return err
		}
		return &intentStateConflictError{message: "vm-operation coordinate already has a canonical outcome"}
	}
	filter := nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)}, Authors: []nostr.PubKey{h.pubkey}, Tags: nostr.TagMap{"d": {coordinate}}, Limit: 1}
	for range h.Events.QueryEvents(filter) {
		return &intentStateConflictError{message: "vm-operation coordinate already has a canonical outcome"}
	}
	return nil
}

func (h *virtualizationRuntime) executeEffect(ctx context.Context, intent *Intent, effect *virtualizationEffectInput) error {
	if err := domain.ValidateVMOperation(&effect.Operation); err != nil {
		return err
	}
	provider, err := h.provider(ctx, effect.Host)
	if err != nil {
		return err
	}
	receipt := h.newReceipt(intent)
	receipt.Phase = "prepared"
	receipt.Effect = effect
	if err := h.saveReceipt(receipt); err != nil {
		return err
	}
	result, err := provider.Execute(ctx, domain.VMProviderOperation{Host: effect.Host, Image: effect.Image, Deployment: effect.VM, Operation: effect.Operation})
	if err != nil || result == nil || !result.Confirmed || result.OperationID != effect.Operation.ID ||
		result.Observation == nil || result.Observation.Marker == nil ||
		result.Observation.Marker.OperationID != effect.Operation.ID ||
		result.Observation.Marker.AppliedGeneration != effect.VM.Generation ||
		!reflect.DeepEqual(result.Observation.Marker.VMResourceIdentity, effect.VM.Identity) ||
		result.Observation.Ownership != domain.VMOwned || result.Observation.Availability != domain.VMObservationAvailable || result.Observation.Drift != domain.VMDriftInSync ||
		!virtualizationDefineMarkerMatches(effect, result.Observation.Marker) ||
		!virtualizationObservedEffect(effect.Operation.Kind, result.Observation) {
		return &VirtualizationUncertainError{Coordinate: intent.Coordinate}
	}
	return h.completeEffect(ctx, intent, receipt, result.Observation)
}

func (h *virtualizationRuntime) provider(ctx context.Context, host domain.VirtualizationHost) (domain.PersistentVMProvider, error) {
	if h.ProviderForHost != nil {
		return h.ProviderForHost(ctx, host)
	}
	if h.Provider != nil {
		return h.Provider, nil
	}
	return nil, fmt.Errorf("bahia-4f10q.1: persistent VM provider is not configured")
}

// Recover replays only exact signed operator intents bound in local receipts.
// The processor re-authorizes them; this does not synthesize intent from SQL.
func (h *VirtualizationIntentHandler) Recover(ctx context.Context, process func(context.Context, *nostr.Event) error) error {
	if h == nil || h.runtime == nil || h.runtime.Outbox == nil {
		return nil
	}
	records, err := h.runtime.Outbox.ListControlRecords(virtualizationReceiptFamily)
	if err != nil {
		return err
	}
	var failures []error
	for _, raw := range records {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		var receipt virtualizationReceipt
		if err := json.Unmarshal(raw, &receipt); err != nil {
			failures = append(failures, err)
			continue
		}
		if !receipt.Intent.CheckID() || !receipt.Intent.VerifySignature() {
			failures = append(failures, fmt.Errorf("bahia-4f10q.1: corrupt virtualization receipt intent"))
			continue
		}
		if err := process(ctx, &receipt.Intent); err != nil && !errors.Is(err, ErrVirtualizationPending) {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
