package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/secretstore"
	"github.com/stretchr/testify/require"
)

func TestCanonicalAutoApplySourceBindsLocalSignedDecisionAndWinnersWithoutDatabase(t *testing.T) {
	store, err := localstore.Open(filepath.Join(t.TempDir(), "events.bolt"))
	require.NoError(t, err)
	defer store.Close()
	serviceKey, operatorKey := nostr.Generate(), nostr.Generate()
	orgID, serviceID, envID, unitID, artifactID, intentID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	spec := domain.DesiredServiceSpec{SchemaVersion: domain.DesiredStateSchemaVersion,
		ServiceID: serviceID, EnvironmentID: envID, DeploymentUnitID: &unitID,
		DeploymentUnitKey: "api", UnitRuntimeType: domain.RuntimeTypeDocker,
		ArtifactID: artifactID, StableServiceKey: "api", ImageRef: "registry.example/api@sha256:abc"}
	spec.ComputeDesiredHash()
	decision := nostr.Event{Kind: 30900, CreatedAt: 100, Tags: nostr.Tags{
		{"t", "bahia-intent"}, {"d", "deployment:" + serviceID.String() + ":" + envID.String()},
		{"domain", "deployment"}, {"op", "create"}, {"org", orgID.String()}, {"intent_id", uuid.NewString()},
	}, Content: fmt.Sprintf(`{"service_id":%q,"environment_id":%q,"artifact_id":%q}`, serviceID, envID, artifactID)}
	require.NoError(t, decision.Sign(operatorKey))
	_, err = store.SaveEvent(decision)
	require.NoError(t, err)
	accepted := nostr.Event{Kind: 30315, CreatedAt: 101, Tags: nostr.Tags{
		{"t", "intent-status"}, {"domain", "intent"}, {"status", "accepted"},
		{"d", "intent-status:" + operatorKey.Public().Hex() + ":deployment:" + serviceID.String() + ":" + envID.String()},
		{"p", operatorKey.Public().Hex()}, {"e", decision.ID.Hex()},
	}, Content: `{"result":"applied"}`}
	require.NoError(t, accepted.Sign(serviceKey))
	_, err = store.SaveEvent(accepted)
	require.NoError(t, err)
	svc := domain.Service{ID: serviceID, OrgID: orgID, Name: "api", RuntimeType: domain.RuntimeTypeDocker}
	env := canonicalEnvironment{Environment: domain.Environment{ID: envID, OrgID: orgID, Name: "prod"},
		DeploymentUnits: []domain.DeploymentUnit{{ID: unitID, EnvironmentID: envID, Key: "api",
			RuntimeType: domain.RuntimeTypeDocker, ReconcileMode: domain.ReconcileModeAutoApply,
			OwnershipMode: domain.OwnershipModeBahiaManaged}}}
	artifact := domain.Artifact{ID: artifactID, ServiceID: serviceID, ImageRepo: "registry.example/api", ImageDigest: "sha256:abc"}
	intent := domain.DeploymentIntent{ID: intentID, ServiceID: serviceID, EnvironmentID: envID, DeploymentUnitID: &unitID,
		ArtifactID: artifactID, RequestedBy: operatorKey.Public().Hex(), ApprovalStatus: domain.ApprovalStatusNotRequired,
		Status: domain.IntentStatusApproved, DesiredState: &spec, DesiredHash: spec.DesiredHash,
		Metadata: map[string]any{"nostr_event_id": decision.ID.Hex()}}
	state := domain.EnvironmentServiceState{ServiceID: serviceID, EnvironmentID: envID, DeploymentUnitID: &unitID,
		DesiredArtifactID: &artifactID, DesiredIntentID: &intentID, DesiredRuntimeState: &spec, DesiredHash: spec.DesiredHash}
	saveCanonicalRuntimeJSON(t, store, serviceKey, kinds.ServiceRegistry, serviceID.String(), svc, 101)
	saveCanonicalRuntimeJSON(t, store, serviceKey, kinds.EnvironmentRegistry, envID.String(), env, 101)
	saveCanonicalRuntimeJSON(t, store, serviceKey, kinds.ArtifactRegistry, artifactID.String(), artifact, 101)
	saveCanonicalRuntimeJSON(t, store, serviceKey, kinds.DeploymentIntentRegistry, intentID.String(), intent, 101)
	tags, content := nostradapter.RuntimeStateRecord(&state, nil)
	saveCanonicalRuntimeEvent(t, store, serviceKey, kinds.ServiceState,
		nostradapter.ServiceStateDTag(serviceID, envID), false, content, 101, tags)
	source, err := NewCanonicalRuntimeSource(store, serviceKey.Public().Hex())
	require.NoError(t, err)
	apply := &CanonicalAutoApplySource{Runtime: source}
	got, err := apply.LoadAutoApplySnapshot(context.Background(), serviceID, envID)
	require.NoError(t, err)
	require.Equal(t, decision.ID.Hex(), got.DecisionEventID)
	require.NotEmpty(t, got.ServiceEventID)
	require.NotEmpty(t, got.EnvironmentEventID)
	require.NotEmpty(t, got.ArtifactEventID)
	require.NotEmpty(t, got.IntentEventID)
	require.NotEmpty(t, got.StateEventID)
	require.Equal(t, got.ArtifactEventID, got.ReleaseEventID)

	// A newer signed environment record changes the bound revision, even
	// though its content still names the same unit. The executor refuses the
	// old receipt rather than asking a SQL index what changed.
	saveCanonicalRuntimeJSON(t, store, serviceKey, kinds.EnvironmentRegistry, envID.String(), env, 102)
	moved, err := apply.LoadAutoApplySnapshot(context.Background(), serviceID, envID)
	require.NoError(t, err)
	require.NotEqual(t, got.EnvironmentEventID, moved.EnvironmentEventID)

	// A tombstone is an observed conflict, not a missing-record fallback.
	tombstone, err := json.Marshal(map[string]any{"deleted": true, "id": artifactID})
	require.NoError(t, err)
	saveCanonicalRuntimeEvent(t, store, serviceKey, kinds.ArtifactRegistry, artifactID.String(), true, string(tombstone), 103, nil)
	_, err = apply.LoadAutoApplySnapshot(context.Background(), serviceID, envID)
	require.ErrorIs(t, err, ErrFreshDecisionRequired)
}

