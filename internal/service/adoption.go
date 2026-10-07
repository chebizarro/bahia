package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	secretsAdapter "github.com/openagentsinc/bahia/internal/adapters/secrets"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

const (
	adoptionCISystem      = "adoption"
	adoptionImportedEvent = events.EventAdoptionImported
	adoptionStatusCreated = "created"
	adoptionStatusUpdated = "updated"
	adoptionStatusFailed  = "failed"

	// adoptionBackfillMarker records, in the local outbox's control records,
	// that the SQL-era adopted_runtime_identity rows were published as
	// canonical bindings once.
	adoptionBackfillMarker = "adoption-binding-backfill-v1"
	adoptionBackfillPage   = 500
)

// Adoption step names, in publication order. A result that did not complete
// names the step whose canonical publish failed.
const (
	adoptionStepBinding     = "binding"
	adoptionStepEnvironment = "environment"
	adoptionStepService     = "service"
	adoptionStepBuild       = "build"
	adoptionStepArtifact    = "artifact"
	adoptionStepSecrets     = "secrets"
	adoptionStepObservation = "observation"
	adoptionStepState       = "state"
	adoptionStepFinalize    = "finalize"
	adoptionStepComplete    = "complete"
)

// adoptionIDNamespace derives the deterministic ids of adopted resources.
var adoptionIDNamespace = uuid.NewSHA1(uuid.NameSpaceOID, []byte("bahia:adoption:v1"))

// adoptionEntityID derives the id of an adopted resource from the facts that
// identify it, so a retry or a resumed adoption addresses the same canonical
// coordinate instead of minting a second one.
func adoptionEntityID(parts ...string) uuid.UUID {
	return uuid.NewSHA1(adoptionIDNamespace, []byte(strings.Join(parts, "\x1f")))
}

// AdoptionCanonicalPublisher publishes the signed canonical cp-state record of
// each resource an adoption produces. Every method signs at most one event and
// admits it to the durable publish outbox before the first relay round: a nil
// error means the record is accepted or queued for per-relay retry, any error
// means it was never admitted (or was abandoned) and nothing derived from it
// may be written. internal/adapters/nostr.AdoptionCanonicalPublisher
// implements it.
type AdoptionCanonicalPublisher interface {
	PublishAdoptionBinding(ctx context.Context, binding *domain.AdoptionBinding) error
	PublishEnvironmentRegistry(ctx context.Context, env *domain.Environment, units []domain.DeploymentUnit) error
	PublishServiceRegistry(ctx context.Context, svc *domain.Service) error
	PublishBuildRegistry(ctx context.Context, build *domain.Build) error
	PublishArtifactRegistry(ctx context.Context, artifact *domain.Artifact) error
	PublishSecretRef(ctx context.Context, orgID uuid.UUID, ref domain.SecretRef) error
	PublishRuntimeObservation(ctx context.Context, obs *domain.RuntimeObservation) error
	PublishServiceState(ctx context.Context, state *domain.EnvironmentServiceState, observation *domain.RuntimeObservation) error
}

// AdoptionIndexRepositories are the optional SQL repositories the adoption
// service mirrors its canonical records into. The index is derived: it is
// written after every record of a candidate is published, a failed write is
// reported and never undoes canonical state, and RebuildIndex recreates it.
type AdoptionIndexRepositories struct {
	Services          repository.ServiceRepository
	Environments      repository.EnvironmentRepository
	Builds            repository.BuildRepository
	Artifacts         repository.ArtifactRepository
	DeploymentUnits   repository.DeploymentUnitRepository
	State             repository.EnvironmentServiceStateRepository
	Observations      repository.RuntimeObservationRepository
	AdoptedIdentities repository.AdoptedRuntimeIdentityRepository
	// Tx, when set, writes a candidate's index rows in one transaction.
	Tx repository.TxExecutor
}

func (idx AdoptionIndexRepositories) configured() bool {
	return idx.Services != nil || idx.Environments != nil || idx.Builds != nil || idx.Artifacts != nil ||
		idx.DeploymentUnits != nil || idx.State != nil || idx.Observations != nil || idx.AdoptedIdentities != nil
}

// adoptedIdentityLister is the optional page reader of the SQL identity table
// that BackfillFromIndex uses. repository.PgAdoptedRuntimeIdentityRepository
// implements it.
type adoptedIdentityLister interface {
	List(ctx context.Context, limit, offset int) ([]domain.AdoptedRuntimeIdentity, error)
}

// AdoptionBackfillMarker remembers that the one-time SQL-era backfill ran.
// The local outbox's control records implement it.
type AdoptionBackfillMarker interface {
	GetControlRecord(family, id string) ([]byte, error)
	PutControlRecord(family, id string, value []byte) error
}

// AdoptionService scans Docker hosts and imports existing containers into
// Bahia models.
//
// Adoption output is canonical on relays. Each candidate is first
// planned from the daemon's canonical records in the local event store, where
// every refusal of ambiguous evidence happens and every id is derived from the
// request, and then published one signed cp-state record at a time through the
// outbox: the adoption binding (in progress), the environment with its
// deployment units, the service, the build, the artifact, the imported secret
// references, the runtime observation, the service state, and the binding
// again (complete). A publish failure stops the candidate and is returned to
// the caller; a resumed adoption of the same request re-derives the same
// coordinates, finds the records already published and completes the rest.
// Only then is the optional SQL index written, in one transaction, and a
// failed index write is reported without undoing anything.
type AdoptionService struct {
	// mu serializes the plan-and-publish of candidates, so two imports never
	// plan from the same stale view and overwrite each other's environment
	// units or service revision.
	mu            sync.Mutex
	canonical     AdoptionCanonicalPublisher
	view          AdoptionCanonicalView
	index         AdoptionIndexRepositories
	secrets       repository.SecretRepository
	organizations repository.OrganizationRepository
	publisher     events.Publisher
	logger        *zap.Logger

	secretEncryptor      *secretsAdapter.Encryptor
	runtimeCfg           config.RuntimeConfig
	allowRawDockerHosts  bool
	allowComposeTakeover bool
	now                  func() time.Time
}

// AdoptionServiceOption configures adoption runtime governance behavior.
type AdoptionServiceOption func(*AdoptionService)

// WithAdoptionRuntimeConfig enables server-managed endpoint resolution and raw-host policy.
func WithAdoptionRuntimeConfig(runtimeCfg config.RuntimeConfig, allowRawDockerHosts bool) AdoptionServiceOption {
	return func(s *AdoptionService) {
		s.runtimeCfg = runtimeCfg
		s.allowRawDockerHosts = allowRawDockerHosts
	}
}

// WithAdoptionComposeTakeoverPolicy controls whether Compose-origin containers
// may be imported into Bahia's direct Docker runtime management mode.
func WithAdoptionComposeTakeoverPolicy(allow bool) AdoptionServiceOption {
	return func(s *AdoptionService) {
		s.allowComposeTakeover = allow
	}
}

// WithAdoptionSecrets wires imported sensitive environment values into Bahia's
// secret value store. Secret values are never published; without a store and
// an encryptor a container with sensitive environment values is refused.
func WithAdoptionSecrets(repo repository.SecretRepository, encryptor *secretsAdapter.Encryptor) AdoptionServiceOption {
	return func(s *AdoptionService) {
		s.secrets = repo
		s.secretEncryptor = encryptor
	}
}

// WithAdoptionOrganizations enables org ownership resolution for imported resources.
func WithAdoptionOrganizations(repo repository.OrganizationRepository) AdoptionServiceOption {
	return func(s *AdoptionService) {
		s.organizations = repo
	}
}

// WithAdoptionIndex mirrors canonical adoption records into the given SQL
// repositories after they are published.
func WithAdoptionIndex(index AdoptionIndexRepositories) AdoptionServiceOption {
	return func(s *AdoptionService) {
		s.index = index
	}
}

// NewAdoptionService creates an AdoptionService publishing through canonical
// and planning from view. Both are required for Scan and Import.
func NewAdoptionService(
	canonical AdoptionCanonicalPublisher,
	view AdoptionCanonicalView,
	publisher events.Publisher,
	logger *zap.Logger,
	opts ...AdoptionServiceOption,
) *AdoptionService {
	if logger == nil {
		logger = zap.NewNop()
	}
	if publisher == nil {
		publisher = &events.NoopPublisher{}
	}
	svc := &AdoptionService{
		canonical:            canonical,
		view:                 view,
		publisher:            publisher,
		logger:               logger,
		allowRawDockerHosts:  true,
		allowComposeTakeover: true,
		now:                  func() time.Time { return time.Now().UTC() },
	}
	for _, opt := range opts {
		opt(svc)
	}
	return svc
}

// Ready reports whether the service can plan and publish adoptions.
func (s *AdoptionService) Ready() error {
	switch {
	case s == nil:
		return errors.New("adoption service is not configured")
	case s.canonical == nil:
		return errors.New("adoption canonical publisher is not configured")
	case s.view == nil:
		return errors.New("adoption canonical local view is not configured")
	}
	return nil
}

// AdoptionScanRequest requests a scan of one or more Docker targets.
type AdoptionScanRequest struct {
	Targets []AdoptionTarget
}

// AdoptionTarget identifies a Docker host and target environment.
type AdoptionTarget struct {
	Name            string
	DockerHost      string
	EndpointRef     string
	Endpoint        config.RuntimeEndpointConfig
	EnvironmentName string
}

// AdoptionImportRequest imports selected discovered containers.
type AdoptionImportRequest struct {
	Targets    []AdoptionTarget
	Selections []AdoptionSelection
	ImportAll  bool
	OrgID      uuid.UUID
	// RequestID identifies the signed request (its intent_id). Resources
	// minted per request, such as the observation, derive their ids from it,
	// so re-processing the same request addresses the same coordinates.
	RequestID string
}

// AdoptionSelection selects one container on a target host.
type AdoptionSelection struct {
	TargetName          string
	ContainerID         string
	ServiceNameOverride string
}

