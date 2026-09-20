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
	"github.com/openagentsinc/bahia/internal/strutil"
)

func main() {
	configPath := flag.String("config", strutil.Env("FIPS_BAHIA_BRIDGE_CONFIG", ""), "YAML config path containing bridge settings")
	bahiaPubkey := flag.String("bahia-pubkey", strutil.Env("FIPS_BAHIA_BAHIA_PUBKEY", ""), "Bahia service pubkey as hex or npub")
	relays := flag.String("relays", strutil.Env("FIPS_BAHIA_RELAYS", ""), "comma-separated Nostr relay URLs")
	hostsPath := flag.String("hosts-path", strutil.Env("FIPS_BAHIA_HOSTS_PATH", ""), "FIPS hosts file path")
	marker := flag.String("managed-section-marker", strutil.Env("FIPS_BAHIA_MANAGED_SECTION_MARKER", ""), "managed section marker")
	healthFilter := flag.Bool("health-filter", envBool("FIPS_BAHIA_HEALTH_FILTER", true), "only write healthy endpoints")
	capabilities := flag.String("capability-filter", strutil.Env("FIPS_BAHIA_CAPABILITY_FILTER", ""), "comma-separated required endpoint capabilities")
	environments := flag.String("environment-filter", strutil.Env("FIPS_BAHIA_ENVIRONMENT_FILTER", ""), "comma-separated endpoint environments")
	flag.Parse()
	visited := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { visited[f.Name] = true })

	cfg := fipsbridge.DefaultConfig()
	if strings.TrimSpace(*configPath) != "" {
		data, err := os.ReadFile(*configPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read config: %v\n", err)
			os.Exit(1)
		}
		loaded, err := fipsbridge.LoadConfig(data)
		if err != nil {
			fmt.Fprintf(os.Stderr, "load config: %v\n", err)
			os.Exit(1)
		}
		cfg = loaded
	}
	if strings.TrimSpace(*bahiaPubkey) != "" {
		cfg.BahiaPubkey = *bahiaPubkey
	}
	if relayList := strutil.SplitCSV(*relays); len(relayList) > 0 {
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
	if capabilityList := strutil.SplitCSV(*capabilities); len(capabilityList) > 0 {
		cfg.CapabilityFilter = capabilityList
	}
	if environmentList := strutil.SplitCSV(*environments); len(environmentList) > 0 {
		cfg.EnvironmentFilter = environmentList
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	bridge, err := fipsbridge.NewBridge(cfg, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "configure bridge: %v\n", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := bridge.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(os.Stderr, "bridge stopped: %v\n", err)
		os.Exit(1)
	}
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
