package controlplane

import (
	"context"
	"fmt"
	"strings"
	"testing"

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
					transport.HandleEvent(context.Background(), makeContextVMEvent(t, tc.signer, content))

					if len(publisher.events) == 0 {
						t.Fatal("expected a ContextVM response")
					}
					response := contextVMResponse(t, publisher.events[len(publisher.events)-1])
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