// AdoptionPreview groups discovered containers for one target.
type AdoptionPreview struct {
	Target     AdoptionTarget
	Containers []AdoptionPreviewContainer
	Error      string
}

// AdoptionPreviewContainer is one discovered container plus Bahia import proposal metadata.
type AdoptionPreviewContainer struct {
	Discovered              runtime.DiscoveredContainer
	ProposedServiceName     string
	ExistingServiceID       *uuid.UUID
	WillUpdate              bool
	Warnings                []string
	Adoptable               bool
	SafeEnvironment         map[string]string
	SafeLabels              map[string]string
	RedactedEnvironmentKeys []string
	RedactedLabelKeys       []string
}

// AdoptionImportResult reports per-candidate import outcome.
type AdoptionImportResult struct {
	TargetName              string
	ContainerID             string
	ContainerName           string
	ServiceName             string
	ServiceID               *uuid.UUID
	EnvironmentID           *uuid.UUID
	BuildID                 *uuid.UUID
	ArtifactID              *uuid.UUID
	Status                  string
	Warnings                []string
	RedactedEnvironmentKeys []string
	RedactedLabelKeys       []string
	Error                   string
	// Incomplete reports that the candidate was accepted but a canonical
	// publish failed at Step: the records before it are published, and
	// re-processing the same request completes the remainder.
	Incomplete bool
	Step       string
	// IndexError reports a failed write of the optional SQL index. The
	// canonical records are complete; RebuildIndex repairs the index.
	IndexError string

	// cause is the publish error behind an incomplete result, kept so the
	// import's error chain carries it to the caller.
	cause error
}

// ErrAdoptionIncomplete marks an import whose canonical publication did not
// complete for at least one candidate. The import is resumable.
var ErrAdoptionIncomplete = errors.New("adoption canonical publication incomplete")

// Scan discovers containers and proposes Bahia service names.
func (s *AdoptionService) Scan(ctx context.Context, req AdoptionScanRequest) ([]AdoptionPreview, error) {
	start := time.Now()
	if err := s.Ready(); err != nil {
		return nil, err
	}
	targets, err := s.normalizeAdoptionTargets(req.Targets)
	if err != nil {
		s.logger.Warn("adoption scan rejected", zap.String("result", "failed"), zap.Error(err), zap.Int64("duration_ms", time.Since(start).Milliseconds()))
		return nil, err
	}
	results, err := runtime.DiscoverDockerTargets(ctx, toDockerDiscoveryTargets(targets), s.logger)
	if err != nil {
		s.logger.Warn("adoption scan discovery failed", zap.Int("target_count", len(targets)), zap.String("result", "failed"), zap.Error(err), zap.Int64("duration_ms", time.Since(start).Milliseconds()))
		return nil, err
	}
	known, err := s.loadKnownServices(ctx)
	if err != nil {
		return nil, err
	}
	previews := s.buildPreviews(targets, results, known)
	candidateCount, redactedEnvKeyCount, redactedLabelKeyCount, targetErrors := adoptionPreviewOperationalStats(previews)
	duration := time.Since(start)
	s.publisher.Publish(ctx, events.Event{
		Type:     events.EventAdoptionScanCompleted,
		EntityID: "adoption",
		Data: map[string]any{
			"target_count":             len(targets),
			"candidate_count":          candidateCount,
			"target_error_count":       targetErrors,
			"redacted_env_key_count":   redactedEnvKeyCount,
			"redacted_label_key_count": redactedLabelKeyCount,
			"duration_ms":              duration.Milliseconds(),
		},
	})
	s.logger.Info("adoption scan completed",
		zap.Int("target_count", len(targets)),
		zap.Int("candidate_count", candidateCount),
		zap.Int("target_error_count", targetErrors),
		zap.Int("redacted_env_key_count", redactedEnvKeyCount),
		zap.Int("redacted_label_key_count", redactedLabelKeyCount),
		zap.Int64("duration_ms", duration.Milliseconds()),
		zap.String("result", "success"),
	)
	return previews, nil
}

// Import scans targets and imports selected containers. Candidate refusals
// are returned in result rows. When a candidate's canonical publication did
// not complete, the rows are returned together with an error wrapping
// ErrAdoptionIncomplete, so the caller reports the request as not applied
// and re-processes it to resume.
func (s *AdoptionService) Import(ctx context.Context, req AdoptionImportRequest) ([]AdoptionImportResult, error) {
	start := time.Now()
	if err := s.Ready(); err != nil {
		return nil, err
	}
	targets, err := s.normalizeAdoptionTargets(req.Targets)
	if err != nil {
		s.logger.Warn("adoption import rejected", zap.String("result", "failed"), zap.Error(err), zap.Int64("duration_ms", time.Since(start).Milliseconds()))
		return nil, err
	}
	selectionSet, err := normalizeAdoptionSelections(req)
	if err != nil {
		s.logger.Warn("adoption import selections rejected", zap.Int("target_count", len(targets)), zap.String("result", "failed"), zap.Error(err), zap.Int64("duration_ms", time.Since(start).Milliseconds()))
		return nil, err
	}
	orgID, err := s.resolveImportOrgID(ctx, req.OrgID, targets)
	if err != nil {
		s.logger.Warn("adoption import org resolution rejected", zap.Int("target_count", len(targets)), zap.String("result", "failed"), zap.Error(err), zap.Int64("duration_ms", time.Since(start).Milliseconds()))
		return nil, err
	}

	results, err := runtime.DiscoverDockerTargets(ctx, toDockerDiscoveryTargets(targets), s.logger)
	if err != nil {
		s.logger.Warn("adoption import discovery failed", zap.Int("target_count", len(targets)), zap.String("result", "failed"), zap.Error(err), zap.Int64("duration_ms", time.Since(start).Milliseconds()))
		return nil, err
	}
	known, err := s.loadKnownServices(ctx)
	if err != nil {
		return nil, err
	}
	previews := s.buildPreviews(targets, results, known)

	var imported []AdoptionImportResult
	processedSelections := map[string]struct{}{}
	for _, preview := range previews {
		if preview.Error != "" {
			if req.ImportAll {
				imported = append(imported, AdoptionImportResult{TargetName: preview.Target.Name, Status: adoptionStatusFailed, Error: preview.Error})
			} else {
				for _, selection := range selectionsForTarget(selectionSet, preview.Target.Name) {
					processedSelections[selectionKey(selection.TargetName, selection.ContainerID)] = struct{}{}
					imported = append(imported, AdoptionImportResult{TargetName: preview.Target.Name, ContainerID: selection.ContainerID, Status: adoptionStatusFailed, Error: preview.Error})
				}
			}
			continue
		}
		for _, container := range preview.Containers {
			key := selectionKey(preview.Target.Name, container.Discovered.ContainerID)
			selection, selected := selectionSet[key]
			if selected {
				processedSelections[key] = struct{}{}
			}
			if !req.ImportAll && !selected {
				continue
			}
			serviceName := container.ProposedServiceName
			if selected && selection.ServiceNameOverride != "" {
				serviceName = normalizeResourceName(selection.ServiceNameOverride)
			}
			imported = append(imported, s.importCandidate(ctx, req.RequestID, orgID, preview.Target, container, serviceName))
		}
	}
	if !req.ImportAll {
		for key, selection := range selectionSet {
			if _, ok := processedSelections[key]; ok {
				continue
			}
			imported = append(imported, AdoptionImportResult{TargetName: selection.TargetName, ContainerID: selection.ContainerID, Status: adoptionStatusFailed, Error: "selected container was not discovered"})
		}
	}
	sort.SliceStable(imported, func(i, j int) bool {
		if imported[i].TargetName != imported[j].TargetName {
			return imported[i].TargetName < imported[j].TargetName
		}
		return imported[i].ContainerID < imported[j].ContainerID
	})
	successCount, failureCount, redactedEnvKeyCount, redactedLabelKeyCount := adoptionImportOperationalStats(imported)
	result := "success"
	if failureCount > 0 {
		result = "partial_failure"
	}
	if len(imported) > 0 && successCount == 0 && failureCount > 0 {
		result = "failed"
	}
	s.logger.Info("adoption import completed",
		zap.Int("target_count", len(targets)),
		zap.Int("candidate_count", len(imported)),
		zap.Int("success_count", successCount),
		zap.Int("failure_count", failureCount),
		zap.Int("redacted_env_key_count", redactedEnvKeyCount),
		zap.Int("redacted_label_key_count", redactedLabelKeyCount),
		zap.Int64("duration_ms", time.Since(start).Milliseconds()),
		zap.String("result", result),
	)
	return imported, incompleteImportError(imported)
}

// incompleteImportError summarizes the candidates whose canonical publication
// did not complete, or returns nil when every accepted candidate completed.
func incompleteImportError(results []AdoptionImportResult) error {
	var incomplete []error
	for _, result := range results {
		if result.Incomplete {
			incomplete = append(incomplete, fmt.Errorf("%s/%s at %s: %w", result.TargetName, result.ContainerName, result.Step, result.cause))
		}
	}
	if len(incomplete) == 0 {
		return nil
	}
	return fmt.Errorf("%w for %d of %d candidates; re-process the same request to resume: %w", ErrAdoptionIncomplete, len(incomplete), len(results), errors.Join(incomplete...))
}

// knownServices is the view's live service set at the start of a request,
// joined with the adoption bindings that identify adopted workloads.
type knownServices struct {
	services []domain.Service
	bindings []domain.AdoptionBinding
}

func (s *AdoptionService) loadKnownServices(ctx context.Context) (*knownServices, error) {
	services, err := s.view.ListServices(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading canonical services: %w", err)
	}
	bindings, err := s.view.ListAdoptionBindings(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading canonical adoption bindings: %w", err)
	}
	return &knownServices{services: services, bindings: bindings}, nil
}

func (k *knownServices) byName(name string) *domain.Service {
	for i := range k.services {
		if k.services[i].Name == name {
			svc := k.services[i]
			return &svc
		}
	}
	return nil
}

