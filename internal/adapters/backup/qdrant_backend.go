package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

const (
	defaultQdrantBackendPollInterval = 2 * time.Second
	defaultQdrantBackendPollTimeout  = 120 * time.Second
	defaultQdrantBackendTimeout      = 60 * time.Second
)

type QdrantBackendConfig struct {
	PollInterval time.Duration
	PollTimeout  time.Duration
	StagingDir   string
}

type qdrantSnapshotAPI interface {
	createSnapshot(ctx context.Context, collection string) (*qdrantSnapshotResult, error)
	pollSnapshotExists(ctx context.Context, collection, snapshotName string) error
	downloadSnapshot(ctx context.Context, collection, snapshotName, destPath string) (string, error)
	uploadSnapshot(ctx context.Context, collection, snapshotPath string) error
	recoverFromSnapshot(ctx context.Context, collection, snapshotName string) error
	listSnapshots(ctx context.Context, collection string) ([]qdrantSnapshotDesc, error)
	health(ctx context.Context) error
}

type qdrantSnapshotDesc struct {
	Name         string    `json:"name"`
	CreationTime time.Time `json:"creation_time"`
	Size         int64     `json:"size"`
}

type qdrantSnapshotResult struct {
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	Hash  string `json:"hash,omitempty"`
}

type QdrantBackend struct {
	config QdrantBackendConfig
	api    qdrantSnapshotAPI
}

type QdrantBackendOption func(*QdrantBackend)

func WithQdrantPollInterval(d time.Duration) QdrantBackendOption {
	return func(b *QdrantBackend) {
		if d > 0 {
			b.config.PollInterval = d
		}
	}
}

func WithQdrantPollTimeout(d time.Duration) QdrantBackendOption {
	return func(b *QdrantBackend) {
		if d > 0 {
			b.config.PollTimeout = d
		}
	}
}

func WithQdrantStagingDir(dir string) QdrantBackendOption {
	return func(b *QdrantBackend) { b.config.StagingDir = strings.TrimSpace(dir) }
}

func withQdrantAPI(api qdrantSnapshotAPI) QdrantBackendOption {
	return func(b *QdrantBackend) { b.api = api }
}

