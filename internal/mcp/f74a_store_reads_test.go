package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"fiatjaf.com/nostr/keyer"
	"github.com/google/uuid"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// Goldens are the MCP response contract, not a second repository implementation.
func assertF74aGolden(t *testing.T, server *Server, name string, args map[string]any, want any) {
	t.Helper()
	result, err := server.CallTool(authorizedMCPContext(), name, args)
	require.NoError(t, err)
	require.False(t, result.IsError, "%s: %v", name, result.Content)
	golden, err := json.Marshal(want)
	require.NoError(t, err)
	require.JSONEq(t, string(golden), result.Content[0].Text)
}

func TestF74aMCPReadParityGoldensDBLess(t *testing.T) {
	ctx := context.Background()
	server := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{})
	fixture := attachCanonicalMCPFixture(t, server)
	signer, err := keyer.New(ctx, nil, fixture.privateKey, nil)
	require.NoError(t, err)
	manager := controlplane.NewOCKManager(controlplane.OCKManagerConfig{Signer: signer, ServicePubkey: server.servicePubkey, Publisher: mcpKeyEnvelopeSink{}})
	encryptor := controlplane.NewConfidentialEncryptor(manager, zap.NewNop())
	server.confidentialReader = encryptor
	publisher := nostrpool.NewF74aCanonicalPublisher(fixture.projector, encryptor)
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	release := &domain.LLMRelease{ID: uuid.New(), RouteID: uuid.New(), Version: "v2", ModelRef: "model:test", ModelSource: "oci", Metadata: map[string]any{"private": "release-credential"}, CreatedAt: now}
	require.NoError(t, publisher.PublishLLMRelease(ctx, release))
	wantRelease := map[string]any{
		"id": release.ID.String(), "route_id": release.RouteID.String(), "version": "v2",
		"model_ref": "model:test", "model_source": "oci", "model_revision": "",
		"estimated_vram_gb": 0, "backend_preferences": nil, "runtime_backend": nil,
		"external_backend": nil, "placement_policy": nil, "promotion_gate": nil,
		"metadata": map[string]any{"private": "release-credential"}, "created_at": "2026-10-03T12:00:00Z",
	}
	assertF74aGolden(t, server, "bahia_llm_list_releases", map[string]any{"route_id": release.RouteID.String()}, map[string]any{
		"route_id": release.RouteID.String(), "releases": []map[string]any{wantRelease}, "total": 1, "registry_kind": nostrpool.KindLLMRouteRegistry,
	})
	artifactID := uuid.New()
	verified := &domain.ArtifactSignature{ID: uuid.New(), ArtifactID: artifactID, SignerIdentity: "builder@example.test", SignatureType: domain.SignatureCosign, SignatureRef: "sha256:abc", VerificationStatus: domain.SignatureStatusVerified, Verified: true, VerifiedAt: &now, CreatedAt: now}
	require.NoError(t, publisher.PublishArtifactSignature(ctx, verified))
	wantSignature := map[string]any{
		"id": verified.ID.String(), "artifact_id": artifactID.String(), "signer_identity": "builder@example.test",
		"signature_type": "cosign", "signature_ref": "sha256:abc", "verified": true,
		"verification_error": "", "metadata": nil, "verified_at": "2026-10-03T12:00:00Z",
		"created_at": "2026-10-03T12:00:00Z",
	}
	args := map[string]any{"artifact_id": artifactID.String()}
	assertF74aGolden(t, server, "bahia_list_signatures", args, map[string]any{"artifact_id": artifactID.String(), "signatures": []map[string]any{wantSignature}, "total": 1})
	assertF74aGolden(t, server, "bahia_list_verified_signatures", args, map[string]any{"artifact_id": artifactID.String(), "signatures": []map[string]any{wantSignature}, "total": 1})
	assertF74aGolden(t, server, "bahia_has_verified_signature", args, map[string]any{"artifact_id": artifactID.String(), "has_verified_signature": true})
	assertF74aGolden(t, server, "bahia_get_signature", map[string]any{"signature_id": verified.ID.String()}, wantSignature)
	sbom := &domain.ArtifactSBOM{ID: uuid.New(), ArtifactID: artifactID, Format: domain.SBOMFormatSPDX, PackageCount: 2, CreatedAt: now}
	require.NoError(t, publisher.PublishArtifactSBOM(ctx, sbom))
	wantSBOM := map[string]any{
		"id": sbom.ID.String(), "artifact_id": artifactID.String(), "format": "spdx",
		"source_url": "", "package_count": 2, "vulnerability_count": 0,
		"critical_count": 0, "high_count": 0, "raw_hash": "", "metadata": nil,
		"created_at": "2026-10-03T12:00:00Z",
	}
	packages := []domain.SBOMPackage{{ID: uuid.New(), SBOMID: sbom.ID, Name: "alpha", Version: "1"}, {ID: uuid.New(), SBOMID: sbom.ID, Name: "beta", Version: "2"}}
	for i := range packages {
		require.NoError(t, publisher.PublishSBOMPackage(ctx, &packages[i]))
	}
	wantPackages := []map[string]any{
		{"id": packages[0].ID.String(), "sbom_id": sbom.ID.String(), "name": "alpha", "version": "1", "ecosystem": "", "license": "", "purl": "", "cpe": ""},
		{"id": packages[1].ID.String(), "sbom_id": sbom.ID.String(), "name": "beta", "version": "2", "ecosystem": "", "license": "", "purl": "", "cpe": ""},
	}
	assertF74aGolden(t, server, "bahia_get_sbom", args, wantSBOM)
	assertF74aGolden(t, server, "bahia_get_sbom_packages", args, map[string]any{"artifact_id": artifactID.String(), "sbom_id": sbom.ID.String(), "packages": wantPackages, "total": 2})
	assertF74aGolden(t, server, "bahia_search_sbom_packages", map[string]any{"query": "ALP"}, map[string]any{"query": "ALP", "packages": wantPackages[:1], "total": 1})
	obs := &domain.RuntimeObservation{ID: uuid.New(), ServiceID: uuid.New(), EnvironmentID: uuid.New(), ObservedImageDigest: "sha256:abc", ObservedContainerID: "container-1", HealthStatus: domain.HealthStatusHealthy, ObservedAt: now, Metadata: map[string]any{"secret": "not-projected"}}
	require.NoError(t, publisher.PublishRuntimeObservation(ctx, obs))
	assertF74aGolden(t, server, "bahia_get_observation", map[string]any{"service_id": obs.ServiceID.String(), "environment_id": obs.EnvironmentID.String()}, map[string]any{
		"id": obs.ID.String(), "service_id": obs.ServiceID.String(), "environment_id": obs.EnvironmentID.String(), "image_digest": obs.ObservedImageDigest, "container_id": obs.ObservedContainerID, "health_status": obs.HealthStatus, "observed_at": now.Format("2006-01-02T15:04:05Z"),
	})
	require.NoError(t, publisher.PublishArtifactSignatureState(ctx, verified, true))
	deleted, err := server.CallTool(authorizedMCPContext(), "bahia_get_signature", map[string]any{"signature_id": verified.ID.String()})
	require.NoError(t, err)
	require.True(t, deleted.IsError, "tombstoned signature remained visible")
}
