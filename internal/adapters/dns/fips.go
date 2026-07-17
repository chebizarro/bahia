package dns

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

const (
	fipsManagedBegin = "# bahia-managed begin"
	fipsManagedEnd   = "# bahia-managed end"
)

// FIPSBackend writes Bahia-managed aliases into the FIPS hosts file.
type FIPSBackend struct {
	hostsPath string
	logger    *zap.Logger
}

// NewFIPSBackend creates a DNS backend targeting a FIPS hosts file.
func NewFIPSBackend(hostsPath string, logger *zap.Logger) *FIPSBackend {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &FIPSBackend{hostsPath: strings.TrimSpace(hostsPath), logger: logger}
}

func (b *FIPSBackend) BackendType() domain.DNSBackendType {
	return domain.DNSBackendTypeFIPS
}

func (b *FIPSBackend) Health(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	hostsPath, err := b.requiredHostsPath()
	if err != nil {
		return err
	}
	info, err := os.Stat(hostsPath)
	if err != nil {
		return fmt.Errorf("DNS FIPS backend hosts file %q is not accessible: %w", hostsPath, err)
	}
	if info.IsDir() {
		return fmt.Errorf("DNS FIPS backend hosts file %q is a directory", hostsPath)
	}
	file, err := os.OpenFile(hostsPath, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("DNS FIPS backend hosts file %q is not readable and writable: %w", hostsPath, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close DNS FIPS backend hosts file %q: %w", hostsPath, err)
	}
	file, err = os.CreateTemp(filepath.Dir(hostsPath), ".fips-health-*.tmp")
	if err != nil {
		return fmt.Errorf("DNS FIPS backend hosts directory %q is not writable: %w", filepath.Dir(hostsPath), err)
	}
	name := file.Name()
	if closeErr := file.Close(); closeErr != nil {
		_ = os.Remove(name)
		return fmt.Errorf("DNS FIPS backend health temp file close failed: %w", closeErr)
	}
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("DNS FIPS backend health temp file cleanup failed: %w", err)
	}
	return nil
}

