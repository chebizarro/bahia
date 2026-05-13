package sbom

import (
	"context"
	"log/slog"
	"testing"

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

func TestStorageResolver_ResolveAndVerify(t *testing.T) {
	sbomData := []byte(`{"spdxVersion": "SPDX-2.3"}`)
	// Actual SHA256 of sbomData
	actualHash := "f99b72f98a5c5d2a93e93c7e75c5e5b5c5a5d5e5f5a5b5c5d5e5f5a5b5c5d5e5f5" // This is illustrative

	// Use a 64-char hash for the blob key
	blobKey := "abc123def456abc123def456abc123def456abc123def456abc123def456abcdef"

	mockBlossom := &MockBlossomClient{
		Blobs: map[string][]byte{
			blobKey: sbomData,
		},
	}

	logger := slog.Default()
	resolver := NewStorageResolver(mockBlossom, nil, nil, logger)

	// Create attestation with a mismatched hash (should fail verification).
	att := &domain.SBOMAttestation{
		Predicate: domain.SBOMPredicate{
			Digest: map[string]string{"sha256": "wronghash123456789012345678901234567890123456789012345678901234"},
		},
	}

	// This should fail verification since hash doesn't match.
	_, err := resolver.ResolveAndVerify(context.Background(), att, ResolveInput{
		Location: domain.SBOMLocation{
			Type: domain.SBOMStorageBlossom,
			URI:  "https://blossom.example.com/" + blobKey,
		},
	})
	if err == nil {
		t.Error("Expected hash verification to fail")
	}

	_ = actualHash // Silence unused variable
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
