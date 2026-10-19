package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSidecarAckSignerRemotePinAndLegacyExclusion(t *testing.T) {
	for name, tc := range map[string]struct {
		ack  SidecarAckSignerConfig
		file string
		want string
	}{
		"absent":      {},
		"legacy file": {file: "/run/secrets/sidecar-ack-dev"},
		"nip46":       {ack: SidecarAckSignerConfig{PublicKey: testServicePubkey, NostrSignerConfig: NostrSignerConfig{Method: NostrSignerNIP46, BunkerURI: testBunkerURI, ClientSecretKeyFile: "/run/secrets/sidecar-ack-client"}}},
		"nip55l":      {ack: SidecarAckSignerConfig{PublicKey: testServicePubkey, NostrSignerConfig: NostrSignerConfig{Method: NostrSignerNIP55L}}},
		"both":        {ack: SidecarAckSignerConfig{PublicKey: testServicePubkey, NostrSignerConfig: NostrSignerConfig{Method: NostrSignerNIP55L}}, file: "/run/secrets/ack", want: "mutually exclusive"},
		"local":       {ack: SidecarAckSignerConfig{PublicKey: testServicePubkey, NostrSignerConfig: NostrSignerConfig{Method: NostrSignerLocal}}, want: "must be nip46 or nip55l"},
		"missing pin": {ack: SidecarAckSignerConfig{NostrSignerConfig: NostrSignerConfig{Method: NostrSignerNIP46, BunkerURI: testBunkerURI, ClientSecretKeyFile: "/run/secrets/client"}}, want: "requires nostr.public_key"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := Defaults()
			cfg.Nostr.Sidecar.AckSigner = tc.ack
			cfg.Nostr.Sidecar.AckPrivateKeyFile = tc.file
			err := cfg.Validate()
			if tc.want != "" {
				require.ErrorContains(t, err, tc.want)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestLoadFlatSidecarAckSignerConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := "nostr:\n  sidecar:\n    ack_signer:\n      public_key: " + strings.ToUpper(testServicePubkey) + "\n      method: nip46\n      bunker_uri: " + testBunkerURI + "\n      client_secret_key_file: /run/secrets/sidecar-ack-client\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, testServicePubkey, cfg.Nostr.Sidecar.AckSigner.PublicKey)
	require.Equal(t, NostrSignerNIP46, cfg.Nostr.Sidecar.AckSigner.Method)
	require.Empty(t, cfg.Nostr.Sidecar.AckPrivateKeyFile)
}
