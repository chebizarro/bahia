package loom

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"go.uber.org/zap"
)

type fakeLoomRelayPool struct {
	sub       *nostrAdapter.MergedSubscription
	filters   []nostr.Filter
	authCalls int
	authErr   error
}

func (f *fakeLoomRelayPool) Publish(context.Context, nostr.Event) (int, error) { return 1, nil }

func (f *fakeLoomRelayPool) SubscribeAllWithEOSE(_ context.Context, filters []nostr.Filter) (*nostrAdapter.MergedSubscription, error) {
	f.filters = append([]nostr.Filter(nil), filters...)
	if f.sub == nil {
		return nil, errors.New("missing subscription")
	}
	return f.sub, nil
}

func (f *fakeLoomRelayPool) AuthenticateRelay(context.Context, string) error {
	f.authCalls++
	return f.authErr
}

func testClient(t *testing.T, sub *nostrAdapter.MergedSubscription, clientSK string) (*Client, *fakeLoomRelayPool, string) {
	t.Helper()
	clientPK, err := nostr.GetPublicKey(clientSK)
	if err != nil {
		t.Fatalf("derive client pubkey: %v", err)
	}
	pool := &fakeLoomRelayPool{sub: sub}
	client := &Client{
		pool:         pool,
		privateKey:   clientSK,
		clientPubkey: clientPK,
		jobTimeout:   time.Minute,
		logger:       zap.NewNop(),
	}
	return client, pool, clientPK
}

func testSubscription(events <-chan *nostr.Event, eose <-chan struct{}, relayEOSE <-chan nostrAdapter.RelayEOSE, closed <-chan nostrAdapter.RelayClosed) *nostrAdapter.MergedSubscription {
	return &nostrAdapter.MergedSubscription{
		Events:            events,
		EndOfStoredEvents: eose,
		RelayEOSE:         relayEOSE,
		Closed:            closed,
	}
}

func signLoomEvent(t *testing.T, sk string, kind int, createdAt time.Time, tags nostr.Tags, content string) *nostr.Event {
	t.Helper()
	ev := &nostr.Event{
		Kind:      kind,
		Content:   content,
		CreatedAt: nostr.Timestamp(createdAt.Unix()),
		Tags:      tags,
	}
	if err := ev.Sign(sk); err != nil {
		t.Fatalf("sign event: %v", err)
	}
	return ev
}

func validResultEvent(t *testing.T, workerSK, jobID, clientPK string) *nostr.Event {
	t.Helper()
	return signLoomEvent(t, workerSK, KindJobResult, time.Now().UTC(), nostr.Tags{
		{"e", jobID},
		{"p", clientPK},
		{"success", "true"},
		{"exit_code", "0"},
		{"duration", "7"},
		{"stdout", "https://blossom.example/stdout"},
		{"stderr", "https://blossom.example/stderr"},
	}, "")
}

func validStatusEvent(t *testing.T, workerSK, jobID, clientPK, status string) *nostr.Event {
	t.Helper()
	return signLoomEvent(t, workerSK, KindJobStatus, time.Now().UTC(), nostr.Tags{
		{"d", jobID},
		{"e", jobID},
		{"p", clientPK},
		{"status", status},
	}, "running logs")
}

func TestPollJobStatusFromWorker_ValidTerminalResultCompletes(t *testing.T) {
	clientSK := nostr.GeneratePrivateKey()
	workerSK := nostr.GeneratePrivateKey()
	workerPK, _ := nostr.GetPublicKey(workerSK)
	jobID := strings.Repeat("a", 64)

	events := make(chan *nostr.Event, 1)
	sub := testSubscription(events, nil, nil, nil)
	client, pool, clientPK := testClient(t, sub, clientSK)
	events <- validResultEvent(t, workerSK, jobID, clientPK)

	status, err := client.PollJobStatusFromWorker(context.Background(), jobID, workerPK)
	if err != nil {
		t.Fatalf("PollJobStatusFromWorker() error = %v", err)
	}
	if status.Status != StatusCompleted {
		t.Fatalf("status = %q, want %q", status.Status, StatusCompleted)
	}
	if status.WorkerPubkey != workerPK {
		t.Fatalf("worker pubkey = %q, want %q", status.WorkerPubkey, workerPK)
	}
	if status.ExitCode == nil || *status.ExitCode != 0 {
		t.Fatalf("exit code = %v, want 0", status.ExitCode)
	}
	if len(pool.filters) != 2 {
		t.Fatalf("filters = %d, want 2", len(pool.filters))
	}
	if got := pool.filters[0].Tags["d"]; len(got) != 1 || got[0] != jobID {
		t.Fatalf("status filter d tag = %#v, want job id", got)
	}
	if got := pool.filters[0].Tags["p"]; len(got) != 1 || got[0] != clientPK {
		t.Fatalf("status filter p tag = %#v, want client pubkey", got)
	}
	if got := pool.filters[1].Authors; len(got) != 1 || got[0] != workerPK {
		t.Fatalf("result filter authors = %#v, want worker", got)
	}
}

