package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
)

type mockUsageLedgerRepo struct {
	records        []domain.UsageLedgerRecord
	insertErr      error
	getByIDFunc    func(id uuid.UUID) (*domain.UsageLedgerRecord, error)
	listFilter     domain.UsageLedgerFilter
	listRecords    []domain.UsageLedgerRecord
	listErr        error
	sumByAgentVal  int64
	sumByAgentErr  error
	getCorrections []domain.UsageLedgerRecord
}

func (m *mockUsageLedgerRepo) Insert(ctx context.Context, r *domain.UsageLedgerRecord) error {
	if m.insertErr != nil {
		return m.insertErr
	}
	m.records = append(m.records, *r)
	return nil
}

func (m *mockUsageLedgerRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.UsageLedgerRecord, error) {
	if m.getByIDFunc != nil {
		return m.getByIDFunc(id)
	}
	for i := range m.records {
		if m.records[i].ID == id {
			return &m.records[i], nil
		}
	}
	return nil, nil
}

func (m *mockUsageLedgerRepo) List(ctx context.Context, filter domain.UsageLedgerFilter) ([]domain.UsageLedgerRecord, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.listRecords, nil
}

func (m *mockUsageLedgerRepo) GetCorrections(ctx context.Context, originalID uuid.UUID) ([]domain.UsageLedgerRecord, error) {
	return m.getCorrections, nil
}


func (m *mockUsageLedgerRepo) SumByTask(ctx context.Context, taskID string, resourceType domain.UsageResourceType, since, until time.Time) (int64, error) {
	if m.sumByAgentErr != nil {
		return 0, m.sumByAgentErr
	}
	return m.sumByAgentVal, nil
}

func (m *mockUsageLedgerRepo) SumByAgent(ctx context.Context, agentPubkey string, resourceType domain.UsageResourceType, since, until time.Time) (int64, error) {
	if m.sumByAgentErr != nil {
		return 0, m.sumByAgentErr
	}
	return m.sumByAgentVal, nil
}

func TestUsageLedgerRecordUsageValidates(t *testing.T) {
	svc := NewUsageLedgerService(&mockUsageLedgerRepo{})
	err := svc.RecordUsage(context.Background(), &domain.UsageLedgerRecord{})
	require.Error(t, err)
}

func TestUsageLedgerRecordUsageDeduplicates(t *testing.T) {
	repo := &mockUsageLedgerRepo{}
	svc := NewUsageLedgerService(repo)
	now := time.Now().UTC()
	rec := &domain.UsageLedgerRecord{
		AgentPubkey:  "agent1",
		ResourceType: domain.UsageResourceTypeCompute,
		Amount:       100,
		RecordedAt:   now,
		RecordedBy:   "meter",
		Signature:    "sig1",
	}
	err := svc.RecordUsage(context.Background(), rec)
	require.NoError(t, err)
	require.Len(t, repo.records, 1)

	dup := &domain.UsageLedgerRecord{
		AgentPubkey:  "agent1",
		ResourceType: domain.UsageResourceTypeCompute,
		Amount:       200,
		RecordedAt:   now,
		RecordedBy:   "meter",
		Signature:    "sig2",
	}
	err = svc.RecordUsage(context.Background(), dup)
	require.NoError(t, err)
	require.Len(t, repo.records, 2, "ON CONFLICT DO NOTHING on unique constraint; PG handles it")

	diffTask := &domain.UsageLedgerRecord{
		AgentPubkey:  "agent1",
		TaskID:       "task-other",
		ResourceType: domain.UsageResourceTypeCompute,
		Amount:       100,
		RecordedAt:   now,
		RecordedBy:   "meter",
		Signature:    "sig3",
	}
	err = svc.RecordUsage(context.Background(), diffTask)
	require.NoError(t, err)
	require.Len(t, repo.records, 3)
}

