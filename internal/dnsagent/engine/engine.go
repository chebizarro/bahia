// Package engine manages Bahia-owned dnsmasq include files.
package engine

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/openagentsinc/bahia/internal/atomicfile"
	"github.com/openagentsinc/bahia/internal/domain"
)

const defaultFilePrefix = "bahia-"

var automaticReloadStrategies = [][]string{
	{"systemctl", "reload", "dnsmasq"},
	{"service", "dnsmasq", "reload"},
	{"/etc/init.d/dnsmasq", "reload"},
	{"killall", "-HUP", "dnsmasq"},
	{"pkill", "-HUP", "dnsmasq"},
}

// CommandRunner executes argv without shell interpretation. Explicit operator
// commands are passed as ["sh", "-c", command].
type CommandRunner func(ctx context.Context, argv []string) error

// ReloadConfig configures dnsmasq validation and reload. ExplicitCommand is an
// operator-provided shell command. When it is empty, one portable argv strategy
// is auto-detected and cached. PreReloadCheck is always executed as argv.
type ReloadConfig struct {
	ExplicitCommand string
	PreReloadCheck  []string
}

// Config configures an Engine.
type Config struct {
	IncludeDir string
	FilePrefix string
	Runner     CommandRunner
	Reload     ReloadConfig
	Logger     *slog.Logger
}

// ZoneSnapshot is the DNS state observed in a Bahia-owned dnsmasq include.
type ZoneSnapshot struct {
	Records       []domain.DNSRecord
	Authoritative bool
}

// Engine atomically manages one include file per zone. It only creates,
// replaces, restores, or removes files whose names begin with FilePrefix inside
// IncludeDir; unrelated and manually managed files are never modified.
type Engine struct {
	includeDir string
	filePrefix string
	runner     CommandRunner
	reload     ReloadConfig
	logger     *slog.Logger

	strategyMu sync.RWMutex
	strategy   []string
}

// New constructs an Engine and detects its reload strategy when no explicit
// command is configured. Configuration errors are reported by Engine methods.
func New(cfg Config) *Engine {
	prefix := strings.TrimSpace(cfg.FilePrefix)
	if prefix == "" {
		prefix = defaultFilePrefix
	}
	runner := cfg.Runner
	if runner == nil {
		runner = runCommand
	}
	e := &Engine{
		includeDir: strings.TrimSpace(cfg.IncludeDir),
		filePrefix: prefix,
		runner:     runner,
		reload: ReloadConfig{
			ExplicitCommand: strings.TrimSpace(cfg.Reload.ExplicitCommand),
			PreReloadCheck:  append([]string(nil), cfg.Reload.PreReloadCheck...),
		},
		logger: cfg.Logger,
	}
	e.strategy = e.detectReloadStrategy()
	if e.logger != nil && len(e.strategy) > 0 {
		e.logger.Debug("selected dnsmasq reload strategy", "argv", e.strategy)
	}
	return e
}

// SelectedReloadStrategy returns the cached argv used to reload dnsmasq. An
// explicit shell command is represented as ["sh", "-c", command].
func (e *Engine) SelectedReloadStrategy() []string {
	if e == nil {
		return nil
	}
	e.strategyMu.RLock()
	defer e.strategyMu.RUnlock()
	return append([]string(nil), e.strategy...)
}

// RenderZone deterministically renders a dnsmasq include file in Bahia's
// established on-host format.
func RenderZone(zone domain.DNSZone, records []domain.DNSRecord) ([]byte, error) {
	if err := domain.ValidateDNSZone(&zone); err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	buffer.WriteString("# Managed by Bahia. Manual changes may be replaced.\n")
	buffer.WriteString("# Zone: ")
	buffer.WriteString(zone.Name)
	buffer.WriteByte('\n')
	if zone.Authoritative {
		buffer.WriteString("local=/")
		buffer.WriteString(zone.Name)
		buffer.WriteString("/\n")
	}
	for _, record := range sortedRecords(records) {
		directive, err := RenderDirective(zone, record)
		if err != nil {
			return nil, err
		}
		buffer.WriteString(directive)
		buffer.WriteByte('\n')
	}
	return buffer.Bytes(), nil
}

