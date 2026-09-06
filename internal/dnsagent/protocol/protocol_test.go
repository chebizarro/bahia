package protocol

import (
	"encoding/json"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
)

func TestSyncParamsRoundTripsAllowEmptyAuthoritativeZoneField(t *testing.T) {
	want := SyncParams{
		Schema: Schema,
		Zone: domain.DNSZone{
			Name:                    "sharegap.net",
			Visibility:              domain.ZoneVisibilityInternal,
			BackendRef:              "core-01",
			TTL:                     300,
			Authoritative:           true,
			AllowEmptyAuthoritative: true,
		},
		Serial: 1,
	}
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal SyncParams: %v", err)
	}
	var got SyncParams
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("unmarshal SyncParams: %v", err)
	}
	if !got.Zone.AllowEmptyAuthoritative {
		t.Fatalf("round-tripped zone = %#v, want allow_empty_authoritative=true", got.Zone)
	}
}
