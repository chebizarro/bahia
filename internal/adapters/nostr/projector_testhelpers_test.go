package nostr

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// relaySink is the relay-shaped fake most projector tests drive: it reports
// how many relays accepted an event.
type relaySink interface {
	Publish(ctx context.Context, ev gonostr.Event) (int, error)
}

// sinkProjectionPublisher adapts a relaySink to ProjectionPublisher the way
// the outbox Publisher behaves for these tests: an accepted event is recorded
// in repo (so a restarted projector hydrates from it), a sink error is
// returned as is, and zero acceptances is a failure that was not queued.
type sinkProjectionPublisher struct {
	sink relaySink
	repo repository.NostrEventRepository
}

func (p sinkProjectionPublisher) PublishProjection(ctx context.Context, ev gonostr.Event, entityType string, entityID *uuid.UUID) error {
	accepted, err := p.sink.Publish(ctx, ev)
	if err != nil {
		return err
	}
	if accepted == 0 {
		return fmt.Errorf("no relays accepted event kind %d", eventKindInt(&ev))
	}
	if p.repo != nil {
		tags, _ := json.Marshal(ev.Tags)
		if _, err := p.repo.Record(ctx, &repository.NostrEventRecord{
			ID: eventIDHex(&ev), Kind: eventKindInt(&ev), PubKey: eventPubKeyHex(&ev), Content: ev.Content,
			Tags: tags, Sig: eventSignatureHex(&ev), CreatedAt: ev.CreatedAt.Time(), ReceivedAt: time.Now().UTC(),
			EntityType: entityType, EntityID: entityID, PublishState: repository.NostrPublishStatePublished,
		}); err != nil {
			return err
		}
	}
	return nil
}

// newTestProjector builds a Projector over a relay-shaped fake sink. A nil
// sink leaves the projector without a publisher (disabled).
func newTestProjector(cfg config.NostrConfig, source ProjectionSource, sink relaySink, repo repository.NostrEventRepository, logger *zap.Logger, opts ...ProjectorOption) *Projector {
	var publisher ProjectionPublisher
	if sink != nil {
		publisher = sinkProjectionPublisher{sink: sink, repo: repo}
	}
	return newKeyedTestProjector(cfg, source, publisher, repo, logger, opts...)
}
