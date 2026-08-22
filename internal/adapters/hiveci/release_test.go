package hiveci

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

type releaseEvidenceFake struct {
	run         *nostr.Event
	policies    []domain.HiveCIPipelinePolicy
	admitted    bool
	unavailable string
}

func (f *releaseEvidenceFake) GetWorkflowRunEvent(context.Context, string) (*nostr.Event, error) {
	return f.run, nil
}
func (f *releaseEvidenceFake) ListPipelinePolicies(context.Context) ([]domain.HiveCIPipelinePolicy, error) {
	return f.policies, nil
}
func (f *releaseEvidenceFake) IsWorkerAdmitted(context.Context, string, string) (bool, error) {
	return f.admitted, nil
}
func (f *releaseEvidenceFake) ArtifactAvailable(_ context.Context, artifact domain.HiveCIReleaseArtifact) (bool, error) {
	return artifact.Digest != f.unavailable, nil
}

type releaseStoreFake struct {
	byIdentity map[string]string
	commits    int
}

func (s *releaseStoreFake) CommitAcceptedRelease(_ context.Context, release domain.HiveCIAcceptedRelease) (domain.HiveCIReleaseCommitResult, error) {
	if s.byIdentity == nil {
		s.byIdentity = map[string]string{}
	}
	if digest, exists := s.byIdentity[release.Result.ReleaseIdentity]; exists {
		if digest == release.ContentDigest {
			return domain.HiveCIReleaseCommitResult{Release: release, Replay: true}, nil
		}
		return domain.HiveCIReleaseCommitResult{}, repository.ErrHiveCIReleaseReplayConflict
	}
	s.byIdentity[release.Result.ReleaseIdentity] = release.ContentDigest
	s.commits++
	return domain.HiveCIReleaseCommitResult{Release: release}, nil
}

type releaseFixture struct {
	now      time.Time
	issuer   nostr.SecretKey
	attestor nostr.SecretKey
	worker   nostr.SecretKey
	evidence *releaseEvidenceFake
	store    *releaseStoreFake
	run      *nostr.Event
	event    *nostr.Event
	result   domain.HiveCIReleaseResult
	ingestor *ReleaseIngestor
}

