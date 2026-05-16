package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	runtimeadapter "github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/domain"
)

const (
	MLProvenanceEdgeConvertedToRKNN = "converted_to_rknn"
	defaultRKNNServerHealthPath     = "/health"
	defaultRKNNServerSmokePath      = "/infer"
)

// ONNXValueInfo captures the declared ONNX graph inputs/outputs that must be
// known before Bahia dispatches conversion work.
type ONNXValueInfo struct {
	Name     string  `json:"name"`
	DataType string  `json:"data_type"`
	Shape    []int64 `json:"shape,omitempty"`
}

// ONNXModelMetadata is the metadata contract Bahia validates before ONNX->RKNN
// conversion. It is intentionally metadata-driven so tests and workers can fake
// validation without importing the full ONNX protobuf stack or requiring RKNN hardware.
type ONNXModelMetadata struct {
	ProducerName string            `json:"producer_name,omitempty"`
	GraphName    string            `json:"graph_name,omitempty"`
	Opset        int               `json:"opset"`
	Inputs       []ONNXValueInfo   `json:"inputs"`
	Outputs      []ONNXValueInfo   `json:"outputs"`
	License      string            `json:"license"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

type ONNXValidationRequest struct {
	Artifact        domain.MLArtifactRef `json:"artifact"`
	Metadata        ONNXModelMetadata    `json:"metadata"`
	AllowedLicenses []string             `json:"allowed_licenses,omitempty"`
	MinOpset        int                  `json:"min_opset,omitempty"`
	MaxOpset        int                  `json:"max_opset,omitempty"`
	RequirePinned   bool                 `json:"require_pinned,omitempty"`
}

type ONNXValidationResult struct {
	ArtifactID string            `json:"artifact_id"`
	URI        string            `json:"uri"`
	SHA256     string            `json:"sha256"`
	Opset      int               `json:"opset"`
	Inputs     []ONNXValueInfo   `json:"inputs"`
	Outputs    []ONNXValueInfo   `json:"outputs"`
	License    string            `json:"license"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// ValidateONNXArtifact fails closed when the source artifact lacks the metadata
// required by the ONNX->RKNN slice: digest, opset, declared I/O, and license.
func ValidateONNXArtifact(req ONNXValidationRequest) (*ONNXValidationResult, error) {
	artifact := req.Artifact
	if artifact.Format != domain.MLArtifactFormatONNX {
		return nil, fmt.Errorf("%w: expected ONNX artifact, got %s", ErrMLProvenanceFailedClosed, artifact.Format)
	}
	if normalizeSHA256(artifact.SHA256) == "" {
		return nil, fmt.Errorf("%w: ONNX artifact %s has no sha256 digest", ErrMLProvenanceFailedClosed, artifact.URI)
	}
	if strings.TrimSpace(artifact.URI) == "" {
		return nil, fmt.Errorf("%w: ONNX artifact URI is required", domain.ErrEmptyField)
	}
	if req.RequirePinned && !isPinnedGitHubArtifact(artifact) {
		return nil, fmt.Errorf("%w: GitHub ONNX artifact must be pinned to a revision", ErrMLProvenanceFailedClosed)
	}
	metadata := req.Metadata
	if metadata.Opset <= 0 {
		return nil, fmt.Errorf("%w: ONNX opset must be declared", ErrMLProvenanceFailedClosed)
	}
	if req.MinOpset > 0 && metadata.Opset < req.MinOpset {
		return nil, fmt.Errorf("%w: ONNX opset %d is below minimum %d", ErrMLProvenanceFailedClosed, metadata.Opset, req.MinOpset)
	}
	if req.MaxOpset > 0 && metadata.Opset > req.MaxOpset {
		return nil, fmt.Errorf("%w: ONNX opset %d exceeds maximum %d", ErrMLProvenanceFailedClosed, metadata.Opset, req.MaxOpset)
	}
	if err := validateONNXValueInfos("input", metadata.Inputs); err != nil {
		return nil, err
	}
	if err := validateONNXValueInfos("output", metadata.Outputs); err != nil {
		return nil, err
	}
	license := strings.TrimSpace(metadata.License)
	if license == "" {
		return nil, fmt.Errorf("%w: ONNX license must be declared", ErrMLProvenanceFailedClosed)
	}
	if len(req.AllowedLicenses) > 0 && !containsNormalizedString(req.AllowedLicenses, license) {
		return nil, fmt.Errorf("%w: ONNX license %q is not allowed", ErrMLProvenanceFailedClosed, license)
	}
	return &ONNXValidationResult{ArtifactID: artifact.ID.String(), URI: artifact.URI, SHA256: normalizeSHA256(artifact.SHA256), Opset: metadata.Opset, Inputs: append([]ONNXValueInfo(nil), metadata.Inputs...), Outputs: append([]ONNXValueInfo(nil), metadata.Outputs...), License: license, Metadata: copyStringMap(metadata.Metadata)}, nil
}

func validateONNXValueInfos(kind string, values []ONNXValueInfo) error {
	if len(values) == 0 {
		return fmt.Errorf("%w: ONNX %ss must be declared", ErrMLProvenanceFailedClosed, kind)
	}
	for i, value := range values {
		if strings.TrimSpace(value.Name) == "" {
			return fmt.Errorf("%w: ONNX %s %d has no name", ErrMLProvenanceFailedClosed, kind, i)
		}
		if strings.TrimSpace(value.DataType) == "" {
			return fmt.Errorf("%w: ONNX %s %q has no data type", ErrMLProvenanceFailedClosed, kind, value.Name)
		}
	}
	return nil
}

func isPinnedGitHubArtifact(artifact domain.MLArtifactRef) bool {
	if !isGitHubArtifact(artifact) {
		return true
	}
	return isImmutableGitHubRevision(githubRevisionFromArtifact(artifact))
}

func isGitHubArtifact(artifact domain.MLArtifactRef) bool {
	if artifact.Source != nil && artifact.Source.Kind == "github" {
		return true
	}
	raw := artifact.URI
	if artifact.Source != nil && artifact.Source.URI != "" {
		raw = artifact.Source.URI
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Scheme, "github") || strings.EqualFold(u.Host, "github.com")
}

func githubRevisionFromArtifact(artifact domain.MLArtifactRef) string {
	if artifact.Source != nil && strings.TrimSpace(artifact.Source.Revision) != "" {
		return strings.TrimSpace(artifact.Source.Revision)
	}
	if artifact.Metadata != nil {
		if revision, _ := stringValue(artifact.Metadata["revision"]); revision != "" {
			return revision
		}
	}
	for _, raw := range []string{artifact.URI, func() string {
		if artifact.Source != nil {
			return artifact.Source.URI
		}
		return ""
	}()} {
		if u, err := url.Parse(raw); err == nil {
			for _, key := range []string{"revision", "commit", "ref"} {
				if v := strings.TrimSpace(u.Query().Get(key)); v != "" {
					return v
				}
			}
			parts := strings.Split(strings.Trim(u.Path, "/"), "/")
			if len(parts) > 0 {
				if idx := strings.Index(parts[0], "@"); idx >= 0 {
					return parts[0][idx+1:]
				}
			}
		}
	}
	return ""
}

func isImmutableGitHubRevision(revision string) bool {
	revision = strings.TrimSpace(revision)
	if len(revision) != 40 {
		return false
	}
	for _, ch := range revision {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') && (ch < 'A' || ch > 'F') {
			return false
		}
	}
	return true
}

