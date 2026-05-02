// Package config provides configuration loading and validation for Bahia.
package config

import (
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
	Server        ServerConfig          `koanf:"server"`
	DB            DBConfig              `koanf:"db"`
	Harbor        HarborConfig          `koanf:"harbor"`
	Loom          LoomConfig            `koanf:"loom"`
	Nostr         NostrConfig           `koanf:"nostr"`
	Reconcile     ReconcileConfig       `koanf:"reconcile"`
	Runtime       RuntimeConfig         `koanf:"runtime"`
	Log           LogConfig             `koanf:"log"`
	Auth          AuthConfig            `koanf:"auth"`
	Adoption      AdoptionConfig        `koanf:"adoption"`
	DirectRuntime DirectRuntimeConfig   `koanf:"direct_runtime_actions"`
	CORS          CORSConfig            `koanf:"cors"`
	Blossom       BlossomConfig         `koanf:"blossom"`
	OCI           OCIServerConfig       `koanf:"oci"`
	HiveCI        HiveCIConfig          `koanf:"hiveci"`
	Cashu         CashuConfig           `koanf:"cashu"`
	Telemetry     TelemetryConfig       `koanf:"telemetry"`
	Notifications NotificationsConfig   `koanf:"notifications"`
	Registry      RegistryAdapterConfig `koanf:"registry"`
	LLM           LLMControlplaneConfig `koanf:"llm"`
}

// LLMControlplaneConfig holds DB-first LLM provisioning control-plane settings.
type LLMControlplaneConfig struct {
	Enabled                 bool                                `koanf:"enabled"`
	DefaultGatewayRef       string                              `koanf:"default_gateway_ref"`
	CoordinatorPollInterval time.Duration                       `koanf:"coordinator_poll_interval"`
	StaleRunTimeout         time.Duration                       `koanf:"stale_run_timeout"`
	ReconcileInterval       time.Duration                       `koanf:"reconcile_interval"`
	Gateways                map[string]LLMGatewayEndpointConfig `koanf:"gateways"`
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
	PrivateKey        string             `koanf:"private_key"`
	Relays            []string           `koanf:"relays"`
	PrivateRelays     []string           `koanf:"private_relays"`
	BrowserRelays     []string           `koanf:"browser_relays"`
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
type AuthConfig struct {
	Enabled      bool   `koanf:"enabled"`
	JWTSecret    string `koanf:"jwt_secret"`
	NIP98Enabled bool   `koanf:"nip98_enabled"`
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
			CoordinatorPollInterval: 5 * time.Second,
			StaleRunTimeout:         15 * time.Minute,
			ReconcileInterval:       60 * time.Second,
			Gateways:                map[string]LLMGatewayEndpointConfig{},
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

	if err := k.Unmarshal("", cfg); err != nil {
		return nil, fmt.Errorf("unmarshaling config: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func (c *Config) validate() error {
	if c.Adoption.Enabled {
		if !c.Auth.Enabled {
			return fmt.Errorf("config validation failed: auth.enabled=true is required when adoption.enabled=true")
		}
		if strings.TrimSpace(c.Auth.JWTSecret) == "" && !c.Auth.NIP98Enabled {
			return fmt.Errorf("config validation failed: auth.jwt_secret or auth.nip98_enabled=true is required when adoption.enabled=true")
		}
		if c.Adoption.OperatorAccessConfig.Empty() {
			return fmt.Errorf("config validation failed: adoption operator allowlist is required when adoption.enabled=true")
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
		if strings.TrimSpace(c.Auth.JWTSecret) == "" && !c.Auth.NIP98Enabled {
			return fmt.Errorf("config validation failed: auth.jwt_secret or auth.nip98_enabled=true is required when direct_runtime_actions.enabled=true")
		}
		if c.DirectRuntime.OperatorAccessConfig.Empty() {
			return fmt.Errorf("config validation failed: direct_runtime_actions operator allowlist is required when direct_runtime_actions.enabled=true")
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
	if err := c.validateLLM(); err != nil {
		return err
	}
	if err := c.validateRelaySidecar(); err != nil {
		return err
	}
	return nil
}

func (c *Config) validateLLM() error {
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
