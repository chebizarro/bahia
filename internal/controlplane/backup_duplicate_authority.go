package controlplane

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

// A coordinate collision is only an idempotent replay when the row still
// matches the validated request being handled. SQL alone cannot supply the
// identity or effect inputs for a new signed status or an executor call.
func backupDuplicateSourceMatches(existingBy, existingEvent string, existingKind int, existingDTag string, expectedBy, expectedEvent string, expectedKind int, expectedDTag string) bool {
	return expectedEvent != "" &&
		existingBy == expectedBy &&
		existingEvent == expectedEvent &&
		existingKind == expectedKind &&
		existingDTag == expectedDTag
}

func backupDuplicateMetadataMatches(existing, expected map[string]any) bool {
	// Extra SQL keys can also change a backend's execution inputs. A progressed
	// row with additional runtime metadata is conservatively refused on replay
	// until canonical replay supplies an immutable request snapshot.
	if len(existing) != len(expected) {
		return false
	}
	for key, value := range expected {
		stored, ok := existing[key]
		if !ok {
			return false
		}
		wantJSON, wantErr := json.Marshal(value)
		gotJSON, gotErr := json.Marshal(stored)
		if wantErr != nil || gotErr != nil || !bytes.Equal(wantJSON, gotJSON) {
			return false
		}
	}
	return true
}

func backupDuplicateUUIDMatches(existing, expected *uuid.UUID) bool {
	if expected == nil {
		return existing == nil
	}
	return existing != nil && *existing == *expected
}

func backupRunDuplicateMatches(existing, expected *domain.BackupRun, enforceID bool) error {
	if existing == nil || expected == nil ||
		(enforceID && existing.ID != expected.ID) ||
		!backupDuplicateSourceMatches(existing.RequestedBy, existing.RequestEventID, existing.RequestKind, existing.RequestDTag,
			expected.RequestedBy, expected.RequestEventID, expected.RequestKind, expected.RequestDTag) ||
		existing.RecipeID != expected.RecipeID ||
		existing.RepositoryID != expected.RepositoryID ||
		!backupDuplicateUUIDMatches(existing.PolicyID, expected.PolicyID) ||
		existing.Backend != expected.Backend ||
		existing.TargetRef != expected.TargetRef ||
		(expected.VerificationMode != "" && existing.VerificationMode != expected.VerificationMode) ||
		!backupDuplicateMetadataMatches(existing.Metadata, expected.Metadata) {
		return fmt.Errorf("backup run coordinate conflicts with signed request")
	}
	return nil
}

func backupRestoreDuplicateMatches(existing, expected *domain.BackupRestoreRun, enforceID bool) error {
	if existing == nil || expected == nil ||
		(enforceID && existing.ID != expected.ID) ||
		!backupDuplicateSourceMatches(existing.RequestedBy, existing.RequestEventID, existing.RequestKind, existing.RequestDTag,
			expected.RequestedBy, expected.RequestEventID, expected.RequestKind, expected.RequestDTag) ||
		existing.BackupRunID != expected.BackupRunID ||
		existing.RestoreTargetRef != expected.RestoreTargetRef ||
		(expected.RecipeID != uuid.Nil && existing.RecipeID != expected.RecipeID) ||
		(expected.RepositoryID != uuid.Nil && existing.RepositoryID != expected.RepositoryID) ||
		(expected.PolicyID != nil && !backupDuplicateUUIDMatches(existing.PolicyID, expected.PolicyID)) ||
		(expected.SnapshotID != "" && existing.SnapshotID != expected.SnapshotID) ||
		(expected.Backend != "" && existing.Backend != expected.Backend) ||
		!backupDuplicateMetadataMatches(existing.Metadata, expected.Metadata) {
		return fmt.Errorf("backup restore coordinate conflicts with signed request")
	}
	return nil
}

func backupRetentionDuplicateMatches(existing, expected *domain.BackupRetentionRun, enforceID bool) error {
	if existing == nil || expected == nil ||
		(enforceID && existing.ID != expected.ID) ||
		!backupDuplicateSourceMatches(existing.RequestedBy, existing.RequestEventID, existing.RequestKind, existing.RequestDTag,
			expected.RequestedBy, expected.RequestEventID, expected.RequestKind, expected.RequestDTag) ||
		existing.RepositoryID != expected.RepositoryID ||
		!backupDuplicateUUIDMatches(existing.PolicyID, expected.PolicyID) ||
		(expected.Backend != "" && existing.Backend != expected.Backend) ||
		existing.DryRun != expected.DryRun ||
		!backupDuplicateMetadataMatches(existing.Metadata, expected.Metadata) {
		return fmt.Errorf("backup retention coordinate conflicts with signed request")
	}
	return nil
}
