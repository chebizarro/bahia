package repository

import (
	"context"
	"time"
)

// RelayProjectionMeta records last-applied relay projection metadata for a cache entity.
type RelayProjectionMeta struct {
	Stream        string
	EntityKey     string
	UpdatedAt     time.Time
	SourceEventID string
	Tombstoned    bool
}

// RelayProjectionMetaRepository persists projection ordering metadata.
type RelayProjectionMetaRepository interface {
	Get(ctx context.Context, stream, entityKey string) (*RelayProjectionMeta, error)
	Upsert(ctx context.Context, meta RelayProjectionMeta) error
	ListByStream(ctx context.Context, stream string) ([]RelayProjectionMeta, error)
}
