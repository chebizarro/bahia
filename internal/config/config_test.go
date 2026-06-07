package config

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaults(t *testing.T) {
	cfg := Defaults()

	if cfg.Server.Port != 8080 {
		t.Errorf("expected default port 8080, got %d", cfg.Server.Port)
	}

	if cfg.Server.Host != "0.0.0.0" {
		t.Errorf("expected default host 0.0.0.0, got %s", cfg.Server.Host)
	}

	if cfg.DB.Host != "localhost" {
		t.Errorf("expected default DB host localhost, got %s", cfg.DB.Host)
	}

	if cfg.DB.Port != 5432 {
		t.Errorf("expected default DB port 5432, got %d", cfg.DB.Port)
	}

	if cfg.DB.SSLMode != "disable" {
		t.Errorf("expected default SSL mode disable, got %s", cfg.DB.SSLMode)
	}

	if cfg.Reconcile.Interval != 60*time.Second {
		t.Errorf("expected default reconcile interval 60s, got %s", cfg.Reconcile.Interval)
	}

	if !cfg.Reconcile.Enabled {
		t.Error("expected reconcile enabled by default")
	}

	if cfg.Runtime.Type != "docker" {
		t.Errorf("expected default runtime type docker, got %s", cfg.Runtime.Type)
	}
	if cfg.Adoption.Enabled {
		t.Error("expected adoption disabled by default")
	}
	if cfg.DirectRuntime.Enabled {
		t.Error("expected direct runtime actions disabled by default")
	}
	if cfg.LLM.AllowOperationalREST {
		t.Error("expected LLM operational REST disabled by default")
	}
	if cfg.Nostr.Sidecar.Enabled {
		t.Error("expected relay sidecar disabled by default")
	}
	if cfg.Nostr.Sidecar.ListenAddr != "0.0.0.0:3334" {
		t.Errorf("default sidecar ListenAddr = %q", cfg.Nostr.Sidecar.ListenAddr)
	}
	if cfg.Nostr.Sidecar.MaxQueryLimit != 500 {
		t.Errorf("default sidecar MaxQueryLimit = %d", cfg.Nostr.Sidecar.MaxQueryLimit)
	}
	if cfg.SoulFactory.Enabled {
		t.Error("expected SoulFactory disabled by default")
	}
	if len(cfg.SoulFactory.Relays) != 0 || len(cfg.SoulFactory.AdditionalRelays) != 0 {
		t.Errorf("expected default SoulFactory relays to be empty, got relays=%v additional=%v", cfg.SoulFactory.Relays, cfg.SoulFactory.AdditionalRelays)
	}
	if cfg.WorkerPressure.MemoryWarningMinGB != 4 || cfg.WorkerPressure.DiskWarningMinGB != 40 || cfg.WorkerPressure.VRAMWarningMinGB != 4 {
		t.Errorf("worker pressure defaults = %#v", cfg.WorkerPressure)
	}
}

func TestDBConfigDSN(t *testing.T) {
	cfg := DBConfig{
		Host:     "db.example.com",
		Port:     5432,
		User:     "admin",
		Password: "secret",
		Name:     "mydb",
		SSLMode:  "require",
	}

	expected := "postgres://admin:secret@db.example.com:5432/mydb?sslmode=require"
	if got := cfg.DSN(); got != expected {
		t.Errorf("expected DSN %s, got %s", expected, got)
	}
}

func TestDBConfigDSN_SpecialCharacters(t *testing.T) {
	tests := []struct {
		name     string
		user     string
		password string
		host     string
		port     int
		dbName   string
		sslMode  string
		wantSub  string // substring that must appear in the DSN
	}{
		{
			name:     "password with @",
			user:     "admin",
			password: "p@ssword",
			host:     "localhost",
			port:     5432,
			dbName:   "mydb",
			sslMode:  "disable",
			wantSub:  "p%40ssword",
		},
		{
			name:     "password with colon",
			user:     "admin",
			password: "pass:word",
			host:     "localhost",
			port:     5432,
			dbName:   "mydb",
			sslMode:  "disable",
			wantSub:  "pass%3Aword",
		},
		{
			name:     "password with slash",
			user:     "admin",
			password: "pass/word",
			host:     "localhost",
			port:     5432,
			dbName:   "mydb",
			sslMode:  "disable",
			wantSub:  "pass%2Fword",
		},
		{
			name:     "password with hash",
			user:     "admin",
			password: "pass#word",
			host:     "localhost",
			port:     5432,
			dbName:   "mydb",
			sslMode:  "disable",
			wantSub:  "pass%23word",
		},
		{
			name:     "password with question mark",
			user:     "admin",
			password: "pass?word",
			host:     "localhost",
			port:     5432,
			dbName:   "mydb",
			sslMode:  "disable",
			wantSub:  "pass%3Fword",
		},
		{
			name:     "user with special characters",
			user:     "admin@corp",
			password: "secret",
			host:     "localhost",
			port:     5432,
			dbName:   "mydb",
			sslMode:  "disable",
			wantSub:  "admin%40corp:secret@",
		},
		{
			name:     "password with multiple special chars",
			user:     "admin",
			password: "p@ss:w/rd#1?",
			host:     "localhost",
			port:     5432,
			dbName:   "mydb",
			sslMode:  "disable",
			wantSub:  "p%40ss%3Aw%2Frd%231%3F",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DBConfig{
				Host:     tt.host,
				Port:     tt.port,
				User:     tt.user,
				Password: tt.password,
				Name:     tt.dbName,
				SSLMode:  tt.sslMode,
			}
			got := cfg.DSN()

			if !strings.Contains(got, tt.wantSub) {
				t.Errorf("DSN %q should contain escaped %q", got, tt.wantSub)
			}

			// Verify the DSN is a valid URL that can be parsed back
			parsed, err := url.Parse(got)
			if err != nil {
				t.Fatalf("DSN is not a valid URL: %v", err)
			}

			if parsed.User.Username() != tt.user {
				t.Errorf("parsed user = %q, want %q", parsed.User.Username(), tt.user)
			}

			pw, _ := parsed.User.Password()
			if pw != tt.password {
				t.Errorf("parsed password = %q, want %q", pw, tt.password)
			}
		})
	}
}

func TestLoadFromEnvVars(t *testing.T) {
	// Set env vars using single-underscore convention (matches .env.example).
	envs := map[string]string{
		"BAHIA_DB_HOST":                               "envhost",
		"BAHIA_DB_PORT":                               "9999",
		"BAHIA_DB_MAX_OPEN_CONNS":                     "42",
		"BAHIA_SERVER_READ_TIMEOUT":                   "5s",
		"BAHIA_SERVER_PORT":                           "3000",
		"BAHIA_NOSTR_PUBLISH_ENABLED":                 "false",
		"BAHIA_RUNTIME_DOCKER_HOST":                   "tcp://remote:2375",
		"BAHIA_LOG_LEVEL":                             "debug",
		"BAHIA_RECONCILE_ENABLED":                     "false",
		"BAHIA_WORKER_PRESSURE_MEMORY_WARNING_MIN_GB": "8",
	}
	for k, v := range envs {
		t.Setenv(k, v)
	}

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.DB.Host != "envhost" {
		t.Errorf("DB.Host = %q, want %q", cfg.DB.Host, "envhost")
	}
	if cfg.DB.Port != 9999 {
		t.Errorf("DB.Port = %d, want %d", cfg.DB.Port, 9999)
	}
	if cfg.DB.MaxOpenConns != 42 {
		t.Errorf("DB.MaxOpenConns = %d, want %d", cfg.DB.MaxOpenConns, 42)
	}
	if cfg.Server.ReadTimeout != 5*time.Second {
		t.Errorf("Server.ReadTimeout = %v, want %v", cfg.Server.ReadTimeout, 5*time.Second)
	}
	if cfg.Server.Port != 3000 {
		t.Errorf("Server.Port = %d, want %d", cfg.Server.Port, 3000)
	}
	if cfg.Nostr.PublishEnabled != false {
		t.Error("Nostr.PublishEnabled should be false")
	}
	if cfg.Runtime.DockerHost != "tcp://remote:2375" {
		t.Errorf("Runtime.DockerHost = %q, want %q", cfg.Runtime.DockerHost, "tcp://remote:2375")
	}
	if cfg.Log.Level != "debug" {
		t.Errorf("Log.Level = %q, want %q", cfg.Log.Level, "debug")
	}
	if cfg.Reconcile.Enabled != false {
		t.Error("Reconcile.Enabled should be false")
	}
	if cfg.WorkerPressure.MemoryWarningMinGB != 8 {
		t.Errorf("WorkerPressure.MemoryWarningMinGB = %d, want 8", cfg.WorkerPressure.MemoryWarningMinGB)
	}
}

