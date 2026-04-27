package config

import (
	"net/url"
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
		"BAHIA_DB_HOST":              "envhost",
		"BAHIA_DB_PORT":              "9999",
		"BAHIA_DB_MAX_OPEN_CONNS":    "42",
		"BAHIA_SERVER_READ_TIMEOUT":  "5s",
		"BAHIA_SERVER_PORT":          "3000",
		"BAHIA_NOSTR_PUBLISH_ENABLED": "false",
		"BAHIA_RUNTIME_DOCKER_HOST":  "tcp://remote:2375",
		"BAHIA_AUTH_JWT_SECRET":      "supersecret",
		"BAHIA_LOG_LEVEL":            "debug",
		"BAHIA_RECONCILE_ENABLED":    "false",
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
