// Command bahia-deploy-edge resolves and applies the edge Compose project in memory.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	"go.uber.org/zap"
)

const (
	receiptSchema = "cascadia.bahia.edge-compose-project.v1"
	releaseRoot   = "/srv/data/bahia-controlplane/releases"
)

var (
	tagPattern    = regexp.MustCompile(`^github-[0-9a-f]{7}$`)
	digestPattern = regexp.MustCompile(`^(?:[a-z0-9./_-]+@)?sha256:[0-9a-f]{64}$`)
)

type projectSDK interface {
	LoadProjectFiles(context.Context, ...string) (*types.Project, error)
	ApplyProject(context.Context, *types.Project, ...string) error
	Close() error
}

type receipt struct {
	Schema              string                  `json:"schema"`
	BaseFile            string                  `json:"base_file"`
	Project             *types.Project          `json:"project"`
	WorkingDir          string                  `json:"working_dir"`
	ComposeFiles        []string                `json:"compose_files"`
	Environment         types.Mapping           `json:"environment"`
	ServiceCustomLabels map[string]types.Labels `json:"service_custom_labels"`
	NetworkCustomLabels map[string]types.Labels `json:"network_custom_labels,omitempty"`
	VolumeCustomLabels  map[string]types.Labels `json:"volume_custom_labels,omitempty"`
}