// RenderDirective renders one record in Bahia's established dnsmasq format.
func RenderDirective(zone domain.DNSZone, record domain.DNSRecord) (string, error) {
	record.Zone = domain.NormalizeDNSZoneName(record.Zone)
	if record.Zone == "" {
		record.Zone = zone.Name
	}
	if record.Zone != domain.NormalizeDNSZoneName(zone.Name) {
		return "", fmt.Errorf("DNS record %q zone %q does not match sync zone %q", record.FQDN, record.Zone, zone.Name)
	}
	fqdn := strings.Trim(strings.ToLower(strings.TrimSpace(record.FQDN)), ".")
	if fqdn == "" {
		fqdn = fqdnFromRecordName(record.Name, zone.Name)
	}
	if fqdn == "" {
		return "", fmt.Errorf("DNS record FQDN is required")
	}
	if !isInZone(fqdn, zone.Name) {
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

// ParseZoneFile parses Bahia-supported directives from a dnsmasq include file.
// Comments, blank lines, and unsupported directives are ignored.
func ParseZoneFile(zone domain.DNSZone, data []byte) (ZoneSnapshot, error) {
	if err := domain.ValidateDNSZone(&zone); err != nil {
		return ZoneSnapshot{}, err
	}
	return parseZone(zone, bytes.NewReader(data), nil)
}

// ApplyZone atomically replaces a Bahia-owned zone include, validates it when
// configured, reloads dnsmasq, and restores the exact prior bytes and mode if
// validation or reload fails. A failed reload is followed by one reload after
// restoration; it never falls through to another detected strategy.
func (e *Engine) ApplyZone(ctx context.Context, zone domain.DNSZone, records []domain.DNSRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := RenderZone(zone, records)
	if err != nil {
		return err
	}
	dir, err := e.requiredIncludeDir()
	if err != nil {
		return err
	}
	if err := validateDirectory(dir); err != nil {
		return err
	}
	path, err := e.zonePath(zone.Name)
	if err != nil {
		return err
	}
	previous, err := captureDNSMasqSnapshot(path)
	if err != nil {
		return err
	}
	if err := writeAtomic(ctx, path, zone.Name, data, 0o644); err != nil {
		return err
	}

	if len(e.reload.PreReloadCheck) > 0 {
		if err := e.runner(ctx, append([]string(nil), e.reload.PreReloadCheck...)); err != nil {
			checkErr := fmt.Errorf("validate dnsmasq after syncing zone %q: %w", zone.Name, err)
			if rollbackErr := atomicfile.Restore(path, ".dnsmasq-rollback-*.tmp", previous); rollbackErr != nil {
				return errors.Join(checkErr, fmt.Errorf("restore previous dnsmasq config for zone %q: %w", zone.Name, rollbackErr))
			}
			return checkErr
		}
	}

	strategy := e.SelectedReloadStrategy()
	if len(strategy) == 0 {
		if rollbackErr := atomicfile.Restore(path, ".dnsmasq-rollback-*.tmp", previous); rollbackErr != nil {
			return errors.Join(fmt.Errorf("DNS dnsmasq reload strategy is required"), fmt.Errorf("restore previous dnsmasq config for zone %q: %w", zone.Name, rollbackErr))
		}
		return fmt.Errorf("DNS dnsmasq reload strategy is required")
	}
	if err := e.runner(ctx, strategy); err != nil {
		reloadErr := fmt.Errorf("reload dnsmasq after syncing zone %q: %w", zone.Name, err)
		if rollbackErr := atomicfile.Restore(path, ".dnsmasq-rollback-*.tmp", previous); rollbackErr != nil {
			return errors.Join(reloadErr, fmt.Errorf("restore previous dnsmasq config for zone %q: %w", zone.Name, rollbackErr))
		}
		if rollbackReloadErr := e.runner(ctx, strategy); rollbackReloadErr != nil {
			return errors.Join(reloadErr, fmt.Errorf("reload dnsmasq after restoring zone %q: %w", zone.Name, rollbackReloadErr))
		}
		return reloadErr
	}
	return nil
}

// ListZone reads and parses the Bahia-owned include file for zone. Missing zone
// files produce an empty record list.
func (e *Engine) ListZone(ctx context.Context, zone domain.DNSZone) (ZoneSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return ZoneSnapshot{}, err
	}
	if err := domain.ValidateDNSZone(&zone); err != nil {
		return ZoneSnapshot{}, err
	}
	path, err := e.zonePath(zone.Name)
	if err != nil {
		return ZoneSnapshot{}, err
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return ZoneSnapshot{Records: []domain.DNSRecord{}}, nil
	}
	if err != nil {
		return ZoneSnapshot{}, fmt.Errorf("read dnsmasq zone config %q: %w", path, err)
	}
	defer file.Close()
	snapshot, err := parseZone(zone, file, ctx)
	if err != nil {
		var lineErr *parseLineError
		if errors.As(err, &lineErr) {
			return ZoneSnapshot{}, fmt.Errorf("parse dnsmasq zone config %q line %d: %w", path, lineErr.line, lineErr.err)
		}
		var scannerErr *zoneScanError
		if errors.As(err, &scannerErr) {
			return ZoneSnapshot{}, fmt.Errorf("scan dnsmasq zone config %q: %w", path, scannerErr.err)
		}
		return ZoneSnapshot{}, err
	}
	return snapshot, nil
}

// HealthCheck verifies that the include directory exists, is a directory, and
// permits creating and removing a temporary file.
func (e *Engine) HealthCheck(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := e.requiredIncludeDir()
	if err != nil {
		return err
	}
	if err := validateDirectory(dir); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".health-*.tmp")
	if err != nil {
		return fmt.Errorf("DNS dnsmasq backend config dir %q is not writable: %w", dir, err)
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

// SanitizeFileName converts a zone name into the stable filename component used
// by existing Bahia dnsmasq installations.
func SanitizeFileName(zoneName string) string {
	zoneName = domain.NormalizeDNSZoneName(zoneName)
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

func (e *Engine) detectReloadStrategy() []string {
	if e.reload.ExplicitCommand != "" {
		return []string{"sh", "-c", e.reload.ExplicitCommand}
	}
	return detectAutomaticReloadStrategy(exec.LookPath, os.Stat)
}

func detectAutomaticReloadStrategy(lookPath func(string) (string, error), stat func(string) (os.FileInfo, error)) []string {
	for _, strategy := range automaticReloadStrategies {
		binary := strategy[0]
		if filepath.IsAbs(binary) {
			info, err := stat(binary)
			if err == nil && !info.IsDir() {
				return append([]string(nil), strategy...)
			}
			continue
		}
		if _, err := lookPath(binary); err == nil {
			return append([]string(nil), strategy...)
		}
	}
	return nil
}

func (e *Engine) requiredIncludeDir() (string, error) {
	if e == nil || strings.TrimSpace(e.includeDir) == "" {
		return "", fmt.Errorf("DNS dnsmasq backend config dir is required")
	}
	return e.includeDir, nil
}

func (e *Engine) zonePath(zoneName string) (string, error) {
	dir, err := e.requiredIncludeDir()
	if err != nil {
		return "", err
	}
	zoneName = strings.TrimSpace(zoneName)
	if zoneName == "" {
		return "", fmt.Errorf("DNS zone name is required")
	}
	if e.filePrefix == "" || filepath.Base(e.filePrefix) != e.filePrefix || strings.ContainsAny(e.filePrefix, `/\\`) {
		return "", fmt.Errorf("DNS dnsmasq backend file prefix %q must be a filename prefix", e.filePrefix)
	}
	name := e.filePrefix + SanitizeFileName(zoneName) + ".conf"
	path := filepath.Join(dir, name)
	if filepath.Dir(path) != filepath.Clean(dir) || !strings.HasPrefix(filepath.Base(path), e.filePrefix) {
		return "", fmt.Errorf("DNS dnsmasq zone config path escapes include directory")
	}
	return path, nil
}

func validateDirectory(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("DNS dnsmasq backend config dir %q is not accessible: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("DNS dnsmasq backend config dir %q is not a directory", dir)
	}
	return nil
}

func writeAtomic(ctx context.Context, path, zoneName string, data []byte, mode os.FileMode) error {
	err := atomicfile.WriteFile(ctx, path, "."+SanitizeFileName(zoneName)+"-*.tmp", data, mode)
	if err == nil {
		return nil
	}
	var operationErr *atomicfile.Error
	if !errors.As(err, &operationErr) {
		return err
	}
	switch operationErr.Operation {
	case atomicfile.OperationCreateTemp:
		return fmt.Errorf("create dnsmasq zone config temp file: %w", operationErr.Err)
	case atomicfile.OperationWrite:
		return fmt.Errorf("write dnsmasq zone config temp file: %w", operationErr.Err)
	case atomicfile.OperationChmod:
		return fmt.Errorf("chmod dnsmasq zone config temp file: %w", operationErr.Err)
	case atomicfile.OperationSync:
		return fmt.Errorf("sync dnsmasq zone config temp file: %w", operationErr.Err)
	case atomicfile.OperationClose:
		return fmt.Errorf("close dnsmasq zone config temp file: %w", operationErr.Err)
	case atomicfile.OperationRename:
		return fmt.Errorf("replace dnsmasq zone config %q: %w", path, operationErr.Err)
	default:
		return err
	}
}

func captureDNSMasqSnapshot(path string) (atomicfile.Snapshot, error) {
	snapshot, err := atomicfile.Capture(path, 0o644)
	if err == nil {
		return snapshot, nil
	}
	var operationErr *atomicfile.Error
	if !errors.As(err, &operationErr) {
		return atomicfile.Snapshot{}, err
	}
	switch operationErr.Operation {
	case atomicfile.OperationRead:
		return atomicfile.Snapshot{}, fmt.Errorf("read previous dnsmasq zone config %q: %w", path, operationErr.Err)
	case atomicfile.OperationStat:
		return atomicfile.Snapshot{}, fmt.Errorf("stat previous dnsmasq zone config %q: %w", path, operationErr.Err)
	default:
		return atomicfile.Snapshot{}, err
	}
}

type parseLineError struct {
	line int
	err  error
}

func (e *parseLineError) Error() string { return fmt.Sprintf("line %d: %v", e.line, e.err) }
func (e *parseLineError) Unwrap() error { return e.err }

type zoneScanError struct{ err error }

func (e *zoneScanError) Error() string { return fmt.Sprintf("scan dnsmasq zone config: %v", e.err) }
func (e *zoneScanError) Unwrap() error { return e.err }

func parseZone(zone domain.DNSZone, reader io.Reader, ctx context.Context) (ZoneSnapshot, error) {
	snapshot := ZoneSnapshot{Records: []domain.DNSRecord{}}
	scanner := bufio.NewScanner(reader)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return ZoneSnapshot{}, err
			}
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if localZone, ok := parseLocalDirective(line); ok {
			if localZone == domain.NormalizeDNSZoneName(zone.Name) {
				snapshot.Authoritative = true
			}
			continue
		}
		record, ok, err := parseDirective(zone, line)
		if err != nil {
			return ZoneSnapshot{}, &parseLineError{line: lineNumber, err: err}
		}
		if ok {
			snapshot.Records = append(snapshot.Records, record)
		}
	}
	if err := scanner.Err(); err != nil {
		return ZoneSnapshot{}, &zoneScanError{err: err}
	}
	snapshot.Records = sortedRecords(snapshot.Records)
	return snapshot, nil
}

func parseLocalDirective(line string) (string, bool) {
	if !strings.HasPrefix(line, "local=/") || !strings.HasSuffix(line, "/") {
		return "", false
	}
	zoneName := domain.NormalizeDNSZoneName(strings.TrimSuffix(strings.TrimPrefix(line, "local=/"), "/"))
	return zoneName, zoneName != ""
}

func parseDirective(zone domain.DNSZone, line string) (domain.DNSRecord, bool, error) {
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
		return parsedRecord(zone, fqdn, recordType, value, nil, nil, nil)
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
		return parsedRecord(zone, fqdn, domain.DNSRecordTypeCNAME, value, nil, nil, nil)
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
		return parsedRecord(zone, fqdn, domain.DNSRecordTypeSRV, value, &priority, &weight, &port)
	default:
		return domain.DNSRecord{}, false, nil
	}
}

