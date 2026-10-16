package identityrecovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	adapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/kinds"
	"go.uber.org/zap"
)

// NotificationRecoveryAdapter retains the OCK-visible layer byte-for-byte and
// changes only the old service-key NIP-44 inner layer. OldSigner is used only
// for scoped decryption; it never signs a recovery event.
type NotificationRecoveryAdapter struct {
	OldPubkey nostr.PubKey
	Source    EventSource
	Local     RecoveryLocalStore
	Ledger    *Ledger
	OldSigner func(context.Context) (nostr.Keyer, error)
	NewSigner func(context.Context) (nostr.Keyer, error)
}

func (a *NotificationRecoveryAdapter) core() canonicalHeadAdapter {
	return canonicalHeadAdapter{old: a.OldPubkey, source: a.Source, local: a.Local, ledger: a.Ledger,
		kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)}, name: "notification-channels",
		belongs: func(ev nostr.Event) bool {
			return tag(ev, "legacy_kind") == strconv.Itoa(adapter.KindNotificationChannelRegistry) || tag(ev, "domain") == "notification" && tag(ev, "t") == kinds.CPStateTopicNotificationChannelRegistry
		},
		preflight: validateNotificationHead,
		publish: func(context.Context, nostr.Keyer, nostr.Event, registryRecoverySink) error {
			return ErrAdapterNotRegistered
		},
	}
}

func validateNotificationHead(ev nostr.Event) error {
	d := tag(ev, "d")
	parsed, parseErr := uuid.Parse(d)
	if ev.Kind != nostr.Kind(kinds.CASControlState) || parseErr != nil || parsed.String() != d || tag(ev, "legacy_kind") != strconv.Itoa(adapter.KindNotificationChannelRegistry) {
		return errors.New("notification recovery coordinate or family invalid")
	}
	deleted := tag(ev, "deleted")
	if deleted != "true" && deleted != "false" {
		return errors.New("notification deletion marker invalid")
	}
	wk, tags := adapter.ControlStateEnvelope(adapter.KindNotificationChannelRegistry, d, deleted == "true")
	if wk != int(ev.Kind) || !reflect.DeepEqual(tags, ev.Tags) {
		return errors.New("notification public envelope invalid")
	}
	env, err := controlplane.ParseConfidentialEnvelope(ev.Content)
	if err != nil || env.Algorithm != controlplane.ConfidentialAlgorithm || env.KeyOrg == "" || env.KeyRef == "" || env.KeyVersion == "" || env.Nonce == "" || env.Ciphertext == "" {
		return errors.New("notification confidential envelope invalid")
	}
	for key, want := range map[string]string{"schema": controlplane.ConfidentialSchema, "key_org": env.KeyOrg, "key_ref": env.KeyRef, "key_version": env.KeyVersion, "legacy_kind": strconv.Itoa(adapter.KindNotificationChannelRegistry), "d": d, "t": kinds.CPStateTopicNotificationChannelRegistry} {
		if env.AssociatedData[key] != want {
			return errors.New("notification associated data invalid")
		}
	}
	if deleted == "false" && env.ServiceInner == "" {
		return errors.New("notification service inner missing")
	}
	if deleted == "true" && env.ServiceInner != "" {
		return errors.New("notification tombstone contains service inner")
	}
	return nil
}

func (a *NotificationRecoveryAdapter) Inventory(ctx context.Context) ([]string, error) {
	return a.core().Inventory(ctx)
}

func notificationInner(ctx context.Context, ev nostr.Event, signer nostr.Keyer, pub nostr.PubKey) (string, error) {
	env, err := controlplane.ParseConfidentialEnvelope(ev.Content)
	if err != nil {
		return "", errors.New("notification envelope unavailable")
	}
	if env.ServiceInner == "" {
		return "", nil
	}
	plain, err := signer.Decrypt(ctx, env.ServiceInner, pub)
	if err != nil {
		return "", errors.New("notification scoped decryption failed")
	}
	return plain, nil
}

func notificationEquivalent(ctx context.Context, old, next nostr.Event, oldSigner, newSigner nostr.Keyer, oldPub, newPub nostr.PubKey) error {
	if next.PubKey != newPub || !next.CheckID() || !next.VerifySignature() {
		return errors.New("notification new-key signature invalid")
	}
	if err := validateNotificationHead(next); err != nil {
		return err
	}
	if !reflect.DeepEqual(old.Tags, next.Tags) {
		return errors.New("notification tags changed")
	}
	o, nerr := controlplane.ParseConfidentialEnvelope(old.Content)
	n, err := controlplane.ParseConfidentialEnvelope(next.Content)
	if nerr != nil || err != nil {
		return errors.New("notification envelope parse failed")
	}
	o.ServiceInner, n.ServiceInner = "", ""
	if !reflect.DeepEqual(o, n) {
		return errors.New("notification OCK layer changed")
	}
	oldPlain, err := notificationInner(ctx, old, oldSigner, oldPub)
	if err != nil {
		return err
	}
	newPlain, err := notificationInner(ctx, next, newSigner, newPub)
	if err != nil || oldPlain != newPlain {
		return errors.New("notification inner plaintext changed")
	}
	return nil
}

