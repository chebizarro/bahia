// Package config provides configuration loading and validation for Bahia.
package config

import (
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

// Config is the top-level configuration for Bahia.
type Config struct {
	Mode           string                    `koanf:"mode" yaml:"mode"`
	DevMode        bool                      `koanf:"dev_mode" yaml:"dev_mode"`
	Server         ServerConfig              `koanf:"server"`
	DB             DBConfig                  `koanf:"db"`
	Harbor         HarborConfig              `koanf:"harbor"`
	Loom           LoomConfig                `koanf:"loom"`
	Nostr          NostrConfig               `koanf:"nostr"`
	Reconcile      ReconcileConfig           `koanf:"reconcile"`
	Supervision    SupervisionConfig         `koanf:"supervision" yaml:"supervision"`
	Runtime        RuntimeConfig             `koanf:"runtime"`
	Log            LogConfig                 `koanf:"log"`
	Auth           AuthConfig                `koanf:"auth"`
	Adoption       AdoptionConfig            `koanf:"adoption"`
	DirectRuntime  DirectRuntimeConfig       `koanf:"direct_runtime_actions"`
	CORS           CORSConfig                `koanf:"cors"`
	Blossom        BlossomConfig             `koanf:"blossom"`
	SBOM           SBOMConfig                `koanf:"sbom" yaml:"sbom"`
	OCI            OCIServerConfig           `koanf:"oci"`
	HiveCI         HiveCIConfig              `koanf:"hiveci"`
	Cashu          CashuConfig               `koanf:"cashu"`
	Qdrant         QdrantConfig              `koanf:"qdrant"`
	Telemetry      TelemetryConfig           `koanf:"telemetry"`
	WorkerPressure WorkerPressureConfig      `koanf:"worker_pressure"`
	WorkerCleanup  WorkerCleanupConfig       `koanf:"worker_cleanup"`
	Hygiene        HygieneConfig             `koanf:"hygiene" yaml:"hygiene"`
	Notifications  NotificationsConfig       `koanf:"notifications"`
	Registry       RegistryAdapterConfig     `koanf:"registry"`
	LLM            LLMControlplaneConfig     `koanf:"llm"`
	Packages       PackageControlplaneConfig `koanf:"packages"`
	Assistant      AssistantConfig           `koanf:"assistant"`
	DNS            DNSConfig                 `koanf:"dns"`
	EdgeRouting    EdgeRoutingConfig         `koanf:"edge_routing" yaml:"edge_routing"`
	FIPS           FIPSConfig                `koanf:"fips"`
	SoulFactory    SoulFactoryConfig         `koanf:"soul_factory" yaml:"soul_factory"`
}

// SBOMConfig controls SBOM generation adapters.
type SBOMConfig struct {
	Cdxgen SBOMCdxgenConfig `koanf:"cdxgen" yaml:"cdxgen"`
}

// SBOMCdxgenConfig controls the optional external cdxgen generator.
type SBOMCdxgenConfig struct {
	Enabled    bool   `koanf:"enabled" yaml:"enabled"`
	BinaryPath string `koanf:"binary_path" yaml:"binary_path"`
}

// WorkerPressureConfig controls Bahia-owned worker pressure and dynamic admission thresholds.
// HygieneConfig controls the fleet hygiene (Swabbie) reconciler (fp-jan).
// The policy document itself is a versioned JSON file validated against
// schemas/hygiene_policy.json.
type HygieneConfig struct {
	Enabled    bool          `koanf:"enabled" yaml:"enabled"`
	PolicyPath string        `koanf:"policy_path" yaml:"policy_path"`
	Interval   time.Duration `koanf:"interval" yaml:"interval"`
	// Workers lists maintenance-driver worker pubkeys to reconcile when
	// the policy document does not itself target specific workers.
	Workers []string `koanf:"workers" yaml:"workers"`
}

type WorkerPressureConfig struct {
	MemoryWarningMinGB  int     `koanf:"memory_warning_min_gb" yaml:"memory_warning_min_gb"`
	MemoryWarningRatio  float64 `koanf:"memory_warning_min_ratio" yaml:"memory_warning_min_ratio"`
	MemoryCriticalMinGB int     `koanf:"memory_critical_min_gb" yaml:"memory_critical_min_gb"`
	MemoryCriticalRatio float64 `koanf:"memory_critical_min_ratio" yaml:"memory_critical_min_ratio"`
	DiskWarningMinGB    int     `koanf:"disk_warning_min_gb" yaml:"disk_warning_min_gb"`
	DiskWarningRatio    float64 `koanf:"disk_warning_min_ratio" yaml:"disk_warning_min_ratio"`
	DiskCriticalMinGB   int     `koanf:"disk_critical_min_gb" yaml:"disk_critical_min_gb"`
	DiskCriticalRatio   float64 `koanf:"disk_critical_min_ratio" yaml:"disk_critical_min_ratio"`
	VRAMWarningMinGB    int     `koanf:"vram_warning_min_gb" yaml:"vram_warning_min_gb"`
	VRAMWarningRatio    float64 `koanf:"vram_warning_min_ratio" yaml:"vram_warning_min_ratio"`
	VRAMCriticalMinGB   int     `koanf:"vram_critical_min_gb" yaml:"vram_critical_min_gb"`
	VRAMCriticalRatio   float64 `koanf:"vram_critical_min_ratio" yaml:"vram_critical_min_ratio"`
	ThermalWarningC     float64 `koanf:"thermal_warning_c" yaml:"thermal_warning_c"`
	ThermalCriticalC    float64 `koanf:"thermal_critical_c" yaml:"thermal_critical_c"`
	QueueWarningRatio   float64 `koanf:"queue_warning_ratio" yaml:"queue_warning_ratio"`
	QueueCriticalRatio  float64 `koanf:"queue_critical_ratio" yaml:"queue_critical_ratio"`
}

// WorkerCleanupConfig controls pressure-triggered worker cleanup orchestration.
type WorkerCleanupConfig struct {
	Mode             string        `koanf:"mode" yaml:"mode"`
	Cooldown         time.Duration `koanf:"cooldown" yaml:"cooldown"`
	TargetFreeGB     int           `koanf:"target_free_gb" yaml:"target_free_gb"`
	PaymentToken     string        `koanf:"payment_token" yaml:"payment_token"`
	RequiredSoftware []string      `koanf:"required_software" yaml:"required_software"`
}

// FIPSConfig controls FIPS overlay advert ingestion.
type FIPSConfig struct {
	Enabled              bool     `koanf:"enabled"`
	RelayURLs            []string `koanf:"relay_urls"`
	AppNamespace         string   `koanf:"app_namespace"`
	AutoRegisterWorkers  bool     `koanf:"auto_register_workers"`
	AllowedNpubs         []string `koanf:"allowed_npubs"`
	OverlayAddressPrefix string   `koanf:"overlay_address_prefix"`
}

// EdgeRoutingConfig controls signed public hostname provisioning through a managed provider.
type EdgeRoutingConfig struct {
	Enabled        bool                      `koanf:"enabled" yaml:"enabled"`
	Provider       string                    `koanf:"provider" yaml:"provider"`
	BackendRef     string                    `koanf:"backend_ref" yaml:"backend_ref"`
	APIBaseURL     string                    `koanf:"api_base_url" yaml:"api_base_url"`
	APITokenRef    string                    `koanf:"api_token_ref" yaml:"api_token_ref"`
	AccountID      string                    `koanf:"account_id" yaml:"account_id"`
	TunnelID       string                    `koanf:"tunnel_id" yaml:"tunnel_id"`
	VerifyTimeout  time.Duration             `koanf:"verify_timeout" yaml:"verify_timeout"`
	VerifyResolver string                    `koanf:"verify_resolver" yaml:"verify_resolver"`
	Zones          []EdgeRoutingZoneConfig   `koanf:"zones" yaml:"zones"`
	Origins        []EdgeRoutingOriginConfig `koanf:"origins" yaml:"origins"`
}

type EdgeRoutingZoneConfig struct {
	Name          string   `koanf:"name" yaml:"name"`
	ZoneID        string   `koanf:"zone_id" yaml:"zone_id"`
	AllowedOrgIDs []string `koanf:"allowed_org_ids" yaml:"allowed_org_ids"`
	Protected     bool     `koanf:"protected" yaml:"protected"`
	TTL           int      `koanf:"ttl" yaml:"ttl"`
}

type EdgeRoutingOriginConfig struct {
	DeploymentUnitID string `koanf:"deployment_unit_id" yaml:"deployment_unit_id"`
	Host             string `koanf:"host" yaml:"host"`
	AllowedPorts     []int  `koanf:"allowed_ports" yaml:"allowed_ports"`
}

// DNSConfig controls DNS orchestration projection and backend settings.
type DNSConfig struct {
	Enabled           bool                        `koanf:"enabled"`
	DefaultTTL        int                         `koanf:"default_ttl"`
	ReconcileInterval time.Duration               `koanf:"reconcile_interval"`
	Zones             []DNSZoneConfig             `koanf:"zones"`
	Backends          map[string]DNSBackendConfig `koanf:"backends"`
	Projection        DNSProjectionConfig         `koanf:"projection"`
}

// DNSZoneConfig binds a managed DNS zone to a configured backend.
type DNSZoneConfig struct {
	Name       string `koanf:"name"`
	Visibility string `koanf:"visibility"`
	Backend    string `koanf:"backend"`
	TTL        int    `koanf:"ttl"`
}

// DNSBackendConfig describes one DNS backend connector.
type DNSBackendConfig struct {
	Type                      string        `koanf:"type"`
	RootDir                   string        `koanf:"root_dir"`
	EtcdEndpoints             []string      `koanf:"etcd_endpoints"`
	EtcdPrefix                string        `koanf:"etcd_prefix"`
	EtcdDialTimeout           time.Duration `koanf:"etcd_dial_timeout"`
	PowerDNSAPIURL            string        `koanf:"powerdns_api_url" yaml:"powerdns_api_url"`
	PowerDNSAPIKey            string        `koanf:"powerdns_api_key" yaml:"powerdns_api_key"`
	PowerDNSServerID          string        `koanf:"powerdns_server_id" yaml:"powerdns_server_id"`
	PowerDNSAllowInsecureHTTP bool          `koanf:"powerdns_allow_insecure_http" yaml:"powerdns_allow_insecure_http"`
	DnsmasqConfigDir          string        `koanf:"dnsmasq_config_dir" yaml:"dnsmasq_config_dir"`
	DnsmasqReloadCommand      string        `koanf:"dnsmasq_reload_command" yaml:"dnsmasq_reload_command"`
	DnsmasqFilePrefix         string        `koanf:"dnsmasq_file_prefix" yaml:"dnsmasq_file_prefix"`
	HostsPath                 string        `koanf:"hosts_path" yaml:"hosts_path"`
}

// DNSProjectionConfig selects source state for DNS endpoint projection.
type DNSProjectionConfig struct {
	Services          bool              `koanf:"services"`
	LLMRoutes         bool              `koanf:"llm_routes"`
	MLEndpoints       bool              `koanf:"ml_endpoints"`
	Workers           bool              `koanf:"workers"`
	MeshEndpoints     bool              `koanf:"mesh_endpoints"`
	CapabilityAliases bool              `koanf:"capability_aliases"`
	EnvironmentZones  map[string]string `koanf:"environment_zones"`
	WorkerZone        string            `koanf:"worker_zone"`
	MeshZone          string            `koanf:"mesh_zone"`
}

// SoulFactoryConfig controls the Nostr-native Soul Factory provisioning reactor.
type SoulFactoryConfig struct {
	Enabled bool `koanf:"enabled" yaml:"enabled"`
	// AgentRuntimes is the validated list of administratively enabled
	// SoulFactory agent runtime targets (for example openclaw, metiq).
	// When unset it defaults to [openclaw] to preserve prior behavior.
	AgentRuntimes []string `koanf:"agent_runtimes" yaml:"agent_runtimes"`
	// RuntimePubkeys pins each enabled runtime target to the exact signing
	// identities whose kind:30317 capabilities Bahia may trust.
	RuntimePubkeys                  map[string][]string    `koanf:"runtime_pubkeys" yaml:"runtime_pubkeys"`
	Relays                          []string               `koanf:"relays" yaml:"relays"`
	AdditionalRelays                []string               `koanf:"additional_relays" yaml:"additional_relays"`
	NIP05Relays                     []string               `koanf:"nip05_relays" yaml:"nip05_relays"`
	NIP29Groups                     []NIP29Group           `koanf:"nip29_groups" yaml:"nip29_groups"`
	CommunikeysCommunities          []CommunikeysCommunity `koanf:"communikeys_communities" yaml:"communikeys_communities"`
	ConcordCommunities              []ConcordCommunity     `koanf:"concord_communities" yaml:"concord_communities"`
	AuthorizedPubkeys               []string               `koanf:"authorized_pubkeys" yaml:"authorized_pubkeys"`
	SoulFactoryPubkey               string                 `koanf:"soul_factory_pubkey" yaml:"soul_factory_pubkey"`
	SignetBunkerURI                 string                 `koanf:"signet_bunker_uri" yaml:"signet_bunker_uri"`
	SignetClientSecretKey           string                 `koanf:"signet_client_secret_key" yaml:"signet_client_secret_key"`
	StartupTimeout                  time.Duration          `koanf:"startup_timeout" yaml:"startup_timeout"`
	LLMBaseURL                      string                 `koanf:"llm_base_url" yaml:"llm_base_url"`
	LLMModel                        string                 `koanf:"llm_model" yaml:"llm_model"`
	LLMAPIKey                       string                 `koanf:"llm_api_key" yaml:"llm_api_key"`
	LLMTimeout                      time.Duration          `koanf:"llm_timeout" yaml:"llm_timeout"`
	WorkspaceGiteaURL               string                 `koanf:"workspace_gitea_url" yaml:"workspace_gitea_url"`
	WorkspaceTemplateDir            string                 `koanf:"workspace_template_dir" yaml:"workspace_template_dir"`
	WorkspacePrivateKeyRef          string                 `koanf:"workspace_private_key_ref" yaml:"workspace_private_key_ref"`
	WorkspaceAgentMemoryMCPURLRef   string                 `koanf:"workspace_agent_memory_mcp_url_ref" yaml:"workspace_agent_memory_mcp_url_ref"`
	AgentMemoryTaskIDFile           string                 `koanf:"agent_memory_task_id_file" yaml:"agent_memory_task_id_file"`
	WorkspaceGatewayPort            int                    `koanf:"workspace_gateway_port" yaml:"workspace_gateway_port"`
	OpenClawSignetEnabled           bool                   `koanf:"openclaw_signet_enabled" yaml:"openclaw_signet_enabled"`
	OpenClawSignetStateDir          string                 `koanf:"openclaw_signet_state_dir" yaml:"openclaw_signet_state_dir"`
	OpenClawSignetClientKeyDir      string                 `koanf:"openclaw_signet_client_key_dir" yaml:"openclaw_signet_client_key_dir"`
	OpenClawSignetContainer         string                 `koanf:"openclaw_signet_container" yaml:"openclaw_signet_container"`
	OpenClawSignetConfigPath        string                 `koanf:"openclaw_signet_config_path" yaml:"openclaw_signet_config_path"`
	OpenClawSignetProvisionerFile   string                 `koanf:"openclaw_signet_provisioner_file" yaml:"openclaw_signet_provisioner_file"`
	OpenClawSignetProvisionerPubkey string                 `koanf:"openclaw_signet_provisioner_pubkey" yaml:"openclaw_signet_provisioner_pubkey"`
}

// NIP29Group identifies a fleet group that newly provisioned souls join.
type NIP29Group struct {
	Relay string `koanf:"relay" yaml:"relay"`
	ID    string `koanf:"id" yaml:"id"`
}

// CommunikeysCommunity identifies controller-owned section ACLs assigned to newly provisioned souls.
type CommunikeysCommunity struct {
	Pubkey   string   `koanf:"pubkey" yaml:"pubkey"`
	Sections []string `koanf:"sections" yaml:"sections"`
}

// ConcordCommunity identifies CORD-05 invite material for a fleet community.
// Exactly one secret source is required; raw membership keys are never accepted inline.
// InviteBundleSealedFile is the Signet-backed custody form: the file holds only a
// NIP-44 payload sealed to the staff key, and it is the only source CORD-06
// rotation can write fresh material back to.
type ConcordCommunity struct {
	CommunityID            string `koanf:"community_id" yaml:"community_id"`
	InviteBundleEnv        string `koanf:"invite_bundle_env" yaml:"invite_bundle_env"`
	InviteBundleFile       string `koanf:"invite_bundle_file" yaml:"invite_bundle_file"`
	InviteBundleSealedFile string `koanf:"invite_bundle_sealed_file" yaml:"invite_bundle_sealed_file"`
}

// AssistantConfig controls the operator assistant backend orchestration path.
type AssistantConfig struct {
	Enabled    bool   `koanf:"enabled" yaml:"enabled"`
	LLMBaseURL string `koanf:"llm_base_url" yaml:"llm_base_url"`
	LLMModel   string `koanf:"llm_model" yaml:"llm_model"`
	LLMAPIKey  string `koanf:"llm_api_key" yaml:"llm_api_key"`
	// LLMStreaming controls whether the legacy planner uses streaming chat completions.
	// When false (the default), the legacy planner uses non-streaming chat completions;
	// some OpenAI-compatible providers do not emit delta.content for streamed
	// response_format (json_schema) outputs, so streaming is opt-in per provider.
	LLMStreaming         bool                       `koanf:"llm_streaming" yaml:"llm_streaming"`
	SignetBunkerURI      string                     `koanf:"signet_bunker_uri" yaml:"signet_bunker_uri"`
	SignetAllowMock      bool                       `koanf:"signet_allow_mock" yaml:"signet_allow_mock"`
	SignetConnectTimeout time.Duration              `koanf:"signet_connect_timeout" yaml:"signet_connect_timeout"`
	Agentic              AssistantAgenticConfig     `koanf:"agentic" yaml:"agentic"`
	Permissions          AssistantPermissionsConfig `koanf:"permissions" yaml:"permissions"`
	MCP                  AssistantMCPConfig         `koanf:"mcp" yaml:"mcp"`
	// Item 10 extensibility surface. Each block points at directories of
	// markdown+frontmatter (subagents/skills/commands) or JSON (hooks) sources.
	Subagents AssistantExtensionSourceConfig `koanf:"subagents" yaml:"subagents"`
	Skills    AssistantExtensionSourceConfig `koanf:"skills" yaml:"skills"`
	Commands  AssistantExtensionSourceConfig `koanf:"commands" yaml:"commands"`
	Hooks     AssistantExtensionSourceConfig `koanf:"hooks" yaml:"hooks"`
}

// AssistantExtensionSourceConfig points the assistant at directories that hold
// one class of extension definitions (subagents, skills, commands, or hooks).
// Paths must not contain parent traversal so an operator cannot point the loader
// outside an intended tree.
type AssistantExtensionSourceConfig struct {
	Enabled bool     `koanf:"enabled" yaml:"enabled"`
	Paths   []string `koanf:"paths" yaml:"paths"`
}

const (
	AssistantAgenticToolModeNative   = "native"
	AssistantAgenticToolModePrompted = "prompted"
)

// AssistantAgenticConfig selects the provider-neutral agent loop model backend.
// OpenAI-compatible providers default to native chat-completions tool calls;
// prompted mode is available for instruction-tuned OpenAI-compatible endpoints
// that do not implement native function-calling.
type AssistantAgenticConfig struct {
	Enabled                    bool          `koanf:"enabled" yaml:"enabled"`
	Provider                   string        `koanf:"provider" yaml:"provider"`
	ToolMode                   string        `koanf:"tool_mode" yaml:"tool_mode"`
	BaseURL                    string        `koanf:"base_url" yaml:"base_url"`
	Model                      string        `koanf:"model" yaml:"model"`
	APIKey                     string        `koanf:"api_key" yaml:"api_key"`
	MaxIterations              int           `koanf:"max_iterations" yaml:"max_iterations"`
	MaxConsecutiveToolFailures int           `koanf:"max_consecutive_tool_failures" yaml:"max_consecutive_tool_failures"`
	RequestTimeout             time.Duration `koanf:"request_timeout" yaml:"request_timeout"`
}

// AssistantPermissionsConfig configures the assistant permission posture. The
// engine implementation lands later; this config owns the canonical default.
type AssistantPermissionsConfig struct {
	Mode domain.AssistantPermissionMode `koanf:"mode" yaml:"mode"`
}

// AssistantMCPConfig holds assistant-specific MCP runtime settings.
type AssistantMCPConfig struct {
	AsyncObservation AssistantMCPAsyncObservationConfig `koanf:"async_observation" yaml:"async_observation"`
	ExternalServers  []AssistantExternalMCPServerConfig `koanf:"external_servers" yaml:"external_servers"`
}

// AssistantMCPAsyncObservationConfig bounds event-native async tool observation.
type AssistantMCPAsyncObservationConfig struct {
	MaxWait       time.Duration `koanf:"max_wait" yaml:"max_wait"`
	BackfillLimit int           `koanf:"backfill_limit" yaml:"backfill_limit"`
}

// AssistantExternalMCPServerConfig describes one opt-in external MCP server.
// Servers are disabled by default and only enabled entries are contacted.
type AssistantExternalMCPServerConfig struct {
	Enabled       bool                                   `koanf:"enabled" yaml:"enabled"`
	Name          string                                 `koanf:"name" yaml:"name"`
	URL           string                                 `koanf:"url" yaml:"url"`
	ToolPrefix    string                                 `koanf:"tool_prefix" yaml:"tool_prefix"`
	Timeout       time.Duration                          `koanf:"timeout" yaml:"timeout"`
	AuthHeaders   map[string]string                      `koanf:"auth_headers" yaml:"auth_headers"`
	DefaultEffect domain.AssistantToolEffect             `koanf:"default_effect" yaml:"default_effect"`
	DefaultRisk   domain.AssistantToolRisk               `koanf:"default_risk" yaml:"default_risk"`
	ResourceTypes []string                               `koanf:"resource_types" yaml:"resource_types"`
	Permissions   []AssistantExternalMCPPermissionConfig `koanf:"permissions" yaml:"permissions"`
}

// AssistantExternalMCPPermissionConfig is converted into an assistant
// permission rule scoped to the server's prefixed tool names.
type AssistantExternalMCPPermissionConfig struct {
	ID             string                              `koanf:"id" yaml:"id"`
	Decision       domain.AssistantPermissionDecision  `koanf:"decision" yaml:"decision"`
	ToolNames      []string                            `koanf:"tool_names" yaml:"tool_names"`
	ToolPrefixes   []string                            `koanf:"tool_prefixes" yaml:"tool_prefixes"`
	Effects        []domain.AssistantToolEffect        `koanf:"effects" yaml:"effects"`
	Risks          []domain.AssistantToolRisk          `koanf:"risks" yaml:"risks"`
	ExecutionModes []domain.AssistantToolExecutionMode `koanf:"execution_modes" yaml:"execution_modes"`
	ResourceTypes  []string                            `koanf:"resource_types" yaml:"resource_types"`
	Reason         string                              `koanf:"reason" yaml:"reason"`
}

// PackageControlplaneConfig registers package repository backends and source-fetch guardrails.
type PackageControlplaneConfig struct {
	Enabled            bool                            `koanf:"enabled"`
	AllowedSourceHosts []string                        `koanf:"allowed_source_hosts"`
	AllowHTTPSource    bool                            `koanf:"allow_http_source"`
	AllowFileSource    bool                            `koanf:"allow_file_source"`
	Backends           map[string]PackageBackendConfig `koanf:"backends"`
}

// PackageBackendConfig describes one named package backend connector.
// Secrets must be referenced by name/path and resolved by the secrets layer;
// inline passwords, tokens, or private key material are intentionally not part
// of this config shape.
type PackageBackendConfig struct {
	Type               string            `koanf:"type"`
	BaseURL            string            `koanf:"base_url"`
	RootDir            string            `koanf:"root_dir"`
	PublicBaseURL      string            `koanf:"public_base_url"`
	Timeout            time.Duration     `koanf:"timeout"`
	InsecureSkipVerify bool              `koanf:"insecure_skip_verify"`
	AuthSecretRef      string            `koanf:"auth_secret_ref"`
	TLSSecretRef       string            `koanf:"tls_secret_ref"`
	SecretRefs         map[string]string `koanf:"secret_refs"`
}

// LLMControlplaneConfig holds DB-first LLM provisioning control-plane settings.
type LLMControlplaneConfig struct {
	Enabled              bool                                `koanf:"enabled"`
	AllowOperationalREST bool                                `koanf:"allow_operational_rest"`
	DefaultGatewayRef    string                              `koanf:"default_gateway_ref"`
	RecoveryPollInterval time.Duration                       `koanf:"recovery_poll_interval"`
	StaleRunTimeout      time.Duration                       `koanf:"stale_run_timeout"`
	ReconcileInterval    time.Duration                       `koanf:"reconcile_interval"`
	Gateways             map[string]LLMGatewayEndpointConfig `koanf:"gateways"`
	OperatorAccessConfig `koanf:",squash"`
}

// LLMGatewayEndpointConfig describes one inference gateway admin endpoint.
type LLMGatewayEndpointConfig struct {
	Type          string        `koanf:"type"`
	BaseURL       string        `koanf:"base_url"`
	AuthToken     string        `koanf:"auth_token"`
	AuthTokenFile string        `koanf:"auth_token_file"`
	Timeout       time.Duration `koanf:"timeout"`
}

// RegistryAdapterConfig holds OCI registry adapter settings for multi-registry support.
// When configured, this supersedes HarborConfig for image verification.
type RegistryAdapterConfig struct {
	Type     string `koanf:"type"`     // ghcr, dockerhub, harbor, oci (auto-detected from URL if empty)
	URL      string `koanf:"url"`      // registry base URL (required for harbor/oci)
	Username string `koanf:"username"` // credentials (optional for public repos)
	Password string `koanf:"password"` // password or PAT
}

// ServerConfig holds HTTP server settings.
type ServerConfig struct {
	Host            string        `koanf:"host"`
	Port            int           `koanf:"port"`
	ReadTimeout     time.Duration `koanf:"read_timeout"`
	WriteTimeout    time.Duration `koanf:"write_timeout"`
	ShutdownTimeout time.Duration `koanf:"shutdown_timeout"`
}

// DBConfig holds PostgreSQL connection settings.
type DBConfig struct {
	Host            string        `koanf:"host"`
	Port            int           `koanf:"port"`
	User            string        `koanf:"user"`
	Password        string        `koanf:"password"`
	Name            string        `koanf:"name"`
	SSLMode         string        `koanf:"sslmode"`
	MaxOpenConns    int           `koanf:"max_open_conns"`
	MaxIdleConns    int           `koanf:"max_idle_conns"`
	ConnMaxLifetime time.Duration `koanf:"conn_max_lifetime"`
}

// DSN returns a PostgreSQL connection string with properly escaped components.
func (c DBConfig) DSN() string {
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(c.User, c.Password),
		Host:   fmt.Sprintf("%s:%d", c.Host, c.Port),
		Path:   c.Name,
	}
	q := u.Query()
	q.Set("sslmode", c.SSLMode)
	u.RawQuery = q.Encode()
	return u.String()
}