func (b *FIPSBackend) ListRecords(ctx context.Context, zone domain.DNSZone) ([]domain.DNSRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := domain.ValidateDNSZone(&zone); err != nil {
		return nil, err
	}
	hostsPath, err := b.requiredHostsPath()
	if err != nil {
		return nil, err
	}
	file, err := os.Open(hostsPath)
	if errors.Is(err, os.ErrNotExist) {
		return []domain.DNSRecord{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read DNS FIPS hosts file %q: %w", hostsPath, err)
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
		record, ok, err := parseFIPSHostsLine(zone, scanner.Text())
		if err != nil {
			return nil, fmt.Errorf("parse DNS FIPS hosts file %q line %d: %w", hostsPath, lineNumber, err)
		}
		if ok {
			records = append(records, record)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan DNS FIPS hosts file %q: %w", hostsPath, err)
	}
	return sortedRecords(records), nil
}

func (b *FIPSBackend) SyncZone(ctx context.Context, zone domain.DNSZone, records []domain.DNSRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := domain.ValidateDNSZone(&zone); err != nil {
		return err
	}
	hostsPath, err := b.requiredHostsPath()
	if err != nil {
		return err
	}
	info, err := os.Stat(hostsPath)
	if err != nil {
		return fmt.Errorf("DNS FIPS backend hosts file %q is not accessible: %w", hostsPath, err)
	}
	if info.IsDir() {
		return fmt.Errorf("DNS FIPS backend hosts file %q is a directory", hostsPath)
	}
	current, err := os.ReadFile(hostsPath)
	if err != nil {
		return fmt.Errorf("read DNS FIPS hosts file %q: %w", hostsPath, err)
	}
	section, err := fipsManagedSection(zone, records)
	if err != nil {
		return err
	}
	data := replaceFIPSManagedSection(current, section)
	if err := ctx.Err(); err != nil {
		return err
	}
	return writeFIPSHostsAtomic(ctx, hostsPath, data, info.Mode().Perm())
}

func (b *FIPSBackend) requiredHostsPath() (string, error) {
	if b == nil || strings.TrimSpace(b.hostsPath) == "" {
		return "", fmt.Errorf("DNS FIPS backend hosts path is required")
	}
	return b.hostsPath, nil
}

func fipsManagedSection(zone domain.DNSZone, records []domain.DNSRecord) ([]byte, error) {
	entries := make(map[string]string)
	for _, record := range records {
		fqdn, value, ok, err := fipsHostsEntry(zone, record)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if existing, exists := entries[fqdn]; exists && existing != value {
			return nil, fmt.Errorf("DNS FIPS hosts entry %q has conflicting values %q and %q", fqdn, existing, value)
		}
		entries[fqdn] = value
	}

	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var buffer bytes.Buffer
	buffer.WriteString(fipsManagedBegin)
	buffer.WriteByte('\n')
	buffer.WriteString("# Zone: ")
	buffer.WriteString(zone.Name)
	buffer.WriteByte('\n')
	for _, key := range keys {
		buffer.WriteString(key)
		buffer.WriteString("  ")
		buffer.WriteString(entries[key])
		buffer.WriteByte('\n')
	}
	buffer.WriteString(fipsManagedEnd)
	buffer.WriteByte('\n')
	return buffer.Bytes(), nil
}

func fipsHostsEntry(zone domain.DNSZone, record domain.DNSRecord) (string, string, bool, error) {
	record.Zone = strings.TrimSpace(record.Zone)
	if record.Zone == "" {
		record.Zone = zone.Name
	}
	if record.Zone != zone.Name {
		return "", "", false, fmt.Errorf("DNS record %q zone %q does not match sync zone %q", record.FQDN, record.Zone, zone.Name)
	}
	value := strings.TrimSpace(record.Value)
	if !isFIPSHostsValue(value) {
		return "", "", false, fmt.Errorf("DNS FIPS record %q has unsupported value %q: expected an npub or fd00::/8 address", record.FQDN, value)
	}
	hostname := fipsHostnameForRecord(zone, record)
	if hostname == "" {
		return "", "", false, fmt.Errorf("DNS FIPS record hostname is required")
	}
	return hostname, value, true, nil
}

func fipsHostnameForRecord(zone domain.DNSZone, record domain.DNSRecord) string {
	fqdn := strings.Trim(strings.ToLower(strings.TrimSpace(record.FQDN)), ".")
	if strings.HasSuffix(fqdn, ".fips") || fqdn == "fips" {
		return fqdn
	}
	name := strings.Trim(strings.ToLower(strings.TrimSpace(record.Name)), ".")
	if name == "" || name == "@" {
		name = strings.TrimSuffix(fqdn, "."+strings.Trim(strings.ToLower(zone.Name), "."))
		if name == fqdn {
			name = strings.Split(fqdn, ".")[0]
		}
	}
	name = strings.Trim(name, ".")
	if name == "" || name == "@" {
		return ""
	}
	return name + ".fips"
}

func parseFIPSHostsLine(zone domain.DNSZone, line string) (domain.DNSRecord, bool, error) {
	line = strings.TrimSpace(stripInlineComment(line))
	if line == "" {
		return domain.DNSRecord{}, false, nil
	}
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return domain.DNSRecord{}, false, fmt.Errorf("invalid FIPS hosts entry %q", line)
	}
	fqdn := strings.Trim(strings.ToLower(strings.TrimSpace(fields[0])), ".")
	value := strings.TrimSpace(fields[1])
	if !strings.HasSuffix(fqdn, ".fips") {
		return domain.DNSRecord{}, false, nil
	}
	if !isFIPSHostsValue(value) {
		return domain.DNSRecord{}, false, nil
	}
	return domain.DNSRecord{
		Zone:  zone.Name,
		Name:  relativeDNSName(fqdn, "fips"),
		FQDN:  fqdn,
		Type:  domain.DNSRecordTypeCNAME,
		Value: value,
		TTL:   zone.TTL,
	}, true, nil
}

func stripInlineComment(line string) string {
	if idx := strings.IndexByte(line, '#'); idx >= 0 {
		return line[:idx]
	}
	return line
}

func isFIPSHostsValue(value string) bool {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(strings.ToLower(value), "npub1") && len(value) > len("npub1") {
		return true
	}
	ip := net.ParseIP(value)
	if ip == nil {
		return false
	}
	ip = ip.To16()
	return ip != nil && ip[0] == 0xfd
}

func replaceFIPSManagedSection(current []byte, section []byte) []byte {
	text := string(current)
	begin := strings.Index(text, fipsManagedBegin)
	end := -1
	if begin >= 0 {
		end = strings.Index(text[begin:], fipsManagedEnd)
		if end >= 0 {
			end += begin + len(fipsManagedEnd)
		}
	}
	if begin >= 0 && end >= 0 {
		replacementEnd := end
		if replacementEnd < len(text) && text[replacementEnd] == '\r' {
			replacementEnd++
		}
		if replacementEnd < len(text) && text[replacementEnd] == '\n' {
			replacementEnd++
		}
		updated := strings.TrimRight(text[:begin], "\n")
		if updated != "" {
			updated += "\n"
		}
		updated += string(section)
		trailing := strings.TrimLeft(text[replacementEnd:], "\n")
		if trailing != "" {
			updated += trailing
		}
		return []byte(ensureTrailingNewline(updated))
	}
	trimmed := strings.TrimRight(text, "\n")
	if trimmed == "" {
		return section
	}
	return []byte(trimmed + "\n" + string(section))
}

func ensureTrailingNewline(text string) string {
	if strings.HasSuffix(text, "\n") {
		return text
	}
	return text + "\n"
}

func writeFIPSHostsAtomic(ctx context.Context, hostsPath string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(hostsPath)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(hostsPath)+"-*.tmp")
	if err != nil {
		return fmt.Errorf("create DNS FIPS hosts temp file: %w", err)
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
		return fmt.Errorf("write DNS FIPS hosts temp file: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod DNS FIPS hosts temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync DNS FIPS hosts temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close DNS FIPS hosts temp file: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, hostsPath); err != nil {
		return fmt.Errorf("replace DNS FIPS hosts file %q: %w", hostsPath, err)
	}
	committed = true
	return nil
}

var _ Backend = (*FIPSBackend)(nil)
