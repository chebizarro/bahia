package nostr

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestDeploymentRunRegistryRecordAbsentUnitWireShape(t *testing.T) {
	run := &domain.DeploymentRun{
		ID: domain.NewEntityID(), Status: domain.RunStatusRunning,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	_, content := deploymentRunRegistryRecord(run, false)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(content), &payload))
	require.Equal(t, "", payload["deployment_unit_id"])
}
