package dns

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/dnsagent/protocol"
	"github.com/openagentsinc/bahia/internal/domain"
	bahiaclient "github.com/openagentsinc/bahia/pkg/client"
	"go.uber.org/zap"
)

// ContextVMRequester is the request surface used by the remote dnsmasq backend.
type ContextVMRequester interface {
	Request(context.Context, string, any, nostr.Tags, func(bahiaclient.OperatorStatusEvent)) (*nostr.Event, error)
}

// DnsmasqAgentConfig configures a relay-backed DNS agent client.
type DnsmasqAgentConfig struct {
	Relays        []string
	Signer        nostr.Signer
	SenderPubkey  string
	AgentPubkey   string
	Encrypted     bool
	ResultTimeout time.Duration
	ResultRetries *int
}

// DnsmasqAgentBackend manages dnsmasq on a remote resolver through ContextVM.
type DnsmasqAgentBackend struct {
	client ContextVMRequester
	now    func() time.Time

	serialMu sync.Mutex
	serials  map[string]int64
}

var _ Backend = (*DnsmasqAgentBackend)(nil)

// NewDnsmasqAgentBackend constructs a backend with an injected request client.
func NewDnsmasqAgentBackend(requestClient ContextVMRequester) (*DnsmasqAgentBackend, error) {
	return newDnsmasqAgentBackend(requestClient, time.Now)
}

// NewRelayDnsmasqAgentBackend constructs the production relay-backed backend.
func NewRelayDnsmasqAgentBackend(cfg DnsmasqAgentConfig, logger *zap.Logger) (*DnsmasqAgentBackend, error) {
	requestClient, err := bahiaclient.NewContextVMRequestClient(bahiaclient.ContextVMRequestConfig{
		Relays:          cfg.Relays,
		Signer:          cfg.Signer,
		SenderPubkey:    cfg.SenderPubkey,
		RecipientPubkey: cfg.AgentPubkey,
		Encrypted:       cfg.Encrypted,
		ResultTimeout:   cfg.ResultTimeout,
		ResultRetries:   cfg.ResultRetries,
	}, bahiaclient.WithContextVMRequestLogger(logger))
	if err != nil {
		return nil, fmt.Errorf("create DNS agent ContextVM client: %w", err)
	}
	return NewDnsmasqAgentBackend(requestClient)
}

func newDnsmasqAgentBackend(requestClient ContextVMRequester, now func() time.Time) (*DnsmasqAgentBackend, error) {
	if requestClient == nil {
		return nil, fmt.Errorf("DNS agent ContextVM request client is required")
	}
	if now == nil {
		now = time.Now
	}
	return &DnsmasqAgentBackend{client: requestClient, now: now, serials: make(map[string]int64)}, nil
}

// Close releases resources owned by the underlying request client — in the
// production NewRelayDnsmasqAgentBackend wiring that is the ContextVM client's
// internally-created relay pool. Injected requesters that expose no Close
// method are left untouched, so test doubles need no extra plumbing.
func (b *DnsmasqAgentBackend) Close() error {
	switch closer := b.client.(type) {
	case io.Closer:
		return closer.Close()
	case interface{ Close() }:
		closer.Close()
	}
	return nil
}

func (b *DnsmasqAgentBackend) BackendType() domain.DNSBackendType {
	return domain.DNSBackendTypeDnsmasqAgent
}

