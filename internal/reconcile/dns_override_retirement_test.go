package reconcile

import (
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

// TestRetiredOverrideRestoresProjectedRecord encodes the live acceptance
// condition for retiring override 1273e277 on sharegap.net.
//
// While an override is ACTIVE it shadows the Bahia-projected record for the same
// name/type. Retirement makes the override inactive, so the override read path
// stops returning it, and the Bahia-owned projected record must reappear
// unchanged. The zone must never be left without a record for astillero.
func TestRetiredOverrideRestoresProjectedRecord(t *testing.T) {
	zone := domain.DNSZone{Name: "sharegap.net"}
	projected := []domain.DNSRecord{{
		Zone:             "sharegap.net",
		Name:             "astillero",
		FQDN:             "astillero.sharegap.net",
		Type:             domain.DNSRecordTypeA,
		Value:            "192.168.40.104",
		TTL:              300,
		SourceCoordinate: "service:astillero",
	}}
	override := domain.DNSRecordOverride{
		ID:         uuid.MustParse("1273e277-dfa7-4459-a452-89598eeca4a2"),
		ZoneName:   "sharegap.net",
		RecordName: "astillero",
		RecordType: domain.DNSRecordTypeA,
		Value:      "192.168.40.104",
		TTL:        60,
	}

	// Active override: it shadows the projected record.
	withOverride := applyDNSRecordOverrides(zone, append([]domain.DNSRecord(nil), projected...), []domain.DNSRecordOverride{override})
	if len(withOverride) != 1 {
		t.Fatalf("expected exactly one record while overridden, got %d", len(withOverride))
	}
	if got := withOverride[0].SourceCoordinate; got != "manual_override:"+override.ID.String() {
		t.Fatalf("active override should own the record, got source %q", got)
	}

	// Retired override: ListByZone no longer returns it, so no overrides apply.
	afterRetirement := applyDNSRecordOverrides(zone, append([]domain.DNSRecord(nil), projected...), nil)
	if len(afterRetirement) != 1 {
		t.Fatalf("projected record must survive retirement, got %d records", len(afterRetirement))
	}
	restored := afterRetirement[0]
	if restored.SourceCoordinate != "service:astillero" {
		t.Fatalf("expected the Bahia-projected record to be restored, got source %q", restored.SourceCoordinate)
	}
	if restored.FQDN != "astillero.sharegap.net" || restored.Value != "192.168.40.104" {
		t.Fatalf("projected record altered by retirement: %+v", restored)
	}
	if restored.TTL != 300 {
		t.Fatalf("projected TTL should be the projected value, not the override's: %d", restored.TTL)
	}
}
