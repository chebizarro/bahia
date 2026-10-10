package nip55l

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"runtime"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip19"
	"fiatjaf.com/nostr/nip44"
	"github.com/godbus/dbus/v5"
)

// setup starts a private bus, exports f on it and returns a connected Keyer.
func setup(t *testing.T, f *fakeSigner) *Keyer {
	t.Helper()
	addr := startPrivateBus(t)
	f.export(t, addr)
	k, err := New(context.Background(), Config{ServicePubkey: f.pk, BusAddress: addr})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = k.Close() })
	return k
}

func testEvent() *nostr.Event {
	return &nostr.Event{
		Kind:      30078,
		CreatedAt: 1_760_000_000,
		Tags:      nostr.Tags{{"d", "bahia/test"}, {"p", nostr.Generate().Public().Hex()}},
		Content:   `{"hello":"world <&>"}`,
	}
}

func TestHappyPath(t *testing.T) {
	f := newFakeSigner()
	k := setup(t, f)
	ctx := context.Background()

	pk, err := k.GetPublicKey(ctx)
	if err != nil || pk != f.pk {
		t.Fatalf("GetPublicKey = %s, %v; want %s", pk.Hex(), err, f.pk.Hex())
	}

	evt := testEvent()
	if err := k.SignEvent(ctx, evt); err != nil {
		t.Fatalf("SignEvent: %v", err)
	}
	if evt.PubKey != f.pk || !evt.CheckID() || !evt.VerifySignature() {
		t.Fatalf("SignEvent produced an invalid event: %s", evt)
	}

	unset := testEvent()
	unset.CreatedAt = 0
	before := nostr.Now()
	if err := k.SignEvent(ctx, unset); err != nil {
		t.Fatalf("SignEvent with zero created_at: %v", err)
	}
	if unset.CreatedAt < before || !unset.VerifySignature() {
		t.Fatalf("created_at %d not set locally before signing", unset.CreatedAt)
	}
	f.mu.Lock()
	lastInput := f.received[len(f.received)-1]
	f.mu.Unlock()
	var sent struct {
		CreatedAt int64 `json:"created_at"`
	}
	if err := json.Unmarshal([]byte(lastInput), &sent); err != nil || sent.CreatedAt == 0 {
		t.Fatalf("eventJson sent with created_at %d (err %v); want it set", sent.CreatedAt, err)
	}

	peerSK := nostr.Generate()
	peer := peerSK.Public()
	ck, err := nip44.GenerateConversationKey(f.pk, peerSK)
	if err != nil {
		t.Fatal(err)
	}

	ct, err := k.Encrypt(ctx, "service secret", peer)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if pt, err := nip44.Decrypt(ct, ck); err != nil || pt != "service secret" {
		t.Fatalf("peer cannot open Encrypt output: %q, %v", pt, err)
	}
	inbound, _ := nip44.Encrypt("from peer", ck)
	if pt, err := k.Decrypt(ctx, inbound, peer); err != nil || pt != "from peer" {
		t.Fatalf("Decrypt = %q, %v", pt, err)
	}

	binary := []byte{0x00, 0xff, 0xfe, 0x00, 'o', 'c', 'k', 0x80}
	ctb, err := k.EncryptBytes(ctx, binary, peer)
	if err != nil {
		t.Fatalf("EncryptBytes: %v", err)
	}
	if pt, err := nip44.Decrypt(ctb, ck); err != nil || pt != string(binary) {
		t.Fatalf("peer cannot open EncryptBytes output: %x, %v", pt, err)
	}
	inboundBin, _ := nip44.Encrypt(string(binary), ck)
	if pt, err := k.DecryptBytes(ctx, inboundBin, peer); err != nil || !bytes.Equal(pt, binary) {
		t.Fatalf("DecryptBytes = %x, %v", pt, err)
	}

	calls, optIns := f.snapshot()
	gated := 0
	for _, c := range calls {
		if c.Method == "ListIdentities" {
			continue
		}
		gated++
		if c.Identity != f.pk.Hex() {
			t.Errorf("%s sent identity %q; want the 64-hex service pubkey", c.Method, c.Identity)
		}
		if c.AppID != DefaultAppID {
			t.Errorf("%s sent app_id %q; want %q", c.Method, c.AppID, DefaultAppID)
		}
	}
	if gated != 6 {
		t.Errorf("signer saw %d gated calls, want 6: %+v", gated, calls)
	}
	if optIns < gated {
		t.Errorf("EnableTypedApprovalErrors sent %d times for %d gated calls", optIns, gated)
	}
}

