package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	cascontextvm "git.sharegap.net/cascadia/cascadia-go/contextvm"
	casnostr "git.sharegap.net/cascadia/cascadia-go/nostr"
)

type recordingMaintenanceObserver struct {
	correlations []MaintenanceRequestCorrelation
	cancelled    int
	err          error
	registered   bool
}

func (o *recordingMaintenanceObserver) RegisterMaintenanceRequest(c MaintenanceRequestCorrelation) (func(), error) {
	if o.err != nil {
		return nil, o.err
	}
	o.registered = true
	o.correlations = append(o.correlations, c)
	return func() { o.cancelled++ }, nil
}

type maintenancePublishProbe struct {
	observer  *recordingMaintenanceObserver
	published int
	calls     int
	err       error
}

func (p *maintenancePublishProbe) Publish(_ context.Context, _ nostr.Event) (int, error) {
	p.calls++
	if !p.observer.registered {
		return 0, errors.New("correlation was not registered before publish")
	}
	return p.published, p.err
}

func newMaintenanceWorkerSigner(t *testing.T) (casnostr.Signer, string) {
	t.Helper()
	signer, err := NewPrivateKeySigner(nostr.Generate().Hex())
	if err != nil {
		t.Fatalf("create worker signer: %v", err)
	}
	pubkey, err := signer.GetPublicKey(context.Background())
	if err != nil {
		t.Fatalf("worker pubkey: %v", err)
	}
	return signer, pubkey.Hex()
}

func unwrapMaintenanceCommand(t *testing.T, signer casnostr.Signer, outer nostr.Event, method string) (nostr.Event, map[string]any) {
	t.Helper()
	if outer.Kind != KindContextVMGiftWrap {
		t.Fatalf("maintenance wire kind = %d, want %d", outer.Kind, KindContextVMGiftWrap)
	}
	inner, err := cascontextvm.UnwrapNIP59(context.Background(), signer, &outer)
	if err != nil {
		t.Fatalf("unwrap maintenance command: %v", err)
	}
	if inner.Kind != KindContextVMMessage || inner.ID != inner.GetID() || inner.Sig != ([64]byte{}) {
		t.Fatalf("invalid NIP-59 rumor: %+v", inner)
	}
	assertReactorTag(t, inner.Tags, "method", method)
	assertReactorTag(t, inner.Tags, ContextVMRoutingTag, ContextVMWireVersion)
	var rpc ContextVMJSONRPCRequest
	if err := json.Unmarshal([]byte(inner.Content), &rpc); err != nil {
		t.Fatalf("decode ContextVM command: %v", err)
	}
	if rpc.JSONRPC != "2.0" || rpc.Method != method {
		t.Fatalf("unexpected ContextVM command: %#v", rpc)
	}
	var params map[string]any
	if err := json.Unmarshal(rpc.Params, &params); err != nil {
		t.Fatalf("decode ContextVM params: %v", err)
	}
	return *inner, params
}

