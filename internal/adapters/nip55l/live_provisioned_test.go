//go:build nip55llive

package nip55l

// Provisioned live test against the nostrc reference nostr-signer-daemon,
// run by scripts/nip55l_live_test.sh: a private dbus-daemon, a
// libsecret-backed daemon whose Secret Service is this test process
// (live_secretservice_test.go), and throwaway keys generated here. Nothing
// touches the session bus, a real keyring or the Keychain.
//
//	NIP55L_LIVE_BUS_ADDRESS  private bus address (required)
//	NIP55L_LIVE_PROVISION=1  store fresh keys with StoreKey (the daemon runs
//	                         with NOSTR_SIGNER_ALLOW_KEY_MUTATIONS=1)
//	NIP55L_LIVE_GRANTS_FILE  the daemon's $XDG_CONFIG_HOME/gnostr/signer-grants.ini
//	NIP55L_LIVE_LEGACY=1     the daemon predates nip55l 0.4.0 (no ...ForApp):
//	                         only check that New refuses it

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip19"
	"fiatjaf.com/nostr/nip44"
	"github.com/godbus/dbus/v5"
)

const liveAppID = "bahia-live"

// LiveFixture is the provisioned daemon state shared by the live tests.
type LiveFixture struct {
	BusAddress string
	AppID      string
	Service    nostr.PubKey // stored first; not the active identity
	Decoy      nostr.PubKey // stored last; the daemon's active identity

	conn       *dbus.Conn
	store      *memSecretService
	grantsFile string
	principals []string
}

var (
	liveOnce sync.Once
	liveFix  *LiveFixture
	liveErr  error
)

// LiveProvision returns the shared fixture, provisioning it on first use, or
// skips the test when the provisioned live environment is not configured.
func LiveProvision(t testing.TB) *LiveFixture {
	t.Helper()
	if os.Getenv("NIP55L_LIVE_PROVISION") != "1" || os.Getenv("NIP55L_LIVE_BUS_ADDRESS") == "" {
		t.Skip("run scripts/nip55l_live_test.sh (sets NIP55L_LIVE_PROVISION and NIP55L_LIVE_BUS_ADDRESS)")
	}
	liveOnce.Do(func() { liveFix, liveErr = provisionLive() })
	if liveErr != nil {
		t.Fatalf("provision live signer: %v", liveErr)
	}
	return liveFix
}

func provisionLive() (*LiveFixture, error) {
	f := &LiveFixture{
		BusAddress: os.Getenv("NIP55L_LIVE_BUS_ADDRESS"),
		AppID:      liveAppID,
		grantsFile: os.Getenv("NIP55L_LIVE_GRANTS_FILE"),
	}
	if f.grantsFile == "" {
		return nil, errors.New("NIP55L_LIVE_GRANTS_FILE is required")
	}
	conn, err := dbus.Connect(f.BusAddress)
	if err != nil {
		return nil, fmt.Errorf("connect private bus: %w", err)
	}
	f.conn = conn
	if f.store, err = startMemSecretService(conn); err != nil {
		return nil, err
	}
	// The daemon identifies a caller by executable when the bus reports PIDs
	// and by its claimed app_id otherwise (macOS dbus-daemon); grant both.
	f.principals = []string{"claimed:" + liveAppID}
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		f.principals = append(f.principals, "exe:"+exe)
	}
	for _, slot := range []*nostr.PubKey{&f.Service, &f.Decoy} {
		sk := nostr.Generate()
		if err := storeKey(f.BusAddress, sk); err != nil {
			return nil, err
		}
		*slot = sk.Public()
	}
	if n := f.store.count(); n != 2 {
		return nil, fmt.Errorf("secret store holds %d items after two StoreKey calls, want 2", n)
	}
	if err := f.writeGrants(map[string]string{"event": "allow", "nip44_encrypt": "allow", "nip44_decrypt": "allow", "get_public_key": "allow"}); err != nil {
		return nil, err
	}
	return f, nil
}

