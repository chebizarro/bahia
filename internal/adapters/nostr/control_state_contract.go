package nostr

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"

	"github.com/openagentsinc/bahia/internal/domain"
)

// The functions below expose the projector's own envelope and tag builders to
// producers outside this package that must emit byte-identical control-state
// records, such as the cmd/bahia-test-relay seed corpus. They add no policy:
// each one forwards to the builder the projector publishes with, so a change
// to the producer contract changes every caller at once.

// ControlStateEnvelope returns the wire kind and envelope tags (d, domain,
// schema, legacy_kind, deleted) the projector stamps on the record identified
// by (legacyKind, id). See controlStateEnvelope.
func ControlStateEnvelope(legacyKind int, id string, deleted bool) (wireKind int, tags gonostr.Tags) {
	return controlStateEnvelope(legacyKind, id, deleted)
}

// DNSEndpointTags returns the per-endpoint tags (family, health, dns, addr,
// the dns-endpoint t topic, npub/mesh, ...) the projector appends to a live
// DNS endpoint record's envelope.
func DNSEndpointTags(endpoint domain.DNSEndpoint) gonostr.Tags {
	return dnsEndpointTags(endpoint)
}

// DNSZoneDTag, DNSBackendDTag and DNSPolicyDTag return the record ids the
// projector uses for DNS zone, backend and policy state.
func DNSZoneDTag(name string) string { return dnsZoneDTag(name) }

func DNSBackendDTag(ref string) string { return dnsBackendDTag(ref) }

func DNSPolicyDTag(id uuid.UUID) string { return dnsPolicyDTag(id) }

// Service and environment registry records ----------------------------------
//
// The service-registry and environment-registry cp-state coordinates have two
// writers until Phase 3 removes the dual write: the projector, and the
// relay-first registry (internal/service.RelayFirstRegistry), which publishes
// before it writes the local cache. Both build the record here, on the
// envelope controlStateEnvelope derives, so for one entity state they emit one
// tag set and one content serialization (bahia-irsry.41, audit B-5). Before,
// the relay-first record had no t or legacy_kind tag, so #t consumers missed
// it, and its content differed (org_id, repository, timestamp precision), so
// the coordinate alternated between two shapes.

// serviceRegistryRecord returns the family tags and content of a service's
// registry record. svc must already carry the read normalization readers see
// (RegistryService.GetService/ListServices), which is what both writers pass.
func serviceRegistryRecord(svc *domain.Service, deleted bool) (gonostr.Tags, string) {
	content := map[string]any{
		"deleted": deleted,
		"id":      svc.ID.String(),
	}
	putRecordTime(content, "updated_at", svc.UpdatedAt)
	tags := gonostr.Tags{}
	if !deleted {
		if svc.OrgID != uuid.Nil {
			content["org_id"] = svc.OrgID.String()
		}
		content["name"] = svc.Name
		content["repo_url"] = svc.RepoURL
		if svc.Repository != nil {
			content["repository"] = svc.Repository
		}
		content["artifact_repo"] = svc.ArtifactRepo
		content["default_branch"] = svc.DefaultBranch
		content["runtime_type"] = string(svc.RuntimeType)
		putRecordTime(content, "created_at", svc.CreatedAt)
		tags = append(tags,
			gonostr.Tag{"name", svc.Name},
			gonostr.Tag{"runtime", string(svc.RuntimeType)},
		)
	}
	contentJSON, _ := json.Marshal(content)
	return tags, string(contentJSON)
}