func TestLoadSoulFactoryConfigFromYAMLAndEnv(t *testing.T) {
	controller := strings.Repeat("a", 64)
	authorized := strings.Repeat("b", 64)
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := []byte(`soul_factory:
  enabled: true
  relays:
    - " wss://relay.example "
    - "wss://relay.example"
  additional_relays:
    - "wss://private.example"
  authorized_pubkeys:
    - "` + authorized + `"
  soul_factory_pubkey: "` + controller + `"
  signet_bunker_uri: "bunker://` + controller + `?relay=wss://relay.example"
  startup_timeout: 20s
  llm_base_url: "https://llm.example/"
  llm_model: "soul-model"
  llm_api_key: "from-yaml"
  llm_timeout: 45s
  workspace_gitea_url: "https://git.example/"
  workspace_private_key_ref: "secret://souls/openclaw/nostr-private-key"
  workspace_agent_memory_mcp_url_ref: "config://souls/agent-memory-mcp-url"
  workspace_gateway_port: 18781
`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("writing temp config: %v", err)
	}
	t.Setenv("BAHIA_SOUL_FACTORY_LLM_API_KEY", "from-env")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if !cfg.SoulFactory.Enabled {
		t.Fatal("SoulFactory should be enabled")
	}
	if got := cfg.SoulFactory.Relays; len(got) != 1 || got[0] != "wss://relay.example" {
		t.Fatalf("SoulFactory relays = %v", got)
	}
	if got := cfg.SoulFactory.AdditionalRelays; len(got) != 1 || got[0] != "wss://private.example" {
		t.Fatalf("SoulFactory additional relays = %v", got)
	}
	if cfg.SoulFactory.SoulFactoryPubkey != controller {
		t.Fatalf("SoulFactory pubkey = %q", cfg.SoulFactory.SoulFactoryPubkey)
	}
	if cfg.SoulFactory.AuthorizedPubkeys[0] != authorized {
		t.Fatalf("SoulFactory authorized pubkeys = %v", cfg.SoulFactory.AuthorizedPubkeys)
	}
	if cfg.SoulFactory.StartupTimeout != 20*time.Second {
		t.Fatalf("SoulFactory startup_timeout = %s", cfg.SoulFactory.StartupTimeout)
	}
	if cfg.SoulFactory.LLMBaseURL != "https://llm.example" {
		t.Fatalf("SoulFactory llm_base_url = %q", cfg.SoulFactory.LLMBaseURL)
	}
	if cfg.SoulFactory.LLMAPIKey != "from-env" {
		t.Fatalf("SoulFactory llm_api_key = %q", cfg.SoulFactory.LLMAPIKey)
	}
	if cfg.SoulFactory.LLMTimeout != 45*time.Second {
		t.Fatalf("SoulFactory llm_timeout = %s", cfg.SoulFactory.LLMTimeout)
	}
	if cfg.SoulFactory.WorkspaceGiteaURL != "https://git.example" {
		t.Fatalf("SoulFactory workspace_gitea_url = %q", cfg.SoulFactory.WorkspaceGiteaURL)
	}
	if cfg.SoulFactory.WorkspacePrivateKeyRef != "secret://souls/openclaw/nostr-private-key" {
		t.Fatalf("SoulFactory workspace_private_key_ref = %q", cfg.SoulFactory.WorkspacePrivateKeyRef)
	}
	if cfg.SoulFactory.WorkspaceAgentMemoryMCPURLRef != "config://souls/agent-memory-mcp-url" {
		t.Fatalf("SoulFactory workspace_agent_memory_mcp_url_ref = %q", cfg.SoulFactory.WorkspaceAgentMemoryMCPURLRef)
	}
	if cfg.SoulFactory.WorkspaceGatewayPort != 18781 {
		t.Fatalf("SoulFactory workspace_gateway_port = %d", cfg.SoulFactory.WorkspaceGatewayPort)
	}
}

func TestLoadRejectsInvalidSoulFactoryConfig(t *testing.T) {
	validPubkey := strings.Repeat("c", 64)
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "missing relays",
			yaml: `soul_factory:
  enabled: true
  authorized_pubkeys: ["` + validPubkey + `"]
  signet_bunker_uri: "bunker://` + validPubkey + `"
  llm_base_url: "https://llm.example"
  llm_model: "soul-model"
  llm_api_key: "secret"
`,
			want: "soul_factory.relays requires at least one relay",
		},
		{
			name: "missing signet",
			yaml: `soul_factory:
  enabled: true
  relays: ["wss://relay.example"]
  authorized_pubkeys: ["` + validPubkey + `"]
  llm_base_url: "https://llm.example"
  llm_model: "soul-model"
  llm_api_key: "secret"
`,
			want: "soul_factory.signet_bunker_uri is required",
		},
		{
			name: "invalid authorized pubkey",
			yaml: `soul_factory:
  enabled: true
  relays: ["wss://relay.example"]
  authorized_pubkeys: ["not-hex"]
  signet_bunker_uri: "bunker://` + validPubkey + `"
  llm_base_url: "https://llm.example"
  llm_model: "soul-model"
  llm_api_key: "secret"
`,
			want: "soul_factory.authorized_pubkeys",
		},
		{
			name: "missing llm",
			yaml: `soul_factory:
  enabled: true
  relays: ["wss://relay.example"]
  authorized_pubkeys: ["` + validPubkey + `"]
  signet_bunker_uri: "bunker://` + validPubkey + `"
`,
			want: "soul_factory.llm_base_url is required",
		},
		{
			name: "llm base url with path",
			yaml: `soul_factory:
  enabled: true
  relays: ["wss://relay.example"]
  authorized_pubkeys: ["` + validPubkey + `"]
  signet_bunker_uri: "bunker://` + validPubkey + `"
  llm_base_url: "https://llm.example/v1"
  llm_model: "soul-model"
  llm_api_key: "secret"
`,
			want: "soul_factory.llm_base_url must be an API origin without a path",
		},
		{
			name: "workspace missing private key ref",
			yaml: `soul_factory:
  enabled: true
  relays: ["wss://relay.example"]
  authorized_pubkeys: ["` + validPubkey + `"]
  signet_bunker_uri: "bunker://` + validPubkey + `"
  llm_base_url: "https://llm.example"
  llm_model: "soul-model"
  llm_api_key: "secret"
  workspace_gitea_url: "https://git.example"
  workspace_agent_memory_mcp_url_ref: "config://souls/agent-memory"
`,
			want: "soul_factory.workspace_private_key_ref is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tt.yaml), 0o644); err != nil {
				t.Fatalf("writing temp config: %v", err)
			}
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestLoadWorkerPressureConfigOverridesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`worker_pressure:
  memory_warning_min_gb: 12
  memory_warning_min_ratio: 0.25
  thermal_warning_c: 80
`), 0o644); err != nil {
		t.Fatalf("writing temp config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.WorkerPressure.MemoryWarningMinGB != 12 {
		t.Fatalf("MemoryWarningMinGB = %d, want 12", cfg.WorkerPressure.MemoryWarningMinGB)
	}
	if cfg.WorkerPressure.MemoryWarningRatio != 0.25 {
		t.Fatalf("MemoryWarningRatio = %v, want 0.25", cfg.WorkerPressure.MemoryWarningRatio)
	}
	if cfg.WorkerPressure.DiskWarningMinGB != 40 {
		t.Fatalf("DiskWarningMinGB default = %d, want 40", cfg.WorkerPressure.DiskWarningMinGB)
	}
	if cfg.WorkerPressure.ThermalWarningC != 80 {
		t.Fatalf("ThermalWarningC = %v, want 80", cfg.WorkerPressure.ThermalWarningC)
	}
}

