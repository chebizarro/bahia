//go:build signetinterop

package signet

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/secretstore"
	"go.uber.org/zap"
)

// liveSecretStoreFixture is written by scripts/signet_secret_store_live.py
// for one disposable signetd; it never names an existing identity or relay.
type liveSecretStoreFixture struct {
	Disposable        bool   `json:"disposable"`
	SignetCommit      string `json:"signet_commit"`
	RelayURL          string `json:"relay_url"`
	SignetPubkey      string `json:"signet_pubkey"`
	RequesterKeyHex   string `json:"requester_secret_key_hex"`
	Owner             string `json:"owner"`
	CredentialID      string `json:"credential_id"`
	ForeignCredential string `json:"foreign_credential_id"`
	OrgID             string `json:"org_id"`
	ServiceID         string `json:"service_id"`
	ValueV1File       string `json:"value_v1_file"`
	ValueV2File       string `json:"value_v2_file"`
	Signetctl         string `json:"signetctl"`
	SignetConfig      string `json:"signet_config"`
	OtherClientPubkey string `json:"other_client_pubkey"`
}

// TestLiveSignetCredentialStore proves the Signet credential store against a
// real signetd and a requester holding only the owner's exact delivery
// grant: the requester resolves the pinned version through
// credential/inspect + credential/deliver, after which Signet's reported
// version makes every resolve one credential/deliver with expected_version;
// a rotation makes the old pin fail closed and the new pin resolve (alone and
// as a paced batch); a withdrawn delivery grant and a revoked credential fail
// closed, and nothing is ever returned in their place.
func TestLiveSignetCredentialStore(t *testing.T) {
	path := os.Getenv("BAHIA_SIGNET_SECRET_STORE_CONFIG")
	if path == "" {
		t.Skip("set BAHIA_SIGNET_SECRET_STORE_CONFIG (scripts/signet_secret_store_live.py)")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fx liveSecretStoreFixture
	if err := json.Unmarshal(raw, &fx); err != nil || !fx.Disposable {
		t.Fatal("fixture must be a disposable live fixture")
	}
	signetPK, err := nostr.PubKeyFromHex(fx.SignetPubkey)
	if err != nil {
		t.Fatal("fixture signet_pubkey")
	}
	requester, err := nostr.SecretKeyFromHex(fx.RequesterKeyHex)
	if err != nil {
		t.Fatal("fixture requester key")
	}
	value1, err := os.ReadFile(fx.ValueV1File)
	if err != nil {
		t.Fatal("fixture value_v1_file")
	}
	value2, err := os.ReadFile(fx.ValueV2File)
	if err != nil {
		t.Fatal("fixture value_v2_file")
	}
	orgID, orgErr := uuid.Parse(fx.OrgID)
	serviceID, serviceErr := uuid.Parse(fx.ServiceID)
	if orgErr != nil || serviceErr != nil {
		t.Fatal("fixture org_id/service_id")
	}
	scope := secretstore.Scope{OrgID: orgID, ServiceID: serviceID}

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	store, err := OpenCredentialStore(ctx, CredentialStoreConfig{SignetPubkey: signetPK, Relays: []string{fx.RelayURL}, ClientKey: requester,
		Owner: fx.Owner, RequestTimeout: 30 * time.Second, OutboundAdmission: generousTestAdmission()}, zap.NewNop())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	signetctl := func(args ...string) {
		t.Helper()
		// signetctl output can name the fixture; it is discarded and only
		// the failing command is reported.
		if err := exec.CommandContext(ctx, fx.Signetctl, append([]string{"-c", fx.SignetConfig}, args...)...).Run(); err != nil {
			t.Fatalf("signetctl %s failed: %v", args[0], err)
		}
	}
	resolve := func(version int) (*secretstore.Value, error) {
		return store.Resolve(ctx, domain.SecretCredentialRef{Store: secretstore.BackendSignet, CredentialID: fx.CredentialID, Version: version}, scope)
	}
	expectRefused := func(step string, version int, want error) {
		t.Helper()
		value, err := resolve(version)
		if value != nil {
			value.Zeroize()
			t.Fatalf("%s: a value was released", step)
		}
		if !errors.Is(err, want) {
			t.Fatalf("%s: err = %v, want %v", step, err, want)
		}
	}

	if err := store.Health(ctx); err != nil {
		t.Fatalf("health: %v", err)
	}
	meta, err := store.Inspect(ctx, fx.CredentialID, scope)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if meta.Owner != fx.Owner || meta.Version != 1 || meta.Status != secretstore.StatusActive {
		t.Fatalf("inspect metadata: owner %q version %d status %q", meta.Owner, meta.Version, meta.Status)
	}

	// The same owner holds another tenant's credential; its label is not in
	// this scope, so it is never delivered here.
	foreign, err := store.Resolve(ctx, domain.SecretCredentialRef{Store: secretstore.BackendSignet, CredentialID: fx.ForeignCredential, Version: 1}, scope)
	if foreign != nil || !errors.Is(err, secretstore.ErrDenied) {
		t.Fatalf("another tenant's credential: err = %v", err)
	}

	value, err := resolve(1)
	if err != nil {
		t.Fatalf("resolve v1: %v", err)
	}
	if string(value.Bytes()) != string(value1) {
		t.Fatal("resolve v1 returned another value")
	}
	if lease := value.LeaseExpiresAt(); !lease.After(time.Now()) || lease.After(time.Now().Add(2*time.Minute)) {
		t.Fatalf("lease expiry %v is not Signet's short delivery lease", lease)
	}
	value.Zeroize()

	// Signet reported the delivered version and the inspect proved the
	// label: from here a resolve is a single credential/deliver.
	if !store.oneRoundTrip(fx.CredentialID, scope) {
		t.Fatal("Signet did not report the delivered version: resolves keep the inspect bracket")
	}
	value, err = resolve(1)
	if err != nil || string(value.Bytes()) != string(value1) {
		t.Fatalf("one-round-trip resolve v1: %v", err)
	}
	value.Zeroize()

	signetctl("rotate-credential", fx.CredentialID, "--file", fx.ValueV2File)
	expectRefused("v1 after rotation", 1, secretstore.ErrVersionMismatch)
	value, err = resolve(2)
	if err != nil {
		t.Fatalf("resolve v2: %v", err)
	}
	if string(value.Bytes()) != string(value2) {
		t.Fatal("resolve v2 returned another value")
	}
	value.Zeroize()
	batch, err := store.ResolveAll(ctx, []secretstore.Request{
		{Ref: domain.SecretCredentialRef{Store: secretstore.BackendSignet, CredentialID: fx.CredentialID, Version: 2}, Scope: scope},
		{Ref: domain.SecretCredentialRef{Store: secretstore.BackendSignet, CredentialID: fx.CredentialID, Version: 2}, Scope: scope},
	})
	if err != nil || len(batch) != 2 || string(batch[0].Bytes()) != string(value2) || string(batch[1].Bytes()) != string(value2) {
		t.Fatalf("paced batch resolve v2: %v", err)
	}
	secretstore.ZeroizeAll(batch)

	grant := func(client string) string {
		policy, _ := json.Marshal(map[string]any{"default": "deny", "allow_clients": []string{client}, "allow_methods": []string{"credential.deliver"}})
		return string(policy)
	}
	signetctl("set-policy", fx.Owner, grant(fx.OtherClientPubkey))
	expectRefused("delivery grant names another client", 2, secretstore.ErrDenied)
	signetctl("set-policy", fx.Owner, grant(requester.Public().Hex()))

	signetctl("revoke-credential", fx.CredentialID)
	expectRefused("revoked", 2, secretstore.ErrRevoked)
}