type RKNNPackagingRequest struct {
	SourceONNX        domain.MLArtifactRef `json:"source_onnx"`
	RKNNURI           string               `json:"rknn_uri"`
	RKNNBytes         []byte               `json:"-"`
	ExpectedSHA256    string               `json:"expected_sha256,omitempty"`
	ExpectedSizeBytes int64                `json:"expected_size_bytes,omitempty"`
	ModelVersionID    *uuid.UUID           `json:"model_version_id,omitempty"`
	ToolkitVersion    string               `json:"toolkit_version"`
	Target            string               `json:"target"`
	Quantization      string               `json:"quantization,omitempty"`
	Preprocess        map[string]any       `json:"preprocess"`
	Postprocess       map[string]any       `json:"postprocess"`
	Calibration       map[string]any       `json:"calibration"`
	Metadata          map[string]any       `json:"metadata,omitempty"`
}

// PackageRKNNArtifact creates the digest-addressed artifact metadata that a
// conversion worker reports after RKNN Toolkit2 completes. It does not run RKNN
// itself; conversion remains behind MLRecipeJobDispatcher/Loom/container jobs.
func PackageRKNNArtifact(req RKNNPackagingRequest) (*domain.MLArtifactRef, error) {
	if req.SourceONNX.Format != domain.MLArtifactFormatONNX {
		return nil, fmt.Errorf("%w: RKNN package source must be ONNX", ErrMLProvenanceFailedClosed)
	}
	if strings.TrimSpace(req.RKNNURI) == "" {
		return nil, fmt.Errorf("%w: RKNN URI is required", domain.ErrEmptyField)
	}
	sha := normalizeSHA256(req.ExpectedSHA256)
	size := req.ExpectedSizeBytes
	if len(req.RKNNBytes) > 0 {
		sum := sha256.Sum256(req.RKNNBytes)
		computed := hex.EncodeToString(sum[:])
		if sha != "" && sha != computed {
			return nil, fmt.Errorf("%w: RKNN digest mismatch: expected %s got %s", ErrMLProvenanceFailedClosed, sha, computed)
		}
		sha = computed
		size = int64(len(req.RKNNBytes))
	}
	if sha == "" {
		return nil, fmt.Errorf("%w: RKNN artifact has no sha256 digest", ErrMLProvenanceFailedClosed)
	}
	if strings.TrimSpace(req.ToolkitVersion) == "" {
		return nil, fmt.Errorf("%w: rknn_toolkit2 version is required", ErrMLProvenanceFailedClosed)
	}
	if strings.TrimSpace(req.Target) == "" {
		return nil, fmt.Errorf("%w: RKNN target is required", ErrMLProvenanceFailedClosed)
	}
	if len(req.Preprocess) == 0 || len(req.Postprocess) == 0 || len(req.Calibration) == 0 {
		return nil, fmt.Errorf("%w: RKNN package requires preprocess, postprocess, and calibration metadata", ErrMLProvenanceFailedClosed)
	}
	metadata := map[string]any{
		"source_onnx_artifact_id": req.SourceONNX.ID.String(),
		"source_onnx_sha256":      normalizeSHA256(req.SourceONNX.SHA256),
		"toolchain":               "rknn_toolkit2",
		"toolkit_version":         strings.TrimSpace(req.ToolkitVersion),
		"target":                  strings.TrimSpace(req.Target),
		"preprocess":              req.Preprocess,
		"postprocess":             req.Postprocess,
		"calibration":             req.Calibration,
	}
	if req.Quantization != "" {
		metadata["quantization"] = req.Quantization
	}
	for k, v := range req.Metadata {
		metadata[k] = v
	}
	artifact := &domain.MLArtifactRef{ID: uuid.New(), ModelVersionID: req.ModelVersionID, Kind: domain.MLArtifactKindModel, Format: domain.MLArtifactFormatRKNN, URI: req.RKNNURI, SHA256: sha, SizeBytes: size, MediaType: "application/x-rknn", Source: &domain.MLSourceRef{Kind: "rknn_toolkit2", URI: req.RKNNURI, Metadata: metadata}, Metadata: metadata, CreatedAt: time.Now().UTC()}
	if err := domain.ValidateMLArtifactRef(artifact); err != nil {
		return nil, err
	}
	return artifact, nil
}

