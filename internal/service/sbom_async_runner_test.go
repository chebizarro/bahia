package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	sbomadapter "github.com/openagentsinc/bahia/internal/adapters/sbom"
	"github.com/openagentsinc/bahia/internal/domain"
)

func TestSBOMAsyncRunnerAcceptedAckThenWorkerPublishesAfterAUTHAndOK(t *testing.T) {
	publisher := &fakeSBOMPublisher{results: []sbomadapter.PublishOKResult{{RelayURL: "wss://relay.example", Accepted: true, Reason: "stored"}}}
	repo := newFakeSBOMManifestRepo()
	subscriber := &fakeSBOMAvailabilitySubscriber{messages: []SBOMAvailabilitySubscriptionMessage{
		{Auth: SBOMAvailabilityRelayAuth{RelayURL: "wss://relay.example", Challenge: "challenge-async"}},
		{EOSE: true},
	}}
	orchestrator := newTestSBOMOrchestrator(t, publisher, subscriber, repo, fakeGenerator{payload: testSPDXPayload(t), generatorID: sbomadapter.GeneratorSyft})
	runner, results, stop := startTestSBOMAsyncRunner(t, orchestrator)
	defer stop()

	ack, err := runner.EnqueueGenerate(context.Background(), SBOMGenerateRequest{
		IDempotencyKey: "async-ok",
		Subject:        domain.SBOMSubject{Type: domain.SBOMSubjectArtifact, ID: "artifact-async", Digest: testSubjectDigest},
		Source:         sbomadapter.SourceRequest{Kind: sbomadapter.SourceKindDirectory, Locator: "/tmp/source"},
		Formats:        []domain.SBOMFormat{domain.SBOMFormatSPDX},
		Generator:      sbomadapter.GeneratorSyft,
		Storage:        domain.SBOMStorageBlossom,
	})
	if err != nil {
		t.Fatalf("EnqueueGenerate() error = %v", err)
	}
	if !ack.Accepted || ack.StatusDTag != "sbom:run:async-ok" || ack.IDempotencyKey != "async-ok" {
		t.Fatalf("unexpected ack: %#v", ack)
	}

	result := <-results
	if result.Err != nil {
		t.Fatalf("worker result error = %v", result.Err)
	}
	if result.Run == nil || result.Run.AvailabilityID == "" {
		t.Fatalf("worker did not complete canonical publication: %#v", result.Run)
	}
	if len(subscriber.authenticatedRelays) != 1 || subscriber.authenticatedRelays[0] != "wss://relay.example" {
		t.Fatalf("AUTH challenge was not handled: %#v", subscriber.authenticatedRelays)
	}
	if !publisher.containsKind(sbomadapter.KindSBOMReference) || !publisher.containsKind(sbomadapter.KindSBOMAvailabilityList) || len(repo.projected) != 1 {
		t.Fatalf("worker did not drive observable publication/projection: kinds=%v projected=%d", publishedKinds(publisher), len(repo.projected))
	}
}

func TestSBOMAsyncRunnerImportAckThenWorkerPublishesAfterOK(t *testing.T) {
	publisher := &fakeSBOMPublisher{results: []sbomadapter.PublishOKResult{{RelayURL: "wss://relay.example", Accepted: true, Reason: "stored"}}}
	repo := newFakeSBOMManifestRepo()
	subscriber := &fakeSBOMAvailabilitySubscriber{messages: []SBOMAvailabilitySubscriptionMessage{{EOSE: true}}}
	orchestrator := newTestSBOMOrchestrator(t, publisher, subscriber, repo, fakeGenerator{payload: testSPDXPayload(t), generatorID: sbomadapter.GeneratorSyft})
	runner, results, stop := startTestSBOMAsyncRunner(t, orchestrator)
	defer stop()

	ack, err := runner.EnqueueImport(context.Background(), SBOMImportRequest{
		IDempotencyKey: "async-import-ok",
		Subject:        domain.SBOMSubject{Type: domain.SBOMSubjectArtifact, ID: "artifact-import-async", Digest: testSubjectDigest},
		Format:         domain.SBOMFormatSPDX,
		Payload:        testSPDXPayload(t),
		Storage:        domain.SBOMStorageBlossom,
		Generator:      domain.SBOMGenerator{ID: "import"},
	})
	if err != nil {
		t.Fatalf("EnqueueImport() error = %v", err)
	}
	if !ack.Accepted || ack.StatusDTag != "sbom:run:async-import-ok" || ack.IDempotencyKey != "async-import-ok" {
		t.Fatalf("unexpected import ack: %#v", ack)
	}

	result := <-results
	if result.Err != nil {
		t.Fatalf("worker result error = %v", result.Err)
	}
	if result.Run == nil || result.Run.AvailabilityID == "" {
		t.Fatalf("import worker did not complete canonical publication: %#v", result.Run)
	}
	if !publisher.containsKind(sbomadapter.KindSBOMReference) || !publisher.containsKind(sbomadapter.KindSBOMAvailabilityList) || len(repo.projected) != 1 {
		t.Fatalf("import worker did not drive observable publication/projection: kinds=%v projected=%d", publishedKinds(publisher), len(repo.projected))
	}
	if repo.projected[0].SourceKind != domain.SBOMSourceImported {
		t.Fatalf("projected source kind = %q, want imported", repo.projected[0].SourceKind)
	}
}