func TestPollJobStatusFromWorker_DropsInvalidEventsBeforeResult(t *testing.T) {
	clientSK := nostr.GeneratePrivateKey()
	workerSK := nostr.GeneratePrivateKey()
	workerPK, _ := nostr.GetPublicKey(workerSK)
	otherWorkerSK := nostr.GeneratePrivateKey()
	jobID := strings.Repeat("b", 64)
	otherJobID := strings.Repeat("c", 64)

	cases := []struct {
		name  string
		event func(t *testing.T, clientPK string) *nostr.Event
	}{
		{
			name: "forged id signature mismatch",
			event: func(t *testing.T, clientPK string) *nostr.Event {
				ev := validResultEvent(t, workerSK, jobID, clientPK)
				ev.Content = "tampered"
				return ev
			},
		},
		{
			name: "stale event",
			event: func(t *testing.T, clientPK string) *nostr.Event {
				return signLoomEvent(t, workerSK, KindJobResult, time.Now().UTC().Add(-366*24*time.Hour), nostr.Tags{
					{"e", jobID}, {"p", clientPK}, {"success", "true"}, {"exit_code", "0"}, {"duration", "1"}, {"stdout", "u"}, {"stderr", "u"},
				}, "")
			},
		},
		{
			name: "future event",
			event: func(t *testing.T, clientPK string) *nostr.Event {
				return signLoomEvent(t, workerSK, KindJobResult, time.Now().UTC().Add(11*time.Minute), nostr.Tags{
					{"e", jobID}, {"p", clientPK}, {"success", "true"}, {"exit_code", "0"}, {"duration", "1"}, {"stdout", "u"}, {"stderr", "u"},
				}, "")
			},
		},
		{
			name: "wrong kind",
			event: func(t *testing.T, clientPK string) *nostr.Event {
				return signLoomEvent(t, workerSK, KindJobCancelReq, time.Now().UTC(), nostr.Tags{{"e", jobID}, {"p", clientPK}}, "")
			},
		},
		{
			name: "wrong job",
			event: func(t *testing.T, clientPK string) *nostr.Event {
				return signLoomEvent(t, workerSK, KindJobResult, time.Now().UTC(), nostr.Tags{
					{"e", otherJobID}, {"p", clientPK}, {"success", "true"}, {"exit_code", "0"}, {"duration", "1"}, {"stdout", "u"}, {"stderr", "u"},
				}, "")
			},
		},
		{
			name: "wrong worker",
			event: func(t *testing.T, clientPK string) *nostr.Event {
				return validResultEvent(t, otherWorkerSK, jobID, clientPK)
			},
		},
		{
			name: "malformed missing success",
			event: func(t *testing.T, clientPK string) *nostr.Event {
				return signLoomEvent(t, workerSK, KindJobResult, time.Now().UTC(), nostr.Tags{
					{"e", jobID}, {"p", clientPK}, {"exit_code", "0"}, {"duration", "1"}, {"stdout", "u"}, {"stderr", "u"},
				}, "")
			},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			events := make(chan *nostr.Event, 2)
			sub := testSubscription(events, nil, nil, nil)
			client, _, clientPK := testClient(t, sub, clientSK)
			events <- tt.event(t, clientPK)
			events <- validResultEvent(t, workerSK, jobID, clientPK)

			status, err := client.PollJobStatusFromWorker(context.Background(), jobID, workerPK)
			if err != nil {
				t.Fatalf("PollJobStatusFromWorker() error = %v", err)
			}
			if status.WorkerPubkey != workerPK || status.Status != StatusCompleted {
				t.Fatalf("trusted status = %#v, want valid worker completion", status)
			}
		})
	}
}

func TestPollJobStatus_UsesRememberedSubmittedWorkerForValidation(t *testing.T) {
	clientSK := nostr.GeneratePrivateKey()
	workerSK := nostr.GeneratePrivateKey()
	workerPK, _ := nostr.GetPublicKey(workerSK)
	otherWorkerSK := nostr.GeneratePrivateKey()
	jobID := strings.Repeat("3", 64)

	events := make(chan *nostr.Event, 2)
	sub := testSubscription(events, nil, nil, nil)
	client, pool, clientPK := testClient(t, sub, clientSK)
	client.rememberSubmittedWorker(jobID, workerPK)
	events <- validResultEvent(t, otherWorkerSK, jobID, clientPK)
	events <- validResultEvent(t, workerSK, jobID, clientPK)

	status, err := client.PollJobStatus(context.Background(), jobID)
	if err != nil {
		t.Fatalf("PollJobStatus() error = %v", err)
	}
	if status.WorkerPubkey != workerPK {
		t.Fatalf("worker pubkey = %q, want remembered worker %q", status.WorkerPubkey, workerPK)
	}
	if got := pool.filters[0].Authors; len(got) != 1 || got[0] != workerPK {
		t.Fatalf("status filter authors = %#v, want remembered worker", got)
	}
}

