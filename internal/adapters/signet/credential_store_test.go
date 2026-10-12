package signet

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore/slicestore"
	"fiatjaf.com/nostr/keyer"
	"fiatjaf.com/nostr/khatru"
	"fiatjaf.com/nostr/nip44"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/secretstore"
	"go.uber.org/zap"
)

// fakeVault is an in-process Signet credential plane with Signet's
// authorization order and reason codes (mgmt_protocol.c, credential_access.c):
// provisioner-only senders (-32002 otherwise), an exact per-owner delivery
// grant, ownership, then status. Like Signet (nips/nip17 GW_TIME_WINDOW) it
// backdates every reply; the fixture uses 47 hours, close to the two-day
// maximum, so a reply subscription with a narrower since never sees it.
type fakeVault struct {
	key          nostr.SecretKey
	pool         *nostrpool.RelayPool
	backdate     time.Duration
	mu           sync.Mutex
	provisioners map[nostr.PubKey]bool
	grants       map[string]nostr.PubKey
	creds        map[string]*fakeCredential
	afterDeliver func(id string)
	methods      []string
	silent       bool
}

type fakeCredential struct {
	owner   string
	label   string
	version int
	status  string
	payload []byte
}

func fakeCredentialID(label string) string {
	sum := sha256.Sum256([]byte(label))
	return "cred_" + hex.EncodeToString(sum[:])
}

func startFakeVault(t *testing.T, ctx context.Context, relayURL string) *fakeVault {
	t.Helper()
	key := nostr.Generate()
	v := &fakeVault{key: key, backdate: 47 * time.Hour, provisioners: map[nostr.PubKey]bool{}, grants: map[string]nostr.PubKey{}, creds: map[string]*fakeCredential{}}
	v.pool = nostrpool.NewRelayPool([]string{relayURL}, zap.NewNop(), nostrpool.WithAuthSigner(keyer.NewPlainKeySigner(key)), nostrpool.WithOutboundAdmission(generousTestAdmission()))
	t.Cleanup(v.pool.Close)
	sub, err := v.pool.SubscribeWithOptions(ctx, []nostr.Filter{{Kinds: []nostr.Kind{signetKindGiftWrap}, Tags: nostr.TagMap{"p": []string{key.Public().Hex()}}}}, nostrpool.SubscribeOptions{})
	if err != nil {
		t.Fatalf("fake vault subscribe: %v", err)
	}
	select {
	case <-sub.EndOfStoredEvents:
	case <-ctx.Done():
		t.Fatal("fake vault subscription never reached EOSE")
	}
	go func() {
		defer sub.Close()
		for ev := range sub.Events {
			reply, err := v.answer(*ev)
			if err == nil {
				_, _ = v.pool.Publish(ctx, reply)
			}
		}
	}()
	return v
}

func (v *fakeVault) put(owner, label string, version int, status, payload string) string {
	v.mu.Lock()
	defer v.mu.Unlock()
	id := fakeCredentialID(label)
	v.creds[id] = &fakeCredential{owner: owner, label: label, version: version, status: status, payload: []byte(payload)}
	return id
}

func (v *fakeVault) rotate(id, payload string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.creds[id].version++
	v.creds[id].payload = []byte(payload)
}

func (v *fakeVault) seen() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]string(nil), v.methods...)
}

func (v *fakeVault) answer(gift nostr.Event) (nostr.Event, error) {
	open := func(from nostr.PubKey, content string, out any) error {
		key, err := nip44.GenerateConversationKey(from, v.key)
		if err != nil {
			return err
		}
		plain, err := nip44.Decrypt(content, key)
		if err != nil {
			return err
		}
		return json.Unmarshal([]byte(plain), out)
	}
	var seal, rumor nostr.Event
	if err := open(gift.PubKey, gift.Content, &seal); err != nil {
		return nostr.Event{}, err
	}
	if !seal.CheckID() || !seal.VerifySignature() {
		return nostr.Event{}, errors.New("bad seal")
	}
	if err := open(seal.PubKey, seal.Content, &rumor); err != nil {
		return nostr.Event{}, err
	}
	var req struct {
		ID     string         `json:"id"`
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}
	if err := json.Unmarshal([]byte(rumor.Content), &req); err != nil || req.Method == "" {
		return nostr.Event{}, errNotARequest
	}
	requester := seal.PubKey
	v.mu.Lock()
	v.methods = append(v.methods, req.Method)
	silent := v.silent
	var body map[string]any
	if !v.provisioners[requester] {
		body = map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{"code": -32002, "message": "sender is not an authorized Signet provisioner"}}
	} else {
		result, code := v.dispatchLocked(req.Method, req.Params, requester)
		if code != "" {
			body = map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32000, "message": "denied", "data": code}}
		} else {
			body = map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}
		}
	}
	hook := v.afterDeliver
	v.mu.Unlock()
	if req.Method == "credential/deliver" && hook != nil {
		hook(req.Params["credential_id"].(string))
	}
	if silent {
		return nostr.Event{}, errors.New("silent")
	}
	return v.wrapReply(requester, body)
}

