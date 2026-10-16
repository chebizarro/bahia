package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/kinds"
	"go.uber.org/zap"
)

const identityIncidentRecordFamily = "identity-incident-containment-v1"

type identityIncidentPool interface {
	URLs() []string
	SubscribeAllWithEOSE(context.Context, []nostr.Filter) (*nostradapter.MergedSubscription, error)
}

type incidentContainment interface {
	ActivateRuntimeKillSwitch(string) error
	ReleaseRuntimeKillSwitch() error
	RuntimeKillSwitch() (bool, string)
}

type identityIncidentContent struct {
	IncidentID       string   `json:"incident_id"`
	OldPubkey        string   `json:"old_pubkey"`
	DeclaredAt       string   `json:"declared_at"`
	ReasonCode       string   `json:"reason_code"`
	ControlledRelays []string `json:"controlled_relays"`
	Phase            string   `json:"phase"`
	Aborted          *bool    `json:"aborted"`
}

type identityIncidentHead struct {
	Event       nostr.Event `json:"event"`
	SeenCutover bool        `json:"seen_cutover"`
}

// IdentityIncidents consumes only operator-root-authored declarations for this
// daemon's old service pubkey. The outbox control record is durable state;
// the inbound event cache is retained as evidence but is not the boot gate.
// A declaration contains immediately, while an abort can release only after
// every relay has supplied a real EOSE for the current subscription.
type IdentityIncidents struct {
	pool        identityIncidentPool
	store       *localstore.Store
	records     *localstore.Outbox
	containment incidentContainment
	logger      *zap.Logger
	authors     []nostr.PubKey
	old         nostr.PubKey

	mu       sync.RWMutex
	heads    map[string]identityIncidentHead
	caughtUp bool
}

func NewIdentityIncidents(pool identityIncidentPool, store *localstore.Store, records *localstore.Outbox, containment incidentContainment, trustedAuthors []string, oldPubkey string, logger *zap.Logger) (*IdentityIncidents, error) {
	old, err := nostr.PubKeyFromHex(oldPubkey)
	if err != nil || old == nostr.ZeroPK || old.Hex() != oldPubkey {
		return nil, errors.New("identity incidents require a canonical service pubkey")
	}
	if pool == nil || store == nil || records == nil || containment == nil {
		return nil, errors.New("identity incidents require relay, local stores and containment")
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	w := &IdentityIncidents{pool: pool, store: store, records: records, containment: containment, logger: logger.Named("identity-incidents"), old: old, heads: make(map[string]identityIncidentHead)}
	for _, raw := range trustedAuthors {
		key, err := nostr.PubKeyFromHex(raw)
		if err != nil || key == nostr.ZeroPK || key.Hex() != raw {
			return nil, errors.New("identity incident trust root is invalid")
		}
		if !slices.Contains(w.authors, key) {
			w.authors = append(w.authors, key)
		}
	}
	if len(w.authors) == 0 {
		return nil, errors.New("identity incidents require an operator trust root")
	}
	return w, nil
}

func (w *IdentityIncidents) Name() string   { return "identity-incidents" }
func (w *IdentityIncidents) CaughtUp() bool { w.mu.RLock(); defer w.mu.RUnlock(); return w.caughtUp }

func (w *IdentityIncidents) filter() nostr.Filter {
	// Relays index only single-letter tags. "old" is verified locally.
	return nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)}, Authors: slices.Clone(w.authors), Tags: nostr.TagMap{"t": {kinds.CPStateTopicIdentityIncident}}}
}

func identityIncidentTag(tags nostr.Tags, name string) (string, bool) {
	var value string
	found := false
	for _, tag := range tags {
		if len(tag) == 0 || tag[0] != name {
			continue
		}
		if len(tag) != 2 || found {
			return "", false
		}
		value, found = tag[1], true
	}
	return value, found && value != ""
}

func incidentPhaseRank(phase string) int {
	for i, name := range []string{"declare", "contain", "inventory", "provision-new-identity", "cut-activate-trust", "reassert-canonical-state", "rotate-external-secrets", "delete-and-purge", "verify", "retire-old-identity"} {
		if phase == name {
			return i + 1
		}
	}
	if phase == "abort" {
		return 0
	}
	return -1
}

