package loom

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	cascadia "git.sharegap.net/cascadia/cascadia-go"
	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/nostrutil"
)

type recordingCanonicalPublisher struct {
	events []nostr.Event
}

func (p *recordingCanonicalPublisher) Publish(_ context.Context, event nostr.Event) (int, error) {
	p.events = append(p.events, event)
	return 1, nil
}

type recordingLoomPool struct {
	recordingCanonicalPublisher
}

func (p *recordingLoomPool) SubscribeAllWithEOSE(context.Context, []nostr.Filter) (*nostrAdapter.MergedSubscription, error) {
	return nil, errors.New("unused in canonical projection tests")
}

func (p *recordingLoomPool) AuthenticateRelay(context.Context, string) error {
	return nil
}

type recordingCanonicalSigner struct {
	secret string
	calls  int
	err    error
}

func (s *recordingCanonicalSigner) Sign(_ context.Context, event *nostr.Event) error {
	s.calls++
	if s.err != nil {
		return s.err
	}
	return nostrutil.SignEventWithHexKey(event, s.secret)
}

func TestProjectCanonicalJobStateWithSignerPublishesStateAndAudit(t *testing.T) {
	ctx := context.Background()
	publisher := &recordingCanonicalPublisher{}
	signer := &recordingCanonicalSigner{secret: nostrutil.GeneratePrivateKeyHex()}
	exitCode := 0
	duration := 42
	success := true

	err := ProjectCanonicalJobStateWithSigner(ctx, publisher, signer, &JobStatus{
		JobID:        "job-123",
		Status:       "completed",
		Success:      &success,
		ExitCode:     &exitCode,
		Duration:     &duration,
		WorkerPubkey: strings.Repeat("a", 64),
		StdoutURL:    "https://blossom.example/stdout",
		StderrURL:    "https://blossom.example/stderr",
	}, "loom.result")
	if err != nil {
		t.Fatalf("ProjectCanonicalJobStateWithSigner() error = %v", err)
	}
	if signer.calls != 2 {
		t.Fatalf("signer calls = %d, want 2", signer.calls)
	}
	if len(publisher.events) != 2 {
		t.Fatalf("published events = %d, want 2", len(publisher.events))
	}
	state := publisher.events[0]
	if state.Kind != nostr.Kind(cascadia.CAS_CP_STATE) {
		t.Fatalf("state kind = %d, want %d", state.Kind, cascadia.CAS_CP_STATE)
	}
	if tagValueForTest(state.Tags, "d") != "loom-job:job-123" {
		t.Fatalf("state d tag = %q", tagValueForTest(state.Tags, "d"))
	}
	if state.ID.Hex() == "" || nostrutil.EventSignatureHex(&state) == "" {
		t.Fatalf("state event was not signed: id=%q sig=%q", state.ID.Hex(), nostrutil.EventSignatureHex(&state))
	}
	var stateContent map[string]any
	if err := json.Unmarshal([]byte(state.Content), &stateContent); err != nil {
		t.Fatalf("state content json: %v", err)
	}
	if stateContent["schema"] != CanonicalLoomJobSchema || stateContent["job_id"] != "job-123" || stateContent["status"] != "completed" {
		t.Fatalf("unexpected state content: %#v", stateContent)
	}

	audit := publisher.events[1]
	if cascadia.CAS_AUDIT != 4903 {
		t.Fatalf("cascadia.CAS_AUDIT = %d, want 4903", cascadia.CAS_AUDIT)
	}
	if audit.Kind != nostr.Kind(cascadia.CAS_AUDIT) {
		t.Fatalf("audit kind = %d, want %d", audit.Kind, cascadia.CAS_AUDIT)
	}
	if tagValueForTest(audit.Tags, "type") != "loom.result" {
		t.Fatalf("audit type tag = %q", tagValueForTest(audit.Tags, "type"))
	}
	if audit.ID.Hex() == "" || nostrutil.EventSignatureHex(&audit) == "" {
		t.Fatalf("audit event was not signed: id=%q sig=%q", audit.ID.Hex(), nostrutil.EventSignatureHex(&audit))
	}
}

func tagValueForTest(tags nostr.Tags, key string) string {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == key {
			return tag[1]
		}
	}
	return ""
}

func TestClientProjectCanonicalJobStateUsesConfiguredSigner(t *testing.T) {
	pool := &recordingLoomPool{}
	signer := &recordingCanonicalSigner{secret: nostrutil.GeneratePrivateKeyHex()}
	client := &Client{pool: pool}
	WithCanonicalSigner(signer)(client)

	err := client.ProjectCanonicalJobState(context.Background(), &JobStatus{JobID: "job-client", Status: "running"}, "loom.status")
	if err != nil {
		t.Fatalf("ProjectCanonicalJobState() error = %v", err)
	}
	if signer.calls != 2 {
		t.Fatalf("signer calls = %d, want 2", signer.calls)
	}
	if len(pool.events) != 2 {
		t.Fatalf("published events = %d, want 2", len(pool.events))
	}
}

func TestProjectCanonicalJobStateWithSignerRejectsMissingSigner(t *testing.T) {
	err := ProjectCanonicalJobStateWithSigner(context.Background(), &recordingCanonicalPublisher{}, nil, &JobStatus{JobID: "job-123"}, "loom.result")
	if err == nil || !strings.Contains(err.Error(), "signer is not configured") {
		t.Fatalf("error = %v, want missing signer", err)
	}
}

func TestProjectCanonicalJobStateWithSignerPropagatesSignerError(t *testing.T) {
	want := errors.New("signet unavailable")
	err := ProjectCanonicalJobStateWithSigner(context.Background(), &recordingCanonicalPublisher{}, &recordingCanonicalSigner{err: want}, &JobStatus{JobID: "job-123"}, "loom.result")
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want wrapped %v", err, want)
	}
}