func newReleaseFixture(t *testing.T) *releaseFixture {
	t.Helper()
	f := &releaseFixture{
		now:    time.Unix(1_800_000_000, 0).UTC(),
		issuer: nostr.Generate(), attestor: nostr.Generate(), worker: nostr.Generate(),
		store: &releaseStoreFake{},
	}
	lineage := domain.HiveCIReleaseLineage{
		TriggerIdentity: strings.Repeat("1", 64), TriggerSource: "gitea",
		TriggerID: "delivery-42", PREventID: strings.Repeat("2", 64),
		ReviewEventID: strings.Repeat("3", 64), AuditEventID: strings.Repeat("4", 64),
		RepoAddress:         "30617:" + strings.Repeat("a", 64) + ":bahia",
		SourceRepoIdentity:  "gitea.example/team/bahia",
		SourceProvenanceRef: sourceProvenancePrefix + strings.Repeat("5", 64),
		Commit:              strings.Repeat("6", 40), Tree: strings.Repeat("7", 40),
		WorkflowDigest: strings.Repeat("8", 64),
	}
	execution := domain.HiveCIReleaseExecution{
		Complete: true, Status: "success", ExitCode: 0, DurationMS: 12000,
		BahiaDuration: "12", WorkerIdentity: f.worker.Public().Hex(),
		WorkerCapability:            "linux-amd64-release",
		BuildEnvironmentImageDigest: "sha256:" + strings.Repeat("9", 64),
		Tests:                       domain.HiveCIReleaseTestSummary{Status: "success", Total: 3, Passed: 3},
		DurableLogReference:         "cas://sha256/" + strings.Repeat("b", 64),
	}
	f.run = &nostr.Event{
		Kind: kinds.HiveCIWorkflowRun, CreatedAt: nostr.Timestamp(f.now.Add(-time.Minute).Unix()),
		Tags: nostr.Tags{
			{"a", lineage.RepoAddress}, {"repo-address", lineage.RepoAddress},
			{"commit", lineage.Commit}, {"branch", "main"}, {"workflow", ".gitea/workflows/release.yml"},
			{"trigger", "push"}, {"triggered-by", strings.Repeat("c", 64)},
			{"publisher", nostr.Generate().Public().Hex()}, {"t", "hive-ci"},
			{"pr", lineage.PREventID}, {"pr-event", lineage.PREventID},
			{"review", lineage.ReviewEventID}, {"audit", lineage.AuditEventID},
			{"tree", lineage.Tree}, {"workflow-digest", lineage.WorkflowDigest},
			{"source-provenance", lineage.SourceProvenanceRef}, {"source-repo", lineage.SourceRepoIdentity},
			{"idempotency", lineage.TriggerIdentity}, {"trigger-envelope", lineage.TriggerIdentity},
			{"trigger-source", lineage.TriggerSource}, {"trigger-id", lineage.TriggerID},
			{"worker", execution.WorkerIdentity}, {"worker-ad", strings.Repeat("d", 64)},
			{"worker-capability", execution.WorkerCapability},
			{"review-policy", "review-policy-v1"}, {"policy-digest", strings.Repeat("e", 64)},
		},
	}
	mustSignReleaseEvent(t, f.run, f.issuer)
	lineage.WorkflowRunEventID = f.run.ID.Hex()

	repositoryName := "harbor.example/team/bahia"
	manifest := domain.HiveCIReleaseArtifact{Repository: repositoryName,
		Digest: "sha256:" + strings.Repeat("1", 64), MediaType: ociImageManifestMediaType, Size: 321}
	sbom := domain.HiveCIReleaseArtifact{Repository: repositoryName,
		Digest: "sha256:" + strings.Repeat("2", 64), MediaType: cycloneDXJSONMediaType, Size: 654}
	provenance := domain.HiveCIReleaseArtifact{Repository: repositoryName,
		Digest: "sha256:" + strings.Repeat("3", 64), MediaType: inTotoJSONMediaType, Size: 987}
	identity, err := releaseIdentity(lineage)
	if err != nil {
		t.Fatal(err)
	}
	f.result = domain.HiveCIReleaseResult{
		SchemaVersion: domain.HiveCIReleaseSchemaV1, ResultType: domain.HiveCIReleaseResultType,
		Status: "success", ReleaseIdentity: identity, Lineage: lineage, Execution: execution,
		ImageTag: "release-20260821", Manifest: manifest, SBOM: sbom, Provenance: provenance,
		ArtifactAttestation: domain.HiveCISignetArtifactAttestation{
			Type: signetAttestationType, SignerPubkey: f.attestor.Public().Hex(),
			Subjects: []domain.HiveCIReleaseArtifact{manifest, sbom, provenance},
		},
	}
	f.event = releaseEventFromResult(t, f.result, f.attestor, f.now)
	policy := domain.HiveCIPipelinePolicy{
		ID: uuid.New(), RepoCoordinate: lineage.RepoAddress,
		WorkflowPath: ".gitea/workflows/release.yml", BranchPattern: "main",
		Enabled: true, Metadata: map[string]any{
			"workflow_digest":          lineage.WorkflowDigest,
			"policy_digest":            strings.Repeat("e", 64),
			"review_policy":            "review-policy-v1",
			"source_repo_identity":     lineage.SourceRepoIdentity,
			"release_image_repository": repositoryName,
			"release_attestors":        []any{f.attestor.Public().Hex()},
		},
	}
	f.evidence = &releaseEvidenceFake{run: f.run, policies: []domain.HiveCIPipelinePolicy{policy}, admitted: true}
	f.ingestor = NewReleaseIngestor(f.evidence, f.store,
		[]string{f.attestor.Public().Hex()}, []string{f.issuer.Public().Hex()})
	f.ingestor.now = func() time.Time { return f.now }
	return f
}

