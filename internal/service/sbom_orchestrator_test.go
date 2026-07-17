package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	sbomadapter "github.com/openagentsinc/bahia/internal/adapters/sbom"
	"github.com/openagentsinc/bahia/internal/domain"
)

const testSubjectDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestSBOMOrchestratorGeneratePublishesProjectsAndAudits(t *testing.T) {
	ctx := context.Background()
	publisher := &fakeSBOMPublisher{results: []sbomadapter.PublishOKResult{{RelayURL: "wss://relay.example", Accepted: true, Reason: "stored"}}}
	repo := newFakeSBOMManifestRepo()
	repo.eventCount = func() int { return len(publisher.events) }
	subscriber := &fakeSBOMAvailabilitySubscriber{messages: []SBOMAvailabilitySubscriptionMessage{{EOSE: true}}}
	orchestrator := newTestSBOMOrchestrator(t, publisher, subscriber, repo, fakeGenerator{payload: testSPDXPayload(t), generatorID: sbomadapter.GeneratorSyft})

	result, err := orchestrator.Generate(ctx, SBOMGenerateRequest{
		IDempotencyKey: "run-1",
		Subject:        domain.SBOMSubject{Type: domain.SBOMSubjectArtifact, ID: "artifact-1", Digest: testSubjectDigest, DisplayName: "artifact"},
		Source:         sbomadapter.SourceRequest{Kind: sbomadapter.SourceKindDirectory, Locator: "/tmp/source"},
		Formats:        []domain.SBOMFormat{domain.SBOMFormatSPDX},
		Generator:      sbomadapter.GeneratorSyft,
		Storage:        domain.SBOMStorageBlossom,
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if result.PublishState != domain.SBOMPublishPublished || len(result.ManifestIDs) != 1 || len(result.ReferenceEventIDs) != 1 || result.AvailabilityID == "" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if len(repo.projected) != 1 || len(repo.packages) != 1 {
		t.Fatalf("projection counts manifests=%d packages=%d", len(repo.projected), len(repo.packages))
	}
	manifest := repo.projected[0]
	if manifest.StorageType != domain.SBOMStorageBlossom || manifest.ReferenceDTag == "" || manifest.AvailabilityDTag == "" || manifest.PayloadSHA256 == "" || manifest.PublishState != domain.SBOMPublishPublished {
		t.Fatalf("manifest projection missing canonical publication fields: %#v", manifest)
	}
	if len(repo.projectEventCounts) != 1 || repo.projectEventCounts[0] <= publisher.firstKindIndex(sbomadapter.KindSBOMAvailabilityList) {
		t.Fatalf("projection happened before accepted availability list publication: project counts=%v", repo.projectEventCounts)
	}
	assertPublishedKinds(t, publisher, []int{KindSBOMStatus, KindSBOMStatus, KindSBOMStatus, KindSBOMStatus, KindSBOMStatus, sbomadapter.KindSBOMReference, KindSBOMStatus, sbomadapter.KindSBOMAvailabilityList, KindSBOMStatus, KindSBOMAudit, KindSBOMStatus})
	if publisher.containsKind(sbomadapter.KindLegacySBOMIndex) {
		t.Fatalf("legacy 30079 publication observed")
	}

	cached, err := orchestrator.Generate(ctx, SBOMGenerateRequest{IDempotencyKey: "run-1", Subject: manifest.Subject, Source: sbomadapter.SourceRequest{Kind: sbomadapter.SourceKindDirectory, Locator: "/tmp/source"}, Formats: []domain.SBOMFormat{domain.SBOMFormatSPDX}, Generator: sbomadapter.GeneratorSyft})
	if err != nil {
		t.Fatalf("cached Generate() error = %v", err)
	}
	if cached.AvailabilityID != result.AvailabilityID || len(publisher.events) != 11 {
		t.Fatalf("idempotency did not return cached result without new events")
	}
}

func TestSBOMOrchestratorStopsOnRelayRejection(t *testing.T) {
	publisher := &fakeSBOMPublisher{results: []sbomadapter.PublishOKResult{{RelayURL: "wss://relay.example", Accepted: false, Reason: "auth-required: restricted write"}}}
	repo := newFakeSBOMManifestRepo()
	orchestrator := newTestSBOMOrchestrator(t, publisher, &fakeSBOMAvailabilitySubscriber{messages: []SBOMAvailabilitySubscriptionMessage{{EOSE: true}}}, repo, fakeGenerator{payload: testSPDXPayload(t), generatorID: sbomadapter.GeneratorSyft})

	_, err := orchestrator.Generate(context.Background(), SBOMGenerateRequest{IDempotencyKey: "run-auth", Subject: domain.SBOMSubject{Type: domain.SBOMSubjectArtifact, ID: "artifact-1", Digest: testSubjectDigest}, Source: sbomadapter.SourceRequest{Kind: sbomadapter.SourceKindDirectory, Locator: "/tmp/source"}, Formats: []domain.SBOMFormat{domain.SBOMFormatSPDX}, Generator: sbomadapter.GeneratorSyft})
	if err == nil || !strings.Contains(err.Error(), "auth-required") {
		t.Fatalf("Generate() error = %v, want auth-required rejection", err)
	}
	if len(repo.projected) != 0 {
		t.Fatalf("projection occurred after relay rejection")
	}
}

func TestSBOMOrchestratorFailsOnClosedLikePublishError(t *testing.T) {
	publisher := &fakeSBOMPublisher{results: []sbomadapter.PublishOKResult{{RelayURL: "wss://relay.example", Accepted: false, Error: errors.New("CLOSED: policy violation")}}}
	repo := newFakeSBOMManifestRepo()
	orchestrator := newTestSBOMOrchestrator(t, publisher, &fakeSBOMAvailabilitySubscriber{messages: []SBOMAvailabilitySubscriptionMessage{{EOSE: true}}}, repo, fakeGenerator{payload: testSPDXPayload(t), generatorID: sbomadapter.GeneratorSyft})

	_, err := orchestrator.Generate(context.Background(), SBOMGenerateRequest{IDempotencyKey: "run-closed", Subject: domain.SBOMSubject{Type: domain.SBOMSubjectArtifact, ID: "artifact-1", Digest: testSubjectDigest}, Source: sbomadapter.SourceRequest{Kind: sbomadapter.SourceKindDirectory, Locator: "/tmp/source"}, Formats: []domain.SBOMFormat{domain.SBOMFormatSPDX}, Generator: sbomadapter.GeneratorSyft})
	if err == nil || !strings.Contains(err.Error(), "CLOSED") {
		t.Fatalf("Generate() error = %v, want CLOSED-like failure", err)
	}
	if len(repo.projected) != 0 {
		t.Fatalf("projection occurred after CLOSED-like failure")
	}
}

func TestSBOMOrchestratorMergesRelayAvailabilityBeforeReplacement(t *testing.T) {
	ctx := context.Background()
	publisher := &fakeSBOMPublisher{results: []sbomadapter.PublishOKResult{{RelayURL: "wss://relay.example", Accepted: true, Reason: "stored"}}}
	repo := newFakeSBOMManifestRepo()
	subject := domain.SBOMSubject{Type: domain.SBOMSubjectArtifact, ID: "artifact-merge", Digest: testSubjectDigest, DisplayName: "artifact"}
	remoteEvent := signedAvailabilityEvent(t, subject, publisherPubkeyFromSecret(t, publisher), publisher.signer, []domain.SBOMIndexEntry{remoteAvailabilityEntry(subject)})
	subscriber := &fakeSBOMAvailabilitySubscriber{messages: []SBOMAvailabilitySubscriptionMessage{
		{Auth: SBOMAvailabilityRelayAuth{RelayURL: "wss://relay.example", Challenge: "challenge-1"}},
		{RelayEOSE: SBOMAvailabilityRelayEOSE{RelayURL: "wss://relay.example", SubscriptionID: "sub-1"}},
		{Event: remoteEvent},
		{EOSE: true},
	}}
	orchestrator := newTestSBOMOrchestrator(t, publisher, subscriber, repo, fakeGenerator{payload: testSPDXPayload(t), generatorID: sbomadapter.GeneratorSyft})

	result, err := orchestrator.Generate(ctx, SBOMGenerateRequest{IDempotencyKey: "run-merge", Subject: subject, Source: sbomadapter.SourceRequest{Kind: sbomadapter.SourceKindDirectory, Locator: "/tmp/source"}, Formats: []domain.SBOMFormat{domain.SBOMFormatSPDX}, Generator: sbomadapter.GeneratorSyft, Storage: domain.SBOMStorageBlossom})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if result.AvailabilityID == "" {
		t.Fatalf("availability ID missing")
	}
	if len(subscriber.authenticatedRelays) != 1 || subscriber.authenticatedRelays[0] != "wss://relay.example" {
		t.Fatalf("AUTH challenge was not handled deterministically: %#v", subscriber.authenticatedRelays)
	}
	if len(subscriber.filters) != 1 {
		t.Fatalf("expected one scoped availability subscription, got %d", len(subscriber.filters))
	}
	filter := subscriber.filters[0]
	wantD, err := sbomadapter.AvailabilityListDTag(subject)
	if err != nil {
		t.Fatal(err)
	}
	if len(filter.Kinds) != 1 || int(filter.Kinds[0]) != sbomadapter.KindSBOMAvailabilityList || filter.Tags["d"][0] != wantD || filter.Tags["subject"][0] != subject.Digest || filter.Tags["subject_type"][0] != string(subject.Type) {
		t.Fatalf("availability filter was not scoped to kind/d/subject/subject_type: %#v", filter)
	}
	availability := parsePublishedAvailability(t, publisher)
	if len(availability.Entries) != 2 {
		t.Fatalf("merged availability entries = %d, want local + relay entries: %#v", len(availability.Entries), availability.Entries)
	}
	if !availabilityContainsPayload(availability, remotePayloadSHA) {
		t.Fatalf("relay-backed SBOM reference was dropped from replacement list: %#v", availability.Entries)
	}
}

func TestSBOMOrchestratorFailsClosedWhenAvailabilitySubscriptionClosesBeforeEOSE(t *testing.T) {
	publisher := &fakeSBOMPublisher{results: []sbomadapter.PublishOKResult{{RelayURL: "wss://relay.example", Accepted: true, Reason: "stored"}}}
	repo := newFakeSBOMManifestRepo()
	subscriber := &fakeSBOMAvailabilitySubscriber{messages: []SBOMAvailabilitySubscriptionMessage{{Closed: SBOMAvailabilityRelayClosed{RelayURL: "wss://relay.example", SubscriptionID: "sub-closed", Reason: "auth-required: restricted read"}}}}
	orchestrator := newTestSBOMOrchestrator(t, publisher, subscriber, repo, fakeGenerator{payload: testSPDXPayload(t), generatorID: sbomadapter.GeneratorSyft})

	_, err := orchestrator.Generate(context.Background(), SBOMGenerateRequest{IDempotencyKey: "run-sub-closed", Subject: domain.SBOMSubject{Type: domain.SBOMSubjectArtifact, ID: "artifact-1", Digest: testSubjectDigest}, Source: sbomadapter.SourceRequest{Kind: sbomadapter.SourceKindDirectory, Locator: "/tmp/source"}, Formats: []domain.SBOMFormat{domain.SBOMFormatSPDX}, Generator: sbomadapter.GeneratorSyft})
	if err == nil || !strings.Contains(err.Error(), "before EOSE") || !strings.Contains(err.Error(), "auth-required") {
		t.Fatalf("Generate() error = %v, want CLOSED before EOSE failure", err)
	}
	if !subscriber.closed {
		t.Fatalf("subscription was not closed")
	}
	if publisher.containsKind(sbomadapter.KindSBOMAvailabilityList) {
		t.Fatalf("availability list was published after incomplete relay catch-up")
	}
	if len(repo.projected) != 0 {
		t.Fatalf("projection occurred after incomplete relay catch-up")
	}
}

func TestSBOMOrchestratorRejectsInvalidRelayAvailabilityContent(t *testing.T) {
	publisher := &fakeSBOMPublisher{results: []sbomadapter.PublishOKResult{{RelayURL: "wss://relay.example", Accepted: true, Reason: "stored"}}}
	repo := newFakeSBOMManifestRepo()
	subject := domain.SBOMSubject{Type: domain.SBOMSubjectArtifact, ID: "artifact-invalid", Digest: testSubjectDigest}
	badEvent := signedAvailabilityEvent(t, subject, publisherPubkeyFromSecret(t, publisher), publisher.signer, []domain.SBOMIndexEntry{remoteAvailabilityEntry(subject)})
	badEvent.Content = `{"entries":[{"subjectDigest":"sha256:not-this-subject"}]}`
	_ = badEvent.Sign(publisher.signer)
	subscriber := &fakeSBOMAvailabilitySubscriber{messages: []SBOMAvailabilitySubscriptionMessage{{Event: badEvent}, {EOSE: true}}}
	orchestrator := newTestSBOMOrchestrator(t, publisher, subscriber, repo, fakeGenerator{payload: testSPDXPayload(t), generatorID: sbomadapter.GeneratorSyft})

	_, err := orchestrator.Generate(context.Background(), SBOMGenerateRequest{IDempotencyKey: "run-invalid-relay", Subject: subject, Source: sbomadapter.SourceRequest{Kind: sbomadapter.SourceKindDirectory, Locator: "/tmp/source"}, Formats: []domain.SBOMFormat{domain.SBOMFormatSPDX}, Generator: sbomadapter.GeneratorSyft})
	if err == nil || !strings.Contains(err.Error(), "availability entry subject digest") {
		t.Fatalf("Generate() error = %v, want invalid relay content rejection", err)
	}
	if publisher.containsKind(sbomadapter.KindSBOMAvailabilityList) {
		t.Fatalf("availability list was published after invalid relay content")
	}
}

func TestSBOMSubjectResolverArtifactAndDeployment(t *testing.T) {
	artifactID := uuid.New()
	intentID := uuid.New()
	resolver := SBOMSubjectResolver{Artifacts: fakeArtifactRepo{artifact: &domain.Artifact{ID: artifactID, ImageRepo: "ghcr.io/acme/api", ImageTag: "v1", ImageDigest: testSubjectDigest}}, Deployments: fakeDeploymentRepo{intent: &domain.DeploymentIntent{ID: intentID, DesiredHash: testSubjectDigest}}}

	artifact, err := resolver.Resolve(context.Background(), domain.SBOMSubject{Type: domain.SBOMSubjectArtifact, ID: artifactID.String()})
	if err != nil || artifact.Digest != testSubjectDigest || artifact.DisplayName != "ghcr.io/acme/api:v1" {
		t.Fatalf("artifact Resolve() = %#v, %v", artifact, err)
	}
	deployment, err := resolver.Resolve(context.Background(), domain.SBOMSubject{Type: domain.SBOMSubjectDeployment, ID: intentID.String()})
	if err != nil || deployment.Digest != testSubjectDigest {
		t.Fatalf("deployment Resolve() = %#v, %v", deployment, err)
	}
	_, err = resolver.Resolve(context.Background(), domain.SBOMSubject{Type: domain.SBOMSubjectRepository, ID: "repo"})
	if err == nil || !strings.Contains(err.Error(), "subjectLocator.repository") {
		t.Fatalf("repository ambiguity error = %v", err)
	}
}

func TestSBOMSubjectResolverPackageLocator(t *testing.T) {
	repositoryID := uuid.New()
	artifact := &domain.PackageArtifact{
		ID:             uuid.New(),
		RepositoryID:   repositoryID,
		RepositoryName: "internal-npm",
		Format:         domain.PackageRepositoryFormatNPM,
		Namespace:      "@acme",
		PackageName:    "utils",
		Version:        "1.2.3",
		Filename:       "utils-1.2.3.tgz",
		SHA256:         strings.TrimPrefix(testSubjectDigest, "sha256:"),
		Status:         domain.PackageArtifactStatusAvailable,
	}
	resolver := SBOMSubjectResolver{Packages: fakePackageRepo{artifact: artifact}}

	resolved, err := resolver.ResolveWithLocator(context.Background(), domain.SBOMSubject{Type: domain.SBOMSubjectPackage}, domain.SBOMSubjectLocator{Package: &domain.SBOMPackageArtifactLocator{
		RepositoryID: repositoryID.String(),
		Namespace:    "@acme",
		PackageName:  "utils",
		Version:      "1.2.3",
		Filename:     "utils-1.2.3.tgz",
		SHA256:       testSubjectDigest,
	}})
	if err != nil {
		t.Fatalf("ResolveWithLocator() error = %v", err)
	}
	if resolved.Digest != testSubjectDigest {
		t.Fatalf("package digest = %q, want %q", resolved.Digest, testSubjectDigest)
	}
	if resolved.ID == "" || !strings.Contains(resolved.ID, repositoryID.String()) {
		t.Fatalf("package subject ID was not derived from immutable coordinates: %#v", resolved)
	}
	if resolved.DisplayName != "@acme/utils@1.2.3 (utils-1.2.3.tgz)" {
		t.Fatalf("package display name = %q", resolved.DisplayName)
	}
}

func TestSBOMSubjectResolverPackageLocatorRejectsSHA256Mismatch(t *testing.T) {
	repositoryID := uuid.New()
	artifact := &domain.PackageArtifact{RepositoryID: repositoryID, PackageName: "utils", Version: "1.2.3", Filename: "utils.tgz", SHA256: strings.TrimPrefix(testSubjectDigest, "sha256:"), Status: domain.PackageArtifactStatusAvailable}
	resolver := SBOMSubjectResolver{Packages: fakePackageRepo{artifact: artifact}}

	_, err := resolver.ResolveWithLocator(context.Background(), domain.SBOMSubject{Type: domain.SBOMSubjectPackage}, domain.SBOMSubjectLocator{Package: &domain.SBOMPackageArtifactLocator{RepositoryID: repositoryID.String(), PackageName: "utils", Version: "1.2.3", Filename: "utils.tgz", SHA256: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}})
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("ResolveWithLocator() error = %v, want sha256 mismatch", err)
	}
}

func TestSBOMSubjectResolverPackageLocatorRejectsNonCanonicalSubjectID(t *testing.T) {
	repositoryID := uuid.New()
	artifact := &domain.PackageArtifact{RepositoryID: repositoryID, Format: domain.PackageRepositoryFormatNPM, PackageName: "utils", Version: "1.2.3", Filename: "utils.tgz", SHA256: strings.TrimPrefix(testSubjectDigest, "sha256:"), Status: domain.PackageArtifactStatusAvailable}
	resolver := SBOMSubjectResolver{Packages: fakePackageRepo{artifact: artifact}}

	_, err := resolver.ResolveWithLocator(context.Background(), domain.SBOMSubject{Type: domain.SBOMSubjectPackage, ID: "pkg:npm/mutable-name@1.2.3"}, domain.SBOMSubjectLocator{Package: &domain.SBOMPackageArtifactLocator{RepositoryID: repositoryID.String(), PackageName: "utils", Version: "1.2.3", Filename: "utils.tgz", SHA256: testSubjectDigest}})
	if err == nil || !strings.Contains(err.Error(), "does not match canonical") {
		t.Fatalf("ResolveWithLocator() error = %v, want non-canonical subject id rejection", err)
	}
}

func TestSBOMSubjectResolverPackageLocatorRejectsUnavailableArtifact(t *testing.T) {
	repositoryID := uuid.New()
	artifact := &domain.PackageArtifact{RepositoryID: repositoryID, PackageName: "utils", Version: "1.2.3", Filename: "utils.tgz", SHA256: strings.TrimPrefix(testSubjectDigest, "sha256:"), Status: domain.PackageArtifactStatusPending}
	resolver := SBOMSubjectResolver{Packages: fakePackageRepo{artifact: artifact}}

	_, err := resolver.ResolveWithLocator(context.Background(), domain.SBOMSubject{Type: domain.SBOMSubjectPackage}, domain.SBOMSubjectLocator{Package: &domain.SBOMPackageArtifactLocator{RepositoryID: repositoryID.String(), PackageName: "utils", Version: "1.2.3", Filename: "utils.tgz", SHA256: testSubjectDigest}})
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("ResolveWithLocator() error = %v, want unavailable artifact rejection", err)
	}
}