func (k *knownServices) byID(id uuid.UUID) *domain.Service {
	for i := range k.services {
		if k.services[i].ID == id {
			svc := k.services[i]
			return &svc
		}
	}
	return nil
}

func (k *knownServices) binding(serviceID, environmentID uuid.UUID) *domain.AdoptionBinding {
	for i := range k.bindings {
		if k.bindings[i].ServiceID == serviceID && k.bindings[i].EnvironmentID == environmentID {
			binding := k.bindings[i]
			return &binding
		}
	}
	return nil
}

// byAdoptedTarget resolves the service that the workload's stable
// fingerprints are bound to, in orgID. It fails closed when the fingerprints
// resolve to more than one service.
func (k *knownServices) byAdoptedTarget(orgID uuid.UUID, target AdoptionTarget, discovered runtime.DiscoveredContainer) (*domain.Service, error) {
	fingerprints := map[string]struct{}{}
	for _, fingerprint := range adoptedRuntimeFingerprintsByKind(target, discovered) {
		fingerprints[fingerprint] = struct{}{}
	}
	var matched *domain.Service
	for _, binding := range k.bindings {
		if binding.OrgID != orgID {
			continue
		}
		bound := false
		for _, fingerprint := range binding.Fingerprints {
			if _, ok := fingerprints[fingerprint]; ok {
				bound = true
				break
			}
		}
		if !bound {
			continue
		}
		svc := k.byID(binding.ServiceID)
		if svc == nil {
			continue
		}
		if matched != nil && matched.ID != svc.ID {
			return nil, fmt.Errorf("adopted runtime identity matches multiple services in org %s", orgID)
		}
		matched = svc
	}
	if matched != nil {
		return matched, nil
	}
	for i := range k.services {
		if k.services[i].OrgID == orgID && sameAdoptedTarget(&k.services[i], target, discovered) {
			svc := k.services[i]
			return &svc, nil
		}
	}
	return nil, nil
}

func (s *AdoptionService) buildPreviews(targets []AdoptionTarget, results []runtime.DockerDiscoveryResult, known *knownServices) []AdoptionPreview {
	previews := make([]AdoptionPreview, len(results))
	usedNames := map[string]int{}
	for i, result := range results {
		target := targets[i]
		preview := AdoptionPreview{Target: target, Error: result.Error}
		if result.Error != "" {
			previews[i] = preview
			continue
		}
		for _, discovered := range result.Containers {
			classified := classifyDiscoveredSensitiveData(discovered)
			proposed := proposedServiceNameFor(target, discovered, usedNames, known)
			existing := known.byName(proposed)
			warnings := append([]string(nil), discovered.Warnings...)
			adoptable := discovered.Adoptable
			if isComposeOrigin(discovered) {
				warnings = append(warnings, "compose-origin workload will be taken over by Bahia direct Docker runtime actions")
				if !s.allowComposeTakeover {
					warnings = append(warnings, "compose takeover is disabled by adoption policy")
					adoptable = false
				}
			}
			candidate := AdoptionPreviewContainer{
				Discovered:              discovered,
				ProposedServiceName:     proposed,
				Warnings:                warnings,
				Adoptable:               adoptable,
				SafeEnvironment:         classified.SafeEnvironment,
				SafeLabels:              classified.SafeLabels,
				RedactedEnvironmentKeys: classified.SensitiveEnvironmentKeys,
				RedactedLabelKeys:       classified.SensitiveLabelKeys,
			}
			if existing != nil {
				candidate.ExistingServiceID = &existing.ID
				candidate.WillUpdate = sameAdoptedTarget(existing, target, discovered)
			}
			preview.Containers = append(preview.Containers, candidate)
		}
		previews[i] = preview
	}
	return previews
}

func proposedServiceNameFor(target AdoptionTarget, discovered runtime.DiscoveredContainer, usedNames map[string]int, known *knownServices) string {
	base := proposedServiceName(discovered)
	if base == "" {
		base = "adopted-" + shortID(discovered.ContainerID)
	}
	conflicts := func(name string) bool {
		existing := known.byName(name)
		return existing != nil && !sameAdoptedTarget(existing, target, discovered)
	}
	name := base
	if usedNames[name] > 0 || conflicts(name) {
		name = normalizeResourceName(base + "-" + target.EnvironmentName)
		if usedNames[name] > 0 || conflicts(name) {
			name = normalizeResourceName(base + "-" + shortID(discovered.ContainerID))
		}
	}
	usedNames[name]++
	return name
}

// adoptionPlan is everything one candidate publishes, derived before the
// first publish so that a refusal leaves no record behind.
type adoptionPlan struct {
	orgID      uuid.UUID
	target     AdoptionTarget
	discovered runtime.DiscoveredContainer
	classified sensitiveDataClassification

	env            domain.Environment
	envUnits       []domain.DeploymentUnit
	unit           domain.DeploymentUnit
	svc            domain.Service
	createdService bool
	build          domain.Build
	artifact       domain.Artifact
	secrets        []plannedSecret
	obs            domain.RuntimeObservation
	state          domain.EnvironmentServiceState
	binding        domain.AdoptionBinding
}

type plannedSecret struct {
	id    uuid.UUID
	name  string
	value string
}

