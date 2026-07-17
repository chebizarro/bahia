// Command composesdk is a throwaway prototype for bahia-k97wt: verify the
// official Docker Compose v5 Go SDK (github.com/docker/compose/v5) can be
// embedded in-process as a replacement for the CLI shell-out compose path.
//
// Usage:
//
//	go run ./prototype/composesdk -project-dir /path/to/compose/project [-up]
//
// Without -up it only loads the project and lists containers (read-only).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/flags"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/docker/compose/v5/pkg/compose"
)

func main() {
	projectDir := flag.String("project-dir", ".", "compose project directory")
	doUp := flag.Bool("up", false, "run Up (mutating) instead of read-only load+ps")
	flag.Parse()

	if err := run(*projectDir, *doUp); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(projectDir string, doUp bool) error {
	ctx := context.Background()

	dockerCli, err := command.NewDockerCli()
	if err != nil {
		return fmt.Errorf("new docker cli: %w", err)
	}
	if err := dockerCli.Initialize(flags.NewClientOptions()); err != nil {
		return fmt.Errorf("initialize docker cli: %w", err)
	}

	svc, err := compose.NewComposeService(dockerCli)
	if err != nil {
		return fmt.Errorf("new compose service: %w", err)
	}

	absDir, err := filepath.Abs(projectDir)
	if err != nil {
		return err
	}

	project, err := svc.LoadProject(ctx, api.ProjectLoadOptions{
		WorkingDir: absDir,
	})
	if err != nil {
		return fmt.Errorf("load project: %w", err)
	}

	fmt.Printf("loaded project %q with %d services:\n", project.Name, len(project.Services))
	for name := range project.Services {
		fmt.Printf("  - %s\n", name)
	}

	if doUp {
		if err := svc.Up(ctx, project, api.UpOptions{
			Create: api.CreateOptions{RemoveOrphans: true},
			Start:  api.StartOptions{Wait: false},
		}); err != nil {
			return fmt.Errorf("up: %w", err)
		}
		fmt.Println("up: ok")
	}

	containers, err := svc.Ps(ctx, project.Name, api.PsOptions{All: true})
	if err != nil {
		return fmt.Errorf("ps: %w", err)
	}
	fmt.Printf("ps: %d containers\n", len(containers))
	for _, c := range containers {
		fmt.Printf("  - %s (%s) state=%s\n", c.Name, c.Image, c.State)
	}
	return nil
}
