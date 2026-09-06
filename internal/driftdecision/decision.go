package driftdecision

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// ArtifactSource is the repository capability needed to resolve the desired
// artifact digest for a runtime drift decision.
type ArtifactSource interface {
	GetByID(context.Context, uuid.UUID) (*domain.Artifact, error)
}

// DesiredArtifactDigest resolves and normalizes the desired artifact digest.
func DesiredArtifactDigest(ctx context.Context, artifacts ArtifactSource, artifactID *uuid.UUID, logger *zap.Logger) string {
	if artifacts == nil || artifactID == nil || *artifactID == uuid.Nil {
		return ""
	}
	desired, err := artifacts.GetByID(ctx, *artifactID)
	if err != nil {
		logger.Error("failed to fetch desired artifact for drift check",
			zap.String("artifact_id", artifactID.String()),
			zap.Error(err),
		)
		return ""
	}
	if desired == nil {
		return ""
	}
	return domain.NormalizeImageDigest(desired.ImageDigest)
}

// LogInput contains the common structured fields for a runtime drift decision.
type LogInput struct {
	Service        string
	Environment    string
	ServiceID      uuid.UUID
	EnvironmentID  uuid.UUID
	Status         domain.DriftStatus
	PreviousStatus domain.DriftStatus
	Branch         string
	DesiredHash    string
	ObservedHash   string
	DesiredDigest  string
	ObservedDigest string
	Health         domain.HealthStatus
	ObservationID  uuid.UUID
	Source         string
}

// Log emits one consistently shaped drift-decision record.
func Log(logger *zap.Logger, input LogInput) {
	fields := []zap.Field{
		zap.String("service", input.Service),
		zap.String("environment", input.Environment),
		zap.String("service_id", input.ServiceID.String()),
		zap.String("environment_id", input.EnvironmentID.String()),
		zap.String("status", string(input.Status)),
		zap.String("branch", input.Branch),
		zap.Bool("desired_hash_present", strings.TrimSpace(input.DesiredHash) != ""),
		zap.Bool("observed_hash_present", strings.TrimSpace(input.ObservedHash) != ""),
		zap.String("desired_hash_prefix", prefix(input.DesiredHash, 12)),
		zap.String("observed_hash_prefix", prefix(input.ObservedHash, 12)),
		zap.String("desired_digest_prefix", prefix(input.DesiredDigest, 12)),
		zap.String("observed_digest_prefix", prefix(input.ObservedDigest, 12)),
		zap.String("health", string(input.Health)),
		zap.String("observation_source", input.Source),
		zap.String("observation_id", input.ObservationID.String()),
	}
	if input.PreviousStatus == domain.DriftStatusInSync {
		logger.Warn("runtime drift decision changed from in_sync", fields...)
		return
	}
	logger.Info("runtime drift decision", fields...)
}

func prefix(value string, length int) string {
	value = strings.TrimSpace(value)
	if len(value) <= length {
		return value
	}
	return value[:length]
}