// storeKey stores sk in the daemon as an unlabelled identity. Each call uses
// its own connection: the daemon rate-limits key mutations per sender.
func storeKey(address string, sk nostr.SecretKey) error {
	conn, err := dbus.Connect(address)
	if err != nil {
		return fmt.Errorf("connect private bus: %w", err)
	}
	defer conn.Close()
	var ok bool
	var npub string
	call := conn.Object(signerBusName, signerPath).Call(signerInterface+".StoreKey", 0, sk.Hex(), "")
	if call.Err != nil {
		return fmt.Errorf("StoreKey: %w", call.Err)
	}
	if err := call.Store(&ok, &npub); err != nil || !ok {
		return fmt.Errorf("StoreKey: ok=%v err=%v", ok, err)
	}
	if want := nip19.EncodeNpub(sk.Public()); npub != want {
		return fmt.Errorf("StoreKey returned %s, want %s", npub, want)
	}
	return nil
}

// writeGrants replaces the daemon's grants file: kind -> allow|deny for
// every test principal and both identities. The daemon reloads it when its
// size or mtime changes.
func (f *LiveFixture) writeGrants(kinds map[string]string) error {
	var b strings.Builder
	for kind, decision := range kinds {
		fmt.Fprintf(&b, "[%s]\n", kind)
		for _, p := range f.principals {
			for _, pk := range []nostr.PubKey{f.Service, f.Decoy} {
				fmt.Fprintf(&b, "%s|%s=%s\n", p, nip19.EncodeNpub(pk), decision)
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(f.grantsFile), 0o700); err != nil {
		return err
	}
	return os.WriteFile(f.grantsFile, []byte(b.String()), 0o600)
}

// SetGrants is writeGrants for the external live tests.
func (f *LiveFixture) SetGrants(t testing.TB, kinds map[string]string) {
	t.Helper()
	if err := f.writeGrants(kinds); err != nil {
		t.Fatal(err)
	}
}

func (f *LiveFixture) defaultGrants(t testing.TB) {
	f.SetGrants(t, map[string]string{"event": "allow", "nip44_encrypt": "allow", "nip44_decrypt": "allow", "get_public_key": "allow"})
}

// rawCall calls org.nostr.Signer directly, bypassing the Keyer.
func (f *LiveFixture) rawCall(ctx context.Context, method string, args ...any) (string, error) {
	var out string
	c := f.conn.Object(signerBusName, signerPath).CallWithContext(ctx, signerInterface+"."+method, 0, args...)
	if c.Err != nil {
		return "", c.Err
	}
	return out, c.Store(&out)
}

func liveCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func dbusErrName(err error) string {
	var e dbus.Error
	if errors.As(err, &e) {
		return e.Name
	}
	return ""
}

// TestLiveLegacyDaemonRejected: a pre-0.4.0 signer (no ListIdentities, no
// ...ForApp) is refused at New with errors.ErrUnsupported rather than used
// through methods that ignore app_id or read a hex selector as a key.
func TestLiveLegacyDaemonRejected(t *testing.T) {
	if os.Getenv("NIP55L_LIVE_LEGACY") != "1" {
		t.Skip("set NIP55L_LIVE_LEGACY=1 with a pre-0.4.0 daemon on NIP55L_LIVE_BUS_ADDRESS")
	}
	k, err := New(liveCtx(t), Config{ServicePubkey: nostr.Generate().Public(), AppID: liveAppID, BusAddress: os.Getenv("NIP55L_LIVE_BUS_ADDRESS")})
	if err == nil {
		k.Close()
		t.Fatal("New accepted a legacy signer")
	}
	if !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("New = %v, want errors.ErrUnsupported", err)
	}
	t.Logf("legacy daemon refused: %v", err)
}

// TestLiveSelectors pins how the reference daemon resolves identity
// selectors for a stored identity that is not the active one.
func TestLiveSelectors(t *testing.T) {
	f := LiveProvision(t)
	ctx := liveCtx(t)

	var ids []string
	if c := f.conn.Object(signerBusName, signerPath).CallWithContext(ctx, signerInterface+".ListIdentities", 0); c.Err != nil {
		t.Fatalf("ListIdentities: %v", c.Err)
	} else if err := c.Store(&ids); err != nil {
		t.Fatal(err)
	}
	t.Logf("ListIdentities = %v", ids)
	for _, pk := range []nostr.PubKey{f.Service, f.Decoy} {
		found := false
		for _, id := range ids {
			if got, ok := parseIdentity(id); ok && got == pk {
				found = true
			}
		}
		if !found {
			t.Errorf("ListIdentities lacks %s", nip19.EncodeNpub(pk))
		}
	}

	active, err := f.rawCall(ctx, "GetPublicKeyForApp", f.AppID)
	if err != nil {
		t.Fatalf("GetPublicKeyForApp: %v", err)
	}
	if pk, _ := parseIdentity(active); pk != f.Decoy {
		t.Fatalf("active identity %s, want the decoy %s", active, nip19.EncodeNpub(f.Decoy))
	}

	unsigned := fmt.Sprintf(`{"pubkey":"","created_at":%d,"kind":1,"tags":[],"content":"selector"}`, nostr.Now())
	for _, tc := range []struct{ name, selector string }{
		{"hex", f.Service.Hex()},
		{"npub", nip19.EncodeNpub(f.Service)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reply, err := f.rawCall(ctx, "SignEvent", unsigned, tc.selector, f.AppID)
			if err != nil {
				t.Fatalf("SignEvent(%s selector): %v", tc.name, err)
			}
			var evt nostr.Event
			if err := evt.UnmarshalJSON([]byte(reply)); err != nil {
				t.Fatalf("reply: %v", err)
			}
			if evt.PubKey != f.Service || !evt.VerifySignature() {
				t.Fatalf("signed by %s (valid=%v), want %s", evt.PubKey.Hex(), evt.VerifySignature(), f.Service.Hex())
			}
		})
	}
	t.Run("unknown hex", func(t *testing.T) {
		_, err := f.rawCall(ctx, "SignEvent", unsigned, nostr.Generate().Public().Hex(), f.AppID)
		if name := dbusErrName(err); name != signerErrorPrefix+"NoKeyConfigured" {
			t.Fatalf("SignEvent(unknown pubkey) = %v, want Error.NoKeyConfigured", err)
		}
	})
	t.Run("nsec refused", func(t *testing.T) {
		_, err := f.rawCall(ctx, "SignEvent", unsigned, nip19.EncodeNsec(nostr.Generate()), f.AppID)
		if name := dbusErrName(err); name != signerErrorPrefix+"InvalidInput" {
			t.Fatalf("SignEvent(nsec selector) = %v, want Error.InvalidInput", err)
		}
	})
}

