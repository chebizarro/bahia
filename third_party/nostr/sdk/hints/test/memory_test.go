package test

import (
	"testing"

	"fiatjaf.com/nostr/sdk/hints/memoryh"
)

func TestMemoryHints(t *testing.T) {
	runTestWith(t, memoryh.NewHintDB())
}
