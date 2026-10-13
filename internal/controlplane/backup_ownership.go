package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"strconv"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// BackupOwnershipReader resolves ownership only from service-signed state.
// Absence permits creation; malformed or ownerless state never does.
type BackupOwnershipReader interface {
	OwnerOf(context.Context, int, uuid.UUID) (uuid.UUID, bool, error)
}

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
	if r == nil || r.events == nil || id == uuid.Nil {
		return uuid.Nil, false, fmt.Errorf("backup ownership lookup is unavailable")
	}
	d, topic, err := backupOwnerCoordinate(family, id)
	if err != nil {
		return uuid.Nil, false, err
	}
	filter := nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)}, Authors: []nostr.PubKey{r.author},
		Tags: nostr.TagMap{"d": {d}, "t": {topic}}, Limit: 1}
	for ev := range r.events.QueryEvents(filter) {
		if err := ctx.Err(); err != nil {
			return uuid.Nil, false, err
		}
		if ev.PubKey != r.author || !ev.CheckID() || !ev.VerifySignature() ||
			backupReceiptTag(ev.Tags, "d") != d || backupReceiptTag(ev.Tags, "t") != topic ||
			backupReceiptTag(ev.Tags, "domain") != "backup" || backupReceiptTag(ev.Tags, "schema") != kinds.CASControlStateSchema ||
			backupReceiptTag(ev.Tags, "legacy_kind") != strconv.Itoa(family) {
			return uuid.Nil, true, fmt.Errorf("backup %s has an invalid canonical envelope", d)
		}
		var content struct {
			ID      uuid.UUID `json:"id"`
			OrgID   uuid.UUID `json:"org_id"`
			Deleted bool      `json:"deleted"`
		}
		if json.Unmarshal([]byte(ev.Content), &content) != nil || content.ID != id ||
			content.OrgID == uuid.Nil || backupReceiptTag(ev.Tags, "org") != content.OrgID.String() {
			return uuid.Nil, true, fmt.Errorf("backup %s has no valid canonical owner; operator must republish owner-attested canonical state", d)
		}
		return content.OrgID, true, nil
	}
	return uuid.Nil, false, nil
}