// TestLiveKeyer drives every Keyer method against the daemon as the
// non-active service identity.
func TestLiveKeyer(t *testing.T) {
	f := LiveProvision(t)
	ctx := liveCtx(t)

	if _, err := New(ctx, Config{ServicePubkey: nostr.Generate().Public(), AppID: f.AppID, BusAddress: f.BusAddress}); !errors.Is(err, ErrIdentityNotFound) {
		t.Fatalf("New(unknown identity) = %v, want ErrIdentityNotFound", err)
	}
	k, err := New(ctx, Config{ServicePubkey: f.Service, AppID: f.AppID, BusAddress: f.BusAddress})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer k.Close()
	if pk, err := k.GetPublicKey(ctx); err != nil || pk != f.Service {
		t.Fatalf("GetPublicKey = %s, %v", pk.Hex(), err)
	}

	t.Run("SignEvent", func(t *testing.T) {
		tags := nostr.Tags{{"d", "bahia/nip55l-live"}, {"p", f.Decoy.Hex()}}
		evt := &nostr.Event{Kind: 30078, CreatedAt: nostr.Now() - 5, Tags: tags, Content: "bahia nip55l live test ✓"}
		want := *evt
		if err := k.SignEvent(ctx, evt); err != nil {
			t.Fatalf("SignEvent: %v", err)
		}
		if evt.PubKey != f.Service || !evt.CheckID() || !evt.VerifySignature() {
			t.Fatalf("signed event pubkey=%s id ok=%v sig ok=%v", evt.PubKey.Hex(), evt.CheckID(), evt.VerifySignature())
		}
		if evt.Kind != want.Kind || evt.CreatedAt != want.CreatedAt || evt.Content != want.Content || !evt.Tags.Eq(want.Tags) {
			t.Fatalf("signer changed fields: %+v, want %+v", evt, want)
		}

		zero := &nostr.Event{Kind: 1, Content: "zero created_at"}
		if err := k.SignEvent(ctx, zero); err != nil || zero.CreatedAt == 0 || !zero.VerifySignature() {
			t.Fatalf("SignEvent(created_at 0) = %v (created_at %d)", err, zero.CreatedAt)
		}
	})

	peerSK := nostr.Generate()
	peer := peerSK.Public()
	ck, err := nip44.GenerateConversationKey(f.Service, peerSK)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("NIP44", func(t *testing.T) {
		ct, err := k.Encrypt(ctx, "service → peer", peer)
		if err != nil {
			t.Fatalf("Encrypt: %v", err)
		}
		if pt, err := nip44.Decrypt(ct, ck); err != nil || pt != "service → peer" {
			t.Fatalf("local Decrypt = %q, %v", pt, err)
		}
		inbound, err := nip44.Encrypt("peer → service", ck)
		if err != nil {
			t.Fatal(err)
		}
		if pt, err := k.Decrypt(ctx, inbound, peer); err != nil || pt != "peer → service" {
			t.Fatalf("Decrypt = %q, %v", pt, err)
		}
		// The decoy cannot open what was sealed to the service key.
		other, _ := nip44.GenerateConversationKey(f.Decoy, peerSK)
		if _, err := nip44.Decrypt(ct, other); err == nil {
			t.Fatal("ciphertext opened with the decoy's conversation key")
		}
	})

	t.Run("NIP44B64", func(t *testing.T) {
		bin := []byte{0, 1, 2, 0xff, 0xfe, 0, 'x'}
		ct, err := k.EncryptBytes(ctx, bin, peer)
		if errors.Is(err, errors.ErrUnsupported) {
			t.Skipf("daemon has no NIP44EncryptB64ForApp: %v", err)
		}
		if err != nil {
			t.Fatalf("EncryptBytes: %v", err)
		}
		if pt, err := nip44.Decrypt(ct, ck); err != nil || pt != string(bin) {
			t.Fatalf("local Decrypt = %x, %v", pt, err)
		}
		inbound, err := nip44.Encrypt(string(bin), ck)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := k.DecryptBytes(ctx, inbound, peer); err != nil || !bytes.Equal(got, bin) {
			t.Fatalf("DecryptBytes = %x, %v", got, err)
		}
	})

	t.Run("Denied", func(t *testing.T) {
		t.Cleanup(func() { f.defaultGrants(t) })
		f.SetGrants(t, map[string]string{"event": "deny", "nip44_encrypt": "allow", "nip44_decrypt": "allow", "get_public_key": "allow"})
		evt := &nostr.Event{Kind: 1, Content: "denied"}
		err := k.SignEvent(ctx, evt)
		if !errors.Is(err, ErrApprovalDenied) {
			t.Fatalf("SignEvent with a deny grant = %v, want ErrApprovalDenied", err)
		}
		if evt.Sig != [64]byte{} {
			t.Fatal("denied SignEvent modified the event")
		}
	})

	t.Run("NoApprovalAgent", func(t *testing.T) {
		t.Cleanup(func() { f.defaultGrants(t) })
		f.SetGrants(t, map[string]string{"nip44_encrypt": "allow", "nip44_decrypt": "allow", "get_public_key": "allow"})
		start := time.Now()
		err := k.SignEvent(ctx, &nostr.Event{Kind: 1, Content: "needs a prompt"})
		if !errors.Is(err, ErrNoApprovalAgent) {
			t.Fatalf("SignEvent with no grant and no approval UI = %v, want ErrNoApprovalAgent (typed opt-in)", err)
		}
		t.Logf("no-agent failure after %s: %v", time.Since(start).Round(time.Millisecond), err)
	})

	t.Run("AppIDLabelsOnly", func(t *testing.T) {
		// A same-user caller claiming another app_id. Where the bus reports
		// PIDs the principal is the executable and the claim is a label, so
		// the grant still applies; on a bus without PIDs the claim is the
		// principal and this app has no grant.
		k2, err := New(ctx, Config{ServicePubkey: f.Service, AppID: "bahia-live-other", BusAddress: f.BusAddress})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		defer k2.Close()
		err = k2.SignEvent(ctx, &nostr.Event{Kind: 1, Content: "other app"})
		t.Logf("SignEvent as app_id bahia-live-other: %v", err)
		if err != nil && !errors.Is(err, ErrNoApprovalAgent) {
			t.Fatalf("SignEvent = %v, want success (exe principal) or ErrNoApprovalAgent (claimed principal)", err)
		}
	})
}

