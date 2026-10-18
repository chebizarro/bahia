package identityrecovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	adapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"go.uber.org/zap"
)

// RecoveryLocalStore extends the engine's inventory read with durable retention
// for newly signed heads. The recovery process runs while the daemon is stopped.
type RecoveryLocalStore interface {
	LocalInventory
	StoreEvent(context.Context, nostr.Event) error
}

// RegistryRecoveryAdapter reasserts one complete cp-state registry family.
// Service, environment and adoption-binding families are supported; the
// environment record includes its complete explicit deployment-unit set.
// Other families must not be registered through this adapter.
type RegistryRecoveryAdapter struct {
	Family    int
	OldPubkey nostr.PubKey
	Source    EventSource
	Local     RecoveryLocalStore
	Ledger    *Ledger
}

func (a *RegistryRecoveryAdapter) familyTopic() (string, error) {
	switch a.Family {
	case adapter.KindServiceRegistry:
		return kinds.CPStateTopicServiceRegistry, nil
	case adapter.KindEnvironmentRegistry:
		return kinds.CPStateTopicEnvironmentRegistry, nil
	case adapter.KindAdoptionBindingRecord:
		return kinds.CPStateTopicAdoptionBinding, nil
	default:
		return "", errors.New("unsupported recovery registry family")
	}
}

func (a *RegistryRecoveryAdapter) state() (State, error) {
	if a == nil {
		return State{}, errors.New("registry recovery adapter required")
	}
	if _, err := a.familyTopic(); err != nil {
		return State{}, err
	}
	if a.Source == nil || a.Local == nil || a.Ledger == nil || a.OldPubkey == nostr.ZeroPK {
		return State{}, errors.New("registry recovery sources and old identity required")
	}
	s, err := a.Ledger.Load()
	if err != nil {
		return State{}, err
	}
	if s.OldPubkey != a.OldPubkey.Hex() || s.InventoryDigest == "" || s.ActivationBarrier <= 0 {
		return State{}, errors.New("accepted recovery inventory unavailable")
	}
	digest, count, err := InventoryDigest(s.Inventory)
	if err != nil || digest != s.InventoryDigest || count != len(s.Inventory) {
		return State{}, errors.New("accepted recovery inventory digest mismatch")
	}
	return s, nil
}

// Inventory reads only accepted old-key events, merging EOSE-complete relay
// history with the durable local store and resolving each addressable head by
// NIP-01 ordering. It never consults a SQL projection.
func (a *RegistryRecoveryAdapter) Inventory(ctx context.Context) ([]string, error) {
	heads, err := a.inventoryHeads(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(heads))
	for _, head := range heads {
		ids = append(ids, head.ID.Hex())
	}
	slices.Sort(ids)
	return ids, nil
}

func (a *RegistryRecoveryAdapter) inventoryHeads(ctx context.Context) (map[string]nostr.Event, error) {
	s, err := a.state()
	if err != nil {
		return nil, err
	}
	topic, _ := a.familyTopic()
	accepted := make(map[nostr.ID]struct{}, len(s.Inventory))
	ids := make([]nostr.ID, 0, len(s.Inventory))
	for _, item := range s.Inventory {
		if item.Kind != kinds.CASControlState {
			continue
		}
		id, err := nostr.IDFromHex(item.ID)
		if err != nil {
			return nil, errors.New("invalid accepted event ID")
		}
		accepted[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return map[string]nostr.Event{}, nil
	}
	relay, err := a.Source.Read(ctx, nostr.Filter{IDs: ids, Kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)}, Authors: []nostr.PubKey{a.OldPubkey}})
	if err != nil {
		return nil, fmt.Errorf("read accepted registry relay history: %w", err)
	}
	local, err := a.Local.Events(ctx, a.OldPubkey)
	if err != nil {
		return nil, fmt.Errorf("read accepted registry local history: %w", err)
	}
	seen := map[nostr.ID]nostr.Event{}
	for _, ev := range append(relay, local...) {
		if _, ok := accepted[ev.ID]; !ok || ev.Kind != nostr.Kind(kinds.CASControlState) {
			continue
		}
		if ev.PubKey != a.OldPubkey || !ev.CheckID() || !ev.VerifySignature() {
			return nil, errors.New("invalid accepted registry event")
		}
		if int64(ev.CreatedAt) > s.ActivationBarrier {
			return nil, errors.New("accepted old-key event crosses trust activation barrier")
		}
		seen[ev.ID] = ev
	}
	if err := completeAcceptedHistory(ctx, s, a.OldPubkey, accepted, seen); err != nil {
		return nil, err
	}

	heads := map[string]nostr.Event{}
	for _, ev := range seen {
		if tag(ev, "legacy_kind") != strconv.Itoa(a.Family) && tag(ev, "t") != topic {
			continue
		}
		d := tag(ev, "d")
		if d == "" {
			return nil, errors.New("accepted registry head lacks coordinate")
		}
		if previous, ok := heads[d]; !ok || newer(ev, previous) {
			heads[d] = ev
		}
	}
	for d, ev := range heads {
		if err := rejectSupersededSnapshotHead(ctx, s, a.Source, local, a.OldPubkey, ev); err != nil {
			return nil, fmt.Errorf("registry coordinate %s: %w", d, err)
		}
		if err := a.preflight(ev); err != nil {
			return nil, fmt.Errorf("registry coordinate %s: %w", d, err)
		}
	}
	return heads, nil
}