func RecordRKNNConversionProvenance(ctx context.Context, provenance *MLProvenanceService, onnxArtifact, rknnArtifact *domain.MLArtifactRef, evidence map[string]any) error {
	if provenance == nil {
		return fmt.Errorf("ML provenance service is not configured")
	}
	if onnxArtifact == nil || rknnArtifact == nil {
		return fmt.Errorf("ONNX and RKNN artifacts are required")
	}
	fromID, toID := onnxArtifact.ID, rknnArtifact.ID
	if evidence == nil {
		evidence = map[string]any{}
	}
	evidence["source_sha256"] = normalizeSHA256(onnxArtifact.SHA256)
	evidence["rknn_sha256"] = normalizeSHA256(rknnArtifact.SHA256)
	edge := &domain.MLProvenanceEdge{FromArtifactID: &fromID, ToArtifactID: &toID, EdgeKind: MLProvenanceEdgeConvertedToRKNN, Evidence: evidence, Verified: true}
	if rknnArtifact.ModelVersionID != nil {
		edge.ModelVersionID = rknnArtifact.ModelVersionID
	} else if onnxArtifact.ModelVersionID != nil {
		edge.ModelVersionID = onnxArtifact.ModelVersionID
	}
	return provenance.RecordProvenanceEdge(ctx, edge)
}

