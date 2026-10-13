package backup

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/secretstore"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/stretchr/testify/require"
)

type inspectCanonicalKopiaRunner struct {
	output string
	err    error
	call   func([]string, []string)
}

func (r inspectCanonicalKopiaRunner) Run(_ context.Context, _ string, args, env []string) (string, string, error) {
	if r.call != nil {
		r.call(args, env)
	}
	return r.output, "secret-stderr", r.err
}

func TestCanonicalKopiaConfigIsPrivateEphemeralAndTagged(t *testing.T) {
	repo := kopiaRepositoryFixture()
	repo.CredentialProfile = uuid.NewString()
	repo.Metadata = nil
	recipe := kopiaRecipeFixture(repo.ID)
	runID := uuid.New()
	const secret = `{"config":{"storage":{"type":"filesystem","config":{"path":"/private/secret-repository"}}},"password":"secret-password"}`
	value := secretstore.NewValue([]byte(secret), time.Now().Add(time.Minute))
	var path string
	runner := inspectCanonicalKopiaRunner{output: `{"id":"snapshot-1"}`, call: func(args, env []string) {
		require.Contains(t, env, "KOPIA_PASSWORD=secret-password")
		require.Contains(t, env, "KOPIA_USE_KEYRING=false")
		require.Equal(t, []string{"snapshot", "create", "--json", "--tags", "bahia-run:" + runID.String(), "--tags", "bahia-step:snapshot-create", "/srv/data"}, args[2:])
		path = args[1]
		info, err := os.Stat(path)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0600), info.Mode().Perm())
		contents, err := os.ReadFile(path)
		require.NoError(t, err)
		require.JSONEq(t, `{"storage":{"type":"filesystem","config":{"path":"/private/secret-repository"}}}`, string(contents))
		parent, err := os.Stat(strings.TrimSuffix(path, "/repository.config"))
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0700), parent.Mode().Perm())
	}}
	backend := NewKopiaBackend(withKopiaCommandRunner(runner))
	result, err := backend.createCanonicalSnapshot(context.Background(), repo, recipe, runID, value)
	require.NoError(t, err)
	require.Equal(t, "snapshot-1", result.ID)
	_, err = os.Stat(path)
	require.ErrorIs(t, err, os.ErrNotExist)
	value.Zeroize()
	require.Nil(t, value.Bytes())
}

func TestCanonicalKopiaDoesNotEchoCredentialOrCLIOutput(t *testing.T) {
	repo := kopiaRepositoryFixture()
	repo.CredentialProfile = uuid.NewString()
	repo.Metadata = nil
	recipe := kopiaRecipeFixture(repo.ID)
	value := secretstore.NewValue([]byte(`{"config":{},"password":"sensitive-marker"}`), time.Now().Add(time.Minute))
	backend := NewKopiaBackend(withKopiaCommandRunner(inspectCanonicalKopiaRunner{err: errors.New("sensitive-marker"), output: "sensitive-marker"}))
	_, err := backend.createCanonicalSnapshot(context.Background(), repo, recipe, uuid.New(), value)
	require.ErrorIs(t, err, service.ErrBackupBackendExecution)
	require.NotContains(t, err.Error(), "sensitive-marker")
	require.NotContains(t, err.Error(), "secret-stderr")
}

func TestCanonicalKopiaObservesExactCompleteTaggedSnapshot(t *testing.T) {
	repo := kopiaRepositoryFixture()
	repo.CredentialProfile = uuid.NewString()
	repo.Metadata = nil
	runID := uuid.New()
	value := secretstore.NewValue([]byte(`{"config":{"storage":{}},"password":"secret-password"}`), time.Now().Add(time.Minute))
	runner := inspectCanonicalKopiaRunner{output: `[{"id":"snapshot-1","tags":{"tag:bahia-run":"` + runID.String() + `","tag:bahia-step":"snapshot-create"}}]`, call: func(args, env []string) {
		require.Equal(t, []string{"snapshot", "list", "--json", "--all", "--incomplete", "--tags", "bahia-run:" + runID.String(), "--tags", "bahia-step:snapshot-create"}, args[2:])
	}}
	backend := NewKopiaBackend(withKopiaCommandRunner(runner))
	result, found, err := backend.observeCanonicalSnapshot(context.Background(), repo, runID, value)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "snapshot-1", result.ID)

	backend = NewKopiaBackend(withKopiaCommandRunner(inspectCanonicalKopiaRunner{output: `[]`}))
	_, found, err = backend.observeCanonicalSnapshot(context.Background(), repo, runID, value)
	require.NoError(t, err)
	require.False(t, found)

	backend = NewKopiaBackend(withKopiaCommandRunner(inspectCanonicalKopiaRunner{output: `[{"id":"snapshot-1","incomplete":"interrupted","tags":{"tag:bahia-run":"` + runID.String() + `","tag:bahia-step":"snapshot-create"}}]`}))
	_, _, err = backend.observeCanonicalSnapshot(context.Background(), repo, runID, value)
	require.ErrorIs(t, err, service.ErrBackupBackendExecution)
}

func TestCanonicalKopiaRejectsLegacyConfigPath(t *testing.T) {
	repo := kopiaRepositoryFixture()
	repo.CredentialProfile = uuid.NewString()
	repo.Metadata = map[string]any{"kopia_config_file": "/some/path"}
	value := secretstore.NewValue([]byte(`{"config":{},"password":"secret-password"}`), time.Now().Add(time.Minute))
	backend := NewKopiaBackend(withKopiaCommandRunner(inspectCanonicalKopiaRunner{}))
	_, _, err := backend.observeCanonicalSnapshot(context.Background(), repo, uuid.New(), value)
	require.ErrorIs(t, err, service.ErrBackupBackendConfiguration)
}

func TestCanonicalKopiaRefusesMissingSignetPassword(t *testing.T) {
	repo := kopiaRepositoryFixture()
	repo.CredentialProfile = uuid.NewString()
	repo.Metadata = nil
	value := secretstore.NewValue([]byte(`{"config":{"storage":{}}}`), time.Now().Add(time.Minute))
	called := false
	backend := NewKopiaBackend(withKopiaCommandRunner(inspectCanonicalKopiaRunner{call: func([]string, []string) { called = true }}))
	_, _, err := backend.observeCanonicalSnapshot(context.Background(), repo, uuid.New(), value)
	require.ErrorIs(t, err, service.ErrBackupBackendConfiguration)
	require.False(t, called, "a host keyring or ambient password must not be consulted")
}
