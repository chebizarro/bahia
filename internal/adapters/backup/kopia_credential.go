package backup

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/secretstore"
)

// ErrBackupCredentialMoved requires a fresh signed operator decision. A
// credential change cannot silently change the input to an accepted run.
var ErrBackupCredentialMoved = errors.New("backup repository credential version moved; fresh signed run required")

type kopiaCredentialRecords interface {
	CurrentReference(context.Context, string) (domain.SecretCredentialRef, error)
}

type kopiaCredentialValues interface {
	Resolve(context.Context, uuid.UUID) (secretstore.Resolved, error)
}

// ResolvePinnedKopiaCredential binds the signed {credential_id, version} to
// the current canonical secret record, then asks Signet for expected_version.
// The caller must zeroize the returned value after the one Kopia invocation.
func ResolvePinnedKopiaCredential(ctx context.Context, snapshot *domain.BackupExecutionSnapshot, records kopiaCredentialRecords, values kopiaCredentialValues) (*secretstore.Value, error) {
	if snapshot == nil || snapshot.CredentialRef == nil || records == nil || values == nil {
		return nil, fmt.Errorf("bahia-cd0wr: pinned Signet backup credential resolver is unavailable")
	}
	if err := secretstore.ValidateRef(*snapshot.CredentialRef); err != nil {
		return nil, fmt.Errorf("bahia-cd0wr: invalid pinned Signet backup credential: %w", err)
	}
	secretID, err := uuid.Parse(snapshot.Repository.CredentialProfile)
	if err != nil || secretID == uuid.Nil {
		return nil, fmt.Errorf("bahia-cd0wr: backup repository does not name a canonical secret UUID")
	}
	current, err := records.CurrentReference(ctx, secretID.String())
	if err != nil {
		return nil, err
	}
	if current != *snapshot.CredentialRef {
		return nil, ErrBackupCredentialMoved
	}
	resolved, err := values.Resolve(ctx, secretID)
	if err != nil {
		if errors.Is(err, secretstore.ErrVersionMismatch) {
			return nil, errors.Join(ErrBackupCredentialMoved, err)
		}
		return nil, err
	}
	if resolved.Value == nil || resolved.Secret.CredentialRef == nil || *resolved.Secret.CredentialRef != *snapshot.CredentialRef {
		if resolved.Value != nil {
			resolved.Value.Zeroize()
		}
		return nil, ErrBackupCredentialMoved
	}
	current, err = records.CurrentReference(ctx, secretID.String())
	if err != nil || current != *snapshot.CredentialRef {
		resolved.Value.Zeroize()
		if err != nil {
			return nil, err
		}
		return nil, ErrBackupCredentialMoved
	}
	return resolved.Value, nil
}
