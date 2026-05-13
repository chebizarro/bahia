package sbom

import (
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

func TestAttestationBuilder_BuildAttestation(t *testing.T) {
	builder := NewAttestationBuilder("syft", "0.95.0", "npub1test...")

	sampleSPDX := []byte(`{
		"spdxVersion": "SPDX-2.3",
		"dataLicense": "CC0-1.0",
		"SPDXID": "SPDXRef-DOCUMENT",
		"name": "test-sbom",
		"creationInfo": {
			"created": "2024-01-01T00:00:00Z",
			"creators": ["Tool: syft-0.95.0"]
		},
		"packages": [
			{
				"name": "lodash",
				"versionInfo": "4.17.21",
				"supplier": "Organization: npm",
				"externalRefs": [{"referenceType": "purl", "referenceLocator": "pkg:npm/lodash@4.17.21"}]
			}
		],
		"relationships": [{"relationshipType": "DEPENDS_ON"}]
	}`)

	input := BuildAttestationInput{
		SubjectName:   "ghcr.io/org/myapp:v1.0.0",
		SubjectDigest: "sha256:abc123def456",
		SBOMData:      sampleSPDX,
		Format:        domain.SBOMFormatSPDX,
		Location: domain.SBOMLocation{
			Type:      domain.SBOMStorageBlossom,
			URI:       "https://blossom.example.com/abc123",
			MediaType: MediaTypeSPDX,
		},
		ParsedPackages: []domain.SBOMPackage{
			{Name: "lodash", Version: "4.17.21", PURL: "pkg:npm/lodash@4.17.21"},
		},
	}

	att, err := builder.BuildAttestation(input)
	if err != nil {
		t.Fatalf("BuildAttestation failed: %v", err)
	}

	// Verify in-toto statement type.
	if att.Type != InTotoStatementType {
		t.Errorf("Type = %q, want %q", att.Type, InTotoStatementType)
	}

	// Verify subject.
	if len(att.Subject) != 1 {
		t.Fatalf("Subject count = %d, want 1", len(att.Subject))
	}
	if att.Subject[0].Name != input.SubjectName {
		t.Errorf("Subject.Name = %q, want %q", att.Subject[0].Name, input.SubjectName)
	}
	if att.Subject[0].Digest["sha256"] != "abc123def456" {
		t.Errorf("Subject.Digest[sha256] = %q, want %q", att.Subject[0].Digest["sha256"], "abc123def456")
	}

	// Verify predicate type.
	if att.PredicateType != domain.AttestationTypeSPDX {
		t.Errorf("PredicateType = %q, want %q", att.PredicateType, domain.AttestationTypeSPDX)
	}

	// Verify predicate fields.
	if att.Predicate.Format != domain.SBOMFormatSPDX {
		t.Errorf("Predicate.Format = %q, want %q", att.Predicate.Format, domain.SBOMFormatSPDX)
	}
	if att.Predicate.Location.Type != domain.SBOMStorageBlossom {
		t.Errorf("Predicate.Location.Type = %q, want %q", att.Predicate.Location.Type, domain.SBOMStorageBlossom)
	}
	if att.Predicate.Generator.ID != "syft" {
		t.Errorf("Predicate.Generator.ID = %q, want %q", att.Predicate.Generator.ID, "syft")
	}

	// Verify NTIA compliance check ran.
	if att.Predicate.NTIA == nil {
		t.Fatal("Predicate.NTIA is nil")
	}
}

func TestAttestationBuilder_CycloneDX(t *testing.T) {
	builder := NewAttestationBuilder("trivy", "0.48.0", "")

	sampleCDX := []byte(`{
		"bomFormat": "CycloneDX",
		"specVersion": "1.5",
		"metadata": {
			"timestamp": "2024-01-01T00:00:00Z",
			"authors": [{"name": "test"}],
			"supplier": {"name": "Acme Inc"}
		},
		"components": [
			{"name": "express", "version": "4.18.2", "purl": "pkg:npm/express@4.18.2"}
		],
		"dependencies": [{"ref": "express"}]
	}`)

	input := BuildAttestationInput{
		SubjectName:   "docker.io/library/node:18",
		SubjectDigest: "sha256:def789ghi012",
		SBOMData:      sampleCDX,
		Format:        domain.SBOMFormatCycloneDX,
		Location: domain.SBOMLocation{
			Type:      domain.SBOMStorageOCI,
			URI:       "sha256:sbomdigest123",
			MediaType: MediaTypeCycloneDX,
		},
		ParsedPackages: []domain.SBOMPackage{
			{Name: "express", Version: "4.18.2", PURL: "pkg:npm/express@4.18.2"},
		},
	}

	att, err := builder.BuildAttestation(input)
	if err != nil {
		t.Fatalf("BuildAttestation failed: %v", err)
	}

	if att.PredicateType != domain.AttestationTypeCycloneDX {
		t.Errorf("PredicateType = %q, want %q", att.PredicateType, domain.AttestationTypeCycloneDX)
	}
}

func TestVerifySubjectDigest(t *testing.T) {
	att := &domain.SBOMAttestation{
		Subject: []domain.AttestationSubject{
			{
				Name:   "test-image",
				Digest: map[string]string{"sha256": "abc123def456"},
			},
		},
	}

	tests := []struct {
		name     string
		digest   string
		expected bool
	}{
		{"matching", "sha256:abc123def456", true},
		{"non-matching", "sha256:different", false},
		{"wrong algo", "sha512:abc123def456", false},
		{"invalid format", "notadigest", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := VerifySubjectDigest(att, tc.digest)
			if result != tc.expected {
				t.Errorf("VerifySubjectDigest(%q) = %v, want %v", tc.digest, result, tc.expected)
			}
		})
	}
}

