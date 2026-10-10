package servicesigner

import (
	"testing"

	"github.com/openagentsinc/bahia/internal/config"
	"github.com/stretchr/testify/require"
)

// Without an override, method nip55l reaches the real D-Bus keyer: an
// unreachable bus fails Open instead of reporting the method unavailable.
func TestOpenNIP55LDefaultConstructorReachesDBus(t *testing.T) {
	cfg := config.NostrConfig{PublicKey: testService.Public().Hex(), Signer: config.NostrSignerConfig{
		Method: config.NostrSignerNIP55L,
		NIP55L: config.NostrSignerNIP55LConfig{AppID: "bahia", BusAddress: "unix:path=" + t.TempDir() + "/no-bus"},
	}}
	signer, err := Open(t.Context(), cfg, Options{})
	require.Error(t, err)
	require.Nil(t, signer)
	require.ErrorContains(t, err, "open nip55l service signer")
	require.NotContains(t, err.Error(), "not available in this build")
}