func NewQdrantBackend(opts ...QdrantBackendOption) *QdrantBackend {
	b := &QdrantBackend{
		config: QdrantBackendConfig{
			PollInterval: defaultQdrantBackendPollInterval,
			PollTimeout:  defaultQdrantBackendPollTimeout,
		},
	}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

func (b *QdrantBackend) BackendKind() domain.BackupBackendKind { return domain.BackupBackendQdrantSnapshot }

func (b *QdrantBackend) Capabilities() service.BackendCapabilities {
	return service.BackendCapabilities{
		SnapshotCreate: true,
		SnapshotVerify: true,
		Restore:        true,
		Retention:      false,
		Probe:          true,
	}
}

func (b *QdrantBackend) Health(ctx context.Context, repo *domain.BackupRepository) error {
	api := b.resolveAPI(repo)
	return api.health(ctx)
}

func (b *QdrantBackend) CreateSnapshot(ctx context.Context, req service.BackupSnapshotRequest) (*service.BackupSnapshotResult, error) {
	if req.Repository == nil || req.Recipe == nil || req.Run == nil {
		return nil, fmt.Errorf("%w: qdrant snapshot request requires repository, recipe, and run", service.ErrBackupBackendConfiguration)
	}
	collection := strings.TrimSpace(req.Recipe.TargetRef)
	if collection == "" {
		return nil, fmt.Errorf("%w: qdrant recipe target_ref must specify the collection name", service.ErrBackupBackendConfiguration)
	}
	api := b.resolveAPI(req.Repository)
	snapshot, err := api.createSnapshot(ctx, collection)
	if err != nil {
		return nil, fmt.Errorf("%w: qdrant create snapshot: %v", service.ErrBackupBackendExecution, err)
	}
	pollCtx, cancel := context.WithTimeout(ctx, b.config.PollTimeout)
	defer cancel()
	if err := api.pollSnapshotExists(pollCtx, collection, snapshot.Name); err != nil {
		return nil, fmt.Errorf("%w: qdrant snapshot %q did not become available: %v", service.ErrBackupBackendExecution, snapshot.Name, err)
	}
	stagingDir := b.stagingDir(req.Repository)
	if err := os.MkdirAll(stagingDir, 0700); err != nil {
		return nil, fmt.Errorf("%w: creating qdrant snapshot staging directory: %w", service.ErrBackupBackendConfiguration, err)
	}
	snapshotFile := filepath.Join(stagingDir, req.Run.ID.String()+".snapshot")
	checksum, err := api.downloadSnapshot(ctx, collection, snapshot.Name, snapshotFile)
	if err != nil {
		return nil, fmt.Errorf("%w: qdrant download snapshot: %v", service.ErrBackupBackendExecution, err)
	}
	info, err := os.Stat(snapshotFile)
	if err != nil {
		return nil, fmt.Errorf("%w: stat qdrant snapshot: %w", service.ErrBackupBackendConfiguration, err)
	}
	evidence := map[string]any{
		"qdrant_snapshot": map[string]any{
			"snapshot_name":   snapshot.Name,
			"snapshot_file":   snapshotFile,
			"snapshot_size":   info.Size(),
			"checksum_sha256": checksum,
			"collection":      collection,
		},
	}
	return &service.BackupSnapshotResult{SnapshotID: snapshot.Name, Evidence: evidence}, nil
}

func (b *QdrantBackend) VerifySnapshot(ctx context.Context, req service.BackupVerifyRequest) (*service.BackupVerifyResult, error) {
	if req.Repository == nil || req.Run == nil {
		return nil, fmt.Errorf("%w: qdrant verification request requires repository and run", service.ErrBackupBackendConfiguration)
	}
	if req.Mode != domain.BackupVerificationQdrantSnapshotVerify {
		return nil, fmt.Errorf("%w: qdrant backend cannot execute verification mode %q", service.ErrBackupBackendUnsupported, req.Mode)
	}
	snapshotFile := b.locateSnapshotFile(req)
	if snapshotFile == "" {
		return nil, fmt.Errorf("%w: qdrant snapshot file not found for snapshot %q", service.ErrBackupBackendConfiguration, req.SnapshotID)
	}
	if _, err := os.Stat(snapshotFile); err != nil {
		return nil, fmt.Errorf("%w: qdrant snapshot file %q is not accessible: %w", service.ErrBackupBackendConfiguration, snapshotFile, err)
	}
	computedChecksum, err := sha256File(snapshotFile)
	if err != nil {
		return nil, fmt.Errorf("%w: qdrant snapshot checksum failed: %w", service.ErrBackupBackendExecution, err)
	}
	evidence := map[string]any{
		"qdrant_verify": map[string]any{
			"snapshot_file":       snapshotFile,
			"computed_checksum":   computedChecksum,
			"snapshot":            req.SnapshotID,
		},
	}
	storedChecksum := storedSnapshotChecksum(req.Run)
	if storedChecksum != "" && storedChecksum != computedChecksum {
		return &service.BackupVerifyResult{
			Verified: false, Status: domain.BackupVerificationFailed,
			Evidence: evidence, Error: fmt.Sprintf("checksum mismatch: stored=%s computed=%s", storedChecksum, computedChecksum),
		}, fmt.Errorf("%w: qdrant snapshot checksum mismatch", service.ErrBackupBackendExecution)
	}
	evidence["checksum_verified"] = true
	return &service.BackupVerifyResult{Verified: true, Status: domain.BackupVerificationSucceeded, Evidence: evidence}, nil
}

func (b *QdrantBackend) Restore(ctx context.Context, req service.BackupRestoreRequest) (*service.BackupRestoreResult, error) {
	if req.Run == nil || req.SourceRun == nil || req.Repository == nil {
		return nil, fmt.Errorf("%w: qdrant restore request requires restore run, source run, and repository", service.ErrBackupBackendConfiguration)
	}
	if req.Run.Backend != domain.BackupBackendQdrantSnapshot || req.SourceRun.Backend != domain.BackupBackendQdrantSnapshot {
		return nil, fmt.Errorf("%w: restore run and source run must use qdrant-snapshot backend", service.ErrBackupBackendUnsupported)
	}
	if req.Run.BackupRunID != req.SourceRun.ID {
		return nil, fmt.Errorf("%w: qdrant restore run source mismatch", service.ErrBackupBackendConfiguration)
	}
	if !domain.BackupRunRestoreEligible(req.SourceRun) {
		return nil, fmt.Errorf("%w: qdrant restore requires a succeeded and verified source backup run", service.ErrBackupBackendConfiguration)
	}
	targetCollection := qdrantRestoreTargetCollection(req)
	if targetCollection == "" {
		return nil, fmt.Errorf("%w: qdrant restore requires a target collection in restore_target_ref", service.ErrBackupBackendConfiguration)
	}
	sourceCollection := strings.TrimSpace(req.SourceRun.TargetRef)
	if sourceCollection == "" {
		sourceCollection = targetCollection
	}
	api := b.resolveAPI(req.Repository)
	snapshotFile := b.locateSnapshotFileFromRun(req.SourceRun)
	if snapshotFile == "" {
		return nil, fmt.Errorf("%w: qdrant snapshot file not found for restore", service.ErrBackupBackendConfiguration)
	}
	if _, err := os.Stat(snapshotFile); err != nil {
		return nil, fmt.Errorf("%w: qdrant snapshot file %q is not accessible: %w", service.ErrBackupBackendConfiguration, snapshotFile, err)
	}
	computedChecksum, err := sha256File(snapshotFile)
	if err != nil {
		return nil, fmt.Errorf("%w: qdrant restore checksum failed: %w", service.ErrBackupBackendExecution, err)
	}
	storedChecksum := storedSnapshotChecksum(req.SourceRun)
	if storedChecksum != "" && storedChecksum != computedChecksum {
		return nil, fmt.Errorf("%w: qdrant restore checksum mismatch: stored=%s computed=%s", service.ErrBackupBackendConfiguration, storedChecksum, computedChecksum)
	}
	uploadDir := filepath.Dir(snapshotFile)
	symlinkName := filepath.Join(uploadDir, targetCollection+".snapshot")
	if err := os.Symlink(snapshotFile, symlinkName); err == nil {
		defer os.Remove(symlinkName)
	}
	if err := api.uploadSnapshot(ctx, targetCollection, snapshotFile); err != nil {
		return nil, fmt.Errorf("%w: qdrant upload snapshot: %v", service.ErrBackupBackendExecution, err)
	}
	if err := api.recoverFromSnapshot(ctx, targetCollection, filepath.Base(snapshotFile)); err != nil {
		return nil, fmt.Errorf("%w: qdrant recover from snapshot: %v", service.ErrBackupBackendExecution, err)
	}
	evidence := map[string]any{
		"qdrant_restore": map[string]any{
			"snapshot_file":     snapshotFile,
			"source_collection": sourceCollection,
			"target_collection": targetCollection,
			"checksum_sha256":   computedChecksum,
		},
	}
	result := &service.BackupRestoreResult{Verified: false, VerificationStatus: domain.BackupVerificationSkipped, Evidence: evidence}
	snapshots, listErr := api.listSnapshots(ctx, targetCollection)
	if listErr == nil && len(snapshots) > 0 {
		result.Verified = true
		result.VerificationStatus = domain.BackupVerificationSucceeded
		evidence["restore_snapshots"] = len(snapshots)
	}
	return result, nil
}

func (b *QdrantBackend) stagingDir(repo *domain.BackupRepository) string {
	if b.config.StagingDir != "" {
		return b.config.StagingDir
	}
	if repo != nil && repo.Metadata != nil {
		if dir, ok := repo.Metadata["qdrant_staging_dir"].(string); ok && strings.TrimSpace(dir) != "" {
			return strings.TrimSpace(dir)
		}
	}
	return filepath.Join(os.TempDir(), "bahia-qdrant-snapshots")
}

func (b *QdrantBackend) locateSnapshotFile(req service.BackupVerifyRequest) string {
	if req.Run != nil && req.Run.Metadata != nil {
		if evidence, ok := req.Run.Metadata["snapshot_evidence"].(map[string]any); ok {
			if qd, ok := evidence["qdrant_snapshot"].(map[string]any); ok {
				if f, ok := qd["snapshot_file"].(string); ok && f != "" {
					return f
				}
			}
		}
	}
	return ""
}

func (b *QdrantBackend) locateSnapshotFileFromRun(sourceRun *domain.BackupRun) string {
	if sourceRun == nil || sourceRun.Metadata == nil {
		return ""
	}
	if evidence, ok := sourceRun.Metadata["snapshot_evidence"].(map[string]any); ok {
		if qd, ok := evidence["qdrant_snapshot"].(map[string]any); ok {
			if f, ok := qd["snapshot_file"].(string); ok && f != "" {
				return f
			}
		}
	}
	return ""
}

func (b *QdrantBackend) resolveAPI(repo *domain.BackupRepository) qdrantSnapshotAPI {
	if b.api != nil {
		return b.api
	}
	return &defaultQdrantHTTPAPI{repo: repo}
}

func qdrantRestoreTargetCollection(req service.BackupRestoreRequest) string {
	if req.Run != nil && strings.TrimSpace(req.Run.RestoreTargetRef) != "" {
		ref := strings.TrimSpace(req.Run.RestoreTargetRef)
		for _, prefix := range []string{"qdrant:", "collection:"} {
			if strings.HasPrefix(ref, prefix) {
				return strings.TrimSpace(strings.TrimPrefix(ref, prefix))
			}
		}
		if strings.HasPrefix(ref, "restore:") {
			return strings.TrimSpace(strings.TrimPrefix(ref, "restore:"))
		}
		return ref
	}
	return ""
}

func storedSnapshotChecksum(run *domain.BackupRun) string {
	if run == nil || run.Metadata == nil {
		return ""
	}
	if evidence, ok := run.Metadata["snapshot_evidence"].(map[string]any); ok {
		if qd, ok := evidence["qdrant_snapshot"].(map[string]any); ok {
			if cs, ok := qd["checksum_sha256"].(string); ok {
				return cs
			}
		}
	}
	return ""
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hasher.Sum(nil)), nil
}

