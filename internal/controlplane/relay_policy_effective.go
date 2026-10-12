package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/repository"
)

// EffectiveControlPlaneRelays are the control-plane relay lists in force
// under the canonical relay settings state: each non-empty canonical list
// governs, and an empty one leaves the static (bootstrap) list from nostr in
// force. ContextVM falls back to the canonical browser relays before the
// static list, as the static ContextVM policy falls back to browser relays.
func EffectiveControlPlaneRelays(static config.NostrConfig, state RelayPolicyState) config.ControlPlaneRelays {
	return config.ControlPlaneRelays{
		Service:   firstNonEmptyRelayList(state.ServiceRelays, static.ServiceRelayPolicyRelays()),
		ContextVM: firstNonEmptyRelayList(state.ContextVMRelays, state.BrowserRelays, static.ContextVMRelayPolicyRelays()),
		Browser:   firstNonEmptyRelayList(state.BrowserRelays, static.BrowserRelayPolicyRelays()),
	}
}

// ValidateRelayPolicy checks canonical relay settings against the daemon's
// relay guard: the control-plane lists they put in force
// (nostr.ValidateControlPlaneRelays: controlled relays, no public relay,
// publish quorum) and their NIP-34 and DM relay lists (public relays only
// when nostr.allowed_public_relays names them).
func ValidateRelayPolicy(static config.NostrConfig, state RelayPolicyState) error {
	if err := static.ValidateControlPlaneRelays("canonical relay settings", EffectiveControlPlaneRelays(static, state)); err != nil {
		return err
	}
	if err := static.ValidatePublicRelayUse("canonical relay settings nip34_relays", state.NIP34Relays); err != nil {
		return err
	}
	for i, list := range state.DMRelayLists {
		if err := static.ValidatePublicRelayUse(fmt.Sprintf("canonical relay settings dm_relay_lists[%d].relays", i), list.Relays); err != nil {
			return err
		}
	}
	return nil
}

func firstNonEmptyRelayList(lists ...[]string) []string {
	for _, list := range lists {
		if len(list) > 0 {
			return cloneStrings(list)
		}
	}
	return nil
}

// RelayPolicyRecordStore is the daemon's durable local record store
// (localstore.Outbox control records) as the relay policy projection uses it.
type RelayPolicyRecordStore interface {
	GetControlRecord(family, id string) ([]byte, error)
	PutControlRecord(family, id string, value []byte) error
}

const relayPolicyProjectionRecordFamily = "relay-policy-projection"

// LocalRelayPolicyProjectionStore is the relay-policy projection without
// PostgreSQL. Like the PostgreSQL projection it durably keeps the last
// promoted head per author, as a local control record, and orders candidates
// with repository.RelayPolicyProjectionShouldReplace. The hydrator promotes
// only settings the daemon accepts, so a restart restores the last accepted
// settings even when a refused event is the newest on the relays.
type LocalRelayPolicyProjectionStore struct {
	records RelayPolicyRecordStore
	mu      sync.Mutex
	// confirmed holds authors whose head a relay served in this process; a
	// head restored from the record is unconfirmed until then, as with
	// the PostgreSQL projection.
	confirmed map[string]bool
}

// NewLocalRelayPolicyProjectionStore returns a projection store over records.
func NewLocalRelayPolicyProjectionStore(records RelayPolicyRecordStore) *LocalRelayPolicyProjectionStore {
	return &LocalRelayPolicyProjectionStore{records: records, confirmed: make(map[string]bool)}
}

func (s *LocalRelayPolicyProjectionStore) Get(_ context.Context, authorPubkey string) (*repository.RelayPolicyProjection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getLocked(normalizeAuthor(authorPubkey))
}

func (s *LocalRelayPolicyProjectionStore) getLocked(author string) (*repository.RelayPolicyProjection, error) {
	raw, err := s.records.GetControlRecord(relayPolicyProjectionRecordFamily, author)
	if err != nil {
		return nil, fmt.Errorf("reading local relay policy projection: %w", err)
	}
	if len(raw) == 0 {
		return nil, nil
	}
	var projection repository.RelayPolicyProjection
	if err := json.Unmarshal(raw, &projection); err != nil {
		return nil, fmt.Errorf("decoding local relay policy projection: %w", err)
	}
	if !s.confirmed[author] {
		projection.RelayConfirmedAt = nil
	}
	return &projection, nil
}

func (s *LocalRelayPolicyProjectionStore) putLocked(projection *repository.RelayPolicyProjection) error {
	raw, err := json.Marshal(projection)
	if err != nil {
		return err
	}
	return s.records.PutControlRecord(relayPolicyProjectionRecordFamily, normalizeAuthor(projection.AuthorPubkey), raw)
}

func (s *LocalRelayPolicyProjectionStore) Promote(_ context.Context, candidate repository.RelayPolicyProjection) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.getLocked(normalizeAuthor(candidate.AuthorPubkey))
	if err != nil {
		return false, err
	}
	if current != nil && !repository.RelayPolicyProjectionShouldReplace(*current, candidate) {
		return false, nil
	}
	confirmedAt := candidate.EventAcceptedAt.UTC()
	promoted := cloneProjection(&candidate)
	if current != nil && current.EventID == candidate.EventID {
		promoted = cloneProjection(current)
		promoted.SourceRelay = candidate.SourceRelay
		promoted.LastSyncAt = candidate.LastSyncAt
	}
	promoted.RelayConfirmedAt = &confirmedAt
	if err := s.putLocked(promoted); err != nil {
		return false, fmt.Errorf("storing local relay policy projection: %w", err)
	}
	s.confirmed[normalizeAuthor(candidate.AuthorPubkey)] = true
	return true, nil
}

// MarkSynced records a completed catch-up. A head restored after a restart
// stays unconfirmed until a relay serves its event again (Promote).
func (s *LocalRelayPolicyProjectionStore) MarkSynced(_ context.Context, authorPubkey string, syncedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	head, err := s.getLocked(normalizeAuthor(authorPubkey))
	if err != nil || head == nil || !syncedAt.After(head.LastSyncAt) {
		return err
	}
	head.LastSyncAt = syncedAt
	return s.putLocked(head)
}

func normalizeAuthor(pubkey string) string {
	return strings.ToLower(strings.TrimSpace(pubkey))
}

func cloneProjection(in *repository.RelayPolicyProjection) *repository.RelayPolicyProjection {
	if in == nil {
		return nil
	}
	out := *in
	out.CanonicalPayload = append([]byte(nil), in.CanonicalPayload...)
	return &out
}