func newer(a, b nostr.Event) bool {
	return a.CreatedAt > b.CreatedAt || a.CreatedAt == b.CreatedAt && strings.Compare(a.ID.Hex(), b.ID.Hex()) < 0
}

func (a *RegistryRecoveryAdapter) preflight(ev nostr.Event) error {
	topic, _ := a.familyTopic()
	if ev.Kind != nostr.Kind(kinds.CASControlState) || tag(ev, "t") != topic || tag(ev, "legacy_kind") != strconv.Itoa(a.Family) {
		return errors.New("registry family envelope mismatch")
	}
	deletedTag := tag(ev, "deleted")
	if deletedTag != "true" && deletedTag != "false" {
		return errors.New("registry deletion marker invalid")
	}
	deleted := deletedTag == "true"
	var kind int
	var tags nostr.Tags
	var content string
	switch a.Family {
	case adapter.KindServiceRegistry:
		var service domain.Service
		if err := json.Unmarshal([]byte(ev.Content), &service); err != nil || service.ID == uuid.Nil {
			return errors.New("service registry content cannot be decoded")
		}
		kind, tags, content = adapter.ServiceRegistryRecoveryRecord(&service, deleted)
	case adapter.KindEnvironmentRegistry:
		var payload struct {
			domain.Environment
			DeploymentUnits []domain.DeploymentUnit `json:"deployment_units"`
		}
		if err := json.Unmarshal([]byte(ev.Content), &payload); err != nil || payload.ID == uuid.Nil {
			return errors.New("environment registry content cannot be decoded")
		}
		units := payload.DeploymentUnits
		if len(units) == 1 && units[0].Implicit {
			units = nil
		}
		kind, tags, content = adapter.EnvironmentRegistryRecoveryRecord(&payload.Environment, units, deleted)
	case adapter.KindAdoptionBindingRecord:
		if deleted {
			return errors.New("adoption binding tombstone has no production publisher")
		}
		var binding domain.AdoptionBinding
		if err := json.Unmarshal([]byte(ev.Content), &binding); err != nil || binding.ServiceID == uuid.Nil || binding.EnvironmentID == uuid.Nil {
			return errors.New("adoption binding content cannot be decoded")
		}
		var err error
		kind, tags, content, err = adapter.AdoptionBindingRecoveryRecord(&binding)
		if err != nil {
			return err
		}
	}
	if kind != int(ev.Kind) || !reflect.DeepEqual(tags, ev.Tags) || content != ev.Content {
		return errors.New("typed registry builder cannot reproduce accepted head")
	}
	return nil
}

type registryRecoverySink struct {
	source EventSource
	local  RecoveryLocalStore
}

func (s registryRecoverySink) publish(ctx context.Context, ev nostr.Event) error {
	if !ev.CheckID() || !ev.VerifySignature() {
		return errors.New("new identity produced invalid registry signature")
	}
	if err := s.source.Publish(ctx, ev); err != nil {
		return err
	}
	return s.local.StoreEvent(ctx, ev)
}

func (s registryRecoverySink) PublishProjection(ctx context.Context, ev nostr.Event, _ string, _ *uuid.UUID) error {
	return s.publish(ctx, ev)
}

func (s registryRecoverySink) PublishBeforeCommit(ctx context.Context, ev nostr.Event, _ string, _ *uuid.UUID) error {
	return s.publish(ctx, ev)
}

