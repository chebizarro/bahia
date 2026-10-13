package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"sort"
	"strconv"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// BackupOwnershipReader resolves ownership only from service-signed state.
// Absence permits creation; malformed or ownerless state never does.
type BackupOwnershipReader interface {
	OwnerOf(context.Context, int, uuid.UUID) (uuid.UUID, bool, error)
}

// BackupConfigReader returns only verified service-signed configuration state.
// A tombstone is present but cannot be used as a live reference or mutation.
type BackupConfigReader interface {
	ConfigOf(context.Context, int, uuid.UUID) (json.RawMessage, bool, error)
}

type BackupConfigRebuilder interface {
	RebuildConfigIndex(context.Context, BackupConfigIndex) error
}

var ErrBackupConfigDeleted = errors.New("backup config is canonically deleted")

const backupReplayLimit = 100000

type localBackupOwnershipReader struct {
	events backupRunEventStore
	author nostr.PubKey
}

func NewLocalBackupOwnershipReader(events interface {
	QueryEvents(nostr.Filter) iter.Seq[nostr.Event]
}, servicePubkey string) (BackupOwnershipReader, error) {
	if events == nil {
		return nil, fmt.Errorf("backup ownership requires the local canonical event store")
	}
	author, err := nostr.PubKeyFromHex(strings.TrimSpace(servicePubkey))
	if err != nil {
		return nil, fmt.Errorf("backup ownership service pubkey: %w", err)
	}
	return &localBackupOwnershipReader{events: events, author: author}, nil
}

func backupOwnerCoordinate(family int, id uuid.UUID) (string, string, error) {
	switch family {
	case kinds.BackupRecipeRegistry:
		return "backup-recipe:" + id.String(), kinds.CPStateTopicBackupRecipe, nil
	case kinds.BackupPolicyRegistry:
		return "backup-policy:" + id.String(), kinds.CPStateTopicBackupPolicy, nil
	case kinds.BackupRepositoryRegistry:
		return "backup-repository:" + id.String(), kinds.CPStateTopicBackupRepository, nil
	case kinds.BackupDefinitionRegistry:
		return "backup-definition:" + id.String(), kinds.CPStateTopicBackupDefinition, nil
	default:
		return "", "", fmt.Errorf("unknown backup family %d", family)
	}
}

func (r *localBackupOwnershipReader) OwnerOf(ctx context.Context, family int, id uuid.UUID) (uuid.UUID, bool, error) {
	content, found, err := r.ConfigOf(ctx, family, id)
	if err != nil || !found {
		return uuid.Nil, found, err
	}
	var owner struct {
		OrgID uuid.UUID `json:"org_id"`
	}
	if err := json.Unmarshal(content, &owner); err != nil {
		return uuid.Nil, true, err
	}
	return owner.OrgID, true, nil
}

func (r *localBackupOwnershipReader) ConfigOf(ctx context.Context, family int, id uuid.UUID) (json.RawMessage, bool, error) {
	if r == nil || r.events == nil || id == uuid.Nil {
		return nil, false, fmt.Errorf("backup canonical config lookup is unavailable")
	}
	d, _, err := backupOwnerCoordinate(family, id)
	if err != nil {
		return nil, false, err
	}
	filter := nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)}, Authors: []nostr.PubKey{r.author},
		Tags: nostr.TagMap{"d": {d}}, Limit: backupReplayLimit + 1}
	var winner *nostr.Event
	count := 0
	for ev := range r.events.QueryEvents(filter) {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		count++
		if count > backupReplayLimit {
			return nil, true, fmt.Errorf("backup %s history reached replay limit", d)
		}
		if winner == nil || backupEventNewer(ev, *winner) {
			candidate := ev
			winner = &candidate
		}
	}
	if winner == nil {
		return nil, false, nil
	}
	return r.configFromEvent(family, id, *winner)
}

func backupEventNewer(candidate, current nostr.Event) bool {
	return candidate.CreatedAt > current.CreatedAt ||
		(candidate.CreatedAt == current.CreatedAt && candidate.ID.Hex() < current.ID.Hex())
}