func (w *IdentityIncidents) validate(ev *nostr.Event) (identityIncidentContent, string, error) {
	var content identityIncidentContent
	if ev == nil || ev.Kind != nostr.Kind(kinds.CASControlState) || !ev.CheckID() || !ev.VerifySignature() {
		return content, "", errors.New("incident event has invalid kind, id or signature")
	}
	if err := nostradapter.ValidateInboundEvent(ev, time.Now().UTC(), nostradapter.InboundEventMaxFutureSkew); err != nil {
		return content, "", fmt.Errorf("incident event is invalid: %w", err)
	}
	if !slices.Contains(w.authors, ev.PubKey) {
		return content, "", errors.New("incident author is not an operator root")
	}
	wanted := map[string]string{"t": kinds.CPStateTopicIdentityIncident, "domain": "identity", "schema": "bahia.identity-incident.v1", "legacy_kind": kinds.CPStateFamilyIdentityIncident.TagValue(), "old": w.old.Hex(), "deleted": "false"}
	for name, expected := range wanted {
		value, ok := identityIncidentTag(ev.Tags, name)
		if !ok || value != expected {
			return content, "", fmt.Errorf("incident %s tag is invalid", name)
		}
	}
	coordinate, ok := identityIncidentTag(ev.Tags, "d")
	if !ok || !strings.HasPrefix(coordinate, "identity-incident:") {
		return content, "", errors.New("incident d tag is invalid")
	}
	id := strings.TrimPrefix(coordinate, "identity-incident:")
	parsed, err := uuid.Parse(id)
	if err != nil || parsed.Version() != 7 || parsed.String() != id {
		return content, "", errors.New("incident id must be UUIDv7")
	}
	decoder := json.NewDecoder(strings.NewReader(ev.Content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&content); err != nil {
		return content, "", errors.New("incident content is invalid JSON")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return content, "", errors.New("incident content has trailing data")
	}
	if content.IncidentID != id || content.OldPubkey != w.old.Hex() || content.Aborted == nil || content.ControlledRelays == nil {
		return content, "", errors.New("incident content identity or aborted flag is invalid")
	}
	if _, err := time.Parse(time.RFC3339, content.DeclaredAt); err != nil {
		return content, "", errors.New("incident declared_at is invalid")
	}
	if len(content.ReasonCode) == 0 || len(content.ReasonCode) > 64 {
		return content, "", errors.New("incident reason_code is invalid")
	}
	for _, ch := range content.ReasonCode {
		if ch < 'a' || ch > 'z' {
			if ch < '0' || ch > '9' {
				if ch != '-' && ch != '_' {
					return content, "", errors.New("incident reason_code is invalid")
				}
			}
		}
	}
	rank := incidentPhaseRank(content.Phase)
	if rank < 0 || content.Phase == "abort" && !*content.Aborted || *content.Aborted && rank >= incidentPhaseRank("cut-activate-trust") {
		return content, "", errors.New("incident phase or abort is invalid")
	}
	return content, coordinate, nil
}

// Load restores accepted heads before the daemon starts background work. A
// malformed durable head activates containment instead of trusting absence.
func (w *IdentityIncidents) Load() error {
	rawHeads, err := w.records.ControlRecordsByID(identityIncidentRecordFamily)
	if err != nil {
		_ = w.containment.ActivateRuntimeKillSwitch("invalid-state")
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for key, raw := range rawHeads {
		var head identityIncidentHead
		if json.Unmarshal(raw, &head) != nil {
			_ = w.containment.ActivateRuntimeKillSwitch("invalid-state")
			return errors.New("stored incident head is malformed")
		}
		content, coordinate, err := w.validate(&head.Event)
		if err != nil {
			_ = w.containment.ActivateRuntimeKillSwitch("invalid-state")
			return errors.New("stored incident head failed verification")
		}
		if key != coordinate {
			_ = w.containment.ActivateRuntimeKillSwitch("invalid-state")
			return errors.New("stored incident coordinate is malformed")
		}
		if head.SeenCutover && *content.Aborted {
			_ = w.containment.ActivateRuntimeKillSwitch("invalid-state")
			return errors.New("stored incident abort follows cutover")
		}
		w.heads[coordinate] = head
	}
	return w.reconcileLocked()
}

// Apply validates and deduplicates before changing durable state. Containment
// is activated before any other write so even a storage failure leaves this
// running process unable to publish or sign.
func (w *IdentityIncidents) Apply(ev *nostr.Event) error {
	content, coordinate, err := w.validate(ev)
	if err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	prior, exists := w.heads[coordinate]
	if exists {
		if prior.Event.ID == ev.ID {
			return nil
		}
		if prior.Event.CreatedAt > ev.CreatedAt || prior.Event.CreatedAt == ev.CreatedAt && prior.Event.ID.Hex() < ev.ID.Hex() {
			return nil
		}
		var oldContent identityIncidentContent
		_ = json.Unmarshal([]byte(prior.Event.Content), &oldContent)
		if oldContent.Aborted != nil && *oldContent.Aborted {
			return errors.New("incident abort is terminal")
		}
		if prior.SeenCutover && *content.Aborted {
			return errors.New("incident cannot abort after cutover")
		}
		if !*content.Aborted && incidentPhaseRank(content.Phase) < incidentPhaseRank(oldContent.Phase) {
			return errors.New("incident phase regressed")
		}
	}
	var activationErr error
	if !*content.Aborted {
		activationErr = w.containment.ActivateRuntimeKillSwitch(content.IncidentID)
	}
	head := identityIncidentHead{Event: *ev, SeenCutover: exists && prior.SeenCutover || incidentPhaseRank(content.Phase) >= incidentPhaseRank("cut-activate-trust")}
	raw, err := json.Marshal(head)
	if err != nil {
		return err
	}
	if err := w.records.PutControlRecord(identityIncidentRecordFamily, coordinate, raw); err != nil {
		return fmt.Errorf("persist incident head: %w", err)
	}
	w.heads[coordinate] = head
	if _, err := w.store.SaveEvent(*ev); err != nil {
		w.logger.Warn("retain incident event failed", zap.String("event_id", ev.ID.Hex()), zap.Error(err))
	}
	if *content.Aborted {
		return w.reconcileLocked()
	}
	w.logger.Error("service identity contained", zap.String("incident_id", content.IncidentID))
	if activationErr != nil {
		return fmt.Errorf("persist incident containment marker: %w", activationErr)
	}
	return nil
}

func (w *IdentityIncidents) reconcileLocked() error {
	for _, head := range w.heads {
		var content identityIncidentContent
		_ = json.Unmarshal([]byte(head.Event.Content), &content)
		if content.Aborted != nil && !*content.Aborted {
			return w.containment.ActivateRuntimeKillSwitch(content.IncidentID)
		}
	}
	if !w.caughtUp {
		return nil
	}
	active, id := w.containment.RuntimeKillSwitch()
	if !active {
		return nil
	}
	for _, head := range w.heads {
		var content identityIncidentContent
		_ = json.Unmarshal([]byte(head.Event.Content), &content)
		if content.IncidentID == id && content.Aborted != nil && *content.Aborted && !head.SeenCutover {
			return w.containment.ReleaseRuntimeKillSwitch()
		}
	}
	return nil
}

func (w *IdentityIncidents) Run(ctx context.Context) error {
	if err := w.Load(); err != nil {
		w.logger.Error("incident state invalid; containment remains active", zap.Error(err))
	}
	backoff := nostradapter.DefaultBackoff()
	for {
		err := w.subscribe(ctx)
		if ctx.Err() != nil {
			return nil
		}
		w.logger.Warn("incident subscription ended; abort cannot lift until catch-up", zap.Error(err))
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff.Next()):
		}
	}
}

