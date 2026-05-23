package dns

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
	clientv3 "go.etcd.io/etcd/client/v3"
)

const defaultCoreDNSEtcdPrefix = "/skydns"

// CoreDNSConfig contains the etcd settings for the CoreDNS etcd plugin backend.
type CoreDNSConfig struct {
	EtcdEndpoints []string
	EtcdPrefix    string
	DialTimeout   time.Duration
}

// CoreDNSBackend stores DNS zone snapshots in the CoreDNS etcd plugin layout.
type CoreDNSBackend struct {
	endpoints   []string
	prefix      string
	dialTimeout time.Duration
	kv          coreDNSKV
}

type coreDNSKV interface {
	Health(ctx context.Context, prefix string) error
	GetPrefix(ctx context.Context, prefix string) ([]coreDNSKVPair, error)
	ReplacePrefix(ctx context.Context, exactKey string, childPrefix string, values map[string]string) error
}

type coreDNSKVPair struct {
	Key   string
	Value string
}

type etcdCoreDNSKV struct {
	client *clientv3.Client
}

type coreDNSServicePayload struct {
	Host             string `json:"host"`
	TTL              int    `json:"ttl,omitempty"`
	Port             *int   `json:"port,omitempty"`
	Priority         *int   `json:"priority,omitempty"`
	Weight           *int   `json:"weight,omitempty"`
	RecordType       string `json:"bahia_record_type,omitempty"`
	SourceCoordinate string `json:"bahia_source_coordinate,omitempty"`
}

// NewCoreDNSBackend creates a CoreDNS etcd backend.
func NewCoreDNSBackend(cfg CoreDNSConfig) (*CoreDNSBackend, error) {
	endpoints := normalizeCoreDNSEndpoints(cfg.EtcdEndpoints)
	if len(endpoints) == 0 {
		return nil, fmt.Errorf("CoreDNS etcd endpoints are required")
	}
	prefix := normalizeCoreDNSPrefix(cfg.EtcdPrefix)
	if cfg.DialTimeout < 0 {
		return nil, fmt.Errorf("CoreDNS etcd dial timeout must not be negative")
	}
	dialTimeout := cfg.DialTimeout
	if dialTimeout == 0 {
		dialTimeout = 5 * time.Second
	}
	client, err := clientv3.New(clientv3.Config{Endpoints: endpoints, DialTimeout: dialTimeout})
	if err != nil {
		return nil, fmt.Errorf("create CoreDNS etcd client: %w", err)
	}
	return &CoreDNSBackend{
		endpoints:   endpoints,
		prefix:      prefix,
		dialTimeout: dialTimeout,
		kv:          &etcdCoreDNSKV{client: client},
	}, nil
}

func (b *CoreDNSBackend) BackendType() domain.DNSBackendType {
	return domain.DNSBackendTypeCoreDNS
}

func (b *CoreDNSBackend) Health(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	kv, err := b.store()
	if err != nil {
		return err
	}
	if err := kv.Health(ctx, b.prefix); err != nil {
		return fmt.Errorf("CoreDNS etcd backend health check failed: %w", err)
	}
	return nil
}

