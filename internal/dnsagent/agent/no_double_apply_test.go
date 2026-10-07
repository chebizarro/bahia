package agent

import (
	"context"
	"testing"

	"github.com/openagentsinc/bahia/internal/dnsagent/protocol"
)

// TestNoDoubleApplyRPCThenSubscription proves that when an agent receives both
// an RPC sync and a subscribed zone sync event for the same serial, the zone
// is applied exactly once. This is the dual-path convergence guarantee.
func TestNoDoubleApplyRPCThenSubscription(t *testing.T) {
	svc := newTestService(t, false)
	zone := testZone()
	records := testRecords("10.0.1.1")
	serial := int64(1000)
	eventID := "aaa0000000000000000000000000000000000000000000000000000000000001"

	// Path 1: RPC sync applies the zone.
	result, err := syncAgent(t, svc.agent, serial, records)
	if err != nil {
		t.Fatalf("RPC sync failed: %v", err)
	}
	if result.Status != protocol.SyncStatusOK || !result.Changed {
		t.Fatalf("RPC sync: status=%q changed=%v, want ok/true", result.Status, result.Changed)
	}
	if *svc.reloadCalls != 1 {
		t.Fatalf("reload called %d times after RPC, want 1", *svc.reloadCalls)
	}

	// Path 2: Subscription delivers the same serial + matching event ID.
	// ApplyZoneSync should detect the duplicate and skip the apply.
	err = svc.agent.ApplyZoneSync(context.Background(), zone, records, serial, eventID)
	if err != nil {
		t.Fatalf("subscription sync failed: %v", err)
	}

	// Engine reload should NOT have been called again.
	if *svc.reloadCalls != 1 {
		t.Fatalf("reload called %d times after both paths, want 1 (no double-apply)", *svc.reloadCalls)
	}
}

// TestNoDoubleApplySubscriptionThenRPC proves the reverse: subscription
// applies first, then the RPC sync is a no-op.
func TestNoDoubleApplySubscriptionThenRPC(t *testing.T) {
	svc := newTestService(t, false)
	zone := testZone()
	records := testRecords("10.0.1.2")
	serial := int64(2000)
	eventID := "bbb0000000000000000000000000000000000000000000000000000000000002"

	// Path 1: Subscription applies first.
	err := svc.agent.ApplyZoneSync(context.Background(), zone, records, serial, eventID)
	if err != nil {
		t.Fatalf("subscription sync failed: %v", err)
	}
	if *svc.reloadCalls != 1 {
		t.Fatalf("reload called %d times after subscription, want 1", *svc.reloadCalls)
	}

	// Path 2: RPC sync with same serial — should be idempotent.
	result, err := syncAgent(t, svc.agent, serial, records)
	if err != nil {
		t.Fatalf("RPC sync failed: %v", err)
	}
	if result.Status != protocol.SyncStatusOK {
		t.Fatalf("RPC sync status = %q, want ok", result.Status)
	}
	// RPC with same serial, no event envelope → idempotent no-op
	if result.Changed {
		t.Fatal("RPC sync should not report changed for same serial")
	}
	if *svc.reloadCalls != 1 {
		t.Fatalf("reload called %d times after both paths, want 1 (no double-apply)", *svc.reloadCalls)
	}
}

// TestHigherSerialSupersedes proves that a higher serial always applies
// regardless of which path delivered the lower one.
func TestHigherSerialSupersedes(t *testing.T) {
	svc := newTestService(t, false)
	zone := testZone()
	recordsV1 := testRecords("10.0.1.1")
	recordsV2 := testRecords("10.0.1.2")

	// Subscription delivers serial 1000.
	err := svc.agent.ApplyZoneSync(context.Background(), zone, recordsV1, 1000, "aaa")
	if err != nil {
		t.Fatalf("subscription v1 failed: %v", err)
	}
	if *svc.reloadCalls != 1 {
		t.Fatalf("reload calls after v1 = %d, want 1", *svc.reloadCalls)
	}

	// RPC delivers serial 2000 with different records.
	result, err := syncAgent(t, svc.agent, 2000, recordsV2)
	if err != nil {
		t.Fatalf("RPC v2 failed: %v", err)
	}
	if result.Status != protocol.SyncStatusOK || !result.Changed {
		t.Fatalf("RPC v2: status=%q changed=%v, want ok/true", result.Status, result.Changed)
	}
	if *svc.reloadCalls != 2 {
		t.Fatalf("reload calls after v2 = %d, want 2", *svc.reloadCalls)
	}

	// Subscription delivers stale serial 1000 — should be ignored.
	err = svc.agent.ApplyZoneSync(context.Background(), zone, recordsV1, 1000, "aaa")
	if err != nil {
		t.Fatalf("stale subscription failed: %v", err)
	}
	if *svc.reloadCalls != 2 {
		t.Fatalf("reload calls after stale = %d, want 2 (stale ignored)", *svc.reloadCalls)
	}
}