func (s *AdoptionService) importCandidate(ctx context.Context, requestID string, orgID uuid.UUID, target AdoptionTarget, candidate AdoptionPreviewContainer, serviceName string) AdoptionImportResult {
	discovered := candidate.Discovered
	result := AdoptionImportResult{
		TargetName:              target.Name,
		ContainerID:             discovered.ContainerID,
		ContainerName:           discovered.ContainerName,
		ServiceName:             serviceName,
		Warnings:                append([]string(nil), candidate.Warnings...),
		RedactedEnvironmentKeys: append([]string(nil), candidate.RedactedEnvironmentKeys...),
		RedactedLabelKeys:       append([]string(nil), candidate.RedactedLabelKeys...),
	}
	fail := func(err error) AdoptionImportResult {
		result.Status = adoptionStatusFailed
		result.Error = err.Error()
		s.logAdoptionImportResult(target, result, "failed")
		return result
	}
	if !candidate.Adoptable {
		return fail(errors.New("container has unsupported adoption warnings"))
	}
	if discovered.ImageRepo == "" || discovered.ImageDigest == "" {
		return fail(errors.New("container image repo and digest are required for import"))
	}
	if serviceName == "" {
		return fail(errors.New("service name is required"))
	}
	classified := classifyDiscoveredSensitiveData(discovered)
	result.RedactedEnvironmentKeys = append([]string(nil), classified.SensitiveEnvironmentKeys...)
	result.RedactedLabelKeys = append([]string(nil), classified.SensitiveLabelKeys...)
	if len(classified.SensitiveEnvironment) > 0 && (s.secrets == nil || s.secretEncryptor == nil) {
		return fail(errors.New("sensitive environment values require configured secret storage and encryption"))
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	plan, err := s.planCandidate(ctx, requestID, orgID, target, discovered, classified, serviceName)
	if err != nil {
		return fail(err)
	}
	result.ServiceName = plan.svc.Name
	result.EnvironmentID = &plan.env.ID
	result.ServiceID = &plan.svc.ID
	result.BuildID = &plan.build.ID
	result.ArtifactID = &plan.artifact.ID
	result.Status = adoptionStatusUpdated
	if plan.createdService {
		result.Status = adoptionStatusCreated
	}

	if step, err := s.publishPlan(ctx, plan); err != nil {
		result.Status = adoptionStatusFailed
		result.Incomplete = true
		result.Step = step
		result.Error = err.Error()
		result.cause = err
		s.logAdoptionImportResult(target, result, "incomplete")
		return result
	}
	result.Step = adoptionStepComplete

	if err := s.writeIndex(ctx, plan); err != nil {
		result.IndexError = err.Error()
		s.logger.Warn("adoption SQL index write failed; canonical state retained", zap.String("service_id", plan.svc.ID.String()), zap.String("environment_id", plan.env.ID.String()), zap.Error(err))
	}
	s.publisher.Publish(ctx, events.Event{
		Type:     adoptionImportedEvent,
		EntityID: plan.svc.ID.String(),
		Data: map[string]any{
			"service_id":     plan.svc.ID,
			"environment_id": plan.env.ID,
			"artifact_id":    plan.artifact.ID,
			"target_name":    target.Name,
			"container_id":   discovered.ContainerID,
			"container_name": discovered.ContainerName,
			"status":         result.Status,
		},
	})
	s.logAdoptionImportResult(target, result, "success")
	return result
}

// planCandidate derives every record of a candidate from the request and the
// canonical records already published, refusing ambiguous or conflicting
// evidence before anything is written.
func (s *AdoptionService) planCandidate(ctx context.Context, requestID string, orgID uuid.UUID, target AdoptionTarget, discovered runtime.DiscoveredContainer, classified sensitiveDataClassification, serviceName string) (*adoptionPlan, error) {
	known, err := s.loadKnownServices(ctx)
	if err != nil {
		return nil, err
	}
	plan := &adoptionPlan{orgID: orgID, target: target, discovered: discovered, classified: classified}
	now := s.now()

	if err := s.planEnvironment(ctx, plan, now); err != nil {
		return nil, err
	}
	if err := planService(plan, known, requestID, serviceName, now); err != nil {
		return nil, err
	}
	planDeploymentUnit(plan, now)
	if err := s.planBuild(ctx, plan, now); err != nil {
		return nil, err
	}
	if err := s.planArtifact(ctx, plan, now); err != nil {
		return nil, err
	}
	for _, name := range sortedStringKeys(classified.SensitiveEnvironment) {
		plan.secrets = append(plan.secrets, plannedSecret{id: adoptionEntityID("secret", plan.svc.ID.String(), plan.env.ID.String(), name), name: name, value: classified.SensitiveEnvironment[name]})
	}

	obs := observationFromDiscovered(target, plan.svc.ID, plan.env.ID, discovered)
	obs.ObservedAt = now
	obs.DeploymentUnitID = &plan.unit.ID
	if requestID != "" {
		obs.ID = adoptionEntityID("observation", plan.svc.ID.String(), plan.env.ID.String(), requestID)
	} else {
		obs.ID = uuid.New()
	}
	normalizeRuntimeObservationHash(obs)
	plan.obs = *obs

	state, err := s.view.GetServiceState(ctx, plan.svc.ID, plan.env.ID)
	if err != nil {
		return nil, fmt.Errorf("reading canonical state of service %s: %w", plan.svc.ID, err)
	}
	if state == nil {
		state = &domain.EnvironmentServiceState{ServiceID: plan.svc.ID, EnvironmentID: plan.env.ID}
	}
	state.DeploymentUnitID = &plan.unit.ID
	state.DesiredArtifactID = &plan.artifact.ID
	state.CurrentObservationID = &obs.ID
	state.DriftStatus = domain.ArtifactDigestDriftStatus(plan.artifact.ImageDigest, obs.ObservedImageDigest, obs.HealthStatus, domain.DriftStatusDeploying)
	state.LastReconciledAt = &now
	state.UpdatedAt = now
	plan.state = *state

	fingerprints := adoptedRuntimeFingerprintsByKind(target, discovered)
	if len(fingerprints) == 0 {
		return nil, fmt.Errorf("adopted runtime identity requires at least one stable fingerprint")
	}
	plan.binding = domain.AdoptionBinding{
		OrgID:            orgID,
		ServiceID:        plan.svc.ID,
		EnvironmentID:    plan.env.ID,
		DeploymentUnitID: &plan.unit.ID,
		BuildID:          &plan.build.ID,
		ArtifactID:       &plan.artifact.ID,
		HostAlias:        target.Name,
		EndpointRef:      target.EndpointRef,
		TargetName:       discovered.TargetName,
		ContainerID:      discovered.ContainerID,
		ImageDigest:      discovered.ImageDigest,
		Compose:          discovered.Compose,
		Fingerprints:     fingerprints,
		RequestID:        requestID,
		ServiceCreated:   plan.createdService,
		Status:           domain.AdoptionBindingInProgress,
		UpdatedAt:        now,
	}
	return plan, nil
}

func (s *AdoptionService) planEnvironment(ctx context.Context, plan *adoptionPlan, now time.Time) error {
	target := plan.target
	existing, err := s.environmentByName(ctx, target.EnvironmentName)
	if err != nil {
		return err
	}
	config := map[string]any{
		"type":            string(domain.RuntimeTypeDocker),
		"host_alias":      target.Name,
		"management_mode": "direct_runtime",
	}
	if target.EndpointRef != "" {
		config["endpoint_ref"] = target.EndpointRef
	} else {
		config["docker_host"] = target.DockerHost
	}
	if existing == nil {
		id := adoptionEntityID("environment", plan.orgID.String(), target.EnvironmentName)
		if occupant, err := s.environmentByID(ctx, id); err != nil {
			return err
		} else if occupant != nil {
			return fmt.Errorf("environment coordinate %s is already occupied by %q", id, occupant.Environment.Name)
		}
		env := domain.Environment{
			ID:            id,
			OrgID:         plan.orgID,
			Name:          target.EnvironmentName,
			RuntimeConfig: config,
		}
		if err := normalizeAndValidateEnvironmentMutation(&env, nil); err != nil {
			return fmt.Errorf("creating environment %q: %w", target.EnvironmentName, err)
		}
		env.CreatedAt, env.UpdatedAt = domain.NormalizeRevisionTime(now), domain.NormalizeRevisionTime(now)
		plan.env = env
		return nil
	}

	env := existing.Environment
	if env.OrgID != uuid.Nil && env.OrgID != plan.orgID {
		return fmt.Errorf("environment %q belongs to different org %s", target.EnvironmentName, env.OrgID)
	}
	changed := false
	if env.OrgID == uuid.Nil && plan.orgID != uuid.Nil {
		env.OrgID = plan.orgID
		changed = true
	}
	if env.RuntimeConfig == nil {
		env.RuntimeConfig = map[string]any{}
	} else {
		env.RuntimeConfig = copyRuntimeConfig(env.RuntimeConfig)
	}
	if currentType, ok := stringFromAny(env.RuntimeConfig["type"]); ok && currentType != "" && currentType != string(domain.RuntimeTypeDocker) {
		return fmt.Errorf("environment %q has incompatible runtime type %q", target.EnvironmentName, currentType)
	}
	if currentMode, ok := stringFromAny(env.RuntimeConfig["management_mode"]); ok && currentMode != "" && currentMode != "direct_runtime" {
		return fmt.Errorf("environment %q has incompatible management_mode %q", target.EnvironmentName, currentMode)
	}
	if currentEndpointRef, ok := stringFromAny(env.RuntimeConfig["endpoint_ref"]); ok && currentEndpointRef != "" && currentEndpointRef != target.EndpointRef {
		return fmt.Errorf("environment %q already targets endpoint_ref %q", target.EnvironmentName, currentEndpointRef)
	}
	if currentHost, ok := stringFromAny(env.RuntimeConfig["docker_host"]); ok && currentHost != "" && currentHost != target.DockerHost {
		return fmt.Errorf("environment %q already targets docker_host %q", target.EnvironmentName, currentHost)
	}
	if target.EndpointRef != "" {
		if _, ok := env.RuntimeConfig["docker_host"]; ok {
			delete(env.RuntimeConfig, "docker_host")
			changed = true
		}
	} else if _, ok := env.RuntimeConfig["endpoint_ref"]; ok {
		delete(env.RuntimeConfig, "endpoint_ref")
		changed = true
	}
	for k, v := range config {
		if env.RuntimeConfig[k] != v {
			env.RuntimeConfig[k] = v
			changed = true
		}
	}
	if changed {
		if err := prepareEnvironmentUpdate(&env); err != nil {
			return fmt.Errorf("updating environment %q: %w", target.EnvironmentName, err)
		}
	}
	plan.env = env
	plan.envUnits = append([]domain.DeploymentUnit(nil), existing.Units...)
	return nil
}

func (s *AdoptionService) environmentByID(ctx context.Context, id uuid.UUID) (*AdoptionEnvironment, error) {
	environments, err := s.view.ListEnvironments(ctx)
	if err != nil {
		return nil, fmt.Errorf("looking up environment %s: %w", id, err)
	}
	for i := range environments {
		if environments[i].Environment.ID == id {
			return &environments[i], nil
		}
	}
	return nil, nil
}

func (s *AdoptionService) environmentByName(ctx context.Context, name string) (*AdoptionEnvironment, error) {
	environments, err := s.view.ListEnvironments(ctx)
	if err != nil {
		return nil, fmt.Errorf("looking up environment %q: %w", name, err)
	}
	for i := range environments {
		if environments[i].Environment.Name == name {
			return &environments[i], nil
		}
	}
	return nil, nil
}

func planService(plan *adoptionPlan, known *knownServices, requestID, serviceName string, now time.Time) error {
	target, discovered := plan.target, plan.discovered
	adopted := adoptedRuntimeConfig(target, discovered, plan.classified)
	byIdentity, err := known.byAdoptedTarget(plan.orgID, target, discovered)
	if err != nil {
		return err
	}
	byName := known.byName(serviceName)
	if byName != nil && byName.OrgID != uuid.Nil && byName.OrgID != plan.orgID {
		return fmt.Errorf("service name %q belongs to different org %s", serviceName, byName.OrgID)
	}
	if byName != nil && byIdentity != nil && byName.ID != byIdentity.ID {
		return fmt.Errorf("service name %q already exists for a different target", serviceName)
	}
	if byName != nil && byIdentity == nil && byName.RuntimeConfig != nil &&
		byName.RuntimeConfig.Adopted != nil && !sameAdoptedTarget(byName, target, discovered) {
		return fmt.Errorf("service name %q already exists for a different target", serviceName)
	}

	existing := byIdentity
	if existing == nil {
		existing = byName
	}
	if existing == nil {
		id := adoptionEntityID("service", plan.orgID.String(), target.Name, discovered.TargetName)
		if occupant := known.byID(id); occupant != nil {
			// The derived coordinate names a record this adoption does not
			// own (it matched neither by identity nor by name): never replace it.
			return fmt.Errorf("service coordinate %s is already occupied by %q", id, occupant.Name)
		}
		svc := domain.Service{
			ID:            id,
			OrgID:         plan.orgID,
			Name:          serviceName,
			ArtifactRepo:  discovered.ImageRepo,
			RuntimeType:   domain.RuntimeTypeDocker,
			RuntimeConfig: &domain.ServiceRuntimeConfig{Adopted: adopted},
			CreatedAt:     now,
			UpdatedAt:     now,
		}
		prepareServiceCreate(&svc)
		normalizeServiceRepositoryForRead(&svc)
		plan.svc = svc
		plan.createdService = true
		return nil
	}
	if existing.OrgID != uuid.Nil && existing.OrgID != plan.orgID {
		return fmt.Errorf("service %q belongs to different org %s", existing.Name, existing.OrgID)
	}
	// A resumed request reports the outcome it had: its binding records
	// whether that same request created the service.
	if binding := known.binding(existing.ID, plan.env.ID); binding != nil && requestID != "" && binding.RequestID == requestID && binding.ServiceCreated {
		plan.createdService = true
	}
	svc := *existing
	desired := svc
	desired.OrgID = plan.orgID
	desired.RuntimeType = domain.RuntimeTypeDocker
	desired.ArtifactRepo = discovered.ImageRepo
	desired.RuntimeConfig = &domain.ServiceRuntimeConfig{Adopted: adopted}
	if !reflect.DeepEqual(svc, desired) {
		// A changed service takes a new revision; an unchanged one keeps its
		// record, so a resumed adoption signs nothing for it.
		prepareServiceUpdate(&desired)
	}
	normalizeServiceRepositoryForRead(&desired)
	plan.svc = desired
	return nil
}

// planDeploymentUnit makes the imported workload a durable, independently
// reconcilable ownership boundary of its environment: the explicit unit keyed
// by the service name, kept from the environment's record when it exists.
func planDeploymentUnit(plan *adoptionPlan, now time.Time) {
	key := normalizeResourceName(plan.svc.Name)
	for _, unit := range plan.envUnits {
		if unit.Key == key {
			plan.unit = unit
			return
		}
	}
	runtimeType := domain.RuntimeTypeDocker
	composeDir := ""
	if isComposeOrigin(plan.discovered) {
		runtimeType = domain.RuntimeTypeCompose
		if plan.discovered.Compose != nil {
			composeDir = strings.TrimSpace(plan.discovered.Compose.WorkingDir)
		}
	}
	unit := domain.DeploymentUnit{
		ID:            adoptionEntityID("deployment-unit", plan.env.ID.String(), key),
		EnvironmentID: plan.env.ID,
		Key:           key,
		DisplayName:   plan.svc.Name,
		RuntimeType:   runtimeType,
		EndpointRef:   strings.TrimSpace(plan.target.EndpointRef),
		ComposeDir:    composeDir,
		ReconcileMode: domain.ReconcileModeAutoApply,
		OwnershipMode: domain.OwnershipModeBahiaManaged,
		CreatedAt:     domain.NormalizeRevisionTime(now),
		UpdatedAt:     domain.NormalizeRevisionTime(now),
	}
	domain.NormalizeDeploymentUnitTargeting(&unit)
	plan.unit = unit
	plan.envUnits = append(plan.envUnits, unit)
}

func (s *AdoptionService) planBuild(ctx context.Context, plan *adoptionPlan, now time.Time) error {
	runID := adoptionRunID(plan.target, plan.discovered)
	builds, err := s.view.ListBuilds(ctx)
	if err != nil {
		return fmt.Errorf("looking up adoption build: %w", err)
	}
	for i := range builds {
		if builds[i].CISystem != adoptionCISystem || builds[i].CIRunID != runID {
			continue
		}
		if builds[i].ServiceID != plan.svc.ID {
			return fmt.Errorf("adoption build %q already belongs to service %s", runID, builds[i].ServiceID)
		}
		plan.build = builds[i]
		return nil
	}
	discovered := plan.discovered
	gitSHA := strings.TrimSpace(discovered.Labels["org.opencontainers.image.revision"])
	if gitSHA == "" {
		gitSHA = "adopted"
	}
	gitRef := discovered.ImageTag
	if gitRef == "" {
		gitRef = "adopted"
	}
	buildID := adoptionEntityID("build", plan.svc.ID.String(), runID)
	for i := range builds {
		if builds[i].ID == buildID {
			return fmt.Errorf("build coordinate %s is already occupied by %s run %q", buildID, builds[i].CISystem, builds[i].CIRunID)
		}
	}
	build := domain.Build{
		ID:        buildID,
		ServiceID: plan.svc.ID,
		GitSHA:    gitSHA,
		GitRef:    gitRef,
		CISystem:  adoptionCISystem,
		CIRunID:   runID,
		Status:    domain.BuildStatusSucceeded,
		Metadata: map[string]any{
			"import_source":  "adoption",
			"target_name":    plan.target.Name,
			"container_id":   discovered.ContainerID,
			"container_name": discovered.ContainerName,
			"image_ref":      discovered.ImageRef,
			"source_runtime": discovered.SourceRuntime,
			"compose":        discovered.Compose,
		},
		CreatedAt: now,
	}
	for k, v := range targetTransportMetadata(plan.target) {
		build.Metadata[k] = v
	}
	finished := now
	build.StartedAt = &finished
	build.FinishedAt = &finished
	plan.build = build
	return nil
}

func (s *AdoptionService) planArtifact(ctx context.Context, plan *adoptionPlan, now time.Time) error {
	discovered := plan.discovered
	artifacts, err := s.view.ListArtifacts(ctx)
	if err != nil {
		return fmt.Errorf("looking up adoption artifact: %w", err)
	}
	for i := range artifacts {
		if artifacts[i].ImageRepo != discovered.ImageRepo || artifacts[i].ImageDigest != discovered.ImageDigest {
			continue
		}
		if artifacts[i].ServiceID != plan.svc.ID {
			return fmt.Errorf("artifact %s@%s already belongs to service %s", discovered.ImageRepo, discovered.ImageDigest, artifacts[i].ServiceID)
		}
		plan.artifact = artifacts[i]
		return nil
	}
	imageTag := discovered.ImageTag
	if imageTag == "" {
		imageTag = "adopted"
	}
	artifactID := adoptionEntityID("artifact", plan.svc.ID.String(), discovered.ImageRepo, discovered.ImageDigest)
	for i := range artifacts {
		if artifacts[i].ID == artifactID {
			return fmt.Errorf("artifact coordinate %s is already occupied by %s@%s", artifactID, artifacts[i].ImageRepo, artifacts[i].ImageDigest)
		}
	}
	plan.artifact = domain.Artifact{
		ID:          artifactID,
		BuildID:     plan.build.ID,
		ServiceID:   plan.svc.ID,
		ImageRepo:   discovered.ImageRepo,
		ImageTag:    imageTag,
		ImageDigest: discovered.ImageDigest,
		ScanStatus:  domain.ScanStatusUnknown,
		Metadata: map[string]any{
			"import_source":  "adoption",
			"source_runtime": discovered.SourceRuntime,
			"container_id":   discovered.ContainerID,
			"container_name": discovered.ContainerName,
			"image_ref":      discovered.ImageRef,
		},
		CreatedAt: now,
	}
	return nil
}

// publishPlan publishes the plan's records in order and returns the step
// whose publish failed. The binding is published first in progress and last
// complete, so an interrupted adoption is visible until it is resumed.
func (s *AdoptionService) publishPlan(ctx context.Context, plan *adoptionPlan) (string, error) {
	binding := plan.binding
	binding.Status = domain.AdoptionBindingInProgress
	if err := s.canonical.PublishAdoptionBinding(ctx, &binding); err != nil {
		return adoptionStepBinding, fmt.Errorf("publish adoption binding: %w", err)
	}
	if err := s.canonical.PublishEnvironmentRegistry(ctx, &plan.env, plan.envUnits); err != nil {
		return adoptionStepEnvironment, fmt.Errorf("publish environment %q: %w", plan.env.Name, err)
	}
	if err := s.canonical.PublishServiceRegistry(ctx, &plan.svc); err != nil {
		return adoptionStepService, fmt.Errorf("publish service %q: %w", plan.svc.Name, err)
	}
	if err := s.canonical.PublishBuildRegistry(ctx, &plan.build); err != nil {
		return adoptionStepBuild, fmt.Errorf("publish adoption build: %w", err)
	}
	if err := s.canonical.PublishArtifactRegistry(ctx, &plan.artifact); err != nil {
		return adoptionStepArtifact, fmt.Errorf("publish adoption artifact: %w", err)
	}
	for _, secret := range plan.secrets {
		if err := s.importSecret(ctx, plan, secret); err != nil {
			return adoptionStepSecrets, err
		}
	}
	if err := s.canonical.PublishRuntimeObservation(ctx, &plan.obs); err != nil {
		return adoptionStepObservation, fmt.Errorf("publish runtime observation: %w", err)
	}
	if err := s.canonical.PublishServiceState(ctx, &plan.state, &plan.obs); err != nil {
		return adoptionStepState, fmt.Errorf("publish service state: %w", err)
	}
	binding.Status = domain.AdoptionBindingComplete
	if err := s.canonical.PublishAdoptionBinding(ctx, &binding); err != nil {
		return adoptionStepFinalize, fmt.Errorf("publish adoption binding: %w", err)
	}
	return adoptionStepComplete, nil
}

// importSecret stores one imported secret value, encrypted, in the secret
// value store and then publishes its reference. The value store is the only
// holder of the value; it is never published.
func (s *AdoptionService) importSecret(ctx context.Context, plan *adoptionPlan, secret plannedSecret) error {
	if s.secrets == nil || s.secretEncryptor == nil {
		return fmt.Errorf("sensitive environment values require configured secret storage and encryption")
	}
	ciphertext, encErr := s.secretEncryptor.Encrypt(secret.value, domain.EncryptionAES256)
	if encErr != nil {
		return fmt.Errorf("encrypting imported secret %q: %w", secret.name, encErr)
	}
	envID := plan.env.ID
	record := &domain.ServiceSecret{
		ID:               secret.id,
		ServiceID:        plan.svc.ID,
		EnvironmentID:    &envID,
		Name:             secret.name,
		EncryptedValue:   ciphertext,
		EncryptionMethod: domain.EncryptionAES256,
		Version:          1,
		CreatedBy:        "adoption",
		CreatedAt:        plan.obs.ObservedAt,
		UpdatedAt:        plan.obs.ObservedAt,
	}
	// The value is replaced atomically when a transaction executor exists,
	// so a failure between the delete and the create cannot lose a working
	// secret; the deterministic id makes a resumed replacement idempotent.
	replace := func(secrets repository.SecretRepository) error {
		if secrets == nil {
			secrets = s.secrets
		}
		if err := secrets.DeleteByName(ctx, plan.svc.ID, &envID, secret.name); err != nil {
			return fmt.Errorf("replacing imported secret %q: %w", secret.name, err)
		}
		if err := secrets.Create(ctx, record); err != nil {
			return fmt.Errorf("creating imported secret %q: %w", secret.name, err)
		}
		return nil
	}
	var err error
	if s.index.Tx != nil {
		err = s.index.Tx.WithinTx(ctx, func(repos repository.TxRepos) error { return replace(repos.Secrets) })
	} else {
		err = replace(nil)
	}
	if err != nil {
		return err
	}
	if err := s.canonical.PublishSecretRef(ctx, plan.orgID, record.ToRef()); err != nil {
		return fmt.Errorf("publish imported secret reference %q: %w", secret.name, err)
	}
	return nil
}

// writeIndex mirrors a published plan into the optional SQL index, in one
// transaction when one is configured. Every write is an upsert, so a resumed
// or repeated adoption converges on the same rows.
func (s *AdoptionService) writeIndex(ctx context.Context, plan *adoptionPlan) error {
	if !s.index.configured() {
		return nil
	}
	write := func(repos repository.TxRepos) error {
		repos = s.completeIndexRepos(repos)
		if err := upsertIndexEnvironment(ctx, repos.Environments, plan.env); err != nil {
			return err
		}
		if err := upsertIndexService(ctx, repos.Services, plan.svc); err != nil {
			return err
		}
		if err := upsertIndexDeploymentUnit(ctx, repos.DeploymentUnits, plan.unit); err != nil {
			return err
		}
		if err := upsertIndexBuild(ctx, repos.Builds, plan.build); err != nil {
			return err
		}
		if err := upsertIndexArtifact(ctx, repos.Artifacts, plan.artifact); err != nil {
			return err
		}
		if err := insertIndexObservation(ctx, repos.Observations, plan.obs); err != nil {
			return err
		}
		if repos.State != nil {
			state := plan.state
			if err := repos.State.Upsert(ctx, &state); err != nil {
				return fmt.Errorf("indexing environment service state: %w", err)
			}
		}
		if repos.AdoptedIdentities != nil {
			if err := repos.AdoptedIdentities.UpsertMany(ctx, plan.binding.Identities()); err != nil {
				return fmt.Errorf("indexing adopted runtime identities: %w", err)
			}
		}
		return nil
	}
	if s.index.Tx != nil {
		return s.index.Tx.WithinTx(ctx, write)
	}
	return write(repository.TxRepos{})
}

func (s *AdoptionService) completeIndexRepos(repos repository.TxRepos) repository.TxRepos {
	if repos.Services == nil {
		repos.Services = s.index.Services
	}
	if repos.Environments == nil {
		repos.Environments = s.index.Environments
	}
	if repos.Builds == nil {
		repos.Builds = s.index.Builds
	}
	if repos.Artifacts == nil {
		repos.Artifacts = s.index.Artifacts
	}
	if repos.DeploymentUnits == nil {
		repos.DeploymentUnits = s.index.DeploymentUnits
	}
	if repos.State == nil {
		repos.State = s.index.State
	}
	if repos.Observations == nil {
		repos.Observations = s.index.Observations
	}
	if repos.AdoptedIdentities == nil {
		repos.AdoptedIdentities = s.index.AdoptedIdentities
	}
	return repos
}

func upsertIndexEnvironment(ctx context.Context, repo repository.EnvironmentRepository, env domain.Environment) error {
	if repo == nil {
		return nil
	}
	existing, err := repo.GetByID(ctx, env.ID)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return fmt.Errorf("indexing environment %q: %w", env.Name, err)
	}
	if existing == nil {
		if err := repo.Create(ctx, &env); err != nil && !errors.Is(err, repository.ErrAlreadyExists) {
			return fmt.Errorf("indexing environment %q: %w", env.Name, err)
		} else if err == nil {
			return nil
		}
	}
	if err := repo.Update(ctx, &env); err != nil {
		return fmt.Errorf("indexing environment %q: %w", env.Name, err)
	}
	return nil
}

