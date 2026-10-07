package config

import (
	"strings"
	"testing"
)

func TestLoadNostrOutboundAdmissionFromEnv(t *testing.T) {
	t.Setenv("BAHIA_NOSTR_OUTBOUND_KILL_SWITCH_FILE", "/run/bahia/nostr-publish.stop")
	t.Setenv("BAHIA_NOSTR_OUTBOUND_AGGREGATE__RATE_PER_MINUTE", "30")
	t.Setenv("BAHIA_NOSTR_OUTBOUND_LANES__PRIORITY__BURST", "6")
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	out := cfg.Nostr.Outbound
	if out.KillSwitchFile != "/run/bahia/nostr-publish.stop" {
		t.Fatalf("kill_switch_file = %q", out.KillSwitchFile)
	}
	if out.Aggregate.RatePerMinute != 30 {
		t.Fatalf("aggregate.rate_per_minute = %d", out.Aggregate.RatePerMinute)
	}
	if out.Lanes.Priority.Burst != 6 {
		t.Fatalf("lanes.priority.burst = %d", out.Lanes.Priority.Burst)
	}
	// Zero values stay zero: the controller substitutes its bounded defaults,
	// and no configured value can disable admission.
	if out.Lanes.General.RatePerMinute != 0 || out.RelayWire.Burst != 0 {
		t.Fatal("unset lanes must stay zero for the controller defaults")
	}
}

func TestNostrOutboundValidationRejectsNegativeBudgets(t *testing.T) {
	t.Setenv("BAHIA_NOSTR_OUTBOUND_LANES__GENERAL__RATE_PER_MINUTE", "-5")
	_, err := Load("")
	if err == nil || !strings.Contains(err.Error(), "nostr.outbound.lanes.general") {
		t.Fatalf("Load() error = %v, want a nostr.outbound.lanes.general validation failure", err)
	}
}
