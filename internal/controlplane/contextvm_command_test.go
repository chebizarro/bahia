package controlplane

import (
	"context"

	"fiatjaf.com/nostr"
)

type captureNostrPublisher struct {
	events    []nostr.Event
	published int
}

func (p *captureNostrPublisher) Publish(_ context.Context, ev nostr.Event) (int, error) {
	p.events = append(p.events, ev)
	return p.published, nil
}