func TestMaintenanceCommandPublisherPublishesIntentsWithCorrelation(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 2}
	signer, err := NewPrivateKeySigner(nostr.Generate().Hex())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	workerSigner, workerKey := newMaintenanceWorkerSigner(t)
	publisher := NewMaintenanceCommandPublisher(capture, signer)

	receipt, err := publisher.PublishScan(ctx, MaintenanceCommand{
		WorkerPubKey:   workerKey,
		Reason:         "periodic hygiene scan",
		IdempotencyKey: "scan-1",
		AgentID:        "swabbie",
	})
	if err != nil {
		t.Fatalf("publish scan: %v", err)
	}
	if receipt.RequestKind != KindContextVMGiftWrap || receipt.ResultKind != KindContextVMGiftWrap || receipt.StateKind != KindCASControlState || receipt.Command != MaintenanceCommandScan || receipt.DTag != "scan-1" || receipt.PublishedRelays != 2 {
		t.Fatalf("unexpected receipt: %#v", receipt)
	}
	if len(capture.events) != 1 {
		t.Fatalf("expected one event, got %d", len(capture.events))
	}
	outer := capture.events[0]
	inner, _ := unwrapMaintenanceCommand(t, workerSigner, outer, ContextVMMethodMaintenanceScan)
	if receipt.RequestEventID != inner.ID.Hex() || receipt.RequestPubkey != inner.PubKey.Hex() {
		t.Fatalf("receipt does not identify the correlated NIP-59 rumor: receipt=%+v rumor=%+v", receipt, inner)
	}
	if tagValueNostr(inner.Tags, "worker") != workerKey || tagValueNostr(inner.Tags, "p") != workerKey || tagValueNostr(inner.Tags, "command") != MaintenanceCommandScan {
		t.Fatalf("unexpected inner tags: %#v", inner.Tags)
	}
	if nonce := tagValueNostr(inner.Tags, "privacy-nonce"); nonce == "" || strings.Contains(string(mustMarshalNostrEvent(t, outer)), nonce) {
		t.Fatalf("privacy nonce missing from rumor or exposed by outer event: nonce=%q", nonce)
	}
	if outer.PubKey == inner.PubKey {
		t.Fatal("NIP-59 outer event exposed the Bahia signer pubkey")
	}
}

func mustMarshalNostrEvent(t *testing.T, event nostr.Event) []byte {
	t.Helper()
	wire, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

func TestMaintenanceCommandPublisherQuarantineCarriesPaths(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 1}
	signer, err := NewPrivateKeySigner(nostr.Generate().Hex())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	workerSigner, workerKey := newMaintenanceWorkerSigner(t)
	publisher := NewMaintenanceCommandPublisher(capture, signer)

	if _, err := publisher.PublishQuarantine(ctx, MaintenanceCommand{WorkerPubKey: workerKey}); err == nil {
		t.Fatal("quarantine without paths must fail")
	}
	secretPath := "/srv/fleet/worktrees/private-repository"
	if _, err := publisher.PublishQuarantine(ctx, MaintenanceCommand{WorkerPubKey: workerKey, Paths: []string{secretPath}, IdempotencyKey: "q-1"}); err != nil {
		t.Fatalf("publish quarantine: %v", err)
	}
	wire, err := json.Marshal(capture.events[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), secretPath) {
		t.Fatalf("absolute path appeared in plaintext request wire event: %s", wire)
	}
	_, params := unwrapMaintenanceCommand(t, workerSigner, capture.events[0], ContextVMMethodMaintenanceQuarantine)
	paths, ok := params["paths"].([]any)
	if !ok || len(paths) != 1 || paths[0] != secretPath {
		t.Fatalf("params = %#v", params)
	}
}

func TestMaintenanceCommandPublisherPurgeRequiresConfirm(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 1}
	signer, err := NewPrivateKeySigner(nostr.Generate().Hex())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	workerKey := testNostrPubKeyHexFromPrivateKey(t, nostr.Generate().Hex())
	publisher := NewMaintenanceCommandPublisher(capture, signer)

	if _, err := publisher.PublishPurge(ctx, MaintenanceCommand{WorkerPubKey: workerKey}); err == nil {
		t.Fatal("purge without confirm must fail at the control plane")
	}
	if _, err := publisher.PublishPurge(ctx, MaintenanceCommand{WorkerPubKey: workerKey, Confirm: true, IdempotencyKey: "purge-1"}); err != nil {
		t.Fatalf("publish purge: %v", err)
	}
}