func TestNewAcceptsHexIdentityAndCustomAppID(t *testing.T) {
	f := newFakeSigner()
	f.identities = []string{nip19.EncodeNpub(nostr.Generate().Public()), f.pk.Hex()}
	addr := startPrivateBus(t)
	f.export(t, addr)
	k, err := New(context.Background(), Config{ServicePubkey: f.pk, BusAddress: addr, AppID: "bahia-test"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer k.Close()
	if err := k.SignEvent(context.Background(), testEvent()); err != nil {
		t.Fatal(err)
	}
	calls, _ := f.snapshot()
	if last := calls[len(calls)-1]; last.AppID != "bahia-test" {
		t.Fatalf("app_id = %q, want bahia-test", last.AppID)
	}
}

func TestNewIdentityMismatch(t *testing.T) {
	f := newFakeSigner()
	f.identities = []string{nip19.EncodeNpub(nostr.Generate().Public())}
	addr := startPrivateBus(t)
	f.export(t, addr)
	_, err := New(context.Background(), Config{ServicePubkey: f.pk, BusAddress: addr})
	if !errors.Is(err, ErrIdentityNotFound) {
		t.Fatalf("New = %v; want ErrIdentityNotFound", err)
	}
}

func TestNewFallsBackToGetPublicKeyForApp(t *testing.T) {
	t.Run("match", func(t *testing.T) {
		f := newFakeSigner()
		f.noListIdentities = true
		setup(t, f)
		calls, _ := f.snapshot()
		if len(calls) != 1 || calls[0].Method != "GetPublicKeyForApp" || calls[0].AppID != DefaultAppID {
			t.Fatalf("calls = %+v; want one GetPublicKeyForApp(bahia)", calls)
		}
	})
	t.Run("mismatch", func(t *testing.T) {
		f := newFakeSigner()
		f.noListIdentities = true
		f.active = nostr.Generate().Public()
		addr := startPrivateBus(t)
		f.export(t, addr)
		_, err := New(context.Background(), Config{ServicePubkey: f.pk, BusAddress: addr})
		if !errors.Is(err, ErrIdentityNotFound) {
			t.Fatalf("New = %v; want ErrIdentityNotFound", err)
		}
	})
}

func TestNewNoSigner(t *testing.T) {
	addr := startPrivateBus(t)
	_, err := New(context.Background(), Config{ServicePubkey: nostr.Generate().Public(), BusAddress: addr})
	if !errors.Is(err, ErrSignerUnavailable) {
		t.Fatalf("New = %v; want ErrSignerUnavailable", err)
	}
}

func TestNewConfigValidation(t *testing.T) {
	ctx := context.Background()
	pk := nostr.Generate().Public()
	for name, cfg := range map[string]Config{
		"zero pubkey":   {},
		"short timeout": {ServicePubkey: pk, CallTimeout: 30 * time.Second},
		"bad app id":    {ServicePubkey: pk, AppID: "a\x00b"},
	} {
		if _, err := New(ctx, cfg); err == nil {
			t.Errorf("%s: New succeeded", name)
		}
	}
}

func TestSignEventRejectsWrongPubkey(t *testing.T) {
	f := newFakeSigner()
	f.signWith = nostr.Generate()
	k := setup(t, f)
	evt := testEvent()
	orig := *evt
	err := k.SignEvent(context.Background(), evt)
	if !errors.Is(err, ErrBadReply) {
		t.Fatalf("SignEvent = %v; want ErrBadReply", err)
	}
	if evt.Sig != orig.Sig || evt.PubKey != orig.PubKey || evt.ID != orig.ID {
		t.Fatal("event was modified despite the rejected reply")
	}
}

func TestSignEventRejectsTamperedReply(t *testing.T) {
	cases := map[string]func(f *fakeSigner) func(*nostr.Event){
		"content re-signed": func(f *fakeSigner) func(*nostr.Event) {
			return func(e *nostr.Event) { e.Content += " (edited)"; _ = e.Sign(f.sk) }
		},
		"tags re-signed": func(f *fakeSigner) func(*nostr.Event) {
			return func(e *nostr.Event) { e.Tags = append(e.Tags, nostr.Tag{"t", "x"}); _ = e.Sign(f.sk) }
		},
		"kind re-signed": func(f *fakeSigner) func(*nostr.Event) {
			return func(e *nostr.Event) { e.Kind = 1; _ = e.Sign(f.sk) }
		},
		"created_at re-signed": func(f *fakeSigner) func(*nostr.Event) {
			return func(e *nostr.Event) { e.CreatedAt++; _ = e.Sign(f.sk) }
		},
		"content without re-sign": func(*fakeSigner) func(*nostr.Event) {
			return func(e *nostr.Event) { e.Content = "swapped" }
		},
		"corrupted sig": func(*fakeSigner) func(*nostr.Event) {
			return func(e *nostr.Event) { e.Sig[5] ^= 1 }
		},
		"corrupted id": func(*fakeSigner) func(*nostr.Event) {
			return func(e *nostr.Event) { e.ID[0] ^= 1 }
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFakeSigner()
			f.tamper = mk(f)
			k := setup(t, f)
			if err := k.SignEvent(context.Background(), testEvent()); !errors.Is(err, ErrBadReply) {
				t.Fatalf("SignEvent = %v; want ErrBadReply", err)
			}
		})
	}
	t.Run("not json", func(t *testing.T) {
		f := newFakeSigner()
		f.rawReply = func(string) string { return "nope" }
		k := setup(t, f)
		if err := k.SignEvent(context.Background(), testEvent()); !errors.Is(err, ErrBadReply) {
			t.Fatalf("SignEvent = %v; want ErrBadReply", err)
		}
	})
	t.Run("bare signature (pre-0.2.0)", func(t *testing.T) {
		f := newFakeSigner()
		f.rawReply = func(signed string) string {
			var e nostr.Event
			_ = json.Unmarshal([]byte(signed), &e)
			return hex.EncodeToString(e.Sig[:])
		}
		k := setup(t, f)
		if err := k.SignEvent(context.Background(), testEvent()); !errors.Is(err, errors.ErrUnsupported) {
			t.Fatalf("SignEvent = %v; want errors.ErrUnsupported", err)
		}
	})
}

func TestB64UnknownMethodIsUnsupported(t *testing.T) {
	f := newFakeSigner()
	f.noB64 = true
	k := setup(t, f)
	peerSK := nostr.Generate()
	peer := peerSK.Public()
	if _, err := k.EncryptBytes(context.Background(), []byte{0, 1, 2}, peer); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("EncryptBytes = %v; want errors.ErrUnsupported", err)
	}
	ck, _ := nip44.GenerateConversationKey(f.pk, peerSK)
	ct, _ := nip44.Encrypt("x", ck)
	if _, err := k.DecryptBytes(context.Background(), ct, peer); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("DecryptBytes = %v; want errors.ErrUnsupported", err)
	}
	// Text NIP-44 still works on the same signer.
	if _, err := k.Encrypt(context.Background(), "text", peer); err != nil {
		t.Fatalf("Encrypt on a pre-B64 signer: %v", err)
	}
}

