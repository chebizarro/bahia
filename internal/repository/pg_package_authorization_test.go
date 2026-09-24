package repository

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/pashagolub/pgxmock/v5"
	"github.com/stretchr/testify/require"
)

func TestPackageRequestClaimIsInsertOnlyAndFingerprintBound(t *testing.T) {
	for _, result := range []string{"fresh", "replay", "conflict", "failure"} {
		t.Run(result, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()
			r := newPgPackageControlPlaneRepositoryWithDB(mock)
			c := PackageRequestClaim{Requester: "requester", Method: "package/publish", Token: "key", EventID: "event", Fingerprint: "hash"}
			expect := mock.ExpectExec("INSERT INTO package_request_claims .*ON CONFLICT DO NOTHING").WithArgs(c.Requester, c.Method, c.Token, c.EventID, c.Fingerprint)
			switch result {
			case "fresh":
				expect.WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
			case "failure":
				expect.WillReturnError(errors.New("database unavailable"))
			default:
				expect.WillReturnResult(pgconn.NewCommandTag("INSERT 0 0"))
				hash := c.Fingerprint
				if result == "conflict" {
					hash = "other"
				}
				mock.ExpectQuery("SELECT requester, method, token, event_id, fingerprint, completed FROM package_request_claims").WithArgs(c.Requester, c.Method, c.Token).
					WillReturnRows(pgxmock.NewRows([]string{"requester", "method", "token", "event_id", "fingerprint", "completed"}).AddRow(c.Requester, c.Method, c.Token, "original-event", hash, true))
			}
			claim, fresh, err := r.ClaimPackageRequest(t.Context(), c)
			if result == "conflict" || result == "failure" {
				require.Error(t, err)
				require.False(t, fresh)
			} else {
				require.NoError(t, err)
				require.Equal(t, result == "fresh", fresh)
				if !fresh {
					require.Equal(t, "original-event", claim.EventID)
					require.True(t, claim.Completed)
				}
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestPackageApprovalConsumptionChecksAllBindingsAtomically(t *testing.T) {
	for _, found := range []bool{true, false} {
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		r := newPgPackageControlPlaneRepositoryWithDB(mock)
		id := uuid.New()
		expect := mock.ExpectQuery(`UPDATE package_approvals SET consumed_at=clock_timestamp\(\).*WHERE id=\$1 AND requester=\$2 AND method=\$3 AND plan_hash=\$4.*AND approver=ANY\(\$5::text\[\]\) AND approver<>requester AND consumed_at IS NULL.*AND created_at<=clock_timestamp\(\) AND expires_at>clock_timestamp\(\).*RETURNING approver`).WithArgs(id, "requester", "package/promote", "plan-hash", []string{"approver"})
		if found {
			expect.WillReturnRows(pgxmock.NewRows([]string{"approver"}).AddRow("approver"))
		} else {
			expect.WillReturnError(pgx.ErrNoRows)
		}
		approver, err := r.ConsumePackageApproval(t.Context(), id, "requester", "package/promote", "plan-hash", []string{"approver"})
		if found {
			require.NoError(t, err)
			require.Equal(t, "approver", approver)
		} else {
			require.ErrorIs(t, err, ErrPackageApprovalInvalid)
			require.Empty(t, approver)
		}
		require.NoError(t, mock.ExpectationsWereMet())
		mock.Close()
	}
}

func TestPackageProjectionZeroRowsIsNotSuccess(t *testing.T) {
	for _, artifact := range []bool{true, false} {
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		r := newPgPackageControlPlaneRepositoryWithDB(mock)
		n := 19
		if artifact {
			n = 22
		}
		args := make([]any, n)
		for i := range args {
			args[i] = pgxmock.AnyArg()
		}
		if artifact {
			mock.ExpectExec("INSERT INTO package_artifacts_projection").WithArgs(args...).WillReturnResult(pgconn.NewCommandTag("INSERT 0 0"))
			err = r.UpsertArtifact(t.Context(), &domain.PackageArtifact{ID: uuid.New(), CreatedAt: time.Now(), UpdatedAt: time.Now()})
		} else {
			mock.ExpectExec("INSERT INTO package_publications_projection").WithArgs(args...).WillReturnResult(pgconn.NewCommandTag("INSERT 0 0"))
			err = r.UpsertPublication(t.Context(), &domain.PackagePublication{ID: uuid.New(), CreatedAt: time.Now(), UpdatedAt: time.Now()})
		}
		require.ErrorIs(t, err, ErrStaleRevision)
		require.NoError(t, mock.ExpectationsWereMet())
		mock.Close()
	}
}