func (b *CoreDNSBackend) ListRecords(ctx context.Context, zone domain.DNSZone) ([]domain.DNSRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := domain.ValidateDNSZone(&zone); err != nil {
		return nil, err
	}
	kv, err := b.store()
	if err != nil {
		return nil, err
	}
	zoneKey, err := b.zoneKey(zone.Name)
	if err != nil {
		return nil, err
	}
	pairs, err := kv.GetPrefix(ctx, zoneKey)
	if err != nil {
		return nil, fmt.Errorf("list CoreDNS zone %q records: %w", zone.Name, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	records := make([]domain.DNSRecord, 0, len(pairs))
	for _, pair := range pairs {
		if pair.Key != zoneKey && !strings.HasPrefix(pair.Key, zoneKey+"/") {
			continue
		}
		fqdn, err := fqdnFromCoreDNSKey(b.prefix, pair.Key)
		if err != nil {
			return nil, err
		}
		var payload coreDNSServicePayload
		if err := json.Unmarshal([]byte(pair.Value), &payload); err != nil {
			return nil, fmt.Errorf("decode CoreDNS record %q: %w", pair.Key, err)
		}
		payload.Host = strings.TrimSpace(payload.Host)
		if payload.Host == "" {
			return nil, fmt.Errorf("CoreDNS record %q host is required", pair.Key)
		}
		recordType, err := decodeCoreDNSRecordType(payload)
		if err != nil {
			return nil, fmt.Errorf("decode CoreDNS record %q type: %w", pair.Key, err)
		}
		ttl := payload.TTL
		if ttl <= 0 {
			ttl = zone.TTL
		}
		records = append(records, domain.DNSRecord{
			Zone:             zone.Name,
			Name:             relativeDNSName(fqdn, zone.Name),
			FQDN:             fqdn,
			Type:             recordType,
			Value:            payload.Host,
			TTL:              ttl,
			Priority:         payload.Priority,
			Weight:           payload.Weight,
			Port:             payload.Port,
			SourceCoordinate: payload.SourceCoordinate,
		})
	}
	return sortedRecords(records), nil
}

func (b *CoreDNSBackend) SyncZone(ctx context.Context, zone domain.DNSZone, records []domain.DNSRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := domain.ValidateDNSZone(&zone); err != nil {
		return err
	}
	kv, err := b.store()
	if err != nil {
		return err
	}
	zoneKey, err := b.zoneKey(zone.Name)
	if err != nil {
		return err
	}
	values := make(map[string]string, len(records))
	for _, record := range sortedRecords(records) {
		key, payload, err := b.recordKV(zone, record)
		if err != nil {
			return err
		}
		if _, exists := values[key]; exists {
			return fmt.Errorf("CoreDNS record key %q is duplicated", key)
		}
		values[key] = payload
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := kv.ReplacePrefix(ctx, zoneKey, zoneKey+"/", values); err != nil {
		return fmt.Errorf("sync CoreDNS zone %q: %w", zone.Name, err)
	}
	return nil
}

func (b *CoreDNSBackend) store() (coreDNSKV, error) {
	if b == nil || b.kv == nil {
		return nil, fmt.Errorf("CoreDNS etcd client is required")
	}
	if b.prefix == "" {
		b.prefix = defaultCoreDNSEtcdPrefix
	}
	return b.kv, nil
}

func (b *CoreDNSBackend) zoneKey(zoneName string) (string, error) {
	return coreDNSKeyForFQDN(b.prefix, zoneName)
}

func (b *CoreDNSBackend) recordKV(zone domain.DNSZone, record domain.DNSRecord) (string, string, error) {
	record.Zone = strings.TrimSpace(record.Zone)
	if record.Zone == "" {
		record.Zone = zone.Name
	}
	if record.Zone != zone.Name {
		return "", "", fmt.Errorf("DNS record %q zone %q does not match sync zone %q", record.FQDN, record.Zone, zone.Name)
	}
	fqdn := strings.TrimSpace(record.FQDN)
	if fqdn == "" {
		fqdn = fqdnFromRecordName(record.Name, zone.Name)
	}
	if fqdn == "" {
		return "", "", fmt.Errorf("DNS record FQDN is required")
	}
	if !strings.HasSuffix(fqdn, zone.Name) || (fqdn != zone.Name && !strings.HasSuffix(fqdn, "."+zone.Name)) {
		return "", "", fmt.Errorf("DNS record FQDN %q is outside zone %q", fqdn, zone.Name)
	}
	if !record.Type.IsValid() {
		return "", "", fmt.Errorf("DNS record %q type %q is not valid", fqdn, record.Type)
	}
	value := strings.TrimSpace(record.Value)
	if value == "" {
		return "", "", fmt.Errorf("DNS record %q value is required", fqdn)
	}
	ttl := record.TTL
	if ttl <= 0 {
		ttl = zone.TTL
	}
	key, err := coreDNSKeyForFQDN(b.prefix, fqdn)
	if err != nil {
		return "", "", err
	}
	payload := coreDNSServicePayload{
		Host:             value,
		TTL:              ttl,
		RecordType:       string(record.Type),
		SourceCoordinate: strings.TrimSpace(record.SourceCoordinate),
	}
	if record.Type == domain.DNSRecordTypeSRV {
		if record.Port == nil || record.Priority == nil || record.Weight == nil {
			return "", "", fmt.Errorf("SRV record %q requires port, priority, and weight", fqdn)
		}
		payload = coreDNSServicePayload{Host: value, Port: record.Port, Priority: record.Priority, Weight: record.Weight}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", "", fmt.Errorf("encode CoreDNS record %q: %w", fqdn, err)
	}
	return key, string(encoded), nil
}

func (kv *etcdCoreDNSKV) Health(ctx context.Context, prefix string) error {
	if kv == nil || kv.client == nil {
		return fmt.Errorf("etcd client is required")
	}
	_, err := kv.client.Get(ctx, prefix, clientv3.WithPrefix(), clientv3.WithLimit(1))
	return err
}

func (kv *etcdCoreDNSKV) GetPrefix(ctx context.Context, prefix string) ([]coreDNSKVPair, error) {
	if kv == nil || kv.client == nil {
		return nil, fmt.Errorf("etcd client is required")
	}
	resp, err := kv.client.Get(ctx, prefix, clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	pairs := make([]coreDNSKVPair, 0, len(resp.Kvs))
	for _, item := range resp.Kvs {
		pairs = append(pairs, coreDNSKVPair{Key: string(item.Key), Value: string(item.Value)})
	}
	return pairs, nil
}

func (kv *etcdCoreDNSKV) ReplacePrefix(ctx context.Context, exactKey string, childPrefix string, values map[string]string) error {
	if kv == nil || kv.client == nil {
		return fmt.Errorf("etcd client is required")
	}
	ops := make([]clientv3.Op, 0, len(values)+2)
	ops = append(ops, clientv3.OpDelete(exactKey), clientv3.OpDelete(childPrefix, clientv3.WithPrefix()))
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		ops = append(ops, clientv3.OpPut(key, values[key]))
	}
	resp, err := kv.client.Txn(ctx).Then(ops...).Commit()
	if err != nil {
		return err
	}
	if resp == nil || !resp.Succeeded {
		return fmt.Errorf("etcd transaction did not commit")
	}
	return nil
}

func coreDNSKeyForFQDN(prefix string, fqdn string) (string, error) {
	prefix = normalizeCoreDNSPrefix(prefix)
	fqdn = strings.Trim(strings.ToLower(strings.TrimSpace(fqdn)), ".")
	if fqdn == "" {
		return "", fmt.Errorf("DNS FQDN is required")
	}
	labels := strings.Split(fqdn, ".")
	parts := make([]string, 0, len(labels)+1)
	parts = append(parts, prefix)
	for i := len(labels) - 1; i >= 0; i-- {
		label := strings.TrimSpace(labels[i])
		if label == "" || strings.Contains(label, "/") {
			return "", fmt.Errorf("DNS FQDN %q contains an invalid label", fqdn)
		}
		parts = append(parts, label)
	}
	return path.Join(parts...), nil
}

func fqdnFromCoreDNSKey(prefix string, key string) (string, error) {
	prefix = normalizeCoreDNSPrefix(prefix)
	key = path.Clean("/" + strings.TrimSpace(key))
	if key != prefix && !strings.HasPrefix(key, prefix+"/") {
		return "", fmt.Errorf("CoreDNS key %q is outside prefix %q", key, prefix)
	}
	trimmed := strings.TrimPrefix(key, prefix)
	trimmed = strings.Trim(trimmed, "/")
	if trimmed == "" {
		return "", fmt.Errorf("CoreDNS key %q does not contain DNS labels", key)
	}
	labels := strings.Split(trimmed, "/")
	for i := range labels {
		if strings.TrimSpace(labels[i]) == "" {
			return "", fmt.Errorf("CoreDNS key %q contains an empty DNS label", key)
		}
	}
	for i, j := 0, len(labels)-1; i < j; i, j = i+1, j-1 {
		labels[i], labels[j] = labels[j], labels[i]
	}
	return strings.Join(labels, "."), nil
}

func decodeCoreDNSRecordType(payload coreDNSServicePayload) (domain.DNSRecordType, error) {
	if payload.Port != nil || payload.Priority != nil || payload.Weight != nil {
		return domain.DNSRecordTypeSRV, nil
	}
	if payload.RecordType != "" {
		recordType := domain.DNSRecordType(strings.ToUpper(strings.TrimSpace(payload.RecordType)))
		if !recordType.IsValid() {
			return "", fmt.Errorf("%q is not valid", payload.RecordType)
		}
		return recordType, nil
	}
	ip := net.ParseIP(payload.Host)
	if ip == nil {
		return domain.DNSRecordTypeCNAME, nil
	}
	if ip.To4() != nil {
		return domain.DNSRecordTypeA, nil
	}
	return domain.DNSRecordTypeAAAA, nil
}

func fqdnFromRecordName(name string, zoneName string) string {
	name = strings.Trim(strings.ToLower(strings.TrimSpace(name)), ".")
	zoneName = strings.Trim(strings.ToLower(strings.TrimSpace(zoneName)), ".")
	if name == "" || name == "@" {
		return zoneName
	}
	return name + "." + zoneName
}

func relativeDNSName(fqdn string, zoneName string) string {
	fqdn = strings.Trim(strings.ToLower(strings.TrimSpace(fqdn)), ".")
	zoneName = strings.Trim(strings.ToLower(strings.TrimSpace(zoneName)), ".")
	if fqdn == zoneName {
		return "@"
	}
	return strings.TrimSuffix(fqdn, "."+zoneName)
}

func normalizeCoreDNSPrefix(prefix string) string {
	prefix = path.Clean("/" + strings.Trim(strings.TrimSpace(prefix), "/"))
	if prefix == "/." || prefix == "/" {
		return defaultCoreDNSEtcdPrefix
	}
	return prefix
}

func normalizeCoreDNSEndpoints(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	normalized := make([]string, 0, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	return normalized
}

var _ Backend = (*CoreDNSBackend)(nil)
