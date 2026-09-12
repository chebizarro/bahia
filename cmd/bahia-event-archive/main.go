// Command bahia-event-archive is the explicit operator control plane for the
// two-phase PostgreSQL Nostr event archive. It never accepts a DSN on argv;
// database credentials are loaded through Bahia's normal protected config.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/db"
	"github.com/openagentsinc/bahia/internal/nostrarchive"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

type options struct {
	configPath    string
	action        string
	archiveDir    string
	batchID       string
	batchSize     int
	olderThan     time.Duration
	cutoff        string
	objectURI     string
	objectVersion string
	kinds         string
	allKinds      bool
}

func main() {
	var opts options
	flag.StringVar(&opts.configPath, "config", "", "path to Bahia config YAML")
	flag.StringVar(&opts.action, "action", "stats", "ensure-indexes, claim-export, confirm, prune, restore, or stats")
	flag.StringVar(&opts.archiveDir, "archive-dir", "/var/lib/bahia/nostr-archive", "absolute archive artifact directory")
	flag.StringVar(&opts.batchID, "batch-id", "", "archive batch UUID")
	flag.IntVar(&opts.batchSize, "batch-size", 5000, "maximum rows claimed or pruned per invocation")
	flag.DurationVar(&opts.olderThan, "older-than", 30*24*time.Hour, "claim rows received before now minus this duration")
	flag.StringVar(&opts.cutoff, "cutoff", "", "explicit RFC3339 claim cutoff (overrides --older-than)")
	flag.StringVar(&opts.objectURI, "object-uri", "", "protected non-file object URI")
	flag.StringVar(&opts.objectVersion, "object-version", "", "immutable protected object version")
	flag.StringVar(&opts.kinds, "kinds", "", "comma-separated event kinds selected for this batch")
	flag.BoolVar(&opts.allKinds, "all-kinds", false, "explicitly select all eligible kinds")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, opts); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, opts options) error {
	if strings.TrimSpace(opts.configPath) == "" {
		return errors.New("--config is required")
	}
	cfg, err := config.Load(opts.configPath)
	if err != nil {
		return fmt.Errorf("loading Bahia config: %w", err)
	}
	logger := zap.NewNop()
	pool, err := db.Connect(ctx, cfg.DB, logger)
	if err != nil {
		return err
	}
	defer pool.Close()
	repo := repository.NewPgNostrEventArchiveRepository(pool)

	switch opts.action {
	case "ensure-indexes":
		if err := repo.EnsureOnlineIndexes(ctx); err != nil {
			return err
		}
		return writeJSON(map[string]any{"status": "ready", "indexes": len(repository.NostrEventArchiveOnlineIndexStatements())})
	case "claim-export":
		cutoff, err := claimCutoff(opts)
		if err != nil {
			return err
		}
		kinds, err := claimKinds(opts)
		if err != nil {
			return err
		}
		batch, err := repo.ClaimArchiveBatch(ctx, cutoff, opts.batchSize, kinds)
		if err != nil {
			return err
		}
		if batch == nil {
			return writeJSON(map[string]any{"status": "empty", "cutoff_at": cutoff})
		}
		manager, err := nostrarchive.NewArtifactManager(repo, opts.archiveDir)
		if err != nil {
			return err
		}
		batch, err = manager.Export(ctx, batch.ID)
		if err != nil {
			return err
		}
		return writeJSON(batch)
	case "confirm":
		id, err := parseBatchID(opts.batchID)
		if err != nil {
			return err
		}
		manager, err := nostrarchive.NewArtifactManager(repo, opts.archiveDir)
		if err != nil {
			return err
		}
		if err := manager.ConfirmProtected(ctx, id, opts.objectURI, opts.objectVersion); err != nil {
			return err
		}
		batch, err := repo.GetArchiveBatch(ctx, id)
		if err != nil {
			return err
		}
		return writeJSON(batch)
	case "prune":
		id, err := parseBatchID(opts.batchID)
		if err != nil {
			return err
		}
		deleted, done, err := repo.PruneArchiveBatch(ctx, id, opts.batchSize)
		if err != nil {
			return err
		}
		return writeJSON(map[string]any{"batch_id": id, "deleted": deleted, "done": done})
	case "restore":
		id, err := parseBatchID(opts.batchID)
		if err != nil {
			return err
		}
		manager, err := nostrarchive.NewArtifactManager(repo, opts.archiveDir)
		if err != nil {
			return err
		}
		inserted, err := manager.Restore(ctx, id)
		if err != nil {
			return err
		}
		return writeJSON(map[string]any{"batch_id": id, "inserted": inserted})
	case "stats":
		stats, err := repo.StorageStats(ctx)
		if err != nil {
			return err
		}
		return writeJSON(stats)
	default:
		return fmt.Errorf("unknown --action %q", opts.action)
	}
}

func claimCutoff(opts options) (time.Time, error) {
	if strings.TrimSpace(opts.cutoff) != "" {
		cutoff, err := time.Parse(time.RFC3339, strings.TrimSpace(opts.cutoff))
		if err != nil {
			return time.Time{}, fmt.Errorf("parsing --cutoff: %w", err)
		}
		return cutoff.UTC(), nil
	}
	if opts.olderThan <= 0 {
		return time.Time{}, errors.New("--older-than must be positive")
	}
	return time.Now().UTC().Add(-opts.olderThan), nil
}

func parseBatchID(value string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid --batch-id: %w", err)
	}
	return id, nil
}

func claimKinds(opts options) ([]int, error) {
	if opts.allKinds && strings.TrimSpace(opts.kinds) != "" {
		return nil, errors.New("use either --kinds or --all-kinds, not both")
	}
	if opts.allKinds {
		return []int{}, nil
	}
	if strings.TrimSpace(opts.kinds) == "" {
		return nil, errors.New("claim-export requires an explicit --kinds list or --all-kinds")
	}
	parts := strings.Split(opts.kinds, ",")
	kinds := make([]int, 0, len(parts))
	seen := make(map[int]struct{}, len(parts))
	for _, part := range parts {
		var kind int
		if _, err := fmt.Sscanf(strings.TrimSpace(part), "%d", &kind); err != nil || kind < 0 {
			return nil, fmt.Errorf("invalid event kind %q", part)
		}
		if _, exists := seen[kind]; exists {
			continue
		}
		seen[kind] = struct{}{}
		kinds = append(kinds, kind)
	}
	return kinds, nil
}

func writeJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
