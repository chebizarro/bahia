package controlplane

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"path/filepath"
)

func signedNonArcanaIntent(t *testing.T, key, domainName, op, coordinate string, org uuid.UUID, content map[string]any) *nostr.Event {
	t.Helper()
	raw, err := json.Marshal(content)
	require.NoError(t, err)
	ev := &nostr.Event{Kind: nostr.Kind(kinds.CASControlState), CreatedAt: nostr.Now(), Tags: nostr.Tags{{"d", coordinate}, {"domain", domainName}, {"op", op}, {"schema", "bahia.intent." + domainName + ".v1"}, {"t", "bahia-intent"}, {"org", org.String()}, {"intent_id", uuid.NewString()}}, Content: string(raw)}
	signer, err := NewPrivateKeySigner(key)
	require.NoError(t, err)
	require.NoError(t, SignGoNostrEvent(t.Context(), signer, ev))
	require.True(t, ev.CheckID())
	require.True(t, ev.VerifySignature())
	return ev
}

func nonArcanaProcessor(t *testing.T, actor string, org uuid.UUID, domainName string, handler DomainHandler) (*IntentProcessor, *statusCollector) {
	t.Helper()
	statuses := &statusCollector{}
	signer, err := NewPrivateKeySigner(nostr.Generate().Hex())
	require.NoError(t, err)
	trust := NewTrustSet(nil, zap.NewNop(), WithBootstrapOwners(map[string]string{org.String(): actor}))
	processor := NewIntentProcessor(trust, nil, NewIntentStatusPublisher(statuses.publish, signer, zap.NewNop()), IntentProcessorConfig{EnabledDomains: map[string]bool{domainName: true}}, zap.NewNop())
	processor.RegisterHandler(domainName, handler)
	return processor, statuses
}

func requireSignedSanitizedDenial(t *testing.T, statuses *statusCollector, privateMetadata string) {
	t.Helper()
	require.Len(t, statuses.events, 1)
	ev := statuses.events[0]
	require.True(t, ev.CheckID())
	require.True(t, ev.VerifySignature())
	require.Equal(t, "rejected", tagValueNostr(ev.Tags, "status"))
	wire, err := json.Marshal(ev)
	require.NoError(t, err)
	require.Contains(t, string(wire), `access_denied`)
	require.NotContains(t, string(wire), privateMetadata)
}

func TestOrgOnlyRequesterDeniedByFleetHandlersOnSignedStatus(t *testing.T) {
	actorKey, actor := testNostrKeypair()
	orgA := uuid.New()
	worker := nostr.Generate().Public().Hex()
	for _, tc := range []struct {
		name, domain, op, coordinate string
		content                      map[string]any
		handler                      DomainHandler
	}{
		{"dns", "dns", "zone-create", "zone:org-b-private.example", map[string]any{"name": "org-b-private.example"}, NewDNSIntentHandler(nil, nil)},
		{"workers", "worker", "cordon", "worker:" + worker, map[string]any{"worker_pubkey": worker, "scheduling_state": "cordoned", "reason": "org-b-private"}, NewWorkerIntentHandler(nil)},
		{"loom-worker-cleanup", "worker", "cleanup", "worker:" + worker, map[string]any{"worker_pubkey": worker, "scheduling_state": "maintenance", "reason": "org-b-private-loom-job"}, NewWorkerIntentHandler(nil)},
		{"relay-settings", "relay", "policy-set", RelaySettingsDTag, map[string]any{"browser_relays": []string{"wss://org-b-private.example"}}, NewRelayPolicyIntentHandler(nil)},
		{"security-scan", "security", "scan-run", "security-scan:" + uuid.NewString(), map[string]any{"target": map[string]any{"type": "package", "package": map[string]any{"name": "org-b-private"}}}, NewSecurityScanIntentHandler(nil)},
		{"sbom", "sbom", "generate", "sbom-generate:" + uuid.NewString(), map[string]any{"subject": map[string]any{"id": "org-b-private"}}, NewSBOMIntentHandler(nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			processor, statuses := nonArcanaProcessor(t, actor, orgA, tc.domain, tc.handler)
			request := signedNonArcanaIntent(t, actorKey, tc.domain, tc.op, tc.coordinate, orgA, tc.content)
			require.ErrorContains(t, processor.ProcessRelayIntent(t.Context(), request), "insufficient permission")
			requireSignedSanitizedDenial(t, statuses, "org-b-private")
		})
	}
}

func TestSecurityAttributionCrossTenantDenialIsSigned(t *testing.T) {
	actorKey, actor := testNostrKeypair()
	orgA, orgB := uuid.New(), uuid.New()
	attribute := domain.SecurityAttribution{OrgID: orgB, SubjectType: "sbom", SubjectDigest: strings.Repeat("b", 64), Visible: true}
	raw, err := json.Marshal(attribute)
	require.NoError(t, err)
	var content map[string]any
	require.NoError(t, json.Unmarshal(raw, &content))
	publisher := &recordingSecurityAttributions{}
	processor, statuses := nonArcanaProcessor(t, actor, orgA, "security", NewSecurityScanIntentHandler(nil, publisher))
	request := signedNonArcanaIntent(t, actorKey, "security", "attribution-apply", nostradapter.SecurityAttributionDTag(attribute), orgA, content)
	require.Error(t, processor.ProcessRelayIntent(t.Context(), request))
	require.Nil(t, publisher.saved)
	requireSignedSanitizedDenial(t, statuses, attribute.SubjectDigest)
}

func TestBackupCrossTenantDenialIsSigned(t *testing.T) {
	store, err := localstore.Open(filepath.Join(t.TempDir(), "events.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	serviceKey := nostr.Generate()
	orgA, orgB := uuid.New(), uuid.New()
	repo := domain.BackupRepository{ID: uuid.New(), OrgID: orgB, Name: "org-b-private", Backend: domain.BackupBackendKopia, RepositoryURI: "file:/private", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	tags, content := nostradapter.BackupRepositoryRegistryRecord(&repo, false)
	state := configSnapshotEvent(t, serviceKey, kinds.BackupRepositoryRegistry, kinds.CPStateTopicBackupRepository, nostradapter.BackupRepositoryDTag(repo.ID), tags, content)
	_, err = store.SaveEvent(state)
	require.NoError(t, err)
	owners, err := NewLocalBackupOwnershipReader(store, serviceKey.Public().Hex())
	require.NoError(t, err)
	actorKey, actor := testNostrKeypair()
	handler := NewBackupIntentHandler(BackupIntentHandlerConfig{Registry: newFakeBackupIntentRegistry(), Publisher: &fakeBackupIntentPublisher{}, OwnerReader: owners})
	processor, statuses := nonArcanaProcessor(t, actor, orgA, "backup", handler)
	request := signedNonArcanaIntent(t, actorKey, "backup", "repository-register", nostradapter.BackupRepositoryDTag(repo.ID), orgA, map[string]any{"id": repo.ID.String(), "org_id": orgA.String(), "name": "org-b-private", "backend": "kopia", "repository_uri": "file:/private"})
	require.Error(t, processor.ProcessRelayIntent(t.Context(), request))
	requireSignedSanitizedDenial(t, statuses, "org-b-private")
}
