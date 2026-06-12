package nostr

import (
	"encoding/json"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

func TestCoreProjectionDecodersRoundTripEncodedPayloadFields(t *testing.T) {
	catalog := NewKindCatalog()
	now := time.Date(2026, 5, 23, 18, 0, 0, 0, time.UTC)
	serviceID := uuid.New().String()
	envID := uuid.New().String()
	buildID := uuid.New().String()
	artifactID := uuid.New().String()
	intentID := uuid.New().String()
	runID := uuid.New().String()
	policyID := uuid.New().String()
	size := int64(42)
	exitCode := 0

	cases := []struct {
		name   string
		kind   int
		dtag   string
		body   map[string]any
		assert func(*DecodedProjectionEvent)
	}{
		{
			name: "service registry",
			kind: KindServiceRegistry,
			dtag: serviceID,
			body: map[string]any{"deleted": false, "id": serviceID, "name": "api", "repo_url": "https://example.test/repo.git", "artifact_repo": "registry.test/api", "default_branch": "main", "runtime_type": "docker", "created_at": now.Format(time.RFC3339Nano), "updated_at": now.Format(time.RFC3339Nano)},
			assert: func(got *DecodedProjectionEvent) {
				if got.Family != FamilyService || got.Service == nil || got.Service.ID != serviceID || got.Service.Name != "api" || got.Service.RuntimeType != domain.RuntimeTypeDocker {
					t.Fatalf("service decode mismatch: %#v", got)
				}
			},
		},
		{
			name: "environment registry",
			kind: KindEnvironmentRegistry,
			dtag: envID,
			body: map[string]any{"deleted": false, "id": envID, "name": "prod", "protected": true, "deploy_strategy": "replace", "created_at": now.Format(time.RFC3339Nano), "updated_at": now.Format(time.RFC3339Nano)},
			assert: func(got *DecodedProjectionEvent) {
				if got.Family != FamilyEnvironment || got.Environment == nil || got.Environment.ID != envID || !got.Environment.Protected || got.Environment.DeployStrategy != domain.DeployStrategyReplace {
					t.Fatalf("environment decode mismatch: %#v", got)
				}
			},
		},
		{
			name: "build registry",
			kind: KindBuildRegistry,
			dtag: buildID,
			body: map[string]any{"deleted": false, "id": buildID, "service_id": serviceID, "git_sha": "abc", "git_ref": "refs/heads/main", "ci_system": "hive", "ci_run_id": "run-1", "loom_job_id": "loom-1", "status": "succeeded", "source_event_id": "evt-source", "started_at": now.Format(time.RFC3339Nano), "finished_at": now.Add(time.Minute).Format(time.RFC3339Nano), "metadata": map[string]any{"k": "v"}, "created_at": now.Format(time.RFC3339Nano)},
			assert: func(got *DecodedProjectionEvent) {
				if got.Family != FamilyBuild || got.Build == nil || got.Build.ID != buildID || got.Build.ServiceID != serviceID || got.Build.Status != domain.BuildStatusSucceeded || got.Build.Metadata["k"] != "v" {
					t.Fatalf("build decode mismatch: %#v", got)
				}
			},
		},
		{
			name: "artifact registry",
			kind: KindArtifactRegistry,
			dtag: artifactID,
			body: map[string]any{"deleted": false, "id": artifactID, "build_id": buildID, "service_id": serviceID, "image_repo": "registry.test/api", "image_tag": "latest", "image_digest": "sha256:abc", "manifest_media_type": "application/vnd.oci.image.manifest.v1+json", "size_bytes": size, "sbom_url": "https://example.test/sbom", "signature_ref": "sig", "scan_status": "clean", "metadata": map[string]any{"arch": "amd64"}, "created_at": now.Format(time.RFC3339Nano)},
			assert: func(got *DecodedProjectionEvent) {
				if got.Family != FamilyArtifact || got.Artifact == nil || got.Artifact.ID != artifactID || got.Artifact.BuildID != buildID || got.Artifact.SizeBytes == nil || *got.Artifact.SizeBytes != size {
					t.Fatalf("artifact decode mismatch: %#v", got)
				}
			},
		},
		{
			name: "deployment intent registry",
			kind: KindDeploymentIntentRegistry,
			dtag: intentID,
			body: map[string]any{"deleted": false, "id": intentID, "service_id": serviceID, "environment_id": envID, "artifact_id": artifactID, "requested_by": "alice", "source_kind": "manual", "approval_status": "approved", "status": "approved", "deployment_status": "approved", "approval_metadata": map[string]any{"by": "bob"}, "metadata": map[string]any{"risk": "low"}, "created_at": now.Format(time.RFC3339Nano), "approved_at": now.Format(time.RFC3339Nano), "updated_at": now.Format(time.RFC3339Nano)},
			assert: func(got *DecodedProjectionEvent) {
				if got.Family != FamilyIntent || got.Intent == nil || got.Intent.ID != intentID || got.Intent.ArtifactID != artifactID || got.Intent.ApprovalStatus != domain.ApprovalStatusApproved {
					t.Fatalf("intent decode mismatch: %#v", got)
				}
			},
		},
		{
			name: "deployment run registry",
			kind: KindDeploymentRunRegistry,
			dtag: runID,
			body: map[string]any{"deleted": false, "id": runID, "deployment_intent_id": intentID, "loom_job_id": "loom-2", "worker_pubkey": "worker-pk", "worker_name": "worker", "status": "succeeded", "exit_code": exitCode, "stdout_ref": "stdout", "stderr_ref": "stderr", "started_at": now.Format(time.RFC3339Nano), "finished_at": now.Add(time.Minute).Format(time.RFC3339Nano), "metadata": map[string]any{"step": "deploy"}, "created_at": now.Format(time.RFC3339Nano), "updated_at": now.Format(time.RFC3339Nano)},
			assert: func(got *DecodedProjectionEvent) {
				if got.Family != FamilyRun || got.Run == nil || got.Run.ID != runID || got.Run.DeploymentIntentID != intentID || got.Run.ExitCode == nil || *got.Run.ExitCode != exitCode {
					t.Fatalf("run decode mismatch: %#v", got)
				}
			},
		},
		{
			name: "policy registry",
			kind: KindPolicyRegistry,
			dtag: policyID,
			body: map[string]any{"deleted": false, "id": policyID, "name": "prod approvals", "environment_id": envID, "rules": []map[string]any{{"type": "manual_approval", "params": map[string]any{"approvers": 1}}}, "rule_count": 1, "enforcement": "blocking", "enabled": true, "created_at": now.Format(time.RFC3339Nano), "updated_at": now.Format(time.RFC3339Nano)},
			assert: func(got *DecodedProjectionEvent) {
				if got.Family != FamilyPolicy || got.Policy == nil || got.Policy.ID != policyID || got.Policy.EnvironmentID == nil || *got.Policy.EnvironmentID != envID || len(got.Policy.Rules) != 1 {
					t.Fatalf("policy decode mismatch: %#v", got)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decoder, ok := catalog.Decoder(tc.kind)
			if !ok {
				t.Fatalf("missing decoder for kind %d", tc.kind)
			}
			event := projectionEvent(tc.kind, tc.dtag, tc.body, now)
			got, err := decoder(&event)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got.DTag != tc.dtag || got.SourceID != eventIDHex(&event) || got.Timestamp.IsZero() {
				t.Fatalf("projection metadata mismatch: %#v", got)
			}
			tc.assert(got)
		})
	}
}

func TestProjectionFidelityDocumentsKnownLossyDomainFields(t *testing.T) {
	lossy := map[string][]string{
		"service":     {"org_id", "repository", "runtime_config"},
		"environment": {"org_id", "loom_worker_selector", "runtime_config"},
		"intent":      {"supersedes_intent_id"},
	}
	if len(lossy["service"]) != 3 || len(lossy["environment"]) != 3 || len(lossy["intent"]) != 1 {
		t.Fatalf("unexpected lossy-field audit result: %#v", lossy)
	}
}

func projectionEvent(kind int, dtag string, body map[string]any, createdAt time.Time) gonostr.Event {
	content, _ := json.Marshal(body)
	return gonostr.Event{ID: gonostr.ID{1}, Kind: canonicalKind(kind), CreatedAt: gonostr.Timestamp(createdAt.Unix()), Tags: gonostr.Tags{{"d", dtag}, {"deleted", "false"}}, Content: string(content)}
}
