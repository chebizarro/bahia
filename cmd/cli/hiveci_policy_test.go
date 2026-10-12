package main

import (
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/pkg/client"
	"github.com/stretchr/testify/require"
)

func TestHiveCIPolicyCLIIntentEnvelope(t *testing.T) {
	id := uuid.Must(uuid.NewV7()).String()
	intentID := uuid.Must(uuid.NewV7()).String()
	command, _, err := newRootCommand().Find([]string{"hiveci-policy", "apply"})
	require.NoError(t, err)
	require.Equal(t, "apply", command.Name())
	content := map[string]any{"id": id, "intent_id": intentID, "deleted": true}
	request := hiveCIPolicyRequest("delete", id, intentID, content)
	event, err := (&client.IntentPublisher{}).BuildIntentEvent(request)
	require.NoError(t, err)
	parsed, err := controlplane.ParseIntent(&event)
	require.NoError(t, err)
	require.Equal(t, "hiveci-policy", parsed.Domain)
	require.Equal(t, "hiveci:policy:"+id, parsed.Coordinate)
	require.Equal(t, "bahia.intent.hiveci-policy.v1", parsed.Schema)
	require.Equal(t, intentID, parsed.IntentID)
	require.Equal(t, true, parsed.Content["deleted"])
	require.Equal(t, uuid.Nil, parsed.OrgID)
	for _, tag := range event.Tags {
		require.NotEqual(t, "org", tag[0])
	}
}

func TestHiveCIPolicyCLIRequiresAuthorMintedUUIDv7(t *testing.T) {
	_, err := hiveCIPolicyID(uuid.New().String())
	require.ErrorContains(t, err, "UUIDv7")
	id := uuid.Must(uuid.NewV7()).String()
	got, err := hiveCIPolicyID(id)
	require.NoError(t, err)
	require.Equal(t, id, got)
}