// HarborConfig holds Harbor registry settings.
type HarborConfig struct {
	URL      string `koanf:"url"`
	Username string `koanf:"username"`
	Password string `koanf:"password"`
	Insecure bool   `koanf:"insecure"`
	Enabled  bool   `koanf:"enabled"`
}

// LoomConfig holds Loom worker integration settings.
type LoomConfig struct {
	Relays              []string                      `koanf:"relays"`
	JobTimeout          time.Duration                 `koanf:"job_timeout"`
	CanonicalProjection LoomCanonicalProjectionConfig `koanf:"canonical_projection" yaml:"canonical_projection"`
}

// LoomCanonicalProjectionConfig controls projection of Loom-native status/result
// events into canonical CAS 30900 state and 4903 audit events.
type LoomCanonicalProjectionConfig struct {
	Enabled               bool          `koanf:"enabled" yaml:"enabled"`
	SignetBunkerURI       string        `koanf:"signet_bunker_uri" yaml:"signet_bunker_uri"`
	SignetClientSecretKey string        `koanf:"signet_client_secret_key" yaml:"signet_client_secret_key"`
	SignetConnectTimeout  time.Duration `koanf:"signet_connect_timeout" yaml:"signet_connect_timeout"`
	AllowRawKeyDev        bool          `koanf:"allow_raw_key_dev" yaml:"allow_raw_key_dev"`
	RawPrivateKey         string        `koanf:"raw_private_key" yaml:"raw_private_key"`
}

const RelayAuthUnavailableExcludeAndFail = "exclude_and_fail"

// NostrConfig holds Nostr relay and identity settings.
type NostrConfig struct {
	PrivateKey                 string                    `koanf:"private_key"`
	Relays                     []string                  `koanf:"relays"`
	ServiceRelays              []string                  `koanf:"service_relays"`
	BrowserRelays              []string                  `koanf:"browser_relays"`
	ContextVMRelays            []string                  `koanf:"contextvm_relays"`
	NIP34Relays                []string                  `koanf:"nip34_relays" yaml:"nip34_relays"`
	TrustedRelayMonitorPubkeys []string                  `koanf:"trusted_relay_monitor_pubkeys" yaml:"trusted_relay_monitor_pubkeys"`
	DMRelayLists               []DMRelayListConfig       `koanf:"dm_relay_lists" yaml:"dm_relay_lists"`
	RelayAdministration        RelayAdministrationConfig `koanf:"relay_administration" yaml:"relay_administration"`

	// RelayAuthUnavailablePolicy is fixed to exclude_and_fail: if a relay requires
	// NIP-42 AUTH and no valid signer is available for the operation, consumers
	// must exclude that relay, surface the CLOSED/OK reason in health/error
	// metadata, and fail deterministically if the remaining relays cannot satisfy
	// the operation's read/publish success rule.
	RelayAuthUnavailablePolicy string `koanf:"relay_auth_unavailable" yaml:"relay_auth_unavailable"`

	// PrivateRelays and PrivateBrowserRelays are internal mirrors for runtime
	// callers that have not moved to the canonical field names yet. They are not
	// loaded from config; nostr.private_relays and nostr.private_browser_relays
	// are rejected in Load.
	PrivateRelays        []string `koanf:"-"`
	PrivateBrowserRelays []string `koanf:"-"`

	AuthorizedPubkeys []string `koanf:"authorized_pubkeys"`
	PublishEnabled    bool     `koanf:"publish_enabled"`
	// StaleRunAfter is the maximum silence allowed between Loom kind-30100
	// status events before Bahia publishes a domain-health status event.
	StaleRunAfter time.Duration `koanf:"stale_run_after" yaml:"stale_run_after"`
	// LegacyRelayBackfill explicitly enables startup reads of retired Bahia
	// request kinds from an external migration relay. The hardened Bahia
	// sidecar intentionally refuses those reads, so this must remain opt-in.
	LegacyRelayBackfill bool               `koanf:"legacy_relay_backfill" yaml:"legacy_relay_backfill"`
	RelayQuorum         RelayQuorumConfig  `koanf:"relay_quorum" yaml:"relay_quorum"`
	Sidecar             RelaySidecarConfig `koanf:"sidecar"`
}

const (
	DMRelayListFeatureNotifications = "notifications"
	DMRelayListIdentityService      = "service"
)

// DMRelayListConfig configures an explicit NIP-51 kind 10050 receive relay list.
// It is never inferred from browser, service, or ContextVM relay policies.
type DMRelayListConfig struct {
	Enabled  bool     `koanf:"enabled" yaml:"enabled"`
	Feature  string   `koanf:"feature" yaml:"feature"`
	Identity string   `koanf:"identity" yaml:"identity"`
	Relays   []string `koanf:"relays" yaml:"relays"`
}

// RelayQuorumConfig holds readiness quorum thresholds by operating mode.
type RelayQuorumConfig struct {
	FullMinHealthy      int `koanf:"full_min_healthy" yaml:"full_min_healthy"`
	DegradedMinHealthy  int `koanf:"degraded_min_healthy" yaml:"degraded_min_healthy"`
	EmergencyMinHealthy int `koanf:"emergency_min_healthy" yaml:"emergency_min_healthy"`
}

// RelaySidecarConfig holds the local Khatru relay sidecar settings.
type RelaySidecarConfig struct {
	Enabled              bool          `koanf:"enabled"`
	ListenAddr           string        `koanf:"listen_addr"`
	PublicURL            string        `koanf:"public_url"`
	BackendURL           string        `koanf:"backend_url"`
	DataDir              string        `koanf:"data_dir"`
	MirrorExternal       bool          `koanf:"mirror_external"`
	EventRetention       time.Duration `koanf:"event_retention"`
	RequestRetention     time.Duration `koanf:"request_retention"`
	AuthPrivateKey       string        `koanf:"auth_private_key"`
	AdministratorPubkeys []string      `koanf:"administrator_pubkeys" yaml:"administrator_pubkeys"`
	ConfigTrustedPubkeys []string      `koanf:"config_trusted_pubkeys" yaml:"config_trusted_pubkeys"`
	AdminPolicyPath      string        `koanf:"admin_policy_path" yaml:"admin_policy_path"`
	ConfigProjectionPath string        `koanf:"config_projection_path" yaml:"config_projection_path"`
	ServiceID            string        `koanf:"service_id" yaml:"service_id"`
	Scope                string        `koanf:"scope" yaml:"scope"`
	MaxQueryLimit        int           `koanf:"max_query_limit"`
}

// RelayAdministrationAuthorization values declare why a NIP-86 target is in
// scope. They are operator assertions for Bahia-owned/Bahia-authorized relays;
// relay-side authorization is still enforced by the relay against the signed
// NIP-98 administrator pubkey.
const (
	RelayAdministrationBahiaOwned      = "bahia_owned"
	RelayAdministrationBahiaAuthorized = "bahia_authorized"
)

// RelayAdministrationConfig holds optional NIP-86 HTTP relay-owner management
// settings. It is disabled by default and intentionally separate from NIP-42
// websocket AUTH and ContextVM application/control-plane mutation transport.
type RelayAdministrationConfig struct {
	Enabled                    bool                        `koanf:"enabled" yaml:"enabled"`
	AdministratorPrivateKeyRef string                      `koanf:"administrator_private_key_ref" yaml:"administrator_private_key_ref"`
	Targets                    []RelayAdministrationTarget `koanf:"targets" yaml:"targets"`
}

// RelayAdministrationTarget is one explicitly allowed NIP-86 management target.
// RelayURL is the websocket relay URL used in the NIP-98 `u` tag. HTTPURL may
// be set when the relay's HTTP management endpoint differs from the ws/wss URL
// converted to http/https.
type RelayAdministrationTarget struct {
	Ref                  string   `koanf:"ref" yaml:"ref"`
	RelayURL             string   `koanf:"relay_url" yaml:"relay_url"`
	HTTPURL              string   `koanf:"http_url" yaml:"http_url"`
	Authorization        string   `koanf:"authorization" yaml:"authorization"`
	AdministratorPubkeys []string `koanf:"administrator_pubkeys" yaml:"administrator_pubkeys"`
}

// ReconcileConfig holds reconciliation loop settings.
type ReconcileConfig struct {
	Interval time.Duration `koanf:"interval"`
	Enabled  bool          `koanf:"enabled"`
}

// RuntimeTargetConfig holds the connection settings for one runtime target.
type RuntimeTargetConfig struct {
	Type          string `koanf:"type"`
	DockerHost    string `koanf:"docker_host"`
	EndpointRef   string `koanf:"endpoint_ref"`
	ComposeDir    string `koanf:"compose_dir"`
	BahiaOwned    *bool  `koanf:"bahia_owned"`
	ExecutionMode string `koanf:"execution_mode"`
	KubeContext   string `koanf:"kube_context"`
	KubeNamespace string `koanf:"kube_namespace"`
	KubeConfig    string `koanf:"kube_config"`

	// VM holds settings for the vm-firecracker and vm-qemu runtime types.
	VM RuntimeVMConfig `koanf:"vm"`

	ResolvedEndpoint RuntimeEndpointConfig `koanf:"-"`
}

