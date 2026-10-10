package app

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/stretchr/testify/require"
)

// reloadSignerConfig is a startup config whose service identity is a remote
// signer session opened through the stubbed openServiceSigner.
func reloadSignerConfig(t *testing.T, service nostr.PubKey, client nostr.SecretKey) *config.Config {
	t.Helper()
	cfg := startupTestConfig("emergency")
	cfg.Nostr.PrivateKey = ""
	nostrCfg := remoteTestConfig(service, client)
	cfg.Nostr.PublicKey, cfg.Nostr.Signer = nostrCfg.PublicKey, nostrCfg.Signer
	cfg.Nostr.LocalStore.Path = filepath.Join(t.TempDir(), "daemon.bolt")
	return cfg
}

// shutdown runs the application's orderly shutdown, which releases its hold
// on the service signer last.
func shutdown(t *testing.T, a *App) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, a.RunContext(ctx))
}

func requireSigns(t *testing.T, a *App, service nostr.PubKey) {
	t.Helper()
	ev := &nostr.Event{Kind: 1, CreatedAt: nostr.Now(), Content: "reload"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, a.serviceSigner.Keyer().SignEvent(ctx, ev))
	require.True(t, ev.VerifySignature())
	require.Equal(t, service, ev.PubKey)
}

func requireSessions(t *testing.T, opener *fakeSignerOpener, opens, closes int, live ...bool) {
	t.Helper()
	gotOpens, gotCloses := opener.counts()
	require.Equal(t, opens, gotOpens, "signer sessions opened")
	require.Equal(t, closes, gotCloses, "signer sessions closed")
	require.Equal(t, live, opener.live(), "signer session lifetimes still running")
}

func TestReloadWithUnchangedSignerReusesTheSession(t *testing.T) {
	restoreDBHooks := stubDBHooks(t, errors.New("database unavailable"), nil)
	defer restoreDBHooks()
	service, client := nostr.Generate(), nostr.Generate()
	opener := stubServiceSignerOpen(t, service)

	running, err := New(reloadSignerConfig(t, service.Public(), client))
	require.NoError(t, err)
	candidate, err := New(reloadSignerConfig(t, service.Public(), client), Replacing(running))
	require.NoError(t, err)
	require.Same(t, running.serviceSigner, candidate.serviceSigner)
	requireSessions(t, opener, 1, 0, true)

	// The replaced application must not close the session its replacement uses.
	shutdown(t, running)
	requireSessions(t, opener, 1, 0, true)
	requireSigns(t, candidate, service.Public())

	// Final shutdown closes the session exactly once; repeated releases are
	// no-ops.
	shutdown(t, candidate)
	requireSessions(t, opener, 1, 1, false)
	running.closeServiceKeyer()
	candidate.closeServiceKeyer()
	requireSessions(t, opener, 1, 1, false)
}

func TestReloadCandidateFailureKeepsTheRunningSigner(t *testing.T) {
	restoreDBHooks := stubDBHooks(t, errors.New("database unavailable"), nil)
	defer restoreDBHooks()
	service, client := nostr.Generate(), nostr.Generate()
	opener := stubServiceSignerOpen(t, service)

	running, err := New(reloadSignerConfig(t, service.Public(), client))
	require.NoError(t, err)

	// Same signer, but the candidate fails after taking its hold on it.
	broken := reloadSignerConfig(t, service.Public(), client)
	broken.SoulFactory.Enabled = true
	_, err = New(broken, Replacing(running))
	require.ErrorContains(t, err, "soul_factory.relays")
	requireSessions(t, opener, 1, 0, true)
	requireSigns(t, running, service.Public())

	shutdown(t, running)
	requireSessions(t, opener, 1, 1, false)
}

func TestReloadWithChangedSignerOpensANewSession(t *testing.T) {
	restoreDBHooks := stubDBHooks(t, errors.New("database unavailable"), nil)
	defer restoreDBHooks()
	service := nostr.Generate()
	opener := stubServiceSignerOpen(t, service)

	running, err := New(reloadSignerConfig(t, service.Public(), nostr.Generate()))
	require.NoError(t, err)
	// A rotated client key is a different signer.
	candidate, err := New(reloadSignerConfig(t, service.Public(), nostr.Generate()), Replacing(running))
	require.NoError(t, err)
	require.NotSame(t, running.serviceSigner, candidate.serviceSigner)
	requireSessions(t, opener, 2, 0, true, true)

	// The replaced application closes its own session; the new one keeps
	// signing.
	shutdown(t, running)
	requireSessions(t, opener, 2, 1, false, true)
	requireSigns(t, candidate, service.Public())

	shutdown(t, candidate)
	requireSessions(t, opener, 2, 2, false, false)
}

func TestReloadWithChangedSignerFailureClosesOnlyTheCandidateSession(t *testing.T) {
	restoreDBHooks := stubDBHooks(t, errors.New("database unavailable"), nil)
	defer restoreDBHooks()
	service := nostr.Generate()
	opener := stubServiceSignerOpen(t, service)

	running, err := New(reloadSignerConfig(t, service.Public(), nostr.Generate()))
	require.NoError(t, err)

	broken := reloadSignerConfig(t, service.Public(), nostr.Generate())
	broken.SoulFactory.Enabled = true
	_, err = New(broken, Replacing(running))
	require.ErrorContains(t, err, "soul_factory.relays")
	requireSessions(t, opener, 2, 1, true, false)
	requireSigns(t, running, service.Public())

	shutdown(t, running)
	requireSessions(t, opener, 2, 2, false, false)
}