func TestSBOMSubjectResolverRepositoryLocators(t *testing.T) {
	commit := "0123456789abcdef0123456789abcdef01234567"
	resolved, err := (SBOMSubjectResolver{}).ResolveWithLocator(context.Background(), domain.SBOMSubject{Type: domain.SBOMSubjectRepository}, domain.SBOMSubjectLocator{Repository: &domain.SBOMRepositoryLocator{RepositoryURL: "https://git.example/acme/api.git", Commit: commit}})
	if err != nil {
		t.Fatalf("ResolveWithLocator(commit) error = %v", err)
	}
	if resolved.ID != "https://git.example/acme/api.git" || resolved.Digest != "git:"+commit {
		t.Fatalf("repository commit resolution = %#v", resolved)
	}

	resolved, err = (SBOMSubjectResolver{}).ResolveWithLocator(context.Background(), domain.SBOMSubject{Type: domain.SBOMSubjectRepository, ID: "acme/api"}, domain.SBOMSubjectLocator{Repository: &domain.SBOMRepositoryLocator{ContentDigest: testSubjectDigest}})
	if err != nil {
		t.Fatalf("ResolveWithLocator(content_digest) error = %v", err)
	}
	if resolved.ID != "acme/api" || resolved.Digest != testSubjectDigest {
		t.Fatalf("repository content digest resolution = %#v", resolved)
	}

	_, err = (SBOMSubjectResolver{}).ResolveWithLocator(context.Background(), domain.SBOMSubject{Type: domain.SBOMSubjectRepository, ID: "acme/api"}, domain.SBOMSubjectLocator{Repository: &domain.SBOMRepositoryLocator{Commit: "not-a-commit"}})
	if err == nil || !strings.Contains(err.Error(), "40- or 64-character") {
		t.Fatalf("ResolveWithLocator(invalid commit) error = %v", err)
	}

	_, err = (SBOMSubjectResolver{}).ResolveWithLocator(context.Background(), domain.SBOMSubject{Type: domain.SBOMSubjectRepository, ID: "acme/api"}, domain.SBOMSubjectLocator{Repository: &domain.SBOMRepositoryLocator{ContentDigest: "tag:v1"}})
	if err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("ResolveWithLocator(non-sha content digest) error = %v", err)
	}
}

