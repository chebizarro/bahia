package dns

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

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
	if err := ctx.Err(); err != nil {
		return err
	}
	configDir, err := b.requiredConfigDir()
	if err != nil {
		return err
	}
	info, err := os.Stat(configDir)
	if err != nil {
		return fmt.Errorf("DNS dnsmasq backend config dir %q is not accessible: %w", configDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("DNS dnsmasq backend config dir %q is not a directory", configDir)
	}
	file, err := os.CreateTemp(configDir, ".health-*.tmp")
	if err != nil {
		return fmt.Errorf("DNS dnsmasq backend config dir %q is not writable: %w", configDir, err)
	}
	name := file.Name()
	if closeErr := file.Close(); closeErr != nil {
		_ = os.Remove(name)
		return fmt.Errorf("DNS dnsmasq backend health temp file close failed: %w", closeErr)
	}
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("DNS dnsmasq backend health temp file cleanup failed: %w", err)
	}
	return nil
}

func (b *DnsmasqBackend) ListRecords(ctx context.Context, zone domain.DNSZone) ([]domain.DNSRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := domain.ValidateDNSZone(&zone); err != nil {
		return nil, err
	}
	path, err := b.zonePath(zone.Name)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return []domain.DNSRecord{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read dnsmasq zone config %q: %w", path, err)
	}
	defer file.Close()

	records := []domain.DNSRecord{}
	scanner := bufio.NewScanner(file)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		record, ok, err := parseDnsmasqDirective(zone, line)
		if err != nil {
			return nil, fmt.Errorf("parse dnsmasq zone config %q line %d: %w", path, lineNumber, err)
		}
		if ok {
			records = append(records, record)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan dnsmasq zone config %q: %w", path, err)
	}
	return sortedRecords(records), nil
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
	data, err := dnsmasqZoneConfig(zone, records)
	if err != nil {
		return err
	}
	path, err := b.zonePath(zone.Name)
	if err != nil {
		return err
	}
	previousData, previousMode, previousExists, err := readPreviousDnsmasqConfig(path)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(configDir, "."+dnsmasqSanitizeZoneFilename(zone.Name)+"-*.tmp")
	if err != nil {
		return fmt.Errorf("create dnsmasq zone config temp file: %w", err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write dnsmasq zone config temp file: %w", err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod dnsmasq zone config temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync dnsmasq zone config temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close dnsmasq zone config temp file: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace dnsmasq zone config %q: %w", path, err)
	}
	committed = true
	if err := b.executor().Run(ctx, b.reloadCommand); err != nil {
		reloadErr := fmt.Errorf("reload dnsmasq after syncing zone %q: %w", zone.Name, err)
		if rollbackErr := restoreDnsmasqConfig(path, previousData, previousMode, previousExists); rollbackErr != nil {
			return errors.Join(reloadErr, fmt.Errorf("restore previous dnsmasq config for zone %q: %w", zone.Name, rollbackErr))
		}
		if rollbackReloadErr := b.executor().Run(ctx, b.reloadCommand); rollbackReloadErr != nil {
			return errors.Join(reloadErr, fmt.Errorf("reload dnsmasq after restoring zone %q: %w", zone.Name, rollbackReloadErr))
		}
		return reloadErr
	}
	return nil
}

func readPreviousDnsmasqConfig(path string) ([]byte, os.FileMode, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0o644, false, nil
	}
	if err != nil {
		return nil, 0, false, fmt.Errorf("read previous dnsmasq zone config %q: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, 0, false, fmt.Errorf("stat previous dnsmasq zone config %q: %w", path, err)
	}
	return data, info.Mode().Perm(), true, nil
}

