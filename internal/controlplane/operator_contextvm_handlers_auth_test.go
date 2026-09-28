package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

func TestAuthorizedContextVMPubkeyFailsClosed(t *testing.T) {
	operator := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	other := testNostrPubKeyHexFromPrivateKey(t, testOtherKey)
	for _, tc := range []struct {
		name       string
		pubkey     string
		authorized []string
		want       bool
	}{
		{name: "nil list", pubkey: operator},
		{name: "empty list", pubkey: operator, authorized: []string{}},
		{name: "blank entries", pubkey: operator, authorized: []string{"", "  "}},
		{name: "blank requester", pubkey: "  ", authorized: []string{"  ", operator}},
		{name: "unlisted requester", pubkey: other, authorized: []string{operator}},
		{name: "listed requester", pubkey: operator, authorized: []string{other, operator}, want: true},
		{name: "uppercase requester", pubkey: strings.ToUpper(operator), authorized: []string{operator}, want: true},
		{name: "uppercase configuration", pubkey: operator, authorized: []string{strings.ToUpper(operator)}, want: true},
		{name: "mixed case and whitespace", pubkey: " " + strings.ToUpper(operator[:32]) + operator[32:] + " ", authorized: []string{" " + operator[:32] + strings.ToUpper(operator[32:]) + " "}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := authorizedContextVMPubkey(tc.pubkey, tc.authorized); got != tc.want {
				t.Fatalf("authorizedContextVMPubkey(%q, %v) = %v, want %v", tc.pubkey, tc.authorized, got, tc.want)
			}
		})
	}
}

// Admit both signers through the transport gate so only each method's own
// allowlist can prevent execution. Exercise registration and signed dispatch
// with valid payloads for all three privileged methods.
func TestOperatorContextVMHandlersScopedAuthorization(t *testing.T) {
	operator := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	other := testNostrPubKeyHexFromPrivateKey(t, testOtherKey)
	for _, action := range []string{"scan", "import", "deploy", "restart", "stop"} {
		t.Run(action, func(t *testing.T) {
			method := ContextVMMethodServiceAction
			params := fmt.Sprintf(`{"action":%q,"service_id":"11111111-1111-1111-1111-111111111111","environment_id":"22222222-2222-2222-2222-222222222222"}`, action)
			authError := "requester not in authorized direct-runtime list"
			if action == "scan" || action == "import" {
				method = "adoption/" + action
				params = `{"targets":[{"name":"prod","endpoint_ref":"prod-docker"}],"import_all":true}`
				authError = "requester not in authorized adoption list"
			}
			for _, tc := range []struct {
				name       string
				allowlist  []string
				signer     string
				otherScope bool
				want       bool
			}{
				{name: "nil list denies globally authorized signer", signer: testRequesterKey},
				{name: "empty list denies", allowlist: []string{}, signer: testRequesterKey},
				{name: "unlisted signer denies", allowlist: []string{operator}, signer: testOtherKey},
				{name: "listed signer executes", allowlist: []string{other, operator}, signer: testRequesterKey, want: true},
				{name: "mixed case configuration executes", allowlist: []string{" " + strings.ToUpper(operator[:32]) + operator[32:] + " "}, signer: testRequesterKey, want: true},
				{name: "other scope cannot grant access", allowlist: []string{operator}, signer: testRequesterKey, otherScope: true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					adoption := &stubAdoptionOperatorService{}
					runtime := &stubRuntimeLifecycleOperatorService{}
					cfg := OperatorContextVMHandlersConfig{Adoption: adoption, RuntimeLifecycle: runtime}
					if (method == ContextVMMethodServiceAction) != tc.otherScope {
						cfg.DirectRuntimeAuthorizedPubkeys = tc.allowlist
					} else {
						cfg.AdoptionAuthorizedPubkeys = tc.allowlist
					}
					publisher := &mockEncryptedPublisher{}
					transport := NewEncryptedRequestTransport(nil, newResponder(t, publisher), []string{operator, other}, zap.NewNop())
					NewOperatorContextVMHandlers(cfg).Register(transport)
					content := fmt.Sprintf(`{"jsonrpc":"2.0","id":"operator-auth","method":%q,"params":%s}`, method, params)
					request := makeContextVMEvent(t, tc.signer, content)
					wrapped := method != ContextVMMethodServiceAction
					if wrapped {
						request = wrapContextVMEventWithWrapperKey(t, request, tc.signer, KindContextVMGiftWrap)
					}
					transport.HandleEvent(context.Background(), request)

					if len(publisher.events) == 0 {
						t.Fatal("expected a ContextVM response")
					}
					last := publisher.events[len(publisher.events)-1]
					var response ContextVMJSONRPCResponse
					if wrapped {
						response = unwrapContextVMResponse(t, last, tc.signer)
					} else {
						response = contextVMResponse(t, last)
					}
					if string(response.ID) != `"operator-auth"` {
						t.Fatalf("response ID = %s, want operator-auth", response.ID)
					}
					if tc.want {
						if response.Error != nil {
							t.Fatalf("authorized request failed: %+v", response.Error)
						}
					} else if response.Error == nil || response.Error.Message != authError {
						t.Fatalf("response error = %+v, want %q", response.Error, authError)
					}
					for name, called := range map[string]bool{
						"scan": adoption.scanCalled, "import": adoption.importCalled,
						"deploy": runtime.deployCalled, "restart": runtime.restartCalled, "stop": runtime.stopCalled,
					} {
						if want := tc.want && name == action; called != want {
							t.Errorf("%s called = %v, want %v", name, called, want)
						}
					}
				})
			}
		})
	}
}

