// Package protocol defines the shared Bahia DNS agent ContextVM wire contract.
package protocol

import (
	"fmt"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
)

const (
	Schema = "bahia.dnsagent.v1"

	MethodHealth = "dns-agent/health"
	MethodList   = "dns-agent/list"
	MethodSync   = "dns-agent/sync"

	// SyncStatusOK marks a sync that was accepted: either applied or an
	// idempotent no-op. SyncResult.Serial echoes the accepted request serial.
	SyncStatusOK = "ok"
	// SyncStatusStale marks a recoverable rejection: the request serial was
	// lower than the agent's last applied serial for the zone (for example
	// after a control-plane restart with a stepped-back clock).
	// SyncResult.Serial carries the agent's current per-zone serial so the
	// backend can resume above it.
	SyncStatusStale = "stale"
)

type HealthParams struct {
	Schema string `json:"schema"`
}

type HealthResult struct {
	Schema          string   `json:"schema"`
	Status          string   `json:"status"`
	IncludeDir      string   `json:"include_dir"`
	FilePrefix      string   `json:"file_prefix"`
	ReloadStrategy  string   `json:"reload_strategy"`
	AllowedZones    []string `json:"allowed_zones"`
	LastApplySerial int64    `json:"last_apply_serial"`
	LastApplyAt     string   `json:"last_apply_at"`
}

type ListParams struct {
	Schema string         `json:"schema"`
	Zone   domain.DNSZone `json:"zone"`
}

type ListResult struct {
	Schema  string             `json:"schema"`
	Records []domain.DNSRecord `json:"records"`
	Serial  int64              `json:"serial"`
	// Authoritative is additive within bahia.dnsagent.v1: older backends ignore
	// the field, while older agents omit it and therefore decode as false.
	Authoritative bool `json:"authoritative"`
}

type SyncParams struct {
	Schema  string             `json:"schema"`
	Zone    domain.DNSZone     `json:"zone"`
	Records []domain.DNSRecord `json:"records"`
	Serial  int64              `json:"serial"`
}

// SyncResult reports the outcome of a sync request. Status is SyncStatusOK or
// SyncStatusStale; for stale results Serial is the agent's last applied serial
// for the zone rather than the request serial.
type SyncResult struct {
	Schema  string `json:"schema"`
	Status  string `json:"status"`
	Changed bool   `json:"changed"`
	Serial  int64  `json:"serial"`
}

func ValidateSchema(schema string) error {
	if strings.TrimSpace(schema) != Schema {
		return fmt.Errorf("unsupported DNS agent schema %q; expected %q", schema, Schema)
	}
	return nil
}

func (p HealthParams) Validate() error { return ValidateSchema(p.Schema) }

func (p ListParams) Validate() error { return ValidateSchema(p.Schema) }

func (p SyncParams) Validate() error { return ValidateSchema(p.Schema) }
