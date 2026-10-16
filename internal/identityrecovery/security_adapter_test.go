package identityrecovery

import (
	"context"
	"encoding/hex"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	adapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/kinds"
)

func signedSecurityFixture(t *testing.T, oldKey nostr.Keyer, family int, d string, deleted bool, extra nostr.Tags, scope string, at int64) nostr.Event {
	t.Helper()
	key, err := controlplane.GenerateOrgContentKey(scope, 1)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := controlplane.EncryptConfidentialContent(context.Background(), key, []byte(`{"canonical":"unchanged"}`), controlplane.ConfidentialRecordContext{LegacyKind: family, DTag: d, Topic: securityRecoveryTopic(family)}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	kind, envelope := adapter.ControlStateEnvelope(family, d, deleted)
	return signedRegistryHead(t, oldKey, kind, append(append(envelope, extra...), nostr.Tag{"state_hash", hex.EncodeToString(make([]byte, 32))}), ciphertext, at)
}

func securityFixture(t *testing.T) (*SecurityRecoveryAdapter, *registryTestRelay, *registryTestLocal, nostr.Keyer, []nostr.Event) {
	t.Helper()
	oldKey := signer(t)
	org := uuid.NewString()
	digest := strings.Repeat("a", 64)
	var heads []nostr.Event
	add := func(family int, d string, deleted bool, tags nostr.Tags, scope string) {
		heads = append(heads, signedSecurityFixture(t, oldKey, family, d, deleted, tags, scope, int64(100+len(heads))))
	}
	add(adapter.KindSecurityTargetRecord, "security:target:targethash", false, nostr.Tags{{"target_key_hash", "targethash"}, {"target_type", "sbom"}}, kinds.FleetOCKScope)
	runID := uuid.NewString()
	add(adapter.KindSecurityRunRecord, "security:run:"+runID, false, nostr.Tags{{"run_id", runID}, {"target_key_hash", "targethash"}, {"status", "completed"}}, kinds.FleetOCKScope)
	add(adapter.KindSecurityFindingRecord, "security:finding:findinghash", false, nostr.Tags{{"osv_id", "OSV-1"}, {"severity", "high"}, {"target_key_hash", "targethash"}, {"run_id", runID}}, kinds.FleetOCKScope)
	add(adapter.KindSecurityFindingDetailRecord, "security:finding-detail:findinghash", false, nostr.Tags{{"finding_key_hash", "findinghash"}, {"osv_id", "OSV-1"}}, kinds.FleetOCKScope)
	add(adapter.KindSecurityScheduleRecord, "security:schedule:"+uuid.NewString(), false, nostr.Tags{{"target_key_hash", "targethash"}, {"enabled", "true"}}, kinds.FleetOCKScope)
	add(adapter.KindSecurityAttributionRecord, "security:attribution:"+org+":sbom:"+digest, false, nostr.Tags{{"org", org}, {"subject_type", "sbom"}, {"subject_digest", digest}}, org)
	base, relay, local, key := registryFixture(t, adapter.KindServiceRegistry, heads)
	return &SecurityRecoveryAdapter{OldPubkey: heads[0].PubKey, Source: relay, Local: local, Ledger: base.Ledger}, relay, local, key, heads
}

func TestSecurityRecoveryRoundTripIdempotenceAndCiphertext(t *testing.T) {
	a, relay, local, key, heads := securityFixture(t)
	ctx := context.Background()
	ids, err := a.Inventory(ctx)
	if err != nil || len(ids) != len(heads) || !slices.Contains(ids, heads[0].ID.Hex()) {
		t.Fatalf("inventory %v: %v", ids, err)
	}
	if _, err := a.Reassert(ctx, key); err != nil {
		t.Fatal(err)
	}
	if relay.publishes != len(heads) || local.writes != len(heads) {
		t.Fatalf("writes relay=%d local=%d", relay.publishes, local.writes)
	}
	for _, old := range heads {
		found := false
		for _, ev := range relay.events {
			if ev.PubKey != old.PubKey && tag(ev, "d") == tag(old, "d") && ev.Content == old.Content && reflect.DeepEqual(ev.Tags, old.Tags) {
				found = true
			}
		}
		if !found {
			t.Fatalf("ciphertext or keyed hash changed for %s", tag(old, "d"))
		}
	}
	if proof, err := a.Verify(ctx); err != nil || len(proof) != 2 {
		t.Fatalf("verify %v: %v", proof, err)
	}
	if _, err := a.Reassert(ctx, key); err != nil {
		t.Fatal(err)
	}
	if relay.publishes != len(heads) || local.writes != len(heads) {
		t.Fatal("idempotent rerun wrote heads")
	}
	bad := signedRegistryHead(t, key, int(heads[0].Kind), heads[0].Tags, `{"changed":true}`, time.Now().Unix()+100)
	relay.events = append(relay.events, bad)
	local.events = append(local.events, bad)
	if _, err := a.Verify(ctx); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("divergent verify: %v", err)
	}
	if _, err := a.Reassert(ctx, key); err == nil || relay.publishes != len(heads) {
		t.Fatalf("divergent reassert: %v", err)
	}
}

func TestSecurityRecoveryRejectsServiceInnerAndEOSEFailure(t *testing.T) {
	a, relay, _, key, heads := securityFixture(t)
	bad := heads[0]
	bad.Content = strings.TrimSuffix(bad.Content, "}") + `,"service_inner":"old-key-secret"}`
	if _, _, err := validateSecurityRecoveryHead(bad); err == nil {
		t.Fatal("service_inner accepted")
	}
	relay.readErr = errors.New("EOSE incomplete")
	if _, err := a.Inventory(context.Background()); err == nil {
		t.Fatal("EOSE failure accepted as absence")
	}
	if _, err := a.Reassert(context.Background(), key); err == nil || relay.publishes != 0 {
		t.Fatalf("EOSE failure published: %v", err)
	}
}
