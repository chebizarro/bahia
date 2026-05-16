package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
)

const resolverTestSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestMLArtifactResolverSet_HuggingFace(t *testing.T) {
	set := NewDefaultMLArtifactResolverSet(nil, SeaweedFSResolverConfig{})
	ref, err := set.ResolveArtifact(context.Background(), MLArtifactResolveInput{
		URI:           "hf://Qwen/Qwen2.5-Coder-32B-Instruct@abc123?sha256=" + resolverTestSHA + "&size=42",
		ExpectedMedia: "application/vnd.huggingface.snapshot",
	})
	require.NoError(t, err)
	require.Equal(t, domain.MLArtifactFormatHuggingFaceSnapshot, ref.Format)
	require.Equal(t, resolverTestSHA, ref.SHA256)
	require.Equal(t, int64(42), ref.SizeBytes)
	require.Equal(t, "huggingface", ref.Source.Kind)
}

func TestMLArtifactResolverSet_GitHubInfersONNX(t *testing.T) {
	set := NewDefaultMLArtifactResolverSet(nil, SeaweedFSResolverConfig{})
	ref, err := set.ResolveArtifact(context.Background(), MLArtifactResolveInput{
		URI: "github://owner/repo/releases/download/v1/model.onnx?sha256=" + resolverTestSHA,
	})
	require.NoError(t, err)
	require.Equal(t, domain.MLArtifactFormatONNX, ref.Format)
	require.Equal(t, "application/onnx", ref.MediaType)
	require.Equal(t, "github", ref.Source.Kind)
}

func TestHTTPMLArtifactResolver_UsesHeadMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodHead, r.Method)
		w.Header().Set("X-Checksum-Sha256", resolverTestSHA)
		w.Header().Set("Content-Type", "application/onnx")
		w.Header().Set("Content-Length", "7")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ref, err := (&HTTPMLArtifactResolver{Client: srv.Client()}).ResolveArtifact(context.Background(), MLArtifactResolveInput{URI: srv.URL + "/model.onnx"})
	require.NoError(t, err)
	require.Equal(t, resolverTestSHA, ref.SHA256)
	require.Equal(t, int64(7), ref.SizeBytes)
	require.Equal(t, "application/onnx", ref.MediaType)
	require.Equal(t, domain.MLArtifactFormatONNX, ref.Format)
}

func TestLocalFileMLArtifactResolver_ComputesDigestAndSize(t *testing.T) {
	path := t.TempDir() + "/model.gguf"
	data := []byte("artifact")
	require.NoError(t, os.WriteFile(path, data, 0o600))

	ref, err := (LocalFileMLArtifactResolver{}).ResolveArtifact(context.Background(), MLArtifactResolveInput{URI: path})
	require.NoError(t, err)
	want := sha256.Sum256(data)
	require.Equal(t, hex.EncodeToString(want[:]), ref.SHA256)
	require.Equal(t, int64(len(data)), ref.SizeBytes)
	require.Equal(t, domain.MLArtifactFormatGGUF, ref.Format)
	require.Equal(t, "local", ref.Source.Kind)
}

func TestSeaweedFSS3MLArtifactResolver_FailClosedUnlessConfigured(t *testing.T) {
	_, err := (SeaweedFSS3MLArtifactResolver{}).ResolveArtifact(context.Background(), MLArtifactResolveInput{URI: "s3://models/model.onnx?sha256=" + resolverTestSHA})
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "disabled"))

	_, err = (SeaweedFSS3MLArtifactResolver{Config: SeaweedFSResolverConfig{Enabled: true, Endpoint: "http://seaweed:8333"}}).ResolveArtifact(context.Background(), MLArtifactResolveInput{URI: "s3://models/model.onnx?sha256=" + resolverTestSHA})
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "credentials policy"))

	ref, err := (SeaweedFSS3MLArtifactResolver{Config: SeaweedFSResolverConfig{Enabled: true, Endpoint: "http://seaweed:8333", AccessKeyRef: "secret/access", SecretKeyRef: "secret/key"}}).ResolveArtifact(context.Background(), MLArtifactResolveInput{URI: "s3://models/model.onnx?sha256=" + resolverTestSHA})
	require.NoError(t, err)
	require.Equal(t, "seaweedfs_s3", ref.Source.Kind)
	require.Equal(t, "http://seaweed:8333", ref.Metadata["endpoint"])
}

func TestRemoteResolver_FailsClosedWithoutDigest(t *testing.T) {
	_, err := (BlossomMLArtifactResolver{}).ResolveArtifact(context.Background(), MLArtifactResolveInput{URI: "blossom://host/blob"})
	require.ErrorIs(t, err, ErrMLProvenanceFailedClosed)
}
