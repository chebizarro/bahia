package signet

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/nostrout"
	"github.com/openagentsinc/bahia/internal/secretstore"
	"go.uber.org/zap"
)

// Once an Inspect has proven a credential's label (its id is a digest of the
// label, so the label cannot change) and Signet reports delivered versions,
// a resolve is one credential/deliver with expected_version.
func TestCredentialStoreResolvesInOneRoundTripOnceLabelAndVersionAreKnown(t *testing.T) {
	f := newVaultFixture(t, 10*time.Second)
	f.vault.set(func(v *fakeVault) { v.reportVersion = true })
	id := f.vault.put("bahia-vault", f.label("API_TOKEN"), 1, "active", "token-v1")

	resolve := func(version int) (string, error) {
		t.Helper()
		value, err := f.store.Resolve(f.ctx, ref(id, version), f.scope)
		if err != nil {
			return "", err
		}
		defer value.Zeroize()
		return string(value.Bytes()), nil
	}
	if got, err := resolve(1); err != nil || got != "token-v1" {
		t.Fatalf("first resolve = %q, %v", got, err)
	}
	if got := f.vault.seen(); !slices.Equal(got, []string{"credential/inspect", "credential/deliver"}) {
		t.Fatalf("first resolve: Signet saw %v", got)
	}
	if got, err := resolve(1); err != nil || got != "token-v1" {
		t.Fatalf("second resolve = %q, %v", got, err)
	}
	if got := f.vault.seen()[2:]; !slices.Equal(got, []string{"credential/deliver"}) {
		t.Fatalf("second resolve: Signet saw %v, want one deliver", got)
	}

	// A rotation is refused by Signet's expected_version in the same trip.
	f.vault.rotate(id, "token-v2")
	if _, err := resolve(1); !errors.Is(err, secretstore.ErrVersionMismatch) {
		t.Fatalf("stale pin = %v, want ErrVersionMismatch", err)
	}
	if got := f.vault.seen()[3:]; !slices.Equal(got, []string{"credential/deliver"}) {
		t.Fatalf("stale pin: Signet saw %v", got)
	}

	// The cached label is no licence across tenants: another scope takes the
	// full path and is refused before any delivery.
	other := secretstore.Scope{OrgID: uuid.New(), ServiceID: f.scope.ServiceID}
	if _, err := f.store.Resolve(f.ctx, ref(id, 2), other); !errors.Is(err, secretstore.ErrDenied) {
		t.Fatalf("other tenant = %v, want ErrDenied", err)
	}
	if got := f.vault.seen()[4:]; !slices.Equal(got, []string{"credential/inspect"}) {
		t.Fatalf("other tenant: Signet saw %v, want only an inspect", got)
	}

	// A Signet that stops reporting versions proves nothing about the pin:
	// the store discards that delivery and brackets a fresh one.
	f.vault.set(func(v *fakeVault) { v.reportVersion = false })
	if got, err := resolve(2); err != nil || got != "token-v2" {
		t.Fatalf("resolve without version reporting = %q, %v", got, err)
	}
	want := []string{"credential/deliver", "credential/inspect", "credential/deliver", "credential/inspect"}
	if got := f.vault.seen()[5:]; !slices.Equal(got, want) {
		t.Fatalf("fallback: Signet saw %v, want %v", got, want)
	}
}

// A multi-secret resolve is one declared outbound operation: its gift wraps
// are paced on the bulk lane, so it succeeds where single requests would
// exhaust the priority burst; a failure returns no value and names the
// failing request.
func TestCredentialStoreResolveAllIsPacedAndAllOrNothing(t *testing.T) {
	relayURL := recipientAuthRelay(t, relayOptions{})
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	vault := startFakeVault(t, ctx, relayURL)
	client := nostr.Generate()
	vault.provisioners[client.Public()] = true
	vault.grants["bahia-vault"] = client.Public()
	generous := nostrout.PurposeBudget{RatePerMinute: 600_000, Burst: 100_000}
	admission := nostrout.New(nostrout.Config{
		Aggregate: generous,
		PurposeBudgets: map[nostrout.Purpose]nostrout.PurposeBudget{
			// Room for the reply subscription's NIP-42 AUTH only.
			nostrout.PurposePriority: {RatePerMinute: 1, Burst: 1},
			nostrout.PurposeBulk:     {RatePerMinute: 1200, Burst: 1},
			nostrout.PurposeState:    generous, nostrout.PurposeGeneral: generous, nostrout.PurposeSigner: generous,
		},
		RelayWire: generous, RelayWirePriority: generous,
		MaxActivePublications: 1000, MaxRelayIdentities: 1000, MaxOpaqueWaiters: 1000,
	})
	store, err := OpenCredentialStore(ctx, CredentialStoreConfig{SignetPubkey: vault.key.Public(), Relays: []string{relayURL}, ClientKey: client,
		Owner: "bahia-vault", RequestTimeout: 20 * time.Second, OutboundAdmission: admission}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	scope := secretstore.Scope{OrgID: uuid.New(), ServiceID: uuid.New()}
	var reqs []secretstore.Request
	for _, name := range []string{"A", "B", "C"} {
		id := vault.put("bahia-vault", scope.LabelPrefix()+name, 1, "active", "value-"+name)
		reqs = append(reqs, secretstore.Request{Ref: ref(id, 1), Scope: scope})
	}

	values, err := store.ResolveAll(ctx, reqs)
	if err != nil {
		t.Fatalf("ResolveAll: %v", err)
	}
	for i, name := range []string{"A", "B", "C"} {
		if got := string(values[i].Bytes()); got != "value-"+name {
			t.Fatalf("value %d = %q", i, got)
		}
	}
	secretstore.ZeroizeAll(values)

	revoked := vault.put("bahia-vault", scope.LabelPrefix()+"D", 1, "revoked", "gone")
	reqs = append(reqs[:1], secretstore.Request{Ref: ref(revoked, 1), Scope: scope})
	values, err = store.ResolveAll(ctx, reqs)
	var batch *secretstore.BatchError
	if values != nil || !errors.As(err, &batch) || batch.Index != 1 || !errors.Is(err, secretstore.ErrRevoked) {
		t.Fatalf("ResolveAll with a revoked credential = %v, %v", values, err)
	}

	// Outside an operation the same budget refuses a single resolve at once.
	if _, err := store.Resolve(ctx, reqs[0].Ref, scope); !errors.Is(err, secretstore.ErrUnavailable) {
		t.Fatalf("single resolve over the priority budget = %v", err)
	}
}