type autoSecretRecords struct {
	orgID uuid.UUID
	ref   domain.SecretRef
}

func (r autoSecretRecords) CurrentSecretRef(context.Context, uuid.UUID) (*domain.SecretRef, error) {
	return &r.ref, nil
}
func (r autoSecretRecords) ServiceOrgID(context.Context, uuid.UUID) (string, error) {
	return r.orgID.String(), nil
}

type rotatedAutoSecretStore struct {
	activeVersion int
	deliveries    int
}

func (s *rotatedAutoSecretStore) Resolve(_ context.Context, ref domain.SecretCredentialRef, _ secretstore.Scope) (*secretstore.Value, error) {
	s.deliveries++
	if ref.Version != s.activeVersion {
		return nil, secretstore.ErrVersionMismatch
	}
	return secretstore.NewValue([]byte("private-value"), time.Now().Add(time.Minute)), nil
}
func (s *rotatedAutoSecretStore) ResolveAll(ctx context.Context, reqs []secretstore.Request) ([]*secretstore.Value, error) {
	out := make([]*secretstore.Value, 0, len(reqs))
	for i, req := range reqs {
		value, err := s.Resolve(ctx, req.Ref, req.Scope)
		if err != nil {
			secretstore.ZeroizeAll(out)
			return nil, &secretstore.BatchError{Index: i, Err: err}
		}
		out = append(out, value)
	}
	return out, nil
}
func (s *rotatedAutoSecretStore) Inspect(context.Context, string, secretstore.Scope) (secretstore.Metadata, error) {
	return secretstore.Metadata{}, nil
}
func (s *rotatedAutoSecretStore) Health(context.Context) error { return nil }

func TestCanonicalAutoApplyResolveRequiresDecisionPinnedSignetVersion(t *testing.T) {
	orgID, serviceID, secretID := uuid.New(), uuid.New(), uuid.New()
	pin := domain.SecretCredentialRef{Store: secretstore.BackendSignet, CredentialID: "cred-7", Version: 7}
	records := autoSecretRecords{orgID: orgID, ref: domain.SecretRef{
		ID: secretID, ServiceID: serviceID, Name: "TOKEN", CredentialRef: &pin,
	}}
	store := &rotatedAutoSecretStore{activeVersion: 8}
	resolver := secretstore.NewResolver(store)
	resolver.UseRecords(records)
	source := &CanonicalAutoApplySource{Records: records, Secrets: resolver}
	snapshot := ApplySnapshot{SecretPins: []ApplySecretPin{{
		SecretID: secretID, EnvVar: "TOKEN", CredentialID: pin.CredentialID, Version: pin.Version,
	}}}
	values, release, err := source.ResolvePinnedSecrets(context.Background(), snapshot)
	require.ErrorIs(t, err, ErrFreshDecisionRequired)
	require.Nil(t, values)
	require.Nil(t, release)
	require.Equal(t, 1, store.deliveries, "Signet expected_version is checked at use time")

	store.activeVersion = 7
	values, release, err = source.ResolvePinnedSecrets(context.Background(), snapshot)
	require.NoError(t, err)
	require.Equal(t, "private-value", values["TOKEN"])
	release()
	require.Empty(t, values, "plaintext map is dropped after the effect")
}
