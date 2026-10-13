package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConfigFabricSignerRequiresRemotePinnedIdentity(t *testing.T) {
	for name, tc := range map[string]struct {
		config ConfigFabricConfig
		want   string
	}{
		"absent":          {},
		"nip46":           {config: ConfigFabricConfig{PublicKey: testServicePubkey, Signer: NostrSignerConfig{Method: NostrSignerNIP46, BunkerURI: testBunkerURI, ClientSecretKey: testClientSecret}}},
		"nip55l":          {config: ConfigFabricConfig{PublicKey: testServicePubkey, Signer: NostrSignerConfig{Method: NostrSignerNIP55L}}},
		"local":           {config: ConfigFabricConfig{PublicKey: testServicePubkey, Signer: NostrSignerConfig{Method: NostrSignerLocal}}, want: "must be nip46 or nip55l"},
		"no method":       {config: ConfigFabricConfig{PublicKey: testServicePubkey}, want: "must be nip46 or nip55l"},
		"missing key pin": {config: ConfigFabricConfig{Signer: NostrSignerConfig{Method: NostrSignerNIP46, BunkerURI: testBunkerURI, ClientSecretKey: testClientSecret}}, want: "requires nostr.public_key"},
		"missing bunker":  {config: ConfigFabricConfig{PublicKey: testServicePubkey, Signer: NostrSignerConfig{Method: NostrSignerNIP46, ClientSecretKey: testClientSecret}}, want: "requires nostr.signer.bunker_uri"},
	} {
		t.Run(name, func(t *testing.T) {
			err := tc.config.validate()
			if tc.want != "" {
				require.ErrorContains(t, err, tc.want)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestLoadConfigFabricSignerFromExplicitConfig(t *testing.T) {
	cfg, err := Load("")
	require.NoError(t, err)
	require.Equal(t, ConfigFabricConfig{}, cfg.ConfigFabric)
	cfg.ConfigFabric = ConfigFabricConfig{PublicKey: strings.ToUpper(testServicePubkey), Signer: NostrSignerConfig{Method: "NIP46", BunkerURI: testBunkerURI, ClientSecretKey: testClientSecret}}
	require.NoError(t, cfg.Validate())
	require.Equal(t, testServicePubkey, cfg.ConfigFabric.PublicKey)
	require.Equal(t, NostrSignerNIP46, cfg.ConfigFabric.Signer.Method)
}
