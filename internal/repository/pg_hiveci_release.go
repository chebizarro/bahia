package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/openagentsinc/bahia/internal/domain"
)

var ErrHiveCIReleaseReplayConflict = errors.New("Hive-CI release replay conflicts with accepted content")

var releaseDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// HiveCIReleaseRepository is the atomic accepted-release identity boundary.
type HiveCIReleaseRepository interface {
	CommitAcceptedRelease(context.Context, domain.HiveCIAcceptedRelease) (domain.HiveCIReleaseCommitResult, error)
}

func (r *PgHiveCIRepository) CommitAcceptedRelease(ctx context.Context, release domain.HiveCIAcceptedRelease) (result domain.HiveCIReleaseCommitResult, err error) {
	if err := validateAcceptedRelease(release); err != nil {
		return result, err
	}
	resultJSON, err := json.Marshal(release.Result)
	if err != nil {
		return result, fmt.Errorf("encode accepted Hive-CI release: %w", err)
	}
	policyJSON, _ := json.Marshal(release.Policy)
	workerJSON, _ := json.Marshal(release.WorkerAdmissionEvidence)
	rollbackJSON, _ := json.Marshal(release.RollbackCompatibility)
	healthJSON, _ := json.Marshal(release.HealthReadinessContracts)

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return result, fmt.Errorf("begin accepted Hive-CI release commit: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if rollbackErr := tx.Rollback(context.Background()); rollbackErr != nil &&
			rollbackErr != pgx.ErrTxClosed && err == nil {
			err = fmt.Errorf("rollback accepted Hive-CI release commit: %w", rollbackErr)
		}
	}()

	tag, err := tx.Exec(ctx, `
		INSERT INTO hiveci_accepted_releases (
			release_identity, result_event_id, content_digest, attestor_pubkey,
			workflow_run_event_id, workflow_path, branch, policy_id,
			manifest_digest, sbom_digest, provenance_digest,
			result_json, signed_event_json, accepted_at,
			policy_snapshot, workflow_run_signed_event, worker_admission_evidence,
			rollback_compatibility, health_readiness_contracts
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, NULLIF($8, '')::uuid,
			$9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19
		)
		ON CONFLICT (release_identity) DO NOTHING
	`, release.Result.ReleaseIdentity, release.ResultEventID, release.ContentDigest, release.Attestor,
		release.Result.Lineage.WorkflowRunEventID, release.Workflow, release.Branch, release.PolicyID,
		release.Result.Manifest.Digest, release.Result.SBOM.Digest, release.Result.Provenance.Digest,
		resultJSON, release.SignedEvent, release.AcceptedAt, policyJSON, release.WorkflowRunSignedEvent,
		workerJSON, rollbackJSON, healthJSON)
	if err != nil {
		return result, fmt.Errorf("insert accepted Hive-CI release: %w", err)
	}
	if tag.RowsAffected() == 1 {
		if err = tx.Commit(ctx); err != nil {
			return result, fmt.Errorf("commit accepted Hive-CI release: %w", err)
		}
		committed = true
		return domain.HiveCIReleaseCommitResult{Release: release}, nil
	}

	var existingDigest string
	if err = tx.QueryRow(ctx, `
		SELECT content_digest
		FROM hiveci_accepted_releases
		WHERE release_identity = $1
		FOR UPDATE
	`, release.Result.ReleaseIdentity).Scan(&existingDigest); err != nil {
		return result, fmt.Errorf("load accepted Hive-CI release replay: %w", err)
	}
	if existingDigest == release.ContentDigest {
		if err = tx.Commit(ctx); err != nil {
			return result, fmt.Errorf("commit accepted Hive-CI release replay: %w", err)
		}
		committed = true
		return domain.HiveCIReleaseCommitResult{Release: release, Replay: true}, nil
	}

	if _, err = tx.Exec(ctx, `
		INSERT INTO hiveci_release_conflicts (
			release_identity, accepted_content_digest, conflicting_content_digest,
			result_event_id, signed_event_json
		) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (release_identity, conflicting_content_digest) DO NOTHING
	`, release.Result.ReleaseIdentity, existingDigest, release.ContentDigest,
		release.ResultEventID, release.SignedEvent); err != nil {
		return result, fmt.Errorf("quarantine conflicting Hive-CI release: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return result, fmt.Errorf("commit conflicting Hive-CI release quarantine: %w", err)
	}
	committed = true
	return result, fmt.Errorf("%w: %s", ErrHiveCIReleaseReplayConflict, release.Result.ReleaseIdentity)
}

func validateAcceptedRelease(release domain.HiveCIAcceptedRelease) error {
	if !strings.HasPrefix(release.Result.ReleaseIdentity, domain.HiveCIReleaseIdentityPrefix) ||
		!releaseDigestPattern.MatchString("sha256:"+strings.TrimPrefix(release.Result.ReleaseIdentity, domain.HiveCIReleaseIdentityPrefix)) ||
		!releaseDigestPattern.MatchString(release.ContentDigest) ||
		!releaseDigestPattern.MatchString(release.Result.Manifest.Digest) ||
		!releaseDigestPattern.MatchString(release.Result.SBOM.Digest) ||
		!releaseDigestPattern.MatchString(release.Result.Provenance.Digest) ||
		release.ResultEventID == "" || release.Attestor == "" || release.SignedEvent == "" ||
		release.AcceptedAt.IsZero() {
		return fmt.Errorf("complete canonical accepted Hive-CI release is required")
	}
	return nil
}

var _ HiveCIReleaseRepository = (*PgHiveCIRepository)(nil)