func TestVerifyPayloadDigest(t *testing.T) {
	testData := []byte(`{"test": "data"}`)
	// SHA256 of testData (computed via: echo -n '{"test": "data"}' | sha256sum)
	expectedHash := "40b61fe1b15af0a4d5402735b26343e8cf8a045f4d81710e6108a21d91eaf366"

	att := &domain.SBOMAttestation{
		Predicate: domain.SBOMPredicate{
			Digest: map[string]string{"sha256": expectedHash},
		},
	}

	if !VerifyPayloadDigest(att, testData) {
		t.Error("VerifyPayloadDigest should return true for matching data")
	}

	if VerifyPayloadDigest(att, []byte("different data")) {
		t.Error("VerifyPayloadDigest should return false for non-matching data")
	}
}

func TestCheckNTIACompliance(t *testing.T) {
	// Fully compliant SPDX.
	compliantSPDX := []byte(`{
		"creationInfo": {
			"created": "2024-01-01T00:00:00Z",
			"creators": ["Tool: syft"]
		},
		"packages": [{"supplier": "Acme Inc"}],
		"relationships": [{"relationshipType": "DEPENDS_ON"}]
	}`)

	packages := []domain.SBOMPackage{
		{Name: "pkg1", Version: "1.0.0", PURL: "pkg:npm/pkg1@1.0.0"},
	}

	ntia := checkNTIACompliance(compliantSPDX, packages)

	if !ntia.HasTimestamp {
		t.Error("Expected HasTimestamp = true")
	}
	if !ntia.HasAuthor {
		t.Error("Expected HasAuthor = true")
	}
	if !ntia.HasSupplierName {
		t.Error("Expected HasSupplierName = true")
	}
	if !ntia.HasComponentName {
		t.Error("Expected HasComponentName = true")
	}
	if !ntia.HasComponentVersion {
		t.Error("Expected HasComponentVersion = true")
	}
	if !ntia.HasUniqueID {
		t.Error("Expected HasUniqueID = true")
	}
	if !ntia.HasRelationship {
		t.Error("Expected HasRelationship = true")
	}
	if !ntia.IsCompliant {
		t.Error("Expected IsCompliant = true")
	}
}

func TestSerializeAndParseAttestation(t *testing.T) {
	original := &domain.SBOMAttestation{
		Type: InTotoStatementType,
		Subject: []domain.AttestationSubject{
			{Name: "test", Digest: map[string]string{"sha256": "abc123"}},
		},
		PredicateType: domain.AttestationTypeSPDX,
		Predicate: domain.SBOMPredicate{
			Format: domain.SBOMFormatSPDX,
			Location: domain.SBOMLocation{
				Type: domain.SBOMStorageBlossom,
				URI:  "https://example.com/abc123",
			},
			Digest:    map[string]string{"sha256": "def456"},
			Timestamp: time.Now().UTC().Truncate(time.Second),
		},
	}

	data, err := SerializeAttestation(original)
	if err != nil {
		t.Fatalf("SerializeAttestation failed: %v", err)
	}

	parsed, err := ParseAttestation(data)
	if err != nil {
		t.Fatalf("ParseAttestation failed: %v", err)
	}

	if parsed.Type != original.Type {
		t.Errorf("Type mismatch: got %q, want %q", parsed.Type, original.Type)
	}
	if parsed.PredicateType != original.PredicateType {
		t.Errorf("PredicateType mismatch: got %q, want %q", parsed.PredicateType, original.PredicateType)
	}
	if len(parsed.Subject) != len(original.Subject) {
		t.Errorf("Subject length mismatch: got %d, want %d", len(parsed.Subject), len(original.Subject))
	}
}

func TestMediaTypeForFormat(t *testing.T) {
	tests := []struct {
		format   domain.SBOMFormat
		expected string
	}{
		{domain.SBOMFormatSPDX, MediaTypeSPDX},
		{domain.SBOMFormatCycloneDX, MediaTypeCycloneDX},
		{"unknown", "application/json"},
	}

	for _, tc := range tests {
		result := MediaTypeForFormat(tc.format)
		if result != tc.expected {
			t.Errorf("MediaTypeForFormat(%q) = %q, want %q", tc.format, result, tc.expected)
		}
	}
}