func TestValidateWorkerPressureRejectsUnsafeThresholds(t *testing.T) {
	cfg := Defaults()
	cfg.WorkerPressure.MemoryWarningMinGB = 1
	cfg.WorkerPressure.MemoryCriticalMinGB = 2
	if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "memory_warning_min_gb") {
		t.Fatalf("validate() error = %v, want memory threshold ordering error", err)
	}
}

func TestLoadRejectsRemovedAuthKeys(t *testing.T) {
	t.Run("jwt_secret yaml", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte("auth:\n  jwt_secret: test-secret\n"), 0o644); err != nil {
			t.Fatalf("writing temp config: %v", err)
		}
		_, err := Load(path)
		if err == nil || !strings.Contains(err.Error(), "auth.jwt_secret has been removed") {
			t.Fatalf("Load() error = %v, want removed jwt_secret error", err)
		}
	})

	t.Run("nip98_enabled yaml", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte("auth:\n  enabled: true\n  nip98_enabled: true\n"), 0o644); err != nil {
			t.Fatalf("writing temp config: %v", err)
		}
		_, err := Load(path)
		if err == nil || !strings.Contains(err.Error(), "auth.nip98_enabled has been removed") {
			t.Fatalf("Load() error = %v, want removed nip98_enabled error", err)
		}
	})

	t.Run("jwt_secret env", func(t *testing.T) {
		t.Setenv("BAHIA_AUTH_JWT_SECRET", "test-secret")
		_, err := Load("")
		if err == nil || !strings.Contains(err.Error(), "auth.jwt_secret has been removed") {
			t.Fatalf("Load() error = %v, want removed jwt_secret error", err)
		}
	})
}

func TestLoadFromEnvVars_DoubleUnderscore(t *testing.T) {
	// Double-underscore should still work as an explicit separator.
	t.Setenv("BAHIA_DB__HOST", "legacy-host")
	t.Setenv("BAHIA_DB__MAX_OPEN_CONNS", "77")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.DB.Host != "legacy-host" {
		t.Errorf("DB.Host = %q, want %q (via double-underscore)", cfg.DB.Host, "legacy-host")
	}
	if cfg.DB.MaxOpenConns != 77 {
		t.Errorf("DB.MaxOpenConns = %d, want %d (via double-underscore)", cfg.DB.MaxOpenConns, 77)
	}
}

func TestLoadNestedRuntimeConfigFromYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := []byte(`runtime:
  type: docker
  docker_host: unix:///legacy.sock
  default:
    type: compose
    execution_mode: cli
    docker_host: tcp://default:2375
    compose_dir: /srv/bahia/default
  environments:
    production:
      endpoint_ref: prod-docker
      execution_mode: cli
      compose_dir: /srv/bahia/production
      docker_host: tcp://prod:2375
    staging:
      type: kubernetes
      kube_context: staging-cluster
      kube_namespace: staging
  endpoints:
    prod-docker:
      docker_host: tcp://docker-prod.example.com:2376
      ca_cert_file: /etc/bahia/docker/ca.pem
      client_cert_file: /etc/bahia/docker/cert.pem
      client_key_file: /etc/bahia/docker/key.pem
`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("writing temp config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Runtime.Type != "docker" {
		t.Errorf("legacy Runtime.Type = %q, want docker", cfg.Runtime.Type)
	}
	if cfg.Runtime.Default.Type != "compose" {
		t.Errorf("Runtime.Default.Type = %q, want compose", cfg.Runtime.Default.Type)
	}
	if cfg.Runtime.Default.ComposeDir != "/srv/bahia/default" {
		t.Errorf("Runtime.Default.ComposeDir = %q", cfg.Runtime.Default.ComposeDir)
	}
	if cfg.Runtime.Default.ExecutionMode != "cli" {
		t.Errorf("Runtime.Default.ExecutionMode = %q", cfg.Runtime.Default.ExecutionMode)
	}
	prod := cfg.Runtime.Environments["production"]
	if prod.ComposeDir != "/srv/bahia/production" || prod.ExecutionMode != "cli" || prod.DockerHost != "tcp://prod:2375" || prod.EndpointRef != "prod-docker" {
		t.Errorf("production runtime target = %+v", prod)
	}
	staging := cfg.Runtime.Environments["staging"]
	if staging.Type != "kubernetes" || staging.KubeContext != "staging-cluster" || staging.KubeNamespace != "staging" {
		t.Errorf("staging runtime target = %+v", staging)
	}
	endpoint := cfg.Runtime.Endpoints["prod-docker"]
	if endpoint.DockerHost != "tcp://docker-prod.example.com:2376" || endpoint.ClientKeyFile == "" {
		t.Errorf("runtime endpoint not loaded: %+v", endpoint)
	}
}

func TestLoadRelaySidecarConfigFromYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := []byte(`nostr:
  private_key: ""
  browser_relays:
    - "ws://localhost:3000/relay"
  sidecar:
    enabled: true
    listen_addr: "127.0.0.1:3334"
    public_url: "ws://localhost:3000/relay"
    backend_url: "ws://relay:3334"
    data_dir: "/tmp/bahia-relay"
    mirror_external: false
    event_retention: 168h
    request_retention: 24h
    auth_private_key: ""
    max_query_limit: 250
`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("writing temp config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if !cfg.Nostr.Sidecar.Enabled {
		t.Fatal("expected sidecar enabled")
	}
	if cfg.Nostr.Sidecar.ListenAddr != "127.0.0.1:3334" {
		t.Errorf("ListenAddr = %q", cfg.Nostr.Sidecar.ListenAddr)
	}
	if cfg.Nostr.Sidecar.EventRetention != 168*time.Hour {
		t.Errorf("EventRetention = %s", cfg.Nostr.Sidecar.EventRetention)
	}
	if cfg.Nostr.Sidecar.BackendURL != "ws://relay:3334" {
		t.Errorf("BackendURL = %q", cfg.Nostr.Sidecar.BackendURL)
	}
	if got := cfg.Nostr.BrowserRelays; len(got) != 1 || got[0] != "ws://localhost:3000/relay" {
		t.Fatalf("BrowserRelays = %#v", got)
	}
}

func TestNormalizeNostrRelaysFromYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := []byte(`nostr:
  relays:
    - " wss://relay-backend.example "
    - "wss://relay-backend.example"
  browser_relays:
    - "wss://relay-browser.example"
    - ""
`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("writing temp config: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	assertStringSlice(t, cfg.Nostr.Relays, []string{"wss://relay-backend.example"})
	assertStringSlice(t, cfg.Nostr.BrowserRelays, []string{"wss://relay-browser.example"})
}

func TestNostrRelayPolicySourcesAreIndependent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := []byte(`nostr:
  relays:
    - " wss://legacy-service.example "
  service_relays:
    - "wss://service-write.example"
    - "wss://service-write.example"
  browser_relays:
    - "wss://browser-read.example"
  contextvm_relays:
    - "wss://contextvm-request.example"
  relay_auth_unavailable: "exclude_and_fail"
`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("writing temp config: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	assertStringSlice(t, cfg.Nostr.ServiceRelays, []string{"wss://service-write.example"})
	assertStringSlice(t, cfg.Nostr.Relays, []string{"wss://service-write.example"})
	assertStringSlice(t, cfg.Nostr.ServiceRelayPolicyRelays(), []string{"wss://service-write.example"})
	assertStringSlice(t, cfg.Nostr.BrowserRelayPolicyRelays(), []string{"wss://browser-read.example"})
	assertStringSlice(t, cfg.Nostr.ContextVMRelays, []string{"wss://contextvm-request.example"})
	assertStringSlice(t, cfg.Nostr.ContextVMRelayPolicyRelays(), []string{"wss://contextvm-request.example"})
	if cfg.Nostr.RelayAuthUnavailableSemantics() != RelayAuthUnavailableExcludeAndFail {
		t.Fatalf("RelayAuthUnavailableSemantics() = %q", cfg.Nostr.RelayAuthUnavailableSemantics())
	}
}

func TestNostrRelayPolicyCompatibilityFallbacks(t *testing.T) {
	cfg := Defaults()
	cfg.Nostr.Relays = []string{"wss://service-compat.example"}
	cfg.Nostr.BrowserRelays = []string{"wss://browser.example"}

	if err := cfg.validate(); err != nil {
		t.Fatalf("validate() error: %v", err)
	}

	assertStringSlice(t, cfg.Nostr.ServiceRelays, []string{"wss://service-compat.example"})
	assertStringSlice(t, cfg.Nostr.ServiceRelayPolicyRelays(), []string{"wss://service-compat.example"})
	if len(cfg.Nostr.ContextVMRelays) != 0 {
		t.Fatalf("ContextVMRelays = %#v, want empty configured source", cfg.Nostr.ContextVMRelays)
	}
	assertStringSlice(t, cfg.Nostr.ContextVMRelayPolicyRelays(), []string{"wss://browser.example"})
	if cfg.Nostr.RelayAuthUnavailablePolicy != RelayAuthUnavailableExcludeAndFail {
		t.Fatalf("RelayAuthUnavailablePolicy = %q", cfg.Nostr.RelayAuthUnavailablePolicy)
	}
}

func TestNostrRelayPolicyLoadsFromEnvironment(t *testing.T) {
	t.Setenv("BAHIA_NOSTR_SERVICE_RELAYS", "wss://service1.example, wss://service2.example")
	t.Setenv("BAHIA_NOSTR_BROWSER_RELAYS", "wss://browser.example")
	t.Setenv("BAHIA_NOSTR_CONTEXTVM_RELAYS", "wss://contextvm.example")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	assertStringSlice(t, cfg.Nostr.ServiceRelayPolicyRelays(), []string{"wss://service1.example", "wss://service2.example"})
	assertStringSlice(t, cfg.Nostr.BrowserRelayPolicyRelays(), []string{"wss://browser.example"})
	assertStringSlice(t, cfg.Nostr.ContextVMRelayPolicyRelays(), []string{"wss://contextvm.example"})
}

func TestNostrRelayAdministrationDefaultDisabled(t *testing.T) {
	cfg := Defaults()
	if cfg.Nostr.RelayAdministration.Enabled {
		t.Fatal("relay administration must be disabled by default")
	}
	if cfg.Nostr.RelayAdministration.AdministratorPrivateKeyRef != "" {
		t.Fatalf("AdministratorPrivateKeyRef = %q, want empty", cfg.Nostr.RelayAdministration.AdministratorPrivateKeyRef)
	}
	if len(cfg.Nostr.RelayAdministration.Targets) != 0 {
		t.Fatalf("Targets = %#v, want empty", cfg.Nostr.RelayAdministration.Targets)
	}
}

func TestNostrRelayAdministrationEnabledValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := []byte(strings.Join([]string{
		"nostr:",
		"  relay_administration:",
		"    enabled: true",
		"    administrator_private_key_ref: \"secret://relay-admin/sidecar\"",
		"    targets:",
		"      - ref: \" sidecar \"",
		"        relay_url: \" wss://relay.example.com/nostr/ \"",
		"        http_url: \" https://relay.example.com/relay/ \"",
		"        authorization: \" BAHIA_OWNED \"",
		"        administrator_pubkeys:",
		"          - \"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\"",
		"          - \"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\"",
		"",
	}, "\n"))
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("writing temp config: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	admin := cfg.Nostr.RelayAdministration
	if !admin.Enabled {
		t.Fatal("RelayAdministration.Enabled = false")
	}
	if admin.AdministratorPrivateKeyRef != "secret://relay-admin/sidecar" {
		t.Fatalf("AdministratorPrivateKeyRef = %q", admin.AdministratorPrivateKeyRef)
	}
	if len(admin.Targets) != 1 {
		t.Fatalf("Targets len = %d", len(admin.Targets))
	}
	target := admin.Targets[0]
	if target.Ref != "sidecar" || target.RelayURL != "wss://relay.example.com/nostr/" || target.HTTPURL != "https://relay.example.com/relay/" {
		t.Fatalf("target normalized incorrectly or pathful URL not preserved: %#v", target)
	}
	if target.Authorization != RelayAdministrationBahiaOwned {
		t.Fatalf("Authorization = %q", target.Authorization)
	}
	assertStringSlice(t, target.AdministratorPubkeys, []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})
}

func TestNostrRelayAdministrationRejectsUnsafeEnabledConfig(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{
			name: "missing secret ref",
			mutate: func(cfg *Config) {
				cfg.Nostr.RelayAdministration = RelayAdministrationConfig{Enabled: true}
			},
			want: "administrator_private_key_ref is required",
		},
		{
			name: "raw private key ref rejected",
			mutate: func(cfg *Config) {
				cfg.Nostr.RelayAdministration = validRelayAdministrationConfig()
				cfg.Nostr.RelayAdministration.AdministratorPrivateKeyRef = strings.Repeat("a", 64)
			},
			want: "must be a secret reference",
		},
		{
			name: "unasserted authorization rejected",
			mutate: func(cfg *Config) {
				cfg.Nostr.RelayAdministration = validRelayAdministrationConfig()
				cfg.Nostr.RelayAdministration.Targets[0].Authorization = "public_relay"
			},
			want: "authorization must be",
		},
		{
			name: "missing administrator pubkey rejected",
			mutate: func(cfg *Config) {
				cfg.Nostr.RelayAdministration = validRelayAdministrationConfig()
				cfg.Nostr.RelayAdministration.Targets[0].AdministratorPubkeys = nil
			},
			want: "requires administrator_pubkeys",
		},
		{
			name: "non relay url rejected",
			mutate: func(cfg *Config) {
				cfg.Nostr.RelayAdministration = validRelayAdministrationConfig()
				cfg.Nostr.RelayAdministration.Targets[0].RelayURL = "https://relay.example.com"
			},
			want: "scheme must be ws or wss",
		},
		{
			name: "external plaintext relay rejected",
			mutate: func(cfg *Config) {
				cfg.Nostr.RelayAdministration = validRelayAdministrationConfig()
				cfg.Nostr.RelayAdministration.Targets[0].RelayURL = "ws://relay.example.com"
			},
			want: "use wss",
		},
		{
			name: "external plaintext http endpoint rejected",
			mutate: func(cfg *Config) {
				cfg.Nostr.RelayAdministration = validRelayAdministrationConfig()
				cfg.Nostr.RelayAdministration.Targets[0].HTTPURL = "http://relay.example.com"
			},
			want: "use https",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Defaults()
			tt.mutate(cfg)
			err := cfg.validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validate() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func validRelayAdministrationConfig() RelayAdministrationConfig {
	return RelayAdministrationConfig{
		Enabled:                    true,
		AdministratorPrivateKeyRef: "secret://relay-admin/sidecar",
		Targets: []RelayAdministrationTarget{{
			Ref:                  "sidecar",
			RelayURL:             "wss://relay.example.com",
			Authorization:        RelayAdministrationBahiaAuthorized,
			AdministratorPubkeys: []string{strings.Repeat("b", 64)},
		}},
	}
}

func TestNostrRelayAuthUnavailablePolicyValidation(t *testing.T) {
	valid := Defaults()
	valid.Nostr.RelayAuthUnavailablePolicy = " EXCLUDE_AND_FAIL "
	if err := valid.validate(); err != nil {
		t.Fatalf("exclude_and_fail policy should validate: %v", err)
	}
	if valid.Nostr.RelayAuthUnavailablePolicy != RelayAuthUnavailableExcludeAndFail {
		t.Fatalf("RelayAuthUnavailablePolicy normalized to %q", valid.Nostr.RelayAuthUnavailablePolicy)
	}

	invalid := Defaults()
	invalid.Nostr.RelayAuthUnavailablePolicy = "fallback_to_rest"
	if err := invalid.validate(); err == nil || !strings.Contains(err.Error(), "nostr.relay_auth_unavailable") {
		t.Fatalf("validate() error = %v, want relay_auth_unavailable validation error", err)
	}
}

func TestLoadRejectsRemovedEncryptedRequestKeys(t *testing.T) {
	tests := []struct {
		name        string
		yaml        string
		envKey      string
		envValue    string
		want        string
		replacement string
	}{
		{
			name:        "private relays yaml",
			yaml:        "nostr:\n  private_relays:\n    - wss://legacy-backend.example\n",
			want:        "nostr.private_relays has been removed",
			replacement: "use nostr.relays",
		},
		{
			name:        "private browser relays yaml",
			yaml:        "nostr:\n  private_browser_relays:\n    - wss://legacy-browser.example\n",
			want:        "nostr.private_browser_relays has been removed",
			replacement: "use nostr.browser_relays",
		},
		{
			name:        "encrypted request relays yaml",
			yaml:        "nostr:\n  encrypted_request_relays:\n    - wss://legacy-encrypted.example\n",
			want:        "nostr.encrypted_request_relays has been removed",
			replacement: "use nostr.relays",
		},
		{
			name:        "browser encrypted request relays yaml",
			yaml:        "nostr:\n  browser_encrypted_request_relays:\n    - wss://legacy-browser-encrypted.example\n",
			want:        "nostr.browser_encrypted_request_relays has been removed",
			replacement: "use nostr.browser_relays",
		},
		{
			name:        "private transport feature yaml",
			yaml:        "features:\n  private_nostr_transport: true\n",
			want:        "features.private_nostr_transport has been removed",
			replacement: "use the encrypted_nostr_requests discovery feature; configure nostr.private_key and Bahia browser relay discovery to enable it",
		},
		{
			name:        "private relays env",
			envKey:      "BAHIA_NOSTR_PRIVATE_RELAYS",
			envValue:    "wss://legacy-backend.example",
			want:        "nostr.private_relays has been removed",
			replacement: "use nostr.relays",
		},
		{
			name:        "private browser relays env",
			envKey:      "BAHIA_NOSTR_PRIVATE_BROWSER_RELAYS",
			envValue:    "wss://legacy-browser.example",
			want:        "nostr.private_browser_relays has been removed",
			replacement: "use nostr.browser_relays",
		},
		{
			name:        "encrypted request relays env",
			envKey:      "BAHIA_NOSTR_ENCRYPTED_REQUEST_RELAYS",
			envValue:    "wss://legacy-encrypted.example",
			want:        "nostr.encrypted_request_relays has been removed",
			replacement: "use nostr.relays",
		},
		{
			name:        "browser encrypted request relays env",
			envKey:      "BAHIA_NOSTR_BROWSER_ENCRYPTED_REQUEST_RELAYS",
			envValue:    "wss://legacy-browser-encrypted.example",
			want:        "nostr.browser_encrypted_request_relays has been removed",
			replacement: "use nostr.browser_relays",
		},
		{
			name:        "private transport feature env",
			envKey:      "BAHIA_FEATURES_PRIVATE_NOSTR_TRANSPORT",
			envValue:    "true",
			want:        "features.private_nostr_transport has been removed",
			replacement: "use the encrypted_nostr_requests discovery feature; configure nostr.private_key and Bahia browser relay discovery to enable it",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := ""
			if tt.yaml != "" {
				path = filepath.Join(t.TempDir(), "config.yaml")
				if err := os.WriteFile(path, []byte(tt.yaml), 0o644); err != nil {
					t.Fatalf("writing temp config: %v", err)
				}
			}
			if tt.envKey != "" {
				t.Setenv(tt.envKey, tt.envValue)
			}
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), tt.replacement) {
				t.Fatalf("Load() error = %v, want %q and %q", err, tt.want, tt.replacement)
			}
		})
	}
}

