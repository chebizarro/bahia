package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/openagentsinc/bahia/internal/fipsbridge"
)

func main() {
	configPath := flag.String("config", env("FIPS_BAHIA_BRIDGE_CONFIG", ""), "YAML config path containing bridge settings")
	bahiaPubkey := flag.String("bahia-pubkey", env("FIPS_BAHIA_BAHIA_PUBKEY", ""), "Bahia service pubkey as hex or npub")
	relays := flag.String("relays", env("FIPS_BAHIA_RELAYS", ""), "comma-separated Nostr relay URLs")
	hostsPath := flag.String("hosts-path", env("FIPS_BAHIA_HOSTS_PATH", ""), "FIPS hosts file path")
	marker := flag.String("managed-section-marker", env("FIPS_BAHIA_MANAGED_SECTION_MARKER", ""), "managed section marker")
	healthFilter := flag.Bool("health-filter", envBool("FIPS_BAHIA_HEALTH_FILTER", true), "only write healthy endpoints")
	capabilities := flag.String("capability-filter", env("FIPS_BAHIA_CAPABILITY_FILTER", ""), "comma-separated required endpoint capabilities")
	environments := flag.String("environment-filter", env("FIPS_BAHIA_ENVIRONMENT_FILTER", ""), "comma-separated endpoint environments")
	flag.Parse()
	visited := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { visited[f.Name] = true })

	cfg := fipsbridge.DefaultConfig()
	if strings.TrimSpace(*configPath) != "" {
		data, err := os.ReadFile(*configPath)
		if err != nil {
			fatalf("read config: %v", err)
		}
		loaded, err := fipsbridge.LoadConfig(data)
		if err != nil {
			fatalf("load config: %v", err)
		}
		cfg = loaded
	}
	if strings.TrimSpace(*bahiaPubkey) != "" {
		cfg.BahiaPubkey = *bahiaPubkey
	}
	if relayList := splitCSV(*relays); len(relayList) > 0 {
		cfg.RelayURLs = relayList
	}
	if strings.TrimSpace(*hostsPath) != "" {
		cfg.HostsPath = *hostsPath
	}
	if strings.TrimSpace(*marker) != "" {
		cfg.ManagedSectionMarker = *marker
	}
	if visited["health-filter"] || strings.TrimSpace(os.Getenv("FIPS_BAHIA_HEALTH_FILTER")) != "" {
		cfg.HealthFilter = *healthFilter
	}
	if capabilityList := splitCSV(*capabilities); len(capabilityList) > 0 {
		cfg.CapabilityFilter = capabilityList
	}
	if environmentList := splitCSV(*environments); len(environmentList) > 0 {
		cfg.EnvironmentFilter = environmentList
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	bridge, err := fipsbridge.NewBridge(cfg, logger)
	if err != nil {
		fatalf("configure bridge: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := bridge.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		fatalf("bridge stopped: %v", err)
	}
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch value {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func fatalf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
