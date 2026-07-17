package sbom

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/adapters/blossom"
	"github.com/openagentsinc/bahia/internal/domain"
)

// testHash is a valid 64-character SHA256 hash for testing.
const testHash = "abc123def456abc123def456abc123def456abc123def456abc123def456abcd"

func TestStorageResolver_ResolveFromBlossom(t *testing.T) {
	mockBlossom := &MockBlossomClient{
		Blobs: map[string][]byte{
			testHash: []byte(`{"spdxVersion": "SPDX-2.3"}`),
		},
	}

	logger := slog.Default()
	resolver := NewStorageResolver(mockBlossom, nil, nil, logger)

	// Test successful resolution.
	data, err := resolver.Resolve(context.Background(), ResolveInput{
		Location: domain.SBOMLocation{
			Type: domain.SBOMStorageBlossom,
			URI:  "https://blossom.example.com/" + testHash,
		},
	})
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if string(data) != `{"spdxVersion": "SPDX-2.3"}` {
		t.Errorf("Unexpected data: %s", data)
	}

	// Test resolution with extension.
	data, err = resolver.Resolve(context.Background(), ResolveInput{
		Location: domain.SBOMLocation{
			Type: domain.SBOMStorageBlossom,
			URI:  "https://blossom.example.com/" + testHash + ".json",
		},
	})
	if err != nil {
		t.Fatalf("Resolve with extension failed: %v", err)
	}
	if string(data) != `{"spdxVersion": "SPDX-2.3"}` {
		t.Errorf("Unexpected data: %s", data)
	}
}

func TestStorageResolver_StoreToBlossom(t *testing.T) {
	mockBlossom := &MockBlossomClient{
		Blobs: make(map[string][]byte),
	}

	logger := slog.Default()
	resolver := NewStorageResolver(mockBlossom, nil, nil, logger)

	sbomData := []byte(`{"spdxVersion": "SPDX-2.3", "name": "test"}`)
	result, err := resolver.Store(context.Background(), StoreInput{
		Data:        sbomData,
		Format:      domain.SBOMFormatSPDX,
		BackendType: domain.SBOMStorageBlossom,
	})
	if err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	if result.Location.Type != domain.SBOMStorageBlossom {
		t.Errorf("Location.Type = %q, want %q", result.Location.Type, domain.SBOMStorageBlossom)
	}
	if result.Location.MediaType != MediaTypeSPDX {
		t.Errorf("Location.MediaType = %q, want %q", result.Location.MediaType, MediaTypeSPDX)
	}
	if result.Hash == "" {
		t.Error("Expected non-empty hash")
	}

	// Verify data was stored.
	if _, ok := mockBlossom.Blobs[result.Hash]; !ok {
		t.Error("Blob was not stored in mock")
	}
}

type descriptorBlossomClient struct {
	descriptor *blossom.BlobDescriptor
}

func (c *descriptorBlossomClient) Download(context.Context, string) ([]byte, error) {
	return nil, fmt.Errorf("unexpected download")
}

func (c *descriptorBlossomClient) Upload(context.Context, []byte, string) (*blossom.BlobDescriptor, error) {
	return c.descriptor, nil
}

func TestStorageResolver_StoreToBlossomValidatesDescriptor(t *testing.T) {
	data := []byte(`{"spdxVersion":"SPDX-2.3"}`)
	hash := hashData(data)
	tests := []struct {
		name string
		desc *blossom.BlobDescriptor
	}{
		{name: "nil descriptor", desc: nil},
		{name: "non-hex hash", desc: &blossom.BlobDescriptor{SHA256: strings.Repeat("z", 64), URL: "https://blossom.example.com/" + hash, Size: int64(len(data))}},
		{name: "hash mismatch", desc: &blossom.BlobDescriptor{SHA256: strings.Repeat("0", 64), URL: "https://blossom.example.com/" + hash, Size: int64(len(data))}},
		{name: "URL hash mismatch", desc: &blossom.BlobDescriptor{SHA256: hash, URL: "https://blossom.example.com/" + strings.Repeat("0", 64), Size: int64(len(data))}},
		{name: "size mismatch", desc: &blossom.BlobDescriptor{SHA256: hash, URL: "https://blossom.example.com/" + hash, Size: int64(len(data) + 1)}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver := NewStorageResolver(&descriptorBlossomClient{descriptor: tt.desc}, nil, nil, nil)
			if _, err := resolver.Store(context.Background(), StoreInput{Data: data, Format: domain.SBOMFormatSPDX, BackendType: domain.SBOMStorageBlossom}); err == nil {
				t.Fatal("Store succeeded with invalid Blossom descriptor")
			}
		})
	}
}

func TestStorageResolver_NilLoggerUsesNoOpLogger(t *testing.T) {
	resolver := NewStorageResolver(&MockBlossomClient{Blobs: make(map[string][]byte)}, nil, nil, nil)
	if _, err := resolver.Store(context.Background(), StoreInput{Data: []byte(`{"bomFormat":"CycloneDX"}`), Format: domain.SBOMFormatCycloneDX, BackendType: domain.SBOMStorageBlossom}); err != nil {
		t.Fatalf("Store with nil logger failed: %v", err)
	}
}