// RuntimeVMConfig holds host-local VM runtime settings for the
// vm-firecracker and vm-qemu runtime types. Setting any of these fields for a
// non-VM runtime type is a configuration error (explicit-failure convention).
type RuntimeVMConfig struct {
	// StateDir is the host directory holding per-instance VM state
	// (pidfiles, API sockets, metadata.json, overlays).
	StateDir string `koanf:"state_dir"`
	// ImageRoot is the host directory containing VM image release channels
	// (each channel a hash-pinned release dir with manifest.json and an
	// atomic "current" symlink).
	ImageRoot string `koanf:"image_root"`
	// LibvirtURI is the libvirt connection URI (vm-qemu only).
	LibvirtURI string `koanf:"libvirt_uri"`
	// VsockGuestPort is the guest agent vsock port used for ping/metrics.
	VsockGuestPort int `koanf:"vsock_guest_port"`
	// VCPUs is the default vCPU count for instances without an explicit spec.
	VCPUs int `koanf:"vcpus"`
	// MemoryMB is the default memory size (MiB) for instances without an
	// explicit spec.
	MemoryMB int `koanf:"memory_mb"`
	// NetworkProfile names the host network profile applied to instances.
	NetworkProfile string `koanf:"network_profile"`
}

// Empty reports whether no VM runtime settings are configured.
func (c RuntimeVMConfig) Empty() bool {
	return strings.TrimSpace(c.StateDir) == "" &&
		strings.TrimSpace(c.ImageRoot) == "" &&
		strings.TrimSpace(c.LibvirtURI) == "" &&
		c.VsockGuestPort == 0 &&
		c.VCPUs == 0 &&
		c.MemoryMB == 0 &&
		strings.TrimSpace(c.NetworkProfile) == ""
}

// RuntimeEndpointConfig holds server-managed Docker endpoint transport settings.
type RuntimeEndpointConfig struct {
	Ref                string `koanf:"-"`
	DockerHost         string `koanf:"docker_host"`
	CACertFile         string `koanf:"ca_cert_file"`
	ClientCertFile     string `koanf:"client_cert_file"`
	ClientKeyFile      string `koanf:"client_key_file"`
	InsecureSkipVerify bool   `koanf:"insecure_skip_verify"`
}

// Empty reports whether no endpoint transport settings are configured.
func (c RuntimeEndpointConfig) Empty() bool {
	return strings.TrimSpace(c.DockerHost) == "" &&
		strings.TrimSpace(c.CACertFile) == "" &&
		strings.TrimSpace(c.ClientCertFile) == "" &&
		strings.TrimSpace(c.ClientKeyFile) == "" &&
		!c.InsecureSkipVerify
}

// RuntimeConfig holds runtime targeting settings.
//
// The flat fields are retained for backward compatibility with existing
// runtime.type, runtime.docker_host, and runtime.compose_dir configuration.
// New installations should prefer runtime.default.* plus
// runtime.environments.<environment-name>.*. Environment variables for nested
// runtime settings must use double underscores, for example:
// BAHIA_RUNTIME__DEFAULT__TYPE=compose and
// BAHIA_RUNTIME__ENVIRONMENTS__production__COMPOSE_DIR=/srv/bahia/prod.
type RuntimeConfig struct {
	// Legacy flat fields.
	Type          string          `koanf:"type"`
	DockerHost    string          `koanf:"docker_host"`
	ComposeDir    string          `koanf:"compose_dir"`
	BahiaOwned    *bool           `koanf:"bahia_owned"`
	ExecutionMode string          `koanf:"execution_mode"`
	KubeContext   string          `koanf:"kube_context"`
	KubeNamespace string          `koanf:"kube_namespace"`
	KubeConfig    string          `koanf:"kube_config"`
	VM            RuntimeVMConfig `koanf:"vm"`

	// Environment-targeted fields.
	Default      RuntimeTargetConfig              `koanf:"default"`
	Environments map[string]RuntimeTargetConfig   `koanf:"environments"`
	Endpoints    map[string]RuntimeEndpointConfig `koanf:"endpoints"`
}

// LogConfig holds logging settings.
type LogConfig struct {
	Level  string `koanf:"level"`
	Format string `koanf:"format"`
}

// AuthConfig holds authentication settings.
// When enabled, protected HTTP routes require NIP-98 Authorization headers.
type AuthConfig struct {
	Enabled               bool     `koanf:"enabled"`
	BootstrapOwnerPubkeys []string `koanf:"bootstrap_owner_pubkeys"`
}

// OperatorAccessConfig holds system-operator allowlists for privileged API routes.
type OperatorAccessConfig struct {
	AllowedSubjects []string `koanf:"allowed_subjects"`
	AllowedPubkeys  []string `koanf:"allowed_pubkeys"`
	AllowedEmails   []string `koanf:"allowed_emails"`
}

// Empty reports whether no operator identities are configured.
func (c OperatorAccessConfig) Empty() bool {
	return len(c.AllowedSubjects) == 0 && len(c.AllowedPubkeys) == 0 && len(c.AllowedEmails) == 0
}

// AdoptionConfig holds privileged adoption route settings.
type AdoptionConfig struct {
	Enabled              bool `koanf:"enabled"`
	AllowRawDockerHosts  bool `koanf:"allow_raw_docker_hosts"`
	AllowComposeTakeover bool `koanf:"allow_compose_takeover"`
	OperatorAccessConfig `koanf:",squash"`
}

// DirectRuntimeConfig holds privileged direct runtime action route settings.
type DirectRuntimeConfig struct {
	Enabled              bool `koanf:"enabled"`
	OperatorAccessConfig `koanf:",squash"`
}

// CORSConfig holds CORS middleware settings.
type CORSConfig struct {
	// AllowedOrigins is the list of origins allowed to make cross-origin requests.
	// If empty, no origins are allowed (secure default).
	// Use ["*"] only for development.
	AllowedOrigins []string `koanf:"allowed_origins"`
}

// BlossomConfig holds Blossom media/blob storage settings.
type BlossomConfig struct {
	Enabled      bool          `koanf:"enabled"`
	URL          string        `koanf:"url"`
	Servers      []string      `koanf:"servers"`
	Timeout      time.Duration `koanf:"timeout"`
	MaxRetries   int           `koanf:"max_retries"`
	RetryDelay   time.Duration `koanf:"retry_delay"`
	PrivateKey   string        `koanf:"private_key"`
	StorageClass string        `koanf:"storage_class"`
}

// OCIServerConfig holds server-side OCI registry settings.
type OCIServerConfig struct {
	Enabled                 bool                      `koanf:"enabled"`
	PublicHost              string                    `koanf:"public_host"`
	SpoolDir                string                    `koanf:"spool_dir"`
	UploadExpiry            time.Duration             `koanf:"upload_expiry"`
	AllowAnonymousPullCIDRs []string                  `koanf:"allow_anonymous_pull_cidrs"`
	TrustedProxyCIDRs       []string                  `koanf:"trusted_proxy_cidrs"`
	AuthorizedPushPubkeys   []string                  `koanf:"authorized_push_pubkeys"`
	ServiceAccounts         []OCIServiceAccountConfig `koanf:"service_accounts"`
}

// OCIServiceAccountConfig defines a basic-auth service account for OCI token/auth flows.
type OCIServiceAccountConfig struct {
	Username     string   `koanf:"username"`
	PasswordHash string   `koanf:"password_hash"`
	Permissions  []string `koanf:"permissions"`   // pull, push
	RepoPrefixes []string `koanf:"repo_prefixes"` // e.g. cascadia/
}

// HiveCIPolicyConfig declares a pipeline policy to ensure exists at startup.
// Policies are matched by (repo_coordinate, workflow_path, service_name,
// environment_name); if a matching row already exists it is left untouched.
type HiveCIPolicyConfig struct {
	RepoCoordinate  string         `koanf:"repo_coordinate" yaml:"repo_coordinate"`
	WorkflowPath    string         `koanf:"workflow_path" yaml:"workflow_path"`
	BranchPattern   string         `koanf:"branch_pattern" yaml:"branch_pattern"`
	ServiceName     string         `koanf:"service_name" yaml:"service_name"`
	EnvironmentName string         `koanf:"environment_name" yaml:"environment_name"`
	Enabled         *bool          `koanf:"enabled" yaml:"enabled"`
	Metadata        map[string]any `koanf:"metadata" yaml:"metadata"`
}

// HiveCIInitiatorConfig configures the fleet Gitea private-mirror and
// Hive-CI build initiation adapter. When Enabled, all fields except
// RepoAnnouncementAddr and RelayHint are required; the GitHub credential is
// never configured here — it is resolved per request from an opaque
// server-side secret reference.
type HiveCIInitiatorConfig struct {
	Enabled bool `koanf:"enabled"`
	// GiteaBaseURL is the fleet Gitea API base URL, e.g. https://git.fleet.internal
	GiteaBaseURL string `koanf:"gitea_base_url"`
	// GiteaToken is the fleet Gitea admin token used for mirror provisioning.
	GiteaToken string `koanf:"gitea_token"`
	// MirrorOwner is the Gitea org/user that owns private mirrors.
	MirrorOwner string `koanf:"mirror_owner"`
	// WorkflowPath is the Hive-CI workflow file invoked for builds.
	WorkflowPath string `koanf:"workflow_path"`
	// SourceCloneURL optionally overrides the upstream clone URL.
	SourceCloneURL string `koanf:"source_clone_url"`
	// RepoAnnouncementAddr optionally carries the NIP-34 30617 address of the mirror.
	RepoAnnouncementAddr string `koanf:"repo_announcement_addr"`
	// RelayHint is attached to published run-request/evidence events.
	RelayHint string `koanf:"relay_hint"`
}

// HiveCIConfig holds Hive-CI integration settings.
type HiveCIConfig struct {
	Enabled                         bool                 `koanf:"enabled"`
	TrustedCIPubkeys                []string             `koanf:"trusted_ci_pubkeys"`
	TrustedReleaseAttestors         []string             `koanf:"trusted_release_attestors"`
	AutoRegisterBuilds              bool                 `koanf:"auto_register_builds"`
	AllowManualArtifactRegistration bool                 `koanf:"allow_manual_artifact_registration"`
	RetryInterval                   time.Duration        `koanf:"retry_interval"`
	MaxRetries                      int                  `koanf:"max_retries"`
	Policies                        []HiveCIPolicyConfig  `koanf:"policies" yaml:"policies"`
	Initiator                       HiveCIInitiatorConfig `koanf:"initiator" yaml:"initiator"`
}

// CashuConfig holds Cashu ecash payment integration settings.
type CashuConfig struct {
	Enabled  bool   `koanf:"enabled"`
	MintURL  string `koanf:"mint_url"`
	WalletDB string `koanf:"wallet_db"` // path to wallet database
}

// QdrantConfig holds vector database settings.
type QdrantConfig struct {
	URL                       string        `koanf:"url"`
	Timeout                   time.Duration `koanf:"timeout"`
	APIKey                    string        `koanf:"api_key"`
	AuthHeaderName            string        `koanf:"auth_header_name"`
	AllowUnauthenticatedLocal bool          `koanf:"allow_unauthenticated_local"`
}

// TelemetryConfig holds observability / metrics settings.
type TelemetryConfig struct {
	Enabled      bool   `koanf:"enabled"`
	ServiceName  string `koanf:"service_name"`
	OTLPEndpoint string `koanf:"otlp_endpoint"` // OpenTelemetry collector endpoint
}

// NotificationsConfig holds notification dispatcher settings.
type NotificationsConfig struct {
	Enabled    bool     `koanf:"enabled"`
	WebhookURL string   `koanf:"webhook_url"`
	NostrDM    bool     `koanf:"nostr_dm"` // send DMs to subscribed pubkeys
	Kinds      []string `koanf:"kinds"`    // event kinds to notify on (e.g. "deployment.completed")
}

func defaultWorkerPressureConfig() WorkerPressureConfig {
	return WorkerPressureConfig{
		MemoryWarningMinGB:  4,
		MemoryWarningRatio:  0.20,
		MemoryCriticalMinGB: 2,
		MemoryCriticalRatio: 0.10,
		DiskWarningMinGB:    40,
		DiskWarningRatio:    0.15,
		DiskCriticalMinGB:   20,
		DiskCriticalRatio:   0.08,
		VRAMWarningMinGB:    4,
		VRAMWarningRatio:    0.20,
		VRAMCriticalMinGB:   2,
		VRAMCriticalRatio:   0.10,
		ThermalWarningC:     85,
		ThermalCriticalC:    92,
		QueueWarningRatio:   0.80,
		QueueCriticalRatio:  1.0,
	}
}

// SupervisionConfig controls local managed-instance health checks and recovery.
type SupervisionConfig struct {
	Enabled            bool                        `koanf:"enabled" yaml:"enabled"`
	ObserveOnly        bool                        `koanf:"observe_only" yaml:"observe_only"`
	Interval           time.Duration               `koanf:"interval" yaml:"interval"`
	ObservationTimeout time.Duration               `koanf:"observation_timeout" yaml:"observation_timeout"`
	MemoryThreshold    float64                     `koanf:"memory_threshold" yaml:"memory_threshold"`
	Instances          []SupervisionInstanceConfig `koanf:"instances" yaml:"instances"`
}

// SupervisionInstanceConfig identifies one explicitly configured managed instance.
type SupervisionInstanceConfig struct {
	ServiceID          string        `koanf:"service_id" yaml:"service_id"`
	EnvironmentID      string        `koanf:"environment_id" yaml:"environment_id"`
	DeploymentUnitID   string        `koanf:"deployment_unit_id" yaml:"deployment_unit_id"`
	RuntimeTargetName  string        `koanf:"runtime_target_name" yaml:"runtime_target_name"`
	Host               string        `koanf:"host" yaml:"host"`
	SupervisorType     string        `koanf:"supervisor_type" yaml:"supervisor_type"`
	DesiredRunning     bool          `koanf:"desired_running" yaml:"desired_running"`
	DockerHost         string        `koanf:"docker_host" yaml:"docker_host"`
	ComposeDir         string        `koanf:"compose_dir" yaml:"compose_dir"`
	ProbeURL           string        `koanf:"probe_url" yaml:"probe_url"`
	ProbeTimeout       time.Duration `koanf:"probe_timeout" yaml:"probe_timeout"`
	RestartMaxAttempts int           `koanf:"restart_max_attempts" yaml:"restart_max_attempts"`
	RestartWindow      time.Duration `koanf:"restart_window" yaml:"restart_window"`
	BackoffBase        time.Duration `koanf:"backoff_base" yaml:"backoff_base"`
	BackoffCap         time.Duration `koanf:"backoff_cap" yaml:"backoff_cap"`
	WarningMinInterval time.Duration `koanf:"warning_min_interval" yaml:"warning_min_interval"`
}

