package nostr

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// MLCanonicalPublisher publishes authoritative ML state records through the
// shared builder and outbox. It replaces the projector's ML snapshot legs
// (publishMLSnapshots and the ML handleEvent cases) that previously ran on a
// 10-minute timer and reactively from bus events.
//
// The ML registry service calls this after each material mutation, so each
// canonical record is published once per change instead of O(fleet) per tick.
type MLCanonicalPublisher struct {
	projector *Projector
	logger    *zap.Logger
}

// NewMLCanonicalPublisher creates a publisher that delegates to the projector's
// shared signing and outbox pipeline.
func NewMLCanonicalPublisher(projector *Projector, logger *zap.Logger) *MLCanonicalPublisher {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &MLCanonicalPublisher{
		projector: projector,
		logger:    logger.Named("ml-canonical"),
	}
}

// PublishModel publishes a canonical ML model registry record.
func (p *MLCanonicalPublisher) PublishModel(ctx context.Context, model *domain.MLModel) error {
	if p.projector == nil || !p.projector.Enabled() || model == nil || model.Slug == "" {
		return nil
	}
	dTag := "model:" + model.Slug
	tags := gonostr.Tags{{"model", dTag}, {"name", model.Name}, {"deleted", "false"}}
	if model.Family != "" {
		tags = append(tags, gonostr.Tag{"family", model.Family})
	}
	for _, modality := range model.Modalities {
		tags = append(tags, gonostr.Tag{"modality", modality})
	}
	for _, task := range model.TaskKinds {
		tags = append(tags, gonostr.Tag{"task", string(task)})
	}
	for _, capability := range model.Capabilities {
		tags = append(tags, gonostr.Tag{"capability", capability})
	}
	if model.License != "" {
		tags = append(tags, gonostr.Tag{"license", model.License})
	}
	return p.projector.publishReplaceableJSON(ctx, KindMLModelRegistry, dTag, tags, model, "ml_model.projection", &model.ID)
}

func (p *MLCanonicalPublisher) PublishModelTombstone(ctx context.Context, model *domain.MLModel) error {
	if p.projector == nil || !p.projector.Enabled() {
		return nil
	}
	if model == nil || model.Slug == "" {
		return fmt.Errorf("ML model tombstone requires a slug")
	}
	dTag := "model:" + model.Slug
	return p.projector.publishReplaceableTombstone(ctx, KindMLModelRegistry, dTag,
		gonostr.Tags{{"model", dTag}}, map[string]any{"deleted": true, "id": model.ID.String(), "slug": model.Slug, "updated_at": time.Now().UTC().Format(time.RFC3339Nano)}, "ml_model.projection", &model.ID)
}

// PublishModelVersion publishes a canonical ML model version registry record.
func (p *MLCanonicalPublisher) PublishModelVersion(ctx context.Context, version *domain.MLModelVersion) error {
	if p.projector == nil || !p.projector.Enabled() || version == nil {
		return nil
	}
	if p.projector.mlSource == nil {
		return nil
	}
	model, err := p.projector.mlSource.GetModel(ctx, version.ModelID)
	if err != nil || model == nil || model.Slug == "" {
		if err != nil {
			return err
		}
		return nil
	}
	dTag := fmt.Sprintf("model-version:%s:%s", model.Slug, version.Version)
	tags := gonostr.Tags{{"model", "model:" + model.Slug}, {"model_id", version.ModelID.String()}, {"model_version", dTag}, {"version", version.Version}}
	for _, format := range version.RuntimeRequirements.RequiredFormats {
		tags = append(tags, gonostr.Tag{"format", string(format)})
	}
	for _, runtime := range version.RuntimeRequirements.PreferredRuntimes {
		tags = append(tags, gonostr.Tag{"runtime", string(runtime)})
	}
	return p.projector.publishReplaceableJSON(ctx, KindMLModelVersionRegistry, dTag, tags, version, "ml_model_version.projection", &version.ID)
}

func (p *MLCanonicalPublisher) PublishModelVersionTombstone(ctx context.Context, version *domain.MLModelVersion, modelSlug string) error {
	if p.projector == nil || !p.projector.Enabled() {
		return nil
	}
	if version == nil || modelSlug == "" || version.Version == "" {
		return fmt.Errorf("ML model version tombstone requires model slug and version")
	}
	dTag := fmt.Sprintf("model-version:%s:%s", modelSlug, version.Version)
	return p.projector.publishReplaceableTombstone(ctx, KindMLModelVersionRegistry, dTag,
		gonostr.Tags{{"model", "model:" + modelSlug}, {"model_version", dTag}},
		map[string]any{"deleted": true, "id": version.ID.String(), "model_id": version.ModelID.String(), "version": version.Version, "updated_at": time.Now().UTC().Format(time.RFC3339Nano)}, "ml_model_version.projection", &version.ID)
}

