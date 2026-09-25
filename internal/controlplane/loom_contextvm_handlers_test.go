package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"fiatjaf.com/nostr"
	loomadapter "github.com/openagentsinc/bahia/internal/adapters/loom"
	"go.uber.org/zap"
)

var (
	testSubmitterPub = newTestNostrPubkey()
	testOperatorPub  = newTestNostrPubkey()
	testUnrelatedPub = newTestNostrPubkey()
)

func newTestNostrPubkey() string {
	_, pubkey := testNostrKeypair()
	return pubkey
}

type mockLoomClient struct {
	mu               sync.RWMutex
	submitters       map[string]string
	submitErr        error
	cancelErr        error
	lastCancelJobID  string
	lastCancelWorker string
	projectionReady  bool
}

func (m *mockLoomClient) SubmitJob(ctx context.Context, job loomadapter.JobRequest) (string, error) {
	if m.submitErr != nil {
		return "", m.submitErr
	}
	return "mock-job-event-id", nil
}

func (m *mockLoomClient) CancelJob(ctx context.Context, jobEventID string, workerPubkey string) error {
	m.lastCancelJobID = jobEventID
	m.lastCancelWorker = workerPubkey
	return m.cancelErr
}

func (m *mockLoomClient) RememberJobSubmitter(jobEventID, submitter string) {
	if jobEventID == "" || submitter == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.submitters == nil {
		m.submitters = make(map[string]string)
	}
	m.submitters[jobEventID] = submitter
}

func (m *mockLoomClient) JobSubmitter(jobEventID string) string {
	if jobEventID == "" {
		return ""
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.submitters[jobEventID]
}

func (m *mockLoomClient) CanonicalProjectionReady() bool {
	return m.projectionReady
}

func (m *mockLoomClient) StartCanonicalProjection(jobEventID string) {
}

func makeParams(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("marshal params: %v", err))
	}
	return b
}

func makeContextVMRequest(pubkeyHex string, method string, params json.RawMessage) ContextVMRequest {
	pk, err := nostr.PubKeyFromHex(pubkeyHex)
	if err != nil {
		panic(fmt.Sprintf("pubkey from hex %q: %v", pubkeyHex, err))
	}
	return ContextVMRequest{
		Event: &nostr.Event{PubKey: pk},
		RPC: ContextVMJSONRPCRequest{
			Method: method,
			Params: params,
		},
	}
}

func TestSubmitRecordsSubmitterFromVerifiedEvent(t *testing.T) {
	client := &mockLoomClient{projectionReady: true}
	h := loomContextVMHandlers{client: client, servicePubkey: testNostrPubKeyHexFromPrivateKey(t, testServiceKey)}

	req := makeContextVMRequest(testSubmitterPub, ContextVMMethodLoomSubmit, makeParams(loomSubmitContextVMPayload{
		Image:       "alpine",
		Service:     "test-svc",
		Environment: "staging",
		Type:        "deploy",
	}))

	_, err := h.submit(context.Background(), req)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	submitter := client.JobSubmitter("mock-job-event-id")
	if submitter != testSubmitterPub {
		t.Fatalf("submitter recorded as %q, want %q", submitter, testSubmitterPub)
	}
}

func TestSubmitterNotSpoofableViaPayload(t *testing.T) {
	client := &mockLoomClient{projectionReady: true}
	h := loomContextVMHandlers{client: client, servicePubkey: testNostrPubKeyHexFromPrivateKey(t, testServiceKey)}

	payload := map[string]string{
		"image":       "alpine",
		"service":     "test-svc",
		"environment": "staging",
		"type":        "deploy",
		"submitter":   testUnrelatedPub,
	}

	req := makeContextVMRequest(testSubmitterPub, ContextVMMethodLoomSubmit, makeParams(payload))

	_, err := h.submit(context.Background(), req)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	submitter := client.JobSubmitter("mock-job-event-id")
	if submitter != testSubmitterPub {
		t.Fatalf("submitter from event pubkey should be %q, got %q", testSubmitterPub, submitter)
	}
}

