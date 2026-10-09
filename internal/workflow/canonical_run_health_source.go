package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
)

// CanonicalRunHealthSource reads the retained, service-authored deployment-run
// cp-state records. The caller must supply the relay-hydrated local event
// repository, never the optional PostgreSQL nostr_events index.
type CanonicalRunHealthSource struct {
	events repository.NostrEventRepository
	author string
}

const canonicalRunHealthScanLimit = 100001

func NewCanonicalRunHealthSource(events repository.NostrEventRepository, author string) *CanonicalRunHealthSource {
	return &CanonicalRunHealthSource{events: events, author: author}
}

func (s *CanonicalRunHealthSource) ListNonTerminal(ctx context.Context) ([]domain.DeploymentRun, error) {
	if s.events == nil || s.author == "" {
		return nil, fmt.Errorf("canonical run health source is not configured")
	}
	records, err := s.events.FindByTag(ctx, "t", kinds.CPStateTopicDeploymentRun, []int{kinds.CASControlState}, canonicalRunHealthScanLimit)
	if err != nil {
		return nil, err
	}
	if len(records) == canonicalRunHealthScanLimit {
		return nil, fmt.Errorf("canonical deployment-run scan exceeds %d records", canonicalRunHealthScanLimit-1)
	}
	runs := make([]domain.DeploymentRun, 0, len(records))
	seen := make(map[uuid.UUID]struct{}, len(records))
	for _, record := range records {
		run, deleted, err := s.decode(record)
		if err != nil {
			return nil, err
		}
		if run == nil || deleted {
			continue
		}
		if _, exists := seen[run.ID]; exists {
			return nil, fmt.Errorf("multiple canonical deployment-run records for %s", run.ID)
		}
		seen[run.ID] = struct{}{}
		if !isTerminalRunStatus(run.Status) {
			runs = append(runs, *run)
		}
	}
	return runs, nil
}

func (s *CanonicalRunHealthSource) GetByID(ctx context.Context, id uuid.UUID) (*domain.DeploymentRun, error) {
	if s.events == nil || s.author == "" || id == uuid.Nil {
		return nil, fmt.Errorf("canonical run health source is not configured")
	}
	records, err := s.events.FindByTag(ctx, "d", id.String(), []int{kinds.CASControlState}, canonicalRunHealthScanLimit)
	if err != nil {
		return nil, err
	}
	if len(records) == canonicalRunHealthScanLimit {
		return nil, fmt.Errorf("canonical deployment-run coordinate scan exceeds %d records", canonicalRunHealthScanLimit-1)
	}
	var found *domain.DeploymentRun
	for _, record := range records {
		run, deleted, err := s.decode(record)
		if err != nil {
			return nil, err
		}
		if run == nil || deleted {
			continue
		}
		if run.ID != id || found != nil {
			return nil, fmt.Errorf("canonical deployment-run coordinate mismatch for %s", id)
		}
		found = run
	}
	return found, nil
}

func (s *CanonicalRunHealthSource) decode(record repository.NostrEventRecord) (*domain.DeploymentRun, bool, error) {
	if record.PubKey != s.author {
		return nil, false, nil
	}
	var tags [][]string
	if err := json.Unmarshal(record.Tags, &tags); err != nil {
		return nil, false, fmt.Errorf("decode canonical deployment-run tags: %w", err)
	}
	values := make(map[string]string, len(tags))
	for _, tag := range tags {
		if len(tag) >= 2 {
			values[tag[0]] = tag[1]
		}
	}
	if values["t"] != kinds.CPStateTopicDeploymentRun {
		return nil, false, nil
	}
	if values[kinds.CASControlStateTagLegacyKind] != strconv.Itoa(kinds.DeploymentRunRegistry) ||
		values[kinds.CASControlStateTagSchema] != kinds.CASControlStateSchema {
		return nil, false, fmt.Errorf("invalid canonical deployment-run envelope %s", record.ID)
	}
	id, err := uuid.Parse(values[kinds.CASControlStateTagD])
	if err != nil || id == uuid.Nil {
		return nil, false, fmt.Errorf("invalid canonical deployment-run coordinate %s", record.ID)
	}
	if values[kinds.CASControlStateTagDeleted] == "true" {
		return &domain.DeploymentRun{ID: id}, true, nil
	}
	if values[kinds.CASControlStateTagDeleted] != "false" {
		return nil, false, fmt.Errorf("invalid canonical deployment-run deletion tag %s", record.ID)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(record.Content), &payload); err != nil {
		return nil, false, fmt.Errorf("decode canonical deployment-run %s: %w", record.ID, err)
	}
	// The canonical registry producer represents an absent unit as "", while
	// DeploymentRun uses a UUID pointer. Normalize only that producer shape.
	if bytes.Equal(bytes.TrimSpace(payload["deployment_unit_id"]), []byte(`""`)) {
		payload["deployment_unit_id"] = json.RawMessage("null")
	}
	normalized, err := json.Marshal(payload)
	if err != nil {
		return nil, false, fmt.Errorf("normalize canonical deployment-run %s: %w", record.ID, err)
	}
	var run domain.DeploymentRun
	if err := json.Unmarshal(normalized, &run); err != nil {
		return nil, false, fmt.Errorf("decode canonical deployment-run %s: %w", record.ID, err)
	}
	if run.ID != id || values["run"] != id.String() || run.CreatedAt.IsZero() ||
		(values["status"] != "" && values["status"] != string(run.Status)) {
		return nil, false, fmt.Errorf("invalid canonical deployment-run payload %s", record.ID)
	}
	switch run.Status {
	case domain.RunStatusQueued, domain.RunStatusRunning, domain.RunStatusSucceeded,
		domain.RunStatusFailed, domain.RunStatusCancelled, domain.RunStatusTimeout:
	default:
		return nil, false, fmt.Errorf("invalid canonical deployment-run status %s", record.ID)
	}
	return &run, false, nil
}