type MLRuntimeFactory func(*domain.WorkerRuntimeTarget) (runtimeadapter.Runtime, error)

type RKNNServerDeploymentConfig struct {
	Image         string            `json:"image"`
	Scheme        string            `json:"scheme,omitempty"`
	ContainerPort int               `json:"container_port"`
	HostPort      int               `json:"host_port"`
	BasePath      string            `json:"base_path,omitempty"`
	HealthPath    string            `json:"health_path,omitempty"`
	SmokePath     string            `json:"smoke_path,omitempty"`
	SmokePayload  map[string]any    `json:"smoke_payload,omitempty"`
	Environment   map[string]string `json:"environment,omitempty"`
	Volumes       []string          `json:"volumes,omitempty"`
	Command       []string          `json:"command,omitempty"`
	Entrypoint    []string          `json:"entrypoint,omitempty"`
	WorkingDir    string            `json:"working_dir,omitempty"`
	NetworkMode   string            `json:"network_mode,omitempty"`
	PullAlways    bool              `json:"pull_always,omitempty"`
}

type RKNNServerProvisioner struct {
	runtimeFactory MLRuntimeFactory
	httpClient     *http.Client
}

func NewRKNNServerProvisioner(factory MLRuntimeFactory, httpClient *http.Client) *RKNNServerProvisioner {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 5 * time.Second}
	}
	return &RKNNServerProvisioner{runtimeFactory: factory, httpClient: httpClient}
}