func assertStringSlice(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("slice length = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("slice[%d] = %q, want %q; full slice %#v", i, got[i], want[i], got)
		}
	}
}

func TestRelaySidecarValidation(t *testing.T) {
	cfg := Defaults()
	cfg.Nostr.Sidecar.Enabled = true
	cfg.Nostr.Sidecar.PublicURL = ""
	if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "nostr.sidecar.public_url") {
		t.Fatalf("validate error = %v, want public_url requirement", err)
	}
}

func TestPrivilegedRouteConfigValidation(t *testing.T) {
	t.Run("adoption requires auth", func(t *testing.T) {
		cfg := Defaults()
		cfg.Adoption.Enabled = true
		cfg.Adoption.AllowedSubjects = []string{"ops"}
		err := cfg.validate()
		if err == nil || !strings.Contains(err.Error(), "auth.enabled=true") {
			t.Fatalf("validate error = %v, want auth requirement", err)
		}
	})

	t.Run("adoption requires allowlist", func(t *testing.T) {
		cfg := Defaults()
		cfg.Auth.Enabled = true
		cfg.Adoption.Enabled = true
		err := cfg.validate()
		if err == nil || !strings.Contains(err.Error(), "adoption operator allowlist") {
			t.Fatalf("validate error = %v, want adoption allowlist requirement", err)
		}
	})

	t.Run("direct runtime requires allowlist", func(t *testing.T) {
		cfg := Defaults()
		cfg.Auth.Enabled = true
		cfg.DirectRuntime.Enabled = true
		err := cfg.validate()
		if err == nil || !strings.Contains(err.Error(), "direct_runtime_actions operator allowlist") {
			t.Fatalf("validate error = %v, want direct runtime allowlist requirement", err)
		}
	})

	t.Run("LLM operational REST requires auth", func(t *testing.T) {
		cfg := Defaults()
		cfg.LLM.Enabled = true
		cfg.LLM.AllowOperationalREST = true
		cfg.LLM.AllowedSubjects = []string{"ops"}
		err := cfg.validate()
		if err == nil || !strings.Contains(err.Error(), "auth.enabled=true") {
			t.Fatalf("validate error = %v, want auth requirement", err)
		}
	})

	t.Run("LLM operational REST requires allowlist", func(t *testing.T) {
		cfg := Defaults()
		cfg.Auth.Enabled = true
		cfg.LLM.Enabled = true
		cfg.LLM.AllowOperationalREST = true
		err := cfg.validate()
		if err == nil || !strings.Contains(err.Error(), "llm operator allowlist") {
			t.Fatalf("validate error = %v, want llm allowlist requirement", err)
		}
	})

	t.Run("LLM operational REST requires LLM subsystem", func(t *testing.T) {
		cfg := Defaults()
		cfg.Auth.Enabled = true
		cfg.LLM.AllowOperationalREST = true
		cfg.LLM.AllowedSubjects = []string{"ops"}
		err := cfg.validate()
		if err == nil || !strings.Contains(err.Error(), "llm.enabled=true") {
			t.Fatalf("validate error = %v, want llm enabled requirement", err)
		}
	})

	t.Run("enabled with auth and allowlists is valid", func(t *testing.T) {
		cfg := Defaults()
		cfg.Nostr.PrivateKey = "test-secret-key"
		cfg.Auth.Enabled = true
		cfg.Adoption.Enabled = true
		cfg.Adoption.AllowedSubjects = []string{"ops"}
		cfg.DirectRuntime.Enabled = true
		cfg.DirectRuntime.AllowedPubkeys = []string{"abcdef"}
		cfg.LLM.Enabled = true
		cfg.LLM.AllowOperationalREST = true
		cfg.LLM.AllowedSubjects = []string{"llm-ops"}
		if err := cfg.validate(); err != nil {
			t.Fatalf("validate error = %v", err)
		}
	})

	t.Run("runtime endpoint requires docker host", func(t *testing.T) {
		cfg := Defaults()
		cfg.Runtime.Endpoints["prod"] = RuntimeEndpointConfig{}
		err := cfg.validate()
		if err == nil || !strings.Contains(err.Error(), "runtime endpoint \"prod\" requires docker_host") {
			t.Fatalf("validate error = %v, want endpoint docker_host requirement", err)
		}
	})

	t.Run("runtime endpoint requires cert key pair", func(t *testing.T) {
		cfg := Defaults()
		cfg.Runtime.Endpoints["prod"] = RuntimeEndpointConfig{DockerHost: "tcp://docker:2376", ClientCertFile: "cert.pem"}
		err := cfg.validate()
		if err == nil || !strings.Contains(err.Error(), "requires both client_cert_file and client_key_file") {
			t.Fatalf("validate error = %v, want endpoint cert pair requirement", err)
		}
	})

	t.Run("runtime endpoint refs must exist", func(t *testing.T) {
		cfg := Defaults()
		cfg.Runtime.Environments["prod"] = RuntimeTargetConfig{EndpointRef: "missing"}
		err := cfg.validate()
		if err == nil || !strings.Contains(err.Error(), `endpoint_ref "missing" is not configured`) {
			t.Fatalf("validate error = %v, want unknown endpoint_ref requirement", err)
		}
	})

	t.Run("auth enabled satisfies privileged auth method", func(t *testing.T) {
		cfg := Defaults()
		cfg.Nostr.PrivateKey = "test-secret-key"
		cfg.Auth.Enabled = true
		cfg.Adoption.Enabled = true
		cfg.Adoption.AllowedPubkeys = []string{"abcdef"}
		if err := cfg.validate(); err != nil {
			t.Fatalf("validate error = %v", err)
		}
	})
}

