package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/pkg/client"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

var newCLIReadPool = func(ctx context.Context, relays []string) (client.SubscriptionPool, func(), error) {
	pool := nostrpool.NewRelayPool(relays, zap.NewNop())
	pool.Connect(ctx)
	return client.WrapRelayPool(pool), pool.Close, nil
}

func readNostrEvents(cmd *cobra.Command, domainName string, legacyKind int) ([]nostr.Event, error) {
	if strings.TrimSpace(operatorServicePubkey) == "" {
		return nil, fmt.Errorf("--service-pubkey or BAHIA_NOSTR_SERVICE_PUBKEY is required for Nostr reads")
	}
	relays, err := resolveOperatorRelays(cmd)
	if err != nil {
		return nil, fmt.Errorf("resolve relays: %w", err)
	}
	storePath, err := nostrServiceStorePath(operatorServicePubkey)
	if err != nil {
		return nil, err
	}
	timeout, err := readEOSETimeout(cmd)
	if err != nil {
		return nil, err
	}
	pool, closePool, err := newCLIReadPool(cmd.Context(), relays)
	if err != nil {
		return nil, fmt.Errorf("connect to relays: %w", err)
	}
	defer closePool()
	nc, err := client.NewNostrClient(client.NostrClientConfig{
		StorePath: storePath, ServicePubkey: operatorServicePubkey, Pool: pool, EOSETimeout: timeout,
	})
	if err != nil {
		return nil, fmt.Errorf("create Nostr client: %w", err)
	}
	defer nc.Close()
	events, result, err := nc.SyncAndQueryFamily(cmd.Context(), legacyKind)
	if err != nil {
		return nil, fmt.Errorf("sync %s state: %w", domainName, err)
	}
	if !result.Fresh {
		fmt.Fprintln(cmd.ErrOrStderr(), "warning: relay data may be stale (no EOSE within timeout)")
	}
	return events, nil
}

func runServicesListNostr(cmd *cobra.Command) error {
	events, err := readNostrEvents(cmd, "service", kinds.ServiceRegistry)
	if err != nil {
		return err
	}
	var services []domain.Service
	for _, ev := range events {
		svc, err := client.DecodeService(ev)
		if err != nil {
			return fmt.Errorf("decode service event %s: %w", ev.GetID(), err)
		}
		if svc == nil {
			continue
		}
		services = append(services, *svc)
	}
	sort.Slice(services, func(i, j int) bool { return services[i].Name < services[j].Name })
	return renderServices(services)
}

func runServiceGetNostr(cmd *cobra.Command, id string) error {
	want, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid service ID %q: %w", id, err)
	}
	events, err := readNostrEvents(cmd, "service", kinds.ServiceRegistry)
	if err != nil {
		return err
	}
	for _, ev := range events {
		svc, err := client.DecodeService(ev)
		if err != nil {
			return fmt.Errorf("decode service event %s: %w", ev.GetID(), err)
		}
		if svc != nil && svc.ID == want {
			return outputSingle(svc)
		}
	}
	return fmt.Errorf("service %s not found", id)
}

func renderServices(services []domain.Service) error {
	return output(services, []string{"ID", "NAME", "ARTIFACT_REPO", "RUNTIME"}, func(s domain.Service) []string {
		return []string{s.ID.String(), s.Name, s.ArtifactRepo, string(s.RuntimeType)}
	})
}

// nostrServiceStorePath returns the local store path for a given service
// pubkey. A full pubkey namespace prevents cross-service cache contamination.
func nostrServiceStorePath(servicePubkey string) (string, error) {
	pubkey, err := nostr.PubKeyFromHex(servicePubkey)
	if err != nil {
		return "", fmt.Errorf("invalid service pubkey: %w", err)
	}
	base := os.Getenv("BAHIA_DATA_DIR")
	if base == "" {
		if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
			base = filepath.Join(xdg, "bahia")
		} else {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("determine home directory: %w", err)
			}
			base = filepath.Join(home, ".local", "share", "bahia")
		}
	}
	dir := filepath.Join(base, "store", pubkey.Hex())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create store directory: %w", err)
	}
	return filepath.Join(dir, "events.db"), nil
}
