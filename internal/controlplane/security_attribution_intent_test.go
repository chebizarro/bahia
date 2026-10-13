package controlplane

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
)

type recordingSecurityAttributions struct{ saved *domain.SecurityAttribution }

func (p *recordingSecurityAttributions) PublishAttribution(_ context.Context, a *domain.SecurityAttribution) error {
	copy := *a
	p.saved = &copy
	return nil
}

func TestSecurityAttributionIntentBindsSignedOrgAndCoordinate(t *testing.T) {
	org := uuid.New()
	a := domain.SecurityAttribution{OrgID: org, SubjectType: "sbom", SubjectDigest: strings.Repeat("c", 64), Visible: true}
	contentJSON, err := json.Marshal(a)
	require.NoError(t, err)
	var content map[string]interface{}
	require.NoError(t, json.Unmarshal(contentJSON, &content))
	pub := &recordingSecurityAttributions{}
	h := NewSecurityScanIntentHandler(nil, pub)
	intent := &Intent{Op: "attribution-apply", OrgID: org, Coordinate: nostradapter.SecurityAttributionDTag(a), Content: content}
	require.NoError(t, h.HandleIntent(context.Background(), intent))
	require.Equal(t, &a, pub.saved)
	pub.saved = nil
	intent.OrgID = uuid.New()
	require.ErrorContains(t, h.HandleIntent(context.Background(), intent), "match")
	require.Nil(t, pub.saved)
	intent.OrgID = org
	intent.Coordinate = "security:attribution:other"
	require.ErrorContains(t, h.HandleIntent(context.Background(), intent), "match")
	require.Nil(t, pub.saved)
	intent.Coordinate = nostradapter.SecurityAttributionDTag(a)
	a.OrgID = uuid.Nil
	contentJSON, err = json.Marshal(a)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(contentJSON, &intent.Content))
	require.ErrorContains(t, h.HandleIntent(context.Background(), intent), "org_id")
	require.Nil(t, pub.saved)
}
