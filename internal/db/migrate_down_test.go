package db

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"
)

func TestAvailableMigrationsUseFullStem(t *testing.T) {
	files := fstest.MapFS{
		"migrations/000050_alpha.up.sql":   &fstest.MapFile{Data: []byte("SELECT 1")},
		"migrations/000050_beta.up.sql":    &fstest.MapFile{Data: []byte("SELECT 2")},
		"migrations/000050_alpha.down.sql": &fstest.MapFile{Data: []byte("SELECT 3")},
	}
	versions, err := availableMigrations(files)
	require.NoError(t, err)
	require.Equal(t, []string{"000050_alpha", "000050_beta"}, versions)
}
