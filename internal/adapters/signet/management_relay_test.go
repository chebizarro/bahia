package signet

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore/slicestore"
	"fiatjaf.com/nostr/khatru"
	"fiatjaf.com/nostr/nip44"
	"fiatjaf.com/nostr/nip46"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"go.uber.org/zap"
)

var errNotARequest = errors.New("not a management request")

// fakeSignet is an in-process Signet: a NIP-46 bunker and its gift-wrapped
// ContextVM management plane, both served over a RelayPool.
type fakeSignet struct {
	key     nostr.SecretKey
	signer  nip46.StaticKeySigner
	pool    *nostrpool.RelayPool
	mu      sync.Mutex
	methods []string
}

func startFakeSignet(t *testing.T, ctx context.Context, relayURL string) *fakeSignet {
	t.Helper()
	key := nostr.Generate()
	s := &fakeSignet{key: key, signer: nip46.NewStaticKeySigner(key)}
	s.pool = nostrpool.NewRelayPool([]string{relayURL}, zap.NewNop(), nostrpool.WithPrivateKey(key.Hex()))
	t.Cleanup(s.pool.Close)
	sub, err := s.pool.SubscribeWithOptions(ctx, []nostr.Filter{
		{Kinds: []nostr.Kind{nostr.KindNostrConnect}, Tags: nostr.TagMap{"p": []string{key.Public().Hex()}}},
		{Kinds: []nostr.Kind{signetKindGiftWrap}, Tags: nostr.TagMap{"p": []string{key.Public().Hex()}}},
	}, nostrpool.SubscribeOptions{})
	if err != nil {
		t.Fatalf("fake Signet subscribe: %v", err)
	}
	select {
	case <-sub.EndOfStoredEvents:
	case <-ctx.Done():
		t.Fatal("fake Signet subscription never reached EOSE")
	}
	go func() {
		defer sub.Close()
		for ev := range sub.Events {
			var reply nostr.Event
			var err error
			switch ev.Kind {
			case nostr.KindNostrConnect:
				_, _, reply, err = s.signer.HandleRequest(ctx, *ev)
			case signetKindGiftWrap:
				reply, err = s.answerManagement(*ev)
			default:
				continue
			}
			if err == nil {
				_, _ = s.pool.Publish(ctx, reply)
			}
		}
	}()
	return s
}

func (s *fakeSignet) bunkerURI(relayURL string) string {
	return "bunker://" + s.key.Public().Hex() + "?relay=" + url.QueryEscape(relayURL)
}

func (s *fakeSignet) seenMethods() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.methods...)
}

// answerManagement unwraps a NIP-59 management request and gift-wraps a
// JSON-RPC success back to the seal's author (the provisioner).
func (s *fakeSignet) answerManagement(gift nostr.Event) (nostr.Event, error) {
	open := func(from nostr.PubKey, content string, out any) error {
		key, err := nip44.GenerateConversationKey(from, s.key)
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
	if err := open(seal.PubKey, seal.Content, &rumor); err != nil {
		return nostr.Event{}, err
	}
	var request signetJSONRPCRequest
	if err := json.Unmarshal([]byte(rumor.Content), &request); err != nil {
		return nostr.Event{}, err
	}
	if request.Method == "" {
		// One of its own replies: the provisioner is the bunker's user key.
		return nostr.Event{}, errNotARequest
	}
	s.mu.Lock()
	s.methods = append(s.methods, request.Method)
	s.mu.Unlock()

	body, _ := json.Marshal(signetJSONRPCResponse{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage(`{"ok":true}`)})
	provisioner := seal.PubKey
	replyRumor := nostr.Event{Kind: nostr.KindDirectMessage, Content: string(body), CreatedAt: nostr.Now(), PubKey: s.key.Public(), Tags: nostr.Tags{{"p", provisioner.Hex()}}}
	replyRumor.ID = replyRumor.GetID()
	sealKey, err := nip44.GenerateConversationKey(provisioner, s.key)
	if err != nil {
		return nostr.Event{}, err
	}
	sealContent, err := nip44.Encrypt(replyRumor.String(), sealKey)
	if err != nil {
		return nostr.Event{}, err
	}
	replySeal := nostr.Event{Kind: nostr.KindSeal, Content: sealContent, CreatedAt: nostr.Now(), Tags: nostr.Tags{}}
	if err := replySeal.Sign(s.key); err != nil {
		return nostr.Event{}, err
	}
	nonce := nostr.Generate()
	giftKey, err := nip44.GenerateConversationKey(provisioner, nonce)
	if err != nil {
		return nostr.Event{}, err
	}
	giftContent, err := nip44.Encrypt(replySeal.String(), giftKey)
	if err != nil {
		return nostr.Event{}, err
	}
	reply := nostr.Event{Kind: signetKindGiftWrap, Content: giftContent, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"p", provisioner.Hex()}}}
	return reply, reply.Sign(nonce)
}

// TestSignetManagementRunsOnRelayPoolWithRecipientAuth: management calls run
// on the shared RelayPool. The relay is an inbox that serves
// gift wraps only to their authenticated recipient, so the reply REQ succeeds
// only because the pool answers NIP-42 as the provisioner, through the
// bunker. The pool belongs to one bunker connection: after the connection
// manager's Close and a reconnect, management still works.
func TestSignetManagementRunsOnRelayPoolWithRecipientAuth(t *testing.T) {
	relay := khatru.NewRelay()
	store := &slicestore.SliceStore{}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	relay.UseEventstore(store, 500)
	var authMu sync.Mutex
	var refused int
	var giftReaders []string
	relay.OnRequest = func(ctx context.Context, filter nostr.Filter) (bool, string) {
		if !slices.Contains(filter.Kinds, signetKindGiftWrap) {
			return false, ""
		}
		authed, ok := khatru.GetAuthed(ctx)
		authMu.Lock()
		defer authMu.Unlock()
		if !ok || !slices.Equal(filter.Tags["p"], []string{authed.Hex()}) {
			refused++
			return true, "auth-required: gift wraps are served to their recipient"
		}
		giftReaders = append(giftReaders, authed.Hex())
		return false, ""
	}
	server := httptest.NewServer(relay)
	t.Cleanup(server.Close)
	relayURL := "ws" + strings.TrimPrefix(server.URL, "http")
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	signet := startFakeSignet(t, ctx, relayURL)
	client, err := NewClient(Config{BunkerURI: signet.bunkerURI(relayURL), ClientSecretKey: nostr.Generate().Hex(), RequireReal: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })

	for round := 1; round <= 2; round++ {
		if err := client.Connect(ctx); err != nil {
			t.Fatalf("round %d: connect: %v", round, err)
		}
		if err := client.RevokeAgent(ctx, nostr.Generate().Public().Hex()); err != nil {
			t.Fatalf("round %d: agent/revoke: %v", round, err)
		}
		if got := signet.seenMethods(); len(got) != round || got[round-1] != "agent/revoke" {
			t.Fatalf("round %d: Signet saw %v", round, got)
		}
		// What ConnectionManager does when a connection is lost.
		if err := client.Close(); err != nil {
			t.Fatal(err)
		}
	}

	authMu.Lock()
	defer authMu.Unlock()
	if refused == 0 {
		t.Fatal("the relay never refused an unauthenticated gift-wrap REQ; AUTH was not exercised")
	}
	provisioner := signet.key.Public().Hex()
	for _, reader := range giftReaders {
		if reader != provisioner {
			t.Fatalf("gift wraps read as %s, want the provisioner %s", reader, provisioner)
		}
	}
}