func upsertIndexService(ctx context.Context, repo repository.ServiceRepository, svc domain.Service) error {
	if repo == nil {
		return nil
	}
	existing, err := repo.GetByID(ctx, svc.ID)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return fmt.Errorf("indexing service %q: %w", svc.Name, err)
	}
	if existing == nil {
		if err := repo.Create(ctx, &svc); err != nil && !errors.Is(err, repository.ErrAlreadyExists) {
			return fmt.Errorf("indexing service %q: %w", svc.Name, err)
		} else if err == nil {
			return nil
		}
	}
	if err := repo.Update(ctx, &svc); err != nil {
		return fmt.Errorf("indexing service %q: %w", svc.Name, err)
	}
	return nil
}

func upsertIndexDeploymentUnit(ctx context.Context, repo repository.DeploymentUnitRepository, unit domain.DeploymentUnit) error {
	if repo == nil || unit.ID == uuid.Nil {
		return nil
	}
	existing, err := repo.GetByEnvironmentKey(ctx, unit.EnvironmentID, unit.Key)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return fmt.Errorf("indexing deployment unit %q: %w", unit.Key, err)
	}
	if existing != nil {
		return nil
	}
	if err := repo.Create(ctx, &unit); err != nil && !errors.Is(err, repository.ErrAlreadyExists) {
		return fmt.Errorf("indexing deployment unit %q: %w", unit.Key, err)
	}
	return nil
}

