package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestOCIClientResolveObjectByDigestBindsConfiguredAuthorityAndDigestPath(t *testing.T) {
	const (
		digest    = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		mediaType = "application/vnd.cyclonedx+json"
		body      = `{"bomFormat":"CycloneDX"}`
	)
	var requestedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPath = r.URL.Path
		w.Header().Set("Content-Type", mediaType)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	host := strings.TrimPrefix(server.URL, "http://")
	client := NewOCIClient(server.URL, zap.NewNop(), WithHTTPClient(server.Client()))
	object, err := client.ResolveObjectByDigest(
		context.Background(), host+"/team/bahia", digest, mediaType, int64(len(body)),
	)
	if err != nil {
		t.Fatal(err)
	}
	if string(object.Content) != body || object.MediaType != mediaType || object.Size != int64(len(body)) {
		t.Fatalf("unexpected object: %+v", object)
	}
	wantPath := "/v2/team/bahia/blobs/" + digest
	if requestedPath != wantPath {
		t.Fatalf("request path = %q, want %q", requestedPath, wantPath)
	}

	if _, err := client.ResolveObjectByDigest(
		context.Background(), "other.example/team/bahia", digest, mediaType, int64(len(body)),
	); err == nil {
		t.Fatal("mismatched repository authority was accepted")
	}
}
