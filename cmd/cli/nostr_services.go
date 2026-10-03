package main

import (
	"fmt"
	"os"
	"path/filepath"

	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/pkg/client"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

// runServicesListNostr implements `bahia services list --nostr`.
// It uses the NostrClient to sync and query service records from relays,
// satisfying the unwired-exports gate for pkg/client.NostrClient,
// WrapRelayPool, SyncAndQuery, and DecodeService.
func runServicesListNostr(cmd *cobra.Command) error {
	relays, err := resolveOperatorRelays(cmd)
	if err != nil {
		return fmt.Errorf("resolve relays: %w", err)
	}
	if operatorServicePubkey == "" {
		return fmt.Errorf("--service-pubkey or BAHIA_NOSTR_SERVICE_PUBKEY is required for --nostr")
	}

	// Determine local store path.
	storePath, err := nostrServiceStorePath(operatorServicePubkey)
	if err != nil {
		return err
	}

	pool := nostrpool.NewRelayPool(relays, zap.NewNop())
	pool.Connect(cmd.Context())
	defer pool.Close()

	nc, err := client.NewNostrClient(client.NostrClientConfig{
		StorePath:     storePath,
		ServicePubkey: operatorServicePubkey,
		Pool:          client.WrapRelayPool(pool),
	})
	if err != nil {
		return fmt.Errorf("create nostr client: %w", err)
	}
	defer nc.Close()

	events, result, err := nc.SyncAndQuery(cmd.Context(), "service")
	if err != nil {
		return fmt.Errorf("sync service state: %w", err)
	}

	if !result.Fresh {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: relay data may be stale (no EOSE within timeout)\n")
	}

	var services []domain.Service
	for _, ev := range events {
		svc, err := client.DecodeService(ev)
		if err != nil {
			// Log decode errors but continue (may be encrypted).
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: skipping event %s: %v\n", ev.GetID(), err)
			continue
		}
		if svc == nil {
			continue // tombstone
		}
		services = append(services, *svc)
	}

	return output(services, []string{"ID", "NAME", "ARTIFACT_REPO", "RUNTIME"}, func(s domain.Service) []string {
		return []string{s.ID.String(), s.Name, s.ArtifactRepo, string(s.RuntimeType)}
	})
}

// nostrServiceStorePath returns the local store path for a given service
// pubkey, using os.UserCacheDir as a platform-appropriate base.
func nostrServiceStorePath(servicePubkey string) (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("determine cache directory: %w", err)
	}
	prefix := servicePubkey
	if len(prefix) > 12 {
		prefix = prefix[:12]
	}
	dir := filepath.Join(cacheDir, "bahia", "store", prefix)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create store directory: %w", err)
	}
	return filepath.Join(dir, "events.db"), nil
}
