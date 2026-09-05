package dns

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/openagentsinc/bahia/internal/dnsagent/engine"
	"github.com/openagentsinc/bahia/internal/domain"
)

const defaultDnsmasqFilePrefix = "bahia-"

type DnsmasqConfig struct {
	ConfigDir     string
	ReloadCommand string
	FilePrefix    string
}

type DnsmasqBackend struct {
	configDir       string
	reloadCommand   string
	filePrefix      string
	commandExecutor dnsmasqCommandExecutor
}

type dnsmasqCommandExecutor interface {
	Run(ctx context.Context, command string) error
}

type shellDnsmasqCommandExecutor struct{}

func NewDnsmasqBackend(cfg DnsmasqConfig) *DnsmasqBackend {
	prefix := strings.TrimSpace(cfg.FilePrefix)
	if prefix == "" {
		prefix = defaultDnsmasqFilePrefix
	}
	return &DnsmasqBackend{
		configDir:       strings.TrimSpace(cfg.ConfigDir),
		reloadCommand:   strings.TrimSpace(cfg.ReloadCommand),
		filePrefix:      prefix,
		commandExecutor: shellDnsmasqCommandExecutor{},
	}
}

func (b *DnsmasqBackend) BackendType() domain.DNSBackendType {
	return domain.DNSBackendTypeDNSMasq
}

func (b *DnsmasqBackend) Health(ctx context.Context) error {
	return b.engine().HealthCheck(ctx)
}

func (b *DnsmasqBackend) ListRecords(ctx context.Context, zone domain.DNSZone) ([]domain.DNSRecord, error) {
	return b.engine().ListZone(ctx, zone)
}

func (b *DnsmasqBackend) SyncZone(ctx context.Context, zone domain.DNSZone, records []domain.DNSRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := domain.ValidateDNSZone(&zone); err != nil {
		return err
	}
	configDir, err := b.requiredConfigDir()
	if err != nil {
		return err
	}
	if strings.TrimSpace(b.reloadCommand) == "" {
		return fmt.Errorf("DNS dnsmasq backend reload command is required")
	}
	info, err := os.Stat(configDir)
	if err != nil {
		return fmt.Errorf("DNS dnsmasq backend config dir %q is not accessible: %w", configDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("DNS dnsmasq backend config dir %q is not a directory", configDir)
	}
	return b.engine().ApplyZone(ctx, zone, records)
}

func (b *DnsmasqBackend) engine() *engine.Engine {
	if b == nil {
		return engine.New(engine.Config{})
	}
	return engine.New(engine.Config{
		IncludeDir: b.configDir,
		FilePrefix: b.filePrefix,
		Runner: func(ctx context.Context, argv []string) error {
			if len(argv) == 3 && argv[0] == "sh" && argv[1] == "-c" {
				return b.executor().Run(ctx, argv[2])
			}
			return fmt.Errorf("unexpected dnsmasq command argv: %q", argv)
		},
		Reload: engine.ReloadConfig{ExplicitCommand: b.reloadCommand},
	})
}

func (b *DnsmasqBackend) requiredConfigDir() (string, error) {
	if b == nil || strings.TrimSpace(b.configDir) == "" {
		return "", fmt.Errorf("DNS dnsmasq backend config dir is required")
	}
	return b.configDir, nil
}

func (b *DnsmasqBackend) zonePath(zoneName string) (string, error) {
	configDir, err := b.requiredConfigDir()
	if err != nil {
		return "", err
	}
	zoneName = strings.TrimSpace(zoneName)
	if zoneName == "" {
		return "", fmt.Errorf("DNS zone name is required")
	}
	prefix := b.filePrefix
	if strings.TrimSpace(prefix) == "" {
		prefix = defaultDnsmasqFilePrefix
	}
	return filepath.Join(configDir, prefix+engine.SanitizeFileName(zoneName)+".conf"), nil
}

func (b *DnsmasqBackend) executor() dnsmasqCommandExecutor {
	if b != nil && b.commandExecutor != nil {
		return b.commandExecutor
	}
	return shellDnsmasqCommandExecutor{}
}

func (shellDnsmasqCommandExecutor) Run(ctx context.Context, command string) error {
	command = strings.TrimSpace(command)
	if command == "" {
		return fmt.Errorf("reload command is required")
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			return err
		}
		return fmt.Errorf("%w: %s", err, message)
	}
	return nil
}

var _ Backend = (*DnsmasqBackend)(nil)
