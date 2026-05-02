package mcp

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

type testMCPSignatureRepo struct {
	signatures map[uuid.UUID]*domain.ArtifactSignature
}

func newTestMCPSignatureRepo() *testMCPSignatureRepo {
	return &testMCPSignatureRepo{signatures: make(map[uuid.UUID]*domain.ArtifactSignature)}
}

func (m *testMCPSignatureRepo) Create(_ context.Context, sig *domain.ArtifactSignature) error {
	if sig.ID == uuid.Nil {
		sig.ID = uuid.New()
	}
	if sig.CreatedAt.IsZero() {
		sig.CreatedAt = time.Now().UTC()
	}
	cp := *sig
	m.signatures[sig.ID] = &cp
	return nil
}

func (m *testMCPSignatureRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.ArtifactSignature, error) {
	sig, ok := m.signatures[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	cp := *sig
	return &cp, nil
}

func (m *testMCPSignatureRepo) ListByArtifact(_ context.Context, artifactID uuid.UUID) ([]domain.ArtifactSignature, error) {
	out := make([]domain.ArtifactSignature, 0)
	for _, sig := range m.signatures {
		if sig.ArtifactID == artifactID {
			out = append(out, *sig)
		}
	}
	return out, nil
}

func (m *testMCPSignatureRepo) ListVerifiedByArtifact(ctx context.Context, artifactID uuid.UUID) ([]domain.ArtifactSignature, error) {
	sigs, err := m.ListByArtifact(ctx, artifactID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.ArtifactSignature, 0)
	for _, sig := range sigs {
		if sig.Verified {
			out = append(out, sig)
		}
	}
	return out, nil
}

func (m *testMCPSignatureRepo) HasVerifiedSignature(ctx context.Context, artifactID uuid.UUID) (bool, error) {
	sigs, err := m.ListVerifiedByArtifact(ctx, artifactID)
	if err != nil {
		return false, err
	}
	return len(sigs) > 0, nil
}

type testMCPSignatureVerifier struct {
	signatures []domain.ArtifactSignature
	artifactID uuid.UUID
}

func (v *testMCPSignatureVerifier) VerifySignatures(_ context.Context, artifact *domain.Artifact) ([]domain.ArtifactSignature, error) {
	v.artifactID = artifact.ID
	out := make([]domain.ArtifactSignature, len(v.signatures))
	copy(out, v.signatures)
	return out, nil
}

func newTestMCPSignatureServer(t *testing.T, verifier SignatureVerifier) (*Server, *testMCPSignatureRepo, uuid.UUID) {
	t.Helper()

	artifactID := uuid.New()
	artifactRepo := newTestArtifactRepo()
	if err := artifactRepo.Create(context.Background(), &domain.Artifact{
		ID:          artifactID,
		BuildID:     uuid.New(),
		ServiceID:   uuid.New(),
		ImageRepo:   "registry.example.com/team/app",
		ImageTag:    "v1.2.3",
		ImageDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ScanStatus:  domain.ScanStatusClean,
	}); err != nil {
		t.Fatalf("seed artifact: %v", err)
	}

	registry := service.NewRegistryService(
		nil,
		nil,
		nil,
		artifactRepo,
		nil,
		nil,
		nil,
		nil,
		nil,
		events.NewInProcessPublisher(zap.NewNop()),
		zap.NewNop(),
	)
	sigRepo := newTestMCPSignatureRepo()
	server := NewServerWithOptions(registry, zap.NewNop(), ServerDeps{
		Signatures:   sigRepo,
		SignVerifier: verifier,
	})
	return server, sigRepo, artifactID
}

func TestGetTools_IncludesSignatureTools(t *testing.T) {
	server, _, _ := newTestMCPSignatureServer(t, &testMCPSignatureVerifier{})

	required := map[string]bool{
		"bahia_list_signatures":          false,
		"bahia_list_verified_signatures": false,
		"bahia_has_verified_signature":   false,
		"bahia_get_signature":            false,
		"bahia_verify_signatures":        false,
	}
	for _, tool := range server.GetTools() {
		if _, ok := required[tool.Name]; ok {
			required[tool.Name] = true
		}
	}
	for name, found := range required {
		if !found {
			t.Fatalf("missing %s tool", name)
		}
	}
}

func TestCallTool_SignatureListingStatusAndGet(t *testing.T) {
	ctx := context.Background()
	server, sigRepo, artifactID := newTestMCPSignatureServer(t, &testMCPSignatureVerifier{})
	now := time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC)
	verifiedID := uuid.New()
	unverifiedID := uuid.New()

	for _, sig := range []domain.ArtifactSignature{
		{
			ID:             verifiedID,
			ArtifactID:     artifactID,
			SignerIdentity: "builder@example.com",
			SignatureType:  domain.SignatureCosign,
			SignatureRef:   "sha256:signature",
			Verified:       true,
			VerifiedAt:     &now,
			CreatedAt:      now,
		},
		{
			ID:                unverifiedID,
			ArtifactID:        artifactID,
			SignerIdentity:    "untrusted@example.com",
			SignatureType:     domain.SignatureNostr,
			SignatureRef:      "nostr-event-id",
			Verified:          false,
			VerificationError: "untrusted signer",
			CreatedAt:         now,
		},
	} {
		s := sig
		if err := sigRepo.Create(ctx, &s); err != nil {
			t.Fatalf("seed signature: %v", err)
		}
	}

	listRes, err := server.CallTool(ctx, "bahia_list_signatures", map[string]interface{}{"artifact_id": artifactID.String()})
	if err != nil {
		t.Fatalf("list signatures: %v", err)
	}
	listPayload := decodeResultMap(t, listRes)
	if listPayload["total"] != float64(2) {
		t.Fatalf("total = %v, want 2", listPayload["total"])
	}

	verifiedRes, err := server.CallTool(ctx, "bahia_list_verified_signatures", map[string]interface{}{"artifact_id": artifactID.String()})
	if err != nil {
		t.Fatalf("list verified signatures: %v", err)
	}
	verifiedPayload := decodeResultMap(t, verifiedRes)
	if verifiedPayload["total"] != float64(1) {
		t.Fatalf("verified total = %v, want 1", verifiedPayload["total"])
	}

	statusRes, err := server.CallTool(ctx, "bahia_has_verified_signature", map[string]interface{}{"artifact_id": artifactID.String()})
	if err != nil {
		t.Fatalf("signature status: %v", err)
	}
	statusPayload := decodeResultMap(t, statusRes)
	if statusPayload["has_verified_signature"] != true {
		t.Fatalf("has_verified_signature = %v, want true", statusPayload["has_verified_signature"])
	}

	getRes, err := server.CallTool(ctx, "bahia_get_signature", map[string]interface{}{"signature_id": verifiedID.String()})
	if err != nil {
		t.Fatalf("get signature: %v", err)
	}
	getPayload := decodeResultMap(t, getRes)
	if getPayload["id"] != verifiedID.String() {
		t.Fatalf("id = %v, want %s", getPayload["id"], verifiedID)
	}
	if getPayload["signature_type"] != string(domain.SignatureCosign) {
		t.Fatalf("signature_type = %v", getPayload["signature_type"])
	}
}

