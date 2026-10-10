package config

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/nostrout"
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

func TestNostrOutboundAdmissionMapsEveryLaneAndTheKillSwitch(t *testing.T) {
	t.Setenv(nostrout.KillSwitchEnv, "/run/env.stop")
	out := NostrOutboundConfig{
		Aggregate: NostrOutboundLaneConfig{RatePerMinute: 90, Burst: 30},
		Lanes: NostrOutboundLaneConfigs{
			Priority: NostrOutboundLaneConfig{RatePerMinute: 1, Burst: 2},
			State:    NostrOutboundLaneConfig{RatePerMinute: 3, Burst: 4},
			General:  NostrOutboundLaneConfig{RatePerMinute: 5, Burst: 6},
			Bulk:     NostrOutboundLaneConfig{RatePerMinute: 7, Burst: 8},
			Signer:   NostrOutboundLaneConfig{RatePerMinute: 9, Burst: 10},
		},
		RelayWire:         NostrOutboundLaneConfig{RatePerMinute: 11, Burst: 12},
		RelayWirePriority: NostrOutboundLaneConfig{RatePerMinute: 13, Burst: 14},
		BreakerMin:        time.Second,
		BreakerMax:        time.Hour,
		DuplicateTTL:      time.Minute,
		DuplicateLimit:    15,
	}
	got := out.Admission()
	want := nostrout.Config{
		KillSwitchFile: "/run/env.stop",
		Aggregate:      nostrout.PurposeBudget{RatePerMinute: 90, Burst: 30},
		PurposeBudgets: map[nostrout.Purpose]nostrout.PurposeBudget{
			nostrout.PurposePriority: {RatePerMinute: 1, Burst: 2},
			nostrout.PurposeState:    {RatePerMinute: 3, Burst: 4},
			nostrout.PurposeGeneral:  {RatePerMinute: 5, Burst: 6},
			nostrout.PurposeBulk:     {RatePerMinute: 7, Burst: 8},
			nostrout.PurposeSigner:   {RatePerMinute: 9, Burst: 10},
		},
		RelayWire:         nostrout.PurposeBudget{RatePerMinute: 11, Burst: 12},
		RelayWirePriority: nostrout.PurposeBudget{RatePerMinute: 13, Burst: 14},
		BreakerMin:        time.Second,
		BreakerMax:        time.Hour,
		DuplicateTTL:      time.Minute,
		DuplicateLimit:    15,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Admission() = %+v\nwant %+v", got, want)
	}
	out.KillSwitchFile = " /run/yaml.stop "
	if file := out.Admission().KillSwitchFile; file != "/run/yaml.stop" {
		t.Fatalf("configured kill switch file = %q, want it to win over the environment", file)
	}
}