func TestSignerErrorsAreTyped(t *testing.T) {
	cases := map[string]error{
		signerErrorPrefix + "ApprovalDenied":   ErrApprovalDenied,
		signerErrorPrefix + "ApprovalTimedOut": ErrApprovalTimedOut,
		signerErrorPrefix + "NoApprovalAgent":  ErrNoApprovalAgent,
		signerErrorPrefix + "IdentityChanged":  ErrIdentityChanged,
		signerErrorPrefix + "RateLimited":      ErrRateLimited,
		signerErrorPrefix + "NoKeyConfigured":  ErrNoKeyConfigured,
		signerErrorPrefix + "InvalidInput":     ErrInvalidInput,
		signerErrorPrefix + "PermissionDenied": ErrPermissionDenied,
		signerErrorPrefix + "NotFound":         ErrNotFound,
		signerErrorPrefix + "InvalidConfig":    ErrInvalidConfig,
		signerErrorPrefix + "Internal":         ErrInternal,
		signerErrorPrefix + "FromTheFuture":    ErrUnknownSignerError,
		dbusErrNoReply:                         ErrTimeout,
		"com.example.Weird":                    ErrUnknownSignerError,
	}
	f := newFakeSigner()
	f.failWith = map[string]*dbus.Error{}
	k := setup(t, f)
	peer := nostr.Generate().Public()
	for name, want := range cases {
		f.mu.Lock()
		f.failWith["SignEvent"] = dbus.NewError(name, []any{"informational"})
		f.failWith["NIP44EncryptForApp"] = dbus.NewError(name, nil)
		f.mu.Unlock()

		err := k.SignEvent(context.Background(), testEvent())
		var se *SignerError
		if !errors.Is(err, want) || !errors.As(err, &se) || se.Name != name || se.Message != "informational" {
			t.Errorf("SignEvent with %s = %v; want %v carrying name and message", name, err, want)
		}
		if _, err := k.Encrypt(context.Background(), "x", peer); !errors.Is(err, want) {
			t.Errorf("Encrypt with %s = %v; want %v", name, err, want)
		}
	}
}