// Defaults returns a Config with sensible default values.
func Defaults() *Config {
	return &Config{
		Mode: "full",
		Server: ServerConfig{
			Host:            "127.0.0.1",
			Port:            8080,
			ReadTimeout:     30 * time.Second,
			WriteTimeout:    30 * time.Second,
			ShutdownTimeout: 15 * time.Second,
		},
		DB: DBConfig{
			Host:            "localhost",
			Port:            5432,
			User:            "bahia",
			Password:        "",
			Name:            "bahia",
			SSLMode:         "require",
			MaxOpenConns:    25,
			MaxIdleConns:    5,
			ConnMaxLifetime: 5 * time.Minute,
		},
		Harbor: HarborConfig{
			URL:      "https://harbor.example.com",
			Insecure: false,
			Enabled:  false,
		},
		Loom: LoomConfig{
			JobTimeout: 30 * time.Minute,
		},
		Nostr: NostrConfig{
			ContextVMRelays:            []string{},
			PublishEnabled:             true,
			StaleRunAfter:              5 * time.Minute,
			RelayAuthUnavailablePolicy: RelayAuthUnavailableExcludeAndFail,
			RelayQuorum: RelayQuorumConfig{
				FullMinHealthy:      2,
				DegradedMinHealthy:  1,
				EmergencyMinHealthy: 1,
			},
			Sidecar: RelaySidecarConfig{
				Enabled:          false,
				ListenAddr:       "127.0.0.1:3334",
				PublicURL:        "ws://127.0.0.1:3334",
				BackendURL:       "ws://127.0.0.1:3334",
				DataDir:          "./data/relay-sidecar",
				MirrorExternal:   false,
				EventRetention:   7 * 24 * time.Hour,
				RequestRetention: 24 * time.Hour,
				ServiceID:        "bahia-relay-sidecar",
				Scope:            "prod",
				MaxQueryLimit:    2000,
			},
		},
		Reconcile: ReconcileConfig{
			Interval: 60 * time.Second,
			Enabled:  true,
		},
		Supervision: SupervisionConfig{
			Enabled:            false,
			ObserveOnly:        true,
			Interval:           30 * time.Second,
			ObservationTimeout: 30 * time.Second,
			MemoryThreshold:    0.90,
			Instances:          []SupervisionInstanceConfig{},
		},
		Runtime: RuntimeConfig{
			Type:         "docker",
			DockerHost:   "unix:///var/run/docker.sock",
			Environments: map[string]RuntimeTargetConfig{},
			Endpoints:    map[string]RuntimeEndpointConfig{},
		},
		LLM: LLMControlplaneConfig{
			Enabled:              false,
			AllowOperationalREST: false,
			RecoveryPollInterval: 30 * time.Second,
			StaleRunTimeout:      15 * time.Minute,
			ReconcileInterval:    60 * time.Second,
			Gateways:             map[string]LLMGatewayEndpointConfig{},
		},
		Assistant: AssistantConfig{
			Enabled:    false,
			LLMBaseURL: "https://api.openai.com",
			Agentic: AssistantAgenticConfig{
				Enabled:                    true,
				Provider:                   "openai_compatible",
				ToolMode:                   AssistantAgenticToolModeNative,
				BaseURL:                    "https://api.openai.com",
				MaxIterations:              12,
				MaxConsecutiveToolFailures: 3,
				RequestTimeout:             110 * time.Second,
			},
			Permissions: AssistantPermissionsConfig{
				Mode: domain.AssistantPermissionModeAudited,
			},
			MCP: AssistantMCPConfig{
				AsyncObservation: AssistantMCPAsyncObservationConfig{
					MaxWait:       30 * time.Minute,
					BackfillLimit: 50,
				},
			},
		},
		DNS: DNSConfig{
			Enabled:           false,
			DefaultTTL:        300,
			ReconcileInterval: 30 * time.Second,
			Zones:             []DNSZoneConfig{},
			Backends:          map[string]DNSBackendConfig{},
			Projection: DNSProjectionConfig{
				Services:         true,
				LLMRoutes:        true,
				MLEndpoints:      true,
				Workers:          true,
				EnvironmentZones: map[string]string{},
			},
		},
		FIPS: FIPSConfig{
			Enabled:              false,
			RelayURLs:            []string{},
			AppNamespace:         "fips-overlay-v1",
			AutoRegisterWorkers:  false,
			AllowedNpubs:         []string{},
			OverlayAddressPrefix: "fd00",
		},
		SoulFactory: SoulFactoryConfig{
			Enabled:          false,
			Relays:           []string{},
			AdditionalRelays: []string{},
			NIP05Relays:      []string{},
			StartupTimeout:   15 * time.Second,
			LLMTimeout:       120 * time.Second,
		},
		Packages: PackageControlplaneConfig{
			Enabled:            false,
			AllowedSourceHosts: []string{},
			Backends:           map[string]PackageBackendConfig{},
		},
		Log: LogConfig{
			Level:  "info",
			Format: "json",
		},
		Auth: AuthConfig{
			Enabled: false,
		},
		Adoption: AdoptionConfig{
			Enabled: false,
		},
		DirectRuntime: DirectRuntimeConfig{
			Enabled: false,
		},
		CORS: CORSConfig{
			AllowedOrigins: []string{}, // secure default: no cross-origin requests
		},
		Blossom: BlossomConfig{
			Enabled:    false,
			Timeout:    30 * time.Second,
			MaxRetries: 3,
			RetryDelay: 1 * time.Second,
		},
		SBOM: SBOMConfig{
			Cdxgen: SBOMCdxgenConfig{
				Enabled:    false,
				BinaryPath: "cdxgen",
			},
		},
		OCI: OCIServerConfig{
			Enabled:                 false,
			SpoolDir:                "/tmp/bahia-oci-spool",
			UploadExpiry:            24 * time.Hour,
			AllowAnonymousPullCIDRs: []string{},
			ServiceAccounts:         []OCIServiceAccountConfig{},
		},
		HiveCI: HiveCIConfig{
			Enabled:                         false,
			AutoRegisterBuilds:              true,
			AllowManualArtifactRegistration: false,
			RetryInterval:                   30 * time.Second,
			MaxRetries:                      10,
		},
		Cashu: CashuConfig{
			Enabled: false,
		},
		Qdrant: QdrantConfig{
			Timeout: 30 * time.Second,
		},
		Telemetry: TelemetryConfig{
			Enabled:     false,
			ServiceName: "bahia",
		},
		WorkerPressure: defaultWorkerPressureConfig(),
		WorkerCleanup: WorkerCleanupConfig{
			Mode:             "recommend_only",
			Cooldown:         30 * time.Minute,
			TargetFreeGB:     40,
			RequiredSoftware: []string{"bash", "docker"},
		},
		Notifications: NotificationsConfig{
			Enabled: false,
		},
	}
}

// Load reads configuration from an optional YAML file and environment variables.
// Environment variables are prefixed with BAHIA_ and use _ as the section separator
// (e.g. BAHIA_DB_HOST → db.host). Double underscore (__) is accepted as an
// explicit nested separator and is required for nested runtime maps such as
// BAHIA_RUNTIME__DEFAULT__TYPE or
// BAHIA_RUNTIME__ENVIRONMENTS__production__COMPOSE_DIR.
func Load(configPath string) (*Config, error) {
	k := koanf.New(".")
	cfg := Defaults()
	protectedMutableEnv, err := seedMutablePolicy(configPath)
	if err != nil {
		return nil, err
	}

	// Load from YAML file if provided.
	if configPath != "" {
		if err := k.Load(file.Provider(configPath), yaml.Parser()); err != nil {
			return nil, fmt.Errorf("loading config file %s: %w", configPath, err)
		}
	}

	// Load from environment variables with BAHIA_ prefix.
	// Convention: BAHIA_<SECTION>_<FIELD> maps to section.field in koanf.
	// All config sections are single words (server, db, harbor, etc.),
	// so only the first underscore separates section from field name.
	// Field names may contain underscores (e.g., read_timeout, max_open_conns).
	// Double-underscore (__) is also accepted as an explicit level separator.
	if err := k.Load(env.Provider("BAHIA_", ".", func(s string) string {
		if _, protected := protectedMutableEnv[s]; protected {
			return "bootstrap_ignored." + strings.ToLower(strings.TrimPrefix(s, "BAHIA_"))
		}
		key := strings.ToLower(strings.TrimPrefix(s, "BAHIA_"))
		// First, honour explicit double-underscore separators.
		key = strings.ReplaceAll(key, "__", ".")
		if strings.HasPrefix(key, "worker_pressure_") {
			return "worker_pressure." + strings.TrimPrefix(key, "worker_pressure_")
		}
		if strings.HasPrefix(key, "soul_factory_") {
			return "soul_factory." + strings.TrimPrefix(key, "soul_factory_")
		}
		if strings.HasPrefix(key, "loom_canonical_projection_") {
			return "loom.canonical_projection." + strings.TrimPrefix(key, "loom_canonical_projection_")
		}
		if strings.HasPrefix(key, "sbom_cdxgen_") {
			return "sbom.cdxgen." + strings.TrimPrefix(key, "sbom_cdxgen_")
		}
		if strings.HasPrefix(key, "assistant_agentic_") {
			return "assistant.agentic." + strings.TrimPrefix(key, "assistant_agentic_")
		}
		if strings.HasPrefix(key, "assistant_permissions_") {
			return "assistant.permissions." + strings.TrimPrefix(key, "assistant_permissions_")
		}
		if strings.HasPrefix(key, "assistant_mcp_async_observation_") {
			return "assistant.mcp.async_observation." + strings.TrimPrefix(key, "assistant_mcp_async_observation_")
		}
		switch key {
		case "dev_mode":
			return "dev_mode"
		case "assistant_enabled", "assistant_llm_base_url", "assistant_llm_model", "assistant_llm_api_key", "assistant_llm_streaming":
			return key
		}
		// If no explicit separator was found, split on the first underscore.
		if !strings.Contains(key, ".") {
			if i := strings.Index(key, "_"); i >= 0 {
				key = key[:i] + "." + key[i+1:]
			}
		}
		return key
	}), nil); err != nil {
		return nil, fmt.Errorf("loading environment config: %w", err)
	}

	if err := rejectRemovedAuthKeys(k); err != nil {
		return nil, err
	}
	if err := rejectRemovedEncryptedRequestKeys(k); err != nil {
		return nil, err
	}
	if err := k.Unmarshal("", cfg); err != nil {
		return nil, fmt.Errorf("unmarshaling config: %w", err)
	}
	applyAssistantFlatCompat(k, cfg)
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func applyAssistantFlatCompat(k *koanf.Koanf, cfg *Config) {
	if k.Exists("assistant_enabled") {
		cfg.Assistant.Enabled = k.Bool("assistant_enabled")
	}
	if k.Exists("assistant_llm_base_url") {
		cfg.Assistant.LLMBaseURL = k.String("assistant_llm_base_url")
	}
	if k.Exists("assistant_llm_model") {
		cfg.Assistant.LLMModel = k.String("assistant_llm_model")
	}
	if k.Exists("assistant_llm_api_key") {
		cfg.Assistant.LLMAPIKey = k.String("assistant_llm_api_key")
	}
	if k.Exists("assistant_llm_streaming") {
		cfg.Assistant.LLMStreaming = k.Bool("assistant_llm_streaming")
	}
}

func rejectRemovedAuthKeys(k *koanf.Koanf) error {
	for _, key := range []string{"auth.jwt_secret", "auth.nip98_enabled"} {
		if k.Exists(key) {
			return fmt.Errorf("config validation failed: %s has been removed; auth.enabled=true now requires NIP-98-only HTTP auth", key)
		}
	}
	return nil
}

func rejectRemovedEncryptedRequestKeys(k *koanf.Koanf) error {
	removed := []struct {
		key      string
		guidance string
	}{
		{"nostr.private_relays", "use nostr.relays"},
		{"nostr.private_browser_relays", "use nostr.browser_relays"},
		{"nostr.encrypted_request_relays", "encrypted request/result traffic now uses the standard Bahia relay set; use nostr.relays"},
		{"nostr.browser_encrypted_request_relays", "encrypted browser traffic now uses the standard Bahia browser relay set; use nostr.browser_relays"},
		{"features.private_nostr_transport", "use the encrypted_nostr_requests discovery feature; configure nostr.private_key and Bahia browser relay discovery to enable it"},
	}
	for _, item := range removed {
		if k.Exists(item.key) {
			return fmt.Errorf("config validation failed: %s has been removed; %s", item.key, item.guidance)
		}
	}
	return nil
}

func (c *Config) validate() error {
	if err := c.validateSupervision(); err != nil {
		return err
	}
	mode := strings.ToLower(strings.TrimSpace(c.Mode))
	switch mode {
	case "full", "degraded", "emergency":
		c.Mode = mode
	default:
		return fmt.Errorf("config validation failed: mode must be one of full, degraded, emergency")
	}
	if err := c.validateProductionSecurity(); err != nil {
		return err
	}

	if c.Adoption.Enabled {
		if !c.Auth.Enabled {
			return fmt.Errorf("config validation failed: auth.enabled=true is required when adoption.enabled=true")
		}
		if c.Adoption.OperatorAccessConfig.Empty() {
			return fmt.Errorf("config validation failed: adoption operator allowlist is required when adoption.enabled=true")
		}
		if strings.TrimSpace(c.Nostr.PrivateKey) == "" {
			return fmt.Errorf("config validation failed: nostr.private_key is required when adoption.enabled=true because adopted workload secret import requires encryption")
		}
	}
	for name, endpoint := range c.Runtime.Endpoints {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("config validation failed: runtime endpoint names must not be empty")
		}
		if strings.TrimSpace(endpoint.DockerHost) == "" {
			return fmt.Errorf("config validation failed: runtime endpoint %q requires docker_host", name)
		}
		if (strings.TrimSpace(endpoint.ClientCertFile) == "") != (strings.TrimSpace(endpoint.ClientKeyFile) == "") {
			return fmt.Errorf("config validation failed: runtime endpoint %q requires both client_cert_file and client_key_file", name)
		}
	}
	if err := c.validateRuntimeEndpointRefs(); err != nil {
		return err
	}
	if c.DirectRuntime.Enabled {
		if !c.Auth.Enabled {
			return fmt.Errorf("config validation failed: auth.enabled=true is required when direct_runtime_actions.enabled=true")
		}
		if c.DirectRuntime.OperatorAccessConfig.Empty() {
			return fmt.Errorf("config validation failed: direct_runtime_actions operator allowlist is required when direct_runtime_actions.enabled=true")
		}
		if strings.TrimSpace(c.Nostr.PrivateKey) == "" {
			return fmt.Errorf("config validation failed: nostr.private_key is required when direct_runtime_actions.enabled=true because runtime secret handling requires encryption")
		}
	}
	if c.OCI.Enabled {
		if strings.TrimSpace(c.OCI.PublicHost) == "" {
			return fmt.Errorf("config validation failed: oci.public_host is required when oci.enabled=true")
		}
		if strings.TrimSpace(c.OCI.SpoolDir) == "" {
			return fmt.Errorf("config validation failed: oci.spool_dir is required when oci.enabled=true")
		}
		if c.OCI.UploadExpiry <= 0 {
			return fmt.Errorf("config validation failed: oci.upload_expiry must be > 0 when oci.enabled=true")
		}
	}
	if c.HiveCI.Enabled && len(c.HiveCI.TrustedCIPubkeys) == 0 {
		return fmt.Errorf("config validation failed: hiveci.trusted_ci_pubkeys is required when hiveci.enabled=true")
	}
	if c.HiveCI.RetryInterval <= 0 {
		return fmt.Errorf("config validation failed: hiveci.retry_interval must be > 0")
	}
	if c.HiveCI.MaxRetries <= 0 {
		return fmt.Errorf("config validation failed: hiveci.max_retries must be > 0")
	}
	if c.HiveCI.Initiator.Enabled {
		if strings.TrimSpace(c.HiveCI.Initiator.GiteaBaseURL) == "" {
			return fmt.Errorf("config validation failed: hiveci.initiator.gitea_base_url is required when hiveci.initiator.enabled=true")
		}
		if strings.TrimSpace(c.HiveCI.Initiator.GiteaToken) == "" {
			return fmt.Errorf("config validation failed: hiveci.initiator.gitea_token is required when hiveci.initiator.enabled=true")
		}
		if strings.TrimSpace(c.HiveCI.Initiator.MirrorOwner) == "" {
			return fmt.Errorf("config validation failed: hiveci.initiator.mirror_owner is required when hiveci.initiator.enabled=true")
		}
		if strings.TrimSpace(c.HiveCI.Initiator.WorkflowPath) == "" {
			return fmt.Errorf("config validation failed: hiveci.initiator.workflow_path is required when hiveci.initiator.enabled=true")
		}
	}
	if c.Cashu.Enabled {
		return fmt.Errorf("config validation failed: cashu.enabled=true is unsupported because mint-backed token flows are not implemented; disable cashu.enabled")
	}
	if err := c.validateLoom(); err != nil {
		return err
	}
	if err := c.validateQdrant(); err != nil {
		return err
	}
	if err := c.validateLLM(); err != nil {
		return err
	}
	if err := c.validateAssistant(); err != nil {
		return err
	}
	if err := c.validatePackages(); err != nil {
		return err
	}
	if err := c.validateDNS(); err != nil {
		return err
	}
	if err := c.validateEdgeRouting(); err != nil {
		return err
	}
	if err := c.validateFIPS(); err != nil {
		return err
	}
	if err := c.validateSoulFactory(); err != nil {
		return err
	}
	if err := c.validateWorkerPressure(); err != nil {
		return err
	}
	if err := c.validateRelaySidecar(); err != nil {
		return err
	}
	if err := c.validateRelayAdministration(); err != nil {
		return err
	}
	c.normalizeNostrRelays()
	if err := c.validateDMRelayLists(); err != nil {
		return err
	}
	if err := c.validateNostrRelayPolicy(); err != nil {
		return err
	}

	nostrAuthorized, err := normalizePubkeyList(c.Nostr.AuthorizedPubkeys)
	if err != nil {
		return fmt.Errorf("config validation failed: nostr.authorized_pubkeys: %w", err)
	}
	c.Nostr.AuthorizedPubkeys = nostrAuthorized

	trustedRelayMonitors, err := normalizePubkeyList(c.Nostr.TrustedRelayMonitorPubkeys)
	if err != nil {
		return fmt.Errorf("config validation failed: nostr.trusted_relay_monitor_pubkeys: %w", err)
	}
	c.Nostr.TrustedRelayMonitorPubkeys = trustedRelayMonitors

	bootstrapOwners, err := normalizePubkeyList(c.Auth.BootstrapOwnerPubkeys)
	if err != nil {
		return fmt.Errorf("config validation failed: auth.bootstrap_owner_pubkeys: %w", err)
	}
	c.Auth.BootstrapOwnerPubkeys = bootstrapOwners

	return nil
}

const bundledOCIServiceAccountHash = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"

func (c *Config) validateSupervision() error {
	if !c.Supervision.Enabled {
		return nil
	}
	if c.Supervision.Interval <= 0 {
		return fmt.Errorf("config validation failed: supervision.interval must be > 0")
	}
	if c.Supervision.ObservationTimeout <= 0 {
		return fmt.Errorf("config validation failed: supervision.observation_timeout must be > 0")
	}
	if c.Supervision.MemoryThreshold <= 0 || c.Supervision.MemoryThreshold > 1 {
		return fmt.Errorf("config validation failed: supervision.memory_threshold must be > 0 and <= 1")
	}
	for i := range c.Supervision.Instances {
		spec := &c.Supervision.Instances[i]
		if _, err := uuid.Parse(strings.TrimSpace(spec.ServiceID)); err != nil {
			return fmt.Errorf("config validation failed: supervision.instances[%d].service_id must be a UUID", i)
		}
		if _, err := uuid.Parse(strings.TrimSpace(spec.EnvironmentID)); err != nil {
			return fmt.Errorf("config validation failed: supervision.instances[%d].environment_id must be a UUID", i)
		}
		if _, err := uuid.Parse(strings.TrimSpace(spec.DeploymentUnitID)); err != nil {
			return fmt.Errorf("config validation failed: supervision.instances[%d].deployment_unit_id must be a UUID", i)
		}
		if strings.TrimSpace(spec.RuntimeTargetName) == "" {
			return fmt.Errorf("config validation failed: supervision.instances[%d].runtime_target_name is required", i)
		}
		switch domain.InstanceSupervisorType(strings.TrimSpace(spec.SupervisorType)) {
		case domain.InstanceSupervisorDocker, domain.InstanceSupervisorCompose, domain.InstanceSupervisorSystemd, domain.InstanceSupervisorUserSystemd:
		default:
			return fmt.Errorf("config validation failed: supervision.instances[%d].supervisor_type is invalid", i)
		}
		if spec.RestartMaxAttempts <= 0 || spec.RestartMaxAttempts > 500 || spec.RestartWindow <= 0 || spec.BackoffBase <= 0 || spec.BackoffCap < spec.BackoffBase {
			return fmt.Errorf("config validation failed: supervision.instances[%d] recovery budget/backoff is invalid", i)
		}
		if spec.ProbeURL != "" && spec.ProbeTimeout <= 0 {
			return fmt.Errorf("config validation failed: supervision.instances[%d].probe_timeout must be > 0", i)
		}
	}
	return nil
}