func TestStorageResolver_ResolveAndVerify(t *testing.T) {
	sbomData := []byte(`{"spdxVersion": "SPDX-2.3"}`)
	mockBlossom := &MockBlossomClient{Blobs: make(map[string][]byte)}
	resolver := NewStorageResolver(mockBlossom, nil, nil, slog.Default())

	stored, err := resolver.Store(context.Background(), StoreInput{
		Data:        sbomData,
		Format:      domain.SBOMFormatSPDX,
		BackendType: domain.SBOMStorageBlossom,
	})
	if err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	att := &domain.SBOMAttestation{Type: InTotoStatementType, Predicate: domain.SBOMPredicate{Digest: map[string]string{"sha256": stored.Hash}}}
	if err := SignAttestation(context.Background(), att, testNostrDSSESigner(t)); err != nil {
		t.Fatal(err)
	}
	verified, err := resolver.ResolveAndVerify(context.Background(), att, ResolveInput{Location: stored.Location})
	if err != nil {
		t.Fatalf("ResolveAndVerify failed: %v", err)
	}
	if string(verified) != string(sbomData) {
		t.Fatalf("verified payload = %s, want %s", verified, sbomData)
	}

	att.Predicate.Digest["sha256"] = "wronghash123456789012345678901234567890123456789012345678901234"
	_, err = resolver.ResolveAndVerify(context.Background(), att, ResolveInput{Location: stored.Location})
	if err == nil {
		t.Error("Expected hash verification to fail")
	}
}

func TestStorageResolver_StoreRejectsNonCanonicalBackends(t *testing.T) {
	resolver := NewStorageResolver(&MockBlossomClient{Blobs: make(map[string][]byte)}, nil, nil, slog.Default())

	for _, backend := range []domain.SBOMStorageType{domain.SBOMStorageOCI, domain.SBOMStoragePackage} {
		_, err := resolver.Store(context.Background(), StoreInput{Data: []byte(`{}`), Format: domain.SBOMFormatSPDX, BackendType: backend})
		if err == nil {
			t.Fatalf("Store(%s) succeeded, want unsupported backend error", backend)
		}
	}
}

func TestExtractBlossomHash(t *testing.T) {
	tests := []struct {
		name     string
		uri      string
		expected string
		wantErr  bool
	}{
		{
			name:     "simple hash",
			uri:      "https://blossom.example.com/abc123def456abc123def456abc123def456abc123def456abc123def456abcd",
			expected: "abc123def456abc123def456abc123def456abc123def456abc123def456abcd",
		},
		{
			name:     "with json extension",
			uri:      "https://blossom.example.com/abc123def456abc123def456abc123def456abc123def456abc123def456abcd.json",
			expected: "abc123def456abc123def456abc123def456abc123def456abc123def456abcd",
		},
		{
			name:    "invalid - too short",
			uri:     "https://blossom.example.com/abc123",
			wantErr: true,
		},
		{
			name:    "invalid - non-hex",
			uri:     "https://blossom.example.com/" + strings.Repeat("z", 64),
			wantErr: true,
		},
		{
			name:    "invalid - no slash",
			uri:     "abc123def456abc123def456abc123def456abc123def456abc123def456abcd",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hash, err := extractBlossomHash(tc.uri)
			if tc.wantErr {
				if err == nil {
					t.Errorf("Expected error, got hash: %s", hash)
				}
				return
			}
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}
			if hash != tc.expected {
				t.Errorf("Hash = %q, want %q", hash, tc.expected)
			}
		})
	}
}

func TestStorageResolver_UnsupportedBackend(t *testing.T) {
	logger := slog.Default()
	resolver := NewStorageResolver(nil, nil, nil, logger)

	// Test unsupported storage type.
	_, err := resolver.Resolve(context.Background(), ResolveInput{
		Location: domain.SBOMLocation{
			Type: "unsupported",
			URI:  "https://example.com/sbom",
		},
	})
	if err == nil {
		t.Error("Expected error for unsupported storage type")
	}
}

func TestStorageResolver_MissingClient(t *testing.T) {
	logger := slog.Default()
	resolver := NewStorageResolver(nil, nil, nil, logger)

	// Test Blossom without client.
	_, err := resolver.Resolve(context.Background(), ResolveInput{
		Location: domain.SBOMLocation{
			Type: domain.SBOMStorageBlossom,
			URI:  "https://blossom.example.com/abc123",
		},
	})
	if err == nil {
		t.Error("Expected error when Blossom client not configured")
	}

	// Test OCI without repo.
	_, err = resolver.Resolve(context.Background(), ResolveInput{
		Location: domain.SBOMLocation{
			Type: domain.SBOMStorageOCI,
			URI:  "sha256:abc123",
		},
	})
	if err == nil {
		t.Error("Expected error when OCI repo not provided")
	}
}

type recordingBlossomClient struct {
	urls []string
	data []byte
}

func (r *recordingBlossomClient) Download(_ context.Context, value string) ([]byte, error) {
	r.urls = append(r.urls, value)
	return append([]byte(nil), r.data...), nil
}

func (r *recordingBlossomClient) Upload(context.Context, []byte, string) (*blossom.BlobDescriptor, error) {
	return nil, fmt.Errorf("unexpected upload")
}

func TestStorageResolver_ResolveFromBlossomPassesCanonicalURLToClient(t *testing.T) {
	client := &recordingBlossomClient{
		data: []byte(`{"spdxVersion": "SPDX-2.3"}`),
	}
	resolver := NewStorageResolver(client, nil, nil, slog.Default())
	uri := "https://blossom.example.com/" + testHash + ".json"

	_, err := resolver.Resolve(context.Background(), ResolveInput{
		Location: domain.SBOMLocation{
			Type: domain.SBOMStorageBlossom,
			URI:  uri,
		},
	})
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if len(client.urls) != 1 {
		t.Fatalf("Download calls = %d, want 1", len(client.urls))
	}
	if client.urls[0] != uri {
		t.Fatalf("Download arg = %q, want %q", client.urls[0], uri)
	}
}