func TestSBOMOrchestratorGenerateResolvesPackageSubjectLocator(t *testing.T) {
	ctx := context.Background()
	publisher := &fakeSBOMPublisher{results: []sbomadapter.PublishOKResult{{RelayURL: "wss://relay.example", Accepted: true, Reason: "stored"}}}
	repo := newFakeSBOMManifestRepo()
	subscriber := &fakeSBOMAvailabilitySubscriber{messages: []SBOMAvailabilitySubscriptionMessage{{EOSE: true}}}
	orchestrator := newTestSBOMOrchestrator(t, publisher, subscriber, repo, fakeGenerator{payload: testSPDXPayload(t), generatorID: sbomadapter.GeneratorSyft})
	repositoryID := uuid.New()
	orchestrator.Resolver = SBOMSubjectResolver{Packages: fakePackageRepo{artifact: &domain.PackageArtifact{RepositoryID: repositoryID, Format: domain.PackageRepositoryFormatNPM, Namespace: "@acme", PackageName: "utils", Version: "1.2.3", Filename: "utils.tgz", SHA256: strings.TrimPrefix(testSubjectDigest, "sha256:"), Status: domain.PackageArtifactStatusAvailable}}}

	_, err := orchestrator.Generate(ctx, SBOMGenerateRequest{
		IDempotencyKey: "run-package-locator",
		Subject:        domain.SBOMSubject{Type: domain.SBOMSubjectPackage},
		SubjectLocator: domain.SBOMSubjectLocator{Package: &domain.SBOMPackageArtifactLocator{RepositoryID: repositoryID.String(), Namespace: "@acme", PackageName: "utils", Version: "1.2.3", Filename: "utils.tgz", SHA256: testSubjectDigest}},
		Source:         sbomadapter.SourceRequest{Kind: sbomadapter.SourceKindArchive, Locator: "packages/internal-npm/@acme/utils/-/utils.tgz"},
		Formats:        []domain.SBOMFormat{domain.SBOMFormatSPDX},
		Generator:      sbomadapter.GeneratorSyft,
		Storage:        domain.SBOMStorageBlossom,
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if len(repo.projected) != 1 || repo.projected[0].Subject.Type != domain.SBOMSubjectPackage || repo.projected[0].Subject.Digest != testSubjectDigest {
		t.Fatalf("projected package subject = %#v", repo.projected)
	}
}

func newTestSBOMOrchestrator(t *testing.T, publisher *fakeSBOMPublisher, subscriber *fakeSBOMAvailabilitySubscriber, repo *fakeSBOMManifestRepo, generator fakeGenerator) *SBOMOrchestrator {
	t.Helper()
	registry, err := sbomadapter.NewGeneratorRegistry(generator, nil)
	if err != nil {
		t.Fatal(err)
	}
	if publisher.signer.Hex() == strings.Repeat("0", 64) {
		publisher.signer = nostr.Generate()
	}
	if subscriber == nil {
		subscriber = &fakeSBOMAvailabilitySubscriber{messages: []SBOMAvailabilitySubscriptionMessage{{EOSE: true}}}
	}
	return NewSBOMOrchestrator(SBOMOrchestratorConfig{Generators: registry, Storage: sbomadapter.NewStorageResolver(&sbomadapter.MockBlossomClient{Blobs: map[string][]byte{}}, nil, nil, slog.Default()), Repo: repo, Publisher: publisher, Subscriber: subscriber, Pubkey: publisher.signer.Public().Hex()})
}

type fakeGenerator struct {
	payload     []byte
	generatorID sbomadapter.GeneratorID
}

func (g fakeGenerator) ID() sbomadapter.GeneratorID { return g.generatorID }
func (g fakeGenerator) GenerateSBOM(_ context.Context, req sbomadapter.GenerateRequest) (*sbomadapter.GenerateResult, error) {
	return &sbomadapter.GenerateResult{Subject: req.Subject, Format: req.Format, MediaType: sbomadapter.MediaTypeForFormat(req.Format), Payload: g.payload, Generator: domain.SBOMGenerator{ID: string(g.generatorID), Version: "test"}, Source: req.Source}, nil
}

type fakeSBOMPublisher struct {
	results []sbomadapter.PublishOKResult
	events  []nostr.Event
	signer  nostr.SecretKey
}

type fakeSBOMAvailabilitySubscriber struct {
	messages            []SBOMAvailabilitySubscriptionMessage
	filters             []nostr.Filter
	authenticatedRelays []string
	closed              bool
}

type fakeSBOMAvailabilitySubscription struct {
	parent *fakeSBOMAvailabilitySubscriber
	idx    int
}

func (s *fakeSBOMAvailabilitySubscriber) SubscribeAllWithEOSE(_ context.Context, filters []nostr.Filter) (SBOMAvailabilitySubscription, error) {
	s.filters = append([]nostr.Filter(nil), filters...)
	return &fakeSBOMAvailabilitySubscription{parent: s}, nil
}

func (s *fakeSBOMAvailabilitySubscriber) AuthenticateRelay(_ context.Context, relayURL string) error {
	s.authenticatedRelays = append(s.authenticatedRelays, relayURL)
	return nil
}

func (s *fakeSBOMAvailabilitySubscription) Next(_ context.Context) (SBOMAvailabilitySubscriptionMessage, bool, error) {
	if s.idx >= len(s.parent.messages) {
		return SBOMAvailabilitySubscriptionMessage{}, false, nil
	}
	msg := s.parent.messages[s.idx]
	s.idx++
	return msg, true, nil
}

func (s *fakeSBOMAvailabilitySubscription) Close() {
	s.parent.closed = true
}

const remotePayloadSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func publisherPubkeyFromSecret(t *testing.T, publisher *fakeSBOMPublisher) string {
	t.Helper()
	if publisher.signer.Hex() == strings.Repeat("0", 64) {
		publisher.signer = nostr.Generate()
	}
	return publisher.signer.Public().Hex()
}

func remoteAvailabilityEntry(subject domain.SBOMSubject) domain.SBOMIndexEntry {
	refD := "sbom:ref:" + sbomadapter.SubjectKey(subject) + ":cyclonedx:" + remotePayloadSHA
	return domain.SBOMIndexEntry{
		SubjectDigest: subject.Digest,
		ReferenceDTag: refD,
		Format:        domain.SBOMFormatCycloneDX,
		LocationURI:   "https://blossom.example.com/" + remotePayloadSHA,
		StorageType:   domain.SBOMStorageBlossom,
		PayloadSHA256: remotePayloadSHA,
		GeneratorID:   "cdxgen",
		Timestamp:     time.Unix(1710000000, 0).UTC(),
	}
}

func signedAvailabilityEvent(t *testing.T, subject domain.SBOMSubject, publisherPubkey string, signer nostr.SecretKey, entries []domain.SBOMIndexEntry) *nostr.Event {
	t.Helper()
	createdAt := time.Now().UTC()
	ev, _, err := sbomadapter.BuildSBOMAvailabilityListEvent(sbomadapter.BuildSBOMAvailabilityListEventInput{Subject: subject, Entries: entries, PublisherPubkey: publisherPubkey, CreatedAt: &createdAt})
	if err != nil {
		t.Fatal(err)
	}
	if err := ev.Sign(signer); err != nil {
		t.Fatal(err)
	}
	return ev
}

func parsePublishedAvailability(t *testing.T, publisher *fakeSBOMPublisher) domain.SBOMIndex {
	t.Helper()
	for i := len(publisher.events) - 1; i >= 0; i-- {
		if int(publisher.events[i].Kind) != sbomadapter.KindSBOMAvailabilityList {
			continue
		}
		var idx domain.SBOMIndex
		if err := json.Unmarshal([]byte(publisher.events[i].Content), &idx); err != nil {
			t.Fatal(err)
		}
		return idx
	}
	t.Fatal("no availability list published")
	return domain.SBOMIndex{}
}

func availabilityContainsPayload(index domain.SBOMIndex, payloadSHA string) bool {
	for _, entry := range index.Entries {
		if entry.PayloadSHA256 == payloadSHA {
			return true
		}
	}
	return false
}

func (p *fakeSBOMPublisher) PublishSignedEventWithResults(_ context.Context, ev *nostr.Event) ([]sbomadapter.PublishOKResult, error) {
	if err := ev.Sign(p.signer); err != nil {
		return nil, err
	}
	p.events = append(p.events, *ev)
	return p.results, nil
}

func (p *fakeSBOMPublisher) containsKind(kind int) bool {
	return p.firstKindIndex(kind) >= 0
}

func (p *fakeSBOMPublisher) firstKindIndex(kind int) int {
	for i, ev := range p.events {
		if int(ev.Kind) == kind {
			return i
		}
	}
	return -1
}

func assertPublishedKinds(t *testing.T, publisher *fakeSBOMPublisher, kinds []int) {
	t.Helper()
	if len(publisher.events) != len(kinds) {
		t.Fatalf("published %d events, want %d", len(publisher.events), len(kinds))
	}
	for i, want := range kinds {
		if int(publisher.events[i].Kind) != want {
			t.Fatalf("event %d kind = %d, want %d", i, publisher.events[i].Kind, want)
		}
	}
}

type fakeSBOMManifestRepo struct {
	projected          []domain.SBOMManifest
	packages           []domain.SBOMManifestPackage
	eventCount         func() int
	projectEventCounts []int
}

func newFakeSBOMManifestRepo() *fakeSBOMManifestRepo { return &fakeSBOMManifestRepo{} }
func (r *fakeSBOMManifestRepo) CreateManifest(context.Context, *domain.SBOMManifest) error {
	return nil
}
func (r *fakeSBOMManifestRepo) ProjectManifest(_ context.Context, manifest *domain.SBOMManifest, packages []domain.SBOMManifestPackage) error {
	if r.eventCount != nil {
		r.projectEventCounts = append(r.projectEventCounts, r.eventCount())
	}
	r.projected = append(r.projected, *manifest)
	r.packages = append(r.packages, packages...)
	return nil
}
func (r *fakeSBOMManifestRepo) UpdateCompatibilityVulnerabilityCounts(context.Context, uuid.UUID, string, domain.SecuritySeverityCounts, int) error {
	return nil
}
func (r *fakeSBOMManifestRepo) GetManifestByID(_ context.Context, id uuid.UUID) (*domain.SBOMManifest, error) {
	return nil, nil
}
func (r *fakeSBOMManifestRepo) ListManifestsBySubject(_ context.Context, subject domain.SBOMSubject, limit int) ([]domain.SBOMManifest, error) {
	return append([]domain.SBOMManifest(nil), r.projected...), nil
}
func (r *fakeSBOMManifestRepo) ListPublishedManifests(context.Context, int) ([]domain.SBOMManifest, error) {
	out := make([]domain.SBOMManifest, 0, len(r.projected))
	for _, manifest := range r.projected {
		if manifest.PublishState == domain.SBOMPublishPublished {
			out = append(out, manifest)
		}
	}
	return out, nil
}
func (r *fakeSBOMManifestRepo) UpdateManifestPublishState(_ context.Context, id uuid.UUID, state domain.SBOMPublishState, referenceEventID, availabilityEventID, publishError string) error {
	return nil
}
func (r *fakeSBOMManifestRepo) CreateManifestPackages(context.Context, []domain.SBOMManifestPackage) error {
	return nil
}
func (r *fakeSBOMManifestRepo) ListPackagesByManifest(context.Context, uuid.UUID) ([]domain.SBOMManifestPackage, error) {
	return nil, nil
}
func (r *fakeSBOMManifestRepo) SearchManifestPackagesByName(context.Context, string, int) ([]domain.SBOMManifestPackage, error) {
	return nil, nil
}

type fakeArtifactRepo struct{ artifact *domain.Artifact }

func (r fakeArtifactRepo) Create(context.Context, *domain.Artifact) error { return nil }
func (r fakeArtifactRepo) GetByID(context.Context, uuid.UUID) (*domain.Artifact, error) {
	return r.artifact, nil
}
func (r fakeArtifactRepo) GetByDigest(context.Context, string, string) (*domain.Artifact, error) {
	return nil, nil
}
func (r fakeArtifactRepo) GetByImageRepoDigest(context.Context, string, string) (*domain.Artifact, error) {
	return nil, nil
}
func (r fakeArtifactRepo) ListByService(context.Context, uuid.UUID, int, int) ([]domain.Artifact, error) {
	return nil, nil
}
func (r fakeArtifactRepo) ListByBuild(context.Context, uuid.UUID) ([]domain.Artifact, error) {
	return nil, nil
}

type fakeDeploymentRepo struct{ intent *domain.DeploymentIntent }

func (r fakeDeploymentRepo) Create(context.Context, *domain.DeploymentIntent) error { return nil }
func (r fakeDeploymentRepo) GetByID(context.Context, uuid.UUID) (*domain.DeploymentIntent, error) {
	return r.intent, nil
}
func (r fakeDeploymentRepo) GetByHiveResultEventID(context.Context, string) (*domain.DeploymentIntent, error) {
	return nil, nil
}
func (r fakeDeploymentRepo) ListByServiceEnv(context.Context, uuid.UUID, uuid.UUID, int, int) ([]domain.DeploymentIntent, error) {
	return nil, nil
}
func (r fakeDeploymentRepo) UpdateStatus(context.Context, uuid.UUID, domain.DeploymentIntentStatus) error {
	return nil
}
func (r fakeDeploymentRepo) UpdateApproval(context.Context, uuid.UUID, domain.ApprovalStatus) error {
	return nil
}
func (r fakeDeploymentRepo) UpdateDesiredState(context.Context, uuid.UUID, *domain.DesiredServiceSpec, string) error {
	return nil
}

type fakePackageRepo struct{ artifact *domain.PackageArtifact }

func (r fakePackageRepo) UpsertRepository(context.Context, *domain.PackageRepository) error {
	return nil
}
func (r fakePackageRepo) GetRepository(context.Context, uuid.UUID) (*domain.PackageRepository, error) {
	return nil, nil
}
func (r fakePackageRepo) GetRepositoryByName(context.Context, string) (*domain.PackageRepository, error) {
	return nil, nil
}
func (r fakePackageRepo) ListRepositories(context.Context, bool) ([]domain.PackageRepository, error) {
	return nil, nil
}
func (r fakePackageRepo) UpsertArtifact(context.Context, *domain.PackageArtifact) error { return nil }
func (r fakePackageRepo) GetArtifact(_ context.Context, repositoryID uuid.UUID, namespace, packageName, version, filename string) (*domain.PackageArtifact, error) {
	if r.artifact == nil || r.artifact.RepositoryID != repositoryID || r.artifact.Namespace != namespace || r.artifact.PackageName != packageName || r.artifact.Version != version || r.artifact.Filename != filename {
		return nil, nil
	}
	return r.artifact, nil
}
func (r fakePackageRepo) ListArtifacts(context.Context, uuid.UUID, int, int) ([]domain.PackageArtifact, error) {
	return nil, nil
}
func (r fakePackageRepo) UpsertPublication(context.Context, *domain.PackagePublication) error {
	return nil
}
func (r fakePackageRepo) GetPublication(context.Context, uuid.UUID) (*domain.PackagePublication, error) {
	return nil, nil
}
func (r fakePackageRepo) ListPublicationsByArtifact(context.Context, uuid.UUID) ([]domain.PackagePublication, error) {
	return nil, nil
}
func (r fakePackageRepo) ListPublicationsByRepository(context.Context, uuid.UUID, bool) ([]domain.PackagePublication, error) {
	return nil, nil
}
func (r fakePackageRepo) UpsertIntent(context.Context, *domain.PackageIntent) error { return nil }
func (r fakePackageRepo) GetIntent(context.Context, uuid.UUID) (*domain.PackageIntent, error) {
	return nil, nil
}
func (r fakePackageRepo) GetIntentByRequestEventID(context.Context, string) (*domain.PackageIntent, error) {
	return nil, nil
}
func (r fakePackageRepo) ListNonTerminalIntents(context.Context, int) ([]domain.PackageIntent, error) {
	return nil, nil
}

func testSPDXPayload(t *testing.T) []byte {
	t.Helper()
	return []byte(`{"spdxVersion":"SPDX-2.3","dataLicense":"CC0-1.0","SPDXID":"SPDXRef-DOCUMENT","name":"fixture","documentNamespace":"https://bahia.test/spdx/fixture","creationInfo":{"created":"2026-06-13T00:00:00Z","creators":["Tool: test"]},"packages":[{"name":"pkg","SPDXID":"SPDXRef-Package-pkg","versionInfo":"1.0.0","supplier":"Organization: Acme","licenseConcluded":"MIT","externalRefs":[{"referenceCategory":"PACKAGE-MANAGER","referenceType":"purl","referenceLocator":"pkg:generic/pkg@1.0.0"}]}],"relationships":[{"spdxElementId":"SPDXRef-DOCUMENT","relationshipType":"DESCRIBES","relatedSpdxElement":"SPDXRef-Package-pkg"}]}`)
}