func (c *Config) validateProductionSecurity() error {
	c.Server.Host = strings.TrimSpace(c.Server.Host)
	c.DB.SSLMode = strings.ToLower(strings.TrimSpace(c.DB.SSLMode))
	for i, account := range c.OCI.ServiceAccounts {
		if strings.TrimSpace(account.PasswordHash) == bundledOCIServiceAccountHash {
			return fmt.Errorf("config validation failed: oci.service_accounts[%d] uses the bundled password hash; configure an operator-generated credential", i)
		}
	}
	if c.DevMode {
		return nil
	}
	if isWildcardHost(c.Server.Host) {
		return fmt.Errorf("config validation failed: server.host must not be a wildcard outside dev_mode; configure an explicit interface address")
	}
	if !isLoopbackHost(c.Server.Host) && !c.Auth.Enabled {
		return fmt.Errorf("config validation failed: auth.enabled=true is required for a non-loopback server.host outside dev_mode")
	}
	if strings.TrimSpace(c.DB.Password) == "bahia" {
		return fmt.Errorf("config validation failed: db.password must not use the bundled default outside dev_mode")
	}
	switch c.DB.SSLMode {
	case "require", "verify-ca", "verify-full":
	default:
		return fmt.Errorf("config validation failed: db.sslmode must require TLS outside dev_mode (require, verify-ca, or verify-full)")
	}
	return nil
}

func (c *Config) validateLoom() error {
	projection := &c.Loom.CanonicalProjection
	projection.SignetBunkerURI = strings.TrimSpace(projection.SignetBunkerURI)
	projection.SignetClientSecretKey = strings.TrimSpace(projection.SignetClientSecretKey)
	projection.RawPrivateKey = strings.TrimSpace(projection.RawPrivateKey)
	if projection.SignetConnectTimeout == 0 {
		projection.SignetConnectTimeout = 15 * time.Second
	}

	if projection.AllowRawKeyDev {
		return fmt.Errorf("config validation failed: loom.canonical_projection.allow_raw_key_dev is unavailable in validated runtime configuration; use Signet/NIP-46 projection signing")
	}
	if projection.RawPrivateKey != "" {
		return fmt.Errorf("config validation failed: loom.canonical_projection.raw_private_key is unavailable in validated runtime configuration; use Signet/NIP-46 projection signing")
	}
	if !projection.Enabled {
		return nil
	}
	if projection.SignetBunkerURI == "" && !c.DevMode {
		return fmt.Errorf("config validation failed: loom.canonical_projection.signet_bunker_uri is required when loom.canonical_projection.enabled=true outside dev_mode")
	}
	if projection.SignetConnectTimeout <= 0 {
		return fmt.Errorf("config validation failed: loom.canonical_projection.signet_connect_timeout must be > 0 when enabled")
	}
	return nil
}

func (c *Config) validateQdrant() error {
	c.Qdrant.URL = strings.TrimRight(strings.TrimSpace(c.Qdrant.URL), "/")
	c.Qdrant.APIKey = strings.TrimSpace(c.Qdrant.APIKey)
	c.Qdrant.AuthHeaderName = strings.TrimSpace(c.Qdrant.AuthHeaderName)
	if c.Qdrant.URL == "" {
		return nil
	}
	parsed, err := url.Parse(c.Qdrant.URL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("config validation failed: qdrant.url must be a valid URL")
	}
	if c.Qdrant.Timeout < 0 {
		return fmt.Errorf("config validation failed: qdrant.timeout must not be negative")
	}
	if c.Qdrant.APIKey == "" && !c.Qdrant.AllowUnauthenticatedLocal {
		return fmt.Errorf("config validation failed: qdrant.api_key is required when qdrant.url is configured unless qdrant.allow_unauthenticated_local=true")
	}
	if c.Qdrant.APIKey == "" && c.Qdrant.AllowUnauthenticatedLocal && !isLocalQdrantURL(parsed) {
		return fmt.Errorf("config validation failed: qdrant.allow_unauthenticated_local=true is only allowed for localhost or loopback qdrant.url")
	}
	return nil
}

func isLocalQdrantURL(parsed *url.URL) bool {
	host := strings.ToLower(parsed.Hostname())
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func (c *Config) normalizeNostrRelays() {
	c.Nostr.Relays = normalizeRelayList(c.Nostr.Relays)
	c.Nostr.ServiceRelays = normalizeRelayList(c.Nostr.ServiceRelays)
	if len(c.Nostr.ServiceRelays) == 0 {
		c.Nostr.ServiceRelays = cloneStrings(c.Nostr.Relays)
	} else {
		c.Nostr.Relays = cloneStrings(c.Nostr.ServiceRelays)
	}
	c.Nostr.BrowserRelays = normalizeRelayList(c.Nostr.BrowserRelays)
	c.Nostr.ContextVMRelays = normalizeRelayList(c.Nostr.ContextVMRelays)
	if pubkeys, err := normalizePubkeyList(c.Nostr.TrustedRelayMonitorPubkeys); err == nil {
		c.Nostr.TrustedRelayMonitorPubkeys = pubkeys
	}
	for i := range c.Nostr.DMRelayLists {
		c.Nostr.DMRelayLists[i].Feature = strings.ToLower(strings.TrimSpace(c.Nostr.DMRelayLists[i].Feature))
		c.Nostr.DMRelayLists[i].Identity = strings.ToLower(strings.TrimSpace(c.Nostr.DMRelayLists[i].Identity))
		c.Nostr.DMRelayLists[i].Relays = normalizeRelayList(c.Nostr.DMRelayLists[i].Relays)
	}
	c.Nostr.PrivateRelays = cloneStrings(c.Nostr.ServiceRelays)
	c.Nostr.PrivateBrowserRelays = cloneStrings(c.Nostr.BrowserRelays)
	c.Nostr.RelayAuthUnavailablePolicy = strings.ToLower(strings.TrimSpace(c.Nostr.RelayAuthUnavailablePolicy))
	if c.Nostr.RelayAuthUnavailablePolicy == "" {
		c.Nostr.RelayAuthUnavailablePolicy = RelayAuthUnavailableExcludeAndFail
	}
}

func (c NostrConfig) ServiceRelayPolicyRelays() []string {
	if len(c.ServiceRelays) > 0 {
		return cloneStrings(c.ServiceRelays)
	}
	return cloneStrings(c.Relays)
}

func (c NostrConfig) BrowserRelayPolicyRelays() []string {
	return cloneStrings(c.BrowserRelays)
}

func (c NostrConfig) ContextVMRelayPolicyRelays() []string {
	if len(c.ContextVMRelays) > 0 {
		return cloneStrings(c.ContextVMRelays)
	}
	return cloneStrings(c.BrowserRelays)
}

func (c NostrConfig) NIP34RelayPolicyRelays() []string {
	return cloneStrings(c.NIP34Relays)
}

func (c NostrConfig) EnabledDMRelayLists() []DMRelayListConfig {
	out := make([]DMRelayListConfig, 0, len(c.DMRelayLists))
	for _, list := range c.DMRelayLists {
		if !list.Enabled {
			continue
		}
		list.Relays = cloneStrings(list.Relays)
		out = append(out, list)
	}
	return out
}

func (c NostrConfig) RelayAuthUnavailableSemantics() string {
	policy := strings.ToLower(strings.TrimSpace(c.RelayAuthUnavailablePolicy))
	if policy == "" {
		return RelayAuthUnavailableExcludeAndFail
	}
	return policy
}

func normalizeRelayList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	normalized := make([]string, 0, len(values))
	for _, raw := range values {
		for _, part := range strings.Split(raw, ",") {
			value := strings.TrimSpace(part)
			if value == "" {
				continue
			}
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			normalized = append(normalized, value)
		}
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

func cloneStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return append([]string(nil), values...)
}

func normalizeStringList(values []string) []string {
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
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

// agentRuntimeIDPattern constrains SoulFactory agent runtime target IDs to
// stable lowercase protocol identifiers suitable for Nostr tag values.
var agentRuntimeIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// DefaultAgentRuntimes preserves the historical single-runtime behavior when
// soul_factory.agent_runtimes is not configured.
var DefaultAgentRuntimes = []string{"openclaw"}

// normalizeAgentRuntimeList validates the administratively enabled SoulFactory
// agent runtime list. An unset list defaults to DefaultAgentRuntimes; an
// explicitly configured list rejects empty, malformed, and duplicate targets.
func normalizeAgentRuntimeList(values []string) ([]string, error) {
	if len(values) == 0 {
		return append([]string(nil), DefaultAgentRuntimes...), nil
	}
	if len(values) > 16 {
		return nil, fmt.Errorf("at most 16 agent runtimes may be enabled, got %d", len(values))
	}
	seen := make(map[string]struct{}, len(values))
	normalized := make([]string, 0, len(values))
	for i, raw := range values {
		target := strings.ToLower(strings.TrimSpace(raw))
		if target == "" {
			return nil, fmt.Errorf("entry %d must not be empty", i)
		}
		if !agentRuntimeIDPattern.MatchString(target) {
			return nil, fmt.Errorf("entry %d %q is not a valid runtime target id (expected %s)", i, raw, agentRuntimeIDPattern.String())
		}
		if _, ok := seen[target]; ok {
			return nil, fmt.Errorf("entry %d duplicates runtime target %q", i, target)
		}
		seen[target] = struct{}{}
		normalized = append(normalized, target)
	}
	return normalized, nil
}

func normalizePubkeyList(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(values))
	normalized := make([]string, 0, len(values))
	for _, raw := range values {
		pk := strings.ToLower(strings.TrimSpace(raw))
		if pk == "" {
			continue
		}
		if len(pk) != 64 {
			return nil, fmt.Errorf("pubkey %q must be 64 hex characters", raw)
		}
		if _, err := hex.DecodeString(pk); err != nil {
			return nil, fmt.Errorf("pubkey %q must be valid hex", raw)
		}
		if _, ok := seen[pk]; ok {
			continue
		}
		seen[pk] = struct{}{}
		normalized = append(normalized, pk)
	}
	if len(normalized) == 0 {
		return nil, nil
	}
	return normalized, nil
}

func (c *Config) validateLLM() error {
	if c.LLM.AllowOperationalREST {
		if !c.Auth.Enabled {
			return fmt.Errorf("config validation failed: auth.enabled=true is required when llm.allow_operational_rest=true")
		}
		if c.LLM.OperatorAccessConfig.Empty() {
			return fmt.Errorf("config validation failed: llm operator allowlist is required when llm.allow_operational_rest=true")
		}
		if !c.LLM.Enabled {
			return fmt.Errorf("config validation failed: llm.enabled=true is required when llm.allow_operational_rest=true")
		}
	}
	if !c.LLM.Enabled {
		return nil
	}
	if c.LLM.RecoveryPollInterval <= 0 {
		return fmt.Errorf("config validation failed: llm.recovery_poll_interval must be > 0 when llm.enabled=true")
	}
	if c.LLM.StaleRunTimeout <= 0 {
		return fmt.Errorf("config validation failed: llm.stale_run_timeout must be > 0 when llm.enabled=true")
	}
	if c.LLM.ReconcileInterval <= 0 {
		return fmt.Errorf("config validation failed: llm.reconcile_interval must be > 0 when llm.enabled=true")
	}
	for name, gateway := range c.LLM.Gateways {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("config validation failed: llm gateway names must not be empty")
		}
		if typ := strings.TrimSpace(gateway.Type); typ != "" && typ != "http" {
			return fmt.Errorf("config validation failed: llm.gateways.%s.type %q is unsupported", name, gateway.Type)
		}
		if strings.TrimSpace(gateway.BaseURL) == "" {
			return fmt.Errorf("config validation failed: llm.gateways.%s.base_url is required", name)
		}
		if strings.TrimSpace(gateway.AuthToken) != "" && strings.TrimSpace(gateway.AuthTokenFile) != "" {
			return fmt.Errorf("config validation failed: llm.gateways.%s must set only one of auth_token or auth_token_file", name)
		}
		if file := strings.TrimSpace(gateway.AuthTokenFile); file != "" && !filepath.IsAbs(file) {
			return fmt.Errorf("config validation failed: llm.gateways.%s.auth_token_file must be an absolute path", name)
		}
		parsed, err := url.Parse(gateway.BaseURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("config validation failed: llm.gateways.%s.base_url must be a valid URL", name)
		}
	}
	if ref := strings.TrimSpace(c.LLM.DefaultGatewayRef); ref != "" {
		if _, ok := c.LLM.Gateways[ref]; !ok {
			return fmt.Errorf("config validation failed: llm.default_gateway_ref %q is not configured", ref)
		}
	}
	return nil
}

func (c *Config) validateAssistant() error {
	assistant := &c.Assistant
	assistant.LLMBaseURL = strings.TrimRight(strings.TrimSpace(assistant.LLMBaseURL), "/")
	assistant.LLMModel = strings.TrimSpace(assistant.LLMModel)
	assistant.LLMAPIKey = strings.TrimSpace(assistant.LLMAPIKey)
	assistant.SignetBunkerURI = strings.TrimSpace(assistant.SignetBunkerURI)
	if assistant.SignetConnectTimeout == 0 {
		assistant.SignetConnectTimeout = 15 * time.Second
	}
	if assistant.SignetConnectTimeout < 0 {
		return fmt.Errorf("config validation failed: assistant.signet_connect_timeout must not be negative")
	}
	if assistant.LLMBaseURL == "" {
		assistant.LLMBaseURL = "https://api.openai.com"
	}

	agentic := &assistant.Agentic
	agentic.Provider = normalizeAssistantProvider(agentic.Provider)
	if agentic.Provider == "" {
		agentic.Provider = "openai_compatible"
	}
	agentic.ToolMode = strings.ToLower(strings.TrimSpace(agentic.ToolMode))
	if agentic.ToolMode == "" {
		agentic.ToolMode = AssistantAgenticToolModeNative
	}
	switch agentic.ToolMode {
	case AssistantAgenticToolModeNative, AssistantAgenticToolModePrompted:
	default:
		return fmt.Errorf("config validation failed: assistant.agentic.tool_mode must be one of native, prompted")
	}
	if agentic.ToolMode == AssistantAgenticToolModePrompted && agentic.Provider != "openai_compatible" {
		return fmt.Errorf("config validation failed: assistant.agentic.tool_mode=prompted requires assistant.agentic.provider=openai_compatible")
	}
	agentic.BaseURL = strings.TrimRight(strings.TrimSpace(agentic.BaseURL), "/")
	agenticBaseURLWasUnsetOrDefault := agentic.BaseURL == "" || agentic.BaseURL == "https://api.openai.com"
	if agenticBaseURLWasUnsetOrDefault {
		agentic.BaseURL = assistant.LLMBaseURL
	}
	if agentic.Provider == "anthropic" && agenticBaseURLWasUnsetOrDefault && assistant.LLMBaseURL == "https://api.openai.com" {
		agentic.BaseURL = "https://api.anthropic.com"
	}
	agentic.Model = strings.TrimSpace(agentic.Model)
	if agentic.Model == "" {
		agentic.Model = assistant.LLMModel
	}
	agentic.APIKey = strings.TrimSpace(agentic.APIKey)
	if agentic.APIKey == "" {
		agentic.APIKey = assistant.LLMAPIKey
	}
	if agentic.MaxIterations == 0 {
		agentic.MaxIterations = 12
	}
	if agentic.MaxConsecutiveToolFailures == 0 {
		agentic.MaxConsecutiveToolFailures = 3
	}
	if agentic.RequestTimeout == 0 {
		agentic.RequestTimeout = 110 * time.Second
	}

	permissions := &assistant.Permissions
	permissions.Mode = domain.AssistantPermissionMode(strings.ToLower(strings.TrimSpace(string(permissions.Mode))))
	if permissions.Mode == "" {
		permissions.Mode = domain.AssistantPermissionModeAudited
	}
	switch permissions.Mode {
	case domain.AssistantPermissionModeReview, domain.AssistantPermissionModeAudited, domain.AssistantPermissionModeReadonly, domain.AssistantPermissionModeEmergency:
	default:
		return fmt.Errorf("config validation failed: assistant.permissions.mode must be one of review, audited, readonly, emergency")
	}

	for _, block := range []struct {
		name string
		cfg  *AssistantExtensionSourceConfig
	}{
		{"subagents", &assistant.Subagents},
		{"skills", &assistant.Skills},
		{"commands", &assistant.Commands},
		{"hooks", &assistant.Hooks},
	} {
		if err := validateAssistantExtensionPaths(block.name, block.cfg); err != nil {
			return err
		}
	}

	asyncObservation := &assistant.MCP.AsyncObservation
	if asyncObservation.MaxWait == 0 {
		asyncObservation.MaxWait = 30 * time.Minute
	}
	if asyncObservation.BackfillLimit == 0 {
		asyncObservation.BackfillLimit = 50
	}
	if asyncObservation.MaxWait < 0 {
		return fmt.Errorf("config validation failed: assistant.mcp.async_observation.max_wait must not be negative")
	}
	if asyncObservation.BackfillLimit < 0 {
		return fmt.Errorf("config validation failed: assistant.mcp.async_observation.backfill_limit must not be negative")
	}
	if err := validateAssistantExternalMCPServers(assistant.MCP.ExternalServers); err != nil {
		return err
	}

	if !assistant.Enabled {
		return nil
	}
	if assistant.LLMModel == "" && !agentic.Enabled {
		return fmt.Errorf("config validation failed: assistant.llm_model is required when assistant.enabled=true and assistant.agentic.enabled=false")
	}
	parsed, err := url.Parse(assistant.LLMBaseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("config validation failed: assistant.llm_base_url must be a valid URL")
	}
	if strings.TrimSpace(c.Nostr.PrivateKey) == "" {
		return fmt.Errorf("config validation failed: nostr.private_key is required when assistant.enabled=true")
	}
	if !agentic.Enabled {
		return nil
	}
	if agentic.Provider != "openai_compatible" && agentic.Provider != "anthropic" {
		return fmt.Errorf("config validation failed: assistant.agentic.provider must be one of openai_compatible, anthropic")
	}
	if agentic.Model == "" {
		return fmt.Errorf("config validation failed: assistant.agentic.model or assistant.llm_model is required when assistant.enabled=true and assistant.agentic.enabled=true")
	}
	parsedAgentic, err := url.Parse(agentic.BaseURL)
	if err != nil || parsedAgentic.Scheme == "" || parsedAgentic.Host == "" {
		return fmt.Errorf("config validation failed: assistant.agentic.base_url must be a valid URL")
	}
	if agentic.MaxIterations <= 0 {
		return fmt.Errorf("config validation failed: assistant.agentic.max_iterations must be > 0 when assistant.agentic.enabled=true")
	}
	if agentic.MaxConsecutiveToolFailures <= 0 {
		return fmt.Errorf("config validation failed: assistant.agentic.max_consecutive_tool_failures must be > 0 when assistant.agentic.enabled=true")
	}
	if agentic.RequestTimeout <= 0 {
		return fmt.Errorf("config validation failed: assistant.agentic.request_timeout must be > 0 when assistant.agentic.enabled=true")
	}
	return nil
}

