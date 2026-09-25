package controlplane

import (
	"context"
	"encoding/json"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
)

type conflictingCanonicalStarter struct{}

func (conflictingCanonicalStarter) StartHiveCIBuild(context.Context, HiveCIBuildStartRequest) (*HiveCIBuildStartResult, error) {
	return &HiveCIBuildStartResult{BuildID: uuid.New(), GitSHA: "0123456789abcdef0123456789abcdef01234567", GitRef: "main", CIRunID: "canonical-run"}, nil
}

func TestBuildRequestRejectsConflictingCanonicalInitiationID(t *testing.T) {
	payload := validArcanaBuildRequest()
	registry := &buildTestRegistry{}
	handler := NewEncryptedBuildHandlers(EncryptedBuildHandlersConfig{
		Starter: conflictingCanonicalStarter{}, Registry: registry, Builds: registry,
		Services: buildTestServices{service: &domain.Service{ID: payload.ServiceID, OrgID: uuid.New(), ArtifactRepo: payload.ArtifactRepo,
			Repository: &domain.RepositoryRef{RepoCoordinate: ArcanaRepositoryCoordinate}}},
		Secrets: buildTestCredentials{secret: &domain.ServiceSecret{ID: payload.RepositoryCredentialRef, ServiceID: payload.ServiceID}},
		RBAC:    auth.NewRBAC(buildTestMembers{}),
	})
	params, err := json.Marshal(payload)
	require.NoError(t, err)
	result, err := handler.RequestBuild(context.Background(), ContextVMRequest{Event: &nostr.Event{ID: nostr.ID{1}}, RPC: ContextVMJSONRPCRequest{Params: params}})
	require.ErrorContains(t, err, "conflicting canonical build ID")
	require.Nil(t, result)
	require.Zero(t, registry.calls)
}