func (p *RKNNServerProvisioner) Provision(ctx context.Context, req MLInferenceProvisionRequest) (*MLInferenceProvisionResult, error) {
	if req.RuntimeKind == "" {
		req.RuntimeKind = domain.MLRuntimeKindRKNNServer
	}
	if req.RuntimeKind != domain.MLRuntimeKindRKNNServer {
		return nil, fmt.Errorf("RKNN provisioner cannot provision runtime %s", req.RuntimeKind)
	}
	if req.Worker == nil || req.Worker.RuntimeTarget == nil {
		return nil, fmt.Errorf("worker runtime_target is required for rknn_server")
	}
	cfg, err := rknnServerConfig(req)
	if err != nil {
		return nil, err
	}
	rknn, err := selectRKNNArtifact(req)
	if err != nil {
		return nil, err
	}
	if p.runtimeFactory == nil {
		return nil, fmt.Errorf("RKNN runtime factory is not configured")
	}
	rt, err := p.runtimeFactory(req.Worker.RuntimeTarget)
	if err != nil {
		return nil, fmt.Errorf("resolve RKNN worker runtime target: %w", err)
	}
	if err := rt.Deploy(ctx, req.TargetName, cfg.Image, rknnDeployOptions(req, cfg, rknn)); err != nil {
		return nil, fmt.Errorf("deploy rknn_server %q: %w", req.TargetName, err)
	}
	endpoint := rknnBackendEndpoint(req.Worker.RuntimeTarget, cfg)
	return &MLInferenceProvisionResult{RuntimeKind: domain.MLRuntimeKindRKNNServer, EndpointRef: req.Worker.RuntimeTarget.EndpointRef, WorkerPubkey: req.Worker.PubKey, WorkerName: req.Worker.Name, BackendEndpoint: endpoint, TargetName: req.TargetName, VerifiedDigests: map[string]string{rknn.URI: normalizeSHA256(rknn.SHA256), rknn.ID.String(): normalizeSHA256(rknn.SHA256)}, Metadata: map[string]any{"protocol": "raw_http", "artifact_uri": rknn.URI, "artifact_format": string(rknn.Format), "health_path": cfg.HealthPath, "smoke_path": cfg.SmokePath}}, nil
}

func (p *RKNNServerProvisioner) Observe(ctx context.Context, req MLInferenceProvisionRequest) (*MLInferenceBackendObservation, error) {
	cfg, err := rknnServerConfig(req)
	if err != nil {
		return nil, err
	}
	endpoint := rknnBackendEndpoint(req.Worker.RuntimeTarget, cfg)
	metadata := map[string]any{"protocol": "raw_http", "health_url": joinURL(endpoint, cfg.HealthPath)}
	health := domain.HealthStatusUnknown
	if p.runtimeFactory != nil && req.Worker != nil && req.Worker.RuntimeTarget != nil {
		if rt, err := p.runtimeFactory(req.Worker.RuntimeTarget); err == nil {
			if obs, err := rt.Observe(ctx, req.Endpoint.ID, req.Intent.EnvironmentID, req.TargetName); err == nil && obs != nil {
				metadata["runtime_health"] = obs.HealthStatus
				metadata["runtime_source"] = obs.Source
				health = obs.HealthStatus
			} else if err != nil {
				metadata["runtime_observe_error"] = err.Error()
			}
		}
	}
	health = p.observeHealth(ctx, metadata["health_url"].(string), health)
	if health == domain.HealthStatusHealthy && cfg.SmokePath != "" {
		if smokeMeta, err := p.runSmoke(ctx, endpoint, cfg); err == nil {
			metadata["smoke"] = smokeMeta
		} else {
			metadata["smoke"] = map[string]any{"passed": false, "error": err.Error()}
			return &MLInferenceBackendObservation{RuntimeKind: domain.MLRuntimeKindRKNNServer, BackendEndpoint: endpoint, HealthStatus: domain.HealthStatusUnhealthy, Source: "rknn_server", Metadata: metadata}, err
		}
	}
	return &MLInferenceBackendObservation{RuntimeKind: domain.MLRuntimeKindRKNNServer, BackendEndpoint: endpoint, HealthStatus: health, Source: "rknn_server", Metadata: metadata}, nil
}

func (p *RKNNServerProvisioner) Deprovision(ctx context.Context, req MLInferenceProvisionRequest) error {
	if req.Worker == nil || req.Worker.RuntimeTarget == nil || p.runtimeFactory == nil {
		return nil
	}
	rt, err := p.runtimeFactory(req.Worker.RuntimeTarget)
	if err != nil {
		return fmt.Errorf("resolve RKNN worker runtime target: %w", err)
	}
	return rt.Undeploy(ctx, req.TargetName)
}

func (p *RKNNServerProvisioner) observeHealth(ctx context.Context, healthURL string, fallback domain.HealthStatus) domain.HealthStatus {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	if err != nil {
		return fallbackOrUnhealthy(fallback)
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return fallbackOrUnhealthy(fallback)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return domain.HealthStatusHealthy
	}
	return domain.HealthStatusUnhealthy
}

