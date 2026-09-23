package repository

import (
	"context"
	"testing"

	"github.com/pashagolub/pgxmock/v5"
	"github.com/stretchr/testify/require"
)

// Keep the negative controls used by repository tests strict across mock upgrades.
func TestPGXMockExpectationEnforcement(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		query string
		arg   int
	}{
		{"wrong SQL", "DELETE FROM records WHERE id = $1", 1},
		{"wrong argument", "UPDATE records SET enabled = true WHERE id = $1", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock, err := pgxmock.NewConn()
			require.NoError(t, err)
			mock.ExpectExec(`^UPDATE records SET enabled = true WHERE id = \$1$`).WithArgs(1).
				WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			_, err = mock.Exec(ctx, tc.query, tc.arg)
			require.Error(t, err)
			require.Error(t, mock.ExpectationsWereMet())
		})
	}

	t.Run("unmet required expectation", func(t *testing.T) {
		mock, err := pgxmock.NewConn()
		require.NoError(t, err)
		mock.ExpectBegin()
		require.Error(t, mock.ExpectationsWereMet())
	})
	t.Run("unexpected operation", func(t *testing.T) {
		mock, err := pgxmock.NewConn()
		require.NoError(t, err)
		_, err = mock.Exec(ctx, "DELETE FROM records")
		require.Error(t, err)
	})
	t.Run("default ordering", func(t *testing.T) {
		mock, err := pgxmock.NewConn()
		require.NoError(t, err)
		mock.ExpectBegin()
		mock.ExpectCommit()
		require.Error(t, mock.Commit(ctx))
		require.Error(t, mock.ExpectationsWereMet())
	})
	t.Run("rows must close", func(t *testing.T) {
		mock, err := pgxmock.NewConn()
		require.NoError(t, err)
		mock.ExpectQuery(`^SELECT 1$`).WillReturnRows(pgxmock.NewRows([]string{"n"}).AddRow(1)).RowsWillBeClosed()
		rows, err := mock.Query(ctx, "SELECT 1")
		require.NoError(t, err)
		require.Error(t, mock.ExpectationsWereMet())
		rows.Close()
		require.NoError(t, mock.ExpectationsWereMet())
	})
}