func (v *fakeVault) metadata(id string, c *fakeCredential) map[string]any {
	return map[string]any{"credential_id": id, "agent_id": c.owner, "secret_type": "credential", "label": c.label,
		"policy_id": nil, "provenance": "imported", "created_by": nil, "created_at": 1_700_000_000, "rotated_at": nil,
		"expires_at": nil, "revoked_at": nil, "version": c.version, "active_version": c.version, "status": c.status}
}

func (v *fakeVault) dispatchLocked(method string, params map[string]any, requester nostr.PubKey) (any, string) {
	id, _ := params["credential_id"].(string)
	owner, _ := params["agent_id"].(string)
	switch method {
	case "credential/inspect":
		c := v.creds[id]
		if c == nil {
			return nil, "credential_not_found"
		}
		return v.metadata(id, c), ""
	case "credential/list":
		list := []any{}
		for cid, c := range v.creds {
			if c.owner == owner {
				list = append(list, v.metadata(cid, c))
			}
		}
		return map[string]any{"count": len(list), "credentials": list}, ""
	case "credential/deliver":
		if grant, ok := v.grants[owner]; !ok || grant != requester {
			return nil, "no_capability"
		}
		c := v.creds[id]
		switch {
		case c == nil:
			return nil, "not_found"
		case c.owner != owner:
			return nil, "not_owner"
		case c.status == "revoked":
			return nil, "revoked"
		case c.status == "expired":
			return nil, "expired"
		}
		return map[string]any{"credential_id": id, "agent_id": owner, "payload_b64": base64.StdEncoding.EncodeToString(c.payload),
			"lease_id": strings.Repeat("e", 32), "lease_expires_at": time.Now().Add(60 * time.Second).Unix()}, ""
	}
	return nil, "unknown_method"
}

func (v *fakeVault) wrapReply(to nostr.PubKey, body map[string]any) (nostr.Event, error) {
	content, _ := json.Marshal(body)
	at := nostr.Timestamp(time.Now().Add(-v.backdate).Unix())
	rumor := nostr.Event{Kind: nostr.KindDirectMessage, Content: string(content), CreatedAt: at, PubKey: v.key.Public(), Tags: nostr.Tags{{"p", to.Hex()}}}
	rumor.ID = rumor.GetID()
	sealKey, err := nip44.GenerateConversationKey(to, v.key)
	if err != nil {
		return nostr.Event{}, err
	}
	sealContent, err := nip44.Encrypt(rumor.String(), sealKey)
	if err != nil {
		return nostr.Event{}, err
	}
	seal := nostr.Event{Kind: nostr.KindSeal, Content: sealContent, CreatedAt: at, Tags: nostr.Tags{}}
	if err := seal.Sign(v.key); err != nil {
		return nostr.Event{}, err
	}
	nonce := nostr.Generate()
	giftKey, err := nip44.GenerateConversationKey(to, nonce)
	if err != nil {
		return nostr.Event{}, err
	}
	giftContent, err := nip44.Encrypt(seal.String(), giftKey)
	if err != nil {
		return nostr.Event{}, err
	}
	gift := nostr.Event{Kind: signetKindGiftWrap, Content: giftContent, CreatedAt: at, Tags: nostr.Tags{{"p", to.Hex()}}}
	return gift, gift.Sign(nonce)
}

