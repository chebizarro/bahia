package controlplane

import (
	"context"
	"encoding/json"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSecurityContextVMScanAcknowledgesIntentOnly(t *testing.T) {
	runID := uuid.New()
	fake := &fakeSecurityScannerControlPlane{accepted: &service.SecurityScanAccepted{Status: "accepted", RunID: runID, TargetKeyHash: "hash", TargetType: domain.SecurityTargetPackage, Observables: service.SecurityObservableHint{Kinds: []int{30315, 30900, 30078, 4903}, Tags: map[string]string{"domain": "security", "target_key_hash": "hash"}}}}
	h := securityContextVMHandler{scanner: fake}
	params, err := json.Marshal(securityScanParams{Target: service.SecurityScanTargetInput{Type: domain.SecurityTargetPackage, Package: &service.SecurityPackageInput{Ecosystem: "npm", Name: "lodash", Version: "4.17.21"}}})
	require.NoError(t, err)

	result, err := h.scan(context.Background(), ContextVMRequest{Event: signedContextVMEvent(t), RPC: ContextVMJSONRPCRequest{Params: params}})

	require.NoError(t, err)
	accepted, ok := result.(*service.SecurityScanAccepted)
	require.True(t, ok)
	require.Equal(t, "accepted", accepted.Status)
	require.Equal(t, runID, accepted.RunID)
	require.Equal(t, domain.SecurityTriggerManual, fake.submit.Trigger)
	require.Equal(t, domain.SecurityTargetPackage, fake.submit.Target.Type)
}

func TestSecurityContextVMScanRejectsInvalidTarget(t *testing.T) {
	h := securityContextVMHandler{scanner: &fakeSecurityScannerControlPlane{}}
	params, err := json.Marshal(securityScanParams{Target: service.SecurityScanTargetInput{Type: domain.SecurityTargetPURL}})
	require.NoError(t, err)

	_, err = h.scan(context.Background(), ContextVMRequest{Event: signedContextVMEvent(t), RPC: ContextVMJSONRPCRequest{Params: params}})

	require.Error(t, err)
	require.Contains(t, err.Error(), "purl is required")
}

func TestSecurityContextVMFindingsListRequiresRunOrTarget(t *testing.T) {
	fake := &fakeSecurityScannerControlPlane{}
	h := securityContextVMHandler{scanner: fake}
	params := []byte(`{}`)

	_, err := h.findingsList(context.Background(), ContextVMRequest{Event: signedContextVMEvent(t), RPC: ContextVMJSONRPCRequest{Params: params}})

	require.Error(t, err)
	require.Contains(t, err.Error(), "requires run_id or target_key_hash")
}

func TestSecurityContextVMSchedulesListIsReadOnly(t *testing.T) {
	fake := &fakeSecurityScannerControlPlane{schedules: &service.SecuritySchedulesListResult{Status: "ok", Limit: 25}}
	h := securityContextVMHandler{scanner: fake}

	result, err := h.schedulesList(context.Background(), ContextVMRequest{Event: signedContextVMEvent(t), RPC: ContextVMJSONRPCRequest{Params: []byte(`{"limit":25,"enabled_only":true}`)}})

	require.NoError(t, err)
	list, ok := result.(*service.SecuritySchedulesListResult)
	require.True(t, ok)
	require.Equal(t, 25, list.Limit)
	require.True(t, fake.schedulesReq.EnabledOnly)
	require.False(t, fake.submitCalled)
}

type fakeSecurityScannerControlPlane struct {
	accepted     *service.SecurityScanAccepted
	findings     *service.SecurityFindingsListResult
	schedules    *service.SecuritySchedulesListResult
	submit       service.SecurityScanRequest
	submitCalled bool
	schedulesReq service.SecuritySchedulesListRequest
}

func (f *fakeSecurityScannerControlPlane) SubmitScan(_ context.Context, req service.SecurityScanRequest) (*service.SecurityScanAccepted, error) {
	f.submit = req
	f.submitCalled = true
	return f.accepted, nil
}
func (f *fakeSecurityScannerControlPlane) Rescan(context.Context, service.SecurityRescanRequest) (*service.SecurityScanAccepted, error) {
	return f.accepted, nil
}
func (f *fakeSecurityScannerControlPlane) ListFindings(_ context.Context, req service.SecurityFindingsListRequest) (*service.SecurityFindingsListResult, error) {
	if (req.RunID == nil || *req.RunID == uuid.Nil) && req.TargetKeyHash == "" {
		return nil, serviceListValidationError{}
	}
	return f.findings, nil
}
func (f *fakeSecurityScannerControlPlane) ListSchedules(_ context.Context, req service.SecuritySchedulesListRequest) (*service.SecuritySchedulesListResult, error) {
	f.schedulesReq = req
	return f.schedules, nil
}

type serviceListValidationError struct{}

func (serviceListValidationError) Error() string {
	return "security/findings-list requires run_id or target_key_hash"
}

func signedContextVMEvent(t *testing.T) *nostr.Event {
	secret, err := nostr.SecretKeyFromHex("1111111111111111111111111111111111111111111111111111111111111111")
	require.NoError(t, err)
	ev := &nostr.Event{Kind: KindContextVMMessage, CreatedAt: nostr.Now(), Content: "{}"}
	require.NoError(t, ev.Sign(secret))
	return ev
}