func upsertIndexBuild(ctx context.Context, repo repository.BuildRepository, build domain.Build) error {
	if repo == nil {
		return nil
	}
	existing, err := repo.GetByID(ctx, build.ID)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return fmt.Errorf("indexing adoption build: %w", err)
	}
	if existing == nil {
		existing, err = repo.GetByCISystemRunID(ctx, build.CISystem, build.CIRunID)
		if err != nil && !errors.Is(err, repository.ErrNotFound) {
			return fmt.Errorf("indexing adoption build: %w", err)
		}
	}
	if existing != nil {
		return nil
	}
	if err := repo.Create(ctx, &build); err != nil && !errors.Is(err, repository.ErrAlreadyExists) {
		return fmt.Errorf("indexing adoption build: %w", err)
	}
	return nil
}

func upsertIndexArtifact(ctx context.Context, repo repository.ArtifactRepository, artifact domain.Artifact) error {
	if repo == nil {
		return nil
	}
	existing, err := repo.GetByID(ctx, artifact.ID)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return fmt.Errorf("indexing adoption artifact: %w", err)
	}
	if existing == nil {
		existing, err = repo.GetByImageRepoDigest(ctx, artifact.ImageRepo, artifact.ImageDigest)
		if err != nil && !errors.Is(err, repository.ErrNotFound) {
			return fmt.Errorf("indexing adoption artifact: %w", err)
		}
	}
	if existing != nil {
		return nil
	}
	if err := repo.Create(ctx, &artifact); err != nil && !errors.Is(err, repository.ErrAlreadyExists) {
		return fmt.Errorf("indexing adoption artifact: %w", err)
	}
	return nil
}

func insertIndexObservation(ctx context.Context, repo repository.RuntimeObservationRepository, obs domain.RuntimeObservation) error {
	if repo == nil {
		return nil
	}
	latest, err := repo.GetLatest(ctx, obs.ServiceID, obs.EnvironmentID)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return fmt.Errorf("indexing runtime observation: %w", err)
	}
	if latest != nil && latest.ID == obs.ID {
		return nil
	}
	if err := repo.Create(ctx, &obs); err != nil && !errors.Is(err, repository.ErrAlreadyExists) {
		return fmt.Errorf("indexing runtime observation: %w", err)
	}
	return nil
}

// RebuildIndex replays every complete adoption binding and the canonical
// records it names into the optional SQL index. It is safe to repeat and never
// removes rows. Bindings whose adoption did not complete are skipped: their
// records are published, but a partial adoption must not be indexed as an
// adopted workload.
func (s *AdoptionService) RebuildIndex(ctx context.Context) error {
	if !s.index.configured() {
		return nil
	}
	if err := s.Ready(); err != nil {
		return err
	}
	bindings, err := s.view.ListAdoptionBindings(ctx)
	if err != nil {
		return err
	}
	var failed []error
	for _, binding := range bindings {
		if binding.Status != domain.AdoptionBindingComplete {
			continue
		}
		if err := s.rebuildBinding(ctx, binding); err != nil {
			failed = append(failed, fmt.Errorf("adoption %s/%s: %w", binding.ServiceID, binding.EnvironmentID, err))
		}
	}
	return errors.Join(failed...)
}

func (s *AdoptionService) rebuildBinding(ctx context.Context, binding domain.AdoptionBinding) error {
	known, err := s.loadKnownServices(ctx)
	if err != nil {
		return err
	}
	svc := known.byID(binding.ServiceID)
	if svc == nil {
		return fmt.Errorf("service record is missing")
	}
	environments, err := s.view.ListEnvironments(ctx)
	if err != nil {
		return err
	}
	var env *AdoptionEnvironment
	for i := range environments {
		if environments[i].Environment.ID == binding.EnvironmentID {
			env = &environments[i]
		}
	}
	if env == nil {
		return fmt.Errorf("environment record is missing")
	}
	write := func(repos repository.TxRepos) error {
		repos = s.completeIndexRepos(repos)
		if err := upsertIndexEnvironment(ctx, repos.Environments, env.Environment); err != nil {
			return err
		}
		if err := upsertIndexService(ctx, repos.Services, *svc); err != nil {
			return err
		}
		if binding.DeploymentUnitID != nil {
			for _, unit := range env.Units {
				if unit.ID == *binding.DeploymentUnitID {
					if err := upsertIndexDeploymentUnit(ctx, repos.DeploymentUnits, unit); err != nil {
						return err
					}
				}
			}
		}
		if binding.BuildID != nil {
			build, err := s.view.GetBuild(ctx, *binding.BuildID)
			if err != nil {
				return err
			}
			if build != nil {
				if err := upsertIndexBuild(ctx, repos.Builds, *build); err != nil {
					return err
				}
			}
		}
		if binding.ArtifactID != nil {
			artifact, err := s.view.GetArtifact(ctx, *binding.ArtifactID)
			if err != nil {
				return err
			}
			if artifact != nil {
				if err := upsertIndexArtifact(ctx, repos.Artifacts, *artifact); err != nil {
					return err
				}
			}
		}
		obs, err := s.view.GetRuntimeObservation(ctx, binding.ServiceID, binding.EnvironmentID)
		if err != nil {
			return err
		}
		if obs != nil {
			if err := insertIndexObservation(ctx, repos.Observations, *obs); err != nil {
				return err
			}
		}
		state, err := s.view.GetServiceState(ctx, binding.ServiceID, binding.EnvironmentID)
		if err != nil {
			return err
		}
		if state != nil && repos.State != nil {
			if err := repos.State.Upsert(ctx, state); err != nil {
				return fmt.Errorf("indexing environment service state: %w", err)
			}
		}
		if repos.AdoptedIdentities != nil {
			if err := repos.AdoptedIdentities.UpsertMany(ctx, binding.Identities()); err != nil {
				return fmt.Errorf("indexing adopted runtime identities: %w", err)
			}
		}
		return nil
	}
	if s.index.Tx != nil {
		return s.index.Tx.WithinTx(ctx, write)
	}
	return write(repository.TxRepos{})
}

