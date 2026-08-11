package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/config"
)

func TestLoadSoulFactoryConcordCommunitiesFromSecretSources(t *testing.T) {
	const envBundle = `{"community_id":"from-env"}`
	t.Setenv("FLEET_CONCORD_INVITE", envBundle)
	fileBundle := []byte(`{"community_id":"from-file"}`)
	filePath := filepath.Join(t.TempDir(), "concord.json")
	if err := os.WriteFile(filePath, fileBundle, 0o600); err != nil {
		t.Fatalf("write invite bundle: %v", err)
	}

	communities, err := loadSoulFactoryConcordCommunities([]config.ConcordCommunity{
		{CommunityID: strings.Repeat("a", 64), InviteBundleEnv: "FLEET_CONCORD_INVITE"},
		{CommunityID: strings.Repeat("b", 64), InviteBundleFile: filePath},
	})
	if err != nil {
		t.Fatalf("loadSoulFactoryConcordCommunities() error = %v", err)
	}
	if len(communities) != 2 {
		t.Fatalf("communities = %d, want 2", len(communities))
	}
	if string(communities[0].InviteBundle) != envBundle || string(communities[1].InviteBundle) != string(fileBundle) {
		t.Fatalf("loaded invite bundles = %q / %q", communities[0].InviteBundle, communities[1].InviteBundle)
	}
}

func TestLoadSoulFactoryConcordCommunitiesKeepsSealedCustodyOnDisk(t *testing.T) {
	sealedPath := filepath.Join(t.TempDir(), "concord.sealed")
	if err := os.WriteFile(sealedPath, []byte("AjA1Nzg5-not-real-ciphertext"), 0o600); err != nil {
		t.Fatalf("write sealed custody: %v", err)
	}

	communities, err := loadSoulFactoryConcordCommunities([]config.ConcordCommunity{{
		CommunityID:            strings.Repeat("c", 64),
		InviteBundleSealedFile: sealedPath,
	}})
	if err != nil {
		t.Fatalf("loadSoulFactoryConcordCommunities() error = %v", err)
	}
	if len(communities) != 1 {
		t.Fatalf("communities = %d, want 1", len(communities))
	}
	if communities[0].SealedBundlePath != sealedPath {
		t.Fatalf("sealed bundle path = %q", communities[0].SealedBundlePath)
	}
	// Sealed custody is opened through Signet at use time, never slurped into
	// process configuration at boot.
	if len(communities[0].InviteBundle) != 0 {
		t.Fatalf("sealed custody was read into config: %s", communities[0].InviteBundle)
	}
}

func TestLoadSoulFactoryConcordCommunitiesFailsForMissingSecret(t *testing.T) {
	_, err := loadSoulFactoryConcordCommunities([]config.ConcordCommunity{{
		CommunityID:     strings.Repeat("a", 64),
		InviteBundleEnv: "MISSING_CONCORD_INVITE",
	}})
	if err == nil || !strings.Contains(err.Error(), "unset or empty") {
		t.Fatalf("loadSoulFactoryConcordCommunities() error = %v", err)
	}
}