func TestOriginalSubmitterCanCancel(t *testing.T) {
	client := &mockLoomClient{projectionReady: true}
	h := loomContextVMHandlers{client: client, servicePubkey: testNostrPubKeyHexFromPrivateKey(t, testServiceKey)}

	submitReq := makeContextVMRequest(testSubmitterPub, ContextVMMethodLoomSubmit, makeParams(loomSubmitContextVMPayload{
		Image:       "alpine",
		Service:     "test-svc",
		Environment: "staging",
		Type:        "deploy",
	}))
	_, err := h.submit(context.Background(), submitReq)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	cancelReq := makeContextVMRequest(testSubmitterPub, ContextVMMethodLoomCancel, makeParams(loomCancelContextVMPayload{
		JobEventID: "mock-job-event-id",
	}))
	_, err = h.cancel(context.Background(), cancelReq)
	if err != nil {
		t.Fatalf("original submitter should be able to cancel: %v", err)
	}
	if client.lastCancelJobID != "mock-job-event-id" {
		t.Fatalf("cancel not forwarded to client")
	}
}

func TestOperatorCanCancel(t *testing.T) {
	client := &mockLoomClient{projectionReady: true}
	h := loomContextVMHandlers{
		client:               client,
		fleetOperatorPubkeys: []string{testOperatorPub},
		servicePubkey:        testNostrPubKeyHexFromPrivateKey(t, testServiceKey),
	}

	submitReq := makeContextVMRequest(testSubmitterPub, ContextVMMethodLoomSubmit, makeParams(loomSubmitContextVMPayload{
		Image:       "alpine",
		Service:     "test-svc",
		Environment: "staging",
		Type:        "deploy",
	}))
	_, err := h.submit(context.Background(), submitReq)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	cancelReq := makeContextVMRequest(testOperatorPub, ContextVMMethodLoomCancel, makeParams(loomCancelContextVMPayload{
		JobEventID: "mock-job-event-id",
	}))
	_, err = h.cancel(context.Background(), cancelReq)
	if err != nil {
		t.Fatalf("operator should be able to cancel: %v", err)
	}
	if client.lastCancelJobID != "mock-job-event-id" {
		t.Fatalf("cancel not forwarded to client")
	}
}

func TestUnrelatedCallerDeniedCancel(t *testing.T) {
	client := &mockLoomClient{projectionReady: true}
	h := loomContextVMHandlers{
		client:               client,
		fleetOperatorPubkeys: []string{testOperatorPub},
		servicePubkey:        testNostrPubKeyHexFromPrivateKey(t, testServiceKey),
	}

	submitReq := makeContextVMRequest(testSubmitterPub, ContextVMMethodLoomSubmit, makeParams(loomSubmitContextVMPayload{
		Image:       "alpine",
		Service:     "test-svc",
		Environment: "staging",
		Type:        "deploy",
	}))
	_, err := h.submit(context.Background(), submitReq)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	cancelReq := makeContextVMRequest(testUnrelatedPub, ContextVMMethodLoomCancel, makeParams(loomCancelContextVMPayload{
		JobEventID: "mock-job-event-id",
	}))
	_, err = h.cancel(context.Background(), cancelReq)
	if err == nil {
		t.Fatal("unrelated caller should be denied cancel")
	}
	if !strings.Contains(err.Error(), "not authorized") {
		t.Fatalf("expected authorization error, got: %v", err)
	}
	if client.lastCancelJobID != "" {
		t.Fatalf("cancel should not have been forwarded to client")
	}
}

func TestJobWithoutSubmitterNotCancellable(t *testing.T) {
	client := &mockLoomClient{}
	h := loomContextVMHandlers{
		client:               client,
		fleetOperatorPubkeys: []string{testOperatorPub},
		servicePubkey:        testNostrPubKeyHexFromPrivateKey(t, testServiceKey),
	}

	cancelReq := makeContextVMRequest(testOperatorPub, ContextVMMethodLoomCancel, makeParams(loomCancelContextVMPayload{
		JobEventID: "legacy-job-no-submitter",
	}))
	_, err := h.cancel(context.Background(), cancelReq)
	if err == nil {
		t.Fatal("legacy job without submitter must not be cancellable by operator")
	}
	if !strings.Contains(err.Error(), "not authorized") {
		t.Fatalf("expected authorization error, got: %v", err)
	}

	cancelReq2 := makeContextVMRequest(testSubmitterPub, ContextVMMethodLoomCancel, makeParams(loomCancelContextVMPayload{
		JobEventID: "legacy-job-no-submitter",
	}))
	_, err = h.cancel(context.Background(), cancelReq2)
	if err == nil {
		t.Fatal("legacy job without submitter must not be cancellable by anyone")
	}
	if !strings.Contains(err.Error(), "not authorized") {
		t.Fatalf("expected authorization error, got: %v", err)
	}
}

