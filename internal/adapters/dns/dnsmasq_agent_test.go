package dns

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/dnsagent/protocol"
	"github.com/openagentsinc/bahia/internal/domain"
	bahiaclient "github.com/openagentsinc/bahia/pkg/client"
)

type fakeContextVMRequester struct {
	requests []fakeContextVMRequest
	resultFn func(method string, params any) (any, error)
}

type fakeContextVMRequest struct {
	method string
	params any
}

func (f *fakeContextVMRequester) Request(_ context.Context, method string, params any, _ nostr.Tags, _ func(bahiaclient.OperatorStatusEvent)) (*nostr.Event, error) {
	f.requests = append(f.requests, fakeContextVMRequest{method: method, params: params})
	result, err := f.resultFn(method, params)
	if err != nil {
		return nil, err
	}
	content, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return &nostr.Event{Content: string(content)}, nil
}

func TestDnsmasqAgentHealth(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		requester := &fakeContextVMRequester{resultFn: func(method string, params any) (any, error) {
			if method != protocol.MethodHealth {
				t.Fatalf("method = %q", method)
			}
			if got := params.(protocol.HealthParams).Schema; got != protocol.Schema {
				t.Fatalf("schema = %q", got)
			}
			return protocol.HealthResult{Schema: protocol.Schema, Status: "ok"}, nil
		}}
		backend, err := NewDnsmasqAgentBackend(requester)
		if err != nil {
			t.Fatal(err)
		}
		if err := backend.Health(context.Background()); err != nil {
			t.Fatalf("health: %v", err)
		}
	})

	t.Run("request error", func(t *testing.T) {
		requester := &fakeContextVMRequester{resultFn: func(string, any) (any, error) { return nil, errors.New("relay unavailable") }}
		backend, _ := NewDnsmasqAgentBackend(requester)
		err := backend.Health(context.Background())
		if err == nil || !strings.Contains(err.Error(), "relay unavailable") {
			t.Fatalf("health error = %v", err)
		}
	})

	t.Run("remote JSON-RPC error", func(t *testing.T) {
		remoteErr := &bahiaclient.ContextVMRemoteError{Code: -32001, Message: "zone not allowed"}
		requester := &fakeContextVMRequester{resultFn: func(string, any) (any, error) { return nil, remoteErr }}
		backend, _ := NewDnsmasqAgentBackend(requester)
		err := backend.Health(context.Background())
		if err == nil || !strings.Contains(err.Error(), "zone not allowed") {
			t.Fatalf("health error = %v", err)
		}
		var got *bahiaclient.ContextVMRemoteError
		if !errors.As(err, &got) || got.Code != -32001 {
			t.Fatalf("remote error not preserved: %v", err)
		}
	})

	t.Run("non-ok status", func(t *testing.T) {
		requester := &fakeContextVMRequester{resultFn: func(string, any) (any, error) {
			return protocol.HealthResult{Schema: protocol.Schema, Status: "degraded"}, nil
		}}
		backend, _ := NewDnsmasqAgentBackend(requester)
		err := backend.Health(context.Background())
		if err == nil || !strings.Contains(err.Error(), "degraded") {
			t.Fatalf("health error = %v", err)
		}
	})
}

func TestDnsmasqAgentListZoneStateDecodesResult(t *testing.T) {
	zone := dnsmasqAgentTestZone()
	want := []domain.DNSRecord{{Zone: zone.Name, Name: "api", FQDN: "api.example.test", Type: domain.DNSRecordTypeA, Value: "10.0.0.4", TTL: 60}}
	requester := &fakeContextVMRequester{resultFn: func(method string, params any) (any, error) {
		if method != protocol.MethodList || params.(protocol.ListParams).Zone != zone {
			t.Fatalf("request = %q %#v", method, params)
		}
		return protocol.ListResult{Schema: protocol.Schema, Records: want, Serial: 7, Authoritative: true}, nil
	}}
	backend, _ := NewDnsmasqAgentBackend(requester)
	got, authoritative, err := backend.ListZoneState(context.Background(), zone)
	if err != nil {
		t.Fatalf("list zone state: %v", err)
	}
	if !reflect.DeepEqual(got, want) || !authoritative {
		t.Fatalf("state = records %#v authoritative %t, want records %#v authoritative", got, authoritative, want)
	}
}

func TestDnsmasqAgentSyncZoneUsesMonotonicSerialsAndDecodesResult(t *testing.T) {
	zone := dnsmasqAgentTestZone()
	records := []domain.DNSRecord{{Zone: zone.Name, Name: "api", Type: domain.DNSRecordTypeA, Value: "10.0.0.4"}}
	var serials []int64
	requester := &fakeContextVMRequester{resultFn: func(method string, params any) (any, error) {
		if method != protocol.MethodSync {
			t.Fatalf("method = %q", method)
		}
		request := params.(protocol.SyncParams)
		serials = append(serials, request.Serial)
		return protocol.SyncResult{Schema: protocol.Schema, Status: "ok", Changed: true, Serial: request.Serial}, nil
	}}
	times := []time.Time{time.Unix(0, 100), time.Unix(0, 100), time.Unix(0, 99)}
	index := 0
	backend, err := newDnsmasqAgentBackend(requester, func() time.Time {
		value := times[index]
		index++
		return value
	})
	if err != nil {
		t.Fatal(err)
	}
	for range times {
		if err := backend.SyncZone(context.Background(), zone, records); err != nil {
			t.Fatalf("sync zone: %v", err)
		}
	}
	if want := []int64{100, 101, 102}; !reflect.DeepEqual(serials, want) {
		t.Fatalf("serials = %v, want %v", serials, want)
	}
}

