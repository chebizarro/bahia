package controlplane

import (
	"context"
	"errors"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type toolCallbackStub struct {
	decisions int
	accepted  int
}

func (*toolCallbackStub) AdmitRequest(context.Context, *Intent) error { return nil }
func (s *toolCallbackStub) Decide(context.Context, *Intent) error {
	s.decisions++
	return nil
}
func (s *toolCallbackStub) AfterAccepted(context.Context, *Intent) { s.accepted++ }

func TestToolCanonicalEffectWakeOnlyAfterCheckedAcceptedStatus(t *testing.T) {
	actorKey := nostr.Generate()
	actor := actorKey.Public().Hex()
	for _, tc := range []struct {
		name       string
		publishErr error
		wantWake   int
	}{
		{"quorum accepted", nil, 1},
		{"quorum failed", errors.New("no relay quorum"), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &toolCallbackStub{}
			status := NewIntentStatusPublisher(func(context.Context, nostr.Event) error {
				return tc.publishErr
			}, keyer.NewPlainKeySigner(nostr.Generate()), zap.NewNop())
			processor := NewIntentProcessor(NewTrustSet([]string{actor}, zap.NewNop()), openTestStore(t), status,
				IntentProcessorConfig{EnabledDomains: map[string]bool{"tool": true}}, zap.NewNop())
			processor.RegisterHandler("tool", NewToolIntentHandler(nil, stub))
			intent := signedToolApprovalIntent(t, actorKey.Hex(), uuid.New())
			err := processor.ProcessInProcess(t.Context(), intent)
			if tc.publishErr == nil {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "no relay quorum")
			}
			require.Equal(t, 1, stub.decisions)
			require.Equal(t, tc.wantWake, stub.accepted)
		})
	}
}
