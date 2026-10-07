package nostr

import (
	"context"
	"encoding/json"
	"fmt"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"go.uber.org/zap"
)

// AdoptionCanonicalPublisher publishes every record an adoption produces as
// signed canonical cp-state, before the adoption service updates any derived
// index: the adoption binding, the environment registry record
// with its deployment units, the service registry record, the build and
// artifact registry records, the imported secret references, the runtime
// observation and the service state.
//
// Each call signs at most one event and admits it to the control-plane outbox
// before the first relay round. A nil error means the record is accepted, or
// durably queued and being retried per relay; any error means it was never
// admitted, or was abandoned, and the caller must stop. Records are built by
// the family's shared record builder on the envelope controlStateEnvelope
// derives, so adoption replaces exactly the coordinate every other writer of
// the family uses, and an unchanged record is not signed again.
type AdoptionCanonicalPublisher struct {
	projector *Projector
	encryptor ConfidentialStateEncryptor
	logger    *zap.Logger
}

// NewAdoptionCanonicalPublisher creates the publisher. encryptor may be nil;
// it is required only to publish imported secret references, which are
// confidential.
func NewAdoptionCanonicalPublisher(projector *Projector, encryptor ConfidentialStateEncryptor, logger *zap.Logger) *AdoptionCanonicalPublisher {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &AdoptionCanonicalPublisher{
		projector: projector,
		encryptor: encryptor,
		logger:    logger.Named("adoption-canonical"),
	}
}

// SetEncryptor installs the confidential encryptor after construction, for
// wiring where the encryptor is created after the publisher.
func (p *AdoptionCanonicalPublisher) SetEncryptor(encryptor ConfidentialStateEncryptor) {
	p.encryptor = encryptor
}

// publish signs one live plaintext record on the coordinate (legacyKind, d).
func (p *AdoptionCanonicalPublisher) publish(ctx context.Context, legacyKind int, d string, tags gonostr.Tags, content, entityType string, entityID *uuid.UUID) error {
	if p == nil || p.projector == nil || !p.projector.Enabled() {
		return fmt.Errorf("adoption canonical projector is unavailable")
	}
	wireKind, baseTags := controlStateEnvelope(legacyKind, d, false)
	return p.projector.publishAuthoritative(ctx, wireKind, append(baseTags, tags...), content, entityType, entityID)
}

// PublishAdoptionBinding publishes the binding of one adopted workload on
// "adoption:binding:<service>:<environment>".
func (p *AdoptionCanonicalPublisher) PublishAdoptionBinding(ctx context.Context, binding *domain.AdoptionBinding) error {
	if binding == nil || binding.ServiceID == uuid.Nil || binding.EnvironmentID == uuid.Nil {
		return fmt.Errorf("adoption binding service and environment IDs are required")
	}
	tags, content, err := adoptionBindingRecord(binding)
	if err != nil {
		return err
	}
	return p.publish(ctx, KindAdoptionBindingRecord, AdoptionBindingDTag(binding.ServiceID, binding.EnvironmentID), tags, content, "adoption_binding.projection", &binding.ServiceID)
}

// AdoptionBindingDTag returns the d-tag of the adoption binding of a service
// in an environment.
func AdoptionBindingDTag(serviceID, environmentID uuid.UUID) string {
	return kinds.AdoptionBindingDTag(serviceID.String(), environmentID.String())
}

// adoptionBindingRecord returns the family tags and content of a binding.
func adoptionBindingRecord(binding *domain.AdoptionBinding) (gonostr.Tags, string, error) {
	encoded, err := json.Marshal(binding)
	if err != nil {
		return nil, "", fmt.Errorf("marshal adoption binding: %w", err)
	}
	var content map[string]any
	if err := json.Unmarshal(encoded, &content); err != nil {
		return nil, "", fmt.Errorf("marshal adoption binding: %w", err)
	}
	content["deleted"] = false
	contentJSON, err := json.Marshal(content)
	if err != nil {
		return nil, "", fmt.Errorf("marshal adoption binding: %w", err)
	}
	tags := gonostr.Tags{
		{"service", binding.ServiceID.String()},
		{"environment", binding.EnvironmentID.String()},
		{"status", binding.Status},
	}
	if binding.OrgID != uuid.Nil {
		tags = append(tags, gonostr.Tag{"org", binding.OrgID.String()})
	}
	return tags, string(contentJSON), nil
}