// fakeSyncAgent replays the real agent's serial semantics: stale replies carry
// the agent's last applied serial, equal serials are idempotent no-ops.
type fakeSyncAgent struct {
	applied bool
	serial  int64
	serials []int64
}

func (a *fakeSyncAgent) handle(t *testing.T, method string, params any) (any, error) {
	t.Helper()
	if method != protocol.MethodSync {
		t.Fatalf("method = %q", method)
	}
	request := params.(protocol.SyncParams)
	a.serials = append(a.serials, request.Serial)
	if a.applied && request.Serial < a.serial {
		return protocol.SyncResult{Schema: protocol.Schema, Status: protocol.SyncStatusStale, Changed: false, Serial: a.serial}, nil
	}
	if a.applied && request.Serial == a.serial {
		return protocol.SyncResult{Schema: protocol.Schema, Status: protocol.SyncStatusOK, Changed: false, Serial: a.serial}, nil
	}
	a.applied = true
	a.serial = request.Serial
	return protocol.SyncResult{Schema: protocol.Schema, Status: protocol.SyncStatusOK, Changed: true, Serial: request.Serial}, nil
}

func TestDnsmasqAgentSyncZoneRecoversFromClockStepBack(t *testing.T) {
	zone := dnsmasqAgentTestZone()
	records := []domain.DNSRecord{{Zone: zone.Name, Name: "api", Type: domain.DNSRecordTypeA, Value: "10.0.0.4"}}
	syncAgent := &fakeSyncAgent{}
	requester := &fakeContextVMRequester{resultFn: func(method string, params any) (any, error) {
		return syncAgent.handle(t, method, params)
	}}

	// First Bahia process syncs at wall-clock serial 1000.
	before, err := newDnsmasqAgentBackend(requester, func() time.Time { return time.Unix(0, 1000) })
	if err != nil {
		t.Fatal(err)
	}
	if err := before.SyncZone(context.Background(), zone, records); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	// Bahia restarts (fresh in-memory serial map) with the clock stepped back.
	after, err := newDnsmasqAgentBackend(requester, func() time.Time { return time.Unix(0, 500) })
	if err != nil {
		t.Fatal(err)
	}
	if err := after.SyncZone(context.Background(), zone, records); err != nil {
		t.Fatalf("sync after clock step-back did not recover: %v", err)
	}
	// Subsequent syncs continue above the recovered serial without retries.
	if err := after.SyncZone(context.Background(), zone, records); err != nil {
		t.Fatalf("follow-up sync: %v", err)
	}

	if want := []int64{1000, 500, 1001, 1002}; !reflect.DeepEqual(syncAgent.serials, want) {
		t.Fatalf("serials = %v, want %v", syncAgent.serials, want)
	}
}

func TestDnsmasqAgentSyncZoneErrorsWhenStillStaleAfterRetry(t *testing.T) {
	zone := dnsmasqAgentTestZone()
	requester := &fakeContextVMRequester{resultFn: func(method string, params any) (any, error) {
		return protocol.SyncResult{Schema: protocol.Schema, Status: protocol.SyncStatusStale, Changed: false, Serial: 9000}, nil
	}}
	backend, err := newDnsmasqAgentBackend(requester, func() time.Time { return time.Unix(0, 100) })
	if err != nil {
		t.Fatal(err)
	}
	syncErr := backend.SyncZone(context.Background(), zone, nil)
	if syncErr == nil || !strings.Contains(syncErr.Error(), "still stale after recovery retry") {
		t.Fatalf("sync error = %v", syncErr)
	}
	if len(requester.requests) != 2 {
		t.Fatalf("requests = %d, want exactly one retry", len(requester.requests))
	}
}

func TestDnsmasqAgentListRecordsSeedsSerialFloor(t *testing.T) {
	zone := dnsmasqAgentTestZone()
	syncAgent := &fakeSyncAgent{applied: true, serial: 900}
	requester := &fakeContextVMRequester{resultFn: func(method string, params any) (any, error) {
		if method == protocol.MethodList {
			return protocol.ListResult{Schema: protocol.Schema, Records: []domain.DNSRecord{}, Serial: 900}, nil
		}
		return syncAgent.handle(t, method, params)
	}}
	backend, err := newDnsmasqAgentBackend(requester, func() time.Time { return time.Unix(0, 100) })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backend.ListRecords(context.Background(), zone); err != nil {
		t.Fatal(err)
	}
	if err := backend.SyncZone(context.Background(), zone, nil); err != nil {
		t.Fatalf("sync after list seeding: %v", err)
	}
	if want := []int64{901}; !reflect.DeepEqual(syncAgent.serials, want) {
		t.Fatalf("serials = %v, want %v (seeded from list, no stale retry)", syncAgent.serials, want)
	}
}

func TestDnsmasqAgentSurfacesSchemaMismatch(t *testing.T) {
	requester := &fakeContextVMRequester{resultFn: func(string, any) (any, error) {
		return protocol.ListResult{Schema: "bahia.dnsagent.v2"}, nil
	}}
	backend, _ := NewDnsmasqAgentBackend(requester)
	_, err := backend.ListRecords(context.Background(), dnsmasqAgentTestZone())
	if err == nil || !strings.Contains(err.Error(), "unsupported DNS agent schema") {
		t.Fatalf("schema error = %v", err)
	}
}

func dnsmasqAgentTestZone() domain.DNSZone {
	return domain.DNSZone{Name: "example.test", Visibility: domain.ZoneVisibilityInternal, BackendRef: "remote", TTL: 60}
}
