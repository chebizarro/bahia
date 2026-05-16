package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

const (
	EventMLArtifactChanged    events.EventType = "ml_artifact.changed"
	EventMLProvenanceChanged  events.EventType = "ml_provenance.changed"
	EventMLProvenanceDefected events.EventType = "ml_provenance.defect"

	MLProvenanceEdgeMirrorOf       = "mirror_of"
	MLProvenanceEdgeWorkerVerified = "worker_verified"
)

var ErrMLProvenanceFailedClosed = errors.New("ML provenance validation failed closed")

// MLProvenanceService owns artifact references, provenance edges, and fail-closed digest validation.
type MLProvenanceService struct {
	repo      repository.MLRegistryRepository
	publisher events.Publisher
	logger    *zap.Logger
}

func NewMLProvenanceService(repo repository.MLRegistryRepository, publisher events.Publisher, logger *zap.Logger) *MLProvenanceService {
	if publisher == nil {
		publisher = &events.NoopPublisher{}
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &MLProvenanceService{repo: repo, publisher: publisher, logger: logger}
}

func (s *MLProvenanceService) RegisterArtifactRef(ctx context.Context, artifact *domain.MLArtifactRef) error {
	if err := domain.ValidateMLArtifactRef(artifact); err != nil {
		return err
	}
	artifact.SHA256 = normalizeSHA256(artifact.SHA256)
	if artifact.SHA256 == "" {
		return fmt.Errorf("%w: artifact %s has no sha256 digest", ErrMLProvenanceFailedClosed, artifact.URI)
	}
	if err := s.repo.UpsertArtifactRef(ctx, artifact); err != nil {
		return err
	}
	s.publish(ctx, EventMLArtifactChanged, artifact.ID.String(), map[string]any{"artifact_id": artifact.ID.String(), "uri": artifact.URI, "sha256": artifact.SHA256})
	return nil
}

func (s *MLProvenanceService) RecordProvenanceEdge(ctx context.Context, edge *domain.MLProvenanceEdge) error {
	if edge == nil {
		return fmt.Errorf("ML provenance edge is required")
	}
	if strings.TrimSpace(edge.EdgeKind) == "" {
		return fmt.Errorf("%w: edge_kind must not be empty", domain.ErrEmptyField)
	}
	if edge.FromArtifactID == nil && edge.ToArtifactID == nil && edge.ModelVersionID == nil {
		return fmt.Errorf("%w: provenance edge must reference an artifact or model version", domain.ErrInvalidValue)
	}
	if err := s.repo.UpsertProvenanceEdge(ctx, edge); err != nil {
		return err
	}
	typ := EventMLProvenanceChanged
	if edge.Defect != "" || !edge.Verified {
		typ = EventMLProvenanceDefected
	}
	s.publish(ctx, typ, edge.ID.String(), edge)
	return nil
}

// ValidateModelVersionArtifactMirrors verifies that all artifact mirrors for a model version agree on SHA-256.
func (s *MLProvenanceService) ValidateModelVersionArtifactMirrors(ctx context.Context, modelVersionID uuid.UUID) error {
	artifacts, err := s.repo.ListArtifactRefsByModelVersion(ctx, modelVersionID)
	if err != nil {
		return err
	}
	if len(artifacts) == 0 {
		return fmt.Errorf("%w: model version %s has no artifact refs", ErrMLProvenanceFailedClosed, modelVersionID)
	}
	base := artifacts[0]
	baseDigest := normalizeSHA256(base.SHA256)
	if baseDigest == "" {
		return s.recordMirrorDefect(ctx, modelVersionID, &base, nil, "missing sha256 digest")
	}
	for i := range artifacts {
		artifacts[i].SHA256 = normalizeSHA256(artifacts[i].SHA256)
		if artifacts[i].SHA256 == "" {
			return s.recordMirrorDefect(ctx, modelVersionID, &base, &artifacts[i], "missing sha256 digest")
		}
		if artifacts[i].SHA256 != baseDigest {
			return s.recordMirrorDefect(ctx, modelVersionID, &base, &artifacts[i], fmt.Sprintf("digest mismatch: %s has %s, %s has %s", base.URI, baseDigest, artifacts[i].URI, artifacts[i].SHA256))
		}
		if artifacts[i].ID != base.ID {
			fromID, toID := base.ID, artifacts[i].ID
			edge := &domain.MLProvenanceEdge{FromArtifactID: &fromID, ToArtifactID: &toID, ModelVersionID: &modelVersionID, EdgeKind: MLProvenanceEdgeMirrorOf, Evidence: map[string]any{"sha256": baseDigest, "from_uri": base.URI, "to_uri": artifacts[i].URI}, Verified: true}
			if err := s.RecordProvenanceEdge(ctx, edge); err != nil {
				return err
			}
		}
	}
	return nil
}

// VerifyWorkerReportedDigests compares worker-verified digests against canonical artifact refs before a run advances.
func (s *MLProvenanceService) VerifyWorkerReportedDigests(ctx context.Context, run *domain.MLDeploymentRun, artifacts []domain.MLArtifactRef) error {
	if run == nil {
		return fmt.Errorf("ML deployment run is required")
	}
	if len(artifacts) == 0 {
		return fmt.Errorf("%w: run %s has no artifact refs to verify", ErrMLProvenanceFailedClosed, run.ID)
	}
	if run.VerifiedDigests == nil {
		return s.recordWorkerDefect(ctx, run, nil, "worker reported no verified digests")
	}
	for i := range artifacts {
		want := normalizeSHA256(artifacts[i].SHA256)
		got := normalizeSHA256(run.VerifiedDigests[artifacts[i].URI])
		if got == "" {
			got = normalizeSHA256(run.VerifiedDigests[artifacts[i].ID.String()])
		}
		if want == "" || got == "" || want != got {
			return s.recordWorkerDefect(ctx, run, &artifacts[i], fmt.Sprintf("worker digest mismatch for %s: expected %s got %s", artifacts[i].URI, want, got))
		}
		artifactID := artifacts[i].ID
		edge := &domain.MLProvenanceEdge{ToArtifactID: &artifactID, EdgeKind: MLProvenanceEdgeWorkerVerified, Evidence: map[string]any{"run_id": run.ID.String(), "worker_pubkey": run.WorkerPubkey, "sha256": want}, Verified: true}
		if artifacts[i].ModelVersionID != nil {
			edge.ModelVersionID = artifacts[i].ModelVersionID
		}
		if err := s.RecordProvenanceEdge(ctx, edge); err != nil {
			return err
		}
	}
	return nil
}

func (s *MLProvenanceService) recordMirrorDefect(ctx context.Context, modelVersionID uuid.UUID, base, other *domain.MLArtifactRef, defect string) error {
	edge := &domain.MLProvenanceEdge{ModelVersionID: &modelVersionID, EdgeKind: MLProvenanceEdgeMirrorOf, Evidence: map[string]any{}, Verified: false, Defect: defect}
	if base != nil {
		edge.FromArtifactID = &base.ID
		edge.Evidence["from_uri"] = base.URI
		edge.Evidence["from_sha256"] = normalizeSHA256(base.SHA256)
	}
	if other != nil {
		edge.ToArtifactID = &other.ID
		edge.Evidence["to_uri"] = other.URI
		edge.Evidence["to_sha256"] = normalizeSHA256(other.SHA256)
	}
	if err := s.RecordProvenanceEdge(ctx, edge); err != nil {
		return err
	}
	return fmt.Errorf("%w: %s", ErrMLProvenanceFailedClosed, defect)
}

func (s *MLProvenanceService) recordWorkerDefect(ctx context.Context, run *domain.MLDeploymentRun, artifact *domain.MLArtifactRef, defect string) error {
	edge := &domain.MLProvenanceEdge{EdgeKind: MLProvenanceEdgeWorkerVerified, Evidence: map[string]any{"run_id": run.ID.String(), "worker_pubkey": run.WorkerPubkey}, Verified: false, Defect: defect}
	if artifact != nil {
		edge.ToArtifactID = &artifact.ID
		edge.Evidence["artifact_uri"] = artifact.URI
		edge.Evidence["expected_sha256"] = normalizeSHA256(artifact.SHA256)
		if artifact.ModelVersionID != nil {
			edge.ModelVersionID = artifact.ModelVersionID
		}
	}
	if err := s.RecordProvenanceEdge(ctx, edge); err != nil {
		return err
	}
	return fmt.Errorf("%w: %s", ErrMLProvenanceFailedClosed, defect)
}

func normalizeSHA256(digest string) string {
	digest = strings.ToLower(strings.TrimSpace(digest))
	digest = strings.TrimPrefix(digest, "sha256:")
	return digest
}

func (s *MLProvenanceService) publish(ctx context.Context, typ events.EventType, entityID string, data any) {
	s.publisher.Publish(ctx, events.Event{Type: typ, EntityID: entityID, Data: data})
}