func TestPollJobStatusFromWorker_DeduplicatesStatusCallbacks(t *testing.T) {
	clientSK := nostr.GeneratePrivateKey()
	workerSK := nostr.GeneratePrivateKey()
	workerPK, _ := nostr.GetPublicKey(workerSK)
	jobID := strings.Repeat("d", 64)

	events := make(chan *nostr.Event, 3)
	sub := testSubscription(events, nil, nil, nil)
	client, _, clientPK := testClient(t, sub, clientSK)
	statusEvent := validStatusEvent(t, workerSK, jobID, clientPK, StatusRunning)
	events <- statusEvent
	events <- statusEvent
	events <- validResultEvent(t, workerSK, jobID, clientPK)

	callbackCount := 0
	_, err := client.PollJobStatusFromWorker(context.Background(), jobID, workerPK, func(status *JobStatus) {
		callbackCount++
		if status.Status != StatusRunning {
			t.Fatalf("callback status = %q, want %q", status.Status, StatusRunning)
		}
	})
	if err != nil {
		t.Fatalf("PollJobStatusFromWorker() error = %v", err)
	}
	if callbackCount != 1 {
		t.Fatalf("callback count = %d, want 1", callbackCount)
	}
}

func TestPollJobStatusFromWorker_HandlesEOSEAndWaitsForRealtimeResult(t *testing.T) {
	clientSK := nostr.GeneratePrivateKey()
	workerSK := nostr.GeneratePrivateKey()
	workerPK, _ := nostr.GetPublicKey(workerSK)
	jobID := strings.Repeat("e", 64)
	events := make(chan *nostr.Event)
	eose := make(chan struct{})
	close(eose)
	sub := testSubscription(events, eose, nil, nil)
	client, _, clientPK := testClient(t, sub, clientSK)

	done := make(chan error, 1)
	go func() {
		status, err := client.PollJobStatusFromWorker(context.Background(), jobID, workerPK)
		if err == nil && status.Status != StatusCompleted {
			err = errors.New("unexpected status")
		}
		done <- err
	}()
	events <- validResultEvent(t, workerSK, jobID, clientPK)
	if err := <-done; err != nil {
		t.Fatalf("PollJobStatusFromWorker() error = %v", err)
	}
}

func TestPollJobStatusFromWorker_ContinuesAfterRelayClosedWhenResultArrives(t *testing.T) {
	clientSK := nostr.GeneratePrivateKey()
	workerSK := nostr.GeneratePrivateKey()
	workerPK, _ := nostr.GetPublicKey(workerSK)
	jobID := strings.Repeat("2", 64)
	events := make(chan *nostr.Event, 1)
	closed := make(chan nostrAdapter.RelayClosed, 1)
	closed <- nostrAdapter.RelayClosed{RelayURL: "wss://relay.example", SubscriptionID: "sub", Reason: "rate-limited: slow down"}
	close(closed)
	sub := testSubscription(events, nil, nil, closed)
	client, _, clientPK := testClient(t, sub, clientSK)
	events <- validResultEvent(t, workerSK, jobID, clientPK)

	status, err := client.PollJobStatusFromWorker(context.Background(), jobID, workerPK)
	if err != nil {
		t.Fatalf("PollJobStatusFromWorker() error = %v", err)
	}
	if status.Status != StatusCompleted {
		t.Fatalf("status = %q, want completed", status.Status)
	}
}

func TestPollJobStatusFromWorker_ClosedAuthFailureIsSurfaced(t *testing.T) {
	clientSK := nostr.GeneratePrivateKey()
	jobID := strings.Repeat("f", 64)
	closed := make(chan nostrAdapter.RelayClosed, 1)
	closed <- nostrAdapter.RelayClosed{RelayURL: "wss://relay.example", SubscriptionID: "sub", Reason: "auth-required: restricted"}
	sub := testSubscription(nil, nil, nil, closed)
	client, pool, _ := testClient(t, sub, clientSK)
	pool.authErr = errors.New("auth failed")

	_, err := client.PollJobStatusFromWorker(context.Background(), jobID, "")
	if err == nil || !strings.Contains(err.Error(), "auth") {
		t.Fatalf("error = %v, want auth error", err)
	}
	if pool.authCalls != 1 {
		t.Fatalf("auth calls = %d, want 1", pool.authCalls)
	}
}

func TestPollJobStatusFromWorker_ClosedWithoutTerminalResultIsSurfaced(t *testing.T) {
	clientSK := nostr.GeneratePrivateKey()
	jobID := strings.Repeat("1", 64)
	events := make(chan *nostr.Event)
	close(events)
	closed := make(chan nostrAdapter.RelayClosed, 1)
	closed <- nostrAdapter.RelayClosed{RelayURL: "wss://relay.example", SubscriptionID: "sub", Reason: "rate-limited: slow down"}
	close(closed)
	sub := testSubscription(events, nil, nil, closed)
	client, _, _ := testClient(t, sub, clientSK)

	_, err := client.PollJobStatusFromWorker(context.Background(), jobID, "")
	if err == nil {
		t.Fatal("expected error when subscription closes without a terminal result")
	}
}
