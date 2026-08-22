package hiveci

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	objects     map[string]ResolvedReleaseArtifact
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
func (f *releaseEvidenceFake) ResolveArtifact(_ context.Context, artifact domain.HiveCIReleaseArtifact) (ResolvedReleaseArtifact, error) {
	if artifact.Digest == f.unavailable {
		return ResolvedReleaseArtifact{}, nil
	}
	return f.objects[artifact.Digest], nil
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
	manifestContent := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"layers":[{"digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}]}`)
	sbomContent := []byte(`{"bomFormat":"CycloneDX","specVersion":"1.5"}`)
	manifest := releaseArtifactDescriptor(repositoryName, ociImageManifestMediaType, manifestContent)
	sbom := releaseArtifactDescriptor(repositoryName, cycloneDXJSONMediaType, sbomContent)
	identity, err := releaseIdentity(lineage)
	if err != nil {
		t.Fatal(err)
	}
	provenanceContent, err := json.Marshal(map[string]any{
		"_type":         inTotoStatementType,
		"predicateType": releaseProvenanceType,
		"subject":       []any{map[string]any{"name": repositoryName, "digest": map[string]string{"sha256": strings.TrimPrefix(manifest.Digest, "sha256:")}}},
		"predicate":     map[string]any{"release_identity": identity, "lineage": lineage, "execution": execution, "sbom_digest": sbom.Digest},
	})
	if err != nil {
		t.Fatal(err)
	}
	provenance := releaseArtifactDescriptor(repositoryName, inTotoJSONMediaType, provenanceContent)
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
	f.evidence = &releaseEvidenceFake{run: f.run, policies: []domain.HiveCIPipelinePolicy{policy}, admitted: true,
		objects: map[string]ResolvedReleaseArtifact{
			manifest.Digest:   {Content: manifestContent, MediaType: manifest.MediaType, Size: manifest.Size},
			sbom.Digest:       {Content: sbomContent, MediaType: sbom.MediaType, Size: sbom.Size},
			provenance.Digest: {Content: provenanceContent, MediaType: provenance.MediaType, Size: provenance.Size},
		},
	}
	f.ingestor = NewReleaseIngestor(f.evidence, f.store,
		[]string{f.attestor.Public().Hex()}, []string{f.issuer.Public().Hex()})
	f.ingestor.now = func() time.Time { return f.now }
	return f
}

func releaseArtifactDescriptor(repository, mediaType string, content []byte) domain.HiveCIReleaseArtifact {
	sum := sha256.Sum256(content)
	return domain.HiveCIReleaseArtifact{
		Repository: repository, MediaType: mediaType, Size: int64(len(content)),
		Digest: "sha256:" + hex.EncodeToString(sum[:]),
	}
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

func TestReleaseIngestorRejectsUnverifiedSupplyChainBytes(t *testing.T) {
	tests := []struct {
		name   string
		want   error
		mutate func(*releaseFixture)
	}{
		{name: "missing manifest", want: ErrReleaseArtifactUnavailable, mutate: func(f *releaseFixture) {
			f.evidence.unavailable = f.result.Manifest.Digest
		}},
		{name: "missing SBOM", want: ErrReleaseArtifactUnavailable, mutate: func(f *releaseFixture) {
			f.evidence.unavailable = f.result.SBOM.Digest
		}},
		{name: "missing provenance", want: ErrReleaseArtifactUnavailable, mutate: func(f *releaseFixture) {
			f.evidence.unavailable = f.result.Provenance.Digest
		}},
		{name: "manifest digest mismatch", mutate: func(f *releaseFixture) {
			object := f.evidence.objects[f.result.Manifest.Digest]
			object.Content = append([]byte(nil), object.Content...)
			object.Content[0] ^= 1
			f.evidence.objects[f.result.Manifest.Digest] = object
		}},
		{name: "SBOM size mismatch", mutate: func(f *releaseFixture) {
			object := f.evidence.objects[f.result.SBOM.Digest]
			object.Size++
			f.evidence.objects[f.result.SBOM.Digest] = object
		}},
		{name: "provenance media type mismatch", mutate: func(f *releaseFixture) {
			object := f.evidence.objects[f.result.Provenance.Digest]
			object.MediaType = "application/json"
			f.evidence.objects[f.result.Provenance.Digest] = object
		}},
		{name: "invalid OCI structure", mutate: func(f *releaseFixture) {
			content := []byte(`{"schemaVersion":1}`)
			replaceResolvedContent(f, &f.result.Manifest, content)
		}},
		{name: "invalid SBOM structure", mutate: func(f *releaseFixture) {
			content := []byte(`{"bomFormat":"unknown"}`)
			replaceResolvedContent(f, &f.result.SBOM, content)
		}},
		{name: "provenance subject mismatch", mutate: func(f *releaseFixture) {
			content := []byte(`{"_type":"https://in-toto.io/Statement/v1","predicateType":"https://sharegap.net/hiveci/release-provenance/v1","subject":[],"predicate":{}}`)
			replaceResolvedContent(f, &f.result.Provenance, content)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newReleaseFixture(t)
			test.mutate(f)
			f.result.ArtifactAttestation.Subjects = []domain.HiveCIReleaseArtifact{f.result.Manifest, f.result.SBOM, f.result.Provenance}
			f.event = releaseEventFromResult(t, f.result, f.attestor, f.now)
			want := test.want
			if want == nil {
				want = ErrInvalidRelease
			}
			if _, err := f.ingestor.Ingest(context.Background(), f.event); !errors.Is(err, want) {
				t.Fatalf("error=%v, want %v", err, want)
			}
			if f.store.commits != 0 {
				t.Fatal("unverified supply-chain bytes reached durable store")
			}
		})
	}
}

func replaceResolvedContent(f *releaseFixture, descriptor *domain.HiveCIReleaseArtifact, content []byte) {
	oldDigest := descriptor.Digest
	*descriptor = releaseArtifactDescriptor(descriptor.Repository, descriptor.MediaType, content)
	delete(f.evidence.objects, oldDigest)
	f.evidence.objects[descriptor.Digest] = ResolvedReleaseArtifact{
		Content: content, MediaType: descriptor.MediaType, Size: descriptor.Size,
	}
}

func TestSubscriberDispatchesAcceptedReleaseAndExactReplay(t *testing.T) {
	f := newReleaseFixture(t)
	calls := 0
	subscriber := &Subscriber{logger: zap.NewNop(), releases: f.ingestor,
		onRelease: func(_ context.Context, commit domain.HiveCIReleaseCommitResult) {
			calls++
			if commit.Release.Result.Manifest.Digest != f.result.Manifest.Digest {
				t.Fatalf("subscriber changed accepted digest: %+v", commit.Release)
			}
		}}
	subscriber.handleWorkflowResult(context.Background(), f.event)
	subscriber.handleWorkflowResult(context.Background(), f.event)
	if calls != 2 || f.store.commits != 1 {
		t.Fatalf("release callback calls=%d durable commits=%d, want two decisions and one commit", calls, f.store.commits)
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