func TestCallTool_VerifySignaturesStoresDiscoveredRecords(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 5, 2, 11, 0, 0, 0, time.UTC)
	verifier := &testMCPSignatureVerifier{
		signatures: []domain.ArtifactSignature{
			{
				ID:             uuid.New(),
				SignerIdentity: "builder@example.com",
				SignatureType:  domain.SignatureCosign,
				SignatureRef:   "sha256:signature",
				Verified:       true,
				VerifiedAt:     &now,
				CreatedAt:      now,
			},
		},
	}
	server, sigRepo, artifactID := newTestMCPSignatureServer(t, verifier)
	verifier.signatures[0].ArtifactID = artifactID

	res, err := server.CallTool(ctx, "bahia_verify_signatures", map[string]interface{}{"artifact_id": artifactID.String()})
	if err != nil {
		t.Fatalf("verify signatures: %v", err)
	}
	payload := decodeResultMap(t, res)
	if payload["discovered"] != float64(1) || payload["stored"] != float64(1) {
		t.Fatalf("unexpected verify payload: %#v", payload)
	}
	if verifier.artifactID != artifactID {
		t.Fatalf("verifier artifactID = %s, want %s", verifier.artifactID, artifactID)
	}
	stored, err := sigRepo.ListByArtifact(ctx, artifactID)
	if err != nil {
		t.Fatalf("list stored signatures: %v", err)
	}
	if len(stored) != 1 || !stored[0].Verified {
		t.Fatalf("stored signatures = %#v, want one verified signature", stored)
	}
}

func TestCallTool_SignatureValidationAndConfigurationErrors(t *testing.T) {
	ctx := context.Background()

	server := NewServer(nil, zap.NewNop())
	res, err := server.CallTool(ctx, "bahia_list_signatures", map[string]interface{}{"artifact_id": uuid.New().String()})
	if err != nil {
		t.Fatalf("list signatures without repo: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected error when signature tools are not configured")
	}

	server, _, artifactID := newTestMCPSignatureServer(t, nil)
	res, err = server.CallTool(ctx, "bahia_has_verified_signature", map[string]interface{}{"artifact_id": "not-a-uuid"})
	if err != nil {
		t.Fatalf("invalid artifact id: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected invalid artifact_id to return an MCP error")
	}

	res, err = server.CallTool(ctx, "bahia_verify_signatures", map[string]interface{}{"artifact_id": artifactID.String()})
	if err != nil {
		t.Fatalf("verify signatures without verifier: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected error when signature verifier is not configured")
	}
}
