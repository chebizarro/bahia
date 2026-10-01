package khatru

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"fiatjaf.com/nostr/eventstore/slicestore"
	"fiatjaf.com/nostr/nip11"
)

// TestHandleNIP11ConcurrentRequestsAreRaceFree (Bahia patch, see
// BAHIA_PATCHES.md): HandleNIP11 copied *rl.Info, which shares the
// SupportedNIPs backing array, then appended the NIPs implied by the relay's
// configuration (9, 45, 77) to the copy. UseEventstore leaves spare capacity
// in that array (it appends NIP-40), so concurrent requests wrote the same
// slots of the shared array. Run with -race.
func TestHandleNIP11ConcurrentRequestsAreRaceFree(t *testing.T) {
	relay := NewRelay()
	store := &slicestore.SliceStore{}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	relay.UseEventstore(store, 500) // supported NIPs gain 40, leaving capacity
	relay.Negentropy = true
	server := httptest.NewServer(relay)
	t.Cleanup(server.Close)

	const requests = 16
	docs := make([]nip11.RelayInformationDocument, requests)
	errs := make([]error, requests)
	var wg sync.WaitGroup
	for i := range requests {
		wg.Go(func() {
			req, err := http.NewRequest(http.MethodGet, server.URL, nil)
			if err != nil {
				errs[i] = err
				return
			}
			req.Header.Set("Accept", "application/nostr+json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				errs[i] = err
				return
			}
			defer resp.Body.Close()
			errs[i] = json.NewDecoder(resp.Body).Decode(&docs[i])
		})
	}
	wg.Wait()

	for i, doc := range docs {
		if errs[i] != nil {
			t.Fatalf("request %d: %v", i, errs[i])
		}
		var nips []string
		for _, nip := range doc.SupportedNIPs {
			nips = append(nips, fmt.Sprint(nip))
		}
		for _, want := range []string{"9", "40", "77"} {
			if !slices.Contains(nips, want) {
				t.Fatalf("request %d: supported NIPs %v lack %s", i, nips, want)
			}
		}
		sorted := slices.Clone(nips)
		slices.Sort(sorted)
		if len(slices.Compact(sorted)) != len(nips) {
			t.Fatalf("request %d: duplicate supported NIPs %v", i, nips)
		}
	}
	if got := len(relay.Info.SupportedNIPs); got != 6 {
		t.Fatalf("relay.Info.SupportedNIPs = %v; requests must not change it", relay.Info.SupportedNIPs)
	}
}
