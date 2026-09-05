// Package agent implements the relay-independent Bahia DNS agent service core.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/dnsagent/engine"
	"github.com/openagentsinc/bahia/internal/dnsagent/protocol"
)

const stateSchema = "bahia.dnsagent.state.v1"

type Config struct {
	Engine            *engine.Engine
	IncludeDir        string
	FilePrefix        string
	AllowedZones      []string
	StateFilePath     string
	RequireEncryption bool
}

type Agent struct {
	engine            *engine.Engine
	includeDir        string
	filePrefix        string
	reloadStrategy    string
	allowedZones      []string
	allowed           map[string]struct{}
	stateFilePath     string
	requireEncryption bool

	mu    sync.Mutex
	state persistentState
}

type persistentState struct {
	Schema          string           `json:"schema"`
	ZoneSerials     map[string]int64 `json:"zone_serials"`
	LastApplySerial int64            `json:"last_apply_serial"`
	LastApplyAt     string           `json:"last_apply_at"`
}

// Status is the process-local state exposed by the optional HTTP health endpoint.
type Status struct {
	Alive           bool   `json:"alive"`
	LastApplySerial int64  `json:"last_apply_serial"`
	LastApplyAt     string `json:"last_apply_at"`
}

func New(cfg Config) (*Agent, error) {
	if cfg.Engine == nil {
		return nil, fmt.Errorf("DNS agent engine is required")
	}
	includeDir := strings.TrimSpace(cfg.IncludeDir)
	if includeDir == "" {
		return nil, fmt.Errorf("DNS agent include directory is required")
	}
	filePrefix := strings.TrimSpace(cfg.FilePrefix)
	if filePrefix == "" {
		filePrefix = "bahia-"
	}
	stateFilePath := strings.TrimSpace(cfg.StateFilePath)
	if stateFilePath == "" {
		return nil, fmt.Errorf("DNS agent state file path is required")
	}
	allowedZones, allowed, err := normalizeAllowedZones(cfg.AllowedZones)
	if err != nil {
		return nil, err
	}
	state, err := loadState(stateFilePath)
	if err != nil {
		return nil, err
	}
	return &Agent{
		engine:            cfg.Engine,
		includeDir:        includeDir,
		filePrefix:        filePrefix,
		reloadStrategy:    strings.Join(cfg.Engine.SelectedReloadStrategy(), " "),
		allowedZones:      allowedZones,
		allowed:           allowed,
		stateFilePath:     stateFilePath,
		requireEncryption: cfg.RequireEncryption,
		state:             state,
	}, nil
}

func (a *Agent) RegisterHandlers(transport *controlplane.EncryptedRequestTransport) {
	if a == nil || transport == nil {
		return
	}
	transport.RegisterContextVMHandler(protocol.MethodHealth, a.HealthHandler)
	transport.RegisterContextVMHandler(protocol.MethodList, a.ListHandler)
	transport.RegisterContextVMHandler(protocol.MethodSync, a.SyncHandler)
}

func (a *Agent) HealthHandler(ctx context.Context, request controlplane.ContextVMRequest) (any, error) {
	if err := a.validateRequestEnvelope(request); err != nil {
		return nil, err
	}
	var params protocol.HealthParams
	if err := decodeParams(request, &params); err != nil {
		return nil, fmt.Errorf("invalid %s params: %w", protocol.MethodHealth, err)
	}
	if err := params.Validate(); err != nil {
		return nil, err
	}
	if err := a.engine.HealthCheck(ctx); err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return protocol.HealthResult{
		Schema:          protocol.Schema,
		Status:          "ok",
		IncludeDir:      a.includeDir,
		FilePrefix:      a.filePrefix,
		ReloadStrategy:  a.reloadStrategy,
		AllowedZones:    append([]string(nil), a.allowedZones...),
		LastApplySerial: a.state.LastApplySerial,
		LastApplyAt:     a.state.LastApplyAt,
	}, nil
}

func (a *Agent) ListHandler(ctx context.Context, request controlplane.ContextVMRequest) (any, error) {
	if err := a.validateRequestEnvelope(request); err != nil {
		return nil, err
	}
	var params protocol.ListParams
	if err := decodeParams(request, &params); err != nil {
		return nil, fmt.Errorf("invalid %s params: %w", protocol.MethodList, err)
	}
	if err := params.Validate(); err != nil {
		return nil, err
	}
	zoneName, err := a.requireAllowedZone(params.Zone.Name)
	if err != nil {
		return nil, err
	}
	params.Zone.Name = zoneName

	a.mu.Lock()
	defer a.mu.Unlock()
	records, err := a.engine.ListZone(ctx, params.Zone)
	if err != nil {
		return nil, err
	}
	return protocol.ListResult{Schema: protocol.Schema, Records: records, Serial: a.state.ZoneSerials[zoneName]}, nil
}

