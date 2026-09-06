package domain

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// DNSBackendType identifies a configured DNS backend adapter family.
type DNSBackendType string

const (
	DNSBackendTypeFilesystem     DNSBackendType = "filesystem"
	DNSBackendTypeCoreDNS        DNSBackendType = "coredns"
	DNSBackendTypePowerDNS       DNSBackendType = "powerdns"
	DNSBackendTypeDNSMasq        DNSBackendType = "dnsmasq"
	DNSBackendTypeDnsmasqAgent   DNSBackendType = "dnsmasq_agent"
	DNSBackendTypeFIPS           DNSBackendType = "fips"
	DNSBackendTypeConsul         DNSBackendType = "consul"
	DNSBackendTypeEtcd           DNSBackendType = "etcd"
	DNSBackendTypeK8sExternalDNS DNSBackendType = "k8s_external_dns"
)

// ZoneVisibility describes where a DNS zone is intended to be served.
type ZoneVisibility string

const (
	ZoneVisibilityInternal ZoneVisibility = "internal"
	ZoneVisibilityExternal ZoneVisibility = "external"
	ZoneVisibilityEdge     ZoneVisibility = "edge"
	ZoneVisibilityMesh     ZoneVisibility = "mesh"
)

// DNSRecordType identifies the DNS record families projected in Phase 0.
type DNSRecordType string

const (
	DNSRecordTypeA     DNSRecordType = "A"
	DNSRecordTypeAAAA  DNSRecordType = "AAAA"
	DNSRecordTypeCNAME DNSRecordType = "CNAME"
	DNSRecordTypeSRV   DNSRecordType = "SRV"
)

// DNSEndpointFamily identifies the infrastructure graph source for a DNS endpoint.
type DNSEndpointFamily string

const (
	DNSEndpointFamilyService DNSEndpointFamily = "service"
	DNSEndpointFamilyLLM     DNSEndpointFamily = "llm"
	DNSEndpointFamilyML      DNSEndpointFamily = "ml"
	DNSEndpointFamilyWorker  DNSEndpointFamily = "worker"
	DNSEndpointFamilyMesh    DNSEndpointFamily = "mesh"
)

// DNSEndpoint is a materialized DNS endpoint derived from Bahia infrastructure state.
type DNSEndpoint struct {
	ID             uuid.UUID         `json:"id"`
	ServiceID      *uuid.UUID        `json:"service_id,omitempty"`
	LLMRouteID     *uuid.UUID        `json:"llm_route_id,omitempty"`
	MLEndpointID   *uuid.UUID        `json:"ml_endpoint_id,omitempty"`
	WorkerPubkey   string            `json:"worker_pubkey,omitempty"`
	Family         DNSEndpointFamily `json:"family"`
	Name           string            `json:"name"`
	Environment    string            `json:"environment,omitempty"`
	Zone           string            `json:"zone"`
	FQDN           string            `json:"fqdn"`
	Coordinate     string            `json:"coordinate"`
	Protocol       string            `json:"protocol,omitempty"`
	Address        string            `json:"address"`
	Port           *int              `json:"port,omitempty"`
	Runtime        string            `json:"runtime,omitempty"`
	Hardware       string            `json:"hardware,omitempty"`
	Capabilities   []string          `json:"capabilities,omitempty"`
	Health         HealthStatus      `json:"health"`
	DriftStatus    DriftStatus       `json:"drift_status"`
	Source         string            `json:"source"`
	Metadata       map[string]any    `json:"metadata,omitempty"`
	MaterializedAt time.Time         `json:"materialized_at"`
}

// DNSZone defines a managed DNS zone and its backend binding.
type DNSZone struct {
	Name          string         `json:"name"`
	Visibility    ZoneVisibility `json:"visibility"`
	BackendRef    string         `json:"backend_ref"`
	TTL           int            `json:"ttl"`
	Authoritative bool           `json:"authoritative"`
}