func validateAssistantExternalMCPServers(servers []AssistantExternalMCPServerConfig) error {
	seenNames := map[string]struct{}{}
	seenPrefixes := map[string]struct{}{}
	for i := range servers {
		server := &servers[i]
		field := fmt.Sprintf("assistant.mcp.external_servers[%d]", i)
		server.Name = strings.TrimSpace(server.Name)
		server.URL = strings.TrimRight(strings.TrimSpace(server.URL), "/")
		server.ToolPrefix = strings.TrimSpace(server.ToolPrefix)
		server.ResourceTypes = normalizeStringList(server.ResourceTypes)
		if server.AuthHeaders == nil {
			server.AuthHeaders = map[string]string{}
		} else {
			headers := make(map[string]string, len(server.AuthHeaders))
			for key, value := range server.AuthHeaders {
				key = strings.TrimSpace(key)
				if key == "" {
					return fmt.Errorf("config validation failed: %s.auth_headers contains an empty header name", field)
				}
				headers[key] = strings.TrimSpace(value)
			}
			server.AuthHeaders = headers
		}
		server.DefaultEffect = domain.AssistantToolEffect(strings.ToLower(strings.TrimSpace(string(server.DefaultEffect))))
		if server.DefaultEffect == "" {
			server.DefaultEffect = domain.AssistantToolEffectMutation
		}
		if server.DefaultEffect != domain.AssistantToolEffectRead && server.DefaultEffect != domain.AssistantToolEffectMutation {
			return fmt.Errorf("config validation failed: %s.default_effect must be one of read, mutation", field)
		}
		server.DefaultRisk = domain.AssistantToolRisk(strings.ToLower(strings.TrimSpace(string(server.DefaultRisk))))
		if server.DefaultRisk == "" {
			server.DefaultRisk = domain.AssistantToolRiskHigh
		}
		switch server.DefaultRisk {
		case domain.AssistantToolRiskLow, domain.AssistantToolRiskMedium, domain.AssistantToolRiskHigh, domain.AssistantToolRiskDestructive:
		default:
			return fmt.Errorf("config validation failed: %s.default_risk must be one of low, medium, high, destructive", field)
		}
		if !server.Enabled {
			continue
		}
		if server.Name == "" {
			return fmt.Errorf("config validation failed: %s.name is required when enabled=true", field)
		}
		if _, ok := seenNames[server.Name]; ok {
			return fmt.Errorf("config validation failed: %s.name %q is duplicated", field, server.Name)
		}
		seenNames[server.Name] = struct{}{}
		if server.URL == "" {
			return fmt.Errorf("config validation failed: %s.url is required when enabled=true", field)
		}
		parsed, err := url.Parse(server.URL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return fmt.Errorf("config validation failed: %s.url must be a valid http(s) URL", field)
		}
		if server.ToolPrefix == "" {
			return fmt.Errorf("config validation failed: %s.tool_prefix is required when enabled=true", field)
		}
		if !isAssistantToolNameToken(server.ToolPrefix) {
			return fmt.Errorf("config validation failed: %s.tool_prefix may only contain letters, numbers, underscores, or dashes", field)
		}
		if _, ok := seenPrefixes[server.ToolPrefix]; ok {
			return fmt.Errorf("config validation failed: %s.tool_prefix %q is duplicated", field, server.ToolPrefix)
		}
		seenPrefixes[server.ToolPrefix] = struct{}{}
		if server.Timeout == 0 {
			server.Timeout = 30 * time.Second
		}
		if server.Timeout < 0 {
			return fmt.Errorf("config validation failed: %s.timeout must not be negative", field)
		}
		if len(server.Permissions) == 0 {
			return fmt.Errorf("config validation failed: %s.permissions must contain at least one explicit rule when enabled=true", field)
		}
		for j := range server.Permissions {
			perm := &server.Permissions[j]
			perm.ID = strings.TrimSpace(perm.ID)
			perm.Decision = domain.AssistantPermissionDecision(strings.ToLower(strings.TrimSpace(string(perm.Decision))))
			switch perm.Decision {
			case domain.AssistantPermissionDecisionAllow, domain.AssistantPermissionDecisionAsk, domain.AssistantPermissionDecisionDeny:
			default:
				return fmt.Errorf("config validation failed: %s.permissions[%d].decision must be one of allow, ask, deny", field, j)
			}
			perm.ToolNames = normalizeStringList(perm.ToolNames)
			perm.ToolPrefixes = normalizeStringList(perm.ToolPrefixes)
			for k := range perm.Effects {
				perm.Effects[k] = domain.AssistantToolEffect(strings.ToLower(strings.TrimSpace(string(perm.Effects[k]))))
				switch perm.Effects[k] {
				case domain.AssistantToolEffectRead, domain.AssistantToolEffectMutation:
				default:
					return fmt.Errorf("config validation failed: %s.permissions[%d].effects contains unsupported effect %q", field, j, perm.Effects[k])
				}
			}
			for k := range perm.Risks {
				perm.Risks[k] = domain.AssistantToolRisk(strings.ToLower(strings.TrimSpace(string(perm.Risks[k]))))
				switch perm.Risks[k] {
				case domain.AssistantToolRiskLow, domain.AssistantToolRiskMedium, domain.AssistantToolRiskHigh, domain.AssistantToolRiskDestructive:
				default:
					return fmt.Errorf("config validation failed: %s.permissions[%d].risks contains unsupported risk %q", field, j, perm.Risks[k])
				}
			}
			for k := range perm.ExecutionModes {
				perm.ExecutionModes[k] = domain.AssistantToolExecutionMode(strings.ToLower(strings.TrimSpace(string(perm.ExecutionModes[k]))))
				switch perm.ExecutionModes[k] {
				case domain.AssistantToolExecutionModeSync, domain.AssistantToolExecutionModeAsync:
				default:
					return fmt.Errorf("config validation failed: %s.permissions[%d].execution_modes contains unsupported mode %q", field, j, perm.ExecutionModes[k])
				}
			}
			perm.ResourceTypes = normalizeStringList(perm.ResourceTypes)
			perm.Reason = strings.TrimSpace(perm.Reason)
		}
	}
	return nil
}

func isAssistantToolNameToken(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

// validateAssistantExtensionPaths normalizes and containment-checks one
// extensibility source block. Entries with parent traversal are rejected, and a
// path list is required when the block is enabled.
func validateAssistantExtensionPaths(field string, cfg *AssistantExtensionSourceConfig) error {
	cleaned := make([]string, 0, len(cfg.Paths))
	for _, raw := range cfg.Paths {
		path := strings.TrimSpace(raw)
		if path == "" {
			continue
		}
		for _, segment := range strings.Split(filepath.ToSlash(path), "/") {
			if segment == ".." {
				return fmt.Errorf("config validation failed: assistant.%s.paths entry %q must not contain parent traversal", field, raw)
			}
		}
		cleaned = append(cleaned, filepath.Clean(path))
	}
	cfg.Paths = cleaned
	if cfg.Enabled && len(cleaned) == 0 {
		return fmt.Errorf("config validation failed: assistant.%s.paths is required when assistant.%s.enabled=true", field, field)
	}
	return nil
}

func normalizeAssistantProvider(provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	provider = strings.ReplaceAll(provider, "-", "_")
	return provider
}

func (c *Config) validatePackages() error {
	if c.Packages.Backends == nil {
		c.Packages.Backends = map[string]PackageBackendConfig{}
	}
	if len(c.Packages.AllowedSourceHosts) > 0 {
		seenHosts := make(map[string]struct{}, len(c.Packages.AllowedSourceHosts))
		normalizedHosts := make([]string, 0, len(c.Packages.AllowedSourceHosts))
		for _, raw := range c.Packages.AllowedSourceHosts {
			host := strings.ToLower(strings.TrimSpace(raw))
			if host == "" {
				continue
			}
			if strings.Contains(host, "://") {
				return fmt.Errorf("config validation failed: packages.allowed_source_hosts entries must be hostnames, not URLs: %q", raw)
			}
			if _, ok := seenHosts[host]; ok {
				continue
			}
			seenHosts[host] = struct{}{}
			normalizedHosts = append(normalizedHosts, host)
		}
		c.Packages.AllowedSourceHosts = normalizedHosts
	}

	if c.Packages.Enabled && len(c.Packages.Backends) == 0 {
		return fmt.Errorf("config validation failed: packages.backends requires at least one backend when packages.enabled=true")
	}

	for ref, backend := range c.Packages.Backends {
		name := strings.TrimSpace(ref)
		if name == "" {
			return fmt.Errorf("config validation failed: packages backend names must not be empty")
		}
		backendType := strings.TrimSpace(backend.Type)
		switch backendType {
		case "nexus", "pulp", "filesystem_mock":
		default:
			return fmt.Errorf("config validation failed: packages.backends.%s.type %q is unsupported", name, backend.Type)
		}
		if backend.Timeout < 0 {
			return fmt.Errorf("config validation failed: packages.backends.%s.timeout must not be negative", name)
		}
		if backend.Timeout == 0 {
			backend.Timeout = 30 * time.Second
		}
		if strings.TrimSpace(backend.PublicBaseURL) != "" {
			parsed, err := url.Parse(backend.PublicBaseURL)
			if err != nil || parsed.Scheme == "" || parsed.Host == "" {
				return fmt.Errorf("config validation failed: packages.backends.%s.public_base_url must be a valid URL", name)
			}
		}
		for key, value := range backend.SecretRefs {
			if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
				return fmt.Errorf("config validation failed: packages.backends.%s.secret_refs must have non-empty keys and values", name)
			}
		}
		switch backendType {
		case "nexus", "pulp":
			parsed, err := url.Parse(backend.BaseURL)
			if err != nil || parsed.Scheme == "" || parsed.Host == "" {
				return fmt.Errorf("config validation failed: packages.backends.%s.base_url is required and must be a valid URL", name)
			}
			if parsed.Scheme != "https" && parsed.Scheme != "http" {
				return fmt.Errorf("config validation failed: packages.backends.%s.base_url must use http or https", name)
			}
		case "filesystem_mock":
			if strings.TrimSpace(backend.RootDir) == "" {
				return fmt.Errorf("config validation failed: packages.backends.%s.root_dir is required for filesystem_mock", name)
			}
		}
		c.Packages.Backends[ref] = backend
	}
	return nil
}

func (c *Config) validateDNS() error {
	if c.DNS.Backends == nil {
		c.DNS.Backends = map[string]DNSBackendConfig{}
	}
	if c.DNS.Projection.EnvironmentZones == nil {
		c.DNS.Projection.EnvironmentZones = map[string]string{}
	}
	if !c.DNS.Enabled {
		return nil
	}
	if c.DNS.DefaultTTL <= 0 {
		return fmt.Errorf("config validation failed: dns.default_ttl must be > 0 when dns.enabled=true")
	}
	if c.DNS.ReconcileInterval <= 0 {
		return fmt.Errorf("config validation failed: dns.reconcile_interval must be > 0 when dns.enabled=true")
	}
	zoneNames := make(map[string]struct{}, len(c.DNS.Zones))
	for i, zone := range c.DNS.Zones {
		name := strings.TrimSpace(zone.Name)
		if name == "" {
			return fmt.Errorf("config validation failed: dns.zones[%d].name is required when dns.enabled=true", i)
		}
		if _, exists := zoneNames[name]; exists {
			return fmt.Errorf("config validation failed: dns.zones[%d].name %q is duplicated", i, name)
		}
		zoneNames[name] = struct{}{}
		visibility := strings.TrimSpace(zone.Visibility)
		switch visibility {
		case "internal", "external", "edge", "mesh":
		default:
			return fmt.Errorf("config validation failed: dns.zones[%d].visibility %q is unsupported", i, zone.Visibility)
		}
		backend := strings.TrimSpace(zone.Backend)
		if backend == "" {
			return fmt.Errorf("config validation failed: dns.zones[%d].backend is required when dns.enabled=true", i)
		}
		if _, ok := c.DNS.Backends[backend]; !ok {
			return fmt.Errorf("config validation failed: dns.zones[%d].backend %q is not configured", i, backend)
		}
		if zone.TTL < 0 {
			return fmt.Errorf("config validation failed: dns.zones[%d].ttl must not be negative", i)
		}
	}
	for ref, backend := range c.DNS.Backends {
		name := strings.TrimSpace(ref)
		if name == "" {
			return fmt.Errorf("config validation failed: dns backend names must not be empty")
		}
		backend.Type = strings.TrimSpace(backend.Type)
		backend.RootDir = strings.TrimSpace(backend.RootDir)
		backend.EtcdPrefix = strings.TrimSpace(backend.EtcdPrefix)
		backend.PowerDNSAPIURL = strings.TrimRight(strings.TrimSpace(backend.PowerDNSAPIURL), "/")
		backend.PowerDNSAPIKey = strings.TrimSpace(backend.PowerDNSAPIKey)
		backend.PowerDNSServerID = strings.TrimSpace(backend.PowerDNSServerID)
		backend.DnsmasqConfigDir = strings.TrimSpace(backend.DnsmasqConfigDir)
		backend.DnsmasqReloadCommand = strings.TrimSpace(backend.DnsmasqReloadCommand)
		backend.DnsmasqFilePrefix = strings.TrimSpace(backend.DnsmasqFilePrefix)
		backend.HostsPath = strings.TrimSpace(backend.HostsPath)
		switch backend.Type {
		case "filesystem":
			return fmt.Errorf("config validation failed: dns.backends.%s.type %q is not deployable because no operational activator is wired; choose dnsmasq, coredns, powerdns, or fips", name, backend.Type)
		case "coredns":
			normalizedEndpoints := normalizeStringList(backend.EtcdEndpoints)
			if len(normalizedEndpoints) == 0 {
				return fmt.Errorf("config validation failed: dns.backends.%s.etcd_endpoints is required for coredns", name)
			}
			backend.EtcdEndpoints = normalizedEndpoints
			if backend.EtcdPrefix == "" {
				backend.EtcdPrefix = "/skydns"
			}
			if backend.EtcdDialTimeout < 0 {
				return fmt.Errorf("config validation failed: dns.backends.%s.etcd_dial_timeout must not be negative", name)
			}
			if backend.EtcdDialTimeout == 0 {
				backend.EtcdDialTimeout = 5 * time.Second
			}
		case "powerdns":
			if backend.PowerDNSAPIURL == "" {
				return fmt.Errorf("config validation failed: dns.backends.%s.powerdns_api_url is required for powerdns", name)
			}
			parsed, err := url.Parse(backend.PowerDNSAPIURL)
			if err != nil || parsed.Scheme == "" || parsed.Host == "" {
				return fmt.Errorf("config validation failed: dns.backends.%s.powerdns_api_url must be a valid URL", name)
			}
			switch strings.ToLower(parsed.Scheme) {
			case "https":
			case "http":
				if !backend.PowerDNSAllowInsecureHTTP {
					return fmt.Errorf("config validation failed: dns.backends.%s.powerdns_api_url must use HTTPS unless powerdns_allow_insecure_http is explicitly enabled", name)
				}
			default:
				return fmt.Errorf("config validation failed: dns.backends.%s.powerdns_api_url must use HTTP or HTTPS", name)
			}
			if backend.PowerDNSAPIKey == "" {
				return fmt.Errorf("config validation failed: dns.backends.%s.powerdns_api_key is required for powerdns", name)
			}
			if backend.PowerDNSServerID == "" {
				backend.PowerDNSServerID = "localhost"
			}
		case "dnsmasq":
			if backend.DnsmasqConfigDir == "" {
				return fmt.Errorf("config validation failed: dns.backends.%s.dnsmasq_config_dir is required for dnsmasq", name)
			}
			if backend.DnsmasqReloadCommand == "" {
				return fmt.Errorf("config validation failed: dns.backends.%s.dnsmasq_reload_command is required for dnsmasq", name)
			}
			if backend.DnsmasqFilePrefix == "" {
				backend.DnsmasqFilePrefix = "bahia-"
			}
		case "fips":
			if backend.HostsPath == "" {
				backend.HostsPath = "/etc/fips/hosts"
			}
		default:
			return fmt.Errorf("config validation failed: dns.backends.%s.type %q is unsupported", name, backend.Type)
		}
		c.DNS.Backends[ref] = backend
	}
	if c.DNS.Projection.Services || c.DNS.Projection.LLMRoutes || c.DNS.Projection.MLEndpoints {
		if len(c.DNS.Projection.EnvironmentZones) == 0 {
			return fmt.Errorf("config validation failed: dns.projection.environment_zones is required when environment projection sources are enabled")
		}
	}
	for env, zone := range c.DNS.Projection.EnvironmentZones {
		envName := strings.TrimSpace(env)
		zoneName := strings.TrimSpace(zone)
		if envName == "" || zoneName == "" {
			return fmt.Errorf("config validation failed: dns.projection.environment_zones must have non-empty environment and zone names")
		}
		if _, ok := zoneNames[zoneName]; !ok {
			return fmt.Errorf("config validation failed: dns.projection.environment_zones.%s references unknown zone %q", envName, zoneName)
		}
	}
	if c.DNS.Projection.Workers {
		workerZone := strings.TrimSpace(c.DNS.Projection.WorkerZone)
		if workerZone == "" {
			return fmt.Errorf("config validation failed: dns.projection.worker_zone is required when dns.projection.workers=true")
		}
		if _, ok := zoneNames[workerZone]; !ok {
			return fmt.Errorf("config validation failed: dns.projection.worker_zone %q references unknown zone", workerZone)
		}
	}
	if c.DNS.Projection.MeshEndpoints {
		meshZone := strings.TrimSpace(c.DNS.Projection.MeshZone)
		if meshZone == "" {
			return fmt.Errorf("config validation failed: dns.projection.mesh_zone is required when dns.projection.mesh_endpoints=true")
		}
		if _, ok := zoneNames[meshZone]; !ok {
			return fmt.Errorf("config validation failed: dns.projection.mesh_zone %q references unknown zone", meshZone)
		}
	}
	return nil
}