// recipientAuthRelay serves gift wraps only to their NIP-42-authenticated
// recipient, as a private inbox relay does. reject, when set, refuses EVENTs.
func recipientAuthRelay(t *testing.T, reject func(nostr.Event) (bool, string)) (string, *int) {
	t.Helper()
	relay := khatru.NewRelay()
	store := &slicestore.SliceStore{}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	relay.UseEventstore(store, 500)
	var mu sync.Mutex
	refused := 0
	relay.OnRequest = func(ctx context.Context, filter nostr.Filter) (bool, string) {
		if !slices.Contains(filter.Kinds, signetKindGiftWrap) {
			return false, ""
		}
		authed, ok := khatru.GetAuthed(ctx)
		if !ok || !slices.Equal(filter.Tags["p"], []string{authed.Hex()}) {
			mu.Lock()
			refused++
			mu.Unlock()
			return true, "auth-required: gift wraps are served to their recipient"
		}
		return false, ""
	}
	if reject != nil {
		relay.OnEvent = func(_ context.Context, ev nostr.Event) (bool, string) { return reject(ev) }
	}
	server := httptest.NewServer(relay)
	t.Cleanup(server.Close)
	return "ws" + strings.TrimPrefix(server.URL, "http"), &refused
}

type vaultFixture struct {
	vault  *fakeVault
	store  *CredentialStore
	client nostr.SecretKey
	ctx    context.Context
}

