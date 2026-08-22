package repository

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/pashagolub/pgxmock/v4"
)

func acceptedReleaseStoreFixture() domain.HiveCIAcceptedRelease {
	return domain.HiveCIAcceptedRelease{
		Result: domain.HiveCIReleaseResult{
			ReleaseIdentity: domain.HiveCIReleaseIdentityPrefix + strings.Repeat("1", 64),
			Lineage:         domain.HiveCIReleaseLineage{WorkflowRunEventID: strings.Repeat("2", 64)},
			Manifest:        domain.HiveCIReleaseArtifact{Digest: "sha256:" + strings.Repeat("3", 64)},
			SBOM:            domain.HiveCIReleaseArtifact{Digest: "sha256:" + strings.Repeat("4", 64)},
			Provenance:      domain.HiveCIReleaseArtifact{Digest: "sha256:" + strings.Repeat("5", 64)},
		},
		ResultEventID: strings.Repeat("6", 64), Attestor: strings.Repeat("7", 64),
		Workflow: ".gitea/workflows/release.yml", Branch: "main",
		ContentDigest: "sha256:" + strings.Repeat("8", 64),
		SignedEvent:   `{"kind":5402}`, AcceptedAt: time.Unix(1_800_000_000, 0).UTC(),
	}
}

func anyHiveCIReleaseArgs(count int) []any {
	args := make([]any, count)
	for index := range args {
		args[index] = pgxmock.AnyArg()
	}
	return args
}

func TestPgHiveCIReleaseRepositoryCommitNewReplayAndConflict(t *testing.T) {
	t.Run("new", func(t *testing.T) {
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatal(err)
		}
		defer mock.Close()
		repo := &PgHiveCIRepository{pool: mock}
		release := acceptedReleaseStoreFixture()
		mock.ExpectBegin()
		mock.ExpectExec("INSERT INTO hiveci_accepted_releases").
			WithArgs(anyHiveCIReleaseArgs(14)...).
			WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
		mock.ExpectCommit()

		result, err := repo.CommitAcceptedRelease(context.Background(), release)
		if err != nil {
			t.Fatal(err)
		}
		if result.Replay || result.Release.ResultEventID != release.ResultEventID {
			t.Fatalf("unexpected commit: %+v", result)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("exact content replay", func(t *testing.T) {
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatal(err)
		}
		defer mock.Close()
		repo := &PgHiveCIRepository{pool: mock}
		release := acceptedReleaseStoreFixture()
		mock.ExpectBegin()
		mock.ExpectExec("INSERT INTO hiveci_accepted_releases").
			WithArgs(anyHiveCIReleaseArgs(14)...).
			WillReturnResult(pgconn.NewCommandTag("INSERT 0 0"))
		mock.ExpectQuery("SELECT content_digest").
			WithArgs(release.Result.ReleaseIdentity).
			WillReturnRows(pgxmock.NewRows([]string{"content_digest"}).AddRow(release.ContentDigest))
		mock.ExpectCommit()

		result, err := repo.CommitAcceptedRelease(context.Background(), release)
		if err != nil {
			t.Fatal(err)
		}
		if !result.Replay {
			t.Fatalf("exact content replay was not identified: %+v", result)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("conflict quarantined atomically", func(t *testing.T) {
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatal(err)
		}
		defer mock.Close()
		repo := &PgHiveCIRepository{pool: mock}
		release := acceptedReleaseStoreFixture()
		existing := "sha256:" + strings.Repeat("9", 64)
		mock.ExpectBegin()
		mock.ExpectExec("INSERT INTO hiveci_accepted_releases").
			WithArgs(anyHiveCIReleaseArgs(14)...).
			WillReturnResult(pgconn.NewCommandTag("INSERT 0 0"))
		mock.ExpectQuery("SELECT content_digest").
			WithArgs(release.Result.ReleaseIdentity).
			WillReturnRows(pgxmock.NewRows([]string{"content_digest"}).AddRow(existing))
		mock.ExpectExec("INSERT INTO hiveci_release_conflicts").
			WithArgs(anyHiveCIReleaseArgs(5)...).
			WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
		mock.ExpectCommit()

		_, err = repo.CommitAcceptedRelease(context.Background(), release)
		if !errors.Is(err, ErrHiveCIReleaseReplayConflict) {
			t.Fatalf("error = %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}