func (r *localBackupOwnershipReader) configFromEvent(family int, id uuid.UUID, ev nostr.Event) (json.RawMessage, bool, error) {
	d, topic, err := backupOwnerCoordinate(family, id)
	if err != nil {
		return nil, true, err
	}
	if ev.PubKey != r.author || !ev.CheckID() || !ev.VerifySignature() ||
		backupReceiptTag(ev.Tags, "d") != d || backupReceiptTag(ev.Tags, "t") != topic ||
		backupReceiptTag(ev.Tags, "domain") != "backup" || backupReceiptTag(ev.Tags, "schema") != kinds.CASControlStateSchema ||
		backupReceiptTag(ev.Tags, "legacy_kind") != strconv.Itoa(family) ||
		(backupReceiptTag(ev.Tags, "deleted") != "true" && backupReceiptTag(ev.Tags, "deleted") != "false") {
		return nil, true, fmt.Errorf("backup %s has an invalid canonical envelope", d)
	}
	var content struct {
		ID      uuid.UUID `json:"id"`
		OrgID   uuid.UUID `json:"org_id"`
		Deleted bool      `json:"deleted"`
	}
	if json.Unmarshal([]byte(ev.Content), &content) != nil || content.ID != id ||
		content.OrgID == uuid.Nil || backupReceiptTag(ev.Tags, "org") != content.OrgID.String() {
		return nil, true, fmt.Errorf("backup %s has no valid canonical owner; operator must republish owner-attested canonical state", d)
	}
	if content.Deleted != (backupReceiptTag(ev.Tags, "deleted") == "true") {
		return nil, true, fmt.Errorf("backup %s has conflicting tombstone state", d)
	}
	if content.Deleted {
		return nil, true, fmt.Errorf("%w: %s", ErrBackupConfigDeleted, d)
	}
	return json.RawMessage(ev.Content), true, nil
}

// RebuildConfigIndex projects the current signed head of each configuration
// coordinate in dependency order. Neither SQL rows nor relay EOSE can create
// ownership; malformed heads stop replay rather than admitting legacy state.
func (r *localBackupOwnershipReader) RebuildConfigIndex(ctx context.Context, index BackupConfigIndex) error {
	if r == nil || r.events == nil || index == nil {
		return fmt.Errorf("backup canonical replay is unavailable")
	}
	if err := index.ResetBackupConfigIndex(ctx); err != nil {
		return err
	}
	for _, family := range []int{kinds.BackupRepositoryRegistry, kinds.BackupPolicyRegistry, kinds.BackupRecipeRegistry, kinds.BackupDefinitionRegistry} {
		prefix, topic, _ := backupOwnerCoordinate(family, uuid.Nil)
		prefix = strings.TrimSuffix(prefix, uuid.Nil.String())
		filter := nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)}, Authors: []nostr.PubKey{r.author},
			Tags: nostr.TagMap{"t": {topic}}, Limit: backupReplayLimit + 1}
		winners := map[string]nostr.Event{}
		count := 0
		for ev := range r.events.QueryEvents(filter) {
			if err := ctx.Err(); err != nil {
				return err
			}
			count++
			if count > backupReplayLimit {
				return fmt.Errorf("backup %s replay reached history limit", topic)
			}
			d := backupReceiptTag(ev.Tags, "d")
			if current, ok := winners[d]; !ok || backupEventNewer(ev, current) {
				winners[d] = ev
			}
		}
		keys := make([]string, 0, len(winners))
		for d := range winners {
			keys = append(keys, d)
		}
		sort.Strings(keys)
		for _, d := range keys {
			id, err := uuid.Parse(strings.TrimPrefix(d, prefix))
			if err != nil || d != prefix+id.String() {
				return fmt.Errorf("invalid backup coordinate %q", d)
			}
			raw, _, err := r.configFromEvent(family, id, winners[d])
			if errors.Is(err, ErrBackupConfigDeleted) {
				if err := index.MarkBackupConfigDeleted(ctx, family, id); err != nil {
					return err
				}
				continue
			}
			if err != nil {
				return err
			}
			switch family {
			case kinds.BackupRepositoryRegistry:
				var v domain.BackupRepository
				if err := decodeBackupConfig(raw, &v); err != nil {
					return err
				}
				if err := index.UpsertBackupRepository(ctx, &v); err != nil {
					return err
				}
			case kinds.BackupPolicyRegistry:
				var v domain.BackupPolicy
				if err := decodeBackupConfig(raw, &v); err != nil {
					return err
				}
				if err := index.UpsertBackupPolicy(ctx, &v); err != nil {
					return err
				}
			case kinds.BackupRecipeRegistry:
				var v domain.BackupRecipe
				if err := decodeBackupConfig(raw, &v); err != nil {
					return err
				}
				if err := index.UpsertBackupRecipe(ctx, &v); err != nil {
					return err
				}
			case kinds.BackupDefinitionRegistry:
				var v domain.BackupDefinition
				if err := decodeBackupConfig(raw, &v); err != nil {
					return err
				}
				if err := index.UpsertBackupDefinition(ctx, &v); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
