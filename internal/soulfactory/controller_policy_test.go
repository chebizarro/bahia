package soulfactory

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"fiatjaf.com/nostr"
)

func TestFileOpenClawControllerPolicySeedsOnceAndPersistsMutations(t *testing.T) {
	first := newFakeSigner(t).pubkey
	second := newFakeSigner(t).pubkey
	path := filepath.Join(t.TempDir(), "controllers.json")

	policy, seeded, err := NewFileOpenClawControllerPolicy(path, []string{first})
	if err != nil || !seeded {
		t.Fatalf("seed controller policy: seeded=%v err=%v", seeded, err)
	}
	reopened, seeded, err := NewFileOpenClawControllerPolicy(path, []string{second})
	if err != nil || seeded {
		t.Fatalf("reopen controller policy: seeded=%v err=%v", seeded, err)
	}
	if got := reopened.Controllers(); len(got) != 1 || got[0] != first {
		t.Fatalf("persisted controllers = %v, want original seed", got)
	}
	if err := policy.Apply(OpenClawControllerGrantMethod, second, "event-1", 100); err != nil {
		t.Fatalf("grant controller: %v", err)
	}
	if err := policy.Apply(OpenClawControllerRevokeMethod, first, "event-2", 101); err != nil {
		t.Fatalf("revoke controller: %v", err)
	}
	if err := reopened.Reload(); err != nil {
		t.Fatalf("reload controller policy: %v", err)
	}
	if got := reopened.Controllers(); len(got) != 1 || got[0] != second {
		t.Fatalf("reloaded controllers = %v, want granted controller only", got)
	}
	if err := policy.Apply(OpenClawControllerGrantMethod, first, "event-3", 101); err != nil {
		t.Fatalf("apply same-second ordered event: %v", err)
	}
	if err := policy.Apply(OpenClawControllerGrantMethod, first, "stale", 100); err == nil {
		t.Fatal("expected stale controller policy event rejection")
	}
}

func TestOpenClawSidecarAppliesSignedControllerPolicyIntentLive(t *testing.T) {
	runtime := newFakeSigner(t)
	controller := newFakeSigner(t)
	grantee := newFakeSigner(t)
	transport := &fakeOpenClawSidecarTransport{}
	sidecar := newTestOpenClawSidecar(t, runtime, controller, transport, &fakeOpenClawDriver{})

	content, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      "grant-1",
		"method":  OpenClawControllerGrantMethod,
		"params":  map[string]string{"pubkey": grantee.pubkey},
	})
	if err != nil {
		t.Fatal(err)
	}
	event := &nostr.Event{
		Kind:      nostr.Kind(25910),
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
		Tags: nostr.Tags{
			{tagPubkey, runtime.pubkey},
			{tagMethod, OpenClawControllerGrantMethod},
		},
		Content: string(content),
	}
	if err := signGoNostrEvent(t.Context(), controller, event); err != nil {
		t.Fatalf("sign controller policy intent: %v", err)
	}
	if err := sidecar.HandleControllerPolicyIntent(t.Context(), event); err != nil {
		t.Fatalf("handle controller policy intent: %v", err)
	}
	if !sidecar.isTrustedController(grantee.pubkey) {
		t.Fatal("granted controller was not activated live")
	}
	if len(transport.published) != 2 {
		t.Fatalf("published events = %d, want capability and ContextVM response", len(transport.published))
	}
	if transport.published[1].Kind != nostr.Kind(25910) {
		t.Fatalf("response kind = %d, want 25910", transport.published[1].Kind)
	}
}
