package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"reflect"
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

// backupExecutionConfigProof is deliberately implemented only by the local
// signed-event/relay-ACK reader, never by SQL or an in-memory registry.
type backupExecutionConfigProof interface {
	VerifyBackupExecutionConfig(context.Context, *domain.BackupExecutionSnapshot) error
}

type backupRunEventStore interface {
	QueryEvents(nostr.Filter) iter.Seq[nostr.Event]
}

type backupRunDeliveryStore interface {
	Get(nostr.ID) (localstore.OutboxEntry, bool, error)
	GetDeliveryProof(nostr.ID) (localstore.DeliveryProof, bool, error)
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
		proof, found, err := s.delivery.GetDeliveryProof(ev.ID)
		if err != nil {
			return nil, fmt.Errorf("backup run %s relay delivery receipt: %w", id, err)
		}
		if !found || !proof.ValidFor(ev, repository.NostrPublishTargetControlPlane) || proof.Event.PubKey != s.author {
			return nil, fmt.Errorf("backup run %s has no ACKed relay delivery receipt", id)
		}
		return &run, nil
	}
	return nil, nil
}

func (s *localBackupRunReceipts) VerifyBackupExecutionConfig(ctx context.Context, snapshot *domain.BackupExecutionSnapshot) error {
	if snapshot == nil {
		return fmt.Errorf("execution snapshot is missing")
	}
	for _, source := range []struct {
		id     string
		topic  string
		legacy int
		dtag   string
		want   any
	}{
		{snapshot.RecipeEventID, kinds.CPStateTopicBackupRecipe, kinds.BackupRecipeRegistry, "backup-recipe:" + snapshot.Recipe.ID.String(), snapshot.Recipe},
		{snapshot.RepositoryEventID, kinds.CPStateTopicBackupRepository, kinds.BackupRepositoryRegistry, "backup-repository:" + snapshot.Repository.ID.String(), snapshot.Repository},
	} {
		if err := s.verifyBackupConfigEvent(ctx, source.id, source.topic, source.legacy, source.dtag, source.want); err != nil {
			return err
		}
	}
	if snapshot.Policy != nil {
		if err := s.verifyBackupConfigEvent(ctx, snapshot.PolicyEventID, kinds.CPStateTopicBackupPolicy, kinds.BackupPolicyRegistry, "backup-policy:"+snapshot.Policy.ID.String(), snapshot.Policy); err != nil {
			return err
		}
	}
	return nil
}

func (s *localBackupRunReceipts) verifyBackupConfigEvent(ctx context.Context, idHex, topic string, legacy int, dtag string, want any) error {
	id, err := nostr.IDFromHex(idHex)
	if err != nil {
		return fmt.Errorf("backup config %s has invalid event id: %w", dtag, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	proof, found, err := s.delivery.GetDeliveryProof(id)
	if err != nil {
		return fmt.Errorf("backup config %s relay delivery receipt: %w", dtag, err)
	}
	if !found || !proof.ValidFor(proof.Event, repository.NostrPublishTargetControlPlane) {
		return fmt.Errorf("backup config %s has no ACKed relay delivery receipt", dtag)
	}
	ev := proof.Event
	if ev.ID != id || ev.PubKey != s.author || ev.Kind != nostr.Kind(kinds.CASControlState) ||
		backupReceiptTag(ev.Tags, "d") != dtag || backupReceiptTag(ev.Tags, "t") != topic ||
		backupReceiptTag(ev.Tags, "domain") != "backup" || backupReceiptTag(ev.Tags, "schema") != kinds.CASControlStateSchema ||
		backupReceiptTag(ev.Tags, "legacy_kind") != strconv.Itoa(legacy) || backupReceiptTag(ev.Tags, "deleted") != "false" {
		return fmt.Errorf("backup config %s has invalid signed envelope", dtag)
	}
	var envelope struct {
		Deleted *bool `json:"deleted"`
	}
	if json.Unmarshal([]byte(ev.Content), &envelope) != nil || envelope.Deleted == nil || *envelope.Deleted {
		return fmt.Errorf("backup config %s has invalid content", dtag)
	}
	canonical, err := json.Marshal(want)
	if err != nil {
		return err
	}
	actual := reflect.New(reflect.TypeOf(want))
	if actual.Elem().Kind() == reflect.Pointer {
		actual = reflect.New(actual.Elem().Type().Elem())
	}
	if err := json.Unmarshal([]byte(ev.Content), actual.Interface()); err != nil {
		return fmt.Errorf("backup config %s has invalid content: %w", dtag, err)
	}
	decoded, err := json.Marshal(actual.Elem().Interface())
	if err != nil || !bytes.Equal(canonical, decoded) {
		return fmt.Errorf("backup config %s differs from signed execution snapshot", dtag)
	}
	return nil
}

func backupReceiptTag(tags nostr.Tags, key string) string {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == key {
			return tag[1]
		}
	}
	return ""
}