// PublishEndpoint publishes a canonical ML inference endpoint registry record.
func (p *MLCanonicalPublisher) PublishEndpoint(ctx context.Context, endpoint *domain.MLInferenceEndpoint) error {
	if p.projector == nil || !p.projector.Enabled() || endpoint == nil {
		return nil
	}
	envName, ok, err := p.environmentName(ctx, endpoint.EnvironmentID)
	if err != nil || !ok {
		return err
	}
	dTag := fmt.Sprintf("endpoint:%s:%s", endpoint.Name, envName)
	tags := gonostr.Tags{{"endpoint", dTag}, {"endpoint_id", endpoint.ID.String()}, {"environment", envName}, {"environment_id", endpoint.EnvironmentID.String()}, {"name", endpoint.Name}}
	for _, task := range endpoint.TaskKinds {
		tags = append(tags, gonostr.Tag{"task", string(task)})
	}
	if endpoint.Protocol != "" {
		tags = append(tags, gonostr.Tag{"protocol", endpoint.Protocol})
	}
	return p.projector.publishReplaceableJSON(ctx, KindMLInferenceEndpointRegistry, dTag, tags, endpoint, "ml_endpoint.projection", &endpoint.ID)
}

func (p *MLCanonicalPublisher) PublishEndpointTombstone(ctx context.Context, endpoint *domain.MLInferenceEndpoint) error {
	if p.projector == nil || !p.projector.Enabled() {
		return nil
	}
	if endpoint == nil {
		return fmt.Errorf("ML endpoint tombstone requires an endpoint")
	}
	envName, ok, err := p.environmentName(ctx, endpoint.EnvironmentID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("ML endpoint environment %s not found", endpoint.EnvironmentID)
	}
	dTag := fmt.Sprintf("endpoint:%s:%s", endpoint.Name, envName)
	return p.projector.publishReplaceableTombstone(ctx, KindMLInferenceEndpointRegistry, dTag,
		gonostr.Tags{{"endpoint", dTag}, {"endpoint_id", endpoint.ID.String()}, {"environment", envName}, {"environment_id", endpoint.EnvironmentID.String()}},
		map[string]any{"deleted": true, "id": endpoint.ID.String(), "name": endpoint.Name, "environment_id": endpoint.EnvironmentID.String(), "updated_at": time.Now().UTC().Format(time.RFC3339Nano)}, "ml_endpoint.projection", &endpoint.ID)
}

// PublishEndpointState publishes a canonical ML inference endpoint state record.
func (p *MLCanonicalPublisher) PublishEndpointState(ctx context.Context, state *domain.MLInferenceState) error {
	if p.projector == nil || !p.projector.Enabled() || state == nil {
		return nil
	}
	if p.projector.mlSource == nil {
		return nil
	}
	endpoint, err := p.projector.mlSource.GetInferenceEndpoint(ctx, state.EndpointID)
	if err != nil || endpoint == nil {
		return err
	}
	envName, ok, err := p.environmentName(ctx, state.EnvironmentID)
	if err != nil || !ok {
		return err
	}
	dTag := fmt.Sprintf("endpoint-state:%s:%s", endpoint.Name, envName)
	tags := gonostr.Tags{{"endpoint", fmt.Sprintf("endpoint:%s:%s", endpoint.Name, envName)}, {"endpoint_id", state.EndpointID.String()}, {"environment", envName}, {"environment_id", state.EnvironmentID.String()}, {"drift_status", string(state.DriftStatus)}, {"gateway_status", string(state.GatewayStatus)}}
	if state.DesiredModelVersionID != nil {
		tags = append(tags, gonostr.Tag{"model_version", state.DesiredModelVersionID.String()})
	}
	if state.DesiredIntentID != nil {
		tags = append(tags, gonostr.Tag{"deployment", state.DesiredIntentID.String()}, gonostr.Tag{"intent", state.DesiredIntentID.String()})
	}
	if state.ActiveRunID != nil {
		tags = append(tags, gonostr.Tag{"run", state.ActiveRunID.String()})
	}
	if state.RuntimeKind != "" {
		tags = append(tags, gonostr.Tag{"runtime", string(state.RuntimeKind)})
	}
	return p.projector.publishReplaceableJSON(ctx, KindMLInferenceEndpointState, dTag, tags, state, "ml_endpoint_state.projection", &state.EndpointID)
}

// PublishProvenanceGraph publishes a canonical ML artifact provenance graph record.
func (p *MLCanonicalPublisher) PublishProvenanceGraph(ctx context.Context, artifact *domain.MLArtifactRef) error {
	if p.projector == nil || !p.projector.Enabled() || artifact == nil {
		return nil
	}
	if p.projector.mlSource == nil {
		return nil
	}
	edges, err := p.projector.mlSource.ListProvenanceEdgesByArtifact(ctx, artifact.ID)
	if err != nil {
		return err
	}
	digest := artifact.SHA256
	if digest == "" {
		digest = artifact.ID.String()
	}
	dTag := "artifact:" + digest
	content := map[string]any{"artifact": artifact, "edges": edges}
	tags := gonostr.Tags{{"artifact", artifact.ID.String()}}
	if artifact.SHA256 != "" {
		tags = append(tags, gonostr.Tag{"sha256", artifact.SHA256})
	}
	if artifact.ModelVersionID != nil {
		tags = append(tags, gonostr.Tag{"model_version", artifact.ModelVersionID.String()})
	}
	if artifact.Format != "" {
		tags = append(tags, gonostr.Tag{"format", string(artifact.Format)})
	}
	return p.projector.publishReplaceableJSON(ctx, KindMLArtifactProvenanceGraph, dTag, tags, content, "ml_artifact_provenance.projection", &artifact.ID)
}

