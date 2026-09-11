package gitea

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReloadMirrorReadCredentialRefValidatesBeforeSwap(t *testing.T) {
	const oldRef = "22222222-2222-4222-8222-222222222222"
	const newRef = "33333333-3333-4333-8333-333333333333"
	initiator := &Initiator{cfg: InitiatorConfig{MirrorReadCredentialRef: oldRef}}

	require.Error(t, initiator.ReloadMirrorReadCredentialRef("not-a-uuid"))
	require.Equal(t, oldRef, initiator.cfg.MirrorReadCredentialRef)
	require.NoError(t, initiator.ReloadMirrorReadCredentialRef(newRef))
	require.Equal(t, newRef, initiator.cfg.MirrorReadCredentialRef)
}