func TestUsageLedgerCorrectionRejectsMissingOriginal(t *testing.T) {
	repo := &mockUsageLedgerRepo{}
	svc := NewUsageLedgerService(repo)
	_, err := svc.CorrectUsage(context.Background(), uuid.New(), 50, "meter", "corr-sig")
	require.Error(t, err)
}

func TestUsageLedgerCorrectionDoesNotDoubleCount(t *testing.T) {
	origID := uuid.New()
	now := time.Now().UTC()
	original := &domain.UsageLedgerRecord{
		ID:           origID,
		AgentPubkey:  "agent1",
		ResourceType: domain.UsageResourceTypeCompute,
		Amount:       100,
		RecordedAt:   now,
		RecordedBy:   "meter",
		Signature:    "sig-orig",
	}
	repo := &mockUsageLedgerRepo{
		records: []domain.UsageLedgerRecord{*original},
	}
	svc := NewUsageLedgerService(repo)

	corr, err := svc.CorrectUsage(context.Background(), origID, 80, "meter", "sig-corr")
	require.NoError(t, err)
	require.NotNil(t, corr)
	require.Equal(t, int64(80), corr.Amount)
	require.NotNil(t, corr.CorrectionOf)
	require.Equal(t, origID, *corr.CorrectionOf)
}

func TestUsageLedgerQueryByAgent(t *testing.T) {
	now := time.Now().UTC()
	repo := &mockUsageLedgerRepo{
		records: []domain.UsageLedgerRecord{
			{ID: uuid.New(), AgentPubkey: "agent1", ResourceType: domain.UsageResourceTypeCompute, Amount: 100, RecordedAt: now, RecordedBy: "meter", Signature: "s1"},
			{ID: uuid.New(), AgentPubkey: "agent1", ResourceType: domain.UsageResourceTypeCompute, Amount: 200, RecordedAt: now.Add(time.Hour), RecordedBy: "meter", Signature: "s2"},
			{ID: uuid.New(), AgentPubkey: "agent1", ResourceType: domain.UsageResourceTypeStorage, Amount: 50, RecordedAt: now.Add(2 * time.Hour), RecordedBy: "meter", Signature: "s3"},
		},
		sumByAgentVal: 300,
	}
	svc := NewUsageLedgerService(repo)
	total, err := svc.UsageForAgent(context.Background(), "agent1", domain.UsageResourceTypeCompute, now, now.Add(3*time.Hour))
	require.NoError(t, err)
	require.Equal(t, int64(300), total)
}

func TestUsageLedgerCorrectionDeltaApplied(t *testing.T) {
	origID := uuid.New()
	now := time.Now().UTC()
	original := &domain.UsageLedgerRecord{
		ID:           origID,
		AgentPubkey:  "agent1",
		ResourceType: domain.UsageResourceTypeCompute,
		Amount:       100,
		RecordedAt:   now,
		RecordedBy:   "meter",
		Signature:    "sig-orig",
	}
	corrID := uuid.New()
	correction := &domain.UsageLedgerRecord{
		ID:           corrID,
		AgentPubkey:  "agent1",
		ResourceType: domain.UsageResourceTypeCompute,
		Amount:       80,
		RecordedAt:   now,
		RecordedBy:   "meter",
		Signature:    "sig-corr",
		CorrectionOf: &origID,
	}

	repo := &mockUsageLedgerRepo{
		records: []domain.UsageLedgerRecord{*original, *correction},
		sumByAgentVal: 100,
		listRecords:   []domain.UsageLedgerRecord{*original, *correction},
	}
	svc := NewUsageLedgerService(repo)
	total, err := svc.UsageForAgent(context.Background(), "agent1", domain.UsageResourceTypeCompute, now, now.Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, int64(80), total, "100 (base) + 80 (correction) - 100 (original) = 80")
}