// TestLiveEnvKey: the daemon's env-only key (NOSTR_SIGNER_SECKEY_HEX, nothing
// stored). The reference daemon resolves a hex or npub selector naming it,
// but ListIdentities lists only stored identities, so New cannot confirm the
// key and refuses it; store the service key instead (StoreKey / Grotto).
func TestLiveEnvKey(t *testing.T) {
	skHex := os.Getenv("NIP55L_LIVE_ENV_SECKEY")
	address := os.Getenv("NIP55L_LIVE_BUS_ADDRESS")
	grants := os.Getenv("NIP55L_LIVE_GRANTS_FILE")
	if skHex == "" || address == "" || grants == "" {
		t.Skip("run scripts/nip55l_live_test.sh (sets NIP55L_LIVE_ENV_SECKEY)")
	}
	sk, err := nostr.SecretKeyFromHex(skHex)
	if err != nil {
		t.Fatal(err)
	}
	pk := sk.Public()
	f := &LiveFixture{BusAddress: address, AppID: liveAppID, Service: pk, Decoy: pk, grantsFile: grants,
		principals: []string{"claimed:" + liveAppID}}
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		f.principals = append(f.principals, "exe:"+exe)
	}
	f.defaultGrants(t)
	if f.conn, err = dbus.Connect(address); err != nil {
		t.Fatal(err)
	}
	defer f.conn.Close()
	ctx := liveCtx(t)

	unsigned := fmt.Sprintf(`{"pubkey":"","created_at":%d,"kind":1,"tags":[],"content":"env key"}`, nostr.Now())
	for _, sel := range []string{pk.Hex(), nip19.EncodeNpub(pk)} {
		reply, err := f.rawCall(ctx, "SignEvent", unsigned, sel, f.AppID)
		if err != nil {
			t.Fatalf("SignEvent(selector %s): %v", sel, err)
		}
		var evt nostr.Event
		if err := evt.UnmarshalJSON([]byte(reply)); err != nil || evt.PubKey != pk || !evt.VerifySignature() {
			t.Fatalf("SignEvent(selector %s) = %s, %v", sel, reply, err)
		}
	}

	var ids []string
	if c := f.conn.Object(signerBusName, signerPath).CallWithContext(ctx, signerInterface+".ListIdentities", 0); c.Err != nil {
		t.Fatalf("ListIdentities: %v", c.Err)
	} else if err := c.Store(&ids); err != nil {
		t.Fatal(err)
	}
	t.Logf("ListIdentities with only an env key = %v", ids)

	k, err := New(ctx, Config{ServicePubkey: pk, AppID: f.AppID, BusAddress: address})
	if err == nil {
		k.Close()
		t.Logf("New accepted the env-only key (the daemon lists it)")
		return
	}
	if !errors.Is(err, ErrIdentityNotFound) {
		t.Fatalf("New = %v, want success or ErrIdentityNotFound", err)
	}
	t.Logf("New refused the env-only key: %v", err)
}
