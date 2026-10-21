package archtest

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/config"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestComposeCanonicalStorageAndOptionalSQL(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "docker-compose.yml"))
	require.NoError(t, err)
	var project struct {
		Services map[string]struct {
			Profiles  []string       `yaml:"profiles"`
			Volumes   []string       `yaml:"volumes"`
			DependsOn map[string]any `yaml:"depends_on"`
		} `yaml:"services"`
		Volumes map[string]any `yaml:"volumes"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &project))
	daemon, ok := project.Services["bahia"]
	require.True(t, ok)
	require.NotContains(t, daemon.DependsOn, "postgres", "SQL availability must not gate relay-first startup")
	require.Contains(t, project.Services["postgres"].Profiles, "derived-sql", "the derived index must be opt-in")
	raw, err = os.ReadFile(filepath.Join(root, "config.compose.yaml"))
	require.NoError(t, err)
	var cfg struct {
		Nostr struct {
			LocalStore config.NostrLocalStoreConfig `yaml:"local_store"`
			Sidecar    struct {
				DataDir string `yaml:"data_dir"`
			} `yaml:"sidecar"`
		} `yaml:"nostr"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &cfg))
	store := cfg.Nostr.LocalStore
	require.True(t, path.IsAbs(store.Path), "container state must not depend on process cwd")
	require.True(t, path.IsAbs(store.OutboxPath))
	stateDir := path.Dir(store.Path)
	require.Equal(t, stateDir, path.Dir(store.ResolvedOutboxPath()), "receipts, containment marker and trust ledger stay beside durable state")
	require.NotEqual(t, stateDir, cfg.Nostr.Sidecar.DataDir, "daemon and relay cannot share bbolt files")
	durableMount := func(volumes []string, directory string) string {
		t.Helper()
		for _, mount := range volumes {
			fields := strings.Split(mount, ":")
			if len(fields) == 2 && fields[1] == directory {
				_, declared := project.Volumes[fields[0]]
				if declared {
					return fields[0]
				}
			}
		}
		t.Fatalf("%s lacks a read-write named volume: %v", directory, volumes)
		return ""
	}
	daemonVolume := durableMount(daemon.Volumes, stateDir)
	relayVolume := durableMount(project.Services["relay"].Volumes, cfg.Nostr.Sidecar.DataDir)
	require.NotEqual(t, daemonVolume, relayVolume)
	dockerfile, err := os.ReadFile(filepath.Join(root, "Dockerfile"))
	require.NoError(t, err)
	user := strings.Index(string(dockerfile), "USER bahia")
	require.Greater(t, user, 0)
	beforeUser := string(dockerfile[:user])
	for _, directory := range []string{stateDir, cfg.Nostr.Sidecar.DataDir} {
		require.Contains(t, beforeUser, directory)
	}
	require.Contains(t, beforeUser, "chown -R bahia:docker /var/lib/bahia", "new named volumes inherit non-root ownership")
}

func TestActiveNostrFirstDeploymentDocs(t *testing.T) {
	root := repoRoot(t)
	compose, err := os.ReadFile(filepath.Join(root, "docker-compose.yml"))
	require.NoError(t, err)
	requiredInputs := regexp.MustCompile(`\$\{([A-Z][A-Z0-9_]*):\?`).FindAllStringSubmatch(string(compose), -1)
	require.NotEmpty(t, requiredInputs)
	for _, name := range []string{"README.md", "DOCKER.md", "docs/deployment.md", "docs/user-guide/getting-started.md"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		require.NoError(t, err)
		text := string(data)
		for _, input := range requiredInputs {
			if !strings.Contains(text, input[1]) {
				t.Errorf("%s must explain required Compose input %s", name, input[1])
			}
		}
		for _, stale := range []string{"waits for postgres", "Web → API:", "installs\nrepository-backed services when PostgreSQL returns", "uses relative URLs (`/api/v1/...`)"} {
			require.NotContains(t, text, stale, name)
		}
	}
}
