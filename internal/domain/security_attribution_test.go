package domain

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestSecurityAttributionRequiresOrgAndContentAddress(t *testing.T) {
	a := &SecurityAttribution{OrgID: uuid.New(), SubjectType: "sbom", SubjectDigest: strings.Repeat("a", 64), Visible: true}
	require.NoError(t, ValidateSecurityAttribution(a))
	a.OrgID = uuid.Nil
	require.ErrorContains(t, ValidateSecurityAttribution(a), "org_id")
	a.OrgID = uuid.New()
	a.SubjectType = "artifact-id"
	require.ErrorContains(t, ValidateSecurityAttribution(a), "subject_type")
	a.SubjectType = "security-target"
	a.SubjectDigest = strings.Repeat("A", 64)
	require.ErrorContains(t, ValidateSecurityAttribution(a), "lowercase")
	a.SubjectDigest = strings.Repeat("z", 64)
	require.ErrorContains(t, ValidateSecurityAttribution(a), "subject_digest")
}