// Option B: per-instance detail of unmanaged workloads is available only on
// the authenticated adoption/scan path. It must reach an authorized requester
// over an encrypted response, and nobody else: not an unlisted signer, and
// not a plaintext response readable by every relay subscriber.
func TestAdoptionScanPerInstanceDetailReachesOnlyAuthorizedEncryptedRequester(t *testing.T) {
	operator := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	other := testNostrPubKeyHexFromPrivateKey(t, testOtherKey)
	existing := uuid.New()
	previews := []service.AdoptionPreview{{
		Target: service.AdoptionTarget{Name: "edge-01", EnvironmentName: "production", EndpointRef: "edge-01-docker"},
		Containers: []service.AdoptionPreviewContainer{
			{Discovered: runtime.DiscoveredContainer{TargetName: "mystery-db", ContainerID: "aaaa1111bbbb", ContainerName: "mystery-db", ImageRepo: "postgres", ImageDigest: "sha256:" + strings.Repeat("1", 64)}, ProposedServiceName: "mystery-db", Adoptable: true},
			{Discovered: runtime.DiscoveredContainer{TargetName: "adopted-api", ContainerID: "cccc2222dddd", ContainerName: "adopted-api"}, ExistingServiceID: &existing, WillUpdate: true},
		},
	}}
	detail := []string{"aaaa1111bbbb", "mystery-db", "postgres", "sha256:" + strings.Repeat("1", 64), "cccc2222dddd"}
	params := `{"targets":[{"name":"edge-01","endpoint_ref":"edge-01-docker","environment_name":"production"}]}`

	for _, tc := range []struct {
		name      string
		signer    string
		wrap      bool
		wantError string
	}{
		{name: "authorized encrypted requester receives detail", signer: testRequesterKey, wrap: true},
		{name: "unlisted encrypted requester is denied", signer: testOtherKey, wrap: true, wantError: "requester not in authorized adoption list"},
		{name: "authorized plaintext request is refused", signer: testRequesterKey, wantError: errAdoptionRequiresEncryption.Error()},
		{name: "unlisted plaintext request is denied", signer: testOtherKey, wantError: "requester not in authorized adoption list"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adoption := &stubAdoptionOperatorService{scanResp: previews}
			publisher := &mockEncryptedPublisher{}
			transport := NewEncryptedRequestTransport(nil, newResponder(t, publisher), []string{operator, other}, zap.NewNop())
			NewOperatorContextVMHandlers(OperatorContextVMHandlersConfig{Adoption: adoption, AdoptionAuthorizedPubkeys: []string{operator}}).Register(transport)

			content := fmt.Sprintf(`{"jsonrpc":"2.0","id":"scan-detail","method":%q,"params":%s}`, ContextVMMethodAdoptionScan, params)
			request := makeContextVMEvent(t, tc.signer, content)
			if tc.wrap {
				request = wrapContextVMEventWithWrapperKey(t, request, tc.signer, KindContextVMGiftWrap)
			}
			transport.HandleEvent(context.Background(), request)
			if len(publisher.events) == 0 {
				t.Fatal("expected a ContextVM response")
			}

			// Whatever the outcome, no event visible on the relay may carry
			// per-instance detail in plaintext.
			for _, ev := range publisher.events {
				for _, secret := range detail {
					if strings.Contains(ev.Content, secret) {
						t.Fatalf("relay-visible event kind %d exposes %q in plaintext", ev.Kind, secret)
					}
				}
			}

			last := publisher.events[len(publisher.events)-1]
			var response ContextVMJSONRPCResponse
			if tc.wrap {
				response = unwrapContextVMResponse(t, last, tc.signer)
			} else {
				response = contextVMResponse(t, last)
			}
			if tc.wantError != "" {
				if response.Error == nil || response.Error.Message != tc.wantError {
					t.Fatalf("response error = %+v, want %q", response.Error, tc.wantError)
				}
				if adoption.scanCalled {
					t.Fatal("denied request must not run the scan")
				}
				encoded, _ := json.Marshal(response)
				for _, secret := range detail {
					if strings.Contains(string(encoded), secret) {
						t.Fatalf("denied response exposes %q", secret)
					}
				}
				return
			}
			if response.Error != nil {
				t.Fatalf("authorized scan failed: %+v", response.Error)
			}
			encoded, err := json.Marshal(response.Result)
			if err != nil {
				t.Fatalf("marshal result: %v", err)
			}
			for _, want := range []string{"aaaa1111bbbb", "mystery-db", "sha256:" + strings.Repeat("1", 64), "cccc2222dddd"} {
				if !strings.Contains(string(encoded), want) {
					t.Fatalf("authorized result lacks per-instance detail %q: %s", want, encoded)
				}
			}
		})
	}
}
