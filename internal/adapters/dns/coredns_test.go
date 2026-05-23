package dns

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
)

type mockCoreDNSKV struct {
	values     map[string]string
	healthErr  error
	getErr     error
	replaceErr error
}

func newMockCoreDNSBackend() (*CoreDNSBackend, *mockCoreDNSKV) {
	kv := &mockCoreDNSKV{values: map[string]string{}}
	return &CoreDNSBackend{prefix: defaultCoreDNSEtcdPrefix, kv: kv}, kv
}

func (m *mockCoreDNSKV) Health(ctx context.Context, prefix string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return m.healthErr
}

func (m *mockCoreDNSKV) GetPrefix(ctx context.Context, prefix string) ([]coreDNSKVPair, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if m.getErr != nil {
		return nil, m.getErr
	}
	keys := make([]string, 0, len(m.values))
	for key := range m.values {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	pairs := make([]coreDNSKVPair, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, coreDNSKVPair{Key: key, Value: m.values[key]})
	}
	return pairs, nil
}

func (m *mockCoreDNSKV) ReplacePrefix(ctx context.Context, exactKey string, childPrefix string, values map[string]string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.replaceErr != nil {
		return m.replaceErr
	}
	for key := range m.values {
		if key == exactKey || strings.HasPrefix(key, childPrefix) {
			delete(m.values, key)
		}
	}
	for key, value := range values {
		m.values[key] = value
	}
	return nil
}

func TestCoreDNSReverseDomainKeyGeneration(t *testing.T) {
	got, err := coreDNSKeyForFQDN("/skydns", "drydock-review.prod.cascadia")
	if err != nil {
		t.Fatalf("key for fqdn: %v", err)
	}
	want := "/skydns/cascadia/prod/drydock-review"
	if got != want {
		t.Fatalf("key mismatch: got %q want %q", got, want)
	}
}

func TestCoreDNSSyncZoneReplacesPriorSnapshot(t *testing.T) {
	backend, _ := newMockCoreDNSBackend()
	zone := testCoreDNSZone("prod.cascadia")
	oldRecords := []domain.DNSRecord{{Zone: zone.Name, Name: "old", FQDN: "old.prod.cascadia", Type: domain.DNSRecordTypeA, Value: "10.0.0.1", TTL: 300, SourceCoordinate: "endpoint:service:old:prod"}}
	newRecords := []domain.DNSRecord{{Zone: zone.Name, Name: "new", FQDN: "new.prod.cascadia", Type: domain.DNSRecordTypeA, Value: "10.0.0.2", TTL: 300, SourceCoordinate: "endpoint:service:new:prod"}}

	if err := backend.SyncZone(context.Background(), zone, oldRecords); err != nil {
		t.Fatalf("sync old records: %v", err)
	}
	if err := backend.SyncZone(context.Background(), zone, newRecords); err != nil {
		t.Fatalf("sync new records: %v", err)
	}
	got, err := backend.ListRecords(context.Background(), zone)
	if err != nil {
		t.Fatalf("list records: %v", err)
	}
	if !reflect.DeepEqual(got, newRecords) {
		t.Fatalf("expected replacement snapshot\ngot  %#v\nwant %#v", got, newRecords)
	}
}

func TestCoreDNSListRecordsRoundTripsRecordTypes(t *testing.T) {
	backend, _ := newMockCoreDNSBackend()
	zone := testCoreDNSZone("prod.cascadia")
	want := []domain.DNSRecord{
		{Zone: zone.Name, Name: "api", FQDN: "api.prod.cascadia", Type: domain.DNSRecordTypeA, Value: "10.0.0.8", TTL: 300, SourceCoordinate: "endpoint:service:api:prod"},
		{Zone: zone.Name, Name: "v6", FQDN: "v6.prod.cascadia", Type: domain.DNSRecordTypeAAAA, Value: "2001:db8::8", TTL: 300, SourceCoordinate: "endpoint:service:v6:prod"},
		{Zone: zone.Name, Name: "worker", FQDN: "worker.prod.cascadia", Type: domain.DNSRecordTypeCNAME, Value: "worker.internal", TTL: 300, SourceCoordinate: "endpoint:worker:worker"},
	}
	if err := backend.SyncZone(context.Background(), zone, []domain.DNSRecord{want[2], want[0], want[1]}); err != nil {
		t.Fatalf("sync zone: %v", err)
	}
	got, err := backend.ListRecords(context.Background(), zone)
	if err != nil {
		t.Fatalf("list records: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("records mismatch\ngot  %#v\nwant %#v", got, want)
	}
}

func TestCoreDNSPrefixIsolationBetweenZones(t *testing.T) {
	backend, _ := newMockCoreDNSBackend()
	prod := testCoreDNSZone("prod.cascadia")
	qa := testCoreDNSZone("qa.cascadia")
	prodInitial := []domain.DNSRecord{{Zone: prod.Name, Name: "api", FQDN: "api.prod.cascadia", Type: domain.DNSRecordTypeA, Value: "10.0.0.1", TTL: 300}}
	qaRecords := []domain.DNSRecord{{Zone: qa.Name, Name: "api", FQDN: "api.qa.cascadia", Type: domain.DNSRecordTypeA, Value: "10.0.1.1", TTL: 300}}
	prodReplacement := []domain.DNSRecord{{Zone: prod.Name, Name: "new", FQDN: "new.prod.cascadia", Type: domain.DNSRecordTypeA, Value: "10.0.0.2", TTL: 300}}

	if err := backend.SyncZone(context.Background(), prod, prodInitial); err != nil {
		t.Fatalf("sync prod records: %v", err)
	}
	if err := backend.SyncZone(context.Background(), qa, qaRecords); err != nil {
		t.Fatalf("sync qa records: %v", err)
	}
	if err := backend.SyncZone(context.Background(), prod, prodReplacement); err != nil {
		t.Fatalf("replace prod records: %v", err)
	}
	gotQA, err := backend.ListRecords(context.Background(), qa)
	if err != nil {
		t.Fatalf("list qa records: %v", err)
	}
	if !reflect.DeepEqual(gotQA, qaRecords) {
		t.Fatalf("qa records were not isolated\ngot  %#v\nwant %#v", gotQA, qaRecords)
	}
	gotProd, err := backend.ListRecords(context.Background(), prod)
	if err != nil {
		t.Fatalf("list prod records: %v", err)
	}
	if !reflect.DeepEqual(gotProd, prodReplacement) {
		t.Fatalf("prod records mismatch\ngot  %#v\nwant %#v", gotProd, prodReplacement)
	}
}

func TestCoreDNSHealthFailureOnClientError(t *testing.T) {
	backend, kv := newMockCoreDNSBackend()
	kv.healthErr = errors.New("etcd unavailable")
	if err := backend.Health(context.Background()); err == nil || !strings.Contains(err.Error(), "etcd unavailable") {
		t.Fatalf("expected health error, got %v", err)
	}
}

func testCoreDNSZone(name string) domain.DNSZone {
	return domain.DNSZone{
		Name:       name,
		Visibility: domain.ZoneVisibilityInternal,
		BackendRef: "coredns-test",
		TTL:        300,
	}
}