func TestMaintenanceCommandPublisherRegistersExactCorrelationBeforePublish(t *testing.T) {
	ctx := context.Background()
	observer := &recordingMaintenanceObserver{}
	probe := &maintenancePublishProbe{observer: observer, published: 1}
	signer, err := NewPrivateKeySigner(nostr.Generate().Hex())
	if err != nil {
		t.Fatal(err)
	}
	_, workerKey := newMaintenanceWorkerSigner(t)
	publisher := NewMaintenanceCommandPublisher(probe, signer, observer)

	receipt, err := publisher.PublishScan(ctx, MaintenanceCommand{WorkerPubKey: workerKey, IdempotencyKey: "scan-correlation"})
	if err != nil {
		t.Fatalf("publish scan: %v", err)
	}
	if probe.calls != 1 || len(observer.correlations) != 1 || observer.cancelled != 0 {
		t.Fatalf("publish/observer state: calls=%d correlations=%+v cancelled=%d", probe.calls, observer.correlations, observer.cancelled)
	}
	correlation := observer.correlations[0]
	if correlation.Method != ContextVMMethodMaintenanceScan || correlation.WorkerPubKey != workerKey || correlation.RequestEventID != receipt.RequestEventID || correlation.RequestPubKey != receipt.RequestPubkey || correlation.DTag != receipt.DTag {
		t.Fatalf("correlation and receipt diverged: correlation=%+v receipt=%+v", correlation, receipt)
	}
}

func TestMaintenanceCommandPublisherCorrelationFailsClosedAndCancelsZeroAccepts(t *testing.T) {
	ctx := context.Background()
	signer, err := NewPrivateKeySigner(nostr.Generate().Hex())
	if err != nil {
		t.Fatal(err)
	}
	_, workerKey := newMaintenanceWorkerSigner(t)

	t.Run("registration failure prevents publish", func(t *testing.T) {
		observer := &recordingMaintenanceObserver{err: errors.New("capacity exhausted")}
		probe := &maintenancePublishProbe{observer: observer, published: 1}
		publisher := NewMaintenanceCommandPublisher(probe, signer, observer)
		if _, err := publisher.PublishScan(ctx, MaintenanceCommand{WorkerPubKey: workerKey, IdempotencyKey: "scan-rejected"}); err == nil || !strings.Contains(err.Error(), "capacity exhausted") {
			t.Fatalf("registration error = %v", err)
		}
		if probe.calls != 0 {
			t.Fatalf("untrackable request was published %d times", probe.calls)
		}
	})

	t.Run("zero relay accepts cancels", func(t *testing.T) {
		observer := &recordingMaintenanceObserver{}
		probe := &maintenancePublishProbe{observer: observer, published: 0}
		publisher := NewMaintenanceCommandPublisher(probe, signer, observer)
		if _, err := publisher.PublishPressure(ctx, MaintenanceCommand{WorkerPubKey: workerKey, IdempotencyKey: "pressure-zero"}); err == nil || !strings.Contains(err.Error(), "no relay accepted") {
			t.Fatalf("zero-accept error = %v", err)
		}
		if probe.calls != 1 || len(observer.correlations) != 1 || observer.cancelled != 1 {
			t.Fatalf("zero-accept cleanup: calls=%d correlations=%+v cancelled=%d", probe.calls, observer.correlations, observer.cancelled)
		}
	})

	t.Run("partial acceptance retains correlation", func(t *testing.T) {
		observer := &recordingMaintenanceObserver{}
		probe := &maintenancePublishProbe{observer: observer, published: 1, err: errors.New("second relay failed")}
		publisher := NewMaintenanceCommandPublisher(probe, signer, observer)
		if _, err := publisher.PublishScan(ctx, MaintenanceCommand{WorkerPubKey: workerKey, IdempotencyKey: "scan-partial"}); err == nil || !strings.Contains(err.Error(), "second relay failed") {
			t.Fatalf("partial publish error = %v", err)
		}
		if probe.calls != 1 || len(observer.correlations) != 1 || observer.cancelled != 0 {
			t.Fatalf("partial acceptance lost correlation: calls=%d correlations=%+v cancelled=%d", probe.calls, observer.correlations, observer.cancelled)
		}
	})
}