func TestCashuConfigValidation(t *testing.T) {
	cfg := Defaults()
	cfg.Cashu.Enabled = true
	if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "cashu.enabled=true is unsupported") {
		t.Fatalf("validate error = %v, want unsupported cashu live-mode requirement", err)
	}

	cfg.Cashu.MintURL = "https://mint.example.com"
	if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "cashu.enabled=true is unsupported") {
		t.Fatalf("validate with mint URL error = %v, want unsupported cashu live-mode requirement", err)
	}
}

func TestQdrantConfigValidation(t *testing.T) {
	t.Run("url requires api key by default", func(t *testing.T) {
		cfg := Defaults()
		cfg.Qdrant.URL = "https://qdrant.example.com/"
		if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "qdrant.api_key") {
			t.Fatalf("validate error = %v, want qdrant.api_key requirement", err)
		}
	})

	t.Run("url with api key is valid and normalized", func(t *testing.T) {
		cfg := Defaults()
		cfg.Qdrant.URL = " https://qdrant.example.com/ "
		cfg.Qdrant.APIKey = " secret "
		if err := cfg.validate(); err != nil {
			t.Fatalf("validate error = %v", err)
		}
		if cfg.Qdrant.URL != "https://qdrant.example.com" || cfg.Qdrant.APIKey != "secret" {
			t.Fatalf("qdrant config not normalized: %+v", cfg.Qdrant)
		}
	})

	t.Run("explicit unauthenticated local mode is valid", func(t *testing.T) {
		cfg := Defaults()
		cfg.Qdrant.URL = "http://localhost:6333"
		cfg.Qdrant.AllowUnauthenticatedLocal = true
		if err := cfg.validate(); err != nil {
			t.Fatalf("validate error = %v", err)
		}
	})

	t.Run("unauthenticated remote mode is rejected", func(t *testing.T) {
		cfg := Defaults()
		cfg.Qdrant.URL = "https://qdrant.example.com"
		cfg.Qdrant.AllowUnauthenticatedLocal = true
		if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "allow_unauthenticated_local") {
			t.Fatalf("validate error = %v, want unauthenticated local-only requirement", err)
		}
	})

	t.Run("invalid url fails", func(t *testing.T) {
		cfg := Defaults()
		cfg.Qdrant.URL = "://bad-url"
		cfg.Qdrant.APIKey = "secret"
		if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "qdrant.url") {
			t.Fatalf("validate error = %v, want qdrant.url requirement", err)
		}
	})
}

func TestSecretDependentFeatureValidationRequiresNostrPrivateKey(t *testing.T) {
	t.Run("adoption", func(t *testing.T) {
		cfg := Defaults()
		cfg.Auth.Enabled = true
		cfg.Adoption.Enabled = true
		cfg.Adoption.AllowedSubjects = []string{"ops"}
		if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "nostr.private_key is required when adoption.enabled=true") {
			t.Fatalf("validate error = %v, want nostr private key requirement", err)
		}
	})

	t.Run("direct runtime", func(t *testing.T) {
		cfg := Defaults()
		cfg.Auth.Enabled = true
		cfg.DirectRuntime.Enabled = true
		cfg.DirectRuntime.AllowedSubjects = []string{"ops"}
		if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "nostr.private_key is required when direct_runtime_actions.enabled=true") {
			t.Fatalf("validate error = %v, want nostr private key requirement", err)
		}
	})
}

func TestLoadPrivilegedRouteConfigFromYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := []byte(`auth:
  enabled: true
nostr:
  private_key: test-secret-key
adoption:
  enabled: true
  allow_compose_takeover: true
  allowed_subjects:
    - ops-user
  allowed_pubkeys:
    - abc123
  allowed_emails:
    - ops@example.com
direct_runtime_actions:
  enabled: true
  allowed_subjects:
    - runtime-user
llm:
  enabled: true
  allow_operational_rest: true
  allowed_subjects:
    - llm-ops
`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("writing temp config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if !cfg.Adoption.Enabled || !cfg.Adoption.AllowComposeTakeover || len(cfg.Adoption.AllowedSubjects) != 1 || cfg.Adoption.AllowedSubjects[0] != "ops-user" {
		t.Fatalf("adoption config not loaded: %+v", cfg.Adoption)
	}
	if !cfg.DirectRuntime.Enabled || len(cfg.DirectRuntime.AllowedSubjects) != 1 || cfg.DirectRuntime.AllowedSubjects[0] != "runtime-user" {
		t.Fatalf("direct runtime config not loaded: %+v", cfg.DirectRuntime)
	}
	if !cfg.LLM.AllowOperationalREST || len(cfg.LLM.AllowedSubjects) != 1 || cfg.LLM.AllowedSubjects[0] != "llm-ops" {
		t.Fatalf("llm operational REST config not loaded: %+v", cfg.LLM)
	}
}

func TestLoadNestedRuntimeConfigFromEnvVars(t *testing.T) {
	t.Setenv("BAHIA_RUNTIME__DEFAULT__TYPE", "compose")
	t.Setenv("BAHIA_RUNTIME__DEFAULT__COMPOSE_DIR", "/srv/bahia/default")
	t.Setenv("BAHIA_RUNTIME__ENVIRONMENTS__production__DOCKER_HOST", "tcp://prod:2375")
	t.Setenv("BAHIA_RUNTIME__ENVIRONMENTS__production__ENDPOINT_REF", "prod")
	t.Setenv("BAHIA_RUNTIME__ENVIRONMENTS__production__COMPOSE_DIR", "/srv/bahia/production")
	t.Setenv("BAHIA_RUNTIME__ENDPOINTS__prod__DOCKER_HOST", "tcp://docker-prod:2376")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Runtime.Default.Type != "compose" {
		t.Errorf("Runtime.Default.Type = %q, want compose", cfg.Runtime.Default.Type)
	}
	if cfg.Runtime.Default.ComposeDir != "/srv/bahia/default" {
		t.Errorf("Runtime.Default.ComposeDir = %q", cfg.Runtime.Default.ComposeDir)
	}
	prod, ok := cfg.Runtime.Environments["production"]
	if !ok {
		t.Fatalf("missing production environment target: %+v", cfg.Runtime.Environments)
	}
	if prod.DockerHost != "tcp://prod:2375" || prod.ComposeDir != "/srv/bahia/production" || prod.EndpointRef != "prod" {
		t.Errorf("production runtime target = %+v", prod)
	}
	if cfg.Runtime.Endpoints["prod"].DockerHost != "tcp://docker-prod:2376" {
		t.Errorf("runtime endpoint env config = %+v", cfg.Runtime.Endpoints)
	}
}

