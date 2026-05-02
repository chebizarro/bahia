package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
)

type testSignatureRepo struct {
	sigs []domain.ArtifactSignature
}

func (r *testSignatureRepo) Create(_ context.Context, sig *domain.ArtifactSignature) error {
	sig.NormalizeVerificationStatus()
	r.sigs = append(r.sigs, *sig)
	return nil
}

func (r *testSignatureRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.ArtifactSignature, error) {
	for i := range r.sigs {
		if r.sigs[i].ID == id {
			sig := r.sigs[i]
			return &sig, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (r *testSignatureRepo) ListByArtifact(_ context.Context, artifactID uuid.UUID) ([]domain.ArtifactSignature, error) {
	out := make([]domain.ArtifactSignature, 0)
	for _, sig := range r.sigs {
		if sig.ArtifactID == artifactID {
			out = append(out, sig)
		}
	}
	return out, nil
}

func (r *testSignatureRepo) ListVerifiedByArtifact(ctx context.Context, artifactID uuid.UUID) ([]domain.ArtifactSignature, error) {
	sigs, err := r.ListByArtifact(ctx, artifactID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.ArtifactSignature, 0)
	for _, sig := range sigs {
		sig.NormalizeVerificationStatus()
		if sig.VerificationStatus == domain.SignatureStatusVerified {
			out = append(out, sig)
		}
	}
	return out, nil
}

func (r *testSignatureRepo) HasVerifiedSignature(ctx context.Context, artifactID uuid.UUID) (bool, error) {
	sigs, err := r.ListVerifiedByArtifact(ctx, artifactID)
	return len(sigs) > 0, err
}

type testArtifactRepo struct {
	artifacts map[uuid.UUID]*domain.Artifact
}

func (r *testArtifactRepo) Create(_ context.Context, a *domain.Artifact) error {
	r.artifacts[a.ID] = a
	return nil
}
func (r *testArtifactRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Artifact, error) {
	artifact, ok := r.artifacts[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return artifact, nil
}
func (r *testArtifactRepo) GetByDigest(_ context.Context, _, _ string) (*domain.Artifact, error) {
	return nil, repository.ErrNotFound
}
func (r *testArtifactRepo) GetByImageRepoDigest(_ context.Context, _, _ string) (*domain.Artifact, error) {
	return nil, repository.ErrNotFound
}
func (r *testArtifactRepo) ListByService(_ context.Context, _ uuid.UUID, _, _ int) ([]domain.Artifact, error) {
	return nil, nil
}
func (r *testArtifactRepo) ListByBuild(_ context.Context, _ uuid.UUID) ([]domain.Artifact, error) {
	return nil, nil
}

type testSignatureVerifier struct {
	sigs []domain.ArtifactSignature
}

func (v *testSignatureVerifier) VerifySignatures(_ context.Context, artifact *domain.Artifact) ([]domain.ArtifactSignature, error) {
	out := make([]domain.ArtifactSignature, len(v.sigs))
	copy(out, v.sigs)
	for i := range out {
		out[i].ArtifactID = artifact.ID
	}
	return out, nil
}

func TestSignatureHandler_VerifyReportsVerificationStatusCounts(t *testing.T) {
	artifactID := uuid.New()
	sigRepo := &testSignatureRepo{}
	artifactRepo := &testArtifactRepo{artifacts: map[uuid.UUID]*domain.Artifact{
		artifactID: {ID: artifactID, ImageRepo: "example/app", ImageDigest: "sha256:abc"},
	}}
	verifier := &testSignatureVerifier{sigs: []domain.ArtifactSignature{
		{ID: uuid.New(), SignatureType: domain.SignatureCosign, SignatureRef: "sha256:referrer", VerificationStatus: domain.SignatureStatusDiscovered},
		{ID: uuid.New(), SignatureType: domain.SignatureNostr, SignatureRef: "nostr-event", VerificationStatus: domain.SignatureStatusVerified},
		{ID: uuid.New(), SignatureType: domain.SignatureNostr, SignatureRef: "bad-event", VerificationStatus: domain.SignatureStatusRejected, VerificationError: "untrusted signer"},
	}}
	h := NewSignatureHandler(sigRepo, artifactRepo, verifier)

	req := httptest.NewRequest(http.MethodPost, "/artifacts/"+artifactID.String()+"/signatures/verify", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", artifactID.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()

	h.Verify(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Data map[string]any `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	for key, want := range map[string]float64{
		"found":      3,
		"stored":     3,
		"discovered": 1,
		"verified":   1,
		"rejected":   1,
		"errors":     0,
	} {
		if got := resp.Data[key]; got != want {
			t.Fatalf("%s = %v, want %v", key, got, want)
		}
	}
	if has, err := sigRepo.HasVerifiedSignature(context.Background(), artifactID); err != nil || !has {
		t.Fatalf("HasVerifiedSignature = %v, %v; want true, nil", has, err)
	}
	verified, err := sigRepo.ListVerifiedByArtifact(context.Background(), artifactID)
	if err != nil {
		t.Fatalf("ListVerifiedByArtifact: %v", err)
	}
	if len(verified) != 1 || verified[0].VerificationStatus != domain.SignatureStatusVerified {
		t.Fatalf("verified signatures = %#v, want one verified signature", verified)
	}
}
