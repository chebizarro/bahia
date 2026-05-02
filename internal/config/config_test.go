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
	if cfg.Auth.NIP98Enabled {
		t.Error("expected NIP-98 auth disabled by default")
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
		"BAHIA_DB_HOST":               "envhost",
		"BAHIA_DB_PORT":               "9999",
		"BAHIA_DB_MAX_OPEN_CONNS":     "42",
		"BAHIA_SERVER_READ_TIMEOUT":   "5s",
		"BAHIA_SERVER_PORT":           "3000",
		"BAHIA_NOSTR_PUBLISH_ENABLED": "false",
		"BAHIA_RUNTIME_DOCKER_HOST":   "tcp://remote:2375",
		"BAHIA_AUTH_JWT_SECRET":       "supersecret",
		"BAHIA_LOG_LEVEL":             "debug",
		"BAHIA_RECONCILE_ENABLED":     "false",
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
	if cfg.Auth.JWTSecret != "supersecret" {
		t.Errorf("Auth.JWTSecret = %q, want %q", cfg.Auth.JWTSecret, "supersecret")
	}
	if cfg.Log.Level != "debug" {
		t.Errorf("Log.Level = %q, want %q", cfg.Log.Level, "debug")
	}
	if cfg.Reconcile.Enabled != false {
		t.Error("Reconcile.Enabled should be false")
	}
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
    docker_host: tcp://default:2375
    compose_dir: /srv/bahia/default
  environments:
    production:
      endpoint_ref: prod-docker
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
	prod := cfg.Runtime.Environments["production"]
	if prod.ComposeDir != "/srv/bahia/production" || prod.DockerHost != "tcp://prod:2375" || prod.EndpointRef != "prod-docker" {
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
		cfg.Auth.JWTSecret = "secret"
		cfg.Adoption.Enabled = true
		err := cfg.validate()
		if err == nil || !strings.Contains(err.Error(), "adoption operator allowlist") {
			t.Fatalf("validate error = %v, want adoption allowlist requirement", err)
		}
	})

	t.Run("direct runtime requires allowlist", func(t *testing.T) {
		cfg := Defaults()
		cfg.Auth.Enabled = true
		cfg.Auth.JWTSecret = "secret"
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
		cfg.Auth.JWTSecret = "secret"
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
		cfg.Auth.JWTSecret = "secret"
		cfg.LLM.AllowOperationalREST = true
		cfg.LLM.AllowedSubjects = []string{"ops"}
		err := cfg.validate()
		if err == nil || !strings.Contains(err.Error(), "llm.enabled=true") {
			t.Fatalf("validate error = %v, want llm enabled requirement", err)
		}
	})

	t.Run("enabled with auth and allowlists is valid", func(t *testing.T) {
		cfg := Defaults()
		cfg.Auth.Enabled = true
		cfg.Auth.JWTSecret = "secret"
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

	t.Run("nip98 can satisfy privileged auth method", func(t *testing.T) {
		cfg := Defaults()
		cfg.Auth.Enabled = true
		cfg.Auth.NIP98Enabled = true
		cfg.Adoption.Enabled = true
		cfg.Adoption.AllowedPubkeys = []string{"abcdef"}
		if err := cfg.validate(); err != nil {
			t.Fatalf("validate error = %v", err)
		}
	})
}

func TestLoadPrivilegedRouteConfigFromYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := []byte(`auth:
  enabled: true
  jwt_secret: test-secret
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

func TestPrivilegedFeatureValidationRequiresAuthAndOperatorAllowlists(t *testing.T) {
	adoptionNoAuth := Defaults()
	adoptionNoAuth.Adoption.Enabled = true
	if err := adoptionNoAuth.validate(); err == nil || !strings.Contains(err.Error(), "auth.enabled=true is required when adoption.enabled=true") {
		t.Fatalf("adoption without auth error = %v", err)
	}

	adoptionNoCredential := Defaults()
	adoptionNoCredential.Auth.Enabled = true
	adoptionNoCredential.Adoption.Enabled = true
	if err := adoptionNoCredential.validate(); err == nil || !strings.Contains(err.Error(), "auth.jwt_secret or auth.nip98_enabled=true is required when adoption.enabled=true") {
		t.Fatalf("adoption without auth credential error = %v", err)
	}

	adoptionNoAllowlist := Defaults()
	adoptionNoAllowlist.Auth.Enabled = true
	adoptionNoAllowlist.Auth.JWTSecret = "test-secret"
	adoptionNoAllowlist.Adoption.Enabled = true
	if err := adoptionNoAllowlist.validate(); err == nil || !strings.Contains(err.Error(), "adoption operator allowlist is required") {
		t.Fatalf("adoption without allowlist error = %v", err)
	}

	adoptionAllowed := Defaults()
	adoptionAllowed.Auth.Enabled = true
	adoptionAllowed.Auth.JWTSecret = "test-secret"
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
	directNoAllowlist.Auth.NIP98Enabled = true
	directNoAllowlist.DirectRuntime.Enabled = true
	if err := directNoAllowlist.validate(); err == nil || !strings.Contains(err.Error(), "direct_runtime_actions operator allowlist is required") {
		t.Fatalf("direct runtime without allowlist error = %v", err)
	}

	directAllowed := Defaults()
	directAllowed.Auth.Enabled = true
	directAllowed.Auth.NIP98Enabled = true
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
	llmOperationalNoAllowlist.Auth.NIP98Enabled = true
	llmOperationalNoAllowlist.LLM.Enabled = true
	llmOperationalNoAllowlist.LLM.AllowOperationalREST = true
	if err := llmOperationalNoAllowlist.validate(); err == nil || !strings.Contains(err.Error(), "llm operator allowlist is required") {
		t.Fatalf("llm operational REST without allowlist error = %v", err)
	}

	llmOperationalNoLLM := Defaults()
	llmOperationalNoLLM.Auth.Enabled = true
	llmOperationalNoLLM.Auth.NIP98Enabled = true
	llmOperationalNoLLM.LLM.AllowOperationalREST = true
	llmOperationalNoLLM.LLM.AllowedPubkeys = []string{"0123456789abcdef"}
	if err := llmOperationalNoLLM.validate(); err == nil || !strings.Contains(err.Error(), "llm.enabled=true is required") {
		t.Fatalf("llm operational REST without llm enabled error = %v", err)
	}

	llmOperationalAllowed := Defaults()
	llmOperationalAllowed.Auth.Enabled = true
	llmOperationalAllowed.Auth.NIP98Enabled = true
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
