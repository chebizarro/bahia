package dns

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/openagentsinc/bahia/internal/domain"
)

// FilesystemBackend writes one deterministic JSON snapshot per DNS zone.
type FilesystemBackend struct {
	RootDir string
}

type filesystemZonePayload struct {
	Zone    string             `json:"zone"`
	TTL     int                `json:"ttl"`
	Records []domain.DNSRecord `json:"records"`
}

// NewFilesystemBackend creates a filesystem DNS backend rooted at rootDir.
func NewFilesystemBackend(rootDir string) *FilesystemBackend {
	return &FilesystemBackend{RootDir: strings.TrimSpace(rootDir)}
}

func (b *FilesystemBackend) BackendType() domain.DNSBackendType {
	return domain.DNSBackendTypeFilesystem
}

func (b *FilesystemBackend) Health(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	rootDir, err := b.rootDir()
	if err != nil {
		return err
	}
	info, err := os.Stat(rootDir)
	if err != nil {
		return fmt.Errorf("DNS filesystem backend root %q is not accessible: %w", rootDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("DNS filesystem backend root %q is not a directory", rootDir)
	}

	file, err := os.CreateTemp(rootDir, ".health-*.tmp")
	if err != nil {
		return fmt.Errorf("DNS filesystem backend root %q is not writable: %w", rootDir, err)
	}
	name := file.Name()
	if closeErr := file.Close(); closeErr != nil {
		_ = os.Remove(name)
		return fmt.Errorf("DNS filesystem backend health temp file close failed: %w", closeErr)
	}
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("DNS filesystem backend health temp file cleanup failed: %w", err)
	}
	return nil
}

func (b *FilesystemBackend) ListRecords(ctx context.Context, zone domain.DNSZone) ([]domain.DNSRecord, error) {
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
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return []domain.DNSRecord{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read DNS zone snapshot %q: %w", path, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var payload filesystemZonePayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("decode DNS zone snapshot %q: %w", path, err)
	}
	if payload.Zone != zone.Name {
		return nil, fmt.Errorf("DNS zone snapshot %q contains zone %q, expected %q", path, payload.Zone, zone.Name)
	}
	records := sortedRecords(payload.Records)
	return records, nil
}

func (b *FilesystemBackend) SyncZone(ctx context.Context, zone domain.DNSZone, records []domain.DNSRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := domain.ValidateDNSZone(&zone); err != nil {
		return err
	}
	rootDir, err := b.rootDir()
	if err != nil {
		return err
	}
	info, err := os.Stat(rootDir)
	if err != nil {
		return fmt.Errorf("DNS filesystem backend root %q is not accessible: %w", rootDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("DNS filesystem backend root %q is not a directory", rootDir)
	}

	payload := filesystemZonePayload{
		Zone:    zone.Name,
		TTL:     zone.TTL,
		Records: sortedRecords(records),
	}
	data, err := marshalDeterministic(payload)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	path, err := b.zonePath(zone.Name)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(rootDir, "."+sanitizeZoneFilename(zone.Name)+"-*.tmp")
	if err != nil {
		return fmt.Errorf("create DNS zone snapshot temp file: %w", err)
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
		return fmt.Errorf("write DNS zone snapshot temp file: %w", err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod DNS zone snapshot temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync DNS zone snapshot temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close DNS zone snapshot temp file: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace DNS zone snapshot %q: %w", path, err)
	}
	committed = true
	return nil
}

func (b *FilesystemBackend) rootDir() (string, error) {
	if b == nil || strings.TrimSpace(b.RootDir) == "" {
		return "", fmt.Errorf("DNS filesystem backend root dir is required")
	}
	return b.RootDir, nil
}

func (b *FilesystemBackend) zonePath(zoneName string) (string, error) {
	rootDir, err := b.rootDir()
	if err != nil {
		return "", err
	}
	zoneName = strings.TrimSpace(zoneName)
	if zoneName == "" {
		return "", fmt.Errorf("DNS zone name is required")
	}
	return filepath.Join(rootDir, sanitizeZoneFilename(zoneName)+".json"), nil
}

func sortedRecords(records []domain.DNSRecord) []domain.DNSRecord {
	sorted := append([]domain.DNSRecord(nil), records...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].FQDN != sorted[j].FQDN {
			return sorted[i].FQDN < sorted[j].FQDN
		}
		if sorted[i].Type != sorted[j].Type {
			return sorted[i].Type < sorted[j].Type
		}
		return sorted[i].Value < sorted[j].Value
	})
	return sorted
}

func marshalDeterministic(payload filesystemZonePayload) ([]byte, error) {
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode DNS zone snapshot: %w", err)
	}
	return append(bytes.TrimRight(data, "\n"), '\n'), nil
}

func sanitizeZoneFilename(zoneName string) string {
	zoneName = strings.TrimSpace(zoneName)
	var builder strings.Builder
	for _, r := range zoneName {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '.', r == '-', r == '_':
			builder.WriteRune(r)
		default:
			builder.WriteByte('_')
		}
	}
	if builder.Len() == 0 {
		return "zone"
	}
	return builder.String()
}

var _ Backend = (*FilesystemBackend)(nil)
