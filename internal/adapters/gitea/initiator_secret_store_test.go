package gitea

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// repository credentials are secret values held only in the
// PostgreSQL secret store, so a daemon without it fails an initiation closed
// at credential resolution, with no mirror call and no publication, and
// leaves the journal record claimed. Nothing else in initiation needs the
// store: the same request completes once credentials resolve again.
func TestInitiationWithoutSecretStoreFailsClosedAtCredentialResolutionOnly(t *testing.T) {
	gitea := &fakeGitea{}
	server := httptest.NewServer(gitea.handler(t))
	defer server.Close()

	initiator, publisher, resolver, credentialRef, _ := newConformanceInitiator(t, server)
	// The daemon has no secret store.
	initiator.secrets = SecretStoreUnavailable{}
	req := arcanaStartRequest(credentialRef)

	_, err := initiator.StartHiveCIBuild(context.Background(), req)
	require.True(t, errors.Is(err, ErrSecretStoreUnavailable), "error = %v", err)
	require.Zero(t, gitea.migrateCalls, "no mirror is created before credentials resolve")
	require.Empty(t, publisher.events, "nothing is published before credentials resolve")
	record, err := initiator.store.Get(context.Background(), req.SourceEventID)
	require.NoError(t, err)
	require.NotNil(t, record, "the initiation is journaled without the secret store")
	require.Equal(t, StageClaimed, record.Stage, "a credential failure leaves the initiation claimed, not prepared")

	// The secret store is reachable again: the same signed request prepares
	// and dispatches once.
	initiator.secrets = resolver
	result, err := initiator.StartHiveCIBuild(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, req.BuildID, result.BuildID)
	require.NotEmpty(t, publisher.events)
	record, err = initiator.store.Get(context.Background(), req.SourceEventID)
	require.NoError(t, err)
	require.NotEqual(t, StageClaimed, record.Stage)
}