// PublishCapabilityProfile publishes a canonical ML runtime capability profile
// for a worker. Capability profiles reflect worker ML capabilities (runtimes,
// artifact formats, tasks, accelerators). The profile should be published when
// worker state changes; the wiring from worker registration to this method is
// handled by the worker domain (W1).
func (p *MLCanonicalPublisher) PublishCapabilityProfile(ctx context.Context, worker *domain.Worker) error {
	if p.projector == nil || !p.projector.Enabled() || worker == nil || worker.PubKey == "" {
		return nil
	}
	dTag := fmt.Sprintf("worker:%s:ai-capability", worker.PubKey)
	tags := gonostr.Tags{{"worker", worker.PubKey}, {"role", "worker"}, {"status", string(worker.Status)}}
	for _, runtime := range worker.MLCapabilities.Runtimes {
		tags = append(tags, gonostr.Tag{"runtime", string(runtime)})
	}
	for _, format := range worker.MLCapabilities.ArtifactFormats {
		tags = append(tags, gonostr.Tag{"artifact_format", string(format)})
	}
	for _, task := range worker.MLCapabilities.Tasks {
		tags = append(tags, gonostr.Tag{"task", string(task)})
	}
	for _, accelerator := range worker.MLCapabilities.Accelerators {
		tags = append(tags, gonostr.Tag{"accelerator", accelerator})
	}
	for _, toolchain := range worker.MLCapabilities.Toolchains {
		tags = append(tags, gonostr.Tag{"toolchain", toolchain})
	}
	if worker.Resources != nil && worker.Resources.MemoryGB > 0 {
		tags = append(tags, gonostr.Tag{"ram_gb", fmt.Sprintf("%d", worker.Resources.MemoryGB)})
	}
	for _, accelerator := range worker.Accelerators {
		if accelerator.Model != "" {
			tags = append(tags, gonostr.Tag{"gpu", accelerator.Model})
		}
		if accelerator.MemoryGB > 0 {
			tags = append(tags, gonostr.Tag{"vram_gb", fmt.Sprintf("%d", accelerator.MemoryGB)})
		}
		if accelerator.Driver != "" {
			tags = append(tags, gonostr.Tag{"driver", accelerator.Driver})
		}
	}
	return p.projector.publishReplaceableJSON(ctx, KindMLRuntimeCapabilityProfile, dTag, tags, worker, "ml_runtime_capability.projection", nil)
}

func (p *MLCanonicalPublisher) environmentName(ctx context.Context, envID uuid.UUID) (string, bool, error) {
	if envID == uuid.Nil {
		return "", false, nil
	}
	if p.projector.source != nil {
		env, err := p.projector.source.GetEnvironment(ctx, envID)
		if err != nil {
			return "", false, err
		}
		if env != nil && env.Name != "" {
			return env.Name, true, nil
		}
	}
	if p.projector.history != nil {
		records, err := p.projector.history.FindByTag(ctx, "d", envID.String(), []int{KindCASControlState}, 32)
		if err != nil {
			return "", false, err
		}
		for _, record := range records {
			var tags gonostr.Tags
			if err := json.Unmarshal(record.Tags, &tags); err != nil {
				return "", false, err
			}
			var isEnvironment, deleted bool
			for _, tag := range tags {
				if len(tag) < 2 {
					continue
				}
				if tag[0] == "domain" && tag[1] == "environment" {
					isEnvironment = true
				}
				if tag[0] == "deleted" && tag[1] == "true" {
					deleted = true
				}
			}
			if !isEnvironment || deleted {
				continue
			}
			var content struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal([]byte(record.Content), &content); err != nil {
				return "", false, err
			}
			if content.Name != "" {
				return content.Name, true, nil
			}
		}
	}
	return "", false, fmt.Errorf("ML endpoint environment %s has no canonical registry record", envID)
}

// Ensure MLCanonicalPublisher satisfies the service-level interface at
// compile time. The import is intentionally avoided to prevent a dependency
// cycle; the assertion uses a local copy of the constraint.
var _ interface {
	PublishModel(context.Context, *domain.MLModel) error
	PublishModelVersion(context.Context, *domain.MLModelVersion) error
	PublishEndpoint(context.Context, *domain.MLInferenceEndpoint) error
	PublishEndpointState(context.Context, *domain.MLInferenceState) error
	PublishProvenanceGraph(context.Context, *domain.MLArtifactRef) error
	PublishCapabilityProfile(context.Context, *domain.Worker) error
} = (*MLCanonicalPublisher)(nil)
