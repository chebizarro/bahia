package soulfactory

import (
	"log/slog"
	"testing"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/domain"
)

func TestCanonicalProvisioningProjectionIgnoresDirectInteropRequest(t *testing.T) {
	signer := newFakeSigner(t)
	reactor := NewReactor(Config{Relays: []string{"wss://relay.example"}}, fakeGenerator{}, signer, slog.Default())
	capture := attachPublishCapture(reactor)
	request := &nostr.Event{ID: soulTestID("direct-5950"), Kind: nostr.Kind(domain.KindProvisioningRequest)}
	result := BuildProvisioningErrorResultEvent(request, "deploy", "runtime unavailable")
	if err := reactor.publishCanonicalProvisioningObservable(t.Context(), request, result); err != nil {
		t.Fatalf("publishCanonicalProvisioningObservable() error = %v", err)
	}
	if len(capture.events) != 0 {
		t.Fatalf("published events = %d, want zero", len(capture.events))
	}
}
