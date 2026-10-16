package identityrecovery

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	adapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

func TestNotificationRecoveryReencryptsOnlyServiceInner(t *testing.T) {
	ctx := context.Background()
	oldSigner := signer(t)
	oldPub := pub(t, oldSigner)
	ch := &domain.NotificationChannel{ID: uuid.New(), OrgID: uuid.New(), Name: "oncall", ChannelType: domain.ChannelTypeWebhook, Config: map[string]any{"url": "https://private.invalid/hook", "secret": "test-secret"}, Enabled: true}
	d := adapter.NotificationChannelDTag(ch.ID)
	ock, err := controlplane.GenerateOrgContentKey(ch.OrgID.String(), 1)
	if err != nil {
		t.Fatal(err)
	}
	_, full := adapter.NotificationChannelRegistryRecord(ch, false, true)
	content, err := controlplane.EncryptConfidentialContent(ctx, ock, []byte(`{"id":"visible"}`), controlplane.ConfidentialRecordContext{LegacyKind: adapter.KindNotificationChannelRegistry, DTag: d, Topic: kinds.CPStateTopicNotificationChannelRegistry}, []byte(full), func(ctx context.Context, plain string) (string, error) { return oldSigner.Encrypt(ctx, plain, oldPub) })
	if err != nil {
		t.Fatal(err)
	}
	kind, tags := adapter.ControlStateEnvelope(adapter.KindNotificationChannelRegistry, d, false)
	old := signedRegistryHead(t, oldSigner, kind, tags, content, 100)
	base, relay, local, newSigner := registryFixture(t, adapter.KindServiceRegistry, []nostr.Event{old})
	a := &NotificationRecoveryAdapter{OldPubkey: oldPub, Source: relay, Local: local, Ledger: base.Ledger, OldSigner: func(context.Context) (nostr.Keyer, error) { return oldSigner, nil }, NewSigner: func(context.Context) (nostr.Keyer, error) { return newSigner, nil }}
	if _, err := a.Inventory(ctx); err != nil {
		t.Fatal(err)
	}
	proof, err := a.Reassert(ctx, newSigner)
	if err != nil || len(proof) != 1 {
		t.Fatalf("reassert: %v %v", proof, err)
	}
	if relay.publishes != 1 || local.writes != 1 {
		t.Fatalf("writes=%d/%d", relay.publishes, local.writes)
	}
	newHead := relay.events[len(relay.events)-1]
	var oldEnv, newEnv controlplane.ConfidentialEnvelope
	if json.Unmarshal([]byte(old.Content), &oldEnv) != nil || json.Unmarshal([]byte(newHead.Content), &newEnv) != nil {
		t.Fatal("envelope unavailable")
	}
	oldEnv.ServiceInner, newEnv.ServiceInner = "", ""
	if !reflect.DeepEqual(oldEnv, newEnv) {
		t.Fatal("OCK-visible layer changed")
	}
	newPlain, err := newSigner.Decrypt(ctx, func() string {
		env, _ := controlplane.ParseConfidentialEnvelope(newHead.Content)
		return env.ServiceInner
	}(), pub(t, newSigner))
	if err != nil || newPlain != full {
		t.Fatal("new signer cannot decrypt the exact channel config")
	}
	if verified, err := a.Verify(ctx); err != nil || len(verified) != 1 {
		t.Fatalf("verify: %v %v", verified, err)
	}
	if _, err := a.Reassert(ctx, newSigner); err != nil || relay.publishes != 1 {
		t.Fatalf("idempotence: %v", err)
	}
}