// defaultQdrantHTTPAPI makes direct HTTP calls to the Qdrant REST API.
type defaultQdrantHTTPAPI struct {
	repo *domain.BackupRepository
}

func (a *defaultQdrantHTTPAPI) baseURL() string {
	if a.repo == nil || a.repo.Metadata == nil {
		return ""
	}
	url, _ := a.repo.Metadata["qdrant_url"].(string)
	return strings.TrimRight(strings.TrimSpace(url), "/")
}

func (a *defaultQdrantHTTPAPI) apiKey() (string, string) {
	if a.repo == nil || a.repo.Metadata == nil {
		return "", ""
	}
	key, _ := a.repo.Metadata["qdrant_api_key"].(string)
	header, _ := a.repo.Metadata["qdrant_auth_header"].(string)
	if header == "" {
		header = "api-key"
	}
	return strings.TrimSpace(key), strings.TrimSpace(header)
}

func (a *defaultQdrantHTTPAPI) doJSON(ctx context.Context, method, path string, body any) (*http.Response, error) {
	baseURL := a.baseURL()
	if baseURL == "" {
		return nil, fmt.Errorf("qdrant: URL not configured in repository metadata.qdrant_url")
	}
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("qdrant: encode request: %w", err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, r)
	if err != nil {
		return nil, fmt.Errorf("qdrant: create request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key, header := a.apiKey(); key != "" {
		req.Header.Set(header, key)
	}
	client := &http.Client{Timeout: defaultQdrantBackendTimeout}
	return client.Do(req)
}

func (a *defaultQdrantHTTPAPI) health(ctx context.Context) error {
	resp, err := a.doJSON(ctx, http.MethodGet, "/healthz", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("qdrant: health check returned status %d", resp.StatusCode)
	}
	return nil
}

func (a *defaultQdrantHTTPAPI) createSnapshot(ctx context.Context, collection string) (*qdrantSnapshotResult, error) {
	resp, err := a.doJSON(ctx, http.MethodPost, "/collections/"+collection+"/snapshots", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return nil, fmt.Errorf("qdrant: create snapshot API error %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Status string               `json:"status"`
		Result *qdrantSnapshotResult `json:"result"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("qdrant: decode snapshot create response: %w", err)
	}
	if envelope.Result == nil || envelope.Result.Name == "" {
		return nil, fmt.Errorf("qdrant: snapshot create response missing result.name")
	}
	return envelope.Result, nil
}

func (a *defaultQdrantHTTPAPI) pollSnapshotExists(ctx context.Context, collection, snapshotName string) error {
	for {
		snapshots, err := a.listSnapshots(ctx, collection)
		if err != nil {
			return err
		}
		for _, s := range snapshots {
			if s.Name == snapshotName {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(defaultQdrantBackendPollInterval):
		}
	}
}

func (a *defaultQdrantHTTPAPI) listSnapshots(ctx context.Context, collection string) ([]qdrantSnapshotDesc, error) {
	resp, err := a.doJSON(ctx, http.MethodGet, "/collections/"+collection+"/snapshots", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("qdrant: list snapshots API error %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Status string              `json:"status"`
		Result []qdrantSnapshotDesc `json:"result"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("qdrant: decode snapshot list: %w", err)
	}
	return envelope.Result, nil
}

func (a *defaultQdrantHTTPAPI) downloadSnapshot(ctx context.Context, collection, snapshotName, destPath string) (string, error) {
	resp, err := a.doJSON(ctx, http.MethodGet, "/collections/"+collection+"/snapshots/"+snapshotName, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("qdrant: download snapshot API error %d", resp.StatusCode)
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0700); err != nil {
		return "", err
	}
	f, err := os.Create(destPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hasher := sha256.New()
	writer := io.MultiWriter(f, hasher)
	if _, err := io.Copy(writer, resp.Body); err != nil {
		return "", fmt.Errorf("qdrant: download snapshot write: %w", err)
	}
	return fmt.Sprintf("%x", hasher.Sum(nil)), nil
}

func (a *defaultQdrantHTTPAPI) uploadSnapshot(ctx context.Context, collection, snapshotPath string) error {
	f, err := os.Open(snapshotPath)
	if err != nil {
		return fmt.Errorf("open snapshot file: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	baseURL := a.baseURL()
	if baseURL == "" {
		return fmt.Errorf("qdrant: URL not configured")
	}
	uploadURL := baseURL + "/collections/" + collection + "/snapshots/upload"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("create upload request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	if key, header := a.apiKey(); key != "" {
		req.Header.Set(header, key)
	}
	client := &http.Client{Timeout: defaultQdrantBackendTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("upload snapshot: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("qdrant: upload snapshot API error %d", resp.StatusCode)
	}
	return nil
}

func (a *defaultQdrantHTTPAPI) recoverFromSnapshot(ctx context.Context, collection, snapshotName string) error {
	body := map[string]any{
		"location": "file:///qdrant/snapshots/" + snapshotName,
	}
	resp, err := a.doJSON(ctx, http.MethodPost, "/collections/"+collection+"/snapshots/recover", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("qdrant: recover snapshot API error %d", resp.StatusCode)
	}
	return nil
}

var _ service.BackupBackend = (*QdrantBackend)(nil)
var _ service.BackupSnapshotCreateBackend = (*QdrantBackend)(nil)
var _ service.BackupSnapshotVerifyBackend = (*QdrantBackend)(nil)
var _ service.BackupRestoreBackend = (*QdrantBackend)(nil)
