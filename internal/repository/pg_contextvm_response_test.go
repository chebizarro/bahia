package repository

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestPgContextVMResponseStorePutGetAndDelete(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	store := newPgContextVMResponseStoreWithDB(mock)
	createdAt := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	record := ContextVMResponseRecord{
		RequesterPubkey: "requester",
		Method:          "service/deploy",
		ProgressToken:   "deploy-1",
		Response:        []byte(`{"jsonrpc":"2.0","id":1,"result":{"accepted":true}}`),
		CreatedAt:       createdAt,
	}

	mock.ExpectExec("INSERT INTO contextvm_responses").
		WithArgs(record.RequesterPubkey, record.Method, record.ProgressToken, record.Response, createdAt).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	require.NoError(t, store.Put(ctx, record))

	createdAfter := createdAt.Add(-time.Hour)
	mock.ExpectQuery("FROM contextvm_responses").
		WithArgs(record.RequesterPubkey, record.Method, record.ProgressToken, createdAfter).
		WillReturnRows(pgxmock.NewRows([]string{"requester_pubkey", "method", "progress_token", "response", "created_at"}).
			AddRow(record.RequesterPubkey, record.Method, record.ProgressToken, record.Response, createdAt))
	got, err := store.Get(ctx, record.RequesterPubkey, record.Method, record.ProgressToken, createdAfter)
	require.NoError(t, err)
	require.Equal(t, &record, got)

	cutoff := createdAt.Add(24 * time.Hour)
	mock.ExpectExec("DELETE FROM contextvm_responses").WithArgs(cutoff).WillReturnResult(pgconn.NewCommandTag("DELETE 1"))
	deleted, err := store.DeleteCreatedBefore(ctx, cutoff)
	require.NoError(t, err)
	require.EqualValues(t, 1, deleted)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgContextVMResponseStoreGetMissing(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	store := newPgContextVMResponseStoreWithDB(mock)
	createdAfter := time.Now().UTC().Add(-24 * time.Hour)
	mock.ExpectQuery("FROM contextvm_responses").
		WithArgs("requester", "service/deploy", "missing", createdAfter).
		WillReturnRows(pgxmock.NewRows([]string{"requester_pubkey", "method", "progress_token", "response", "created_at"}))

	got, err := store.Get(context.Background(), "requester", "service/deploy", "missing", createdAfter)
	require.NoError(t, err)
	require.Nil(t, got)
	require.NoError(t, mock.ExpectationsWereMet())
}
