package app

import (
	"context"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/adapters/blossom"
	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type recordingViewPublisher struct{ policies int }

func (r *recordingViewPublisher) PublishSoulRuntimePolicy(context.Context, nostrAdapter.SoulRuntimePolicy) error {
	r.policies++
	return nil
}
func (r *recordingViewPublisher) PublishBlossomAdmin(context.Context, []string, map[string]string) error {
	return nil
}
func (r *recordingViewPublisher) PublishBlossomBlob(context.Context, string, blossom.BlobDescriptor) error {
	return nil
}

type recordingAllowlistPublisher struct {
	published map[string][]string
}

func (r *recordingAllowlistPublisher) PublishOperatorAllowlist(_ context.Context, scope string, pubkeys []string) error {
	if r.published == nil {
		r.published = map[string][]string{}
	}
	r.published[scope] = append([]string{}, pubkeys...)
	return nil
}

// The published allowlists mirror the validated config lists the daemon
// enforces: nostr.authorized_pubkeys for continuity, and
// soul_factory.authorized_pubkeys only while the Soul Factory is enabled.
func TestOperatorAllowlistSetsMirrorConfig(t *testing.T) {
	alice, bob, carol := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	cfg := &config.Config{}
	cfg.Nostr.AuthorizedPubkeys = []string{alice, bob}
	cfg.SoulFactory.Enabled = true
	cfg.SoulFactory.AuthorizedPubkeys = []string{carol}

	sets := operatorAllowlistSets(cfg)
	require.Equal(t, []operatorAllowlistSet{
		{scope: kinds.OperatorAllowlistScopeContinuity, pubkeys: []string{alice, bob}},
		{scope: kinds.OperatorAllowlistScopeSoulFactory, pubkeys: []string{carol}},
	}, sets)
	// The published sets must not alias validated config.
	sets[0].pubkeys[0] = "mutated"
	require.Equal(t, alice, cfg.Nostr.AuthorizedPubkeys[0])

	cfg.SoulFactory.Enabled = false
	sets = operatorAllowlistSets(cfg)
	require.Equal(t, []string{alice, bob}, sets[0].pubkeys)
	require.Empty(t, sets[1].pubkeys, "a disabled Soul Factory authorizes nobody, so its scope is tombstoned")
	require.Equal(t, kinds.OperatorAllowlistScopeSoulFactory, sets[1].scope)
}

// The runner publishes every configured scope at startup, after the runtime
// policy, and stays idle until shutdown.
func TestOperationalViewsRunnerPublishesOperatorAllowlistsAtStartup(t *testing.T) {
	alice := strings.Repeat("a", 64)
	views := &recordingViewPublisher{}
	allowlists := &recordingAllowlistPublisher{}
	runner := &operationalViewsRunner{
		publisher:  views,
		allowlists: allowlists,
		allowlistSets: []operatorAllowlistSet{
			{scope: kinds.OperatorAllowlistScopeContinuity, pubkeys: []string{alice}},
			{scope: kinds.OperatorAllowlistScopeSoulFactory},
		},
		logger: zap.NewNop(),
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, runner.Run(ctx))
	require.Equal(t, 1, views.policies)
	require.Equal(t, map[string][]string{
		kinds.OperatorAllowlistScopeContinuity:  {alice},
		kinds.OperatorAllowlistScopeSoulFactory: {},
	}, allowlists.published)

	// Without a confidential encryptor no allowlist publisher is wired and the
	// runner still publishes the runtime policy.
	plain := &operationalViewsRunner{publisher: views, logger: zap.NewNop()}
	require.NoError(t, plain.Run(ctx))
	require.Equal(t, 2, views.policies)
}