// environmentRegistryRecord returns the family tags and content of an
// environment's registry record. units is the environment's explicit
// deployment-unit set; when it is empty the record carries the implicit
// default unit instead. The relay-first registry passes the set it is about
// to store and the projector the set it reads back (environmentRecordUnits),
// so both writers emit one record for one state (bahia-irsry.53).
func environmentRegistryRecord(env *domain.Environment, units []domain.DeploymentUnit, deleted bool) (gonostr.Tags, string) {
	snapshot := *env
	domain.NormalizeEnvironmentTargeting(&snapshot)
	content := map[string]any{
		"deleted": deleted,
		"id":      snapshot.ID.String(),
	}
	putRecordTime(content, "updated_at", snapshot.UpdatedAt)
	tags := gonostr.Tags{}
	if !deleted {
		if snapshot.OrgID != uuid.Nil {
			content["org_id"] = snapshot.OrgID.String()
		}
		content["name"] = snapshot.Name
		content["loom_worker_selector"] = recordObject(snapshot.LoomWorkerSelector)
		content["runtime_config"] = recordObject(snapshot.RuntimeConfig)
		content["protected"] = snapshot.Protected
		content["deploy_strategy"] = string(snapshot.DeployStrategy)
		content["targeting"] = snapshot.Targeting
		content["deployment_units"] = recordDeploymentUnits(snapshot.Targeting.DefaultUnitKey, units)
		content["reconcile_mode"] = string(snapshot.Targeting.DefaultReconcileMode)
		putRecordTime(content, "created_at", snapshot.CreatedAt)
		tags = append(tags,
			gonostr.Tag{"name", snapshot.Name},
			gonostr.Tag{"protected", fmt.Sprintf("%t", snapshot.Protected)},
			gonostr.Tag{"unit", snapshot.Targeting.DefaultUnitKey},
			gonostr.Tag{"reconcile_mode", string(snapshot.Targeting.DefaultReconcileMode)},
		)
	}
	contentJSON, _ := json.Marshal(content)
	return tags, string(contentJSON)
}

// recordDeploymentUnits is the deployment_units content of an environment
// record: each explicit unit's id and declared fields, sorted by key, or the
// implicit default unit when there are none. Unit ids are minted by the
// registry before the relay-first record is signed (and kept by the
// repository), so both writers know them; timestamps are left out because
// the relay-first record is signed before the repository stamps them.
func recordDeploymentUnits(defaultKey string, units []domain.DeploymentUnit) []map[string]any {
	if len(units) == 0 {
		return []map[string]any{{"key": defaultKey, "implicit": true}}
	}
	sorted := append([]domain.DeploymentUnit(nil), units...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Key < sorted[j].Key })
	out := make([]map[string]any, 0, len(sorted))
	for _, unit := range sorted {
		domain.NormalizeDeploymentUnitTargeting(&unit)
		record := map[string]any{
			"key":            unit.Key,
			"implicit":       false,
			"runtime_type":   string(unit.RuntimeType),
			"reconcile_mode": string(unit.ReconcileMode),
			"ownership_mode": string(unit.OwnershipMode),
		}
		if unit.ID != uuid.Nil {
			record["id"] = unit.ID.String()
		}
		putRecordString(record, "display_name", unit.DisplayName)
		putRecordString(record, "endpoint_ref", unit.EndpointRef)
		putRecordString(record, "compose_dir", unit.ComposeDir)
		putRecordString(record, "namespace", unit.Namespace)
		if len(unit.NetworkProfile) > 0 {
			record["network_profile"] = unit.NetworkProfile
		}
		if unit.GitSource != nil && *unit.GitSource != (domain.GitSourceBinding{}) {
			record["git_source"] = unit.GitSource
		}
		if len(unit.RuntimeConfig) > 0 {
			record["runtime_config"] = unit.RuntimeConfig
		}
		out = append(out, record)
	}
	return out
}

func putRecordString(content map[string]any, key, value string) {
	if value != "" {
		content[key] = value
	}
}

// EnvironmentDeploymentUnitSource is the projector's source of an
// environment's explicit deployment units (service.RegistryService). A
// ProjectionSource that does not implement it has none, and its environment
// records carry the implicit default unit.
type EnvironmentDeploymentUnitSource interface {
	ListEnvironmentDeploymentUnits(ctx context.Context, environmentID uuid.UUID) ([]domain.DeploymentUnit, error)
}

// environmentRecordUnits reads the explicit units the projector's record of
// env carries. A failed read is an error rather than an empty set: publishing
// the implicit default would replace the record's real units.
func (p *Projector) environmentRecordUnits(ctx context.Context, env *domain.Environment, deleted bool) ([]domain.DeploymentUnit, error) {
	if deleted || env == nil {
		return nil, nil
	}
	source, ok := p.source.(EnvironmentDeploymentUnitSource)
	if !ok || source == nil {
		return nil, nil
	}
	units, err := source.ListEnvironmentDeploymentUnits(ctx, env.ID)
	if err != nil {
		return nil, fmt.Errorf("read deployment units of environment %s: %w", env.ID, err)
	}
	return units, nil
}

// putRecordTime stores a registry timestamp. updated_at is the revision
// clients send back as expected_updated_at, so it must survive the round trip
// exactly: it is written at the precision Postgres stores
// (domain.NormalizeRevisionTime), which writers also mint revisions at, so the
// token read from the relay matches the database. A zero time is omitted
// rather than written as "", which a time.Time decoder rejects.
func putRecordTime(content map[string]any, key string, t time.Time) {
	if t.IsZero() {
		return
	}
	content[key] = domain.NormalizeRevisionTime(t).Format(time.RFC3339Nano)
}