func restoreDnsmasqConfig(path string, data []byte, mode os.FileMode, existed bool) error {
	if !existed {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".dnsmasq-rollback-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) //nolint:errcheck
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
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
	return filepath.Join(configDir, prefix+dnsmasqSanitizeZoneFilename(zoneName)+".conf"), nil
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

func dnsmasqZoneConfig(zone domain.DNSZone, records []domain.DNSRecord) ([]byte, error) {
	var buffer bytes.Buffer
	buffer.WriteString("# Managed by Bahia. Manual changes may be replaced.\n")
	buffer.WriteString("# Zone: ")
	buffer.WriteString(zone.Name)
	buffer.WriteByte('\n')
	for _, record := range sortedRecords(records) {
		directive, err := dnsmasqDirective(zone, record)
		if err != nil {
			return nil, err
		}
		buffer.WriteString(directive)
		buffer.WriteByte('\n')
	}
	return buffer.Bytes(), nil
}

func dnsmasqDirective(zone domain.DNSZone, record domain.DNSRecord) (string, error) {
	record.Zone = strings.TrimSpace(record.Zone)
	if record.Zone == "" {
		record.Zone = zone.Name
	}
	if record.Zone != zone.Name {
		return "", fmt.Errorf("DNS record %q zone %q does not match sync zone %q", record.FQDN, record.Zone, zone.Name)
	}
	fqdn := strings.Trim(strings.ToLower(strings.TrimSpace(record.FQDN)), ".")
	if fqdn == "" {
		fqdn = fqdnFromRecordName(record.Name, zone.Name)
	}
	if fqdn == "" {
		return "", fmt.Errorf("DNS record FQDN is required")
	}
	if !strings.HasSuffix(fqdn, zone.Name) || (fqdn != zone.Name && !strings.HasSuffix(fqdn, "."+zone.Name)) {
		return "", fmt.Errorf("DNS record FQDN %q is outside zone %q", fqdn, zone.Name)
	}
	if !record.Type.IsValid() {
		return "", fmt.Errorf("DNS record %q type %q is not valid", fqdn, record.Type)
	}
	value := strings.Trim(strings.ToLower(strings.TrimSpace(record.Value)), ".")
	if value == "" {
		return "", fmt.Errorf("DNS record %q value is required", fqdn)
	}
	switch record.Type {
	case domain.DNSRecordTypeA, domain.DNSRecordTypeAAAA:
		return fmt.Sprintf("address=/%s/%s", fqdn, strings.TrimSpace(record.Value)), nil
	case domain.DNSRecordTypeCNAME:
		return fmt.Sprintf("cname=%s,%s", fqdn, value), nil
	case domain.DNSRecordTypeSRV:
		if record.Port == nil {
			return "", fmt.Errorf("DNS SRV record %q port is required", fqdn)
		}
		priority := 0
		if record.Priority != nil {
			priority = *record.Priority
		}
		weight := 0
		if record.Weight != nil {
			weight = *record.Weight
		}
		return fmt.Sprintf("srv-host=%s,%s,%d,%d,%d", fqdn, value, *record.Port, priority, weight), nil
	default:
		return "", fmt.Errorf("DNS record %q type %q is not supported by dnsmasq", fqdn, record.Type)
	}
}

func parseDnsmasqDirective(zone domain.DNSZone, line string) (domain.DNSRecord, bool, error) {
	switch {
	case strings.HasPrefix(line, "address="):
		parts := strings.Split(strings.TrimPrefix(line, "address="), "/")
		if len(parts) != 3 || parts[0] != "" {
			return domain.DNSRecord{}, false, fmt.Errorf("invalid address directive %q", line)
		}
		fqdn := strings.Trim(strings.ToLower(strings.TrimSpace(parts[1])), ".")
		value := strings.TrimSpace(parts[2])
		if fqdn == "" || value == "" {
			return domain.DNSRecord{}, false, fmt.Errorf("invalid address directive %q", line)
		}
		recordType := domain.DNSRecordTypeA
		if strings.Contains(value, ":") {
			recordType = domain.DNSRecordTypeAAAA
		}
		return dnsmasqParsedRecord(zone, fqdn, recordType, value, nil, nil, nil)
	case strings.HasPrefix(line, "cname="):
		parts := strings.Split(strings.TrimPrefix(line, "cname="), ",")
		if len(parts) != 2 {
			return domain.DNSRecord{}, false, fmt.Errorf("invalid cname directive %q", line)
		}
		fqdn := strings.Trim(strings.ToLower(strings.TrimSpace(parts[0])), ".")
		value := strings.Trim(strings.ToLower(strings.TrimSpace(parts[1])), ".")
		if fqdn == "" || value == "" {
			return domain.DNSRecord{}, false, fmt.Errorf("invalid cname directive %q", line)
		}
		return dnsmasqParsedRecord(zone, fqdn, domain.DNSRecordTypeCNAME, value, nil, nil, nil)
	case strings.HasPrefix(line, "srv-host="):
		parts := strings.Split(strings.TrimPrefix(line, "srv-host="), ",")
		if len(parts) != 5 {
			return domain.DNSRecord{}, false, fmt.Errorf("invalid srv-host directive %q", line)
		}
		fqdn := strings.Trim(strings.ToLower(strings.TrimSpace(parts[0])), ".")
		value := strings.Trim(strings.ToLower(strings.TrimSpace(parts[1])), ".")
		port, err := strconv.Atoi(strings.TrimSpace(parts[2]))
		if err != nil {
			return domain.DNSRecord{}, false, fmt.Errorf("invalid srv-host port %q", parts[2])
		}
		priority, err := strconv.Atoi(strings.TrimSpace(parts[3]))
		if err != nil {
			return domain.DNSRecord{}, false, fmt.Errorf("invalid srv-host priority %q", parts[3])
		}
		weight, err := strconv.Atoi(strings.TrimSpace(parts[4]))
		if err != nil {
			return domain.DNSRecord{}, false, fmt.Errorf("invalid srv-host weight %q", parts[4])
		}
		if fqdn == "" || value == "" {
			return domain.DNSRecord{}, false, fmt.Errorf("invalid srv-host directive %q", line)
		}
		return dnsmasqParsedRecord(zone, fqdn, domain.DNSRecordTypeSRV, value, &priority, &weight, &port)
	default:
		return domain.DNSRecord{}, false, nil
	}
}

func dnsmasqParsedRecord(zone domain.DNSZone, fqdn string, recordType domain.DNSRecordType, value string, priority *int, weight *int, port *int) (domain.DNSRecord, bool, error) {
	if !strings.HasSuffix(fqdn, zone.Name) || (fqdn != zone.Name && !strings.HasSuffix(fqdn, "."+zone.Name)) {
		return domain.DNSRecord{}, false, nil
	}
	return domain.DNSRecord{
		Zone:     zone.Name,
		Name:     relativeDNSName(fqdn, zone.Name),
		FQDN:     fqdn,
		Type:     recordType,
		Value:    value,
		TTL:      zone.TTL,
		Priority: priority,
		Weight:   weight,
		Port:     port,
	}, true, nil
}

func dnsmasqSanitizeZoneFilename(zoneName string) string {
	zoneName = strings.TrimSpace(zoneName)
	var builder strings.Builder
	lastWasDash := false
	for _, r := range zoneName {
		writeDash := false
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			builder.WriteRune(unicode.ToLower(r))
			lastWasDash = false
		case r == '.', r == '-', r == '_':
			writeDash = true
		default:
			writeDash = true
		}
		if writeDash && !lastWasDash && builder.Len() > 0 {
			builder.WriteByte('-')
			lastWasDash = true
		}
	}
	value := strings.Trim(builder.String(), "-")
	if value == "" {
		return "zone"
	}
	return value
}

var _ Backend = (*DnsmasqBackend)(nil)
