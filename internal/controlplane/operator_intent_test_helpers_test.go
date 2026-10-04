package controlplane

import (
	"context"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/service"
)

type stubAdoptionOperatorService struct {
	scanReq      service.AdoptionScanRequest
	scanResp     []service.AdoptionPreview
	scanErr      error
	scanCalled   bool
	importReq    service.AdoptionImportRequest
	importResp   []service.AdoptionImportResult
	importErr    error
	importCalled bool
}

func (s *stubAdoptionOperatorService) Scan(_ context.Context, req service.AdoptionScanRequest) ([]service.AdoptionPreview, error) {
	s.scanCalled = true
	s.scanReq = req
	return s.scanResp, s.scanErr
}

func (s *stubAdoptionOperatorService) Import(_ context.Context, req service.AdoptionImportRequest) ([]service.AdoptionImportResult, error) {
	s.importCalled = true
	s.importReq = req
	return s.importResp, s.importErr
}
func assertSignedEvent(t *testing.T, ev nostr.Event) {
	t.Helper()
	if !ev.VerifySignature() {
		t.Fatalf("published event signature invalid")
	}
}

func assertNoLegacyStatusResultEvents(t *testing.T, events []nostr.Event) {
	t.Helper()
	for _, ev := range events {
		if (ev.Kind >= 6961 && ev.Kind <= 6999) || (ev.Kind >= 7961 && ev.Kind <= 7999) || (ev.Kind >= 38390 && ev.Kind <= 38499) || (ev.Kind >= 31900 && ev.Kind <= 32099) {
			t.Fatalf("production command path published legacy runtime kind %d: %#v", ev.Kind, ev)
		}
	}
}