func (c *Config) validateEdgeRouting() error {
	r := &c.EdgeRouting
	if !r.Enabled {
		return nil
	}
	if !c.DirectRuntime.Enabled {
		return fmt.Errorf("config validation failed: direct_runtime_actions.enabled=true is required when edge_routing.enabled=true")
	}
	r.Provider = strings.ToLower(strings.TrimSpace(r.Provider))
	r.BackendRef = strings.TrimSpace(r.BackendRef)
	r.APIBaseURL = strings.TrimRight(strings.TrimSpace(r.APIBaseURL), "/")
	r.APITokenRef = strings.TrimSpace(r.APITokenRef)
	r.AccountID = strings.TrimSpace(r.AccountID)
	r.TunnelID = strings.TrimSpace(r.TunnelID)
	r.VerifyResolver = strings.TrimSpace(r.VerifyResolver)
	if r.VerifyResolver == "" {
		r.VerifyResolver = "1.1.1.1:53"
	} else if strings.EqualFold(r.VerifyResolver, "system") {
		r.VerifyResolver = "system"
	} else {
		host, port, err := net.SplitHostPort(r.VerifyResolver)
		if err != nil || strings.TrimSpace(host) == "" {
			return fmt.Errorf("config validation failed: edge_routing.verify_resolver must be system or host:port with a numeric port")
		}
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			return fmt.Errorf("config validation failed: edge_routing.verify_resolver must be system or host:port with a numeric port")
		}
	}
	if r.Provider != "cloudflare_tunnel" {
		return fmt.Errorf("config validation failed: edge_routing.provider must be cloudflare_tunnel")
	}
	if r.BackendRef == "" || r.APITokenRef == "" || r.AccountID == "" || r.TunnelID == "" {
		return fmt.Errorf("config validation failed: edge_routing backend_ref, api_token_ref, account_id, and tunnel_id are required")
	}
	if _, err := uuid.Parse(r.APITokenRef); err != nil {
		return fmt.Errorf("config validation failed: edge_routing.api_token_ref must be an opaque secret UUID")
	}
	if r.APIBaseURL != "" {
		parsed, err := url.Parse(r.APIBaseURL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && !(c.DevMode && parsed.Scheme == "http")) {
			return fmt.Errorf("config validation failed: edge_routing.api_base_url must use HTTPS (HTTP is allowed only in dev_mode)")
		}
	}
	if r.VerifyTimeout <= 0 {
		r.VerifyTimeout = 30 * time.Second
	}
	if len(r.Zones) == 0 || len(r.Origins) == 0 {
		return fmt.Errorf("config validation failed: edge_routing zones and origins are required")
	}
	seenZones := map[string]struct{}{}
	for i := range r.Zones {
		z := &r.Zones[i]
		var err error
		z.Name, err = domain.NormalizePublicHostname(z.Name)
		if err != nil {
			return fmt.Errorf("config validation failed: edge_routing.zones[%d].name: %w", i, err)
		}
		z.ZoneID = strings.TrimSpace(z.ZoneID)
		if z.ZoneID == "" || len(z.AllowedOrgIDs) == 0 {
			return fmt.Errorf("config validation failed: edge_routing.zones[%d] requires zone_id and allowed_org_ids", i)
		}
		if _, exists := seenZones[z.Name]; exists {
			return fmt.Errorf("config validation failed: duplicate edge routing zone %s", z.Name)
		}
		seenZones[z.Name] = struct{}{}
		for _, raw := range z.AllowedOrgIDs {
			if _, err := uuid.Parse(strings.TrimSpace(raw)); err != nil {
				return fmt.Errorf("config validation failed: edge_routing.zones[%d] has invalid organization ID", i)
			}
		}
		if z.TTL <= 0 {
			z.TTL = 1
		}
	}
	seenUnits := map[string]struct{}{}
	for i := range r.Origins {
		o := &r.Origins[i]
		id, err := uuid.Parse(strings.TrimSpace(o.DeploymentUnitID))
		if err != nil {
			return fmt.Errorf("config validation failed: edge_routing.origins[%d].deployment_unit_id is invalid", i)
		}
		o.DeploymentUnitID = id.String()
		o.Host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(o.Host)), ".")
		if o.Host == "" || len(o.AllowedPorts) == 0 {
			return fmt.Errorf("config validation failed: edge_routing.origins[%d] requires host and allowed_ports", i)
		}
		if net.ParseIP(o.Host) == nil {
			if normalized, err := domain.NormalizePublicHostname(o.Host); err != nil || normalized != o.Host {
				return fmt.Errorf("config validation failed: edge_routing.origins[%d].host must be an IP address or fully qualified DNS name", i)
			}
		}
		if _, exists := seenUnits[o.DeploymentUnitID]; exists {
			return fmt.Errorf("config validation failed: duplicate edge routing origin for deployment unit %s", o.DeploymentUnitID)
		}
		seenUnits[o.DeploymentUnitID] = struct{}{}
		for _, port := range o.AllowedPorts {
			if port < 1 || port > 65535 {
				return fmt.Errorf("config validation failed: edge_routing.origins[%d] has invalid port", i)
			}
		}
	}
	return nil
}

func (c *Config) validateFIPS() error {
	c.FIPS.AppNamespace = strings.TrimSpace(c.FIPS.AppNamespace)
	if c.FIPS.AppNamespace == "" {
		c.FIPS.AppNamespace = "fips-overlay-v1"
	}
	c.FIPS.OverlayAddressPrefix = strings.ToLower(strings.TrimSpace(c.FIPS.OverlayAddressPrefix))
	if c.FIPS.OverlayAddressPrefix == "" {
		c.FIPS.OverlayAddressPrefix = "fd00"
	}
	if c.FIPS.OverlayAddressPrefix != "fd00" {
		return fmt.Errorf("config validation failed: fips.overlay_address_prefix %q is unsupported; expected fd00", c.FIPS.OverlayAddressPrefix)
	}
	c.FIPS.RelayURLs = normalizeRelayList(c.FIPS.RelayURLs)
	c.FIPS.AllowedNpubs = normalizeStringList(c.FIPS.AllowedNpubs)
	if c.FIPS.Enabled && len(c.FIPS.RelayURLs) == 0 {
		return fmt.Errorf("config validation failed: fips.relay_urls requires at least one relay when fips.enabled=true")
	}
	return nil
}

func (c *Config) validateSoulFactory() error {
	sf := &c.SoulFactory
	sf.Relays = normalizeRelayList(sf.Relays)
	sf.AdditionalRelays = normalizeRelayList(sf.AdditionalRelays)
	sf.NIP05Relays = normalizeRelayList(sf.NIP05Relays)
	for i, relay := range sf.NIP05Relays {
		if err := validateWebsocketRelayURL(relay); err != nil {
			return fmt.Errorf("config validation failed: soul_factory.nip05_relays[%d]: %w", i, err)
		}
	}
	seenGroups := make(map[string]struct{}, len(sf.NIP29Groups))
	normalizedGroups := make([]NIP29Group, 0, len(sf.NIP29Groups))
	for i, group := range sf.NIP29Groups {
		group.Relay = strings.TrimRight(strings.TrimSpace(group.Relay), "/")
		group.ID = strings.TrimSpace(group.ID)
		if err := validateWebsocketRelayURL(group.Relay); err != nil {
			return fmt.Errorf("config validation failed: soul_factory.nip29_groups[%d].relay: %w", i, err)
		}
		if group.ID == "" {
			return fmt.Errorf("config validation failed: soul_factory.nip29_groups[%d].id is required", i)
		}
		key := group.Relay + "\x00" + group.ID
		if _, exists := seenGroups[key]; exists {
			continue
		}
		seenGroups[key] = struct{}{}
		normalizedGroups = append(normalizedGroups, group)
	}
	sf.NIP29Groups = normalizedGroups

	communityIndexes := make(map[string]int, len(sf.CommunikeysCommunities))
	communitySections := make(map[string]map[string]struct{}, len(sf.CommunikeysCommunities))
	normalizedCommunities := make([]CommunikeysCommunity, 0, len(sf.CommunikeysCommunities))
	for i, community := range sf.CommunikeysCommunities {
		pubkeys, err := normalizePubkeyList([]string{community.Pubkey})
		if err != nil || len(pubkeys) != 1 {
			if err == nil {
				err = fmt.Errorf("pubkey is required")
			}
			return fmt.Errorf("config validation failed: soul_factory.communikeys_communities[%d].pubkey: %w", i, err)
		}
		sections := normalizeStringList(community.Sections)
		if len(sections) == 0 {
			return fmt.Errorf("config validation failed: soul_factory.communikeys_communities[%d].sections requires at least one section", i)
		}
		pubkey := pubkeys[0]
		index, exists := communityIndexes[pubkey]
		if !exists {
			index = len(normalizedCommunities)
			communityIndexes[pubkey] = index
			communitySections[pubkey] = make(map[string]struct{}, len(sections))
			normalizedCommunities = append(normalizedCommunities, CommunikeysCommunity{Pubkey: pubkey})
		}
		for _, section := range sections {
			if _, duplicate := communitySections[pubkey][section]; duplicate {
				continue
			}
			communitySections[pubkey][section] = struct{}{}
			normalizedCommunities[index].Sections = append(normalizedCommunities[index].Sections, section)
		}
	}
	sf.CommunikeysCommunities = normalizedCommunities

	seenConcord := make(map[string]struct{}, len(sf.ConcordCommunities))
	normalizedConcord := make([]ConcordCommunity, 0, len(sf.ConcordCommunities))
	for i, community := range sf.ConcordCommunities {
		community.CommunityID = strings.ToLower(strings.TrimSpace(community.CommunityID))
		community.InviteBundleEnv = strings.TrimSpace(community.InviteBundleEnv)
		community.InviteBundleFile = strings.TrimSpace(community.InviteBundleFile)
		community.InviteBundleSealedFile = strings.TrimSpace(community.InviteBundleSealedFile)
		decoded, err := hex.DecodeString(community.CommunityID)
		if err != nil || len(decoded) != 32 || len(community.CommunityID) != 64 {
			return fmt.Errorf("config validation failed: soul_factory.concord_communities[%d].community_id must be 64 lowercase hex characters", i)
		}
		sources := 0
		if community.InviteBundleEnv != "" {
			sources++
		}
		if community.InviteBundleFile != "" {
			sources++
			if !filepath.IsAbs(community.InviteBundleFile) {
				return fmt.Errorf("config validation failed: soul_factory.concord_communities[%d].invite_bundle_file must be an absolute secret path", i)
			}
		}
		if community.InviteBundleSealedFile != "" {
			sources++
			if !filepath.IsAbs(community.InviteBundleSealedFile) {
				return fmt.Errorf("config validation failed: soul_factory.concord_communities[%d].invite_bundle_sealed_file must be an absolute secret path", i)
			}
		}
		if sources != 1 {
			return fmt.Errorf("config validation failed: soul_factory.concord_communities[%d] requires exactly one of invite_bundle_env, invite_bundle_file, or invite_bundle_sealed_file", i)
		}
		if _, duplicate := seenConcord[community.CommunityID]; duplicate {
			continue
		}
		seenConcord[community.CommunityID] = struct{}{}
		normalizedConcord = append(normalizedConcord, community)
	}
	sf.ConcordCommunities = normalizedConcord
	sf.SoulFactoryPubkey = strings.ToLower(strings.TrimSpace(sf.SoulFactoryPubkey))
	sf.SignetBunkerURI = strings.TrimSpace(sf.SignetBunkerURI)
	sf.SignetClientSecretKey = strings.TrimSpace(sf.SignetClientSecretKey)
	sf.LLMBaseURL = strings.TrimRight(strings.TrimSpace(sf.LLMBaseURL), "/")
	sf.LLMModel = strings.TrimSpace(sf.LLMModel)
	sf.LLMAPIKey = strings.TrimSpace(sf.LLMAPIKey)
	sf.WorkspaceGiteaURL = strings.TrimRight(strings.TrimSpace(sf.WorkspaceGiteaURL), "/")
	sf.WorkspaceTemplateDir = strings.TrimSpace(sf.WorkspaceTemplateDir)
	sf.WorkspacePrivateKeyRef = strings.TrimSpace(sf.WorkspacePrivateKeyRef)
	sf.WorkspaceAgentMemoryMCPURLRef = strings.TrimSpace(sf.WorkspaceAgentMemoryMCPURLRef)
	sf.AgentMemoryTaskIDFile = strings.TrimSpace(sf.AgentMemoryTaskIDFile)
	sf.OpenClawSignetStateDir = strings.TrimSpace(sf.OpenClawSignetStateDir)
	sf.OpenClawSignetClientKeyDir = strings.TrimSpace(sf.OpenClawSignetClientKeyDir)
	sf.OpenClawSignetContainer = strings.TrimSpace(sf.OpenClawSignetContainer)
	sf.OpenClawSignetConfigPath = strings.TrimSpace(sf.OpenClawSignetConfigPath)
	sf.OpenClawSignetProvisionerFile = strings.TrimSpace(sf.OpenClawSignetProvisionerFile)
	sf.OpenClawSignetProvisionerPubkey = strings.ToLower(strings.TrimSpace(sf.OpenClawSignetProvisionerPubkey))
	if sf.StartupTimeout == 0 {
		sf.StartupTimeout = 15 * time.Second
	}
	if sf.LLMTimeout == 0 {
		sf.LLMTimeout = 120 * time.Second
	}
	if !sf.Enabled {
		return nil
	}
	runtimes, err := normalizeAgentRuntimeList(sf.AgentRuntimes)
	if err != nil {
		return fmt.Errorf("config validation failed: soul_factory.agent_runtimes: %w", err)
	}
	sf.AgentRuntimes = runtimes
	enabledRuntimes := make(map[string]struct{}, len(runtimes))
	for _, runtime := range runtimes {
		enabledRuntimes[runtime] = struct{}{}
	}
	normalizedRuntimePubkeys := make(map[string][]string, len(sf.RuntimePubkeys))
	for rawTarget, rawPubkeys := range sf.RuntimePubkeys {
		target := strings.ToLower(strings.TrimSpace(rawTarget))
		if target == "" || !agentRuntimeIDPattern.MatchString(target) || target != rawTarget {
			return fmt.Errorf("config validation failed: soul_factory.runtime_pubkeys key %q must be a normalized runtime target id", rawTarget)
		}
		if _, enabled := enabledRuntimes[target]; !enabled {
			return fmt.Errorf("config validation failed: soul_factory.runtime_pubkeys target %q is not enabled in agent_runtimes", target)
		}
		pubkeys, err := normalizePubkeyList(rawPubkeys)
		if err != nil || len(pubkeys) == 0 {
			return fmt.Errorf("config validation failed: soul_factory.runtime_pubkeys.%s requires at least one 64-character hex pubkey", target)
		}
		normalizedRuntimePubkeys[target] = pubkeys
	}
	if len(normalizedRuntimePubkeys) == 0 {
		sf.RuntimePubkeys = nil
	} else {
		sf.RuntimePubkeys = normalizedRuntimePubkeys
	}
	if len(sf.Relays) == 0 {
		return fmt.Errorf("config validation failed: soul_factory.relays requires at least one relay when soul_factory.enabled=true")
	}
	if sf.SignetBunkerURI == "" && !c.DevMode {
		return fmt.Errorf("config validation failed: soul_factory.signet_bunker_uri is required when soul_factory.enabled=true outside dev_mode")
	}
	authorized, err := normalizePubkeyList(sf.AuthorizedPubkeys)
	if err != nil {
		return fmt.Errorf("config validation failed: soul_factory.authorized_pubkeys: %w", err)
	}
	if len(authorized) == 0 {
		return fmt.Errorf("config validation failed: soul_factory.authorized_pubkeys requires at least one pubkey when soul_factory.enabled=true")
	}
	sf.AuthorizedPubkeys = authorized
	if sf.SoulFactoryPubkey != "" {
		normalized, err := normalizePubkeyList([]string{sf.SoulFactoryPubkey})
		if err != nil {
			return fmt.Errorf("config validation failed: soul_factory.soul_factory_pubkey: %w", err)
		}
		sf.SoulFactoryPubkey = normalized[0]
	}
	if sf.StartupTimeout <= 0 {
		return fmt.Errorf("config validation failed: soul_factory.startup_timeout must be > 0 when soul_factory.enabled=true")
	}
	if sf.OpenClawSignetEnabled {
		for name, path := range map[string]string{
			"openclaw_signet_state_dir":        sf.OpenClawSignetStateDir,
			"openclaw_signet_client_key_dir":   sf.OpenClawSignetClientKeyDir,
			"openclaw_signet_provisioner_file": sf.OpenClawSignetProvisionerFile,
		} {
			if !filepath.IsAbs(path) {
				return fmt.Errorf("config validation failed: soul_factory.%s must be an absolute path when OpenClaw Signet enrollment is enabled", name)
			}
		}
		if sf.OpenClawSignetContainer == "" || sf.OpenClawSignetConfigPath == "" {
			return fmt.Errorf("config validation failed: soul_factory.openclaw_signet_container and openclaw_signet_config_path are required when OpenClaw Signet enrollment is enabled")
		}
		provisioners, err := normalizePubkeyList([]string{sf.OpenClawSignetProvisionerPubkey})
		if err != nil || len(provisioners) != 1 {
			return fmt.Errorf("config validation failed: soul_factory.openclaw_signet_provisioner_pubkey must be a 64-character hex pubkey")
		}
		sf.OpenClawSignetProvisionerPubkey = provisioners[0]
	}
	if sf.LLMBaseURL == "" {
		return fmt.Errorf("config validation failed: soul_factory.llm_base_url is required when soul_factory.enabled=true")
	}
	parsed, err := url.Parse(sf.LLMBaseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("config validation failed: soul_factory.llm_base_url must be a valid URL")
	}
	if strings.Trim(parsed.Path, "/") != "" {
		return fmt.Errorf("config validation failed: soul_factory.llm_base_url must be an API origin without a path because the SoulFactory generator appends /v1/messages")
	}
	if sf.LLMModel == "" {
		return fmt.Errorf("config validation failed: soul_factory.llm_model is required when soul_factory.enabled=true")
	}
	if sf.LLMAPIKey == "" {
		return fmt.Errorf("config validation failed: soul_factory.llm_api_key is required when soul_factory.enabled=true")
	}
	if sf.LLMTimeout <= 0 {
		return fmt.Errorf("config validation failed: soul_factory.llm_timeout must be > 0 when soul_factory.enabled=true")
	}
	if sf.WorkspaceGiteaURL != "" {
		parsedWorkspace, err := url.Parse(sf.WorkspaceGiteaURL)
		if err != nil || parsedWorkspace.Scheme == "" || parsedWorkspace.Host == "" {
			return fmt.Errorf("config validation failed: soul_factory.workspace_gitea_url must be a valid URL")
		}
		if sf.WorkspacePrivateKeyRef == "" {
			return fmt.Errorf("config validation failed: soul_factory.workspace_private_key_ref is required when soul_factory.workspace_gitea_url is set")
		}
		if sf.WorkspaceAgentMemoryMCPURLRef == "" {
			return fmt.Errorf("config validation failed: soul_factory.workspace_agent_memory_mcp_url_ref is required when soul_factory.workspace_gitea_url is set")
		}
		if sf.WorkspaceGatewayPort < 0 || sf.WorkspaceGatewayPort > 65535 {
			return fmt.Errorf("config validation failed: soul_factory.workspace_gateway_port must be between 0 and 65535")
		}
	}
	return nil
}