func (w *IdentityIncidents) subscribe(ctx context.Context) error {
	w.mu.Lock()
	w.caughtUp = false
	w.mu.Unlock()
	defer func() { w.mu.Lock(); w.caughtUp = false; w.mu.Unlock() }()
	merged, err := w.pool.SubscribeAllWithEOSE(ctx, []nostr.Filter{w.filter()})
	if err != nil {
		return err
	}
	defer merged.Close()
	eose := make(map[string]bool)
	allStored := false
	markCaughtUp := func() error {
		if !allStored || !merged.AllRelaysReachedEOSE() {
			return nil
		}
		urls := w.pool.URLs()
		if len(urls) == 0 {
			return nil
		}
		for _, url := range urls {
			if !eose[url] {
				return nil
			}
		}
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.caughtUp {
			return nil
		}
		w.caughtUp = true
		if err := w.reconcileLocked(); err != nil {
			w.caughtUp = false
			return err
		}
		return nil
	}
	events, eoses, closed, done := merged.Events, merged.RelayEOSE, merged.Closed, merged.EndOfStoredEvents
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-events:
			if !ok {
				return errors.New("incident event subscription closed")
			}
			if err := w.Apply(ev); err != nil {
				w.logger.Warn("dropping invalid incident declaration", zap.Error(err))
			}
		case info, ok := <-eoses:
			if !ok {
				return errors.New("incident EOSE channel closed")
			}
			eose[info.RelayURL] = true
			if err := markCaughtUp(); err != nil {
				return err
			}
		case _, ok := <-closed:
			if !ok {
				closed = nil
				continue
			}
			w.mu.Lock()
			w.caughtUp = false
			w.mu.Unlock()
			return errors.New("incident subscription closed by relay")
		case <-done:
			done = nil
			allStored = true
			if err := markCaughtUp(); err != nil {
				return err
			}
		}
	}
}
