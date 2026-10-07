package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
)

// resolveCreateByID is the create-path identity rule
// (docs/event-spec.md "Entity identity and coordinates") for one domain:
//   - no entity stored under requested's id: (nil, nil), the caller creates;
//   - an entity with the same content: (stored, nil), an idempotent replay;
//   - an entity with different content: *domain.EntityIDConflictError.
//
// get loads by id and may report absence as (nil, nil) or
// repository.ErrNotFound. fingerprint encodes the declared content (no id, no
// server-stamped timestamps) after write-path normalization.
func resolveCreateByID[T any](ctx context.Context, entity string, id uuid.UUID, requested *T, get func(context.Context, uuid.UUID) (*T, error), fingerprint func(*T) []byte) (*T, error) {
	if id == uuid.Nil || get == nil {
		return nil, nil
	}
	stored, err := get(ctx, id)
	if errors.Is(err, repository.ErrNotFound) || (err == nil && stored == nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("checking %s id %s: %w", entity, id, err)
	}
	if !bytes.Equal(fingerprint(stored), fingerprint(requested)) {
		return nil, &domain.EntityIDConflictError{Entity: entity, ID: id}
	}
	return stored, nil
}

// canonicalCreateContent encodes an entity's declared content for
// resolveCreateByID: its JSON form without the id and server-stamped
// timestamps, with empty values (null, "", false, 0, {} and []) pruned at
// every level. A Go value round-tripped through a database row then compares
// equal to the intent that created it even where the row turns a nil map or
// pointer into an empty one; for Go structs an empty value and an absent one
// decode identically, so pruning loses nothing the entity declares.
func canonicalCreateContent(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil
	}
	if object, ok := decoded.(map[string]any); ok {
		delete(object, "id")
		delete(object, "created_at")
		delete(object, "updated_at")
	}
	out, _ := json.Marshal(pruneEmptyJSON(decoded))
	return out
}

func pruneEmptyJSON(v any) any {
	switch typed := v.(type) {
	case map[string]any:
		for key, value := range typed {
			if pruned := pruneEmptyJSON(value); pruned == nil {
				delete(typed, key)
			} else {
				typed[key] = pruned
			}
		}
		if len(typed) == 0 {
			return nil
		}
		return typed
	case []any:
		if len(typed) == 0 {
			return nil
		}
		for i, value := range typed {
			typed[i] = pruneEmptyJSON(value)
		}
		return typed
	case string:
		if typed == "" {
			return nil
		}
	case bool:
		if !typed {
			return nil
		}
	case json.Number:
		if f, err := typed.Float64(); err == nil && f == 0 {
			return nil
		}
	}
	return v
}
