package app

import (
	"testing"

	"github.com/openagentsinc/bahia/internal/config"
)

func TestInternalRoutingConfigHashChangesWithCommandEnvironment(t *testing.T) {
	cfg := config.InternalRoutingConfig{
		Enabled:       true,
		Provider:      "nginx",
		IncludeDir:    "/etc/nginx/conf.d",
		FilePrefix:    "bahia-",
		TestCommand:   []string{"nginx", "-t"},
		ReloadCommand: []string{"nginx", "-s", "reload"},
		CommandEnv:    []string{"DOCKER_HOST=tcp://docker-a.example:2375"},
		CertFile:      "/certs/fullchain.pem",
		KeyFile:       "/certs/privkey.pem",
		Zones:         []string{"example.com"},
	}
	first := internalRoutingConfigHash(cfg)
	cfg.CommandEnv = []string{"DOCKER_HOST=tcp://docker-b.example:2375"}
	second := internalRoutingConfigHash(cfg)
	if first == second {
		t.Fatalf("internal routing config hash did not change with command_env: %q", first)
	}
}