// DNSBackendState is a materialized DNS backend read model for Nostr projection.
type DNSBackendState struct {
	Ref        string         `json:"ref"`
	Type       DNSBackendType `json:"type"`
	Health     HealthStatus   `json:"health"`
	ZoneRefs   []string       `json:"zone_refs,omitempty"`
	LastSyncAt *time.Time     `json:"last_sync_at,omitempty"`
	UpdatedAt  time.Time      `json:"updated_at"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

// DNSRecord is a single projected DNS record derived from a DNSEndpoint.
type DNSRecord struct {
	Zone             string        `json:"zone"`
	Name             string        `json:"name"`
	FQDN             string        `json:"fqdn"`
	Type             DNSRecordType `json:"type"`
	Value            string        `json:"value"`
	TTL              int           `json:"ttl"`
	Priority         *int          `json:"priority,omitempty"`
	Weight           *int          `json:"weight,omitempty"`
	Port             *int          `json:"port,omitempty"`
	SourceCoordinate string        `json:"source_coordinate"`
}

// DNSPolicy defines rule-based DNS record behavior for zones and environments.
type DNSPolicy struct {
	ID            uuid.UUID       `json:"id"`
	Name          string          `json:"name"`
	ZoneID        *uuid.UUID      `json:"zone_id,omitempty"`
	EnvironmentID *uuid.UUID      `json:"environment_id,omitempty"`
	Rules         []DNSPolicyRule `json:"rules"`
	Enabled       bool            `json:"enabled"`
	Metadata      map[string]any  `json:"metadata,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

// DNSPolicyRule combines endpoint match criteria with DNS policy effects.
type DNSPolicyRule struct {
	Match  DNSPolicyMatch  `json:"match"`
	Action DNSPolicyAction `json:"action"`
}

// DNSPolicyMatch scopes a DNS policy rule to endpoint attributes.
type DNSPolicyMatch struct {
	Capabilities []string `json:"capabilities,omitempty"`
	Hardware     []string `json:"hardware,omitempty"`
	Geohash      string   `json:"geohash,omitempty"`
	Environment  string   `json:"environment,omitempty"`
	Runtime      string   `json:"runtime,omitempty"`
}

// DNSPolicyAction describes the DNS behavior applied by a matching rule.
type DNSPolicyAction struct {
	Visibility  ZoneVisibility `json:"visibility,omitempty"`
	TTLOverride *int           `json:"ttl_override,omitempty"`
	WeightBias  *int           `json:"weight_bias,omitempty"`
	Exclude     bool           `json:"exclude,omitempty"`
}

// DNSRecordOverride is a manually pinned DNS record override.
type DNSRecordOverride struct {
	ID             uuid.UUID     `json:"id"`
	ZoneName       string        `json:"zone_name"`
	RecordName     string        `json:"record_name"`
	RecordType     DNSRecordType `json:"record_type"`
	Value          string        `json:"value"`
	TTL            int           `json:"ttl"`
	Reason         string        `json:"reason"`
	OperatorPubkey string        `json:"operator_pubkey"`
	CreatedAt      time.Time     `json:"created_at"`
	ExpiresAt      *time.Time    `json:"expires_at,omitempty"`
}

func (t DNSBackendType) IsValid() bool {
	switch t {
	case DNSBackendTypeFilesystem, DNSBackendTypeCoreDNS, DNSBackendTypePowerDNS, DNSBackendTypeDNSMasq, DNSBackendTypeDnsmasqAgent, DNSBackendTypeFIPS, DNSBackendTypeConsul, DNSBackendTypeEtcd, DNSBackendTypeK8sExternalDNS:
		return true
	default:
		return false
	}
}

func (v ZoneVisibility) IsValid() bool {
	switch v {
	case ZoneVisibilityInternal, ZoneVisibilityExternal, ZoneVisibilityEdge, ZoneVisibilityMesh:
		return true
	default:
		return false
	}
}

func (t DNSRecordType) IsValid() bool {
	switch t {
	case DNSRecordTypeA, DNSRecordTypeAAAA, DNSRecordTypeCNAME, DNSRecordTypeSRV:
		return true
	default:
		return false
	}
}

func (f DNSEndpointFamily) IsValid() bool {
	switch f {
	case DNSEndpointFamilyService, DNSEndpointFamilyLLM, DNSEndpointFamilyML, DNSEndpointFamilyWorker, DNSEndpointFamilyMesh:
		return true
	default:
		return false
	}
}

// DTag returns the replaceable-event coordinate for this endpoint.
func (e DNSEndpoint) DTag() string {
	name := strings.TrimSpace(e.Name)
	env := strings.TrimSpace(e.Environment)
	family := e.Family
	if family == DNSEndpointFamilyWorker {
		return fmt.Sprintf("endpoint:worker:%s", name)
	}
	return fmt.Sprintf("endpoint:%s:%s:%s", family, name, env)
}

// DeterministicDNSEndpointID derives a stable UUID v5 from an endpoint coordinate.
func DeterministicDNSEndpointID(dTag string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(strings.TrimSpace(dTag)))
}

