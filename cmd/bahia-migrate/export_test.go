package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"
)

func TestLegacyExportProductionShapedDraftsAndSchemas(t *testing.T) {
	org, serviceID, envID := uuid.New(), uuid.New(), uuid.New()
	adoptions := make([]exportAdoption, 47)
	for i := range adoptions {
		adoptions[i] = exportAdoption{ID: uuid.New(), OrgID: org, ServiceID: serviceID, EnvironmentID: envID,
			ContainerID: fmt.Sprintf("container-%02d", i), ServiceName: "api", EnvironmentName: "production"}
	}
	policies := make([]exportPolicy, 7)
	for i := range policies {
		policies[i] = exportPolicy{ID: uuid.New(), RepoCoordinate: fmt.Sprintf("repo:%d", i), WorkflowPath: "ci.yaml",
			ServiceID: serviceID, EnvironmentID: envID, Enabled: true}
	}
	targets := make([]exportSecurityTarget, 28)
	for i := range targets {
		targets[i] = exportSecurityTarget{ID: uuid.New(), TargetType: "package", TargetKeyHash: fmt.Sprintf("%064x", i),
			Package:  json.RawMessage(`{"ecosystem":"Go","name":"example.org/api","version":"v1"}`),
			Metadata: json.RawMessage(`{"api_token":"must-not-export"}`)}
	}
	index, files, err := draftLegacyExport(adoptions, policies, targets, nil)
	require.NoError(t, err)
	require.Len(t, index.Rows, 82)
	require.Len(t, files, 36)
	require.Contains(t, index.SecurityDisposition, "no target-registration intent")
	compiler := jsonschema.NewCompiler()
	schemaBytes, err := os.ReadFile("../../schemas/hiveci_pipeline_policy_intent.json")
	require.NoError(t, err)
	var schemaDoc any
	require.NoError(t, json.Unmarshal(schemaBytes, &schemaDoc))
	require.NoError(t, compiler.AddResource("https://bahia.dev/schemas/hiveci_pipeline_policy_intent.json", schemaDoc))
	compiled, err := compiler.Compile("https://bahia.dev/schemas/hiveci_pipeline_policy_intent.json")
	require.NoError(t, err)
	seenIDs := map[string]bool{}
	adoptionSink := &exportAdoptionSink{}
	for name, raw := range files {
		if name == "adoption-"+org.String()+".json" {
			draft := raw.(unsignedDraft)
			parsed := parseDraft(t, draft)
			require.Equal(t, "adoption", parsed.Domain)
			require.Equal(t, "adoption:"+org.String(), parsed.Coordinate)
			require.Len(t, parsed.Content["selections"], 47)
			require.Len(t, parsed.Content["targets"], 47)
			var mintedServiceID, mintedEnvironmentID string
			for _, item := range parsed.Content["selections"].([]any) {
				id := item.(map[string]any)["service_id"].(string)
				require.Equal(t, 7, int(uuid.MustParse(id).Version()))
				require.NotEqual(t, serviceID.String(), id)
				if mintedServiceID == "" {
					mintedServiceID = id
				} else {
					require.Equal(t, mintedServiceID, id)
				}
			}
			for _, item := range parsed.Content["targets"].([]any) {
				id := item.(map[string]any)["environment_id"].(string)
				require.Equal(t, 7, int(uuid.MustParse(id).Version()))
				require.NotEqual(t, envID.String(), id)
				if mintedEnvironmentID == "" {
					mintedEnvironmentID = id
				} else {
					require.Equal(t, mintedEnvironmentID, id)
				}
			}
			require.NoError(t, controlplane.NewAdoptionIntentHandler(adoptionSink, nil).HandleIntent(context.Background(), parsed))
			require.Equal(t, uuid.MustParse(mintedServiceID), adoptionSink.last.Selections[0].ServiceID)
			require.Equal(t, uuid.MustParse(mintedEnvironmentID), adoptionSink.last.Targets[0].EnvironmentID)
			require.Len(t, adoptionSink.last.Selections, 47)
			continue
		}
		if draft, ok := raw.(unsignedDraft); ok {
			parsed := parseDraft(t, draft)
			require.Equal(t, "hiveci-policy", parsed.Domain)
			require.NoError(t, compiled.Validate(parsed.Content))
			id := parsed.Content["id"].(string)
			require.Equal(t, 7, int(uuid.MustParse(id).Version()))
			require.False(t, seenIDs[id])
			seenIDs[id] = true
			continue
		}
		input := raw.(map[string]any)["target"]
		encoded, err := json.Marshal(input)
		require.NoError(t, err)
		var target service.SecurityScanTargetInput
		require.NoError(t, json.Unmarshal(encoded, &target))
		require.NotEmpty(t, target.Package.Ecosystem)
	}
	encoded, err := json.Marshal(files)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "must-not-export")
	dir := filepath.Join(t.TempDir(), "review")
	require.NoError(t, writeLegacyExport(dir, files, index))
	info, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
	_, err = os.Stat(filepath.Join(dir, "index.json"))
	require.NoError(t, err)
	require.Error(t, writeLegacyExport(dir, files, index), "existing output is never overwritten")
}

