package backup

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/secretstore"
	"github.com/stretchr/testify/require"
)

type backupCredentialRecords struct{ ref domain.SecretCredentialRef }

func (r backupCredentialRecords) CurrentReference(context.Context, string) (domain.SecretCredentialRef, error) {
	return r.ref, nil
}

type backupCredentialValues struct {
	secret domain.SecretRef
	value  *secretstore.Value
	calls  int
}

func (v *backupCredentialValues) Resolve(context.Context, uuid.UUID) (secretstore.Resolved, error) {
	v.calls++
	return secretstore.Resolved{Secret: v.secret, Value: v.value}, nil
}

func TestPinnedKopiaCredentialRefusesMovedVersionBeforeDelivery(t *testing.T) {
	id := uuid.New()
	pin := domain.SecretCredentialRef{Store: secretstore.BackendSignet, CredentialID: "cred_" + strings.Repeat("a", 64), Version: 7}
	snapshot := &domain.BackupExecutionSnapshot{Repository: domain.BackupRepository{CredentialProfile: id.String()}, CredentialRef: &pin}
	values := &backupCredentialValues{}
	moved := pin
	moved.Version++
	value, err := ResolvePinnedKopiaCredential(t.Context(), snapshot, backupCredentialRecords{ref: moved}, values)
	require.Nil(t, value)
	require.ErrorIs(t, err, ErrBackupCredentialMoved)
	require.Zero(t, values.calls)
}

func TestPinnedKopiaCredentialReturnsHeldVersionAndCallerZeroizes(t *testing.T) {
	id := uuid.New()
	pin := domain.SecretCredentialRef{Store: secretstore.BackendSignet, CredentialID: "cred_" + strings.Repeat("a", 64), Version: 7}
	snapshot := &domain.BackupExecutionSnapshot{Repository: domain.BackupRepository{CredentialProfile: id.String()}, CredentialRef: &pin}
	held := secretstore.NewValue([]byte(`{"storage":{}}`), time.Now().Add(time.Minute))
	values := &backupCredentialValues{secret: domain.SecretRef{CredentialRef: &pin}, value: held}
	value, err := ResolvePinnedKopiaCredential(t.Context(), snapshot, backupCredentialRecords{ref: pin}, values)
	require.NoError(t, err)
	require.Same(t, held, value)
	require.Equal(t, 1, values.calls)
	value.Zeroize()
	require.Nil(t, held.Bytes())
}

func TestPinnedKopiaCredentialZeroizesMismatchedDelivery(t *testing.T) {
	id := uuid.New()
	pin := domain.SecretCredentialRef{Store: secretstore.BackendSignet, CredentialID: "cred_" + strings.Repeat("a", 64), Version: 7}
	snapshot := &domain.BackupExecutionSnapshot{Repository: domain.BackupRepository{CredentialProfile: id.String()}, CredentialRef: &pin}
	moved := pin
	moved.Version++
	held := secretstore.NewValue([]byte("secret"), time.Now().Add(time.Minute))
	values := &backupCredentialValues{secret: domain.SecretRef{CredentialRef: &moved}, value: held}
	value, err := ResolvePinnedKopiaCredential(t.Context(), snapshot, backupCredentialRecords{ref: pin}, values)
	require.Nil(t, value)
	require.ErrorIs(t, err, ErrBackupCredentialMoved)
	require.Nil(t, held.Bytes())
}