func releaseEventFromResult(t *testing.T, result domain.HiveCIReleaseResult, signer nostr.SecretKey, at time.Time) *nostr.Event {
	t.Helper()
	content, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	event := &nostr.Event{
		Kind: kinds.HiveCIWorkflowResult, CreatedAt: nostr.Timestamp(at.Unix()),
		Content: string(content),
		Tags: nostr.Tags{
			{"e", result.Lineage.WorkflowRunEventID}, {"status", "success"}, {"result", domain.HiveCIReleaseResultType},
			{"release", result.ReleaseIdentity}, {"trigger-envelope", result.Lineage.TriggerIdentity},
			{"trigger-source", result.Lineage.TriggerSource}, {"trigger-id", result.Lineage.TriggerID},
			{"pr", result.Lineage.PREventID}, {"review", result.Lineage.ReviewEventID},
			{"audit", result.Lineage.AuditEventID}, {"a", result.Lineage.RepoAddress},
			{"source-repo", result.Lineage.SourceRepoIdentity}, {"source-provenance", result.Lineage.SourceProvenanceRef},
			{"commit", result.Lineage.Commit}, {"tree", result.Lineage.Tree},
			{"workflow-digest", result.Lineage.WorkflowDigest}, {"worker", result.Execution.WorkerIdentity},
			{"worker-capability", result.Execution.WorkerCapability},
			{"build-image", result.Execution.BuildEnvironmentImageDigest},
			{"exit_code", "0"}, {"duration", result.Execution.BahiaDuration},
			{"log_url", result.Execution.DurableLogReference}, {"image_repo", result.Manifest.Repository},
			{"image_digest", result.Manifest.Digest}, {"sbom_digest", result.SBOM.Digest},
			{"provenance_digest", result.Provenance.Digest}, {"image_tag", result.ImageTag},
		},
	}
	mustSignReleaseEvent(t, event, signer)
	return event
}

func mustSignReleaseEvent(t *testing.T, event *nostr.Event, signer nostr.SecretKey) {
	t.Helper()
	event.ID, event.Sig = nostr.ID{}, [64]byte{}
	if err := event.Sign(signer); err != nil {
		t.Fatal(err)
	}
}

func replaceReleaseTag(t *testing.T, event *nostr.Event, key, value string, signer nostr.SecretKey) {
	t.Helper()
	for index := range event.Tags {
		if event.Tags[index][0] == key {
			event.Tags[index][1] = value
			mustSignReleaseEvent(t, event, signer)
			return
		}
	}
	t.Fatalf("tag %s not found", key)
}

func TestReleaseIngestorAcceptsProducerContractAndReplaysExactly(t *testing.T) {
	f := newReleaseFixture(t)
	commit, err := f.ingestor.Ingest(context.Background(), f.event)
	if err != nil {
		t.Fatal(err)
	}
	if commit.Replay || commit.Release.Result.Manifest.Digest != f.result.Manifest.Digest ||
		commit.Release.Result.ImageTag != f.result.ImageTag || f.store.commits != 1 {
		t.Fatalf("unexpected first commit: %+v store=%+v", commit, f.store)
	}
	replay, err := f.ingestor.Ingest(context.Background(), f.event)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Replay || f.store.commits != 1 || len(f.store.byIdentity) != 1 {
		t.Fatalf("exact replay was not idempotent: %+v store=%+v", replay, f.store)
	}
}

func TestReleaseIngestorRejectsReplayConflict(t *testing.T) {
	f := newReleaseFixture(t)
	if _, err := f.ingestor.Ingest(context.Background(), f.event); err != nil {
		t.Fatal(err)
	}
	conflictResult := f.result
	conflictResult.ImageTag = "release-conflict"
	conflict := releaseEventFromResult(t, conflictResult, f.attestor, f.now.Add(time.Second))
	if _, err := f.ingestor.Ingest(context.Background(), conflict); !errors.Is(err, repository.ErrHiveCIReleaseReplayConflict) {
		t.Fatalf("conflict error = %v", err)
	}
	if f.store.commits != 1 || len(f.store.byIdentity) != 1 {
		t.Fatalf("conflict mutated accepted state: %+v", f.store)
	}
}

