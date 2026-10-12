package nostr_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// localStoreSink is the daemon's local event store as the projector's sink.
type localStoreSink struct{ store *localstore.Store }

func (s localStoreSink) PublishProjection(_ context.Context, event gonostr.Event, _ string, _ *uuid.UUID) error {
	_, err := s.store.SaveEvent(event)
	return err
}

func (s localStoreSink) PublishBeforeCommit(_ context.Context, event gonostr.Event, _ string, _ *uuid.UUID) error {
	_, err := s.store.SaveEvent(event)
	return err
}

// The view reads the service and secret records the daemon itself published
// to its local store (no PostgreSQL), decrypts the secret under the org's
// OCK, and never trusts another author's record on the same coordinate.
func TestCanonicalSecretViewReadsOwnRecordsWithoutPostgres(t *testing.T) {
	ctx := context.Background()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "daemon.bolt"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	serviceKey := strings.Repeat("1", 64)
	signer, err := nostrutil.NewLocalKeyer(serviceKey)
	require.NoError(t, err)
	servicePub, err := signer.GetPublicKey(ctx)
	require.NoError(t, err)
	history := nostradapter.NewLocalEventRepository(store, nil).Authored(servicePub.Hex())
	sink := localStoreSink{store: store}
	projector := nostradapter.NewProjector(config.NostrConfig{PublishEnabled: true}, &paritySource{}, sink, history, zap.NewNop(), nostradapter.WithProjectorSigner(signer, servicePub.Hex()))

	orgID := uuid.New()
	var ock controlplane.OrgContentKey
	ock.OrgID, ock.Version = orgID.String(), 1
	for i := range ock.Key {
		ock.Key[i] = byte(i + 7)
	}
	encryptor := fleetOCKEncryptor{key: ock}
	view := nostradapter.NewCanonicalSecretView(projector, encryptor)
	secrets := nostradapter.NewSecretCanonicalPublisher(projector, encryptor, view, zap.NewNop())

	service := &domain.Service{ID: uuid.New(), OrgID: orgID, Name: "api", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	require.NoError(t, nostradapter.NewRelayFirstStatePublisher(projector, sink).PublishServiceRegistry(ctx, service, false))
	gotOrg, err := view.ServiceOrgID(ctx, service.ID)
	require.NoError(t, err)
	require.Equal(t, orgID.String(), gotOrg)

	secretID := uuid.New()
	absent, err := view.CurrentSecretRef(ctx, secretID)
	require.NoError(t, err)
	require.Nil(t, absent)

	ref := domain.SecretRef{ID: secretID, ServiceID: service.ID, Name: "DB_PASSWORD", Version: 1, CreatedBy: "operator",
		CreatedAt: time.Now().UTC().Truncate(time.Second), UpdatedAt: time.Now().UTC().Truncate(time.Second),
		CredentialRef: &domain.SecretCredentialRef{Store: "signet", CredentialID: "cred_" + strings.Repeat("cd", 32), Version: 4}}
	require.NoError(t, secrets.PublishSecretRef(ctx, ref))

	current, err := view.CurrentSecretRef(ctx, secretID)
	require.NoError(t, err)
	require.NotNil(t, current)
	require.Equal(t, *ref.CredentialRef, *current.CredentialRef)
	require.Equal(t, ref.Name, current.Name)
	require.Empty(t, current.EncryptionMethod)
	legacy := domain.SecretRef{ID: uuid.New(), ServiceID: service.ID, Name: "LEGACY", Version: 1, EncryptionMethod: domain.EncryptionNIP44, CreatedAt: ref.CreatedAt, UpdatedAt: ref.UpdatedAt}
	require.NoError(t, secrets.PublishSecretRef(ctx, legacy))
	stored, err := view.StoreBackedSecrets(ctx)
	require.NoError(t, err)
	require.Len(t, stored, 1, "only store-backed records are listed")
	require.Equal(t, secretID, stored[0].ID)
	secretOrg, err := view.SecretOrgID(ctx, secretID)
	require.NoError(t, err)
	require.Equal(t, orgID.String(), secretOrg)

	// The record on the wire is OCK ciphertext: the credential reference is
	// not readable to relays.
	for ev := range store.QueryEvents(gonostr.Filter{Tags: gonostr.TagMap{"d": {secretID.String()}}}) {
		raw, err := json.Marshal(ev)
		require.NoError(t, err)
		require.NotContains(t, string(raw), ref.CredentialRef.CredentialID)
	}

	// Another author's record on the coordinate is not the daemon's state.
	foreign := gonostr.Generate()
	forged := gonostr.Event{Kind: gonostr.Kind(nostradapter.KindCASControlState), CreatedAt: gonostr.Now() + 60,
		Tags: gonostr.Tags{{"d", secretID.String()}, {"legacy_kind", "0"}, {"deleted", "true"}}, Content: "{}"}
	require.NoError(t, forged.Sign(foreign))
	_, err = store.SaveEvent(forged)
	require.NoError(t, err)
	stillCurrent, err := view.CurrentSecretRef(ctx, secretID)
	require.NoError(t, err)
	require.NotNil(t, stillCurrent)

	require.NoError(t, secrets.PublishSecretDeleted(ctx, secretID))
	deleted, err := view.CurrentSecretRef(ctx, secretID)
	require.NoError(t, err)
	require.Nil(t, deleted, "a tombstoned secret has no current record")
}