func TestLoadEnvOverridesDefaults(t *testing.T) {
	// Without env vars, defaults should apply.
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Server.Port != 8080 {
		t.Fatalf("default Server.Port = %d, want 8080", cfg.Server.Port)
	}

	// With an env var, it should override the default.
	t.Setenv("BAHIA_SERVER_PORT", "4444")
	cfg, err = Load("")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Server.Port != 4444 {
		t.Errorf("Server.Port = %d, want %d after env override", cfg.Server.Port, 4444)
	}
}

func TestServerAddress(t *testing.T) {
	cfg := &Config{
		Server: ServerConfig{
			Host: "127.0.0.1",
			Port: 9090,
		},
	}

	expected := "127.0.0.1:9090"
	if got := cfg.ServerAddress(); got != expected {
		t.Errorf("expected address %s, got %s", expected, got)
	}
}

func TestAuthBootstrapOwnerPubkeysNormalization(t *testing.T) {
	cfg := Defaults()
	pkAUpper := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	pkALower := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	pkBLower := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	cfg.Auth.BootstrapOwnerPubkeys = []string{"  " + pkAUpper + "  ", pkALower, pkBLower, "   "}

	if err := cfg.validate(); err != nil {
		t.Fatalf("validate() error = %v", err)
	}

	if len(cfg.Auth.BootstrapOwnerPubkeys) != 2 {
		t.Fatalf("len(BootstrapOwnerPubkeys) = %d, want 2", len(cfg.Auth.BootstrapOwnerPubkeys))
	}
	if cfg.Auth.BootstrapOwnerPubkeys[0] != pkALower {
		t.Fatalf("first bootstrap owner = %q, want %q", cfg.Auth.BootstrapOwnerPubkeys[0], pkALower)
	}
	if cfg.Auth.BootstrapOwnerPubkeys[1] != pkBLower {
		t.Fatalf("second bootstrap owner = %q, want %q", cfg.Auth.BootstrapOwnerPubkeys[1], pkBLower)
	}
}

func TestAuthBootstrapOwnerPubkeysValidation(t *testing.T) {
	cfg := Defaults()
	cfg.Auth.BootstrapOwnerPubkeys = []string{"not-hex"}

	err := cfg.validate()
	if err == nil || !strings.Contains(err.Error(), "auth.bootstrap_owner_pubkeys") {
		t.Fatalf("validate() error = %v, want auth.bootstrap_owner_pubkeys validation error", err)
	}
}

func TestAuthBootstrapOwnerPubkeysEmptyListIsAllowed(t *testing.T) {
	cfg := Defaults()
	cfg.Auth.BootstrapOwnerPubkeys = []string{"   "}

	if err := cfg.validate(); err != nil {
		t.Fatalf("validate() error = %v", err)
	}
	if len(cfg.Auth.BootstrapOwnerPubkeys) != 0 {
		t.Fatalf("BootstrapOwnerPubkeys = %#v, want empty", cfg.Auth.BootstrapOwnerPubkeys)
	}
}

func TestPrivilegedFeatureValidationRequiresAuthAndOperatorAllowlists(t *testing.T) {
	adoptionNoAuth := Defaults()
	adoptionNoAuth.Adoption.Enabled = true
	if err := adoptionNoAuth.validate(); err == nil || !strings.Contains(err.Error(), "auth.enabled=true is required when adoption.enabled=true") {
		t.Fatalf("adoption without auth error = %v", err)
	}

	adoptionNoAllowlist := Defaults()
	adoptionNoAllowlist.Auth.Enabled = true
	adoptionNoAllowlist.Adoption.Enabled = true
	if err := adoptionNoAllowlist.validate(); err == nil || !strings.Contains(err.Error(), "adoption operator allowlist is required") {
		t.Fatalf("adoption without allowlist error = %v", err)
	}

	adoptionAllowed := Defaults()
	adoptionAllowed.Nostr.PrivateKey = "test-secret-key"
	adoptionAllowed.Auth.Enabled = true
	adoptionAllowed.Adoption.Enabled = true
	adoptionAllowed.Adoption.AllowedSubjects = []string{"ops"}
	if err := adoptionAllowed.validate(); err != nil {
		t.Fatalf("adoption with auth and allowlist should validate: %v", err)
	}

	directNoAuth := Defaults()
	directNoAuth.DirectRuntime.Enabled = true
	if err := directNoAuth.validate(); err == nil || !strings.Contains(err.Error(), "auth.enabled=true is required when direct_runtime_actions.enabled=true") {
		t.Fatalf("direct runtime without auth error = %v", err)
	}

	directNoAllowlist := Defaults()
	directNoAllowlist.Auth.Enabled = true
	directNoAllowlist.DirectRuntime.Enabled = true
	if err := directNoAllowlist.validate(); err == nil || !strings.Contains(err.Error(), "direct_runtime_actions operator allowlist is required") {
		t.Fatalf("direct runtime without allowlist error = %v", err)
	}

	directAllowed := Defaults()
	directAllowed.Nostr.PrivateKey = "test-secret-key"
	directAllowed.Auth.Enabled = true
	directAllowed.DirectRuntime.Enabled = true
	directAllowed.DirectRuntime.AllowedPubkeys = []string{"0123456789abcdef"}
	if err := directAllowed.validate(); err != nil {
		t.Fatalf("direct runtime with auth and allowlist should validate: %v", err)
	}

	llmOperationalNoAuth := Defaults()
	llmOperationalNoAuth.LLM.Enabled = true
	llmOperationalNoAuth.LLM.AllowOperationalREST = true
	if err := llmOperationalNoAuth.validate(); err == nil || !strings.Contains(err.Error(), "auth.enabled=true is required when llm.allow_operational_rest=true") {
		t.Fatalf("llm operational REST without auth error = %v", err)
	}

	llmOperationalNoAllowlist := Defaults()
	llmOperationalNoAllowlist.Auth.Enabled = true
	llmOperationalNoAllowlist.LLM.Enabled = true
	llmOperationalNoAllowlist.LLM.AllowOperationalREST = true
	if err := llmOperationalNoAllowlist.validate(); err == nil || !strings.Contains(err.Error(), "llm operator allowlist is required") {
		t.Fatalf("llm operational REST without allowlist error = %v", err)
	}

	llmOperationalNoLLM := Defaults()
	llmOperationalNoLLM.Auth.Enabled = true
	llmOperationalNoLLM.LLM.AllowOperationalREST = true
	llmOperationalNoLLM.LLM.AllowedPubkeys = []string{"0123456789abcdef"}
	if err := llmOperationalNoLLM.validate(); err == nil || !strings.Contains(err.Error(), "llm.enabled=true is required") {
		t.Fatalf("llm operational REST without llm enabled error = %v", err)
	}

	llmOperationalAllowed := Defaults()
	llmOperationalAllowed.Auth.Enabled = true
	llmOperationalAllowed.LLM.Enabled = true
	llmOperationalAllowed.LLM.AllowOperationalREST = true
	llmOperationalAllowed.LLM.AllowedPubkeys = []string{"0123456789abcdef"}
	if err := llmOperationalAllowed.validate(); err != nil {
		t.Fatalf("llm operational REST with auth and allowlist should validate: %v", err)
	}
}

