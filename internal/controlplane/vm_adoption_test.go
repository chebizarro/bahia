package controlplane

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
)

type adoptionRegistrationFixture struct{ calls int }

func (f *adoptionRegistrationFixture) MutatePersistentVM(_ context.Context, p VirtualizationPrincipal, method string, m VirtualizationMutation) (VirtualizationAdmission, error) {
	if method != "persistent-vm/register-adoption" || m.VM == nil || m.VM.CreatedBy != p.PubKey {
		return VirtualizationAdmission{}, domain.ErrInvalidValue
	}
	f.calls++
	return VirtualizationAdmission{ResourceID: m.VM.ID, Generation: 1}, nil
}

func TestVMAdoptionRegistrationAcknowledgesNoOwnership(t *testing.T) {
	org, id := uuid.New(), uuid.New()
	fixture := &adoptionRegistrationFixture{}
	h := &VirtualizationHandlers{RBAC: auth.NewRBAC(vmContextMembers{org, domain.RoleDeployer}), Persistent: fixture, CanonicalAuthor: strings.Repeat("a", 64), ProjectionReady: true}
	require.Contains(t, VirtualizationMethods(), "persistent-vm/register-adoption")
	req := VirtualizationMutation{OrgID: org, VM: &domain.PersistentVMDeployment{VirtualizationResourceMeta: domain.VirtualizationResourceMeta{ID: id, OrgID: org, Generation: 1}}}
	result, err := h.Handle(context.Background(), "persistent-vm/register-adoption", vmContextRequest(t, req))
	require.NoError(t, err)
	ack := result.(VirtualizationAcknowledgment)
	require.Equal(t, "registered", ack.Status)
	require.Equal(t, id, ack.ResourceID)
	require.Equal(t, uuid.Nil, ack.OperationID)
	require.Empty(t, ack.OperationDTag)
	req.VM.CreatedBy = "spoofed"
	_, err = h.Handle(context.Background(), "persistent-vm/register-adoption", vmContextRequest(t, req))
	require.Error(t, err)
	require.Equal(t, 1, fixture.calls)
}
