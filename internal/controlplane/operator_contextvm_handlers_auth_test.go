package controlplane

import (
	"context"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

type recordingAdoptionOperatorService struct {
	scans   int
	imports int
}

func (s *recordingAdoptionOperatorService) Scan(context.Context, service.AdoptionScanRequest) ([]service.AdoptionPreview, error) {
	s.scans++
	return []service.AdoptionPreview{}, nil
}

func (s *recordingAdoptionOperatorService) Import(context.Context, service.AdoptionImportRequest) ([]service.AdoptionImportResult, error) {
	s.imports++
	return []service.AdoptionImportResult{}, nil
}

func TestAuthorizedContextVMPubkeyFailsClosed(t *testing.T) {
	operator := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	other := testNostrPubKeyHexFromPrivateKey(t, testOtherKey)
	cases := []struct {
		name       string
		pubkey     string
		authorized []string
		want       bool
	}{
		{name: "nil allowlist denies", pubkey: operator, authorized: nil, want: false},
		{name: "empty allowlist denies", pubkey: operator, authorized: []string{}, want: false},
		{name: "blank requester denies", pubkey: "  ", authorized: []string{operator}, want: false},
		{name: "unlisted requester denies", pubkey: other, authorized: []string{operator}, want: false},
		{name: "listed requester allowed", pubkey: operator, authorized: []string{operator}, want: true},
		{name: "requester case-normalized", pubkey: strings.ToUpper(operator), authorized: []string{operator}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := authorizedContextVMPubkey(tc.pubkey, tc.authorized); got != tc.want {
				t.Fatalf("authorizedContextVMPubkey(%q, %v) = %v, want %v", tc.pubkey, tc.authorized, got, tc.want)
			}
		})
	}
}

func TestOperatorContextVMHandlers_EmptyAllowlistsDenyAnySigner(t *testing.T) {
	adoption := &recordingAdoptionOperatorService{}
	handlers := NewOperatorContextVMHandlers(OperatorContextVMHandlersConfig{Adoption: adoption})
	event := makeContextVMEvent(t, testRequesterKey, `{}`)
	request := ContextVMRequest{Event: event, RPC: ContextVMJSONRPCRequest{Params: []byte(`{"targets":[{"name":"prod","endpoint_ref":"prod-docker"}]}`)}}

	if _, err := handlers.AdoptionScan(context.Background(), request); err == nil || !strings.Contains(err.Error(), "authorized adoption list") {
		t.Fatalf("AdoptionScan with empty allowlist error = %v, want authorization failure", err)
	}
	if _, err := handlers.AdoptionImport(context.Background(), request); err == nil || !strings.Contains(err.Error(), "authorized adoption list") {
		t.Fatalf("AdoptionImport with empty allowlist error = %v, want authorization failure", err)
	}
	if _, err := handlers.ServiceAction(context.Background(), request); err == nil || !strings.Contains(err.Error(), "authorized direct-runtime list") {
		t.Fatalf("ServiceAction with empty allowlist error = %v, want authorization failure", err)
	}
	if adoption.scans != 0 || adoption.imports != 0 {
		t.Fatalf("adoption service reached with empty allowlist: scans=%d imports=%d", adoption.scans, adoption.imports)
	}
}

func TestOperatorContextVMHandlers_AdoptionAllowlistIsNotDirectRuntimeAllowlist(t *testing.T) {
	operator := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	adoption := &recordingAdoptionOperatorService{}
	handlers := NewOperatorContextVMHandlers(OperatorContextVMHandlersConfig{
		Adoption:                  adoption,
		AdoptionAuthorizedPubkeys: []string{operator},
	})
	event := makeContextVMEvent(t, testRequesterKey, `{}`)
	request := ContextVMRequest{Event: event, RPC: ContextVMJSONRPCRequest{Params: []byte(`{"targets":[{"name":"prod","endpoint_ref":"prod-docker"}]}`)}}

	if _, err := handlers.AdoptionScan(context.Background(), request); err != nil {
		t.Fatalf("AdoptionScan for allowlisted operator: %v", err)
	}
	if adoption.scans != 1 {
		t.Fatalf("adoption scans = %d, want 1", adoption.scans)
	}
	if _, err := handlers.ServiceAction(context.Background(), request); err == nil || !strings.Contains(err.Error(), "authorized direct-runtime list") {
		t.Fatalf("ServiceAction with empty direct-runtime allowlist error = %v, want authorization failure", err)
	}
}

// TestContextVMTransport_OperatorMethodsFailClosedWithoutTransportPrefilter is
// the end-to-end regression for bahia-9sav5: with nostr.authorized_pubkeys
// empty (transport pre-filter disabled) and an empty adoption allowlist, a
// signed adoption/scan must be rejected instead of reaching the service.
func TestContextVMTransport_OperatorMethodsFailClosedWithoutTransportPrefilter(t *testing.T) {
	operator := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	content := `{"jsonrpc":"2.0","id":"scan-1","method":"adoption/scan","params":{"targets":[{"name":"prod","endpoint_ref":"prod-docker"}]}}`

	t.Run("empty adoption allowlist rejects any signer", func(t *testing.T) {
		publisher := &mockEncryptedPublisher{}
		adoption := &recordingAdoptionOperatorService{}
		transport := NewEncryptedRequestTransport(nil, newResponder(t, publisher), nil, zap.NewNop())
		NewOperatorContextVMHandlers(OperatorContextVMHandlersConfig{Adoption: adoption}).Register(transport)

		transport.HandleEvent(context.Background(), makeContextVMEvent(t, testRequesterKey, content))

		if adoption.scans != 0 {
			t.Fatalf("adoption service reached by unallowlisted signer")
		}
		if len(publisher.events) == 0 {
			t.Fatalf("expected an error response")
		}
		response := contextVMResponse(t, publisher.events[len(publisher.events)-1])
		if response.Error == nil || !strings.Contains(response.Error.Message, "authorized adoption list") {
			t.Fatalf("unexpected response: %+v", response)
		}
	})

	t.Run("allowlisted operator reaches adoption service", func(t *testing.T) {
		publisher := &mockEncryptedPublisher{}
		adoption := &recordingAdoptionOperatorService{}
		transport := NewEncryptedRequestTransport(nil, newResponder(t, publisher), nil, zap.NewNop())
		NewOperatorContextVMHandlers(OperatorContextVMHandlersConfig{Adoption: adoption, AdoptionAuthorizedPubkeys: []string{operator}}).Register(transport)

		transport.HandleEvent(context.Background(), makeContextVMEvent(t, testRequesterKey, content))

		if adoption.scans != 1 {
			t.Fatalf("adoption scans = %d, want 1", adoption.scans)
		}
		response := contextVMResponse(t, publisher.events[len(publisher.events)-1])
		if response.Error != nil {
			t.Fatalf("unexpected error response: %+v", response.Error)
		}
	})

	t.Run("other signer rejected even when an operator is allowlisted", func(t *testing.T) {
		publisher := &mockEncryptedPublisher{}
		adoption := &recordingAdoptionOperatorService{}
		transport := NewEncryptedRequestTransport(nil, newResponder(t, publisher), nil, zap.NewNop())
		NewOperatorContextVMHandlers(OperatorContextVMHandlersConfig{Adoption: adoption, AdoptionAuthorizedPubkeys: []string{operator}}).Register(transport)

		transport.HandleEvent(context.Background(), makeContextVMEvent(t, testOtherKey, content))

		if adoption.scans != 0 {
			t.Fatalf("adoption service reached by unallowlisted signer")
		}
	})
}