type exportAdoptionSink struct{ last service.AdoptionImportRequest }

func (*exportAdoptionSink) Scan(context.Context, service.AdoptionScanRequest) ([]service.AdoptionPreview, error) {
	return nil, nil
}
func (s *exportAdoptionSink) Import(_ context.Context, req service.AdoptionImportRequest) ([]service.AdoptionImportResult, error) {
	s.last = req
	return nil, nil
}

func parseDraft(t *testing.T, draft unsignedDraft) *controlplane.Intent {
	t.Helper()
	content, err := json.Marshal(draft.Content)
	require.NoError(t, err)
	tags := make(nostr.Tags, 0, len(draft.Tags))
	for _, tag := range draft.Tags {
		tags = append(tags, nostr.Tag(tag))
	}
	event := &nostr.Event{Kind: nostr.Kind(draft.Kind), Tags: tags, Content: string(content)}
	parsed, err := controlplane.ParseIntent(event)
	require.NoError(t, err)
	return parsed
}

func TestLegacyExportConflictsAndUnsafeRowsNeverBecomeDrafts(t *testing.T) {
	org, serviceID, envID := uuid.New(), uuid.New(), uuid.New()
	adoption := exportAdoption{ID: uuid.New(), OrgID: org, ServiceID: serviceID, EnvironmentID: envID, ContainerID: "live"}
	security := exportSecurityTarget{ID: uuid.New(), TargetType: "commit", TargetKeyHash: fmt.Sprintf("%064x", 1), CommitHash: "deadbeef"}
	heads := map[string]string{"adoption:binding:" + serviceID.String() + ":" + envID.String(): "event-a", "security:target:" + security.TargetKeyHash: "event-b"}
	index, files, err := draftLegacyExport([]exportAdoption{adoption}, nil, []exportSecurityTarget{security}, heads)
	require.NoError(t, err)
	require.Empty(t, files)
	require.Equal(t, "canonical_duplicate", index.Rows[0].Status)
	require.Equal(t, "canonical_duplicate", index.Rows[1].Status)
	index, files, err = draftLegacyExport(nil, nil, []exportSecurityTarget{security}, nil)
	require.NoError(t, err)
	require.Len(t, files, 1)
	encoded, _ := json.Marshal(files)
	require.NotContains(t, string(encoded), "secret")
	require.Equal(t, "needs_operator_review", index.Rows[0].Status)
}

func TestLegacyExportFlagScopeAndNoConfigSideEffects(t *testing.T) {
	var out, errs bytes.Buffer
	require.Equal(t, 1, run(context.Background(), []string{"export", "--export-dir", "relative"}, &out, &errs))
	require.Contains(t, errs.String(), "loading Bahia config")
	errs.Reset()
	require.Equal(t, 1, run(context.Background(), []string{"status", "--export-dir", "/tmp/no"}, &out, &errs))
	require.Contains(t, errs.String(), "only valid for export")
}