func TestReleaseIngestorRejectsTrustLineagePolicyAndArtifactFailures(t *testing.T) {
	tests := []struct {
		name   string
		want   error
		mutate func(*testing.T, *releaseFixture)
	}{
		{name: "invalid signature", want: ErrInvalidRelease, mutate: func(_ *testing.T, f *releaseFixture) {
			f.event.Content += " "
		}},
		{name: "untrusted attestor", want: ErrUntrustedReleaseAttestor, mutate: func(_ *testing.T, f *releaseFixture) {
			f.ingestor.trustedAttestors = map[string]struct{}{}
		}},
		{name: "missing signed run", want: ErrReleaseEvidenceUnavailable, mutate: func(_ *testing.T, f *releaseFixture) {
			f.evidence.run = nil
		}},
		{name: "invalid run signature", want: ErrInvalidRelease, mutate: func(_ *testing.T, f *releaseFixture) {
			f.run.Content = "tampered"
		}},
		{name: "untrusted run issuer", want: ErrReleasePolicyDenied, mutate: func(_ *testing.T, f *releaseFixture) {
			f.ingestor.trustedIssuers = map[string]struct{}{}
		}},
		{name: "unknown worker", want: ErrReleaseWorkerNotAdmitted, mutate: func(_ *testing.T, f *releaseFixture) {
			f.evidence.admitted = false
		}},
		{name: "wrong repository policy", want: ErrReleasePolicyDenied, mutate: func(_ *testing.T, f *releaseFixture) {
			f.evidence.policies[0].RepoCoordinate = "30617:" + strings.Repeat("f", 64) + ":other"
		}},
		{name: "wrong workflow policy", want: ErrReleasePolicyDenied, mutate: func(_ *testing.T, f *releaseFixture) {
			f.evidence.policies[0].WorkflowPath = ".gitea/workflows/other.yml"
		}},
		{name: "wrong branch policy", want: ErrReleasePolicyDenied, mutate: func(_ *testing.T, f *releaseFixture) {
			f.evidence.policies[0].BranchPattern = "release/*"
		}},
		{name: "policy digest mismatch", want: ErrReleasePolicyDenied, mutate: func(_ *testing.T, f *releaseFixture) {
			f.evidence.policies[0].Metadata["policy_digest"] = strings.Repeat("0", 64)
		}},
		{name: "trigger lineage mismatch", want: ErrInvalidRelease, mutate: func(t *testing.T, f *releaseFixture) {
			replaceReleaseTag(t, f.run, "trigger-envelope", strings.Repeat("0", 64), f.issuer)
		}},
		{name: "review lineage mismatch", want: ErrInvalidRelease, mutate: func(t *testing.T, f *releaseFixture) {
			replaceReleaseTag(t, f.run, "review", strings.Repeat("0", 64), f.issuer)
		}},
		{name: "repository lineage mismatch", want: ErrInvalidRelease, mutate: func(t *testing.T, f *releaseFixture) {
			replaceReleaseTag(t, f.run, "a", "30617:"+strings.Repeat("f", 64)+":other", f.issuer)
		}},
		{name: "workflow digest lineage mismatch", want: ErrInvalidRelease, mutate: func(t *testing.T, f *releaseFixture) {
			replaceReleaseTag(t, f.run, "workflow-digest", strings.Repeat("0", 64), f.issuer)
		}},
		{name: "manifest missing", want: ErrInvalidRelease, mutate: func(t *testing.T, f *releaseFixture) {
			f.result.Manifest.Size = 0
			f.result.ArtifactAttestation.Subjects[0] = f.result.Manifest
			f.event = releaseEventFromResult(t, f.result, f.attestor, f.now)
		}},
		{name: "sbom missing", want: ErrInvalidRelease, mutate: func(t *testing.T, f *releaseFixture) {
			f.result.SBOM.Digest = ""
			f.result.ArtifactAttestation.Subjects[1] = f.result.SBOM
			f.event = releaseEventFromResult(t, f.result, f.attestor, f.now)
		}},
		{name: "provenance missing", want: ErrInvalidRelease, mutate: func(t *testing.T, f *releaseFixture) {
			f.result.Provenance.Size = 0
			f.result.ArtifactAttestation.Subjects[2] = f.result.Provenance
			f.event = releaseEventFromResult(t, f.result, f.attestor, f.now)
		}},
		{name: "descriptor unavailable", want: ErrReleaseArtifactUnavailable, mutate: func(_ *testing.T, f *releaseFixture) {
			f.evidence.unavailable = f.result.Manifest.Digest
		}},
		{name: "digest tag mismatch", want: ErrInvalidRelease, mutate: func(t *testing.T, f *releaseFixture) {
			replaceReleaseTag(t, f.event, "image_digest", "sha256:"+strings.Repeat("f", 64), f.attestor)
		}},
		{name: "attestation subjects mismatch", want: ErrInvalidRelease, mutate: func(t *testing.T, f *releaseFixture) {
			f.result.ArtifactAttestation.Subjects = f.result.ArtifactAttestation.Subjects[:2]
			f.event = releaseEventFromResult(t, f.result, f.attestor, f.now)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newReleaseFixture(t)
			test.mutate(t, f)
			if _, err := f.ingestor.Ingest(context.Background(), f.event); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if f.store.commits != 0 {
				t.Fatalf("rejected release reached durable store: %+v", f.store)
			}
		})
	}
}

