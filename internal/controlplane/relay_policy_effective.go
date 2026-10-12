package controlplane

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/kinds"
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

func firstNonEmptyRelayList(lists ...[]string) []string {
	for _, list := range lists {
		if len(list) > 0 {
			return cloneStrings(list)
		}
	}
	return nil
}

// RelaySettingsEventStore is the daemon's local event store as the relay
// settings authority uses it: the latest canonical relay-settings event and
// durable storage for each one promoted.
type RelaySettingsEventStore interface {
	QueryEvents(nostr.Filter) iter.Seq[nostr.Event]
	SaveEvent(nostr.Event) (bool, error)
}

// LocalRelayPolicyProjectionStore is the relay-policy projection without
// PostgreSQL: the head lives in memory and is restored from the latest
// valid relay-settings event in the local event store, which the hydrator
// writes every promoted event to. It orders candidates exactly as the
// PostgreSQL projection does (repository.RelayPolicyProjectionShouldReplace).
type LocalRelayPolicyProjectionStore struct {
	events RelaySettingsEventStore

	mu     sync.Mutex
	heads  map[string]*repository.RelayPolicyProjection
	loaded map[string]bool
}

// NewLocalRelayPolicyProjectionStore returns a projection store restored from
// events.
func NewLocalRelayPolicyProjectionStore(events RelaySettingsEventStore) *LocalRelayPolicyProjectionStore {
	return &LocalRelayPolicyProjectionStore{
		events: events,
		heads:  make(map[string]*repository.RelayPolicyProjection),
		loaded: make(map[string]bool),
	}
}

func (s *LocalRelayPolicyProjectionStore) Get(_ context.Context, authorPubkey string) (*repository.RelayPolicyProjection, error) {
	author := strings.ToLower(strings.TrimSpace(authorPubkey))
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.loaded[author] {
		head, err := s.restoreLocked(author)
		if err != nil {
			return nil, err
		}
		s.heads[author] = head
		s.loaded[author] = true
	}
	return cloneProjection(s.heads[author]), nil
}

// restoreLocked rebuilds the head from the newest valid relay-settings event
// the local store holds for author. Restored heads are not relay-confirmed.
func (s *LocalRelayPolicyProjectionStore) restoreLocked(author string) (*repository.RelayPolicyProjection, error) {
	if s.events == nil {
		return nil, nil
	}
	pubkey, err := nostr.PubKeyFromHex(author)
	if err != nil {
		return nil, fmt.Errorf("relay policy projection author: %w", err)
	}
	var head *repository.RelayPolicyProjection
	for ev := range s.events.QueryEvents(nostr.Filter{
		Kinds:   []nostr.Kind{kinds.CASControlState},
		Authors: []nostr.PubKey{pubkey},
		Tags:    nostr.TagMap{kinds.CASControlStateTagD: []string{RelaySettingsDTag}},
	}) {
		if !ev.CheckID() || !ev.VerifySignature() {
			continue
		}
		state, err := relayPolicyStateFromCanonicalEvent(&ev, author)
		if err != nil {
			continue
		}
		payload, hash, err := canonicalRelayPolicyPayload(*state)
		if err != nil {
			continue
		}
		createdAt := ev.CreatedAt.Time().UTC()
		candidate := repository.RelayPolicyProjection{
			AuthorPubkey: author, EventID: ev.ID.Hex(), EventCreatedAt: createdAt, EventAcceptedAt: createdAt,
			Schema: RelaySettingsSchema, CanonicalPayload: payload, PayloadHash: hash,
		}
		if head == nil || repository.RelayPolicyProjectionShouldReplace(*head, candidate) {
			head = &candidate
		}
	}
	return head, nil
}

func (s *LocalRelayPolicyProjectionStore) Promote(ctx context.Context, candidate repository.RelayPolicyProjection) (bool, error) {
	author := strings.ToLower(strings.TrimSpace(candidate.AuthorPubkey))
	if _, err := s.Get(ctx, author); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	confirmedAt := candidate.EventAcceptedAt.UTC()
	current := s.heads[author]
	if current != nil && !repository.RelayPolicyProjectionShouldReplace(*current, candidate) {
		return false, nil
	}
	promoted := cloneProjection(&candidate)
	if current != nil && current.EventID == candidate.EventID {
		promoted = cloneProjection(current)
		promoted.SourceRelay = candidate.SourceRelay
		promoted.LastSyncAt = candidate.LastSyncAt
	}
	promoted.RelayConfirmedAt = &confirmedAt
	s.heads[author] = promoted
	return true, nil
}

func (s *LocalRelayPolicyProjectionStore) MarkSynced(_ context.Context, authorPubkey string, syncedAt time.Time) error {
	author := strings.ToLower(strings.TrimSpace(authorPubkey))
	s.mu.Lock()
	defer s.mu.Unlock()
	if head := s.heads[author]; head != nil && syncedAt.After(head.LastSyncAt) {
		head.LastSyncAt = syncedAt
	}
	return nil
}

func cloneProjection(in *repository.RelayPolicyProjection) *repository.RelayPolicyProjection {
	if in == nil {
		return nil
	}
	out := *in
	out.CanonicalPayload = append([]byte(nil), in.CanonicalPayload...)
	return &out
}
