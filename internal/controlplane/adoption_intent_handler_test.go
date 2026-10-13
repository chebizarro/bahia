package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/stretchr/testify/require"
)

// resumableAdoptionService fails its first import at a canonical publish, the
// way the adoption service reports an interrupted candidate, and completes on
// the next delivery of the same request.
type resumableAdoptionService struct {
	mu       sync.Mutex
	requests []service.AdoptionImportRequest
	failures int
}

func (s *resumableAdoptionService) Scan(context.Context, service.AdoptionScanRequest) ([]service.AdoptionPreview, error) {
	return nil, errors.New("not used")
}

func (s *resumableAdoptionService) Import(_ context.Context, req service.AdoptionImportRequest) ([]service.AdoptionImportResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, req)
	serviceID := uuid.New()
	if s.failures > 0 {
		s.failures--
		return []service.AdoptionImportResult{{TargetName: "prod", ContainerID: "container-1", ContainerName: "api", ServiceName: "api", ServiceID: &serviceID, Status: "failed", Incomplete: true, Step: "artifact", Error: "publish adoption artifact: relay rejected"}},
			errors.Join(service.ErrAdoptionIncomplete, errors.New("relay rejected"))
	}
	return []service.AdoptionImportResult{{TargetName: "prod", ContainerID: "container-1", ContainerName: "api", ServiceName: "api", ServiceID: &serviceID, Status: "created", Step: "complete"}}, nil
}

func TestAdoptionImportIntentIncompletePublishIsRejectedWithProgressAndResumes(t *testing.T) {
	adoption := &resumableAdoptionService{failures: 1}
	p, statuses := d76Processor(t, "adoption", testPubkey, NewAdoptionIntentHandler(adoption, []string{testPubkey}))
	intent := d70Intent("adoption", "import", "adoption:"+testOrgID().String(), testPubkey, map[string]any{
		"org_id":     testOrgID().String(),
		"targets":    []any{map[string]any{"name": "prod", "endpoint_ref": "docker-prod", "environment_id": uuid.Must(uuid.NewV7()).String()}},
		"selections": []any{map[string]any{"target_name": "prod", "container_id": "container-1", "service_id": uuid.Must(uuid.NewV7()).String()}},
	})

	err := p.ProcessInProcess(context.Background(), intent)
	require.ErrorIs(t, err, service.ErrAdoptionIncomplete, "a failed canonical publish is returned to the intent")
	require.False(t, p.IsProcessed(intent.IntentID), "an incomplete adoption must not be marked processed")
	require.Len(t, statuses.events, 1)
	require.Equal(t, "rejected", tagValueNostr(statuses.events[0].Tags, "status"))
	var status map[string]any
	require.NoError(t, json.Unmarshal([]byte(statuses.events[0].Content), &status))
	require.Contains(t, status["reason"], "relay rejected")
	data := status["data"].(map[string]any)
	require.EqualValues(t, 1, data["candidate_count"])
	require.EqualValues(t, 1, data["incomplete_count"])
	require.EqualValues(t, 0, data["imported_count"])
	progress := data["progress"].([]any)
	require.Len(t, progress, 1)
	require.Equal(t, "artifact", progress[0].(map[string]any)["step"])
	require.Equal(t, true, progress[0].(map[string]any)["incomplete"])
	require.Equal(t, intent.IntentID, adoption.requests[0].RequestID, "the intent id keys the resources minted per request")
	require.Equal(t, testPubkey, adoption.requests[0].Actor, "secret audit metadata names the signed operator")

	// Re-delivering the same signed intent resumes the adoption and is then
	// accepted with its progress; a third delivery is an idempotent replay.
	require.NoError(t, p.ProcessInProcess(context.Background(), intent))
	require.Len(t, adoption.requests, 2)
	require.Equal(t, adoption.requests[0].RequestID, adoption.requests[1].RequestID)
	require.Equal(t, adoption.requests[0].Targets[0].EnvironmentID, adoption.requests[1].Targets[0].EnvironmentID)
	require.Equal(t, adoption.requests[0].Selections[0].ServiceID, adoption.requests[1].Selections[0].ServiceID)
	require.True(t, p.IsProcessed(intent.IntentID))
	require.Equal(t, "accepted", tagValueNostr(statuses.events[1].Tags, "status"))
	require.NoError(t, json.Unmarshal([]byte(statuses.events[1].Content), &status))
	data = status["data"].(map[string]any)
	require.EqualValues(t, 1, data["imported_count"])
	require.EqualValues(t, 0, data["incomplete_count"])
	require.NoError(t, p.ProcessInProcess(context.Background(), intent))
	require.Len(t, adoption.requests, 2, "a processed intent is not imported again")
}

func TestAdoptionImportIntentRejectsMissingOrDerivedEntityIDs(t *testing.T) {
	adoption := &resumableAdoptionService{}
	handler := NewAdoptionIntentHandler(adoption, []string{testPubkey})
	base := func() *Intent {
		return d70Intent("adoption", "import", "adoption:"+testOrgID().String(), testPubkey, map[string]any{
			"org_id":     testOrgID().String(),
			"targets":    []any{map[string]any{"name": "prod", "endpoint_ref": "docker-prod", "environment_id": uuid.Must(uuid.NewV7()).String()}},
			"selections": []any{map[string]any{"target_name": "prod", "container_id": "container-1", "service_id": uuid.Must(uuid.NewV7()).String()}},
		})
	}
	for _, tc := range []struct{ name, field, value string }{
		{"missing service", "service_id", ""},
		{"derived service", "service_id", uuid.NewSHA1(uuid.NameSpaceOID, []byte("old-service")).String()},
		{"missing environment", "environment_id", ""},
		{"derived environment", "environment_id", uuid.NewSHA1(uuid.NameSpaceOID, []byte("old-env")).String()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			intent := base()
			if tc.field == "service_id" {
				intent.Content["selections"].([]any)[0].(map[string]any)[tc.field] = tc.value
			} else {
				intent.Content["targets"].([]any)[0].(map[string]any)[tc.field] = tc.value
			}
			require.ErrorContains(t, handler.HandleIntent(context.Background(), intent), "UUIDv7")
			require.Empty(t, adoption.requests)
		})
	}
	intent := base()
	intent.Content["import_all"] = true
	require.ErrorContains(t, handler.HandleIntent(context.Background(), intent), "explicit selections")
	require.Empty(t, adoption.requests)
}
