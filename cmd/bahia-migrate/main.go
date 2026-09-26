// Command bahia-migrate performs explicit database migration operations without
// starting the Bahia server.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/db"
	"go.uber.org/zap"
)

func main() {
	os.Exit(mainExit())
}

func mainExit() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return run(ctx, os.Args[1:], os.Stdout, os.Stderr)
}

// reportError preserves the failing exit status even if stderr is unavailable.
func reportError(stderr io.Writer, format string, args ...any) int {
	if _, err := fmt.Fprintf(stderr, format+"\n", args...); err != nil {
		return 1
	}
	return 1
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("bahia-migrate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "config.yaml", "Bahia configuration file")
	confirm := flags.Bool("confirm", false, "confirm destructive down migration")
	force := flags.Bool("force", false, "allow down across out-of-order applied history")
	to := flags.String("to", "", "full filename stem to retain when running down")
	action := ""
	if len(args) > 0 && (args[0] == "status" || args[0] == "up" || args[0] == "down") {
		action, args = args[0], args[1:]
	}
	if err := flags.Parse(args); err != nil {
		return 1
	}
	if action == "" && flags.NArg() == 1 {
		action = flags.Arg(0)
	} else if flags.NArg() != 0 {
		return reportError(stderr, "usage: bahia-migrate [--config path] [--confirm] [--force] [--to stem] status|up|down")
	}
	if action != "status" && action != "up" && action != "down" {
		return reportError(stderr, "unknown migration action %q", action)
	}
	if action != "down" && (*confirm || *force || *to != "") {
		return reportError(stderr, "--confirm, --force and --to are only valid for down")
	}
	if action == "down" && !*confirm {
		return reportError(stderr, "down requires --confirm")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return reportError(stderr, "loading Bahia config: %v", err)
	}
	pool, err := db.Connect(ctx, cfg.DB, zap.NewNop())
	if err != nil {
		return reportError(stderr, "%v", cfg.DB.RedactError(err))
	}
	defer pool.Close()
	return runAction(ctx, pool, action, db.DownOptions{To: *to, Confirm: *confirm, Force: *force}, stdout, stderr)
}

func runAction(ctx context.Context, pool *pgxpool.Pool, action string, down db.DownOptions, stdout, stderr io.Writer) int {
	logger := zap.NewNop()
	switch action {
	case "status":
		status, err := db.Status(ctx, pool, logger)
		if err != nil {
			return reportError(stderr, "%v", err)
		}
		for _, item := range status.Applied {
			if _, err := fmt.Fprintf(stdout, "applied\t%s\t%s\n", item.Version, item.AppliedAt.Format("2006-01-02T15:04:05.999999999Z07:00")); err != nil {
				return reportError(stderr, "writing status: %v", err)
			}
		}
		for _, version := range status.Pending {
			if _, err := fmt.Fprintf(stdout, "pending\t%s\n", version); err != nil {
				return reportError(stderr, "writing status: %v", err)
			}
		}
		if len(status.Pending) != 0 {
			return 2
		}
		return 0
	case "up":
		if err := db.Migrate(ctx, pool, logger); err != nil {
			return reportError(stderr, "%v", err)
		}
		if _, err := fmt.Fprintln(stdout, "migrations up to date"); err != nil {
			return reportError(stderr, "writing result: %v", err)
		}
		return 0
	case "down":
		versions, err := db.Down(ctx, pool, logger, down)
		for _, version := range versions {
			if _, writeErr := fmt.Fprintf(stdout, "rolled back\t%s\n", version); writeErr != nil {
				return reportError(stderr, "writing result: %v", writeErr)
			}
		}
		if err != nil {
			return reportError(stderr, "%v", err)
		}
		if len(versions) == 0 {
			if _, err := fmt.Fprintln(stdout, "target already current; no migrations rolled back"); err != nil {
				return reportError(stderr, "writing result: %v", err)
			}
		}
		return 0
	default:
		return reportError(stderr, "invalid migration action")
	}
}
