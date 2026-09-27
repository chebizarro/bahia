package app

import (
	"sync"
	"testing"

	giteaAdapter "github.com/openagentsinc/bahia/internal/adapters/gitea"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

const (
	oldMirrorCredentialRef = "22222222-2222-4222-8222-222222222222"
	newMirrorCredentialRef = "33333333-3333-4333-8333-333333333333"
)

func reloadTestConfig(ref string) *config.Config {
	cfg := &config.Config{Mode: "full"}
	cfg.HiveCI.Initiator.MirrorReadCredentialRef = ref
	return cfg
}

func TestReloadConfigRejectsUnsupportedDeltaWithoutChangingActiveConfig(t *testing.T) {
	current := reloadTestConfig(oldMirrorCredentialRef)
	candidate := reloadTestConfig(oldMirrorCredentialRef)
	candidate.Mode = "emergency"
	application := &App{Config: current, Logger: zap.NewNop()}

	handled, err := application.ReloadConfig(candidate)
	require.NoError(t, err)
	require.False(t, handled)
	require.Same(t, current, application.configSnapshot())
}

func TestReloadConfigValidatesBeforeAtomicSwap(t *testing.T) {
	current := reloadTestConfig(oldMirrorCredentialRef)
	initiator := giteaAdapter.NewInitiator(nil, nil, nil, nil, nil, giteaAdapter.InitiatorConfig{
		MirrorReadCredentialRef: oldMirrorCredentialRef,
	}, zap.NewNop())
	application := &App{Config: current, Logger: zap.NewNop(), hiveCIInitiator: initiator}

	invalid := reloadTestConfig("not-a-uuid")
	handled, err := application.ReloadConfig(invalid)
	require.Error(t, err)
	require.False(t, handled)
	require.Same(t, current, application.configSnapshot())

	candidate := reloadTestConfig(newMirrorCredentialRef)
	handled, err = application.ReloadConfig(candidate)
	require.NoError(t, err)
	require.True(t, handled)
	require.Same(t, candidate, application.configSnapshot())
}

func TestReloadConfigAndShutdownReadsAreRaceFree(t *testing.T) {
	first := reloadTestConfig(oldMirrorCredentialRef)
	second := reloadTestConfig(newMirrorCredentialRef)
	initiator := giteaAdapter.NewInitiator(nil, nil, nil, nil, nil, giteaAdapter.InitiatorConfig{
		MirrorReadCredentialRef: oldMirrorCredentialRef,
	}, zap.NewNop())
	application := &App{Config: first, Logger: zap.NewNop(), hiveCIInitiator: initiator}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(offset int) {
			defer wg.Done()
			for iteration := 0; iteration < 100; iteration++ {
				if (iteration+offset)%2 == 0 {
					_, _ = application.ReloadConfig(first)
				} else {
					_, _ = application.ReloadConfig(second)
				}
				_ = application.configSnapshot().Server.ShutdownTimeout
			}
		}(i)
	}
	wg.Wait()
}
