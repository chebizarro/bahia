package auth

import (
	"context"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

func TestPGNIP98ReplayStoreClaimUsesAtomicUniqueInsert(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	store := newPGNIP98ReplayStore(mock)
	expiresAt := time.Now().Add(time.Minute)
	mock.ExpectExec("DELETE FROM nip98_replay_claims").WillReturnResult(pgxmock.NewResult("DELETE", 0))
	mock.ExpectExec("INSERT INTO nip98_replay_claims").
		WithArgs("event-id", expiresAt).
		WillReturnResult(pgxmock.NewResult("INSERT", 0))

	claimed, err := store.Claim(context.Background(), "event-id", expiresAt)
	if err != nil {
		t.Fatal(err)
	}
	if claimed {
		t.Fatal("duplicate durable replay claim must not succeed")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
