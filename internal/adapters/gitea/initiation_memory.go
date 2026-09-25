package gitea

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/openagentsinc/bahia/internal/controlplane"
)

// MemoryInitiationStore supports explicit in-memory fixtures, including adapter
// integration tests in other packages. Production uses PgInitiationStore; there
// is no implicit process-local fallback.
type MemoryInitiationStore struct {
	mu      sync.Mutex
	records map[string][]byte
}

func NewMemoryInitiationStore() *MemoryInitiationStore {
	return &MemoryInitiationStore{records: make(map[string][]byte)}
}

func (s *MemoryInitiationStore) Claim(_ context.Context, req controlplane.HiveCIBuildStartRequest) (*InitiationRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, err := newInitiationRecord(req)
	if err != nil {
		return nil, false, err
	}
	if data, ok := s.records[rec.SourceEventID]; ok {
		var existing InitiationRecord
		if err := json.Unmarshal(data, &existing); err != nil {
			return nil, false, err
		}
		if existing.Fingerprint != rec.Fingerprint {
			return nil, false, fmt.Errorf("source event conflicts with its canonical build initiation")
		}
		return &existing, false, nil
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return nil, false, err
	}
	s.records[rec.SourceEventID] = data
	return rec, true, nil
}

func (s *MemoryInitiationStore) Get(_ context.Context, key string) (*InitiationRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.records[key]
	if !ok {
		return nil, nil
	}
	var rec InitiationRecord
	err := json.Unmarshal(data, &rec)
	return &rec, err
}

func (s *MemoryInitiationStore) Advance(_ context.Context, from InitiationStage, rec *InitiationRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var current InitiationRecord
	if err := json.Unmarshal(s.records[rec.SourceEventID], &current); err != nil {
		return err
	}
	if current.Stage != from || current.Request.BuildID != rec.Request.BuildID {
		return ErrInitiationConflict
	}
	if !validInitiationTransition(from, rec.Stage) {
		return fmt.Errorf("invalid transition")
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	s.records[rec.SourceEventID] = data
	return nil
}