func parsedRecord(zone domain.DNSZone, fqdn string, recordType domain.DNSRecordType, value string, priority, weight, port *int) (domain.DNSRecord, bool, error) {
	if !isInZone(fqdn, zone.Name) {
		return domain.DNSRecord{}, false, nil
	}
	return domain.DNSRecord{
		Zone: zone.Name, Name: relativeDNSName(fqdn, zone.Name), FQDN: fqdn,
		Type: recordType, Value: value, TTL: zone.TTL,
		Priority: priority, Weight: weight, Port: port,
	}, true, nil
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

func fqdnFromRecordName(name, zoneName string) string {
	name = strings.Trim(strings.ToLower(strings.TrimSpace(name)), ".")
	zoneName = domain.NormalizeDNSZoneName(zoneName)
	if name == "" || name == "@" {
		return zoneName
	}
	return name + "." + zoneName
}

func relativeDNSName(fqdn, zoneName string) string {
	fqdn = strings.Trim(strings.ToLower(strings.TrimSpace(fqdn)), ".")
	zoneName = domain.NormalizeDNSZoneName(zoneName)
	if fqdn == zoneName {
		return "@"
	}
	return strings.TrimSuffix(fqdn, "."+zoneName)
}

func isInZone(fqdn, zoneName string) bool {
	return strings.HasSuffix(fqdn, zoneName) && (fqdn == zoneName || strings.HasSuffix(fqdn, "."+zoneName))
}

func runCommand(ctx context.Context, argv []string) error {
	if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
		return fmt.Errorf("command is required")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
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
