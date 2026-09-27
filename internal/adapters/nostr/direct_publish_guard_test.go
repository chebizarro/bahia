package nostr

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNoNewDirectNostrPublishBypasses freezes the currently known lower-level
// publishers while they are migrated behind OutboundAdmission. Any new direct
// path fails CI instead of silently widening the relay flood surface.
func TestNoNewDirectNostrPublishBypasses(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", "..", ".."))
	allowed := map[string]int{
		"internal/adapters/signet/client.go":     1,
		"internal/soulfactory/concord_invite.go": 2,
		"internal/soulfactory/relay_bus.go":      2,
	}
	seen := make(map[string]int)
	for _, dir := range []string{"internal/adapters/signet", "internal/soulfactory"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry fs.DirEntry, err error) error {
			require.NoError(t, err)
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, readErr := os.ReadFile(path)
			require.NoError(t, readErr)
			content := string(data)
			count := strings.Count(content, "endpoint.Publish(") +
				strings.Count(content, "relay.Publish(") +
				strings.Count(content, ".PublishMany(")
			if count > 0 {
				rel, relErr := filepath.Rel(root, path)
				require.NoError(t, relErr)
				seen[filepath.ToSlash(rel)] = count
			}
			return nil
		})
		require.NoError(t, err)
	}
	require.Equal(t, allowed, seen,
		"direct Nostr publishing bypass changed; migrate the path behind OutboundAdmission instead of updating this baseline")
}