// recordObject makes a nil and an empty free-form map serialize alike, so a
// write intent (nil) and the cached row ({}) produce the same record.
func recordObject(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

// PreCommitPublisher delivers a signed event before the caller commits the
// state it records, and fails, leaving nothing queued, unless the publish
// quorum accepted it. Publisher.PublishBeforeCommit implements it.
type PreCommitPublisher interface {
	PublishBeforeCommit(ctx context.Context, ev gonostr.Event, entityType string, entityID *uuid.UUID) error
}

// RelayFirstStatePublisher is the relay-first registry's writer of service
// and environment registry records (service.RelayFirstStatePublisher). It
// builds each record with the projector's builders and signs it under the
// projector's per-coordinate lock, created_at floor and fingerprint memory:
// a later projection of the same state is not signed again, and any later
// event on the coordinate (from either writer) is strictly newer.
//
// It delivers through the control-plane publisher's PublishBeforeCommit
// rather than PublishProjection: the caller writes its cache only once the
// quorum accepted, so a rejected write must leave nothing queued, while an
// accepted one is handed to the outbox, which retries the relays that have
// not accepted yet.
type RelayFirstStatePublisher struct {
	projector *Projector
	publisher PreCommitPublisher
}

// NewRelayFirstStatePublisher returns the relay-first writer that shares
// projector's coordinate state and delivers through publisher (the
// control-plane publisher the projector publishes with).
func NewRelayFirstStatePublisher(projector *Projector, publisher PreCommitPublisher) *RelayFirstStatePublisher {
	return &RelayFirstStatePublisher{projector: projector, publisher: publisher}
}

// PublishServiceRegistry publishes svc's service-registry record (or its
// tombstone when deleted).
func (r *RelayFirstStatePublisher) PublishServiceRegistry(ctx context.Context, svc *domain.Service, deleted bool) error {
	if svc == nil {
		return fmt.Errorf("service is nil")
	}
	tags, content := serviceRegistryRecord(svc, deleted)
	return r.publish(ctx, KindServiceRegistry, svc.ID, deleted, tags, content, "service.projection")
}

// PublishEnvironmentRegistry publishes env's environment-registry record,
// with its explicit deployment units, or its tombstone when deleted.
func (r *RelayFirstStatePublisher) PublishEnvironmentRegistry(ctx context.Context, env *domain.Environment, units []domain.DeploymentUnit, deleted bool) error {
	if env == nil {
		return fmt.Errorf("environment is nil")
	}
	tags, content := environmentRegistryRecord(env, units, deleted)
	return r.publish(ctx, KindEnvironmentRegistry, env.ID, deleted, tags, content, "environment.projection")
}

func (r *RelayFirstStatePublisher) publish(ctx context.Context, legacyKind int, id uuid.UUID, deleted bool, tags gonostr.Tags, content, entityType string) error {
	if r == nil || r.projector == nil {
		return fmt.Errorf("relay-first state publisher is not configured")
	}
	if r.publisher == nil {
		return fmt.Errorf("relay-first state publisher has no publisher")
	}
	wireKind, baseTags := controlStateEnvelope(legacyKind, id.String(), deleted)
	return r.projector.publishSignedRelayFirst(ctx, wireKind, append(baseTags, tags...), content, entityType, &id, r.publisher)
}

// Build, artifact, deployment intent and deployment run registry records -----
//
// Phase 3 S2: these families publish their canonical cp-state directly from
// the code that mutates them (RegistryService), through the shared record
// builder and the outbox, exactly one event per material change. The
// functions below are the single record builders both the projector's (now
// deleted) handleEvent path and the authoritative publisher use.

// buildRegistryRecord returns the family tags and JSON content of a build's
// registry record.
func buildRegistryRecord(build *domain.Build, deleted bool) (gonostr.Tags, string) {
	if deleted {
		content := map[string]any{"deleted": true, "id": build.ID.String()}
		contentJSON, _ := json.Marshal(content)
		return gonostr.Tags{}, string(contentJSON)
	}
	tags := gonostr.Tags{
		{"service", build.ServiceID.String()},
		{"build", build.ID.String()},
		{"status", string(build.Status)},
	}
	content := map[string]any{
		"deleted":         false,
		"id":              build.ID.String(),
		"service_id":      build.ServiceID.String(),
		"git_sha":         build.GitSHA,
		"git_ref":         build.GitRef,
		"ci_system":       build.CISystem,
		"ci_run_id":       build.CIRunID,
		"loom_job_id":     build.LoomJobID,
		"status":          string(build.Status),
		"source_event_id": build.SourceEventID,
		"started_at":      build.StartedAt,
		"finished_at":     build.FinishedAt,
		"metadata":        build.Metadata,
		"created_at":      formatTime(build.CreatedAt),
	}
	contentJSON, _ := json.Marshal(content)
	return tags, string(contentJSON)
}

// artifactRegistryRecord returns the family tags and JSON content of an
// artifact's registry record.
func artifactRegistryRecord(artifact *domain.Artifact, deleted bool) (gonostr.Tags, string) {
	if deleted {
		content := map[string]any{"deleted": true, "id": artifact.ID.String()}
		contentJSON, _ := json.Marshal(content)
		return gonostr.Tags{}, string(contentJSON)
	}
	tags := gonostr.Tags{
		{"service", artifact.ServiceID.String()},
		{"artifact", artifact.ID.String()},
		{"build", artifact.BuildID.String()},
	}
	content := map[string]any{
		"deleted":             false,
		"id":                  artifact.ID.String(),
		"build_id":            artifact.BuildID.String(),
		"service_id":          artifact.ServiceID.String(),
		"image_repo":          artifact.ImageRepo,
		"image_tag":           artifact.ImageTag,
		"image_digest":        artifact.ImageDigest,
		"manifest_media_type": artifact.ManifestMediaType,
		"size_bytes":          artifact.SizeBytes,
		"sbom_url":            artifact.SBOMURL,
		"signature_ref":       artifact.SignatureRef,
		"scan_status":         string(artifact.ScanStatus),
		"metadata":            artifact.Metadata,
		"created_at":          formatTime(artifact.CreatedAt),
	}
	contentJSON, _ := json.Marshal(content)
	return tags, string(contentJSON)
}

// deploymentIntentRegistryRecord returns the family tags and JSON content of
// a deployment intent's registry record.
func deploymentIntentRegistryRecord(intent *domain.DeploymentIntent, deleted bool) (gonostr.Tags, string) {
	if deleted {
		content := map[string]any{"deleted": true, "id": intent.ID.String()}
		contentJSON, _ := json.Marshal(content)
		return gonostr.Tags{}, string(contentJSON)
	}
	tags := gonostr.Tags{
		{"service", intent.ServiceID.String()},
		{"environment", intent.EnvironmentID.String()},
		{"artifact", intent.ArtifactID.String()},
		{"intent", intent.ID.String()},
		{"status", string(intent.Status)},
		{"approval", string(intent.ApprovalStatus)},
		{"unit", unitTagValue(intent.DeploymentUnitID)},
	}
	content := map[string]any{
		"deleted":            false,
		"id":                 intent.ID.String(),
		"service_id":         intent.ServiceID.String(),
		"environment_id":     intent.EnvironmentID.String(),
		"deployment_unit_id": uuidStringPtr(intent.DeploymentUnitID),
		"artifact_id":        intent.ArtifactID.String(),
		"requested_by":       intent.RequestedBy,
		"source_kind":        string(intent.SourceKind),
		"approval_status":    string(intent.ApprovalStatus),
		"status":             string(intent.Status),
		"deployment_status":  string(intent.Status),
		"approval_metadata":  intent.ApprovalMetadata,
		"metadata":           intent.Metadata,
		"created_at":         formatTime(intent.CreatedAt),
		"approved_at":        intent.ApprovedAt,
		"updated_at":         formatTime(intent.UpdatedAt),
	}
	if intent.SupersedesIntentID != nil {
		content["supersedes_intent_id"] = intent.SupersedesIntentID.String()
	}
	if intent.Metadata != nil {
		for _, key := range []string{"artifact_digest", "deployment_target", "policy"} {
			if value, ok := intent.Metadata[key]; ok {
				content[key] = value
			}
		}
	}
	if intent.DesiredHash != "" {
		content["desired_hash"] = intent.DesiredHash
		tags = append(tags, gonostr.Tag{"desired_hash", intent.DesiredHash})
	}
	if intent.DesiredState != nil {
		if renderer := desiredStateRenderer(intent.DesiredState); renderer != "" {
			content["renderer"] = renderer
		}
		if target := desiredStateTarget(intent.DesiredState); target != "" {
			content["target"] = target
		}
	}
	contentJSON, _ := json.Marshal(content)
	return tags, string(contentJSON)
}

// deploymentRunRegistryRecord returns the family tags and JSON content of a
// deployment run's registry record.
func deploymentRunRegistryRecord(run *domain.DeploymentRun, deleted bool) (gonostr.Tags, string) {
	if deleted {
		content := map[string]any{"deleted": true, "id": run.ID.String()}
		contentJSON, _ := json.Marshal(content)
		return gonostr.Tags{}, string(contentJSON)
	}
	tags := gonostr.Tags{
		{"intent", run.DeploymentIntentID.String()},
		{"run", run.ID.String()},
		{"status", string(run.Status)},
		{"unit", unitTagValue(run.DeploymentUnitID)},
	}
	content := map[string]any{
		"deleted":              false,
		"id":                   run.ID.String(),
		"deployment_intent_id": run.DeploymentIntentID.String(),
		"deployment_unit_id":   uuidStringPtr(run.DeploymentUnitID),
		"loom_job_id":          run.LoomJobID,
		"worker_pubkey":        run.WorkerPubkey,
		"worker_name":          run.WorkerName,
		"status":               string(run.Status),
		"exit_code":            run.ExitCode,
		"stdout_ref":           run.StdoutRef,
		"stderr_ref":           run.StderrRef,
		"started_at":           run.StartedAt,
		"finished_at":          run.FinishedAt,
		"metadata":             run.Metadata,
		"created_at":           formatTime(run.CreatedAt),
		"updated_at":           formatTime(run.UpdatedAt),
	}
	if run.ApplyMetadata != nil {
		if renderer, ok := run.ApplyMetadata["renderer"].(string); ok && renderer != "" {
			content["renderer"] = renderer
			tags = append(tags, gonostr.Tag{"renderer", renderer})
		}
		if desiredHash, ok := run.ApplyMetadata["desired_hash"].(string); ok && desiredHash != "" {
			content["desired_hash"] = desiredHash
		}
		if revisionHash, ok := run.ApplyMetadata["revision_hash"].(string); ok && revisionHash != "" {
			content["revision_hash"] = revisionHash
		}
		if target, ok := run.ApplyMetadata["target"].(string); ok && target != "" {
			content["target"] = target
		}
		if applySummary, ok := run.ApplyMetadata["apply_summary"].(string); ok && applySummary != "" {
			content["apply_summary"] = applySummary
		}
		if obsID, ok := run.ApplyMetadata["observation_id"].(string); ok && obsID != "" {
			content["observation_id"] = obsID
		}
		for _, key := range []string{"phase", "phase_sequence", "phases", "failure", "health_status", "deployment_unit_key", "endpoint_ref", "artifact_digest"} {
			if value, ok := run.ApplyMetadata[key]; ok {
				content[key] = value
			}
		}
	}
	contentJSON, _ := json.Marshal(content)
	return tags, string(contentJSON)
}

// PublishBuildRegistry publishes a build's registry record (or its tombstone).
// Phase 3 S2: canonical state published directly from the mutation site.
func (r *RelayFirstStatePublisher) PublishBuildRegistry(ctx context.Context, build *domain.Build, deleted bool) error {
	if build == nil {
		return fmt.Errorf("build is nil")
	}
	tags, content := buildRegistryRecord(build, deleted)
	return r.publishAuthoritativeProjection(ctx, KindBuildRegistry, build.ID, deleted, tags, content, "build.projection")
}

// PublishArtifactRegistry publishes an artifact's registry record.
func (r *RelayFirstStatePublisher) PublishArtifactRegistry(ctx context.Context, artifact *domain.Artifact, deleted bool) error {
	if artifact == nil {
		return fmt.Errorf("artifact is nil")
	}
	tags, content := artifactRegistryRecord(artifact, deleted)
	return r.publishAuthoritativeProjection(ctx, KindArtifactRegistry, artifact.ID, deleted, tags, content, "artifact.projection")
}

// PublishDeploymentIntentRegistry publishes a deployment intent's registry
// record.
func (r *RelayFirstStatePublisher) PublishDeploymentIntentRegistry(ctx context.Context, intent *domain.DeploymentIntent, deleted bool) error {
	if intent == nil {
		return fmt.Errorf("deployment intent is nil")
	}
	tags, content := deploymentIntentRegistryRecord(intent, deleted)
	return r.publishAuthoritativeProjection(ctx, KindDeploymentIntentRegistry, intent.ID, deleted, tags, content, "deployment_intent.projection")
}

// PublishDeploymentRunRegistry publishes a deployment run's registry record.
func (r *RelayFirstStatePublisher) PublishDeploymentRunRegistry(ctx context.Context, run *domain.DeploymentRun, deleted bool) error {
	if run == nil {
		return fmt.Errorf("deployment run is nil")
	}
	tags, content := deploymentRunRegistryRecord(run, deleted)
	return r.publishAuthoritativeProjection(ctx, KindDeploymentRunRegistry, run.ID, deleted, tags, content, "deployment_run.projection")
}

// Package registry records ----------------------------------------------------
//
// Phase 3 P1: package repository, artifact and promotion families publish
// canonical cp-state directly from the intent handler, through the shared
// record builder and the outbox, exactly one event per material change.

// packageRepositoryRegistryRecord returns the family tags and JSON content of
// a package repository's registry record.
func packageRepositoryRegistryRecord(repo *domain.PackageRepository, deleted bool) (gonostr.Tags, string) {
	if deleted {
		content := map[string]any{"deleted": true, "id": repo.ID.String()}
		contentJSON, _ := json.Marshal(content)
		return gonostr.Tags{}, string(contentJSON)
	}
	tags := gonostr.Tags{
		{"repository", repo.ID.String()},
		{"name", repo.Name},
		{"backend_ref", repo.BackendRef},
		{"format", string(repo.Format)},
		{"status", string(repo.Status)},
	}
	content := map[string]any{
		"deleted":                  false,
		"id":                      repo.ID.String(),
		"name":                    repo.Name,
		"backend_ref":             repo.BackendRef,
		"backend_type":            string(repo.BackendType),
		"format":                  string(repo.Format),
		"status":                  string(repo.Status),
		"public_url":              repo.PublicURL,
		"external_repository_name": repo.ExternalRepositoryName,
		"created_at":              formatTime(repo.CreatedAt),
		"updated_at":              formatTime(repo.UpdatedAt),
	}
	contentJSON, _ := json.Marshal(content)
	return tags, string(contentJSON)
}

// packageArtifactRegistryRecord returns the family tags and JSON content of a
// package artifact's registry record.
func packageArtifactRegistryRecord(artifact *domain.PackageArtifact, deleted bool) (gonostr.Tags, string) {
	if deleted {
		content := map[string]any{"deleted": true, "id": artifact.ID.String()}
		contentJSON, _ := json.Marshal(content)
		return gonostr.Tags{}, string(contentJSON)
	}
	tags := gonostr.Tags{
		{"artifact", artifact.ID.String()},
		{"repository", artifact.RepositoryID.String()},
		{"repository_name", artifact.RepositoryName},
		{"package", artifact.PackageName},
		{"version", artifact.Version},
		{"filename", artifact.Filename},
		{"sha256", artifact.SHA256},
		{"status", string(artifact.Status)},
	}
	content := map[string]any{
		"deleted":         false,
		"id":              artifact.ID.String(),
		"repository_id":   artifact.RepositoryID.String(),
		"repository_name": artifact.RepositoryName,
		"namespace":       artifact.Namespace,
		"package_name":    artifact.PackageName,
		"version":         artifact.Version,
		"filename":        artifact.Filename,
		"sha256":          artifact.SHA256,
		"size_bytes":      artifact.SizeBytes,
		"content_type":    artifact.ContentType,
		"status":          string(artifact.Status),
		"metadata":        artifact.Metadata,
		"created_at":      formatTime(artifact.CreatedAt),
		"updated_at":      formatTime(artifact.UpdatedAt),
	}
	contentJSON, _ := json.Marshal(content)
	return tags, string(contentJSON)
}

// packagePromotionRegistryRecord returns the family tags and JSON content of a
// package promotion/publication's registry record.
func packagePromotionRegistryRecord(publication *domain.PackagePublication, deleted bool) (gonostr.Tags, string) {
	if deleted {
		content := map[string]any{"deleted": true, "id": publication.ID.String()}
		contentJSON, _ := json.Marshal(content)
		return gonostr.Tags{}, string(contentJSON)
	}
	tags := gonostr.Tags{
		{"promotion", publication.ID.String()},
		{"repository", publication.RepositoryID.String()},
		{"artifact", publication.ArtifactID.String()},
		{"status", string(publication.Status)},
		{"policy_decision", string(publication.PolicyDecision)},
	}
	if publication.TargetRepositoryID != nil {
		tags = append(tags, gonostr.Tag{"target_repository", publication.TargetRepositoryID.String()})
	}
	content := map[string]any{
		"deleted":         false,
		"id":              publication.ID.String(),
		"repository_id":   publication.RepositoryID.String(),
		"artifact_id":     publication.ArtifactID.String(),
		"status":          string(publication.Status),
		"policy_decision": string(publication.PolicyDecision),
		"policy_ref":      publication.PolicyRef,
		"approved_by":     publication.ApprovedBy,
		"metadata":        publication.Metadata,
		"created_at":      formatTime(publication.CreatedAt),
		"updated_at":      formatTime(publication.UpdatedAt),
	}
	if publication.TargetRepositoryID != nil {
		content["target_repository_id"] = publication.TargetRepositoryID.String()
	}
	if publication.PublishedAt != nil {
		content["published_at"] = formatTime(*publication.PublishedAt)
	}
	contentJSON, _ := json.Marshal(content)
	return tags, string(contentJSON)
}

// PublishPackageRepositoryRegistry publishes a package repository's cp-state record.
// Phase 3 P1: canonical state published directly from the intent handler.
func (r *RelayFirstStatePublisher) PublishPackageRepositoryRegistry(ctx context.Context, repo *domain.PackageRepository, deleted bool) error {
	if repo == nil {
		return fmt.Errorf("package repository is nil")
	}
	tags, content := packageRepositoryRegistryRecord(repo, deleted)
	return r.publishAuthoritativeProjection(ctx, KindPackageRepositoryRegistry, repo.ID, deleted, tags, content, "package_repository.projection")
}

// PublishPackageArtifactRegistry publishes a package artifact's cp-state record.
func (r *RelayFirstStatePublisher) PublishPackageArtifactRegistry(ctx context.Context, artifact *domain.PackageArtifact, deleted bool) error {
	if artifact == nil {
		return fmt.Errorf("package artifact is nil")
	}
	tags, content := packageArtifactRegistryRecord(artifact, deleted)
	return r.publishAuthoritativeProjection(ctx, KindPackageArtifactRegistry, artifact.ID, deleted, tags, content, "package_artifact.projection")
}

// PublishPackagePromotionRegistry publishes a package promotion's cp-state record.
func (r *RelayFirstStatePublisher) PublishPackagePromotionRegistry(ctx context.Context, publication *domain.PackagePublication, deleted bool) error {
	if publication == nil {
		return fmt.Errorf("package publication is nil")
	}
	tags, content := packagePromotionRegistryRecord(publication, deleted)
	return r.publishAuthoritativeProjection(ctx, KindPackagePromotionRegistry, publication.ID, deleted, tags, content, "package_promotion.projection")
}


// publishAuthoritativeProjection delivers a cp-state record through the
// projector's authoritative path (fingerprint-deduped, outbox-queued, no
// backoff gating). Used for domains that publish directly from the mutation
// site after the database write has committed (Phase 3 S2), as opposed to
// publish (which uses the relay-first PublishBeforeCommit path).
func (r *RelayFirstStatePublisher) publishAuthoritativeProjection(ctx context.Context, legacyKind int, id uuid.UUID, deleted bool, tags gonostr.Tags, content, entityType string) error {
	if r == nil || r.projector == nil {
		return fmt.Errorf("relay-first state publisher is not configured")
	}
	wireKind, baseTags := controlStateEnvelope(legacyKind, id.String(), deleted)
	return r.projector.publishAuthoritative(ctx, wireKind, append(baseTags, tags...), content, entityType, &id)
}

// Runtime state records -------------------------------------------------------
//
// The runtime state record for a service+environment pair is published both by
// the projector (RepublishSnapshot, handleEvent) and by the reconciler via its
// RuntimeStatePublisher. Both must emit the same wire shape, so the record
// builder lives here. Phase 3 S1 moves publication to the reconciler and
// deletes the projector state legs.

// RuntimeStateRecord returns the family tags and JSON content of a
// service/environment runtime state record. observation may be nil when the
// state has no linked observation yet.
func RuntimeStateRecord(state *domain.EnvironmentServiceState, observation *domain.RuntimeObservation) (gonostr.Tags, string) {
	content := map[string]any{
		"deleted":            false,
		"service_id":         state.ServiceID.String(),
		"environment_id":     state.EnvironmentID.String(),
		"deployment_unit_id": uuidStringPtr(state.DeploymentUnitID),
		"drift_status":       string(state.DriftStatus),
		"updated_at":         formatTime(state.UpdatedAt),
	}
	if state.DesiredArtifactID != nil {
		content["desired_artifact_id"] = state.DesiredArtifactID.String()
	}
	if state.DesiredIntentID != nil {
		content["desired_intent_id"] = state.DesiredIntentID.String()
	}
	if state.LastSuccessfulRunID != nil {
		content["last_successful_run_id"] = state.LastSuccessfulRunID.String()
	}
	if state.CurrentObservationID != nil {
		content["current_observation_id"] = state.CurrentObservationID.String()
	}
	if state.LastReconciledAt != nil {
		content["last_reconciled_at"] = formatTime(*state.LastReconciledAt)
	}
	if state.DesiredHash != "" {
		content["desired_hash"] = state.DesiredHash
	}
	observedHash := ""
	if observation != nil {
		if observation.NormalizedState != nil {
			observedHash = observation.NormalizedState.ObservationHash
		}
		if observedHash == "" {
			observedHash = observation.NormalizedHash
		}
		content["health_status"] = string(observation.HealthStatus)
		content["observed_image_digest"] = observation.ObservedImageDigest
		content["observed_at"] = formatTime(observation.ObservedAt)
	}
	if observedHash != "" {
		content["observed_hash"] = observedHash
	}
	if state.DesiredRuntimeState != nil {
		if renderer := desiredStateRenderer(state.DesiredRuntimeState); renderer != "" {
			content["renderer"] = renderer
		}
		if target := desiredStateTarget(state.DesiredRuntimeState); target != "" {
			content["target"] = target
		}
	}

	contentJSON, _ := json.Marshal(content)
	tags := gonostr.Tags{
		{"service", state.ServiceID.String()},
		{"environment", state.EnvironmentID.String()},
		{"unit", unitTagValue(state.DeploymentUnitID)},
		{"drift_status", string(state.DriftStatus)},
	}
	if state.DesiredArtifactID != nil {
		tags = append(tags, gonostr.Tag{"artifact", state.DesiredArtifactID.String()})
	}
	if state.DesiredIntentID != nil {
		tags = append(tags, gonostr.Tag{"intent", state.DesiredIntentID.String()})
	}
	if state.LastSuccessfulRunID != nil {
		tags = append(tags, gonostr.Tag{"run", state.LastSuccessfulRunID.String()})
	}
	if state.DesiredHash != "" {
		tags = append(tags, gonostr.Tag{"desired_hash", state.DesiredHash})
	}
	if observedHash != "" {
		tags = append(tags, gonostr.Tag{"observed_hash", observedHash})
	}
	return tags, string(contentJSON)
}

// RuntimeStateTombstoneRecord returns the family tags and JSON content of a
// tombstone for a service/environment state coordinate.
func RuntimeStateTombstoneRecord(serviceID, envID uuid.UUID) (gonostr.Tags, string) {
	content := map[string]any{
		"deleted":        true,
		"service_id":     serviceID.String(),
		"environment_id": envID.String(),
		"updated_at":     formatTime(time.Now().UTC()),
	}
	contentJSON, _ := json.Marshal(content)
	tags := gonostr.Tags{
		{"service", serviceID.String()},
		{"environment", envID.String()},
		{"unit", domain.DefaultDeploymentUnitKey},
	}
	return tags, string(contentJSON)
}

// ServiceStateDTag returns the cp-state d-tag for a service/environment state
// coordinate. Exported for the reconciler's state publisher.
func ServiceStateDTag(serviceID, environmentID uuid.UUID) string {
	return serviceStateDTag(serviceID, environmentID)
}

// PublishState publishes state's runtime state record (or re-publishes it when
// unchanged content is fingerprint-deduped by the projector). observation is the
// latest observation linked to the state; nil if none.
func (r *RelayFirstStatePublisher) PublishState(ctx context.Context, state *domain.EnvironmentServiceState, observation *domain.RuntimeObservation) error {
	if state == nil {
		return fmt.Errorf("state is nil")
	}
	tags, content := RuntimeStateRecord(state, observation)
	return r.publish(ctx, KindServiceState, state.ServiceID, false, tags, content, "state.projection")
}

// PublishStateTombstone publishes a tombstone for the service/environment state
// coordinate, so relay readers see the removal.
func (r *RelayFirstStatePublisher) PublishStateTombstone(ctx context.Context, serviceID, envID uuid.UUID) error {
	tags, content := RuntimeStateTombstoneRecord(serviceID, envID)
	return r.publish(ctx, KindServiceState, serviceID, true, tags, content, "state.projection")
}