// BackfillFromIndex publishes, once, an adoption binding for every adopted
// workload that exists only in the SQL-era adopted_runtime_identity table, so
// identity matching never depends on SQL again. A binding the local store
// already holds is left alone.
func (s *AdoptionService) BackfillFromIndex(ctx context.Context, marker AdoptionBackfillMarker) error {
	lister, ok := s.index.AdoptedIdentities.(adoptedIdentityLister)
	if !ok || marker == nil {
		return nil
	}
	if err := s.Ready(); err != nil {
		return err
	}
	if done, err := marker.GetControlRecord("bootstrap", adoptionBackfillMarker); err != nil || string(done) == "1" {
		return err
	}
	known, err := s.loadKnownServices(ctx)
	if err != nil {
		return err
	}
	type bindingKey struct{ service, environment uuid.UUID }
	grouped := map[bindingKey]*domain.AdoptionBinding{}
	var order []bindingKey
	for offset := 0; ; {
		page, err := lister.List(ctx, adoptionBackfillPage, offset)
		if err != nil {
			return err
		}
		for _, identity := range page {
			key := bindingKey{identity.ServiceID, identity.EnvironmentID}
			if known.binding(key.service, key.environment) != nil {
				continue
			}
			binding := grouped[key]
			if binding == nil {
				binding = &domain.AdoptionBinding{
					OrgID: identity.OrgID, ServiceID: identity.ServiceID, EnvironmentID: identity.EnvironmentID,
					HostAlias: identity.HostAlias, EndpointRef: identity.EndpointRef, TargetName: identity.TargetName,
					ContainerID: identity.ContainerID, ImageDigest: identity.ImageDigest, Compose: identity.Compose,
					Fingerprints: map[string]string{}, Status: domain.AdoptionBindingComplete, UpdatedAt: s.now(),
				}
				grouped[key] = binding
				order = append(order, key)
			}
			binding.Fingerprints[identity.FingerprintKind] = identity.Fingerprint
		}
		if len(page) < adoptionBackfillPage {
			break
		}
		offset += len(page)
	}
	for _, key := range order {
		if err := s.canonical.PublishAdoptionBinding(ctx, grouped[key]); err != nil {
			return fmt.Errorf("backfill adoption binding %s/%s: %w", key.service, key.environment, err)
		}
	}
	return marker.PutControlRecord("bootstrap", adoptionBackfillMarker, []byte("1"))
}

func (s *AdoptionService) logAdoptionImportResult(target AdoptionTarget, result AdoptionImportResult, outcome string) {
	fields := []zap.Field{
		zap.String("target_name", target.Name),
		zap.String("endpoint_ref", target.EndpointRef),
		zap.String("environment_name", target.EnvironmentName),
		zap.String("container_id", result.ContainerID),
		zap.String("container_name", result.ContainerName),
		zap.String("service_name", result.ServiceName),
		zap.String("status", result.Status),
		zap.String("step", result.Step),
		zap.String("result", outcome),
		zap.Int("redacted_env_key_count", len(result.RedactedEnvironmentKeys)),
		zap.Int("redacted_label_key_count", len(result.RedactedLabelKeys)),
	}
	if result.ServiceID != nil {
		fields = append(fields, zap.String("service_id", result.ServiceID.String()))
	}
	if result.EnvironmentID != nil {
		fields = append(fields, zap.String("environment_id", result.EnvironmentID.String()))
	}
	if result.ArtifactID != nil {
		fields = append(fields, zap.String("artifact_id", result.ArtifactID.String()))
	}
	if result.Error != "" {
		fields = append(fields, zap.String("error", result.Error))
	}
	if result.IndexError != "" {
		fields = append(fields, zap.String("index_error", result.IndexError))
	}
	s.logger.Info("adoption candidate import completed", fields...)
}

func adoptionPreviewOperationalStats(previews []AdoptionPreview) (candidateCount, redactedEnvKeyCount, redactedLabelKeyCount, targetErrorCount int) {
	for _, preview := range previews {
		if preview.Error != "" {
			targetErrorCount++
		}
		candidateCount += len(preview.Containers)
		for _, container := range preview.Containers {
			redactedEnvKeyCount += len(container.RedactedEnvironmentKeys)
			redactedLabelKeyCount += len(container.RedactedLabelKeys)
		}
	}
	return candidateCount, redactedEnvKeyCount, redactedLabelKeyCount, targetErrorCount
}

func adoptionImportOperationalStats(results []AdoptionImportResult) (successCount, failureCount, redactedEnvKeyCount, redactedLabelKeyCount int) {
	for _, result := range results {
		if result.Status == adoptionStatusFailed || result.Error != "" {
			failureCount++
		} else {
			successCount++
		}
		redactedEnvKeyCount += len(result.RedactedEnvironmentKeys)
		redactedLabelKeyCount += len(result.RedactedLabelKeys)
	}
	return successCount, failureCount, redactedEnvKeyCount, redactedLabelKeyCount
}

func (s *AdoptionService) resolveImportOrgID(ctx context.Context, requested uuid.UUID, targets []AdoptionTarget) (uuid.UUID, error) {
	if s.organizations == nil {
		return requested, nil
	}
	if requested != uuid.Nil {
		org, err := s.organizations.GetByID(ctx, requested)
		if err != nil {
			return uuid.Nil, fmt.Errorf("resolving adoption org_id %s: %w", requested, err)
		}
		if org == nil {
			return uuid.Nil, fmt.Errorf("resolving adoption org_id %s: %w", requested, repository.ErrNotFound)
		}
		return requested, nil
	}

	resolvedFromEnvironments := map[uuid.UUID]struct{}{}
	for _, target := range targets {
		env, err := s.environmentByName(ctx, target.EnvironmentName)
		if err != nil {
			return uuid.Nil, fmt.Errorf("looking up environment %q for org resolution: %w", target.EnvironmentName, err)
		}
		if env != nil && env.Environment.OrgID != uuid.Nil {
			resolvedFromEnvironments[env.Environment.OrgID] = struct{}{}
		}
	}
	if len(resolvedFromEnvironments) == 1 {
		for orgID := range resolvedFromEnvironments {
			return orgID, nil
		}
	}
	if len(resolvedFromEnvironments) > 1 {
		return uuid.Nil, fmt.Errorf("adoption import org_id is required because target environments resolve to multiple orgs")
	}

	orgs, err := s.organizations.List(ctx)
	if err != nil {
		return uuid.Nil, fmt.Errorf("listing organizations for adoption org resolution: %w", err)
	}
	if len(orgs) == 1 {
		return orgs[0].ID, nil
	}
	if len(orgs) == 0 {
		return uuid.Nil, fmt.Errorf("adoption import requires org_id because no organization is available for inference")
	}
	return uuid.Nil, fmt.Errorf("adoption import requires org_id because %d organizations are available", len(orgs))
}

func (s *AdoptionService) normalizeAdoptionTargets(targets []AdoptionTarget) ([]AdoptionTarget, error) {
	if len(targets) == 0 {
		return nil, fmt.Errorf("at least one adoption target is required")
	}
	out := make([]AdoptionTarget, 0, len(targets))
	seen := map[string]struct{}{}
	for _, target := range targets {
		target.Name = normalizeResourceName(target.Name)
		target.DockerHost = strings.TrimSpace(target.DockerHost)
		target.EndpointRef = strings.TrimSpace(target.EndpointRef)
		if target.EnvironmentName == "" {
			target.EnvironmentName = target.Name
		}
		target.EnvironmentName = normalizeResourceName(target.EnvironmentName)
		if target.Name == "" {
			return nil, fmt.Errorf("adoption target name is required")
		}
		if target.EndpointRef == "" && target.DockerHost == "" {
			target.EndpointRef = target.Name
		}
		if target.EndpointRef != "" && target.DockerHost != "" {
			return nil, fmt.Errorf("adoption target %q cannot combine endpoint_ref with docker_host", target.Name)
		}
		if target.EndpointRef != "" {
			endpoint, ok := s.runtimeCfg.Endpoints[target.EndpointRef]
			if !ok {
				return nil, fmt.Errorf("adoption target %q references unknown endpoint_ref %q", target.Name, target.EndpointRef)
			}
			if strings.TrimSpace(endpoint.DockerHost) == "" {
				return nil, fmt.Errorf("adoption target %q endpoint_ref %q has no docker_host", target.Name, target.EndpointRef)
			}
			endpoint.Ref = target.EndpointRef
			target.Endpoint = endpoint
			target.DockerHost = strings.TrimSpace(endpoint.DockerHost)
		} else {
			if !s.allowRawDockerHosts {
				return nil, fmt.Errorf("raw docker_host targets are disabled by adoption policy")
			}
			if target.DockerHost == "" {
				return nil, fmt.Errorf("docker_host is required for target %q", target.Name)
			}
		}
		if _, ok := seen[target.Name]; ok {
			return nil, fmt.Errorf("duplicate adoption target %q", target.Name)
		}
		seen[target.Name] = struct{}{}
		out = append(out, target)
	}
	return out, nil
}

func normalizeAdoptionSelections(req AdoptionImportRequest) (map[string]AdoptionSelection, error) {
	if !req.ImportAll && len(req.Selections) == 0 {
		return nil, fmt.Errorf("import requires import_all or at least one selection")
	}
	out := map[string]AdoptionSelection{}
	for _, selection := range req.Selections {
		selection.TargetName = normalizeResourceName(selection.TargetName)
		selection.ContainerID = strings.TrimSpace(selection.ContainerID)
		selection.ServiceNameOverride = strings.TrimSpace(selection.ServiceNameOverride)
		if selection.TargetName == "" || selection.ContainerID == "" {
			return nil, fmt.Errorf("selection target_name and container_id are required")
		}
		if selection.ServiceNameOverride != "" {
			selection.ServiceNameOverride = normalizeResourceName(selection.ServiceNameOverride)
			if selection.ServiceNameOverride == "" {
				return nil, fmt.Errorf("selection service_name_override is invalid")
			}
		}
		out[selectionKey(selection.TargetName, selection.ContainerID)] = selection
	}
	return out, nil
}