func TestAuthorizedForLoomCancel(t *testing.T) {
	_, submitterPub := testNostrKeypair()
	_, operatorPub := testNostrKeypair()
	_, unrelatedPub := testNostrKeypair()
	operators := []string{operatorPub}

	tests := []struct {
		name      string
		caller    string
		submitter string
		operators []string
		want      bool
	}{
		{
			name:      "empty caller denied",
			caller:    "",
			submitter: submitterPub,
			operators: operators,
			want:      false,
		},
		{
			name:      "submitter authorized",
			caller:    submitterPub,
			submitter: submitterPub,
			operators: nil,
			want:      true,
		},
		{
			name:      "operator authorized",
			caller:    operatorPub,
			submitter: submitterPub,
			operators: operators,
			want:      true,
		},
		{
			name:      "unrelated denied",
			caller:    unrelatedPub,
			submitter: submitterPub,
			operators: operators,
			want:      false,
		},
		{
			name:      "empty submitter fails closed",
			caller:    submitterPub,
			submitter: "",
			operators: operators,
			want:      false,
		},
		{
			name:      "empty operator list still allows submitter",
			caller:    submitterPub,
			submitter: submitterPub,
			operators: nil,
			want:      true,
		},
		{
			name:      "whitespace-only caller denied",
			caller:    "   ",
			submitter: submitterPub,
			operators: operators,
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := authorizedForLoomCancel(tt.caller, tt.submitter, tt.operators)
			if got != tt.want {
				t.Fatalf("authorizedForLoomCancel(%q, %q, %v) = %v, want %v",
					tt.caller, tt.submitter, tt.operators, got, tt.want)
			}
		})
	}
}

func TestAuthorizedForLoomCancelEmptyOperatorListFailsClosed(t *testing.T) {
	_, submitterPub := testNostrKeypair()
	_, operatorPub := testNostrKeypair()

	if authorizedForLoomCancel(operatorPub, submitterPub, nil) {
		t.Fatal("operator should not be authorized when list is empty")
	}
	if authorizedForLoomCancel(operatorPub, "", nil) {
		t.Fatal("operator should not be authorized for unowned job when list is empty")
	}
}

func TestValidateLoomSubmitPayloadRejectsSecretBearingFieldsWithoutEcho(t *testing.T) {
	const secret = "super-secret-value"
	tests := []struct {
		name    string
		payload loomSubmitContextVMPayload
	}{
		{name: "raw secrets", payload: loomSubmitContextVMPayload{Secrets: map[string]string{"TOKEN": secret}}},
		{name: "payment token", payload: loomSubmitContextVMPayload{PaymentToken: secret}},
		{name: "bunker argv", payload: loomSubmitContextVMPayload{Args: []string{"--signer=bunker://" + secret}}},
		{name: "nostr connect environment", payload: loomSubmitContextVMPayload{Env: map[string]string{"SIGNER": "nostrconnect://" + secret}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateLoomSubmitPayload(test.payload)
			if err == nil {
				t.Fatal("expected secret-bearing payload to be rejected")
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("error leaked secret value: %v", err)
			}
		})
	}
}

func TestValidateLoomSubmitPayloadAllowsNonSecretExecutionMetadata(t *testing.T) {
	err := validateLoomSubmitPayload(loomSubmitContextVMPayload{
		Cmd:    "npm",
		Args:   []string{"run", "build"},
		Env:    map[string]string{"NODE_ENV": "production"},
		Params: map[string]string{"ref": "main"},
	})
	if err != nil {
		t.Fatalf("non-secret payload rejected: %v", err)
	}
}

func TestLoomServiceSignerCannotSubmitOrCancel(t *testing.T) {
	servicePubkey := testNostrPubKeyHexFromPrivateKey(t, testServiceKey)
	for _, method := range []string{ContextVMMethodLoomSubmit, ContextVMMethodLoomCancel} {
		t.Run(method, func(t *testing.T) {
			publisher := &mockEncryptedPublisher{}
			transport := NewEncryptedRequestTransport(nil, newResponder(t, publisher), authzTestTransportAuthors(t), zap.NewNop())
			// Even explicit allowlisting cannot promote the transport signer to
			// an original submitter. No Loom client call may happen on this path.
			RegisterLoomContextVMHandlers(transport, &untouchedOperatorDependencies{}, []string{servicePubkey}, NewFleetOperatorGate([]string{servicePubkey}))
			transport.HandleEvent(t.Context(), backupAuthorityRequest(t, testServiceKey, method, map[string]any{
				"job_event_id": "job", "image": "alpine", "submitter": testSubmitterPub,
			}))
			response := contextVMResponse(t, publisher.events[len(publisher.events)-1])
			if response.Error == nil || !strings.Contains(response.Error.Message, "service signer cannot supply Loom requester") {
				t.Fatalf("service signer obtained Loom authority: %+v", response)
			}
		})
	}
}

