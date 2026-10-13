package main

import (
	"io"
	"testing"

	"fiatjaf.com/nostr"
)

func TestNostrLibraryLogsDoNotExposeRelayEvents(t *testing.T) {
	if nostr.InfoLogger.Writer() != io.Discard {
		t.Fatal("Nostr info logger can expose relay event bodies")
	}
	if nostr.DebugLogger.Writer() != io.Discard {
		t.Fatal("Nostr debug logger can expose relay event bodies")
	}
}
