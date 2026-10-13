package controlplane

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/secretstore"
	"github.com/stretchr/testify/require"
)

func TestDecisionPinsExactSignetCredentialAndVersionWithoutValue(t *testing.T) {
	serviceID, envID, orgID, secretID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	credential := domain.SecretCredentialRef{Store: secretstore.BackendSignet, CredentialID: "cred-pinned", Version: 4}
	records := &memCanonicalSecrets{records: map[uuid.UUID]domain.SecretRef{
		secretID: {ID: secretID, ServiceID: serviceID, EnvironmentID: &envID,
			Name: "TOKEN", CredentialRef: &credential},
	}, serviceOrg: map[uuid.UUID]string{serviceID: orgID.String()}}
	label := orgID.String() + "/" + serviceID.String() + "/TOKEN"
	store := &memSecretStore{creds: map[string]secretstore.Metadata{
		credential.CredentialID: {CredentialID: credential.CredentialID, Version: 4,
			Status: secretstore.StatusActive, Label: label},
	}}
	spec := &domain.DesiredServiceSpec{SchemaVersion: domain.DesiredStateSchemaVersion,
		ServiceID: serviceID, EnvironmentID: envID, SecretRefs: []domain.DesiredSecretRef{{
			SecretID: secretID, EnvVar: "TOKEN", Name: "TOKEN", RedactedValue: "REDACTED(TOKEN)",
		}}}
	before := spec.ComputeDesiredHash()
	require.NoError(t, pinDecisionSecrets(context.Background(), spec, records, store))
	require.Equal(t, credential, *spec.SecretRefs[0].CredentialRef)
	require.NotEqual(t, before, spec.DesiredHash, "the decision hash binds id and version")
	require.NotContains(t, string(marshalPinnedSpec(t, spec)), "secret-value")
	require.True(t, strings.Contains(string(marshalPinnedSpec(t, spec)), "cred-pinned"))
	require.NoError(t, verifyDecisionSecrets(context.Background(), spec, records, store))

	rotated := credential
	rotated.Version = 5
	ref := records.records[secretID]
	ref.CredentialRef = &rotated
	records.records[secretID] = ref
	store.creds[credential.CredentialID] = secretstore.Metadata{CredentialID: credential.CredentialID,
		Version: 5, Status: secretstore.StatusActive, Label: label}
	require.ErrorContains(t, verifyDecisionSecrets(context.Background(), spec, records, store), "fresh decision required")
}

func marshalPinnedSpec(t *testing.T, value any) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	require.NoError(t, err)
	return b
}