func (a *NotificationRecoveryAdapter) Reassert(ctx context.Context, signer nostr.Keyer) ([]string, error) {
	core := a.core()
	heads, err := core.heads(ctx)
	if err != nil {
		return nil, err
	}
	s, err := core.state()
	if err != nil {
		return nil, err
	}
	if a.OldSigner == nil || signer == nil {
		return nil, fmt.Errorf("notification signers: %w", ErrAdapterNotRegistered)
	}
	oldSigner, err := a.OldSigner(ctx)
	if err != nil {
		return nil, errors.New("quarantined notification decryptor unavailable")
	}
	oldPub, err := oldSigner.GetPublicKey(ctx)
	if err != nil || oldPub != a.OldPubkey {
		return nil, errors.New("quarantined notification decryptor identity mismatch")
	}
	pub, err := signer.GetPublicKey(ctx)
	if err != nil || pub.Hex() != s.NewPubkey || pub == a.OldPubkey {
		return nil, errors.New("notification new signer mismatch")
	}
	sink := registryRecoverySink{source: a.Source, local: a.Local}
	proof := make([]string, 0, len(heads))
	for _, key := range sortedCoordinates(heads) {
		old := heads[key]
		relay, local, err := core.newHeads(ctx, pub, old)
		if err != nil {
			return nil, err
		}
		for _, next := range []*nostr.Event{relay, local} {
			if next != nil {
				if err := notificationEquivalent(ctx, old, *next, oldSigner, signer, a.OldPubkey, pub); err != nil {
					return nil, err
				}
			}
		}
		switch {
		case relay != nil && local != nil:
		case relay != nil:
			err = a.Local.StoreEvent(ctx, *relay)
		case local != nil:
			err = a.Source.Publish(ctx, *local)
		default:
			env, _ := controlplane.ParseConfidentialEnvelope(old.Content)
			if env.ServiceInner != "" {
				plain, decErr := oldSigner.Decrypt(ctx, env.ServiceInner, a.OldPubkey)
				if decErr != nil {
					return nil, errors.New("notification scoped decryption failed")
				}
				env.ServiceInner, err = signer.Encrypt(ctx, plain, pub)
				if err != nil {
					return nil, errors.New("notification re-encryption failed")
				}
			}
			content, marshalErr := json.Marshal(env)
			if marshalErr != nil {
				return nil, marshalErr
			}
			projector := adapter.NewProjector(config.NostrConfig{PublishEnabled: true}, nil, sink, nil, zap.NewNop(), adapter.WithProjectorSigner(signer, pub.Hex()))
			err = adapter.NewNotificationCanonicalPublisher(projector, nil, nil, zap.NewNop()).PublishRecoveredEnvelope(ctx, tag(old, "d"), tag(old, "deleted") == "true", string(content))
		}
		if err != nil {
			return nil, err
		}
		relay, local, err = core.newHeads(ctx, pub, old)
		if err != nil || relay == nil || local == nil {
			return nil, fmt.Errorf("notification %s: %w", key, ErrIncomplete)
		}
		if err := notificationEquivalent(ctx, old, *relay, oldSigner, signer, a.OldPubkey, pub); err != nil {
			return nil, err
		}
		if err := notificationEquivalent(ctx, old, *local, oldSigner, signer, a.OldPubkey, pub); err != nil {
			return nil, err
		}
		if relay.ID != local.ID {
			return nil, errors.New("notification relay/local heads differ")
		}
		proof = append(proof, "notification:"+tag(old, "d")+":"+relay.ID.Hex())
	}
	slices.Sort(proof)
	return proof, nil
}

func (a *NotificationRecoveryAdapter) Verify(ctx context.Context) ([]string, error) {
	if a.OldSigner == nil || a.NewSigner == nil {
		return nil, fmt.Errorf("notification verification signers: %w", ErrAdapterNotRegistered)
	}
	oldSigner, err := a.OldSigner(ctx)
	if err != nil {
		return nil, err
	}
	oldPub, err := oldSigner.GetPublicKey(ctx)
	if err != nil || oldPub != a.OldPubkey {
		return nil, errors.New("quarantined notification decryptor identity mismatch")
	}
	newSigner, err := a.NewSigner(ctx)
	if err != nil {
		return nil, err
	}
	core := a.core()
	heads, err := core.heads(ctx)
	if err != nil {
		return nil, err
	}
	s, err := core.state()
	if err != nil {
		return nil, err
	}
	pub, err := newSigner.GetPublicKey(ctx)
	if err != nil || pub.Hex() != s.NewPubkey {
		return nil, errors.New("notification verification signer mismatch")
	}
	proof := make([]string, 0, len(heads))
	for _, key := range sortedCoordinates(heads) {
		old := heads[key]
		relay, local, err := core.newHeads(ctx, pub, old)
		if err != nil || relay == nil || local == nil {
			return nil, fmt.Errorf("notification %s: %w", key, ErrIncomplete)
		}
		if err := notificationEquivalent(ctx, old, *relay, oldSigner, newSigner, a.OldPubkey, pub); err != nil {
			return nil, err
		}
		if err := notificationEquivalent(ctx, old, *local, oldSigner, newSigner, a.OldPubkey, pub); err != nil {
			return nil, err
		}
		if relay.ID != local.ID {
			return nil, errors.New("notification relay/local heads differ")
		}
		proof = append(proof, "notification:"+tag(old, "d")+":"+relay.ID.Hex())
	}
	return proof, nil
}
