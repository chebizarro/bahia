package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// FamilyApplier applies one decoded projection event to a family-specific cache.
type FamilyApplier func(ctx context.Context, event any) error

// RelayProjectionCache applies relay-canonical projection events to local cache repositories.
type RelayProjectionCache struct {
	meta     repository.RelayProjectionMetaRepository
	logger   *zap.Logger
	appliers map[string]FamilyApplier
}

func NewRelayProjectionCache(meta repository.RelayProjectionMetaRepository, logger *zap.Logger) *RelayProjectionCache {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &RelayProjectionCache{
		meta:     meta,
		logger:   logger,
		appliers: make(map[string]FamilyApplier),
	}
}

func (c *RelayProjectionCache) RegisterApplier(family any, fn FamilyApplier) {
	stream := fmt.Sprint(family)
	if fn == nil {
		delete(c.appliers, stream)
		return
	}
	c.appliers[stream] = fn
}

func (c *RelayProjectionCache) Apply(ctx context.Context, event any) error {
	if c == nil || c.meta == nil {
		return errors.New("relay projection cache requires a meta repository")
	}
	projection, err := projectionFields(event)
	if err != nil {
		return err
	}

	existing, err := c.meta.Get(ctx, projection.stream, projection.entityKey)
	if err != nil {
		return fmt.Errorf("getting relay projection meta for %s/%s: %w", projection.stream, projection.entityKey, err)
	}
	if existing != nil && !projection.updatedAt.After(existing.UpdatedAt) {
		c.logger.Debug("skipping stale relay projection event", zap.String("stream", projection.stream), zap.String("entity_key", projection.entityKey), zap.String("source_event_id", projection.sourceEventID))
		return nil
	}

	if applier := c.appliers[projection.stream]; applier != nil {
		if err := applier(ctx, event); err != nil {
			return fmt.Errorf("applying relay projection family %s: %w", projection.stream, err)
		}
	}

	meta := repository.RelayProjectionMeta{
		Stream:        projection.stream,
		EntityKey:     projection.entityKey,
		UpdatedAt:     projection.updatedAt,
		SourceEventID: projection.sourceEventID,
		Tombstoned:    projection.tombstoned,
	}
	if err := c.meta.Upsert(ctx, meta); err != nil {
		return fmt.Errorf("upserting relay projection meta for %s/%s: %w", projection.stream, projection.entityKey, err)
	}
	return nil
}

type relayProjectionFields struct {
	stream        string
	entityKey     string
	updatedAt     time.Time
	sourceEventID string
	tombstoned    bool
}

func projectionFields(event any) (relayProjectionFields, error) {
	if event == nil {
		return relayProjectionFields{}, errors.New("decoded projection event is required")
	}
	value := reflect.ValueOf(event)
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return relayProjectionFields{}, errors.New("decoded projection event is required")
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return relayProjectionFields{}, errors.New("decoded projection event must be a struct or struct pointer")
	}

	familyField := value.FieldByName("Family")
	dTagField := value.FieldByName("DTag")
	timestampField := value.FieldByName("Timestamp")
	sourceIDField := value.FieldByName("SourceID")
	tombstoneField := value.FieldByName("Tombstone")
	if !familyField.IsValid() || !dTagField.IsValid() || !timestampField.IsValid() || !sourceIDField.IsValid() || !tombstoneField.IsValid() {
		return relayProjectionFields{}, errors.New("decoded projection event is missing required projection fields")
	}

	stream := fmt.Sprint(familyField.Interface())
	entityKey, _ := dTagField.Interface().(string)
	updatedAt, _ := timestampField.Interface().(time.Time)
	sourceID, _ := sourceIDField.Interface().(string)
	tombstone, _ := tombstoneField.Interface().(bool)
	if stream == "" {
		return relayProjectionFields{}, errors.New("decoded projection event family is required")
	}
	if entityKey == "" {
		return relayProjectionFields{}, errors.New("decoded projection event d tag is required")
	}
	if updatedAt.IsZero() {
		return relayProjectionFields{}, errors.New("decoded projection event timestamp is required")
	}
	return relayProjectionFields{stream: stream, entityKey: entityKey, updatedAt: updatedAt, sourceEventID: sourceID, tombstoned: tombstone}, nil
}