func (b *DnsmasqAgentBackend) Health(ctx context.Context) error {
	var result protocol.HealthResult
	if err := b.request(ctx, protocol.MethodHealth, protocol.HealthParams{Schema: protocol.Schema}, &result); err != nil {
		return fmt.Errorf("DNS agent health request: %w", err)
	}
	if err := protocol.ValidateSchema(result.Schema); err != nil {
		return fmt.Errorf("DNS agent health response: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(result.Status), "ok") {
		return fmt.Errorf("DNS agent health response status is %q", result.Status)
	}
	return nil
}

func (b *DnsmasqAgentBackend) ListRecords(ctx context.Context, zone domain.DNSZone) ([]domain.DNSRecord, error) {
	records, _, err := b.ListZoneState(ctx, zone)
	return records, err
}

func (b *DnsmasqAgentBackend) ListZoneState(ctx context.Context, zone domain.DNSZone) ([]domain.DNSRecord, bool, error) {
	var result protocol.ListResult
	if err := b.request(ctx, protocol.MethodList, protocol.ListParams{Schema: protocol.Schema, Zone: zone}, &result); err != nil {
		return nil, false, fmt.Errorf("DNS agent list request for zone %q: %w", zone.Name, err)
	}
	if err := protocol.ValidateSchema(result.Schema); err != nil {
		return nil, false, fmt.Errorf("DNS agent list response for zone %q: %w", zone.Name, err)
	}
	// Seed the serial floor with the agent's revealed serial so the first sync
	// after a restart starts above it even if the local clock stepped back.
	b.recordAgentSerial(domain.NormalizeDNSZoneName(zone.Name), result.Serial)
	return result.Records, result.Authoritative, nil
}

func (b *DnsmasqAgentBackend) SyncZone(ctx context.Context, zone domain.DNSZone, records []domain.DNSRecord) error {
	zoneKey := domain.NormalizeDNSZoneName(zone.Name)
	serial := b.nextSerial(zoneKey)
	result, err := b.syncOnce(ctx, zone, records, serial)
	if err != nil {
		return err
	}
	if syncStatusIs(result.Status, protocol.SyncStatusStale) {
		// The agent's persisted serial is ahead of ours (for example after a
		// Bahia restart with a stepped-back clock, or failover to a host with a
		// slower clock). Adopt the agent's serial as the local floor and retry
		// once so zone updates recover immediately instead of waiting for wall
		// time to catch up.
		b.recordAgentSerial(zoneKey, result.Serial)
		serial = b.nextSerial(zoneKey)
		result, err = b.syncOnce(ctx, zone, records, serial)
		if err != nil {
			return err
		}
		if syncStatusIs(result.Status, protocol.SyncStatusStale) {
			return fmt.Errorf("DNS agent sync for zone %q is still stale after recovery retry: agent serial %d, sent serial %d", zone.Name, result.Serial, serial)
		}
	}
	if !syncStatusIs(result.Status, protocol.SyncStatusOK) {
		return fmt.Errorf("DNS agent sync response for zone %q has status %q", zone.Name, result.Status)
	}
	if result.Serial != serial {
		return fmt.Errorf("DNS agent sync response for zone %q returned serial %d, want %d", zone.Name, result.Serial, serial)
	}
	return nil
}

func (b *DnsmasqAgentBackend) syncOnce(ctx context.Context, zone domain.DNSZone, records []domain.DNSRecord, serial int64) (protocol.SyncResult, error) {
	params := protocol.SyncParams{Schema: protocol.Schema, Zone: zone, Records: records, Serial: serial}
	var result protocol.SyncResult
	if err := b.request(ctx, protocol.MethodSync, params, &result); err != nil {
		return protocol.SyncResult{}, fmt.Errorf("DNS agent sync request for zone %q: %w", zone.Name, err)
	}
	if err := protocol.ValidateSchema(result.Schema); err != nil {
		return protocol.SyncResult{}, fmt.Errorf("DNS agent sync response for zone %q: %w", zone.Name, err)
	}
	return result, nil
}

func syncStatusIs(status, want string) bool {
	return strings.EqualFold(strings.TrimSpace(status), want)
}

func (b *DnsmasqAgentBackend) nextSerial(zone string) int64 {
	candidate := b.now().UnixNano()
	b.serialMu.Lock()
	defer b.serialMu.Unlock()
	if candidate <= b.serials[zone] {
		candidate = b.serials[zone] + 1
	}
	b.serials[zone] = candidate
	return candidate
}

// recordAgentSerial raises the local per-zone serial floor to a serial the
// agent has revealed (via a list result or a stale sync response) so that
// nextSerial always continues above the agent's last applied serial.
func (b *DnsmasqAgentBackend) recordAgentSerial(zone string, serial int64) {
	b.serialMu.Lock()
	defer b.serialMu.Unlock()
	if serial > b.serials[zone] {
		b.serials[zone] = serial
	}
}

func (b *DnsmasqAgentBackend) request(ctx context.Context, method string, params any, result any) error {
	event, err := b.client.Request(ctx, method, params, nil, nil)
	if err != nil {
		return err
	}
	if event == nil {
		return fmt.Errorf("empty ContextVM result event")
	}
	if err := json.Unmarshal([]byte(event.Content), result); err != nil {
		return fmt.Errorf("decode ContextVM result: %w", err)
	}
	return nil
}
