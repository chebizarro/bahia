package mcp

import (
	"context"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type artifactManifestVerifier struct{ digest string }

func (v artifactManifestVerifier) VerifyImage(context.Context, string, string) (*service.ImageVerification, error) {
	return &service.ImageVerification{Exists: true, Digest: v.digest, ScanStatus: "clean"}, nil
}

func TestMCPArtifactRegisterRealIntentPipeline(t *testing.T) {
	const digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for _, mode := range []string{"accepted", "replay", "rejected", "pending"} {
		t.Run(mode, func(t *testing.T) {
			orgID, serviceID, buildID := uuid.New(), uuid.New(), uuid.New()
			actor, stranger := nostr.Generate().Public().Hex(), nostr.Generate().Public().Hex()
			services, builds, artifacts := newTestServiceRepo(), newTestBuildRepo(), newTestArtifactRepo()
			svc := &domain.Service{ID: serviceID, OrgID: orgID, Name: "api", ArtifactRepo: "registry.example/api"}
			require.NoError(t, services.Create(t.Context(), svc))
			builds.builds[buildID] = &domain.Build{ID: buildID, ServiceID: serviceID, Status: domain.BuildStatusSucceeded}
			registry := service.NewRegistryService(services, nil, builds, artifacts, nil, nil, nil, nil,
				artifactManifestVerifier{digest: digest}, events.NewInProcessPublisher(zap.NewNop()), zap.NewNop(),
				service.WithManualArtifactRegistration(true))
			server := newTestServerWithOptions(registry, zap.NewNop(), ServerDeps{AuthorizedPubkeys: []string{actor, stranger}})
			canonical := attachCanonicalMCPFixture(t, server)
			canonical.publishService(t, svc)
			if mode != "pending" {
				registry.SetCPStatePublisher(nostrpool.NewRelayFirstStatePublisher(canonical.projector, canonical.sink))
			}
			trust := controlplane.NewTrustSet([]string{actor}, zap.NewNop(), controlplane.WithBootstrapOwners(map[string]string{orgID.String(): actor}))
			processor := controlplane.NewIntentProcessor(trust, canonical.store, nil,
				controlplane.IntentProcessorConfig{EnabledDomains: map[string]bool{"artifact": true}}, zap.NewNop())
			processor.RegisterHandler("artifact", controlplane.NewArtifactIntentHandler(registry, services))
			server.intentProc = processor
			ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: actor, PubKey: actor, Method: auth.MethodNIP98})
			if mode == "rejected" {
				ctx = auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: stranger, PubKey: stranger, Method: auth.MethodNIP98})
			}
			args := map[string]any{
				"service_id": serviceID.String(), "build_id": buildID.String(),
				"image_repo": "registry.example/api", "image_tag": "v1", "image_digest": digest,
				"scan_status": "clean", "size_bytes": float64(4096),
				"_meta": map[string]any{"progressToken": "artifact-register-1"},
			}
			first, err := server.CallTool(ctx, "bahia_register_artifact", args)
			require.NoError(t, err)
			body := mcpIntentResult(t, first)
			require.NotEmpty(t, body["intent_id"])
			require.NotEmpty(t, body["event_id"])
			if mode == "rejected" {
				require.True(t, first.IsError)
				require.Equal(t, "rejected", body["status"])
				require.Zero(t, artifacts.creates)
				return
			}
			require.False(t, first.IsError, "%v", body)
			require.Equal(t, 1, artifacts.creates)
			if mode == "pending" {
				require.Equal(t, "pending", body["status"])
				require.NotContains(t, body, "state")
				return
			}
			require.Equal(t, "accepted", body["status"])
			state := body["state"].(map[string]any)
			require.Equal(t, serviceID.String(), state["service_id"])
			require.Equal(t, digest, state["image_digest"])
			require.Equal(t, float64(4096), state["size_bytes"])
			if mode == "replay" {
				again, err := server.CallTool(ctx, "bahia_register_artifact", args)
				require.NoError(t, err)
				require.False(t, again.IsError)
				replayed := mcpIntentResult(t, again)
				require.Equal(t, "accepted", replayed["status"])
				require.Equal(t, body["intent_id"], replayed["intent_id"])
				require.Equal(t, body["event_id"], replayed["event_id"])
				require.Equal(t, 1, artifacts.creates)
			}
		})
	}
}