func newVaultFixture(t *testing.T, timeout time.Duration) vaultFixture {
	t.Helper()
	relayURL, _ := recipientAuthRelay(t, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	vault := startFakeVault(t, ctx, relayURL)
	client := nostr.Generate()
	vault.provisioners[client.Public()] = true
	vault.grants["bahia-vault"] = client.Public()
	store, err := OpenCredentialStore(ctx, CredentialStoreConfig{SignetPubkey: vault.key.Public(), Relays: []string{relayURL}, ClientKey: client,
		Owner: "bahia-vault", RequestTimeout: timeout, OutboundAdmission: generousTestAdmission()}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	return vaultFixture{vault: vault, store: store, client: client, ctx: ctx}
}

func ref(id string, version int) domain.SecretCredentialRef {
	return domain.SecretCredentialRef{Store: secretstore.BackendSignet, CredentialID: id, Version: version}
}

func TestCredentialStoreResolvesPinnedVersionFromBackdatedReplies(t *testing.T) {
	f := newVaultFixture(t, 10*time.Second)
	id := f.vault.put("bahia-vault", "db-password", 3, "active", "canary-v3")

	if _, ok, _ := f.store.LastOutcome(); ok {
		t.Fatal("LastOutcome reported a round trip before any request")
	}
	if err := f.store.Health(f.ctx); err != nil {
		t.Fatalf("Health: %v", err)
	}
	if _, ok, err := f.store.LastOutcome(); !ok || err != nil {
		t.Fatalf("LastOutcome after Health = ok %v err %v", ok, err)
	}
	meta, err := f.store.Inspect(f.ctx, id)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if meta.CredentialID != id || meta.Owner != "bahia-vault" || meta.Version != 3 || meta.Status != secretstore.StatusActive || meta.Label != "db-password" {
		t.Fatalf("Inspect metadata = %+v", meta)
	}
	before := time.Now()
	value, err := f.store.Resolve(f.ctx, ref(id, 3))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := string(value.Bytes()); got != "canary-v3" {
		t.Fatalf("resolved %q", got)
	}
	if lease := value.LeaseExpiresAt(); lease.Before(before) || lease.After(before.Add(2*time.Minute)) {
		t.Fatalf("lease expiry %v is not the short delivery lease", lease)
	}
	value.Zeroize()
	if value.Bytes() != nil {
		t.Fatal("value survived Zeroize")
	}
	if got, want := f.vault.seen(), []string{"credential/list", "credential/inspect", "credential/inspect", "credential/deliver", "credential/inspect"}; !slices.Equal(got, want) {
		t.Fatalf("Signet saw %v, want %v", got, want)
	}
}

func TestCredentialStoreFailsClosed(t *testing.T) {
	f := newVaultFixture(t, 10*time.Second)
	active := f.vault.put("bahia-vault", "active", 2, "active", "v2")
	revoked := f.vault.put("bahia-vault", "revoked", 1, "revoked", "x")
	expired := f.vault.put("bahia-vault", "expired", 1, "expired", "x")
	foreign := f.vault.put("other-vault", "foreign", 1, "active", "x")

	cases := []struct {
		name      string
		ref       domain.SecretCredentialRef
		want      error
		delivered bool
	}{
		{"stale reference", ref(active, 1), secretstore.ErrVersionMismatch, false},
		{"revoked", ref(revoked, 1), secretstore.ErrRevoked, false},
		{"expired", ref(expired, 1), secretstore.ErrExpired, false},
		{"unknown", ref(fakeCredentialID("missing"), 1), secretstore.ErrNotFound, false},
		{"another owner", ref(foreign, 1), secretstore.ErrDenied, false},
		{"malformed id", ref("cred_XYZ", 1), secretstore.ErrInvalidRef, false},
		{"other backend", domain.SecretCredentialRef{Store: "vault", CredentialID: active, Version: 2}, secretstore.ErrInvalidRef, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := len(f.vault.seen())
			value, err := f.store.Resolve(f.ctx, tc.ref)
			if !errors.Is(err, tc.want) || value != nil {
				t.Fatalf("Resolve = %v, %v; want %v", value, err, tc.want)
			}
			if slices.Contains(f.vault.seen()[before:], "credential/deliver") != tc.delivered {
				t.Fatalf("deliver called = %v, want %v (%v)", !tc.delivered, tc.delivered, f.vault.seen()[before:])
			}
		})
	}

	t.Run("rotated during delivery", func(t *testing.T) {
		id := f.vault.put("bahia-vault", "racy", 1, "active", "old")
		f.vault.mu.Lock()
		f.vault.afterDeliver = func(string) { f.vault.rotate(id, "new") }
		f.vault.mu.Unlock()
		t.Cleanup(func() { f.vault.mu.Lock(); f.vault.afterDeliver = nil; f.vault.mu.Unlock() })
		value, err := f.store.Resolve(f.ctx, ref(id, 1))
		if !errors.Is(err, secretstore.ErrVersionMismatch) || value != nil {
			t.Fatalf("Resolve across a rotation = %v, %v; want ErrVersionMismatch", value, err)
		}
	})

	t.Run("no delivery grant", func(t *testing.T) {
		f.vault.mu.Lock()
		f.vault.grants["bahia-vault"] = nostr.Generate().Public()
		f.vault.mu.Unlock()
		t.Cleanup(func() { f.vault.mu.Lock(); f.vault.grants["bahia-vault"] = f.client.Public(); f.vault.mu.Unlock() })
		value, err := f.store.Resolve(f.ctx, ref(active, 2))
		if !errors.Is(err, secretstore.ErrDenied) || value != nil || !strings.Contains(err.Error(), "no_capability") {
			t.Fatalf("Resolve without a grant = %v, %v", value, err)
		}
		if _, _, health := f.store.LastOutcome(); !errors.Is(health, secretstore.ErrDenied) {
			t.Fatalf("a denied requester must show in LastOutcome, got %v", health)
		}
	})

	t.Run("credential answers are healthy round trips", func(t *testing.T) {
		if _, err := f.store.Resolve(f.ctx, ref(revoked, 1)); !errors.Is(err, secretstore.ErrRevoked) {
			t.Fatal(err)
		}
		if _, _, health := f.store.LastOutcome(); health != nil {
			t.Fatalf("a revoked credential is not a store failure, got %v", health)
		}
	})
}

func TestCredentialStoreUnauthorizedRequesterTimesOutClosed(t *testing.T) {
	f := newVaultFixture(t, 2*time.Second)
	id := f.vault.put("bahia-vault", "db", 1, "active", "v")
	f.vault.mu.Lock()
	delete(f.vault.provisioners, f.client.Public())
	f.vault.mu.Unlock()
	if _, err := f.store.Inspect(f.ctx, id); !errors.Is(err, secretstore.ErrUnavailable) {
		// Signet answers an unauthorized sender with JSON-RPC id null, which
		// no request can be matched to: the call fails closed at its deadline.
		t.Fatalf("Inspect by a non-provisioner = %v; want ErrUnavailable", err)
	}
}

func TestCredentialStoreIgnoresRepliesNotSealedBySignet(t *testing.T) {
	f := newVaultFixture(t, 2*time.Second)
	id := f.vault.put("bahia-vault", "db", 1, "active", "real")
	// Signet stays silent; an impostor answers every request with a value of
	// its choosing, correctly correlated and gift-wrapped to the requester.
	f.vault.mu.Lock()
	f.vault.silent = true
	f.vault.mu.Unlock()
	impostor := &fakeVault{key: nostr.Generate(), backdate: time.Hour}
	relayURL := f.store.pool.URLs()[0]
	pool := nostrpool.NewRelayPool([]string{relayURL}, zap.NewNop(), nostrpool.WithAuthSigner(keyer.NewPlainKeySigner(f.vault.key)), nostrpool.WithOutboundAdmission(generousTestAdmission()))
	t.Cleanup(pool.Close)
	sub, err := pool.SubscribeWithOptions(f.ctx, []nostr.Filter{{Kinds: []nostr.Kind{signetKindGiftWrap}, Tags: nostr.TagMap{"p": []string{f.vault.key.Public().Hex()}}}}, nostrpool.SubscribeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Close)
	<-sub.EndOfStoredEvents
	go func() {
		for ev := range sub.Events {
			var seal, rumor nostr.Event
			k1, _ := nip44.GenerateConversationKey(ev.PubKey, f.vault.key)
			plain, err := nip44.Decrypt(ev.Content, k1)
			if err != nil || json.Unmarshal([]byte(plain), &seal) != nil {
				continue
			}
			k2, _ := nip44.GenerateConversationKey(seal.PubKey, f.vault.key)
			plain, err = nip44.Decrypt(seal.Content, k2)
			if err != nil || json.Unmarshal([]byte(plain), &rumor) != nil {
				continue
			}
			var req struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal([]byte(rumor.Content), &req)
			forged, err := impostor.wrapReply(seal.PubKey, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": impostor.metadata(id, &fakeCredential{owner: "bahia-vault", version: 1, status: "active"})})
			if err == nil {
				_, _ = pool.Publish(f.ctx, forged)
			}
		}
	}()
	if _, err := f.store.Inspect(f.ctx, id); !errors.Is(err, secretstore.ErrUnavailable) {
		t.Fatalf("Inspect answered only by an impostor = %v; want ErrUnavailable", err)
	}
}

func TestCredentialStoreRelayRefusalFailsBeforeTimeout(t *testing.T) {
	relayURL, _ := recipientAuthRelay(t, func(ev nostr.Event) (bool, string) {
		return ev.Kind == signetKindGiftWrap, "blocked: management traffic is not accepted here"
	})
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	store, err := OpenCredentialStore(ctx, CredentialStoreConfig{SignetPubkey: nostr.Generate().Public(), Relays: []string{relayURL}, ClientKey: nostr.Generate(),
		Owner: "bahia-vault", RequestTimeout: 20 * time.Second, OutboundAdmission: generousTestAdmission()}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	err = store.Health(ctx)
	if !errors.Is(err, secretstore.ErrUnavailable) || !strings.Contains(err.Error(), "no relay accepted") || !strings.Contains(err.Error(), "blocked:") {
		t.Fatalf("Health through a refusing relay = %v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("refusal was only detected by the deadline")
	}
}

func TestOpenCredentialStoreValidates(t *testing.T) {
	signet := nostr.Generate()
	good := CredentialStoreConfig{SignetPubkey: signet.Public(), Relays: []string{"ws://127.0.0.1:1"}, ClientKey: nostr.Generate(), Owner: "vault", RequestTimeout: time.Second}
	for name, mutate := range map[string]func(*CredentialStoreConfig){
		"no signet pubkey": func(c *CredentialStoreConfig) { c.SignetPubkey = nostr.ZeroPK },
		"no client key":    func(c *CredentialStoreConfig) { c.ClientKey = nostr.SecretKey{} },
		"no relays":        func(c *CredentialStoreConfig) { c.Relays = nil },
		"no owner":         func(c *CredentialStoreConfig) { c.Owner = " " },
		"no timeout":       func(c *CredentialStoreConfig) { c.RequestTimeout = 0 },
		"client is signet": func(c *CredentialStoreConfig) { c.ClientKey = signet },
	} {
		cfg := good
		mutate(&cfg)
		if store, err := OpenCredentialStore(t.Context(), cfg, nil); err == nil {
			store.Close()
			t.Errorf("%s: accepted", name)
		}
	}
}
