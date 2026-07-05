package app

import (
	"context"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/config"
	"go.uber.org/zap"
)

func TestNewLoomCanonicalProjectionSignerDisabledByDefault(t *testing.T) {
	signer, err := newLoomCanonicalProjectionSigner(context.Background(), config.Defaults(), []string{"wss://relay.example"}, zap.NewNop())
	if err != nil {
		t.Fatalf("newLoomCanonicalProjectionSigner() error = %v", err)
	}
	if signer != nil {
		t.Fatalf("signer = %#v, want nil when projection is disabled", signer)
	}
}

func TestNewLoomCanonicalProjectionSignerRejectsEnabledProjectionWithoutSignet(t *testing.T) {
	cfg := config.Defaults()
	cfg.Loom.CanonicalProjection.Enabled = true

	signer, err := newLoomCanonicalProjectionSigner(context.Background(), cfg, []string{"wss://relay.example"}, zap.NewNop())
	if err == nil || !strings.Contains(err.Error(), "signet_bunker_uri is required") {
		t.Fatalf("newLoomCanonicalProjectionSigner() error = %v, want missing Signet", err)
	}
	if signer != nil {
		t.Fatalf("signer = %#v, want nil on configuration error", signer)
	}
}