func (a *Agent) SyncHandler(ctx context.Context, request controlplane.ContextVMRequest) (any, error) {
	if err := a.validateRequestEnvelope(request); err != nil {
		return nil, err
	}
	var params protocol.SyncParams
	if err := decodeParams(request, &params); err != nil {
		return nil, fmt.Errorf("invalid %s params: %w", protocol.MethodSync, err)
	}
	if err := params.Validate(); err != nil {
		return nil, err
	}
	zoneName, err := a.requireAllowedZone(params.Zone.Name)
	if err != nil {
		return nil, err
	}
	params.Zone.Name = zoneName

	a.mu.Lock()
	defer a.mu.Unlock()
	lastSerial, previouslyApplied := a.state.ZoneSerials[zoneName]
	if previouslyApplied && params.Serial < lastSerial {
		// Recoverable stale signal: report the agent's current serial so the
		// backend can resume above it (for example after a control-plane restart
		// with a stepped-back clock).
		return protocol.SyncResult{Schema: protocol.Schema, Status: protocol.SyncStatusStale, Changed: false, Serial: lastSerial}, nil
	}
	if previouslyApplied && params.Serial == lastSerial {
		return protocol.SyncResult{Schema: protocol.Schema, Status: protocol.SyncStatusOK, Changed: false, Serial: lastSerial}, nil
	}

	changed := true
	if previouslyApplied {
		// Compare the desired render against the RAW on-disk bytes (not a
		// parse-and-rerender round trip) so hand edits the parser ignores —
		// unsupported directives, comments — still trigger a full rewrite,
		// honoring the file-header ownership promise.
		desiredData, renderErr := engine.RenderZone(params.Zone, params.Records)
		if renderErr != nil {
			return nil, renderErr
		}
		currentData, readErr := os.ReadFile(a.zoneIncludePath(zoneName))
		switch {
		case readErr == nil:
			changed = !bytes.Equal(currentData, desiredData)
		case errors.Is(readErr, os.ErrNotExist):
			changed = true
		default:
			return nil, fmt.Errorf("read dnsmasq zone config for zone %q: %w", zoneName, readErr)
		}
	}
	if changed {
		if err := a.engine.ApplyZone(ctx, params.Zone, params.Records); err != nil {
			return nil, err
		}
	}

	next := cloneState(a.state)
	next.ZoneSerials[zoneName] = params.Serial
	next.LastApplySerial = params.Serial
	next.LastApplyAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := writeStateAtomic(a.stateFilePath, next); err != nil {
		// Preserve the monotonic guard for this process even if durable storage
		// fails after dnsmasq was successfully updated.
		a.state = next
		return nil, err
	}
	a.state = next
	return protocol.SyncResult{Schema: protocol.Schema, Status: protocol.SyncStatusOK, Changed: changed, Serial: params.Serial}, nil
}

// zoneIncludePath mirrors the engine's include-file naming for a zone.
func (a *Agent) zoneIncludePath(zoneName string) string {
	return filepath.Join(a.includeDir, a.filePrefix+engine.SanitizeFileName(zoneName)+".conf")
}

func (a *Agent) Status() Status {
	if a == nil {
		return Status{}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return Status{Alive: true, LastApplySerial: a.state.LastApplySerial, LastApplyAt: a.state.LastApplyAt}
}

func (a *Agent) validateRequestEnvelope(request controlplane.ContextVMRequest) error {
	if !a.requireEncryption {
		return nil
	}
	if request.OuterEvent == nil || (request.OuterEvent.Kind != controlplane.KindContextVMGiftWrap && request.OuterEvent.Kind != controlplane.KindContextVMEphemeralWrap) {
		return fmt.Errorf("encrypted ContextVM envelope is required")
	}
	return nil
}

func (a *Agent) requireAllowedZone(zone string) (string, error) {
	normalized := normalizeZone(zone)
	if _, ok := a.allowed[normalized]; !ok {
		return "", fmt.Errorf("zone %q not allowed by agent allowlist", zone)
	}
	return normalized, nil
}

func decodeParams(request controlplane.ContextVMRequest, out any) error {
	if len(request.RPC.Params) == 0 || string(request.RPC.Params) == "null" {
		return json.Unmarshal([]byte(`{}`), out)
	}
	return json.Unmarshal(request.RPC.Params, out)
}

func normalizeAllowedZones(zones []string) ([]string, map[string]struct{}, error) {
	allowed := make(map[string]struct{}, len(zones))
	for _, zone := range zones {
		normalized := normalizeZone(zone)
		if normalized == "" {
			continue
		}
		allowed[normalized] = struct{}{}
	}
	if len(allowed) == 0 {
		return nil, nil, fmt.Errorf("DNS agent allowed zones are required")
	}
	normalized := make([]string, 0, len(allowed))
	for zone := range allowed {
		normalized = append(normalized, zone)
	}
	sort.Strings(normalized)
	return normalized, allowed, nil
}

func normalizeZone(zone string) string {
	return strings.Trim(strings.ToLower(strings.TrimSpace(zone)), ".")
}

func loadState(path string) (persistentState, error) {
	state := persistentState{Schema: stateSchema, ZoneSerials: map[string]int64{}}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, fmt.Errorf("open DNS agent state file %q: %w", path, err)
	}
	defer file.Close() //nolint:errcheck
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return state, fmt.Errorf("decode DNS agent state file %q: %w", path, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			err = fmt.Errorf("multiple JSON values")
		}
		return state, fmt.Errorf("decode DNS agent state file %q: %w", path, err)
	}
	if state.Schema != stateSchema {
		return state, fmt.Errorf("unsupported DNS agent state schema %q", state.Schema)
	}
	if state.ZoneSerials == nil {
		state.ZoneSerials = map[string]int64{}
	}
	return state, nil
}

func writeStateAtomic(path string, state persistentState) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create DNS agent state directory %q: %w", dir, err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode DNS agent state: %w", err)
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(dir, ".dns-agent-state-*.tmp")
	if err != nil {
		return fmt.Errorf("create DNS agent state temp file: %w", err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod DNS agent state temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write DNS agent state temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync DNS agent state temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close DNS agent state temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace DNS agent state file %q: %w", path, err)
	}
	committed = true
	return nil
}

func cloneState(state persistentState) persistentState {
	clone := state
	clone.ZoneSerials = make(map[string]int64, len(state.ZoneSerials))
	for zone, serial := range state.ZoneSerials {
		clone.ZoneSerials[zone] = serial
	}
	return clone
}