func TestSBOMAsyncRunnerSurfacesClosedBeforeEOSEWithoutAvailabilityPublication(t *testing.T) {
	publisher := &fakeSBOMPublisher{results: []sbomadapter.PublishOKResult{{RelayURL: "wss://relay.example", Accepted: true, Reason: "stored"}}}
	repo := newFakeSBOMManifestRepo()
	subscriber := &fakeSBOMAvailabilitySubscriber{messages: []SBOMAvailabilitySubscriptionMessage{{Closed: SBOMAvailabilityRelayClosed{RelayURL: "wss://relay.example", SubscriptionID: "sub-closed", Reason: "auth-required: restricted read"}}}}
	orchestrator := newTestSBOMOrchestrator(t, publisher, subscriber, repo, fakeGenerator{payload: testSPDXPayload(t), generatorID: sbomadapter.GeneratorSyft})
	runner, results, stop := startTestSBOMAsyncRunner(t, orchestrator)
	defer stop()

	ack, err := runner.EnqueueGenerate(context.Background(), SBOMGenerateRequest{IDempotencyKey: "async-closed", Subject: domain.SBOMSubject{Type: domain.SBOMSubjectArtifact, ID: "artifact-async", Digest: testSubjectDigest}, Source: sbomadapter.SourceRequest{Kind: sbomadapter.SourceKindDirectory, Locator: "/tmp/source"}, Formats: []domain.SBOMFormat{domain.SBOMFormatSPDX}, Generator: sbomadapter.GeneratorSyft, Storage: domain.SBOMStorageBlossom})
	if err != nil || !ack.Accepted {
		t.Fatalf("EnqueueGenerate() ack=%#v err=%v", ack, err)
	}

	result := <-results
	if result.Err == nil || !strings.Contains(result.Err.Error(), "before EOSE") || !strings.Contains(result.Err.Error(), "auth-required") {
		t.Fatalf("worker error = %v, want CLOSED before EOSE auth-required failure", result.Err)
	}
	if publisher.containsKind(sbomadapter.KindSBOMAvailabilityList) || len(repo.projected) != 0 {
		t.Fatalf("worker published availability/projection after CLOSED: kinds=%v projected=%d", publishedKinds(publisher), len(repo.projected))
	}
}

func TestSBOMAsyncRunnerSurfacesOKRejectionWithoutProjection(t *testing.T) {
	publisher := &fakeSBOMPublisher{results: []sbomadapter.PublishOKResult{{RelayURL: "wss://relay.example", Accepted: false, Reason: "auth-required: restricted write"}}}
	repo := newFakeSBOMManifestRepo()
	orchestrator := newTestSBOMOrchestrator(t, publisher, &fakeSBOMAvailabilitySubscriber{messages: []SBOMAvailabilitySubscriptionMessage{{EOSE: true}}}, repo, fakeGenerator{payload: testSPDXPayload(t), generatorID: sbomadapter.GeneratorSyft})
	runner, results, stop := startTestSBOMAsyncRunner(t, orchestrator)
	defer stop()

	ack, err := runner.EnqueueGenerate(context.Background(), SBOMGenerateRequest{IDempotencyKey: "async-auth-required", Subject: domain.SBOMSubject{Type: domain.SBOMSubjectArtifact, ID: "artifact-async", Digest: testSubjectDigest}, Source: sbomadapter.SourceRequest{Kind: sbomadapter.SourceKindDirectory, Locator: "/tmp/source"}, Formats: []domain.SBOMFormat{domain.SBOMFormatSPDX}, Generator: sbomadapter.GeneratorSyft, Storage: domain.SBOMStorageBlossom})
	if err != nil || !ack.Accepted {
		t.Fatalf("EnqueueGenerate() ack=%#v err=%v", ack, err)
	}

	result := <-results
	if result.Err == nil || !strings.Contains(result.Err.Error(), "auth-required") {
		t.Fatalf("worker error = %v, want OK rejection surfaced", result.Err)
	}
	if len(repo.projected) != 0 {
		t.Fatalf("projection occurred after OK rejection")
	}
}

func TestSBOMAsyncRunnerRejectsFullQueueWithoutPolling(t *testing.T) {
	orchestrator := newTestSBOMOrchestrator(t, &fakeSBOMPublisher{results: []sbomadapter.PublishOKResult{{RelayURL: "wss://relay.example", Accepted: true, Reason: "stored"}}}, &fakeSBOMAvailabilitySubscriber{messages: []SBOMAvailabilitySubscriptionMessage{{EOSE: true}}}, newFakeSBOMManifestRepo(), fakeGenerator{payload: testSPDXPayload(t), generatorID: sbomadapter.GeneratorSyft})
	runner := NewSBOMAsyncRunner(orchestrator, WithSBOMAsyncRunnerQueueDepth(1))
	_, err := runner.EnqueueGenerate(context.Background(), SBOMGenerateRequest{IDempotencyKey: "queued-1"})
	if err != nil {
		t.Fatalf("first EnqueueGenerate() error = %v", err)
	}
	_, err = runner.EnqueueGenerate(context.Background(), SBOMGenerateRequest{IDempotencyKey: "queued-2"})
	if err == nil || !strings.Contains(err.Error(), "queue is full") {
		t.Fatalf("second EnqueueGenerate() error = %v, want queue full", err)
	}
}

func startTestSBOMAsyncRunner(t *testing.T, orchestrator *SBOMOrchestrator) (*SBOMAsyncRunner, <-chan SBOMAsyncResult, func()) {
	t.Helper()
	results := make(chan SBOMAsyncResult, 1)
	runner := NewSBOMAsyncRunner(orchestrator, WithSBOMAsyncRunnerQueueDepth(4), WithSBOMAsyncResultObserver(func(result SBOMAsyncResult) { results <- result }))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	stop := func() {
		cancel()
		if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("runner stopped with error: %v", err)
		}
	}
	return runner, results, stop
}

func publishedKinds(publisher *fakeSBOMPublisher) []int {
	out := make([]int, 0, len(publisher.events))
	for _, event := range publisher.events {
		out = append(out, int(event.Kind))
	}
	return out
}