func (c *Config) validateWorkerPressure() error {
	p := c.WorkerPressure
	if p.MemoryWarningMinGB <= 0 || p.MemoryCriticalMinGB <= 0 || p.DiskWarningMinGB <= 0 || p.DiskCriticalMinGB <= 0 || p.VRAMWarningMinGB <= 0 || p.VRAMCriticalMinGB <= 0 {
		return fmt.Errorf("config validation failed: worker_pressure minimum GB thresholds must be > 0")
	}
	if p.MemoryWarningMinGB < p.MemoryCriticalMinGB {
		return fmt.Errorf("config validation failed: worker_pressure.memory_warning_min_gb must be >= memory_critical_min_gb")
	}
	if p.DiskWarningMinGB < p.DiskCriticalMinGB {
		return fmt.Errorf("config validation failed: worker_pressure.disk_warning_min_gb must be >= disk_critical_min_gb")
	}
	if p.VRAMWarningMinGB < p.VRAMCriticalMinGB {
		return fmt.Errorf("config validation failed: worker_pressure.vram_warning_min_gb must be >= vram_critical_min_gb")
	}
	if err := validatePressureRatio("memory_warning_min_ratio", p.MemoryWarningRatio); err != nil {
		return err
	}
	if err := validatePressureRatio("memory_critical_min_ratio", p.MemoryCriticalRatio); err != nil {
		return err
	}
	if err := validatePressureRatio("disk_warning_min_ratio", p.DiskWarningRatio); err != nil {
		return err
	}
	if err := validatePressureRatio("disk_critical_min_ratio", p.DiskCriticalRatio); err != nil {
		return err
	}
	if err := validatePressureRatio("vram_warning_min_ratio", p.VRAMWarningRatio); err != nil {
		return err
	}
	if err := validatePressureRatio("vram_critical_min_ratio", p.VRAMCriticalRatio); err != nil {
		return err
	}
	if p.MemoryWarningRatio < p.MemoryCriticalRatio {
		return fmt.Errorf("config validation failed: worker_pressure.memory_warning_min_ratio must be >= memory_critical_min_ratio")
	}
	if p.DiskWarningRatio < p.DiskCriticalRatio {
		return fmt.Errorf("config validation failed: worker_pressure.disk_warning_min_ratio must be >= disk_critical_min_ratio")
	}
	if p.VRAMWarningRatio < p.VRAMCriticalRatio {
		return fmt.Errorf("config validation failed: worker_pressure.vram_warning_min_ratio must be >= vram_critical_min_ratio")
	}
	if p.ThermalWarningC <= 0 || p.ThermalCriticalC <= 0 || p.ThermalWarningC >= p.ThermalCriticalC {
		return fmt.Errorf("config validation failed: worker_pressure thermal thresholds must be > 0 with warning below critical")
	}
	if p.QueueWarningRatio <= 0 || p.QueueCriticalRatio <= 0 || p.QueueWarningRatio >= p.QueueCriticalRatio {
		return fmt.Errorf("config validation failed: worker_pressure queue ratios must be > 0 with warning below critical")
	}
	return nil
}

func validatePressureRatio(name string, value float64) error {
	if value <= 0 || value > 1 {
		return fmt.Errorf("config validation failed: worker_pressure.%s must be > 0 and <= 1", name)
	}
	return nil
}

func (c *Config) validateNostrRelayPolicy() error {
	if c.Nostr.StaleRunAfter <= 0 {
		return fmt.Errorf("config validation failed: nostr.stale_run_after must be > 0")
	}
	switch c.Nostr.RelayAuthUnavailablePolicy {
	case RelayAuthUnavailableExcludeAndFail:
		return nil
	default:
		return fmt.Errorf("config validation failed: nostr.relay_auth_unavailable must be %q", RelayAuthUnavailableExcludeAndFail)
	}
}

func (c *Config) validateDMRelayLists() error {
	seenEnabled := map[string]struct{}{}
	for i := range c.Nostr.DMRelayLists {
		list := &c.Nostr.DMRelayLists[i]
		if !list.Enabled {
			continue
		}
		if list.Feature == "" {
			return fmt.Errorf("config validation failed: nostr.dm_relay_lists[%d].feature is required when enabled", i)
		}
		if list.Feature != DMRelayListFeatureNotifications {
			return fmt.Errorf("config validation failed: nostr.dm_relay_lists[%d].feature %q is not a DM-enabled Bahia feature", i, list.Feature)
		}
		if !c.Notifications.Enabled || !c.Notifications.NostrDM {
			return fmt.Errorf("config validation failed: nostr.dm_relay_lists[%d] requires notifications.enabled=true and notifications.nostr_dm=true", i)
		}
		if list.Identity == "" {
			return fmt.Errorf("config validation failed: nostr.dm_relay_lists[%d].identity is required when enabled", i)
		}
		if list.Identity != DMRelayListIdentityService {
			return fmt.Errorf("config validation failed: nostr.dm_relay_lists[%d].identity %q is not supported; only %q can be signed by Bahia", i, list.Identity, DMRelayListIdentityService)
		}
		if strings.TrimSpace(c.Nostr.PrivateKey) == "" {
			return fmt.Errorf("config validation failed: nostr.private_key is required to publish nostr.dm_relay_lists[%d]", i)
		}
		if len(list.Relays) == 0 {
			return fmt.Errorf("config validation failed: nostr.dm_relay_lists[%d].relays requires at least one DM receive relay", i)
		}
		for _, relay := range list.Relays {
			if err := validateWebsocketRelayURL(relay); err != nil {
				return fmt.Errorf("config validation failed: nostr.dm_relay_lists[%d].relays: %w", i, err)
			}
		}
		key := list.Feature + ":" + list.Identity
		if _, exists := seenEnabled[key]; exists {
			return fmt.Errorf("config validation failed: nostr.dm_relay_lists has duplicate enabled list for feature %q identity %q", list.Feature, list.Identity)
		}
		seenEnabled[key] = struct{}{}
	}
	return nil
}

func (c *Config) validateRelayAdministration() error {
	admin := &c.Nostr.RelayAdministration
	admin.AdministratorPrivateKeyRef = strings.TrimSpace(admin.AdministratorPrivateKeyRef)
	if !admin.Enabled {
		return nil
	}
	if admin.AdministratorPrivateKeyRef == "" {
		return fmt.Errorf("config validation failed: nostr.relay_administration.administrator_private_key_ref is required when relay administration is enabled")
	}
	if looksLikeRawNostrPrivateKey(admin.AdministratorPrivateKeyRef) {
		return fmt.Errorf("config validation failed: nostr.relay_administration.administrator_private_key_ref must be a secret reference, not private key material")
	}
	if len(admin.Targets) == 0 {
		return fmt.Errorf("config validation failed: nostr.relay_administration.targets requires at least one Bahia-owned or Bahia-authorized relay when enabled")
	}
	seen := map[string]struct{}{}
	for i := range admin.Targets {
		target := &admin.Targets[i]
		target.Ref = strings.TrimSpace(target.Ref)
		target.RelayURL = strings.TrimSpace(target.RelayURL)
		target.HTTPURL = strings.TrimSpace(target.HTTPURL)
		target.Authorization = strings.ToLower(strings.TrimSpace(target.Authorization))
		if target.Ref == "" {
			return fmt.Errorf("config validation failed: nostr.relay_administration.targets[%d].ref is required", i)
		}
		if _, exists := seen[target.Ref]; exists {
			return fmt.Errorf("config validation failed: nostr.relay_administration target ref %q is duplicated", target.Ref)
		}
		seen[target.Ref] = struct{}{}
		if err := validateWebsocketRelayURL(target.RelayURL); err != nil {
			return fmt.Errorf("config validation failed: nostr.relay_administration target %q relay_url: %w", target.Ref, err)
		}
		if target.HTTPURL != "" {
			if err := validateHTTPManagementURL(target.HTTPURL); err != nil {
				return fmt.Errorf("config validation failed: nostr.relay_administration target %q http_url: %w", target.Ref, err)
			}
		}
		switch target.Authorization {
		case RelayAdministrationBahiaOwned, RelayAdministrationBahiaAuthorized:
		default:
			return fmt.Errorf("config validation failed: nostr.relay_administration target %q authorization must be %q or %q", target.Ref, RelayAdministrationBahiaOwned, RelayAdministrationBahiaAuthorized)
		}
		pubkeys, err := normalizePubkeyList(target.AdministratorPubkeys)
		if err != nil {
			return fmt.Errorf("config validation failed: nostr.relay_administration target %q administrator_pubkeys: %w", target.Ref, err)
		}
		if len(pubkeys) == 0 {
			return fmt.Errorf("config validation failed: nostr.relay_administration target %q requires administrator_pubkeys", target.Ref)
		}
		target.AdministratorPubkeys = pubkeys
	}
	return nil
}

func looksLikeRawNostrPrivateKey(value string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	if strings.HasPrefix(trimmed, "nsec1") {
		return true
	}
	if len(trimmed) == 64 {
		if _, err := hex.DecodeString(trimmed); err == nil {
			return true
		}
	}
	return false
}

func validateWebsocketRelayURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("must be an absolute ws/wss URL")
	}
	switch parsed.Scheme {
	case "wss":
		return nil
	case "ws":
		if isLoopbackHost(parsed.Hostname()) {
			return nil
		}
		return fmt.Errorf("ws relay administration URLs are allowed only for localhost or loopback targets; use wss")
	default:
		return fmt.Errorf("scheme must be ws or wss")
	}
}

func validateHTTPManagementURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("must be an absolute http/https URL")
	}
	switch parsed.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(parsed.Hostname()) {
			return nil
		}
		return fmt.Errorf("http relay administration URLs are allowed only for localhost or loopback targets; use https")
	default:
		return fmt.Errorf("scheme must be http or https")
	}
}

func isLoopbackHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func isWildcardHost(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsUnspecified()
}

func validateSidecarWebsocketURL(raw string, allowInsecureDev bool) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("must be an absolute ws/wss URL")
	}
	switch parsed.Scheme {
	case "wss":
		return nil
	case "ws":
		if allowInsecureDev || isLoopbackHost(parsed.Hostname()) {
			return nil
		}
		return fmt.Errorf("plaintext ws is allowed only for localhost or loopback targets outside dev_mode; use wss")
	default:
		return fmt.Errorf("scheme must be ws or wss")
	}
}

func (c *Config) validateRelaySidecar() error {
	sidecar := &c.Nostr.Sidecar
	if !sidecar.Enabled {
		return nil
	}
	if strings.TrimSpace(sidecar.ListenAddr) == "" {
		return fmt.Errorf("config validation failed: nostr.sidecar.listen_addr is required when sidecar is enabled")
	}
	if strings.TrimSpace(sidecar.PublicURL) == "" {
		return fmt.Errorf("config validation failed: nostr.sidecar.public_url is required when sidecar is enabled")
	}
	if strings.TrimSpace(sidecar.BackendURL) == "" {
		return fmt.Errorf("config validation failed: nostr.sidecar.backend_url is required when sidecar is enabled")
	}
	if err := validateSidecarWebsocketURL(sidecar.PublicURL, c.DevMode); err != nil {
		return fmt.Errorf("config validation failed: nostr.sidecar.public_url: %w", err)
	}
	if err := validateSidecarWebsocketURL(sidecar.BackendURL, c.DevMode); err != nil {
		return fmt.Errorf("config validation failed: nostr.sidecar.backend_url: %w", err)
	}
	listenHost, _, err := net.SplitHostPort(strings.TrimSpace(sidecar.ListenAddr))
	if err != nil {
		return fmt.Errorf("config validation failed: nostr.sidecar.listen_addr must be host:port: %w", err)
	}
	if !c.DevMode && !isLoopbackHost(listenHost) {
		publicURL, _ := url.Parse(sidecar.PublicURL)
		if publicURL.Scheme != "wss" {
			return fmt.Errorf("config validation failed: nostr.sidecar.public_url must use wss when listen_addr is non-loopback outside dev_mode")
		}
	}
	if strings.TrimSpace(sidecar.DataDir) == "" {
		return fmt.Errorf("config validation failed: nostr.sidecar.data_dir is required when sidecar is enabled")
	}
	if sidecar.EventRetention <= 0 {
		return fmt.Errorf("config validation failed: nostr.sidecar.event_retention must be > 0 when sidecar is enabled")
	}
	if sidecar.RequestRetention <= 0 {
		return fmt.Errorf("config validation failed: nostr.sidecar.request_retention must be > 0 when sidecar is enabled")
	}
	if sidecar.MaxQueryLimit <= 0 {
		return fmt.Errorf("config validation failed: nostr.sidecar.max_query_limit must be > 0 when sidecar is enabled")
	}
	administrators, err := normalizePubkeyList(sidecar.AdministratorPubkeys)
	if err != nil {
		return fmt.Errorf("config validation failed: nostr.sidecar.administrator_pubkeys: %w", err)
	}
	sidecar.AdministratorPubkeys = administrators
	trusted, err := normalizePubkeyList(sidecar.ConfigTrustedPubkeys)
	if err != nil {
		return fmt.Errorf("config validation failed: nostr.sidecar.config_trusted_pubkeys: %w", err)
	}
	if len(trusted) == 0 {
		trusted = append([]string(nil), administrators...)
	}
	sidecar.ConfigTrustedPubkeys = trusted
	if strings.TrimSpace(sidecar.AdminPolicyPath) == "" {
		sidecar.AdminPolicyPath = filepath.Join(sidecar.DataDir, "relay-admin-policy.json")
	}
	if strings.TrimSpace(sidecar.ConfigProjectionPath) == "" {
		sidecar.ConfigProjectionPath = filepath.Join(sidecar.DataDir, "config-fabric-projection.json")
	}
	if !agentRuntimeIDPattern.MatchString(sidecar.ServiceID) {
		return fmt.Errorf("config validation failed: nostr.sidecar.service_id is invalid")
	}
	if sidecar.Scope != "prod" && sidecar.Scope != "staging" && sidecar.Scope != "fleet" && !strings.HasPrefix(sidecar.Scope, "host:") {
		return fmt.Errorf("config validation failed: nostr.sidecar.scope is invalid")
	}
	return nil
}

// PackageBackend returns a configured package backend by reference after trimming whitespace.
func (c *Config) PackageBackend(ref string) (PackageBackendConfig, bool) {
	if c == nil || c.Packages.Backends == nil {
		return PackageBackendConfig{}, false
	}
	backend, ok := c.Packages.Backends[strings.TrimSpace(ref)]
	return backend, ok
}

func (c *Config) validateRuntimeEndpointRefs() error {
	if ref := strings.TrimSpace(c.Runtime.Default.EndpointRef); ref != "" {
		if _, ok := c.Runtime.Endpoints[ref]; !ok {
			return fmt.Errorf("config validation failed: runtime.default.endpoint_ref %q is not configured", ref)
		}
	}
	for name, target := range c.Runtime.Environments {
		ref := strings.TrimSpace(target.EndpointRef)
		if ref == "" {
			continue
		}
		if _, ok := c.Runtime.Endpoints[ref]; !ok {
			return fmt.Errorf("config validation failed: runtime.environments.%s.endpoint_ref %q is not configured", name, ref)
		}
	}
	return nil
}

// Validate applies the same normalization and validation as Load to an in-memory config.
func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("config validation failed: config is required")
	}
	return c.validate()
}

// ServerAddress returns the host:port string for the HTTP server.
func (c *Config) ServerAddress() string {
	return fmt.Sprintf("%s:%d", c.Server.Host, c.Server.Port)
}
