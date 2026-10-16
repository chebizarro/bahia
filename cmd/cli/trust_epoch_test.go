package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/pkg/client"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

type epochTestSubscription struct {
	events chan *nostr.Event
	eose   chan struct{}
}

func (s *epochTestSubscription) Events() <-chan *nostr.Event            { return s.events }
func (s *epochTestSubscription) EndOfStoredEvents() <-chan struct{}     { return s.eose }
func (s *epochTestSubscription) RelayEOSE() <-chan client.RelayEOSEInfo { return nil }
func (s *epochTestSubscription) Close()                                 {}

type epochTestPool struct{ sub *epochTestSubscription }

func (p epochTestPool) SubscribeAllWithEOSE(context.Context, []nostr.Filter) (client.Subscription, error) {
	return p.sub, nil
}

func TestCLIReadsResolveTrustEpochBeforeServicePool(t *testing.T) {
	var fixture struct {
		RootPubkey string      `json:"root_pubkey"`
		OldPubkey  string      `json:"old_pubkey"`
		NewPubkey  string      `json:"new_pubkey"`
		Epoch      nostr.Event `json:"epoch"`
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "web", "tests", "fixtures", "trust-epoch-envelopes.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &fixture))
	oldID, oldRoot := operatorServiceID, operatorRootPubkey
	operatorServiceID, operatorRootPubkey = "deployment-a", fixture.RootPubkey
	t.Cleanup(func() { operatorServiceID, operatorRootPubkey = oldID, oldRoot })
	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	storePath := filepath.Join(t.TempDir(), "events.db")
	events := make(chan *nostr.Event, 1)
	events <- &fixture.Epoch
	eose := make(chan struct{})
	close(eose)
	got, err := resolveCLITrustedPubkey(cmd, fixture.OldPubkey, storePath,
		epochTestPool{&epochTestSubscription{events: events, eose: eose}}, time.Second)
	require.NoError(t, err)
	require.Equal(t, fixture.NewPubkey, got)
	bad := fixture.Epoch
	bad.Tags = nostr.Tags{{"d", "trust-epoch:deployment-a"}, {"t", "wrong"}}
	badEvents := make(chan *nostr.Event, 1)
	badEvents <- &bad
	_, err = resolveCLITrustedPubkey(cmd, fixture.OldPubkey, storePath,
		epochTestPool{&epochTestSubscription{events: badEvents, eose: eose}}, time.Second)
	require.Error(t, err, "present but invalid head cannot fall back to old key")
}
