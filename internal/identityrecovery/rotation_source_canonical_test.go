package identityrecovery

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	adapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

func TestCanonicalCredentialSourceEnumeratesSeededFamilies(t *testing.T) {
	ctx := context.Background()
	oldSigner := signer(t)
	oldPub := pub(t, oldSigner)
	ref := func(ch string) domain.SecretCredentialRef {
		return domain.SecretCredentialRef{Store: "signet", CredentialID: "cred_" + strings.Repeat(ch, 64), Version: 1}
	}
	makeHead := func(family int, d, content string) nostr.Event {
		kind, tags := adapter.ControlStateEnvelope(family, d, false)
		return signedRegistryHead(t, oldSigner, kind, tags, content, 100)
	}
	org := uuid.NewString()
	ock, err := controlplane.GenerateOrgContentKey(org, 1)
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := controlplane.MarshalOCKWrap(ock, oldPub.Hex())
	if err != nil {
		t.Fatal(err)
	}
	wrapCipher, err := oldSigner.Encrypt(ctx, string(wrapped), oldPub)
	if err != nil {
		t.Fatal(err)
	}
	wrap := makeHead(adapter.KindOrgKeyEnvelope, "org-key:"+org+":v1:"+oldPub.Hex(), wrapCipher)
	secretID := uuid.NewString()
	secretBody, _ := json.Marshal(map[string]any{"id": secretID, "credential_ref": ref("a")})
	secretContent, err := controlplane.EncryptConfidentialContent(ctx, ock, secretBody, controlplane.ConfidentialRecordContext{LegacyKind: adapter.KindSecretRegistry, DTag: secretID, Topic: kinds.CPStateTopicSecretRegistry}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	secret := makeHead(adapter.KindSecretRegistry, secretID, secretContent)
	marshal := func(value any) string { raw, _ := json.Marshal(value); return string(raw) }
	repo := makeHead(adapter.KindBackupRepositoryRegistry, uuid.NewString(), marshal(map[string]any{"credential_ref": ref("b")}))
	run := makeHead(adapter.KindBackupRunState, uuid.NewString(), marshal(map[string]any{"execution_snapshot": map[string]any{"credential_ref": ref("c")}}))
	runtime := makeHead(adapter.KindServiceState, uuid.NewString(), marshal(map[string]any{"desired_runtime_state": map[string]any{"secret_refs": []any{map[string]any{"credential_ref": ref("d")}}}}))
	hive := makeHead(adapter.KindHiveCIInitiationRecord, uuid.NewString(), fmt.Sprintf(`{"mirror_read_credential_ref":%q}`, secretID))
	base, relay, local, _ := registryFixture(t, adapter.KindServiceRegistry, []nostr.Event{wrap, secret, repo, run, runtime, hive})
	source := CanonicalCredentialSource{OldPubkey: oldPub, Source: relay, Local: local, Ledger: base.Ledger, OldSigner: func(context.Context) (nostr.Keyer, error) { return oldSigner, nil }}
	refs, err := source.CredentialRefs(ctx)
	if err != nil || len(refs) != 4 {
		t.Fatalf("credential inventory: %v %+v", err, refs)
	}
	families := map[string]bool{}
	for _, item := range refs {
		families[item.Credential.CredentialID] = true
		if item.ID != rotationID(item.Label, item.Credential) {
			t.Fatal("rotation ID is not deterministic")
		}
	}
	for _, ch := range []string{"a", "b", "c", "d"} {
		if !families[ref(ch).CredentialID] {
			t.Fatalf("family %s omitted", ch)
		}
	}
	second, err := source.CredentialRefs(ctx)
	if err != nil || len(second) != len(refs) {
		t.Fatalf("repeat inventory: %v", err)
	}
	for i := range refs {
		if refs[i].ID != second[i].ID {
			t.Fatal("inventory order changed")
		}
	}
}