func (p *RKNNServerProvisioner) runSmoke(ctx context.Context, endpoint string, cfg RKNNServerDeploymentConfig) (map[string]any, error) {
	payload := cfg.SmokePayload
	if payload == nil {
		payload = map[string]any{"sample_image": "fake://sample-image"}
	}
	body, _ := json.Marshal(payload)
	smokeURL := joinURL(endpoint, cfg.SmokePath)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, smokeURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	limited, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	meta := map[string]any{"passed": resp.StatusCode >= 200 && resp.StatusCode < 300, "smoke_url": smokeURL, "status_code": resp.StatusCode}
	if len(limited) > 0 {
		meta["response"] = string(limited)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return meta, fmt.Errorf("RKNN smoke request failed: %s", resp.Status)
	}
	return meta, nil
}

func rknnServerConfig(req MLInferenceProvisionRequest) (RKNNServerDeploymentConfig, error) {
	var cfg RKNNServerDeploymentConfig
	for _, raw := range []any{endpointMetadataValue(req.Endpoint, "rknn_server"), modelVersionMetadataValue(req.ModelVersion, "rknn_server")} {
		if raw == nil {
			continue
		}
		if err := decodeAny(raw, &cfg); err != nil {
			return cfg, fmt.Errorf("decode rknn_server deployment config: %w", err)
		}
	}
	if cfg.Scheme == "" {
		cfg.Scheme = "http"
	}
	if cfg.HealthPath == "" {
		cfg.HealthPath = defaultRKNNServerHealthPath
	}
	if cfg.SmokePath == "" {
		cfg.SmokePath = defaultRKNNServerSmokePath
	}
	if strings.TrimSpace(cfg.Image) == "" {
		return cfg, fmt.Errorf("rknn_server image is required")
	}
	if cfg.ContainerPort <= 0 || cfg.HostPort <= 0 {
		return cfg, fmt.Errorf("rknn_server host_port and container_port are required")
	}
	return cfg, nil
}

func endpointMetadataValue(endpoint *domain.MLInferenceEndpoint, key string) any {
	if endpoint == nil || endpoint.Metadata == nil {
		return nil
	}
	return endpoint.Metadata[key]
}

func modelVersionMetadataValue(version *domain.MLModelVersion, key string) any {
	if version == nil || version.Metadata == nil {
		return nil
	}
	return version.Metadata[key]
}

func selectRKNNArtifact(req MLInferenceProvisionRequest) (domain.MLArtifactRef, error) {
	wantTarget := expectedRKNNTarget(req)
	var fallback *domain.MLArtifactRef
	for i := range req.Artifacts {
		artifact := req.Artifacts[i]
		if artifact.Format != domain.MLArtifactFormatRKNN {
			continue
		}
		if normalizeSHA256(artifact.SHA256) == "" {
			return domain.MLArtifactRef{}, fmt.Errorf("%w: RKNN artifact %s has no digest", ErrMLProvenanceFailedClosed, artifact.URI)
		}
		if fallback == nil {
			cp := artifact
			fallback = &cp
		}
		if wantTarget == "" || artifactMetadataString(artifact, "target") == wantTarget {
			return artifact, nil
		}
	}
	if fallback != nil && wantTarget == "" {
		return *fallback, nil
	}
	if wantTarget != "" {
		return domain.MLArtifactRef{}, fmt.Errorf("%w: deployment requires an RKNN artifact for target %s", ErrMLProvenanceFailedClosed, wantTarget)
	}
	return domain.MLArtifactRef{}, fmt.Errorf("%w: deployment requires an RKNN artifact", ErrMLProvenanceFailedClosed)
}

