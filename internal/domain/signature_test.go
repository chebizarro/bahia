package domain

import "testing"

func TestArtifactSignature_NormalizeVerificationStatus(t *testing.T) {
	tests := []struct {
		name       string
		sig        ArtifactSignature
		wantStatus SignatureVerificationStatus
		wantOK     bool
	}{
		{"explicit verified derives bool", ArtifactSignature{VerificationStatus: SignatureStatusVerified}, SignatureStatusVerified, true},
		{"explicit discovered clears bool", ArtifactSignature{Verified: true, VerificationStatus: SignatureStatusDiscovered}, SignatureStatusDiscovered, false},
		{"legacy verified backfills status", ArtifactSignature{Verified: true}, SignatureStatusVerified, true},
		{"legacy error backfills rejected", ArtifactSignature{VerificationError: "bad signature"}, SignatureStatusRejected, false},
		{"empty legacy record is discovered", ArtifactSignature{}, SignatureStatusDiscovered, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.sig.NormalizeVerificationStatus()
			if tt.sig.VerificationStatus != tt.wantStatus {
				t.Fatalf("status = %q, want %q", tt.sig.VerificationStatus, tt.wantStatus)
			}
			if tt.sig.Verified != tt.wantOK {
				t.Fatalf("verified = %v, want %v", tt.sig.Verified, tt.wantOK)
			}
		})
	}
}

func TestSigningPolicy_AllowsType(t *testing.T) {
	tests := []struct {
		name    string
		allowed []SignatureType
		check   SignatureType
		want    bool
	}{
		{"empty allows all", nil, SignatureCosign, true},
		{"cosign allowed", []SignatureType{SignatureCosign}, SignatureCosign, true},
		{"nostr not allowed", []SignatureType{SignatureCosign}, SignatureNostr, false},
		{"multiple allowed", []SignatureType{SignatureCosign, SignatureNostr}, SignatureNostr, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &SigningPolicy{AllowedSignatureTypes: tt.allowed}
			if got := p.AllowsType(tt.check); got != tt.want {
				t.Errorf("AllowsType(%q) = %v, want %v", tt.check, got, tt.want)
			}
		})
	}
}

func TestSigningPolicy_TrustsSigner(t *testing.T) {
	tests := []struct {
		name    string
		trusted []string
		signer  string
		want    bool
	}{
		{"empty trusts all", nil, "alice@example.com", true},
		{"trusted signer", []string{"alice@example.com"}, "alice@example.com", true},
		{"untrusted signer", []string{"alice@example.com"}, "bob@example.com", false},
		{"pubkey trusted", []string{"npub1abc"}, "npub1abc", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &SigningPolicy{TrustedSigners: tt.trusted}
			if got := p.TrustsSigner(tt.signer); got != tt.want {
				t.Errorf("TrustsSigner(%q) = %v, want %v", tt.signer, got, tt.want)
			}
		})
	}
}