func TestRuntimeEndpointValidationRequiresKnownRefsAndCompleteTLS(t *testing.T) {
	missingHost := Defaults()
	missingHost.Runtime.Endpoints["prod-docker"] = RuntimeEndpointConfig{ClientCertFile: "/cert.pem", ClientKeyFile: "/key.pem"}
	if err := missingHost.validate(); err == nil || !strings.Contains(err.Error(), `runtime endpoint "prod-docker" requires docker_host`) {
		t.Fatalf("missing docker_host error = %v", err)
	}

	missingKey := Defaults()
	missingKey.Runtime.Endpoints["prod-docker"] = RuntimeEndpointConfig{DockerHost: "tcp://docker.example:2376", ClientCertFile: "/cert.pem"}
	if err := missingKey.validate(); err == nil || !strings.Contains(err.Error(), "requires both client_cert_file and client_key_file") {
		t.Fatalf("incomplete TLS pair error = %v", err)
	}

	unknownDefaultRef := Defaults()
	unknownDefaultRef.Runtime.Default.EndpointRef = "missing"
	if err := unknownDefaultRef.validate(); err == nil || !strings.Contains(err.Error(), `runtime.default.endpoint_ref "missing" is not configured`) {
		t.Fatalf("unknown default endpoint_ref error = %v", err)
	}

	unknownEnvRef := Defaults()
	unknownEnvRef.Runtime.Environments["production"] = RuntimeTargetConfig{EndpointRef: "missing"}
	if err := unknownEnvRef.validate(); err == nil || !strings.Contains(err.Error(), `runtime.environments.production.endpoint_ref "missing" is not configured`) {
		t.Fatalf("unknown environment endpoint_ref error = %v", err)
	}

	valid := Defaults()
	valid.Runtime.Endpoints["prod-docker"] = RuntimeEndpointConfig{
		DockerHost:     "tcp://docker.example:2376",
		CACertFile:     "/ca.pem",
		ClientCertFile: "/cert.pem",
		ClientKeyFile:  "/key.pem",
	}
	valid.Runtime.Default.EndpointRef = "prod-docker"
	valid.Runtime.Environments["production"] = RuntimeTargetConfig{EndpointRef: "prod-docker"}
	if err := valid.validate(); err != nil {
		t.Fatalf("valid endpoint refs and TLS pair should validate: %v", err)
	}
}

func TestDNSDefaults(t *testing.T) {
	cfg := Defaults()
	if cfg.DNS.Enabled {
		t.Fatal("expected DNS disabled by default")
	}
	if cfg.DNS.DefaultTTL != 300 {
		t.Fatalf("DNS DefaultTTL = %d, want 300", cfg.DNS.DefaultTTL)
	}
	if cfg.DNS.ReconcileInterval != 30*time.Second {
		t.Fatalf("DNS ReconcileInterval = %s, want 30s", cfg.DNS.ReconcileInterval)
	}
	if !cfg.DNS.Projection.Services || !cfg.DNS.Projection.LLMRoutes || !cfg.DNS.Projection.MLEndpoints || !cfg.DNS.Projection.Workers {
		t.Fatal("expected all DNS projection sources enabled by default")
	}
}

func TestDNSValidationDisabledSkipsDNSRules(t *testing.T) {
	cfg := Defaults()
	cfg.DNS.Enabled = false
	cfg.DNS.DefaultTTL = 0
	cfg.DNS.ReconcileInterval = 0
	cfg.DNS.Zones = []DNSZoneConfig{{Name: "prod.cascadia", Visibility: "invalid", Backend: "missing"}}
	if err := cfg.validate(); err != nil {
		t.Fatalf("DNS disabled should skip DNS validation, got %v", err)
	}
}

func TestDNSValidationEnabled(t *testing.T) {
	validDNSConfig := func() *Config {
		cfg := Defaults()
		cfg.DNS.Enabled = true
		cfg.DNS.Backends = map[string]DNSBackendConfig{
			"fs": {Type: "filesystem", RootDir: t.TempDir()},
		}
		cfg.DNS.Zones = []DNSZoneConfig{
			{Name: "prod.cascadia", Visibility: "internal", Backend: "fs", TTL: 300},
			{Name: "edge.cascadia", Visibility: "edge", Backend: "fs", TTL: 60},
		}
		cfg.DNS.Projection.EnvironmentZones = map[string]string{"prod": "prod.cascadia"}
		cfg.DNS.Projection.WorkerZone = "edge.cascadia"
		return cfg
	}

	t.Run("valid", func(t *testing.T) {
		if err := validDNSConfig().validate(); err != nil {
			t.Fatalf("valid DNS config rejected: %v", err)
		}
	})

	t.Run("default ttl required", func(t *testing.T) {
		cfg := validDNSConfig()
		cfg.DNS.DefaultTTL = 0
		if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "dns.default_ttl") {
			t.Fatalf("expected dns.default_ttl error, got %v", err)
		}
	})

	t.Run("reconcile interval required", func(t *testing.T) {
		cfg := validDNSConfig()
		cfg.DNS.ReconcileInterval = 0
		if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "dns.reconcile_interval") {
			t.Fatalf("expected dns.reconcile_interval error, got %v", err)
		}
	})

	t.Run("duplicate zone names", func(t *testing.T) {
		cfg := validDNSConfig()
		cfg.DNS.Zones[1].Name = "prod.cascadia"
		if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "duplicated") {
			t.Fatalf("expected duplicated zone error, got %v", err)
		}
	})

	t.Run("invalid visibility", func(t *testing.T) {
		cfg := validDNSConfig()
		cfg.DNS.Zones[0].Visibility = "private"
		if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "visibility") {
			t.Fatalf("expected visibility error, got %v", err)
		}
	})

	t.Run("zone backend must exist", func(t *testing.T) {
		cfg := validDNSConfig()
		cfg.DNS.Zones[0].Backend = "missing"
		if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "not configured") {
			t.Fatalf("expected missing backend error, got %v", err)
		}
	})

	t.Run("filesystem requires root dir", func(t *testing.T) {
		cfg := validDNSConfig()
		cfg.DNS.Backends["fs"] = DNSBackendConfig{Type: "filesystem"}
		if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "root_dir") {
			t.Fatalf("expected root_dir error, got %v", err)
		}
	})

	t.Run("unsupported backend type", func(t *testing.T) {
		cfg := validDNSConfig()
		cfg.DNS.Backends["fs"] = DNSBackendConfig{Type: "magical"}
		if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "unsupported") {
			t.Fatalf("expected unsupported backend error, got %v", err)
		}
	})

	t.Run("fips backend defaults hosts path", func(t *testing.T) {
		cfg := validDNSConfig()
		cfg.DNS.Backends["fs"] = DNSBackendConfig{Type: "fips"}
		if err := cfg.validate(); err != nil {
			t.Fatalf("valid fips DNS backend rejected: %v", err)
		}
		if got := cfg.DNS.Backends["fs"].HostsPath; got != "/etc/fips/hosts" {
			t.Fatalf("fips hosts path = %q, want /etc/fips/hosts", got)
		}
	})

	t.Run("environment zones required", func(t *testing.T) {
		cfg := validDNSConfig()
		cfg.DNS.Projection.EnvironmentZones = map[string]string{}
		if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "environment_zones") {
			t.Fatalf("expected environment_zones error, got %v", err)
		}
	})

	t.Run("environment zones reference configured zones", func(t *testing.T) {
		cfg := validDNSConfig()
		cfg.DNS.Projection.EnvironmentZones = map[string]string{"prod": "missing.cascadia"}
		if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "unknown zone") {
			t.Fatalf("expected unknown zone error, got %v", err)
		}
	})

	t.Run("worker zone required", func(t *testing.T) {
		cfg := validDNSConfig()
		cfg.DNS.Projection.WorkerZone = ""
		if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "worker_zone") {
			t.Fatalf("expected worker_zone error, got %v", err)
		}
	})
}
