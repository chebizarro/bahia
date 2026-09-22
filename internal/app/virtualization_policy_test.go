package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/signing"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
)

func TestVirtualizationTrustPolicyVerifiesSignedDigestAndHost(t *testing.T) {
	ctx := context.Background()
	signer := keyer.NewPlainKeySigner([32]byte{9})
	key, err := signer.GetPublicKey(ctx)
	require.NoError(t, err)
	org, host, trust := uuid.New(), uuid.New(), uuid.New()
	store := &vmAppStore{InMemoryNostrEventRepository: repository.NewInMemoryNostrEventRepository(), checkpoint: make(chan struct{}, 1)}
	digest := "sha256:" + strings.Repeat("a", 64)
	body, err := json.Marshal(signing.NostrArtifactAttestation{ImageDigest: digest, Approved: true})
	require.NoError(t, err)
	ev := &nostr.Event{Kind: signing.NostrSignatureKind, CreatedAt: nostr.Now(), Content: string(body)}
	require.NoError(t, (vmAppPublisher{store, signer}).PublishSignedEvent(ctx, ev))
	policy := &virtualizationPolicy{config: config.VirtualizationConfig{OperatorPubkeys: []string{key.Hex()}, Hosts: []config.VirtualizationHostPolicy{{OrgID: org, HostID: host, TrustPolicyRef: trust, TrustedSigners: []string{key.Hex()}}}}, rbac: auth.NewRBAC(vmAppMembers{org}), events: store}
	h := &domain.VirtualizationHost{VirtualizationResourceMeta: domain.VirtualizationResourceMeta{ID: host, OrgID: org}, TrustPolicyRef: trust, Enabled: true}
	provenance := domain.VMProvenance{EventID: ev.ID.Hex(), Signer: key.Hex(), Verified: true, VerifiedAt: time.Now()}
	require.NoError(t, policy.VerifyPlaneProvenance(ctx, h, digest, provenance))
	require.Error(t, policy.VerifyPlaneProvenance(ctx, h, "sha256:"+strings.Repeat("b", 64), provenance))
	provenance.EventID = strings.Repeat("c", 64)
	require.Error(t, policy.VerifyPlaneProvenance(ctx, h, digest, provenance), "caller Verified=true is never proof")
	provenance.EventID = ev.ID.Hex()
	for _, mutate := range []func(*domain.VirtualizationHost){func(h *domain.VirtualizationHost) { h.OrgID = uuid.New() }, func(h *domain.VirtualizationHost) { h.TrustPolicyRef = uuid.New() }, func(h *domain.VirtualizationHost) { h.Enabled = false }} {
		copy := *h
		mutate(&copy)
		require.Error(t, policy.VerifyPlaneProvenance(ctx, &copy, digest, provenance))
	}
	principal := &auth.Principal{Subject: key.Hex(), PubKey: key.Hex(), Method: auth.MethodNIP98}
	require.NoError(t, policy.AuthorizeExecutionPlane(ctx, principal, org))
	require.Error(t, policy.AuthorizeExecutionPlane(ctx, principal, uuid.New()))
	principal.PubKey = strings.Repeat("d", 64)
	require.Error(t, policy.AuthorizeExecutionPlane(ctx, principal, org))
	require.NoError(t, policy.ValidatePlaneConfiguration(ctx, h, domain.ExecutionPlaneConfiguration{Network: domain.VMNetwork{Mode: domain.VMNetworkIsolated}}))
	require.Error(t, policy.ValidatePlaneConfiguration(ctx, h, domain.ExecutionPlaneConfiguration{Network: domain.VMNetwork{Mode: domain.VMNetworkBridged}}))
	require.Error(t, policy.ValidatePlaneConfiguration(ctx, h, domain.ExecutionPlaneConfiguration{Network: domain.VMNetwork{Mode: domain.VMNetworkIsolated, PassthroughDeviceRefs: []uuid.UUID{uuid.New()}}}))
	// Mutating the stored body while keeping the event ID/signature fails closed.
	badStore := &vmAppStore{InMemoryNostrEventRepository: repository.NewInMemoryNostrEventRepository(), checkpoint: make(chan struct{}, 1)}
	record, err := store.GetByID(ctx, ev.ID.Hex())
	require.NoError(t, err)
	record.Content += " "
	_, err = badStore.Record(ctx, record)
	require.NoError(t, err)
	policy.events = badStore
	require.Error(t, policy.VerifyPlaneProvenance(ctx, h, digest, provenance))
}

func TestVirtualizationQueueCoalescesWithoutBlockingAdmission(t *testing.T) {
	r := &virtualizationRuntime{pending: map[vmWork]struct{}{}, wake: make(chan struct{}, 1)}
	key := vmWork{org: uuid.New(), id: uuid.New(), kind: domain.VMOperationResource}
	for i := 0; i < 10000; i++ {
		r.enqueue(key)
	}
	require.Len(t, r.pending, 1)
	require.Len(t, r.wake, 1)
}
