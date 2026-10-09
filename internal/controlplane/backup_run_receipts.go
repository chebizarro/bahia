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
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
)

// BackupRunReceiptReader reads a service-signed run state whose exact event
// received the daemon's configured relay publish quorum. A local cache entry
// alone is not proof of acceptance.
type BackupRunReceiptReader interface {
	GetBackupRunReceipt(context.Context, uuid.UUID) (*domain.BackupRun, error)
}

type backupRunEventStore interface {
	QueryEvents(nostr.Filter) iter.Seq[nostr.Event]
}

type backupRunDeliveryStore interface {
	Get(nostr.ID) (localstore.OutboxEntry, bool, error)
}

type localBackupRunReceipts struct {
	events   backupRunEventStore
	delivery backupRunDeliveryStore
	author   nostr.PubKey
}

// NewLocalBackupRunReceipts constructs a fail-closed canonical read path. An
// outbox entry may be pruned after delivery; in that case this reader refuses
// to infer an ACK from the local event cache.
func NewLocalBackupRunReceipts(events backupRunEventStore, delivery backupRunDeliveryStore, servicePubkey string) (BackupRunReceiptReader, error) {
	if events == nil || delivery == nil {
		return nil, fmt.Errorf("backup run receipt requires local events and outbox delivery records")
	}
	author, err := nostr.PubKeyFromHex(strings.TrimSpace(servicePubkey))
	if err != nil {
		return nil, fmt.Errorf("backup run receipt service pubkey: %w", err)
	}
	return &localBackupRunReceipts{events: events, delivery: delivery, author: author}, nil
}

func (s *localBackupRunReceipts) GetBackupRunReceipt(ctx context.Context, id uuid.UUID) (*domain.BackupRun, error) {
	if id == uuid.Nil {
		return nil, fmt.Errorf("backup run receipt requires a run id")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d := "backup-run:" + id.String()
	filter := nostr.Filter{
		Kinds:   []nostr.Kind{nostr.Kind(kinds.CASControlState)},
		Authors: []nostr.PubKey{s.author},
		Tags: nostr.TagMap{
			"d": {d},
			"t": {kinds.CPStateTopicBackupRun},
		},
		Limit: 1,
	}
	for ev := range s.events.QueryEvents(filter) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if ev.PubKey != s.author || ev.Kind != nostr.Kind(kinds.CASControlState) || !ev.CheckID() || !ev.VerifySignature() ||
			backupReceiptTag(ev.Tags, "d") != d || backupReceiptTag(ev.Tags, "t") != kinds.CPStateTopicBackupRun ||
			backupReceiptTag(ev.Tags, "domain") != "backup" || backupReceiptTag(ev.Tags, "schema") != kinds.CASControlStateSchema ||
			backupReceiptTag(ev.Tags, "legacy_kind") != strconv.Itoa(kinds.BackupRunState) ||
			backupReceiptTag(ev.Tags, "deleted") != "false" {
			return nil, fmt.Errorf("backup run %s has an invalid signed state envelope", id)
		}
		var wire struct {
			Deleted *bool `json:"deleted"`
		}
		var run domain.BackupRun
		if json.Unmarshal([]byte(ev.Content), &wire) != nil || wire.Deleted == nil || *wire.Deleted ||
			json.Unmarshal([]byte(ev.Content), &run) != nil ||
			run.ID != id || run.RecipeID.String() != backupReceiptTag(ev.Tags, "recipe_id") ||
			run.RepositoryID.String() != backupReceiptTag(ev.Tags, "repository_id") ||
			string(run.Status) != backupReceiptTag(ev.Tags, "status") ||
			run.RequestedBy == "" || run.RequestEventID == "" || run.RequestDTag == "" || run.RequestKind == 0 ||
			domain.ValidateBackupRun(&run) != nil {
			return nil, fmt.Errorf("backup run %s has inconsistent canonical content", id)
		}
		entry, found, err := s.delivery.Get(ev.ID)
		if err != nil {
			return nil, fmt.Errorf("backup run %s relay delivery receipt: %w", id, err)
		}
		acked := false
		for _, relay := range entry.Relays {
			acked = acked || relay.Accepted
		}
		if !found || entry.Target != repository.NostrPublishTargetControlPlane || !entry.Delivered || !acked ||
			entry.Event.ID != ev.ID || entry.Event.PubKey != s.author ||
			!entry.Event.CheckID() || !entry.Event.VerifySignature() {
			return nil, fmt.Errorf("backup run %s has no ACKed relay delivery receipt", id)
		}
		return &run, nil
	}
	return nil, nil
}

func backupReceiptTag(tags nostr.Tags, key string) string {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == key {
			return tag[1]
		}
	}
	return ""
}