type deployOptions struct {
	baseFile, legacyOverride, sourceReceipt, receiptPath      string
	currentReceipt                                            string
	tag, releaseDir, backendImage, webImage, safeBackendImage string
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stderr, nil); err != nil {
		fmt.Fprintln(os.Stderr, "bahia-deploy-edge:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stderr io.Writer, injected projectSDK) error {
	if len(args) == 0 {
		return errors.New("expected preflight, apply, snapshot, or rollback")
	}
	mode := args[0]
	if mode != "preflight" && mode != "apply" && mode != "snapshot" && mode != "rollback" {
		return fmt.Errorf("unknown command %q", mode)
	}
	flags := flag.NewFlagSet(mode, flag.ContinueOnError)
	flags.SetOutput(stderr)
	var opts deployOptions
	flags.StringVar(&opts.baseFile, "base-file", "", "absolute host base Compose path")
	flags.StringVar(&opts.legacyOverride, "legacy-override", "", "existing override for one-time snapshot only")
	flags.StringVar(&opts.sourceReceipt, "source-receipt", "", "existing applied-project receipt for snapshot")
	flags.StringVar(&opts.receiptPath, "receipt", "", "canonical JSON receipt path")
	flags.StringVar(&opts.currentReceipt, "current-receipt", "", "current applied-project receipt updated after rollback")
	flags.StringVar(&opts.tag, "tag", "", "github-<7 lowercase hex> release tag")
	flags.StringVar(&opts.releaseDir, "release-dir", "", "release directory")
	flags.StringVar(&opts.backendImage, "backend-image", "", "immutable Bahia image")
	flags.StringVar(&opts.webImage, "web-image", "", "immutable web image")
	flags.StringVar(&opts.safeBackendImage, "safe-backend-image", "", "admitted rollback Bahia image")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if mode == "rollback" {
		if opts.receiptPath == "" || (opts.currentReceipt != "" && filepath.Clean(opts.currentReceipt) == filepath.Clean(opts.receiptPath)) || opts.baseFile != "" || opts.legacyOverride != "" || opts.sourceReceipt != "" || opts.tag != "" || opts.releaseDir != "" || opts.backendImage != "" || opts.webImage != "" || opts.safeBackendImage != "" {
			return errors.New("rollback requires --receipt and optional distinct --current-receipt")
		}
	} else if mode == "snapshot" {
		if opts.receiptPath == "" || (opts.sourceReceipt != "" && filepath.Clean(opts.sourceReceipt) == filepath.Clean(opts.receiptPath)) || (opts.safeBackendImage != "" && !digestPattern.MatchString(opts.safeBackendImage)) || opts.currentReceipt != "" || opts.tag != "" || opts.releaseDir != "" || opts.backendImage != "" || opts.webImage != "" || (opts.sourceReceipt == "" && opts.baseFile == "") || (opts.sourceReceipt != "" && (opts.baseFile != "" || opts.legacyOverride != "")) {
			return errors.New("snapshot requires --receipt and either --source-receipt or --base-file")
		}
	} else if opts.baseFile == "" || opts.legacyOverride != "" || opts.sourceReceipt != "" || opts.safeBackendImage != "" || opts.currentReceipt != "" || (mode == "apply" && opts.receiptPath == "") || (mode == "preflight" && (opts.receiptPath != "" || opts.tag != "" || opts.releaseDir != "" || opts.backendImage != "" || opts.webImage != "")) {
		return fmt.Errorf("%s requires --base-file and valid mode-specific options", mode)
	}
	if injected == nil {
		base := opts.baseFile
		if base == "" && mode == "rollback" {
			base = filepath.Dir(opts.receiptPath)
		}
		injected = runtime.NewSDKComposeExecutor(runtime.NewComposeRuntime(filepath.Dir(base), zap.NewNop()), zap.NewNop())
	}
	defer func() { _ = injected.Close() }()

	var project *types.Project
	var baseFile string
	var err error
	switch mode {
	case "rollback":
		project, baseFile, err = readReceipt(opts.receiptPath)
	case "snapshot":
		if opts.sourceReceipt != "" {
			project, baseFile, err = readReceipt(opts.sourceReceipt)
		} else {
			baseFile, err = filepath.Abs(opts.baseFile)
			if err == nil {
				paths := []string{baseFile}
				if opts.legacyOverride != "" {
					paths = append(paths, opts.legacyOverride)
				}
				project, err = injected.LoadProjectFiles(ctx, paths...)
			}
		}
		if err == nil {
			err = seedWeb(project, os.Getenv)
		}
		if err == nil {
			err = requireEdgeProject(project)
		}
		if err == nil {
			if opts.safeBackendImage != "" {
				service := project.Services["bahia"]
				service.Image = opts.safeBackendImage
				project.Services["bahia"] = service
			}
			err = writeReceipt(opts.receiptPath, baseFile, project)
		}
	case "preflight", "apply":
		baseFile, err = filepath.Abs(opts.baseFile)
		if err == nil {
			project, err = injected.LoadProjectFiles(ctx, baseFile)
		}
		if err == nil {
			err = seedWeb(project, os.Getenv)
		}
		if err == nil {
			err = requireEdgeProject(project)
		}
		if err == nil && mode == "apply" {
			err = pinRelease(project, opts)
		}
	}
	if err != nil {
		return err
	}
	if mode == "snapshot" || mode == "preflight" {
		return nil
	}
	if err := injected.ApplyProject(ctx, project, "bahia", "web"); err != nil {
		return err
	}
	if mode == "apply" {
		return writeReceipt(opts.receiptPath, baseFile, project)
	}
	if opts.currentReceipt != "" {
		return writeReceipt(opts.currentReceipt, baseFile, project)
	}
	return nil
}

func seedWeb(project *types.Project, getenv func(string) string) error {
	if project == nil {
		return errors.New("nil Compose project")
	}
	web, ok := project.Services["web"]
	if !ok {
		return errors.New("missing expected services: web")
	}
	if web.Environment == nil {
		web.Environment = types.MappingWithEquals{}
	}
	for _, key := range []string{"PUBLIC_BAHIA_BOOTSTRAP_RELAYS", "PUBLIC_BAHIA_SERVICE_PUBKEYS", "PUBLIC_WHEELHOUSE_ALLOWED_PUBKEYS"} {
		value, exists := web.Environment[key]
		if !exists {
			seed := getenv(key)
			value = &seed
			web.Environment[key] = value
		}
		if key != "PUBLIC_WHEELHOUSE_ALLOWED_PUBKEYS" && (value == nil || strings.TrimSpace(*value) == "") {
			return fmt.Errorf("web.environment %s must be non-empty", key)
		}
	}
	project.Services["web"] = web
	return nil
}

func requireEdgeProject(project *types.Project) error {
	if project == nil || strings.TrimSpace(project.Name) == "" {
		return errors.New("missing resolved Compose project name")
	}
	for _, name := range []string{"bahia", "web"} {
		service, ok := project.Services[name]
		if !ok {
			return fmt.Errorf("missing expected services: %s", name)
		}
		if strings.TrimSpace(service.Image) == "" {
			return fmt.Errorf("missing image for service: %s", name)
		}
	}
	bahia := project.Services["bahia"]
	matches := 0
	for _, volume := range bahia.Volumes {
		if volume.Target != "/docs" {
			continue
		}
		matches++
		if volume.Type != "bind" || !volume.ReadOnly || !strings.HasPrefix(volume.Source, releaseRoot+"/") || !strings.HasSuffix(volume.Source, "/docs") {
			return fmt.Errorf("missing read-only release docs mount under %s", releaseRoot)
		}
	}
	if matches != 1 {
		return errors.New("expected exactly one release docs mount at bahia:/docs")
	}
	web := project.Services["web"]
	for _, key := range []string{"PUBLIC_BAHIA_BOOTSTRAP_RELAYS", "PUBLIC_BAHIA_SERVICE_PUBKEYS"} {
		value := web.Environment[key]
		if value == nil || strings.TrimSpace(*value) == "" {
			return fmt.Errorf("web.environment %s must be non-empty", key)
		}
	}
	return nil
}

func pinRelease(project *types.Project, opts deployOptions) error {
	if !tagPattern.MatchString(opts.tag) {
		return errors.New("tag must match github-<7 lowercase hex characters>")
	}
	if opts.releaseDir != releaseRoot+"/"+opts.tag {
		return fmt.Errorf("release directory must be %s/%s", releaseRoot, opts.tag)
	}
	for name, image := range map[string]string{"backend": opts.backendImage, "web": opts.webImage} {
		if !digestPattern.MatchString(image) {
			return fmt.Errorf("%s image must be an immutable sha256 image ID or repo@sha256 manifest reference", name)
		}
	}
	bahia := project.Services["bahia"]
	bahia.Image = opts.backendImage
	for i := range bahia.Volumes {
		if bahia.Volumes[i].Target == "/docs" {
			bahia.Volumes[i].Source = opts.releaseDir + "/docs"
		}
	}
	project.Services["bahia"] = bahia
	web := project.Services["web"]
	web.Image = opts.webImage
	project.Services["web"] = web
	return nil
}

func writeReceipt(path, baseFile string, project *types.Project) error {
	if !filepath.IsAbs(path) || !filepath.IsAbs(baseFile) || filepath.Clean(path) == filepath.Clean(baseFile) {
		return errors.New("receipt path must be absolute and differ from absolute base Compose path")
	}
	if err := requireEdgeProject(project); err != nil {
		return err
	}
	if project.WorkingDir != filepath.Dir(baseFile) || len(project.ComposeFiles) == 0 || project.ComposeFiles[0] != baseFile {
		return errors.New("resolved project has no matching base Compose provenance")
	}
	recorded := receipt{
		Schema: receiptSchema, BaseFile: baseFile, Project: project,
		WorkingDir: project.WorkingDir, ComposeFiles: project.ComposeFiles,
		Environment:         project.Environment,
		ServiceCustomLabels: make(map[string]types.Labels, len(project.Services)),
		NetworkCustomLabels: make(map[string]types.Labels, len(project.Networks)),
		VolumeCustomLabels:  make(map[string]types.Labels, len(project.Volumes)),
	}
	for name, service := range project.Services {
		recorded.ServiceCustomLabels[name] = service.CustomLabels
	}
	for name, network := range project.Networks {
		recorded.NetworkCustomLabels[name] = network.CustomLabels
	}
	for name, volume := range project.Volumes {
		recorded.VolumeCustomLabels[name] = volume.CustomLabels
	}
	data, err := json.MarshalIndent(recorded, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal receipt: %w", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".bahia-edge-receipt-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func readReceipt(path string) (*types.Project, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	var recorded receipt
	if err := json.Unmarshal(data, &recorded); err != nil {
		return nil, "", fmt.Errorf("decode receipt: %w", err)
	}
	if recorded.Schema != receiptSchema || !filepath.IsAbs(recorded.BaseFile) || recorded.Project == nil || recorded.WorkingDir != filepath.Dir(recorded.BaseFile) || len(recorded.ComposeFiles) == 0 || recorded.ComposeFiles[0] != recorded.BaseFile {
		return nil, "", errors.New("invalid edge project receipt schema or base path")
	}
	project := recorded.Project
	project.WorkingDir = recorded.WorkingDir
	project.ComposeFiles = recorded.ComposeFiles
	project.Environment = recorded.Environment
	for name, service := range project.Services {
		service.Name = name
		labels, ok := recorded.ServiceCustomLabels[name]
		if !ok {
			return nil, "", fmt.Errorf("receipt missing SDK labels for service %q", name)
		}
		service.CustomLabels = labels
		project.Services[name] = service
	}
	for name, network := range project.Networks {
		network.CustomLabels = recorded.NetworkCustomLabels[name]
		project.Networks[name] = network
	}
	for name, volume := range project.Volumes {
		volume.CustomLabels = recorded.VolumeCustomLabels[name]
		project.Volumes[name] = volume
	}
	if err := requireEdgeProject(project); err != nil {
		return nil, "", err
	}
	return project, recorded.BaseFile, nil
}