func TestLoomRegisteredSubmitAndCancelUseVerifiedOwner(t *testing.T) {
	requester := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	other := testNostrPubKeyHexFromPrivateKey(t, testOtherKey)
	client := &mockLoomClient{projectionReady: true}
	publisher := &mockEncryptedPublisher{}
	transport := NewEncryptedRequestTransport(nil, newResponder(t, publisher), authzTestTransportAuthors(t), zap.NewNop())
	RegisterLoomContextVMHandlers(transport, client, nil, NewFleetOperatorGate([]string{requester}))
	transport.HandleEvent(t.Context(), backupAuthorityRequest(t, testRequesterKey, ContextVMMethodLoomSubmit, map[string]any{
		"image": "alpine", "submitter": other, "requester_pubkey": other,
	}))
	if owner := client.JobSubmitter("mock-job-event-id"); owner != requester {
		t.Fatalf("recorded submitter = %s, want verified signer %s", owner, requester)
	}
	// Re-register with an empty operator list: only server-recorded ownership
	// can authorize cancellation, not caller-supplied requester/submitter data.
	RegisterLoomContextVMHandlers(transport, client, nil, nil)
	transport.HandleEvent(t.Context(), backupAuthorityRequest(t, testOtherKey, ContextVMMethodLoomCancel, map[string]any{
		"job_event_id": "mock-job-event-id", "submitter": requester, "requester_pubkey": requester,
	}))
	response := contextVMResponse(t, publisher.events[len(publisher.events)-1])
	if response.Error == nil || client.lastCancelJobID != "" {
		t.Fatalf("forged owner obtained cancel authority: %+v", response)
	}
	transport.HandleEvent(t.Context(), backupAuthorityRequest(t, testRequesterKey, ContextVMMethodLoomCancel, map[string]any{
		"job_event_id": "mock-job-event-id",
	}))
	response = contextVMResponse(t, publisher.events[len(publisher.events)-1])
	if response.Error != nil || client.lastCancelJobID != "mock-job-event-id" {
		t.Fatalf("recorded owner could not cancel: %+v", response)
	}
}

func TestLoomMissingRequesterFailsBeforeClientAccess(t *testing.T) {
	h := loomContextVMHandlers{client: &untouchedOperatorDependencies{}, servicePubkey: testNostrPubKeyHexFromPrivateKey(t, testServiceKey)}
	for _, event := range []*nostr.Event{nil, {}} {
		for _, handler := range []ContextVMHandler{h.submit, h.cancel} {
			if _, err := handler(t.Context(), ContextVMRequest{Event: event}); err == nil {
				t.Fatal("missing requester accepted")
			}
		}
	}
}

func TestLoomCancelRequiresScopedOperatorNotJustTransportAdmission(t *testing.T) {
	requester := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	operator := testNostrPubKeyHexFromPrivateKey(t, testOtherKey)
	for _, scopedOperators := range [][]string{nil, {operator}} {
		client := &mockLoomClient{submitters: map[string]string{"job": requester}}
		publisher := &mockEncryptedPublisher{}
		transport := NewEncryptedRequestTransport(nil, newResponder(t, publisher), authzTestTransportAuthors(t), zap.NewNop())
		RegisterLoomContextVMHandlers(transport, client, scopedOperators, NewFleetOperatorGate(authzTestTransportAuthors(t)))
		transport.HandleEvent(t.Context(), backupAuthorityRequest(t, testOtherKey, ContextVMMethodLoomCancel, map[string]any{"job_event_id": "job"}))
		response := contextVMResponse(t, publisher.events[len(publisher.events)-1])
		if len(scopedOperators) == 0 {
			if response.Error == nil || client.lastCancelJobID != "" {
				t.Fatal("global transport admission incorrectly granted Loom cancellation")
			}
		} else if response.Error != nil || client.lastCancelJobID != "job" {
			t.Fatalf("explicit Loom operator could not cancel: %+v", response.Error)
		}
	}
}
