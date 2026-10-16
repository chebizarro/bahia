package controlplane

import (
	"context"
	"errors"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestIdentityContainmentDropsRelayMutationWithoutStatusAndRefusesInProcess(t *testing.T) {
	published := 0
	status := NewIntentStatusPublisher(func(context.Context, nostr.Event) error { published++; return nil }, keyer.NewPlainKeySigner(nostr.Generate()), zap.NewNop())
	active := true
	processor := NewIntentProcessor(nil, nil, status, IntentProcessorConfig{EnabledDomains: map[string]bool{"test": true}, Containment: func() (bool, string) { return active, "incident-1" }}, zap.NewNop())
	handler := &testDomainHandler{}
	processor.RegisterHandler("test", handler)
	require.NoError(t, processor.ProcessRelayIntent(t.Context(), makeIntentEvent(t, "update", "entity-1", "intent-1")))
	err := processor.ProcessInProcess(t.Context(), testIntent(t, "update", "entity-2"))
	var refusal *IncidentRefusal
	require.True(t, errors.As(err, &refusal))
	require.Equal(t, "incident-1", refusal.IncidentID)
	require.Zero(t, published, "containment cannot publish a signed rejection status")
	require.Empty(t, handler.handled)
}