func expectedRKNNTarget(req MLInferenceProvisionRequest) string {
	for _, raw := range []any{intentMetadataValue(req.Intent, "rknn_target"), endpointPlacementValue(req.Endpoint, "rknn_target")} {
		if v, ok := stringValue(raw); ok && v != "" {
			return strings.ToLower(v)
		}
	}
	if req.ModelVersion != nil {
		for _, accelerator := range req.ModelVersion.RuntimeRequirements.Accelerators {
			if strings.EqualFold(strings.TrimSpace(accelerator), "npu_rk3588") {
				return "rk3588"
			}
		}
	}
	return ""
}

func artifactMetadataString(artifact domain.MLArtifactRef, key string) string {
	if artifact.Metadata == nil {
		return ""
	}
	if v, ok := stringValue(artifact.Metadata[key]); ok {
		return strings.ToLower(v)
	}
	return ""
}

func intentMetadataValue(intent *domain.MLDeploymentIntent, key string) any {
	if intent == nil || intent.Metadata == nil {
		return nil
	}
	return intent.Metadata[key]
}

func endpointPlacementValue(endpoint *domain.MLInferenceEndpoint, key string) any {
	if endpoint == nil || endpoint.PlacementPolicy == nil {
		return nil
	}
	return endpoint.PlacementPolicy[key]
}

func rknnDeployOptions(req MLInferenceProvisionRequest, cfg RKNNServerDeploymentConfig, artifact domain.MLArtifactRef) runtimeadapter.DeployOptions {
	env := map[string]string{}
	for k, v := range cfg.Environment {
		env[k] = v
	}
	env["BAHIA_ML_RUNTIME"] = string(domain.MLRuntimeKindRKNNServer)
	env["BAHIA_ML_ARTIFACT_URI"] = artifact.URI
	env["BAHIA_ML_ARTIFACT_SHA256"] = normalizeSHA256(artifact.SHA256)
	labels := map[string]string{"bahia.managed": "true", "bahia.ml_runtime": string(domain.MLRuntimeKindRKNNServer), "bahia.ml_artifact": artifact.ID.String()}
	if req.Endpoint != nil {
		labels["bahia.ml_endpoint"] = req.Endpoint.ID.String()
	}
	if req.ModelVersion != nil {
		labels["bahia.ml_model_version"] = req.ModelVersion.ID.String()
	}
	if req.Run != nil {
		labels["bahia.ml_run"] = req.Run.ID.String()
	}
	return runtimeadapter.DeployOptions{Environment: env, Labels: labels, Ports: []string{fmt.Sprintf("%d:%d", cfg.HostPort, cfg.ContainerPort)}, Volumes: append([]string(nil), cfg.Volumes...), Restart: "unless-stopped", Command: append([]string(nil), cfg.Command...), Entrypoint: append([]string(nil), cfg.Entrypoint...), WorkingDir: cfg.WorkingDir, NetworkMode: cfg.NetworkMode, PullAlways: cfg.PullAlways}
}

func rknnBackendEndpoint(target *domain.WorkerRuntimeTarget, cfg RKNNServerDeploymentConfig) string {
	base := ""
	if target != nil {
		base = strings.TrimRight(strings.TrimSpace(target.PublicBaseURL), "/")
	}
	if base == "" {
		base = fmt.Sprintf("%s://localhost:%d", firstNonEmptyString(cfg.Scheme, "http"), cfg.HostPort)
	}
	return joinURL(base, cfg.BasePath)
}

func fallbackOrUnhealthy(fallback domain.HealthStatus) domain.HealthStatus {
	switch fallback {
	case domain.HealthStatusHealthy, domain.HealthStatusUnhealthy, domain.HealthStatusStarting, domain.HealthStatusStopped:
		return fallback
	default:
		return domain.HealthStatusUnhealthy
	}
}

func joinURL(base, path string) string {
	base = strings.TrimRight(base, "/")
	path = strings.TrimSpace(path)
	if path == "" {
		return base
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return base + path
}

func decodeAny(raw any, out any) error {
	body, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}