func TestSubscriberDispatchesAcceptedReleaseExactlyOnce(t *testing.T) {
	f := newReleaseFixture(t)
	calls := 0
	subscriber := &Subscriber{logger: zap.NewNop(), releases: f.ingestor,
		onRelease: func(_ context.Context, release domain.HiveCIAcceptedRelease) {
			calls++
			if release.Result.Manifest.Digest != f.result.Manifest.Digest {
				t.Fatalf("subscriber changed accepted digest: %+v", release)
			}
		}}
	subscriber.handleWorkflowResult(context.Background(), f.event)
	subscriber.handleWorkflowResult(context.Background(), f.event)
	if calls != 1 || f.store.commits != 1 {
		t.Fatalf("release callback calls=%d durable commits=%d, want one each", calls, f.store.commits)
	}
}

func TestReleaseIngestorRequiresTagAndContentReleaseMarkers(t *testing.T) {
	t.Run("ordinary result is not consumed", func(t *testing.T) {
		f := newReleaseFixture(t)
		ordinary := &nostr.Event{Kind: kinds.HiveCIWorkflowResult,
			CreatedAt: nostr.Timestamp(f.now.Unix()), Tags: nostr.Tags{{"status", "success"}},
			Content: `{"status":"success"}`}
		mustSignReleaseEvent(t, ordinary, f.attestor)
		if IsReleaseCandidate(ordinary) {
			t.Fatal("ordinary result classified as release")
		}
		if _, err := f.ingestor.Ingest(context.Background(), ordinary); !errors.Is(err, ErrNotRelease) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("tag only fails closed", func(t *testing.T) {
		f := newReleaseFixture(t)
		f.event.Content = `{"result_type":"BUILD"}`
		mustSignReleaseEvent(t, f.event, f.attestor)
		if _, err := f.ingestor.Ingest(context.Background(), f.event); !errors.Is(err, ErrInvalidRelease) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("content only fails closed", func(t *testing.T) {
		f := newReleaseFixture(t)
		filtered := make(nostr.Tags, 0, len(f.event.Tags))
		for _, tag := range f.event.Tags {
			if tag[0] != "result" {
				filtered = append(filtered, tag)
			}
		}
		f.event.Tags = filtered
		mustSignReleaseEvent(t, f.event, f.attestor)
		if _, err := f.ingestor.Ingest(context.Background(), f.event); !errors.Is(err, ErrInvalidRelease) {
			t.Fatalf("error = %v", err)
		}
	})
}