func ValidateDNSEndpoint(endpoint *DNSEndpoint) error {
	if endpoint == nil {
		return fmt.Errorf("%w: DNS endpoint must not be nil", ErrInvalidValue)
	}
	endpoint.Name = strings.TrimSpace(endpoint.Name)
	endpoint.Environment = strings.TrimSpace(endpoint.Environment)
	endpoint.Zone = strings.TrimSpace(endpoint.Zone)
	endpoint.FQDN = strings.TrimSpace(endpoint.FQDN)
	endpoint.Coordinate = strings.TrimSpace(endpoint.Coordinate)
	endpoint.Address = strings.TrimSpace(endpoint.Address)
	endpoint.Source = strings.TrimSpace(endpoint.Source)
	if !endpoint.Family.IsValid() {
		return fmt.Errorf("%w: DNS endpoint family %q is not valid", ErrInvalidValue, endpoint.Family)
	}
	if err := ValidateRequiredString(endpoint.Name, "name"); err != nil {
		return err
	}
	if endpoint.Family != DNSEndpointFamilyWorker {
		if err := ValidateRequiredString(endpoint.Environment, "environment"); err != nil {
			return err
		}
	}
	if err := ValidateRequiredString(endpoint.Zone, "zone"); err != nil {
		return err
	}
	if err := ValidateRequiredString(endpoint.FQDN, "fqdn"); err != nil {
		return err
	}
	if err := ValidateRequiredString(endpoint.Address, "address"); err != nil {
		return err
	}
	if err := ValidateRequiredString(endpoint.Source, "source"); err != nil {
		return err
	}
	expectedCoordinate := endpoint.DTag()
	if endpoint.Coordinate == "" {
		endpoint.Coordinate = expectedCoordinate
	}
	if endpoint.Coordinate != expectedCoordinate {
		return fmt.Errorf("%w: DNS endpoint coordinate %q does not match %q", ErrInvalidValue, endpoint.Coordinate, expectedCoordinate)
	}
	if endpoint.ID == uuid.Nil {
		endpoint.ID = DeterministicDNSEndpointID(endpoint.Coordinate)
	}
	return nil
}

// NormalizeDNSZoneName returns the canonical key and render name for DNS zones.
// strings.ToLower performs Unicode-aware case folding, matching the names that
// ValidateDNSZone permits.
func NormalizeDNSZoneName(name string) string {
	return strings.TrimRight(strings.ToLower(strings.TrimSpace(name)), ".")
}

func ValidateDNSZone(zone *DNSZone) error {
	if zone == nil {
		return fmt.Errorf("%w: DNS zone must not be nil", ErrInvalidValue)
	}
	zone.Name = NormalizeDNSZoneName(zone.Name)
	zone.BackendRef = strings.TrimSpace(zone.BackendRef)
	if err := ValidateRequiredString(zone.Name, "name"); err != nil {
		return err
	}
	if !zone.Visibility.IsValid() {
		return fmt.Errorf("%w: DNS zone visibility %q is not valid", ErrInvalidValue, zone.Visibility)
	}
	if err := ValidateRequiredString(zone.BackendRef, "backend_ref"); err != nil {
		return err
	}
	if zone.TTL <= 0 {
		return fmt.Errorf("%w: DNS zone ttl must be > 0", ErrInvalidValue)
	}
	return nil
}

func ValidateDNSPolicy(policy *DNSPolicy) error {
	if policy == nil {
		return fmt.Errorf("%w: DNS policy must not be nil", ErrInvalidValue)
	}
	policy.Name = strings.TrimSpace(policy.Name)
	if err := ValidateRequiredString(policy.Name, "name"); err != nil {
		return err
	}
	if len(policy.Rules) == 0 {
		return fmt.Errorf("%w: DNS policy rules must not be empty", ErrInvalidValue)
	}
	for i, rule := range policy.Rules {
		if err := validateDNSPolicyAction(rule.Action); err != nil {
			return fmt.Errorf("DNS policy rule %d: %w", i, err)
		}
	}
	return nil
}

func validateDNSPolicyAction(action DNSPolicyAction) error {
	if action.Visibility != "" && !action.Visibility.IsValid() {
		return fmt.Errorf("%w: DNS policy action visibility %q is not valid", ErrInvalidValue, action.Visibility)
	}
	if action.Visibility == "" && action.TTLOverride == nil && action.WeightBias == nil && !action.Exclude {
		return fmt.Errorf("%w: DNS policy action must have at least one effect", ErrInvalidValue)
	}
	return nil
}

func ValidateDNSRecordOverride(override *DNSRecordOverride) error {
	if override == nil {
		return fmt.Errorf("%w: DNS record override must not be nil", ErrInvalidValue)
	}
	override.ZoneName = strings.TrimSpace(override.ZoneName)
	override.RecordName = strings.TrimSpace(override.RecordName)
	override.Value = strings.TrimSpace(override.Value)
	override.Reason = strings.TrimSpace(override.Reason)
	override.OperatorPubkey = strings.TrimSpace(override.OperatorPubkey)
	if err := ValidateRequiredString(override.ZoneName, "zone_name"); err != nil {
		return err
	}
	if err := ValidateRequiredString(override.RecordName, "record_name"); err != nil {
		return err
	}
	if !override.RecordType.IsValid() {
		return fmt.Errorf("%w: DNS record override type %q is not valid", ErrInvalidValue, override.RecordType)
	}
	if err := ValidateRequiredString(override.Value, "value"); err != nil {
		return err
	}
	if override.TTL <= 0 {
		return fmt.Errorf("%w: DNS record override ttl must be > 0", ErrInvalidValue)
	}
	if err := ValidateRequiredString(override.Reason, "reason"); err != nil {
		return err
	}
	if err := ValidateRequiredString(override.OperatorPubkey, "operator_pubkey"); err != nil {
		return err
	}
	return nil
}
