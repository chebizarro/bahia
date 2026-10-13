package controlplane

import (
	"context"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
)

func TestBackupDelegationIssuerRequiresSignedCurrentGrant(t *testing.T) {
	store := testDelegationStore(t)
	operator, serviceKey, human := nostr.Generate(), nostr.Generate(), nostr.Generate()
	lists := NewDelegationLists(nil, store, []string{operator.Public().Hex()}, []string{serviceKey.Public().Hex()}, nil)
	signer, err := NewPrivateKeySigner(serviceKey.Hex())
	require.NoError(t, err)
	reactor := NewReactor(Config{}, nil, nil, signer, nil, WithDelegationLists(lists))
	event := nostr.Event{Kind: nostr.Kind(KindBackupRunRequest), CreatedAt: nostr.Now(), Tags: nostr.Tags{{"d", "backup:test"}}}
	require.NoError(t, event.Sign(serviceKey))
	authority := backupDelegationRecord{ServicePubkey: serviceKey.Public().Hex(), RequesterPubkey: human.Public().Hex()}
	require.ErrorContains(t, reactor.validateBackupIssuer(context.Background(), &event, authority, true), "not granted")
	grant := signedDelegationList(t, operator, serviceKey.Public(), 100, human.Public())
	require.NoError(t, lists.Apply(&grant))
	require.ErrorContains(t, reactor.validateBackupIssuer(context.Background(), &event, authority, true), "not granted")
	lists.mu.Lock()
	lists.caughtUp = true
	lists.mu.Unlock()
	require.NoError(t, reactor.validateBackupIssuer(context.Background(), &event, authority, true))
	revoke := signedDelegationList(t, operator, serviceKey.Public(), 101)
	require.NoError(t, lists.Apply(&revoke))
	require.ErrorContains(t, reactor.validateBackupIssuer(context.Background(), &event, authority, true), "not granted")
}
