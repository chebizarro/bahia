package fipsbridge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHostsWriterPreservesManualEntriesAndFormatsManagedSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hosts")
	initial := strings.Join([]string{
		"manual.fips  npub1manual",
		"",
		DefaultManagedSectionMarker,
		"old.fips  npub1old",
		DefaultManagedSectionMarker + " end",
		"",
		"custom.fips  npub1custom",
		"",
	}, "\n")
	require.NoError(t, os.WriteFile(path, []byte(initial), 0o600))

	writer := NewHostsWriter(path, DefaultManagedSectionMarker)
	require.NoError(t, writer.Write(map[string]string{
		"embeddings":     "npub1embeddings",
		"drydock-review": "npub1drydock",
	}))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	content := string(data)
	require.Contains(t, content, "manual.fips  npub1manual")
	require.Contains(t, content, "custom.fips  npub1custom")
	require.NotContains(t, content, "old.fips  npub1old")
	require.Contains(t, content, DefaultManagedSectionMarker+"\n")
	require.Contains(t, content, "drydock-review.fips  npub1drydock\n")
	require.Contains(t, content, "embeddings.fips  npub1embeddings\n")
	require.Contains(t, content, DefaultManagedSectionMarker+" end\n")
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestHostsWriterCreatesFileAtomicallyWhenMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "hosts")
	writer := NewHostsWriter(path, "# test-managed")
	require.NoError(t, writer.Write(map[string]string{"api": "npub1api"}))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "# test-managed\napi.fips  npub1api\n# test-managed end\n", string(data))
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".bahia-hosts-*"))
	require.NoError(t, err)
	require.Empty(t, matches)
}

func TestHostsWriterReplacesExistingManagedSectionOnSubsequentWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts")
	writer := NewHostsWriter(path, DefaultManagedSectionMarker)
	require.NoError(t, writer.Write(map[string]string{"api": "npub1api"}))
	require.NoError(t, writer.Write(map[string]string{"worker": "npub1worker"}))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	content := string(data)
	require.NotContains(t, content, "api.fips  npub1api")
	require.Contains(t, content, "worker.fips  npub1worker")
	require.Equal(t, 1, strings.Count(content, DefaultManagedSectionMarker+"\n"))
	require.Equal(t, 1, strings.Count(content, DefaultManagedSectionMarker+" end\n"))
}
