package loom

import (
	"testing"

	"fiatjaf.com/nostr"
)

// TestTagEnvelope_JobStatusResult_FilterRoundTrip proves that job status/result
// events carrying the canonical tag keys (tagJobDedup/tagJobEvent/tagJobPubkey)
// are matched by the exact subscription filters jobStatusFilters builds. This
// guards against producer/consumer drift of the "d"/"e"/"p" tag keys now that
// both sides reference the shared constants (bahia-s7o9).
func TestTagEnvelope_JobStatusResult_FilterRoundTrip(t *testing.T) {
	// clientSK -> clientPubkey is derived inside testClient.
	client, _, clientPK := testClient(t, nil, "0000000000000000000000000000000000000000000000000000000000000001")

	const jobID = "job-event-id-abc"
	filters := client.jobStatusFilters(jobID, "")
	if len(filters) != 2 {
		t.Fatalf("expected 2 filters (status,result), got %d", len(filters))
	}
	statusFilter, resultFilter := filters[0], filters[1]

	statusEvent := nostr.Event{
		Kind: nostr.Kind(KindJobStatus),
		Tags: nostr.Tags{
			{tagJobDedup, jobID},
			{tagJobEvent, jobID},
			{tagJobPubkey, clientPK},
			{"status", StatusRunning},
		},
	}
	if !statusFilter.Matches(statusEvent) {
		t.Fatalf("status event not matched by status filter; filter.Tags=%v event.Tags=%v", statusFilter.Tags, statusEvent.Tags)
	}

	resultEvent := nostr.Event{
		Kind: nostr.Kind(KindJobResult),
		Tags: nostr.Tags{
			{tagJobEvent, jobID},
			{tagJobPubkey, clientPK},
		},
	}
	if !resultFilter.Matches(resultEvent) {
		t.Fatalf("result event not matched by result filter; filter.Tags=%v event.Tags=%v", resultFilter.Tags, resultEvent.Tags)
	}

	// Drift guard: a status event referencing a different job id must NOT match.
	wrongEvent := nostr.Event{
		Kind: nostr.Kind(KindJobStatus),
		Tags: nostr.Tags{
			{tagJobDedup, "other-job"},
			{tagJobEvent, "other-job"},
			{tagJobPubkey, clientPK},
			{"status", StatusRunning},
		},
	}
	if statusFilter.Matches(wrongEvent) {
		t.Fatal("status filter unexpectedly matched an event for a different job id")
	}
}