// PublishEnvironmentRegistry publishes the environment's registry record with
// units as its complete explicit deployment-unit set.
func (p *AdoptionCanonicalPublisher) PublishEnvironmentRegistry(ctx context.Context, env *domain.Environment, units []domain.DeploymentUnit) error {
	if env == nil || env.ID == uuid.Nil {
		return fmt.Errorf("environment ID is required")
	}
	tags, content := environmentRegistryRecord(env, units, false)
	return p.publish(ctx, KindEnvironmentRegistry, env.ID.String(), tags, content, "environment.projection", &env.ID)
}

// PublishServiceRegistry publishes the service's registry record.
func (p *AdoptionCanonicalPublisher) PublishServiceRegistry(ctx context.Context, svc *domain.Service) error {
	if svc == nil || svc.ID == uuid.Nil {
		return fmt.Errorf("service ID is required")
	}
	tags, content := serviceRegistryRecord(svc, false)
	return p.publish(ctx, KindServiceRegistry, svc.ID.String(), tags, content, "service.projection", &svc.ID)
}

// PublishBuildRegistry publishes the build's registry record.
func (p *AdoptionCanonicalPublisher) PublishBuildRegistry(ctx context.Context, build *domain.Build) error {
	if build == nil || build.ID == uuid.Nil {
		return fmt.Errorf("build ID is required")
	}
	tags, content := buildRegistryRecord(build, false)
	return p.publish(ctx, KindBuildRegistry, build.ID.String(), tags, content, "build.projection", &build.ID)
}

// PublishArtifactRegistry publishes the artifact's registry record.
func (p *AdoptionCanonicalPublisher) PublishArtifactRegistry(ctx context.Context, artifact *domain.Artifact) error {
	if artifact == nil || artifact.ID == uuid.Nil {
		return fmt.Errorf("artifact ID is required")
	}
	tags, content := artifactRegistryRecord(artifact, false)
	return p.publish(ctx, KindArtifactRegistry, artifact.ID.String(), tags, content, "artifact.projection", &artifact.ID)
}

// PublishRuntimeObservation publishes the observation the adoption made of the
// workload on the service's observation coordinate.
func (p *AdoptionCanonicalPublisher) PublishRuntimeObservation(ctx context.Context, obs *domain.RuntimeObservation) error {
	if obs == nil || obs.ID == uuid.Nil || obs.ServiceID == uuid.Nil || obs.EnvironmentID == uuid.Nil {
		return fmt.Errorf("runtime observation, service and environment IDs are required")
	}
	d, tags, record := runtimeObservationRecord(obs, false)
	content, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal runtime observation: %w", err)
	}
	return p.publish(ctx, KindRuntimeObservationState, d, tags, string(content), "runtime_observation.projection", &obs.ID)
}

// PublishServiceState publishes the service's desired and observed state in
// the environment. observation is the observation the state links to.
func (p *AdoptionCanonicalPublisher) PublishServiceState(ctx context.Context, state *domain.EnvironmentServiceState, observation *domain.RuntimeObservation) error {
	if state == nil || state.ServiceID == uuid.Nil || state.EnvironmentID == uuid.Nil {
		return fmt.Errorf("state service and environment IDs are required")
	}
	tags, content := RuntimeStateRecord(state, observation)
	return p.publish(ctx, KindServiceState, ServiceStateDTag(state.ServiceID, state.EnvironmentID), tags, content, "state.projection", &state.ServiceID)
}

// PublishSecretRef publishes the reference of one imported secret, encrypted
// under orgID's content key like every secret reference. The secret value is
// never part of the record.
func (p *AdoptionCanonicalPublisher) PublishSecretRef(ctx context.Context, orgID uuid.UUID, ref domain.SecretRef) error {
	if p == nil || p.projector == nil || !p.projector.Enabled() {
		return fmt.Errorf("adoption canonical projector is unavailable")
	}
	if ref.ID == uuid.Nil || ref.ServiceID == uuid.Nil {
		return fmt.Errorf("secret and service IDs are required")
	}
	if p.encryptor == nil {
		return fmt.Errorf("confidential encryptor not configured; refusing plaintext publish of an imported secret reference")
	}
	dTag := SecretDTag(ref.ID)
	tags, content := SecretRegistryRecord(&ref, false)
	encrypted, err := p.encryptor.EncryptConfidential(ctx, orgID.String(), []byte(content), KindSecretRegistry, dTag, cpStateFamilies[KindSecretRegistry].topic, nil)
	if err != nil {
		return fmt.Errorf("encrypt imported secret reference: %w", err)
	}
	return p.projector.publishCanonicalFirst(ctx, KindSecretRegistry, dTag, false, tags, content, encrypted, "secret.projection", &ref.ID)
}