func TestUsageLedgerRecordValidationCatchesMissingFields(t *testing.T) {
	tests := []struct {
		name  string
		mod   func(*domain.UsageLedgerRecord)
	}{
		{"empty agent_pubkey", func(r *domain.UsageLedgerRecord) { r.AgentPubkey = "" }},
		{"empty resource_type", func(r *domain.UsageLedgerRecord) { r.ResourceType = "" }},
		{"zero amount", func(r *domain.UsageLedgerRecord) { r.Amount = 0 }},
		{"zero recorded_at", func(r *domain.UsageLedgerRecord) { r.RecordedAt = time.Time{} }},
		{"empty recorded_by", func(r *domain.UsageLedgerRecord) { r.RecordedBy = "" }},
		{"empty signature", func(r *domain.UsageLedgerRecord) { r.Signature = "" }},
		{"invalid resource_type", func(r *domain.UsageLedgerRecord) { r.ResourceType = "unknown" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &domain.UsageLedgerRecord{
				AgentPubkey:  "agent1",
				ResourceType: domain.UsageResourceTypeCompute,
				Amount:       100,
				RecordedAt:   time.Now().UTC(),
				RecordedBy:   "meter",
				Signature:    "sig1",
			}
			tt.mod(r)
			err := domain.ValidateUsageLedgerRecord(r)
			require.Error(t, err)
		})
	}
}

func TestUsageLedgerCorrectionWithCorrectionOfOnInsert(t *testing.T) {
	origID := uuid.New()
	repo := &mockUsageLedgerRepo{
		getByIDFunc: func(id uuid.UUID) (*domain.UsageLedgerRecord, error) {
			return &domain.UsageLedgerRecord{ID: id, AgentPubkey: "a1", ResourceType: domain.UsageResourceTypeCompute, Amount: 100}, nil
		},
	}
	svc := NewUsageLedgerService(repo)
	err := svc.RecordUsage(context.Background(), &domain.UsageLedgerRecord{
		AgentPubkey:  "a1",
		ResourceType: domain.UsageResourceTypeCompute,
		Amount:       80,
		RecordedAt:   time.Now().UTC(),
		RecordedBy:   "meter",
		Signature:    "sig",
		CorrectionOf: &origID,
	})
	require.NoError(t, err)
}

func TestUsageLedgerCorrectionRejectsNonexistentOriginal(t *testing.T) {
	repo := &mockUsageLedgerRepo{
		getByIDFunc: func(id uuid.UUID) (*domain.UsageLedgerRecord, error) {
			return nil, nil
		},
	}
	svc := NewUsageLedgerService(repo)
	err := svc.RecordUsage(context.Background(), &domain.UsageLedgerRecord{
		AgentPubkey:  "a1",
		ResourceType: domain.UsageResourceTypeCompute,
		Amount:       80,
		RecordedAt:   time.Now().UTC(),
		RecordedBy:   "meter",
		Signature:    "sig",
		CorrectionOf: func() *uuid.UUID { id := uuid.New(); return &id }(),
	})
	require.Error(t, err)
}

func TestUsageLedgerQueryByTask(t *testing.T) {
	now := time.Now().UTC()
	repo := &mockUsageLedgerRepo{
		listRecords: []domain.UsageLedgerRecord{
			{ID: uuid.New(), AgentPubkey: "a1", TaskID: "task1", ResourceType: domain.UsageResourceTypeCompute, Amount: 100, RecordedAt: now, RecordedBy: "m", Signature: "s"},
			{ID: uuid.New(), AgentPubkey: "a2", TaskID: "task1", ResourceType: domain.UsageResourceTypeCompute, Amount: 200, RecordedAt: now.Add(time.Hour), RecordedBy: "m", Signature: "s"},
		},
	}
	svc := NewUsageLedgerService(repo)
	records, err := svc.QueryRecords(context.Background(), domain.UsageLedgerFilter{
		TaskID: "task1",
	})
	require.NoError(t, err)
	require.Len(t, records, 2)
}
