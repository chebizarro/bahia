package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestMLProvenanceServiceMirrorMismatchesFailClosed(t *testing.T) {
	cases := []struct {
		name string
		uri  string
	}{
		{name: "HuggingFace", uri: "hf://org/model@abc123"},
		{name: "GitHub", uri: "github://org/repo/releases/download/v1/model.onnx"},
		{name: "Blossom", uri: "blossom://relay.example/aaaaaaaa"},
		{name: "OCI", uri: "oci://registry.example/models/qwen@sha256:bbbbbbbb"},
		{name: "SeaweedFS-S3", uri: "s3://seaweedfs/ml/models/qwen.safetensors"},
		{name: "local", uri: "file:///var/lib/bahia/models/qwen.safetensors"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repo := newFakeMLRegistryRepo()
			svc := NewMLProvenanceService(repo, nil, nil)
			versionID := uuid.New()
			canonical := artifact(versionID, "hf://org/model@abc123", strings.Repeat("a", 64))
			mirror := artifact(versionID, tc.uri, strings.Repeat("b", 64))
			repo.artifacts = append(repo.artifacts, canonical, mirror)

			err := svc.ValidateModelVersionArtifactMirrors(ctx, versionID)
			require.Error(t, err)
			require.True(t, errors.Is(err, ErrMLProvenanceFailedClosed))
			require.Len(t, repo.edges, 1)
			require.False(t, repo.edges[0].Verified)
			require.Contains(t, repo.edges[0].Defect, "digest mismatch")
			require.Equal(t, MLProvenanceEdgeMirrorOf, repo.edges[0].EdgeKind)
		})
	}
}

func TestMLProvenanceServiceMirrorMatchesRecordVerifiedEdges(t *testing.T) {
	ctx := context.Background()
	repo := newFakeMLRegistryRepo()
	svc := NewMLProvenanceService(repo, nil, nil)
	versionID := uuid.New()
	digest := "sha256:" + strings.Repeat("c", 64)
	repo.artifacts = append(repo.artifacts,
		artifact(versionID, "hf://org/model@abc123", digest),
		artifact(versionID, "oci://registry.example/models/qwen@sha256:ccc", strings.Repeat("c", 64)),
	)

	require.NoError(t, svc.ValidateModelVersionArtifactMirrors(ctx, versionID))
	require.Len(t, repo.edges, 1)
	require.True(t, repo.edges[0].Verified)
	require.Empty(t, repo.edges[0].Defect)
}

func TestMLProvenanceServiceWorkerDigestMismatchFailsClosed(t *testing.T) {
	ctx := context.Background()
	repo := newFakeMLRegistryRepo()
	svc := NewMLProvenanceService(repo, nil, nil)
	versionID := uuid.New()
	ref := artifact(versionID, "s3://seaweedfs/ml/model.gguf", strings.Repeat("d", 64))
	run := &domain.MLDeploymentRun{ID: uuid.New(), WorkerPubkey: "worker", VerifiedDigests: map[string]string{ref.URI: strings.Repeat("e", 64)}}

	err := svc.VerifyWorkerReportedDigests(ctx, run, []domain.MLArtifactRef{ref})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrMLProvenanceFailedClosed))
	require.Len(t, repo.edges, 1)
	require.False(t, repo.edges[0].Verified)
	require.Contains(t, repo.edges[0].Defect, "worker digest mismatch")
}

func TestMLProvenanceServiceWorkerDigestMatchRecordsVerifiedEdge(t *testing.T) {
	ctx := context.Background()
	repo := newFakeMLRegistryRepo()
	svc := NewMLProvenanceService(repo, nil, nil)
	versionID := uuid.New()
	ref := artifact(versionID, "file:///models/model.onnx", strings.Repeat("f", 64))
	run := &domain.MLDeploymentRun{ID: uuid.New(), WorkerPubkey: "worker", VerifiedDigests: map[string]string{ref.ID.String(): "sha256:" + strings.Repeat("f", 64)}}

	require.NoError(t, svc.VerifyWorkerReportedDigests(ctx, run, []domain.MLArtifactRef{ref}))
	require.Len(t, repo.edges, 1)
	require.True(t, repo.edges[0].Verified)
	require.Equal(t, MLProvenanceEdgeWorkerVerified, repo.edges[0].EdgeKind)
}

func TestMLProvenanceServiceRegisterArtifactRequiresDigest(t *testing.T) {
	repo := newFakeMLRegistryRepo()
	svc := NewMLProvenanceService(repo, nil, nil)
	ref := artifact(uuid.New(), "github://org/repo/model.onnx", "")

	err := svc.RegisterArtifactRef(context.Background(), &ref)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrMLProvenanceFailedClosed))
}

func artifact(versionID uuid.UUID, uri, digest string) domain.MLArtifactRef {
	return domain.MLArtifactRef{ID: uuid.New(), ModelVersionID: &versionID, Kind: domain.MLArtifactKindModel, Format: formatForURI(uri), URI: uri, SHA256: digest}
}

func formatForURI(uri string) domain.MLArtifactFormat {
	switch {
	case strings.HasPrefix(uri, "oci://"):
		return domain.MLArtifactFormatOCIArtifact
	case strings.HasPrefix(uri, "blossom://"):
		return domain.MLArtifactFormatBlossomBlob
	case strings.HasSuffix(uri, ".onnx"):
		return domain.MLArtifactFormatONNX
	default:
		return domain.MLArtifactFormatSafeTensors
	}
}
