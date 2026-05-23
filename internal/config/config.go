// Package config provides configuration loading and validation for Bahia.
package config

import (
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

// Config is the top-level configuration for Bahia.
type Config struct {
	Server        ServerConfig              `koanf:"server"`
	DB            DBConfig                  `koanf:"db"`
	Harbor        HarborConfig              `koanf:"harbor"`
	Loom          LoomConfig                `koanf:"loom"`
	Nostr         NostrConfig               `koanf:"nostr"`
	Reconcile     ReconcileConfig           `koanf:"reconcile"`
	Runtime       RuntimeConfig             `koanf:"runtime"`
	Log           LogConfig                 `koanf:"log"`
	Auth          AuthConfig                `koanf:"auth"`
	Adoption      AdoptionConfig            `koanf:"adoption"`
	DirectRuntime DirectRuntimeConfig       `koanf:"direct_runtime_actions"`
	CORS          CORSConfig                `koanf:"cors"`
	Blossom       BlossomConfig             `koanf:"blossom"`
	OCI           OCIServerConfig           `koanf:"oci"`
	HiveCI        HiveCIConfig              `koanf:"hiveci"`
	Cashu         CashuConfig               `koanf:"cashu"`
	Qdrant        QdrantConfig              `koanf:"qdrant"`
	Telemetry     TelemetryConfig           `koanf:"telemetry"`
	Notifications NotificationsConfig       `koanf:"notifications"`
	Registry      RegistryAdapterConfig     `koanf:"registry"`
	LLM           LLMControlplaneConfig     `koanf:"llm"`
	Packages      PackageControlplaneConfig `koanf:"packages"`
	Assistant     AssistantConfig           `koanf:"assistant"`
	DNS           DNSConfig                 `koanf:"dns"`
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
	Type                 string        `koanf:"type"`
	RootDir              string        `koanf:"root_dir"`
	EtcdEndpoints        []string      `koanf:"etcd_endpoints"`
	EtcdPrefix           string        `koanf:"etcd_prefix"`
	EtcdDialTimeout      time.Duration `koanf:"etcd_dial_timeout"`
	PowerDNSAPIURL       string        `koanf:"powerdns_api_url" yaml:"powerdns_api_url"`
	PowerDNSAPIKey       string        `koanf:"powerdns_api_key" yaml:"powerdns_api_key"`
	PowerDNSServerID     string        `koanf:"powerdns_server_id" yaml:"powerdns_server_id"`
	DnsmasqConfigDir     string        `koanf:"dnsmasq_config_dir" yaml:"dnsmasq_config_dir"`
	DnsmasqReloadCommand string        `koanf:"dnsmasq_reload_command" yaml:"dnsmasq_reload_command"`
	DnsmasqFilePrefix    string        `koanf:"dnsmasq_file_prefix" yaml:"dnsmasq_file_prefix"`
}

// DNSProjectionConfig selects source state for DNS endpoint projection.
type DNSProjectionConfig struct {
	Services          bool              `koanf:"services"`
	LLMRoutes         bool              `koanf:"llm_routes"`
	MLEndpoints       bool              `koanf:"ml_endpoints"`
	Workers           bool              `koanf:"workers"`
	CapabilityAliases bool              `koanf:"capability_aliases"`
	EnvironmentZones  map[string]string `koanf:"environment_zones"`
	WorkerZone        string            `koanf:"worker_zone"`
}

// AssistantConfig controls the operator assistant backend orchestration path.
type AssistantConfig struct {
	Enabled         bool   `koanf:"enabled"`
	LLMBaseURL      string `koanf:"llm_base_url"`
	LLMModel        string `koanf:"llm_model"`
	LLMAPIKey       string `koanf:"llm_api_key"`
	SignetBunkerURI string `koanf:"signet_bunker_uri"`
	SignetAllowMock bool   `koanf:"signet_allow_mock"`
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
	Enabled                 bool                                `koanf:"enabled"`
	AllowOperationalREST    bool                                `koanf:"allow_operational_rest"`
	DefaultGatewayRef       string                              `koanf:"default_gateway_ref"`
	CoordinatorPollInterval time.Duration                       `koanf:"coordinator_poll_interval"`
	StaleRunTimeout         time.Duration                       `koanf:"stale_run_timeout"`
	ReconcileInterval       time.Duration                       `koanf:"reconcile_interval"`
	Gateways                map[string]LLMGatewayEndpointConfig `koanf:"gateways"`
	OperatorAccessConfig    `koanf:",squash"`
}

// LLMGatewayEndpointConfig describes one inference gateway admin endpoint.
type LLMGatewayEndpointConfig struct {
	Type      string        `koanf:"type"`
	BaseURL   string        `koanf:"base_url"`
	AuthToken string        `koanf:"auth_token"`
	Timeout   time.Duration `koanf:"timeout"`
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
	Relays       []string      `koanf:"relays"`
	JobTimeout   time.Duration `koanf:"job_timeout"`
	PollInterval time.Duration `koanf:"poll_interval"`
}

// NostrConfig holds Nostr relay and identity settings.
type NostrConfig struct {
	PrivateKey string   `koanf:"private_key"`
	Relays     []string `koanf:"relays"`
	// EncryptedRequestRelays are ordinary backend relay URLs used for encrypted
	// request/result Nostr events.
	EncryptedRequestRelays []string `koanf:"encrypted_request_relays"`
	BrowserRelays          []string `koanf:"browser_relays"`
	// BrowserEncryptedRequestRelays are browser-safe relay URLs advertised for
	// encrypted request/result Nostr events.
	BrowserEncryptedRequestRelays []string `koanf:"browser_encrypted_request_relays"`

	// PrivateRelays and PrivateBrowserRelays are internal mirrors for runtime
	// callers that have not moved to the canonical field names yet. They are not
	// loaded from config; nostr.private_relays and nostr.private_browser_relays
	// are rejected in Load.
	PrivateRelays        []string `koanf:"-"`
	PrivateBrowserRelays []string `koanf:"-"`

	AuthorizedPubkeys []string           `koanf:"authorized_pubkeys"`
	PublishEnabled    bool               `koanf:"publish_enabled"`
	Sidecar           RelaySidecarConfig `koanf:"sidecar"`
}

// RelaySidecarConfig holds the local Khatru relay sidecar settings.
type RelaySidecarConfig struct {
	Enabled          bool          `koanf:"enabled"`
	ListenAddr       string        `koanf:"listen_addr"`
	PublicURL        string        `koanf:"public_url"`
	BackendURL       string        `koanf:"backend_url"`
	DataDir          string        `koanf:"data_dir"`
	MirrorExternal   bool          `koanf:"mirror_external"`
	EventRetention   time.Duration `koanf:"event_retention"`
	RequestRetention time.Duration `koanf:"request_retention"`
	AuthPrivateKey   string        `koanf:"auth_private_key"`
	MaxQueryLimit    int           `koanf:"max_query_limit"`
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
	KubeContext   string `koanf:"kube_context"`
	KubeNamespace string `koanf:"kube_namespace"`
	KubeConfig    string `koanf:"kube_config"`

	ResolvedEndpoint RuntimeEndpointConfig `koanf:"-"`
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
	Type          string `koanf:"type"`
	DockerHost    string `koanf:"docker_host"`
	ComposeDir    string `koanf:"compose_dir"`
	KubeContext   string `koanf:"kube_context"`
	KubeNamespace string `koanf:"kube_namespace"`
	KubeConfig    string `koanf:"kube_config"`

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

// HiveCIConfig holds Hive-CI integration settings.
type HiveCIConfig struct {
	Enabled                      bool          `koanf:"enabled"`
	TrustedCIPubkeys             []string      `koanf:"trusted_ci_pubkeys"`
	AutoRegisterBuilds           bool          `koanf:"auto_register_builds"`
	AutoDeployStagingEnvironment string        `koanf:"auto_deploy_staging_environment"`
	RetryInterval                time.Duration `koanf:"retry_interval"`
	MaxRetries                   int           `koanf:"max_retries"`
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

// Defaults returns a Config with sensible default values.
func Defaults() *Config {
	return &Config{
		Server: ServerConfig{
			Host:            "0.0.0.0",
			Port:            8080,
			ReadTimeout:     30 * time.Second,
			WriteTimeout:    30 * time.Second,
			ShutdownTimeout: 15 * time.Second,
		},
		DB: DBConfig{
			Host:            "localhost",
			Port:            5432,
			User:            "bahia",
			Password:        "bahia",
			Name:            "bahia",
			SSLMode:         "disable",
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
			JobTimeout:   30 * time.Minute,
			PollInterval: 10 * time.Second,
		},
		Nostr: NostrConfig{
			PublishEnabled: true,
			Sidecar: RelaySidecarConfig{
				Enabled:          false,
				ListenAddr:       "0.0.0.0:3334",
				PublicURL:        "ws://localhost:3334",
				BackendURL:       "ws://localhost:3334",
				DataDir:          "./data/relay-sidecar",
				MirrorExternal:   false,
				EventRetention:   7 * 24 * time.Hour,
				RequestRetention: 24 * time.Hour,
				MaxQueryLimit:    500,
			},
		},
		Reconcile: ReconcileConfig{
			Interval: 60 * time.Second,
			Enabled:  true,
		},
		Runtime: RuntimeConfig{
			Type:         "docker",
			DockerHost:   "unix:///var/run/docker.sock",
			Environments: map[string]RuntimeTargetConfig{},
			Endpoints:    map[string]RuntimeEndpointConfig{},
		},
		LLM: LLMControlplaneConfig{
			Enabled:                 false,
			AllowOperationalREST:    false,
			CoordinatorPollInterval: 5 * time.Second,
			StaleRunTimeout:         15 * time.Minute,
			ReconcileInterval:       60 * time.Second,
			Gateways:                map[string]LLMGatewayEndpointConfig{},
		},
		Assistant: AssistantConfig{
			Enabled:    false,
			LLMBaseURL: "https://api.openai.com",
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
		OCI: OCIServerConfig{
			Enabled:                 false,
			SpoolDir:                "/tmp/bahia-oci-spool",
			UploadExpiry:            24 * time.Hour,
			AllowAnonymousPullCIDRs: []string{"192.168.40.0/24"},
			ServiceAccounts: []OCIServiceAccountConfig{
				{
					Username:     "hive-ci",
					PasswordHash: "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy",
					Permissions:  []string{"pull", "push"},
					RepoPrefixes: []string{"cascadia/"},
				},
			},
		},
		HiveCI: HiveCIConfig{
			Enabled:            false,
			AutoRegisterBuilds: true,
			RetryInterval:      30 * time.Second,
			MaxRetries:         10,
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
		key := strings.ToLower(strings.TrimPrefix(s, "BAHIA_"))
		// First, honour explicit double-underscore separators.
		key = strings.ReplaceAll(key, "__", ".")
		switch key {
		case "assistant_enabled", "assistant_llm_base_url", "assistant_llm_model", "assistant_llm_api_key":
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
		{"nostr.private_relays", "use nostr.encrypted_request_relays"},
		{"nostr.private_browser_relays", "use nostr.browser_encrypted_request_relays"},
		{"features.private_nostr_transport", "use the encrypted_nostr_requests discovery feature; configure nostr.encrypted_request_relays, nostr.browser_encrypted_request_relays, and nostr.private_key to enable it"},
	}
	for _, item := range removed {
		if k.Exists(item.key) {
			return fmt.Errorf("config validation failed: %s has been removed; %s", item.key, item.guidance)
		}
	}
	return nil
}

func (c *Config) validate() error {
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
	if c.Cashu.Enabled {
		return fmt.Errorf("config validation failed: cashu.enabled=true is unsupported because mint-backed token flows are not implemented; disable cashu.enabled")
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
	if err := c.validateRelaySidecar(); err != nil {
		return err
	}
	c.normalizeEncryptedRequestRelays()

	nostrAuthorized, err := normalizePubkeyList(c.Nostr.AuthorizedPubkeys)
	if err != nil {
		return fmt.Errorf("config validation failed: nostr.authorized_pubkeys: %w", err)
	}
	c.Nostr.AuthorizedPubkeys = nostrAuthorized

	bootstrapOwners, err := normalizePubkeyList(c.Auth.BootstrapOwnerPubkeys)
	if err != nil {
		return fmt.Errorf("config validation failed: auth.bootstrap_owner_pubkeys: %w", err)
	}
	c.Auth.BootstrapOwnerPubkeys = bootstrapOwners

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

func (c *Config) normalizeEncryptedRequestRelays() {
	c.Nostr.EncryptedRequestRelays = normalizeRelayList(c.Nostr.EncryptedRequestRelays)
	c.Nostr.BrowserEncryptedRequestRelays = normalizeRelayList(c.Nostr.BrowserEncryptedRequestRelays)
	c.Nostr.PrivateRelays = cloneStrings(c.Nostr.EncryptedRequestRelays)
	c.Nostr.PrivateBrowserRelays = cloneStrings(c.Nostr.BrowserEncryptedRequestRelays)
}

func normalizeRelayList(values []string) []string {
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
	if c.LLM.CoordinatorPollInterval <= 0 {
		return fmt.Errorf("config validation failed: llm.coordinator_poll_interval must be > 0 when llm.enabled=true")
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
	c.Assistant.LLMBaseURL = strings.TrimRight(strings.TrimSpace(c.Assistant.LLMBaseURL), "/")
	c.Assistant.LLMModel = strings.TrimSpace(c.Assistant.LLMModel)
	c.Assistant.LLMAPIKey = strings.TrimSpace(c.Assistant.LLMAPIKey)
	c.Assistant.SignetBunkerURI = strings.TrimSpace(c.Assistant.SignetBunkerURI)
	if c.Assistant.LLMBaseURL == "" {
		c.Assistant.LLMBaseURL = "https://api.openai.com"
	}
	if !c.Assistant.Enabled {
		return nil
	}
	if c.Assistant.LLMModel == "" {
		return fmt.Errorf("config validation failed: assistant.llm_model is required when assistant.enabled=true")
	}
	parsed, err := url.Parse(c.Assistant.LLMBaseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("config validation failed: assistant.llm_base_url must be a valid URL")
	}
	if strings.TrimSpace(c.Nostr.PrivateKey) == "" {
		return fmt.Errorf("config validation failed: nostr.private_key is required when assistant.enabled=true")
	}
	return nil
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
		case "internal", "external", "edge":
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
		switch backend.Type {
		case "filesystem":
			if backend.RootDir == "" {
				return fmt.Errorf("config validation failed: dns.backends.%s.root_dir is required for filesystem", name)
			}
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
	return nil
}

func (c *Config) validateRelaySidecar() error {
	sidecar := c.Nostr.Sidecar
	if !sidecar.Enabled {
		return nil
	}
	if strings.TrimSpace(sidecar.ListenAddr) == "" {
		return fmt.Errorf("config validation failed: nostr.sidecar.listen_addr is required when sidecar is enabled")
	}
	if strings.TrimSpace(sidecar.PublicURL) == "" {
		return fmt.Errorf("config validation failed: nostr.sidecar.public_url is required when sidecar is enabled")
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

// ServerAddress returns the host:port string for the HTTP server.
func (c *Config) ServerAddress() string {
	return fmt.Sprintf("%s:%d", c.Server.Host, c.Server.Port)
}