func (a *RegistryRecoveryAdapter) Reassert(ctx context.Context, signer nostr.Keyer) ([]string, error) {
	heads, err := a.inventoryHeads(ctx)
	if err != nil {
		return nil, err
	}
	s, err := a.state()
	if err != nil {
		return nil, err
	}
	pub, err := signer.GetPublicKey(ctx)
	if err != nil || pub == nostr.ZeroPK || pub.Hex() != s.NewPubkey || pub == a.OldPubkey {
		return nil, errors.New("new registry signer does not match recovery ledger")
	}
	sink := registryRecoverySink{source: a.Source, local: a.Local}
	projector := adapter.NewProjector(config.NostrConfig{PublishEnabled: true}, nil, sink, nil, zap.NewNop(), adapter.WithProjectorSigner(signer, pub.Hex()))
	publisher := adapter.NewRelayFirstStatePublisher(projector, sink)
	adoptionPublisher := adapter.NewAdoptionCanonicalPublisher(projector, zap.NewNop())
	coords := sortedCoordinates(heads)
	evidence := make([]string, 0, len(coords))
	for _, d := range coords {
		old := heads[d]
		relay, local, err := a.readNewHeads(ctx, pub, d)
		if err != nil {
			return nil, err
		}
		if relay != nil && !equivalent(*relay, old) || local != nil && !equivalent(*local, old) {
			return nil, fmt.Errorf("registry coordinate %s: new-key head differs from accepted state", d)
		}
		switch {
		case relay != nil && local != nil:
			// Both canonical surfaces already hold equivalent state.
		case relay != nil:
			if err := a.Local.StoreEvent(ctx, *relay); err != nil {
				return nil, err
			}
		case local != nil:
			if err := a.Source.Publish(ctx, *local); err != nil {
				return nil, err
			}
		default:
			deleted := tag(old, "deleted") == "true"
			switch a.Family {
			case adapter.KindServiceRegistry:
				var service domain.Service
				if err := json.Unmarshal([]byte(old.Content), &service); err != nil {
					return nil, err
				}
				err = publisher.PublishServiceRegistry(ctx, &service, deleted)
			case adapter.KindEnvironmentRegistry:
				var payload struct {
					domain.Environment
					DeploymentUnits []domain.DeploymentUnit `json:"deployment_units"`
				}
				if err := json.Unmarshal([]byte(old.Content), &payload); err != nil {
					return nil, err
				}
				units := payload.DeploymentUnits
				if len(units) == 1 && units[0].Implicit {
					units = nil
				}
				err = publisher.PublishEnvironmentRegistry(ctx, &payload.Environment, units, deleted)
			case adapter.KindAdoptionBindingRecord:
				var binding domain.AdoptionBinding
				if err := json.Unmarshal([]byte(old.Content), &binding); err != nil {
					return nil, err
				}
				err = adoptionPublisher.PublishAdoptionBinding(ctx, &binding)
			}
			if err != nil {
				return nil, err
			}
		}
		relay, local, err = a.readNewHeads(ctx, pub, d)
		if err != nil || relay == nil || local == nil || !equivalent(*relay, old) || !equivalent(*local, old) {
			return nil, fmt.Errorf("registry coordinate %s: %w", d, ErrIncomplete)
		}
		evidence = append(evidence, relay.ID.Hex())
	}
	return evidence, nil
}

func (a *RegistryRecoveryAdapter) Verify(ctx context.Context) ([]string, error) {
	heads, err := a.inventoryHeads(ctx)
	if err != nil {
		return nil, err
	}
	s, err := a.state()
	if err != nil {
		return nil, err
	}
	pub, err := nostr.PubKeyFromHex(s.NewPubkey)
	if err != nil || pub == a.OldPubkey {
		return nil, errors.New("new registry identity unavailable")
	}
	hash := sha256.New()
	for _, d := range sortedCoordinates(heads) {
		relay, local, err := a.readNewHeads(ctx, pub, d)
		if err != nil || relay == nil || local == nil || !equivalent(*relay, heads[d]) || !equivalent(*local, heads[d]) {
			return nil, fmt.Errorf("registry coordinate %s: equivalence verification failed: %w", d, ErrIncomplete)
		}
		_, _ = hash.Write([]byte(d + "\x00" + heads[d].Content + "\x00"))
		tagJSON, _ := json.Marshal(heads[d].Tags)
		_, _ = hash.Write(tagJSON)
		_, _ = hash.Write([]byte("\n"))
	}
	topic, _ := a.familyTopic()
	return []string{topic + ":sha256:" + hex.EncodeToString(hash.Sum(nil)), topic + ":count:" + strconv.Itoa(len(heads))}, nil
}

func (a *RegistryRecoveryAdapter) readNewHeads(ctx context.Context, pub nostr.PubKey, d string) (*nostr.Event, *nostr.Event, error) {
	filter := nostr.Filter{Authors: []nostr.PubKey{pub}, Kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)}, Tags: nostr.TagMap{"d": {d}}}
	relayEvents, err := a.Source.Read(ctx, filter)
	if err != nil {
		return nil, nil, err
	}
	localEvents, err := a.Local.Events(ctx, pub)
	if err != nil {
		return nil, nil, err
	}
	return latestCoordinate(relayEvents, pub, d), latestCoordinate(localEvents, pub, d), nil
}

func latestCoordinate(events []nostr.Event, pub nostr.PubKey, d string) *nostr.Event {
	var head *nostr.Event
	for _, ev := range events {
		if ev.PubKey != pub || ev.Kind != nostr.Kind(kinds.CASControlState) || tag(ev, "d") != d {
			continue
		}
		if head == nil || newer(ev, *head) {
			copy := ev
			head = &copy
		}
	}
	return head
}

func equivalent(a, b nostr.Event) bool {
	return a.Kind == b.Kind && reflect.DeepEqual(a.Tags, b.Tags) && a.Content == b.Content && a.CheckID() && a.VerifySignature()
}

func sortedCoordinates(heads map[string]nostr.Event) []string {
	coords := make([]string, 0, len(heads))
	for d := range heads {
		coords = append(coords, d)
	}
	slices.Sort(coords)
	return coords
}