func TestContextCancel(t *testing.T) {
	f := newFakeSigner()
	f.block = make(chan struct{})
	f.entered = make(chan string, 4)
	k := setup(t, f)

	runtime.GC()
	baseline := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- k.SignEvent(ctx, testEvent()) }()
	<-f.entered
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("SignEvent = %v; want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SignEvent did not return after cancel")
	}
	// Let the fake answer the abandoned request; the reply must be dropped.
	close(f.block)

	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > baseline {
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<16)
			t.Fatalf("goroutines: %d > baseline %d\n%s", runtime.NumGoroutine(), baseline, buf[:runtime.Stack(buf, true)])
		}
		time.Sleep(10 * time.Millisecond)
	}

	if _, err := k.Encrypt(ctx, "x", nostr.Generate().Public()); !errors.Is(err, context.Canceled) {
		t.Fatalf("call with a cancelled ctx = %v; want context.Canceled", err)
	}
}

func TestCallTimeout(t *testing.T) {
	f := newFakeSigner()
	f.block = make(chan struct{})
	k := setup(t, f)
	k.callTimeout = 100 * time.Millisecond
	err := k.SignEvent(context.Background(), testEvent())
	if !errors.Is(err, ErrTimeout) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("SignEvent = %v; want ErrTimeout wrapping DeadlineExceeded", err)
	}
	close(f.block)
}

func TestClose(t *testing.T) {
	f := newFakeSigner()
	f.block = make(chan struct{})
	f.entered = make(chan string, 1)
	k := setup(t, f)

	done := make(chan error, 1)
	go func() { done <- k.SignEvent(context.Background(), testEvent()) }()
	<-f.entered
	if err := k.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrSignerUnavailable) {
			t.Fatalf("in-flight SignEvent after Close = %v; want ErrSignerUnavailable", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight call did not return after Close")
	}
	close(f.block)
	if err := k.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if err := k.SignEvent(context.Background(), testEvent()); !errors.Is(err, ErrClosed) {
		t.Fatalf("SignEvent after Close = %v; want ErrClosed", err)
	}
	if _, err := k.GetPublicKey(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("GetPublicKey after Close = %v; want ErrClosed", err)
	}
}

func TestLocalValidation(t *testing.T) {
	f := newFakeSigner()
	k := setup(t, f)
	ctx := context.Background()
	peer := nostr.Generate().Public()

	if _, err := k.Encrypt(ctx, "bad\xffutf8", peer); err == nil {
		t.Error("Encrypt accepted invalid UTF-8")
	}
	if _, err := k.Encrypt(ctx, "", peer); err == nil {
		t.Error("Encrypt accepted an empty plaintext")
	}
	if _, err := k.EncryptBytes(ctx, make([]byte, nip44MaxPlaintext+1), peer); err == nil {
		t.Error("EncryptBytes accepted an oversized plaintext")
	}
	if _, err := k.Encrypt(ctx, "x", nostr.ZeroPK); err == nil {
		t.Error("Encrypt accepted a zero recipient")
	}
	if _, err := k.Decrypt(ctx, "not-a-payload", peer); err == nil {
		t.Error("Decrypt accepted a malformed payload")
	}
	bad := testEvent()
	bad.Content = "nul\x00byte"
	if err := k.SignEvent(ctx, bad); err == nil {
		t.Error("SignEvent accepted content D-Bus cannot carry")
	}
	if err := k.SignEvent(ctx, nil); err == nil {
		t.Error("SignEvent accepted nil")
	}
	if _, err := k.Nip04Encrypt(ctx, "x", peer); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("Nip04Encrypt = %v; want errors.ErrUnsupported", err)
	}
	if _, err := k.Nip04Decrypt(ctx, "x", peer); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("Nip04Decrypt = %v; want errors.ErrUnsupported", err)
	}
	calls, _ := f.snapshot()
	for _, c := range calls {
		if c.Method != "ListIdentities" {
			t.Errorf("locally invalid input reached the signer: %s", c.Method)
		}
	}
}