func toDockerDiscoveryTargets(targets []AdoptionTarget) []runtime.DockerDiscoveryTarget {
	out := make([]runtime.DockerDiscoveryTarget, 0, len(targets))
	for _, target := range targets {
		out = append(out, runtime.DockerDiscoveryTarget{Name: target.Name, DockerHost: target.DockerHost, EndpointRef: target.EndpointRef, Endpoint: target.Endpoint, EnvironmentName: target.EnvironmentName})
	}
	return out
}

func proposedServiceName(discovered runtime.DiscoveredContainer) string {
	if discovered.Compose != nil && discovered.Compose.ProjectName != "" && discovered.Compose.ServiceName != "" {
		return normalizeResourceName(discovered.Compose.ProjectName + "-" + discovered.Compose.ServiceName)
	}
	return normalizeResourceName(discovered.ContainerName)
}

func adoptedRuntimeConfig(target AdoptionTarget, discovered runtime.DiscoveredContainer, classified sensitiveDataClassification) *domain.AdoptedRuntimeConfig {
	return &domain.AdoptedRuntimeConfig{
		TargetName:    discovered.TargetName,
		ContainerID:   discovered.ContainerID,
		ImageDigest:   discovered.ImageDigest,
		SourceRuntime: discovered.SourceRuntime,
		HostAlias:     target.Name,
		EndpointRef:   target.EndpointRef,
		Environment:   copyStringMap(classified.SafeEnvironment),
		Ports:         append([]string(nil), discovered.Ports...),
		Volumes:       append([]string(nil), discovered.Volumes...),
		Restart:       discovered.Restart,
		Command:       append([]string(nil), discovered.Command...),
		Entrypoint:    append([]string(nil), discovered.Entrypoint...),
		WorkingDir:    discovered.WorkingDir,
		NetworkMode:   discovered.NetworkMode,
		Labels:        copyStringMap(classified.SafeLabels),
		Compose:       discovered.Compose,
	}
}

func sameAdoptedTarget(svc *domain.Service, target AdoptionTarget, discovered runtime.DiscoveredContainer) bool {
	if svc == nil || svc.RuntimeConfig == nil || svc.RuntimeConfig.Adopted == nil {
		return false
	}
	adopted := svc.RuntimeConfig.Adopted
	return adopted.TargetName == discovered.TargetName && adopted.HostAlias == target.Name
}

func isComposeOrigin(discovered runtime.DiscoveredContainer) bool {
	return strings.EqualFold(strings.TrimSpace(discovered.SourceRuntime), "compose") || discovered.Compose != nil
}

func adoptionRunID(target AdoptionTarget, discovered runtime.DiscoveredContainer) string {
	return strings.Join([]string{target.Name, discovered.TargetName, discovered.ImageDigest}, ":")
}

func adoptedRuntimeFingerprintsByKind(target AdoptionTarget, discovered runtime.DiscoveredContainer) map[string]string {
	anchor := target.EndpointRef
	if anchor == "" {
		anchor = target.Name
	}
	out := map[string]string{}
	if discovered.ContainerID != "" {
		out["container_id"] = strings.Join([]string{"container_id", anchor, discovered.ContainerID}, "|")
	}
	if discovered.ImageDigest != "" && discovered.TargetName != "" {
		out["image_digest"] = strings.Join([]string{"image_digest", anchor, discovered.TargetName, discovered.ImageDigest}, "|")
	}
	if discovered.Compose != nil && discovered.Compose.ProjectName != "" && discovered.Compose.ServiceName != "" {
		parts := []string{"compose_coordinates", anchor, discovered.Compose.ProjectName, discovered.Compose.ServiceName, discovered.Compose.WorkingDir}
		parts = append(parts, discovered.Compose.ConfigFiles...)
		out["compose_coordinates"] = strings.Join(parts, "|")
	}
	if discovered.TargetName != "" {
		out["endpoint_target"] = strings.Join([]string{"endpoint_target", anchor, discovered.TargetName}, "|")
	}
	return out
}

type sensitiveDataClassification struct {
	SafeEnvironment          map[string]string
	SensitiveEnvironment     map[string]string
	SensitiveEnvironmentKeys []string
	SafeLabels               map[string]string
	SensitiveLabels          map[string]string
	SensitiveLabelKeys       []string
}

func classifyDiscoveredSensitiveData(discovered runtime.DiscoveredContainer) sensitiveDataClassification {
	return sensitiveDataClassification{
		SafeEnvironment:          safeEntries(discovered.Environment, isSensitiveEnvironmentKey),
		SensitiveEnvironment:     sensitiveEntries(discovered.Environment, isSensitiveEnvironmentKey),
		SensitiveEnvironmentKeys: sensitiveKeys(discovered.Environment, isSensitiveEnvironmentKey),
		SafeLabels:               safeEntries(discovered.Labels, isSensitiveLabelKey),
		SensitiveLabels:          sensitiveEntries(discovered.Labels, isSensitiveLabelKey),
		SensitiveLabelKeys:       sensitiveKeys(discovered.Labels, isSensitiveLabelKey),
	}
}

func isSensitiveEnvironmentKey(key string) bool {
	k := strings.ToUpper(strings.TrimSpace(key))
	if k == "" {
		return false
	}
	for _, token := range []string{"PASSWORD", "PASSWD", "PASS", "TOKEN", "SECRET", "PRIVATE", "CREDENTIAL", "API_KEY", "ACCESS_KEY", "AUTH", "BEARER", "COOKIE", "SESSION", "JWT", "MACAROON", "BUNKER", "NSEC", "DATABASE_URL", "DB_URL", "REDIS_URL", "POSTGRES_DSN", "POSTGRES_URL", "MYSQL_DSN", "MYSQL_URL", "MONGODB_URI", "MONGO_URI", "AMQP_URL", "RABBITMQ_URL", "CONNECTION_STRING", "DSN"} {
		if strings.Contains(k, token) {
			return true
		}
	}
	for _, prefix := range []string{"AWS_", "GCP_", "GOOGLE_", "AZURE_", "DOCKER_AUTH", "NPM_TOKEN", "GH_TOKEN", "GITHUB_TOKEN", "SLACK_", "STRIPE_", "SENTRY_DSN"} {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	return strings.HasSuffix(k, "_KEY") || strings.HasSuffix(k, "_CERT") || strings.HasSuffix(k, "_CERTIFICATE")
}

func isSensitiveLabelKey(key string) bool {
	k := strings.ToUpper(strings.TrimSpace(key))
	if k == "" {
		return false
	}
	for _, token := range []string{"PASSWORD", "PASSWD", "TOKEN", "SECRET", "PRIVATE", "CREDENTIAL", "API_KEY", "ACCESS_KEY", "AUTH", "BEARER", "COOKIE", "SESSION", "JWT", "MACAROON", "BUNKER", "NSEC"} {
		if strings.Contains(k, token) {
			return true
		}
	}
	return false
}

func safeEntries(in map[string]string, isSensitive func(string) bool) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := map[string]string{}
	for k, v := range in {
		if !isSensitive(k) {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func sensitiveEntries(in map[string]string, isSensitive func(string) bool) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := map[string]string{}
	for k, v := range in {
		if isSensitive(k) {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func sensitiveKeys(in map[string]string, isSensitive func(string) bool) []string {
	if len(in) == 0 {
		return nil
	}
	var keys []string
	for k := range in {
		if isSensitive(k) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

func sortedStringKeys(in map[string]string) []string {
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func observationFromDiscovered(target AdoptionTarget, serviceID, envID uuid.UUID, discovered runtime.DiscoveredContainer) *domain.RuntimeObservation {
	obs := &domain.RuntimeObservation{
		ServiceID:           serviceID,
		EnvironmentID:       envID,
		ObservedImageDigest: discovered.ImageDigest,
		ObservedImageRepo:   discovered.ImageRepo,
		ObservedContainerID: discovered.ContainerID,
		ObservedHost:        target.Name,
		ObservedVersion:     discovered.ImageRef,
		HealthStatus:        discovered.HealthStatus,
		Source:              "adoption",
		Metadata: map[string]any{
			"import_source":  "adoption",
			"target_name":    target.Name,
			"container_name": discovered.ContainerName,
			"source_runtime": discovered.SourceRuntime,
			"warnings":       discovered.Warnings,
		},
		ObservedAt: time.Now().UTC(),
	}
	for k, v := range targetTransportMetadata(target) {
		obs.Metadata[k] = v
	}
	return obs
}

func targetTransportMetadata(target AdoptionTarget) map[string]any {
	if target.EndpointRef != "" {
		return map[string]any{"endpoint_ref": target.EndpointRef}
	}
	if target.DockerHost != "" {
		return map[string]any{"docker_host": target.DockerHost}
	}
	return nil
}

func selectionsForTarget(selections map[string]AdoptionSelection, targetName string) []AdoptionSelection {
	var out []AdoptionSelection
	prefix := targetName + "/"
	for key, selection := range selections {
		if strings.HasPrefix(key, prefix) {
			out = append(out, selection)
		}
	}
	return out
}

func selectionKey(targetName, containerID string) string {
	return targetName + "/" + containerID
}

func shortID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}

var invalidResourceNameChars = regexp.MustCompile(`[^a-z0-9-]+`)

func normalizeResourceName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = invalidResourceNameChars.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-")
	for strings.Contains(name, "--") {
		name = strings.ReplaceAll(name, "--", "-")
	}
	return name
}

func stringFromAny(v any) (string, bool) {
	s, ok := v.(string)
	if !ok {
		return "", false
	}
	return strings.TrimSpace(s), true
}

func copyRuntimeConfig(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
