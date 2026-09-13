package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
)

type UsageLedgerRepository interface {
	Insert(ctx context.Context, record *domain.UsageLedgerRecord) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.UsageLedgerRecord, error)
	List(ctx context.Context, filter domain.UsageLedgerFilter) ([]domain.UsageLedgerRecord, error)
	GetCorrections(ctx context.Context, originalID uuid.UUID) ([]domain.UsageLedgerRecord, error)
	SumByAgent(ctx context.Context, agentPubkey string, resourceType domain.UsageResourceType, since, until time.Time) (int64, error)
}

type UsageLedgerService struct {
	repo UsageLedgerRepository
}

func NewUsageLedgerService(repo UsageLedgerRepository) *UsageLedgerService {
	return &UsageLedgerService{repo: repo}
}

func (s *UsageLedgerService) RecordUsage(ctx context.Context, record *domain.UsageLedgerRecord) error {
	if err := domain.ValidateUsageLedgerRecord(record); err != nil {
		return err
	}
	if record.CorrectionOf != nil {
		original, err := s.repo.GetByID(ctx, *record.CorrectionOf)
		if err != nil {
			return fmt.Errorf("checking original record for correction: %w", err)
		}
		if original == nil {
			return fmt.Errorf("original usage record %s not found: %w", *record.CorrectionOf, repository.ErrNotFound)
		}
	}
	return s.repo.Insert(ctx, record)
}

func (s *UsageLedgerService) GetRecord(ctx context.Context, id uuid.UUID) (*domain.UsageLedgerRecord, error) {
	return s.repo.GetByID(ctx, id)
}

func (s *UsageLedgerService) QueryRecords(ctx context.Context, filter domain.UsageLedgerFilter) ([]domain.UsageLedgerRecord, error) {
	return s.repo.List(ctx, filter)
}

func (s *UsageLedgerService) GetCorrections(ctx context.Context, originalID uuid.UUID) ([]domain.UsageLedgerRecord, error) {
	return s.repo.GetCorrections(ctx, originalID)
}

func (s *UsageLedgerService) UsageForAgent(ctx context.Context, agentPubkey string, resourceType domain.UsageResourceType, since, until time.Time) (int64, error) {
	base, err := s.repo.SumByAgent(ctx, agentPubkey, resourceType, since, until)
	if err != nil {
		return 0, err
	}
	if base == 0 {
		return 0, nil
	}

	corrections, err := s.repo.List(ctx, domain.UsageLedgerFilter{
		AgentPubkey:  agentPubkey,
		ResourceType: resourceType,
		Since:        since,
		Until:        until,
		Limit:        10000,
	})
	if err != nil {
		return 0, err
	}

	correctionDelta := int64(0)
	correctedIDs := map[uuid.UUID]bool{}
	for _, c := range corrections {
		if c.CorrectionOf != nil {
			if !correctedIDs[*c.CorrectionOf] {
				correctedIDs[*c.CorrectionOf] = true
				original, err := s.repo.GetByID(ctx, *c.CorrectionOf)
				if err != nil {
					return 0, err
				}
				if original != nil {
					correctionDelta += c.Amount - original.Amount
				} else {
					correctionDelta += c.Amount
				}
			}
		}
	}
	return base + correctionDelta, nil
}

func (s *UsageLedgerService) CorrectUsage(ctx context.Context, originalID uuid.UUID, correctAmount int64, recordedBy, signature string) (*domain.UsageLedgerRecord, error) {
	original, err := s.repo.GetByID(ctx, originalID)
	if err != nil {
		return nil, err
	}
	if original == nil {
		return nil, fmt.Errorf("original usage record %s not found: %w", originalID, repository.ErrNotFound)
	}

	correctionID := uuid.New()
	now := time.Now().UTC()
	correction := &domain.UsageLedgerRecord{
		ID:           correctionID,
		AgentPubkey:  original.AgentPubkey,
		TaskID:       original.TaskID,
		ResourceType: original.ResourceType,
		Amount:       correctAmount,
		RecordedAt:   original.RecordedAt,
		RecordedBy:   recordedBy,
		Signature:    signature,
		CorrectionOf: &originalID,
		CreatedAt:    now,
	}
	if err := s.repo.Insert(ctx, correction); err != nil {
		return nil, err
	}
	return correction, nil
}
