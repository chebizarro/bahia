package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	runtimeadapter "github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

func TestONNXRKNNValidationPackagingAndProvenanceUsesPinnedGitHubResolver(t *testing.T) {
	ctx := context.Background()
	set := NewDefaultMLArtifactResolverSet(nil, SeaweedFSResolverConfig{})
	commit := "0123456789abcdef0123456789abcdef01234567"
	ref, err := set.ResolveArtifact(ctx, MLArtifactResolveInput{
		URI: "github://rockchip/models@" + commit + "/releases/download/v1/yolo.onnx?sha256=" + resolverTestSHA + "&size=128",
	})
	if err != nil {
		t.Fatalf("resolve GitHub ONNX artifact: %v", err)
	}
	ref.ID = uuid.New()
	if ref.Source == nil || ref.Source.Kind != "github" || ref.Source.Revision != commit || ref.Metadata["pinned_revision"] != true {
		t.Fatalf("expected pinned GitHub metadata, got source=%#v metadata=%#v", ref.Source, ref.Metadata)
	}
	validation, err := ValidateONNXArtifact(ONNXValidationRequest{
		Artifact:        *ref,
		RequirePinned:   true,
		MinOpset:        11,
		MaxOpset:        19,
		AllowedLicenses: []string{"apache-2.0"},
		Metadata:        ONNXModelMetadata{Opset: 13, License: "apache-2.0", Inputs: []ONNXValueInfo{{Name: "image", DataType: "float32", Shape: []int64{1, 3, 224, 224}}}, Outputs: []ONNXValueInfo{{Name: "boxes", DataType: "float32", Shape: []int64{1, 100, 6}}}},
	})
	if err != nil {
		t.Fatalf("validate ONNX: %v", err)
	}
	if validation.Opset != 13 || len(validation.Inputs) != 1 || len(validation.Outputs) != 1 {
		t.Fatalf("unexpected validation result: %#v", validation)
	}

	invalid := *ref
	invalid.Metadata = map[string]any{"pinned_revision": false, "revision": "main"}
	invalid.Source = &domain.MLSourceRef{Kind: "github", URI: invalid.URI, Revision: "main"}
	if _, err := ValidateONNXArtifact(ONNXValidationRequest{Artifact: invalid, RequirePinned: true, Metadata: ONNXModelMetadata{Opset: 13, License: "apache-2.0", Inputs: []ONNXValueInfo{{Name: "image", DataType: "float32"}}, Outputs: []ONNXValueInfo{{Name: "boxes", DataType: "float32"}}}}); err == nil {
		t.Fatalf("expected mutable GitHub ref to fail closed")
	}
	httpsMutable := *ref
	httpsMutable.URI = "https://github.com/rockchip/models/releases/download/v1/yolo.onnx?ref=main&sha256=" + resolverTestSHA
	httpsMutable.Metadata = nil
	httpsMutable.Source = &domain.MLSourceRef{Kind: "http", URI: httpsMutable.URI}
	if _, err := ValidateONNXArtifact(ONNXValidationRequest{Artifact: httpsMutable, RequirePinned: true, Metadata: ONNXModelMetadata{Opset: 13, License: "apache-2.0", Inputs: []ONNXValueInfo{{Name: "image", DataType: "float32"}}, Outputs: []ONNXValueInfo{{Name: "boxes", DataType: "float32"}}}}); err == nil {
		t.Fatalf("expected mutable HTTPS GitHub ref to fail closed")
	}
	if _, err := ValidateONNXArtifact(ONNXValidationRequest{Artifact: *ref, RequirePinned: true, Metadata: ONNXModelMetadata{Opset: 13, License: "apache-2.0", Inputs: []ONNXValueInfo{{Name: "image", DataType: "float32"}}}}); err == nil {
		t.Fatalf("expected missing ONNX outputs to fail closed")
	}

	repo := newCoordinatorMLRepoFake()
	provenance := NewMLProvenanceService(repo, nil, zap.NewNop())
	versionID := uuid.New()
	ref.ModelVersionID = &versionID
	if err := provenance.RegisterArtifactRef(ctx, ref); err != nil {
		t.Fatalf("register ONNX artifact: %v", err)
	}
	rknnBytes := []byte("fake-rknn-output")
	rknn, err := PackageRKNNArtifact(RKNNPackagingRequest{
		SourceONNX:     *ref,
		RKNNURI:        "blossom://rknn/yolo.rknn",
		RKNNBytes:      rknnBytes,
		ModelVersionID: &versionID,
		ToolkitVersion: "2.3.0",
		Target:         "rk3588",
		Quantization:   "int8",
		Preprocess:     map[string]any{"mean": []float64{0, 0, 0}, "std": []float64{255, 255, 255}},
		Postprocess:    map[string]any{"nms": true},
		Calibration:    map[string]any{"dataset_uri": "blossom://calibration/images", "sample_count": 8},
	})
	if err != nil {
		t.Fatalf("package RKNN: %v", err)
	}
	wantSum := sha256.Sum256(rknnBytes)
	if rknn.Format != domain.MLArtifactFormatRKNN || rknn.SHA256 != hex.EncodeToString(wantSum[:]) || rknn.Metadata["preprocess"] == nil || rknn.Metadata["calibration"] == nil {
		t.Fatalf("unexpected RKNN package metadata: %#v", rknn)
	}
	if err := provenance.RegisterArtifactRef(ctx, rknn); err != nil {
		t.Fatalf("register RKNN artifact: %v", err)
	}
	if err := RecordRKNNConversionProvenance(ctx, provenance, ref, rknn, map[string]any{"opset": validation.Opset}); err != nil {
		t.Fatalf("record conversion provenance: %v", err)
	}
	edges, err := repo.ListProvenanceEdgesByArtifact(ctx, rknn.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, edge := range edges {
		if edge.EdgeKind == MLProvenanceEdgeConvertedToRKNN && edge.Verified {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected verified conversion provenance edge, got %#v", edges)
	}
}

func TestRKNNServerProvisioningDeploysRawHTTPSmokeWithFakes(t *testing.T) {
	ctx := context.Background()
	var smokeSeen bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/healthz":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/infer":
			smokeSeen = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"detections":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	repo := newCoordinatorMLRepoFake()
	registry := NewMLRegistryService(repo, nil, zap.NewNop())
	provenance := NewMLProvenanceService(repo, nil, zap.NewNop())
	createdAt := time.Now().UTC()
	modelID := uuid.New()
	versionID := uuid.New()
	model := &domain.MLModel{ID: modelID, Slug: "yolo-rk3588", Name: "YOLO RK3588", Modalities: []string{"vision"}, TaskKinds: []domain.MLTaskKind{domain.MLTaskKindVisionInference}, License: "apache-2.0", CreatedAt: createdAt, UpdatedAt: createdAt}
	if err := registry.CreateOrUpdateModel(ctx, model); err != nil {
		t.Fatalf("model: %v", err)
	}
	version := &domain.MLModelVersion{ID: versionID, ModelID: modelID, Version: "rk3588-v1", Source: domain.MLSourceRef{Kind: "github", URI: "github://rockchip/models@0123456789abcdef0123456789abcdef01234567/releases/download/v1/yolo.onnx", Revision: "0123456789abcdef0123456789abcdef01234567"}, RuntimeRequirements: domain.MLRuntimeRequirements{PreferredRuntimes: []domain.MLRuntimeKind{domain.MLRuntimeKindRKNNServer}, RequiredFormats: []domain.MLArtifactFormat{domain.MLArtifactFormatRKNN}, Accelerators: []string{"npu_rk3588"}, Toolchains: []string{"rknn_toolkit2"}, MinSystemMemoryGB: 8}, Metadata: map[string]any{"rknn_server": map[string]any{"image": "example/rknn-server:fake", "container_port": 8080, "host_port": 18080, "health_path": "/healthz", "smoke_path": "/infer", "smoke_payload": map[string]any{"sample_image": "fake://cat.jpg"}}}, CreatedAt: createdAt}
	if err := registry.CreateOrUpdateModelVersion(ctx, version); err != nil {
		t.Fatalf("version: %v", err)
	}
	onnx := &domain.MLArtifactRef{ID: uuid.New(), ModelVersionID: &versionID, Kind: domain.MLArtifactKindModel, Format: domain.MLArtifactFormatONNX, URI: "github://rockchip/models@0123456789abcdef0123456789abcdef01234567/releases/download/v1/yolo.onnx", SHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", SizeBytes: 128, MediaType: "application/onnx", CreatedAt: createdAt}
	if err := provenance.RegisterArtifactRef(ctx, onnx); err != nil {
		t.Fatalf("ONNX artifact: %v", err)
	}
	wrongTarget := &domain.MLArtifactRef{ID: uuid.New(), ModelVersionID: &versionID, Kind: domain.MLArtifactKindModel, Format: domain.MLArtifactFormatRKNN, URI: "blossom://rknn/yolo-rv1126.rknn", SHA256: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", SizeBytes: 16, MediaType: "application/x-rknn", Metadata: map[string]any{"target": "rv1126"}, CreatedAt: createdAt.Add(time.Millisecond)}
	if err := provenance.RegisterArtifactRef(ctx, wrongTarget); err != nil {
		t.Fatalf("wrong target RKNN artifact: %v", err)
	}
	rknn := &domain.MLArtifactRef{ID: uuid.New(), ModelVersionID: &versionID, Kind: domain.MLArtifactKindModel, Format: domain.MLArtifactFormatRKNN, URI: "blossom://rknn/yolo.rknn", SHA256: resolverTestSHA, SizeBytes: 16, MediaType: "application/x-rknn", Metadata: map[string]any{"target": "rk3588", "preprocess": map[string]any{"resize": 224}, "postprocess": map[string]any{"topk": 5}, "calibration": map[string]any{"sample_count": 8}}, CreatedAt: createdAt.Add(2 * time.Millisecond)}
	if err := provenance.RegisterArtifactRef(ctx, rknn); err != nil {
		t.Fatalf("artifact: %v", err)
	}
	endpoint := &domain.MLInferenceEndpoint{ID: uuid.New(), Name: "yolo-edge", EnvironmentID: uuid.New(), TaskKinds: []domain.MLTaskKind{domain.MLTaskKindVisionInference}, Protocol: "raw_http", PlacementPolicy: map[string]any{"accelerator": "npu_rk3588", "min_system_memory_gb": 8}}
	if err := registry.CreateOrUpdateInferenceEndpoint(ctx, endpoint); err != nil {
		t.Fatalf("endpoint: %v", err)
	}
	intent := &domain.MLDeploymentIntent{ID: uuid.New(), EndpointID: endpoint.ID, EnvironmentID: endpoint.EnvironmentID, ModelVersionID: versionID, RequestedBy: "tester", SourceKind: domain.SourceKindManual, ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusApproved, RuntimePreference: domain.MLRuntimeKindRKNNServer, CreatedAt: createdAt, UpdatedAt: createdAt}
	if err := registry.CreateDeploymentIntent(ctx, intent); err != nil {
		t.Fatalf("intent: %v", err)
	}
	worker := mlWorker("pk-rk3588", "rk3588-edge", 0, domain.WorkerMLCapabilities{Tasks: []domain.MLTaskKind{domain.MLTaskKindVisionInference}, Runtimes: []domain.MLRuntimeKind{domain.MLRuntimeKindRKNNServer}, ArtifactFormats: []domain.MLArtifactFormat{domain.MLArtifactFormatRKNN}, Accelerators: []string{"npu_rk3588"}, Toolchains: []string{"rknn_toolkit2"}})
	worker.Resources = &domain.WorkerResources{MemoryGB: 16}
	worker.Accelerators = []domain.WorkerAccelerator{{Vendor: "rockchip", Model: "rk3588", Count: 1}}
	worker.RuntimeTarget = &domain.WorkerRuntimeTarget{Type: domain.RuntimeTypeDocker, EndpointRef: "edge-docker", PublicBaseURL: srv.URL}
	placement := NewMLPlacementService(&mockWorkerRepo{workers: []domain.Worker{worker}}, zap.NewNop())
	runtimeFake := &fakeRKNNRuntime{}
	provisioner := NewRKNNServerProvisioner(func(*domain.WorkerRuntimeTarget) (runtimeadapter.Runtime, error) { return runtimeFake, nil }, srv.Client())
	responder := &captureMLProvisioningResponder{}
	coordinator := NewMLInferenceProvisioningCoordinator(registry, placement, provenance, StaticMLInferenceProvisionerResolver{domain.MLRuntimeKindRKNNServer: provisioner}, zap.NewNop(), WithMLInferenceProvisioningResponder(responder))

	if err := coordinator.ProcessOnce(ctx); err != nil {
		t.Fatalf("process RKNN deployment: %v", err)
	}
	if !smokeSeen {
		t.Fatalf("expected raw HTTP smoke endpoint to be called")
	}
	run := onlyRun(t, repo)
	if run.Status != domain.RunStatusSucceeded || run.RuntimeKind != domain.MLRuntimeKindRKNNServer || run.BackendEndpoint != srv.URL {
		t.Fatalf("unexpected RKNN run: %#v", run)
	}
	if len(runtimeFake.deploys) != 1 || runtimeFake.deploys[0].image != "example/rknn-server:fake" || runtimeFake.deploys[0].opts.Environment["BAHIA_ML_ARTIFACT_SHA256"] != resolverTestSHA || runtimeFake.deploys[0].opts.Environment["BAHIA_ML_ARTIFACT_URI"] != rknn.URI {
		t.Fatalf("unexpected runtime deployment: %#v", runtimeFake.deploys)
	}
	state, _ := registry.GetInferenceState(ctx, endpoint.ID, endpoint.EnvironmentID)
	if state == nil || state.RuntimeKind != domain.MLRuntimeKindRKNNServer || state.BackendHealth != domain.HealthStatusHealthy || state.GatewayStatus != domain.GatewayRouteStatusSynced || state.DriftStatus != domain.DriftStatusInSync {
		t.Fatalf("expected healthy raw HTTP RKNN state, got %#v", state)
	}
	obs, _ := repo.GetLatestInferenceObservation(ctx, endpoint.ID, endpoint.EnvironmentID)
	if obs == nil || obs.Metadata["backend"] == nil || !strings.Contains(fmt.Sprint(obs.Metadata), "raw_http") || !strings.Contains(fmt.Sprint(obs.Metadata), "smoke") {
		t.Fatalf("expected raw HTTP/smoke observation metadata, got %#v", obs)
	}
	if len(responder.results) != 1 || responder.results[0] != "succeeded" {
		t.Fatalf("expected succeeded Nostr lifecycle result, got %#v", responder.results)
	}
}

type fakeRKNNRuntime struct {
	mu      sync.Mutex
	deploys []fakeRKNNDeploy
}

type fakeRKNNDeploy struct {
	name  string
	image string
	opts  runtimeadapter.DeployOptions
}

func (r *fakeRKNNRuntime) Type() domain.RuntimeType { return domain.RuntimeTypeDocker }
func (r *fakeRKNNRuntime) Deploy(_ context.Context, name, image string, opts runtimeadapter.DeployOptions) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deploys = append(r.deploys, fakeRKNNDeploy{name: name, image: image, opts: opts})
	return nil
}
func (r *fakeRKNNRuntime) Undeploy(context.Context, string) error { return nil }
func (r *fakeRKNNRuntime) StreamLogs(context.Context, string, runtimeadapter.LogOptions) (<-chan runtimeadapter.LogEntry, error) {
	ch := make(chan runtimeadapter.LogEntry)
	close(ch)
	return ch, nil
}
func (r *fakeRKNNRuntime) Observe(_ context.Context, serviceID, envID uuid.UUID, _ string) (*domain.RuntimeObservation, error) {
	return &domain.RuntimeObservation{ID: uuid.New(), ServiceID: serviceID, EnvironmentID: envID, HealthStatus: domain.HealthStatusHealthy, Source: "fake-rknn-runtime", ObservedAt: time.Now().UTC()}, nil
}
