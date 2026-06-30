package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	sbomadapter "github.com/openagentsinc/bahia/internal/adapters/sbom"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

const (
	KindSBOMStatus = 30315
	KindSBOMAudit  = 4903
)

type SBOMVerifiedPublisher interface {
	PublishSignedEventWithResults(ctx context.Context, ev *nostr.Event) ([]sbomadapter.PublishOKResult, error)
}

type SBOMAvailabilitySubscriber interface {
	SubscribeAllWithEOSE(context.Context, []nostr.Filter) (SBOMAvailabilitySubscription, error)
	AuthenticateRelay(context.Context, string) error
}

type SBOMAvailabilitySubscription interface {
	Next(context.Context) (SBOMAvailabilitySubscriptionMessage, bool, error)
	Close()
}

type SBOMAvailabilitySubscriptionMessage struct {
	Event     *nostr.Event
	EOSE      bool
	RelayEOSE SBOMAvailabilityRelayEOSE
	Closed    SBOMAvailabilityRelayClosed
	Auth      SBOMAvailabilityRelayAuth
}

type SBOMAvailabilityRelayEOSE struct {
	RelayURL       string
	SubscriptionID string
}

type SBOMAvailabilityRelayClosed struct {
	RelayURL       string
	SubscriptionID string
	Reason         string
}

type SBOMAvailabilityRelayAuth struct {
	RelayURL   string
	Challenge  string
	ReasonHint string
}

type SBOMGenerateRequest struct {
	IDempotencyKey string                    `json:"idempotencyKey"`
	Subject        domain.SBOMSubject        `json:"subject"`
	SubjectLocator domain.SBOMSubjectLocator `json:"subjectLocator,omitempty"`
	Source         sbomadapter.SourceRequest `json:"source"`
	Formats        []domain.SBOMFormat       `json:"formats"`
	Generator      sbomadapter.GeneratorID   `json:"generator"`
	Storage        domain.SBOMStorageType    `json:"storage"`
}

type SBOMImportRequest struct {
	IDempotencyKey string                    `json:"idempotencyKey"`
	Subject        domain.SBOMSubject        `json:"subject"`
	SubjectLocator domain.SBOMSubjectLocator `json:"subjectLocator,omitempty"`
	Format         domain.SBOMFormat         `json:"format,omitempty"`
	Payload        []byte                    `json:"-"`
	Location       *domain.SBOMLocation      `json:"location,omitempty"`
	Storage        domain.SBOMStorageType    `json:"storage"`
	Generator      domain.SBOMGenerator      `json:"generator,omitempty"`
}

type SBOMRunResult struct {
	RunID             string                  `json:"run_id"`
	Subject           domain.SBOMSubject      `json:"subject"`
	ManifestIDs       []uuid.UUID             `json:"manifest_ids"`
	ReferenceEventIDs []string                `json:"reference_event_ids"`
	AvailabilityID    string                  `json:"availability_event_id"`
	StatusDTag        string                  `json:"status_d_tag"`
	PublishState      domain.SBOMPublishState `json:"publish_state"`
}

type SBOMSubjectResolver struct {
	Artifacts   repository.ArtifactRepository
	Deployments repository.DeploymentIntentRepository
	Packages    repository.PackageControlPlaneRepository
	Services    repository.ServiceRepository
}

func (r SBOMSubjectResolver) Resolve(ctx context.Context, subject domain.SBOMSubject) (domain.SBOMSubject, error) {
	return r.ResolveWithLocator(ctx, subject, domain.SBOMSubjectLocator{})
}

func (r SBOMSubjectResolver) ResolveWithLocator(ctx context.Context, subject domain.SBOMSubject, locator domain.SBOMSubjectLocator) (domain.SBOMSubject, error) {
	subject.ID = strings.TrimSpace(subject.ID)
	subject.DisplayName = strings.TrimSpace(subject.DisplayName)
	subject.Digest = strings.TrimSpace(subject.Digest)
	if subject.Type == "" {
		return subject, fmt.Errorf("SBOM subject type is required")
	}
	if subject.Digest != "" {
		if subject.ID == "" {
			return subject, fmt.Errorf("SBOM subject id is required")
		}
		return subject, nil
	}
	id, idErr := uuid.Parse(subject.ID)
	switch subject.Type {
	case domain.SBOMSubjectArtifact:
		if subject.ID == "" || r.Artifacts == nil || idErr != nil {
			return subject, fmt.Errorf("artifact subject digest is required when artifact repository/id resolution is unavailable")
		}
		artifact, err := r.Artifacts.GetByID(ctx, id)
		if err != nil {
			return subject, fmt.Errorf("resolve artifact subject: %w", err)
		}
		subject.Digest = artifact.ImageDigest
		subject.DisplayName = strings.TrimSpace(artifact.ImageRepo + ":" + artifact.ImageTag)
	case domain.SBOMSubjectDeployment:
		if subject.ID == "" || r.Deployments == nil || idErr != nil {
			return subject, fmt.Errorf("deployment subject digest is required when deployment repository/id resolution is unavailable")
		}
		intent, err := r.Deployments.GetByID(ctx, id)
		if err != nil {
			return subject, fmt.Errorf("resolve deployment subject: %w", err)
		}
		if intent.DesiredHash == "" {
			return subject, fmt.Errorf("deployment %s has no desired_hash; deployment SBOM subject digest is ambiguous", subject.ID)
		}
		subject.Digest = intent.DesiredHash
		subject.DisplayName = subject.ID
	case domain.SBOMSubjectPackage:
		return r.resolvePackageSubject(ctx, subject, locator.Package)
	case domain.SBOMSubjectRepository:
		return resolveRepositorySubject(subject, locator.Repository)
	default:
		return subject, fmt.Errorf("unsupported SBOM subject type %q", subject.Type)
	}
	if subject.Digest == "" {
		return subject, fmt.Errorf("resolved %s subject %s has no digest", subject.Type, subject.ID)
	}
	return subject, nil
}

var gitCommitPattern = regexp.MustCompile(`^[A-Fa-f0-9]{40}([A-Fa-f0-9]{24})?$`)

func (r SBOMSubjectResolver) resolvePackageSubject(ctx context.Context, subject domain.SBOMSubject, locator *domain.SBOMPackageArtifactLocator) (domain.SBOMSubject, error) {
	if locator == nil {
		return subject, fmt.Errorf("package subject digest resolution requires subjectLocator.package with repository_id, package_name, version, filename, and sha256")
	}
	if r.Packages == nil {
		return subject, fmt.Errorf("package subject digest resolution requires package projection repository")
	}
	repositoryID, err := uuid.Parse(strings.TrimSpace(locator.RepositoryID))
	if err != nil {
		return subject, fmt.Errorf("subjectLocator.package.repository_id must be a UUID")
	}
	packageName := strings.TrimSpace(locator.PackageName)
	version := strings.TrimSpace(locator.Version)
	filename := strings.TrimSpace(locator.Filename)
	if packageName == "" || version == "" || filename == "" {
		return subject, fmt.Errorf("subjectLocator.package package_name, version, and filename are required")
	}
	wantSHA, err := canonicalSHA256(locator.SHA256)
	if err != nil {
		return subject, fmt.Errorf("subjectLocator.package.sha256: %w", err)
	}
	artifact, err := r.Packages.GetArtifact(ctx, repositoryID, strings.TrimSpace(locator.Namespace), packageName, version, filename)
	if err != nil {
		return subject, fmt.Errorf("resolve package artifact subject: %w", err)
	}
	if artifact == nil || artifact.Deleted || artifact.Status == domain.PackageArtifactStatusDeleted {
		return subject, fmt.Errorf("package artifact %s/%s@%s %s not found", repositoryID, packageName, version, filename)
	}
	if artifact.Status != domain.PackageArtifactStatusAvailable {
		return subject, fmt.Errorf("package artifact %s/%s@%s %s is not available", repositoryID, packageName, version, filename)
	}
	artifactSHA, err := canonicalSHA256(artifact.SHA256)
	if err != nil {
		return subject, fmt.Errorf("resolved package artifact has invalid sha256: %w", err)
	}
	if artifactSHA != wantSHA {
		return subject, fmt.Errorf("package artifact sha256 mismatch: locator %s projection %s", wantSHA, artifactSHA)
	}
	canonicalID := packageSubjectID(*artifact)
	if subject.ID == "" {
		subject.ID = canonicalID
	} else if subject.ID != canonicalID {
		return subject, fmt.Errorf("package subject id %q does not match canonical package artifact id %q", subject.ID, canonicalID)
	}
	if subject.DisplayName == "" {
		subject.DisplayName = packageSubjectDisplayName(*artifact)
	}
	subject.Digest = "sha256:" + wantSHA
	return subject, nil
}

func resolveRepositorySubject(subject domain.SBOMSubject, locator *domain.SBOMRepositoryLocator) (domain.SBOMSubject, error) {
	if locator == nil {
		return subject, fmt.Errorf("repository subject digest resolution requires subjectLocator.repository with commit or content_digest")
	}
	commit := strings.TrimSpace(locator.Commit)
	contentDigest := strings.TrimSpace(locator.ContentDigest)
	if commit == "" && contentDigest == "" {
		return subject, fmt.Errorf("subjectLocator.repository commit or content_digest is required")
	}
	if commit != "" && contentDigest != "" {
		return subject, fmt.Errorf("subjectLocator.repository must provide either commit or content_digest, not both")
	}
	identity := firstNonEmptySBOMLocator(strings.TrimSpace(locator.RepositoryURL), strings.TrimSpace(locator.Repository), subject.ID)
	if identity == "" {
		return subject, fmt.Errorf("repository subject id or subjectLocator.repository repository_url/repository is required")
	}
	if subject.ID == "" {
		subject.ID = identity
	}
	if subject.DisplayName == "" {
		subject.DisplayName = identity
	}
	if commit != "" {
		if !gitCommitPattern.MatchString(commit) {
			return subject, fmt.Errorf("subjectLocator.repository.commit must be a 40- or 64-character hex git object id")
		}
		subject.Digest = "git:" + strings.ToLower(commit)
		return subject, nil
	}
	digest, err := canonicalDigest(contentDigest)
	if err != nil {
		return subject, fmt.Errorf("subjectLocator.repository.content_digest: %w", err)
	}
	subject.Digest = digest
	return subject, nil
}

func canonicalSHA256(value string) (string, error) {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "sha256:")
	if len(value) != 64 {
		return "", fmt.Errorf("must be a 64-character SHA-256 hex digest")
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", fmt.Errorf("must be lowercase or uppercase hex: %w", err)
	}
	return strings.ToLower(value), nil
}

func canonicalDigest(value string) (string, error) {
	value = strings.TrimSpace(value)
	parts := strings.SplitN(value, ":", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", fmt.Errorf("must use sha256:<64-hex> form")
	}
	algo := strings.ToLower(strings.TrimSpace(parts[0]))
	if algo != "sha256" {
		return "", fmt.Errorf("must use sha256:<64-hex> form")
	}
	sha, err := canonicalSHA256(parts[1])
	if err != nil {
		return "", err
	}
	return "sha256:" + sha, nil
}

func packageSubjectID(artifact domain.PackageArtifact) string {
	parts := []string{"pkg", string(artifact.Format), artifact.RepositoryID.String(), strings.TrimSpace(artifact.Namespace), strings.TrimSpace(artifact.PackageName), strings.TrimSpace(artifact.Version), strings.TrimSpace(artifact.Filename)}
	return strings.Join(parts, ":")
}

func packageSubjectDisplayName(artifact domain.PackageArtifact) string {
	name := strings.TrimSpace(artifact.PackageName)
	if ns := strings.TrimSpace(artifact.Namespace); ns != "" {
		name = ns + "/" + name
	}
	if artifact.Version != "" {
		name += "@" + strings.TrimSpace(artifact.Version)
	}
	if artifact.Filename != "" {
		name += " (" + strings.TrimSpace(artifact.Filename) + ")"
	}
	return name
}

func firstNonEmptySBOMLocator(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

type SBOMOrchestrator struct {
	Generators *sbomadapter.GeneratorRegistry
	Storage    *sbomadapter.StorageResolver
	Repo       repository.SBOMManifestRepository
	Publisher  SBOMVerifiedPublisher
	Subscriber SBOMAvailabilitySubscriber
	Resolver   SBOMSubjectResolver
	Pubkey     string
	Logger     *zap.Logger

	mu      sync.Mutex
	results map[string]SBOMRunResult
	locks   map[string]*sync.Mutex
}

type SBOMOrchestratorConfig struct {
	Generators *sbomadapter.GeneratorRegistry
	Storage    *sbomadapter.StorageResolver
	Repo       repository.SBOMManifestRepository
	Publisher  SBOMVerifiedPublisher
	Subscriber SBOMAvailabilitySubscriber
	Resolver   SBOMSubjectResolver
	Pubkey     string
	Logger     *zap.Logger
}

func NewSBOMOrchestrator(cfg SBOMOrchestratorConfig) *SBOMOrchestrator {
	logger := cfg.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	return &SBOMOrchestrator{Generators: cfg.Generators, Storage: cfg.Storage, Repo: cfg.Repo, Publisher: cfg.Publisher, Subscriber: cfg.Subscriber, Resolver: cfg.Resolver, Pubkey: strings.TrimSpace(cfg.Pubkey), Logger: logger.Named("sbom-orchestrator"), results: map[string]SBOMRunResult{}, locks: map[string]*sync.Mutex{}}
}

func (s *SBOMOrchestrator) Generate(ctx context.Context, req SBOMGenerateRequest) (*SBOMRunResult, error) {
	if s == nil || s.Generators == nil {
		return nil, fmt.Errorf("SBOM generator registry is not configured")
	}
	if req.Storage != "" && req.Storage != domain.SBOMStorageBlossom {
		return nil, fmt.Errorf("generated SBOM storage must be blossom")
	}
	if len(req.Formats) == 0 {
		req.Formats = []domain.SBOMFormat{domain.SBOMFormatSPDX}
	}
	return s.run(ctx, req.IDempotencyKey, req.Subject, req.SubjectLocator, domain.SBOMSourceGenerated, func(subject domain.SBOMSubject) ([]sbomadapter.GenerateResult, error) {
		out := make([]sbomadapter.GenerateResult, 0, len(req.Formats))
		for _, format := range req.Formats {
			result, err := s.Generators.GenerateSBOM(ctx, sbomadapter.GenerateRequest{Subject: subject, Source: req.Source, Format: format, Generator: req.Generator})
			if err != nil {
				return nil, err
			}
			out = append(out, *result)
		}
		return out, nil
	})
}

func (s *SBOMOrchestrator) Import(ctx context.Context, req SBOMImportRequest) (*SBOMRunResult, error) {
	if req.Storage != "" && req.Storage != domain.SBOMStorageBlossom {
		return nil, fmt.Errorf("imported SBOM storage must be blossom")
	}
	return s.run(ctx, req.IDempotencyKey, req.Subject, req.SubjectLocator, domain.SBOMSourceImported, func(subject domain.SBOMSubject) ([]sbomadapter.GenerateResult, error) {
		payload := req.Payload
		if len(payload) == 0 {
			if req.Location == nil {
				return nil, fmt.Errorf("import payload or location is required")
			}
			data, err := s.Storage.Resolve(ctx, sbomadapter.ResolveInput{Location: *req.Location})
			if err != nil {
				return nil, err
			}
			payload = data
		}
		format := req.Format
		if format == "" {
			parsed, err := sbomadapter.ParseManifest(payload, subject)
			if err != nil {
				return nil, err
			}
			format = parsed.Manifest.Format
		}
		generator := req.Generator
		if generator.ID == "" {
			generator = domain.SBOMGenerator{ID: "import"}
		}
		return []sbomadapter.GenerateResult{{Subject: subject, Format: format, MediaType: sbomadapter.MediaTypeForFormat(format), Payload: payload, Generator: generator}}, nil
	})
}

func (s *SBOMOrchestrator) run(ctx context.Context, key string, subject domain.SBOMSubject, locator domain.SBOMSubjectLocator, sourceKind domain.SBOMSourceKind, produce func(domain.SBOMSubject) ([]sbomadapter.GenerateResult, error)) (*SBOMRunResult, error) {
	if err := s.validateRuntimeConfigured(); err != nil {
		return nil, err
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, fmt.Errorf("idempotencyKey is required")
	}
	keyLock := s.lockForKey("idempotency", key)
	keyLock.Lock()
	defer keyLock.Unlock()
	if cached, ok := s.cached(key); ok {
		return &cached, nil
	}
	runID := key
	statusD, err := SBOMStatusDTag(runID)
	if err != nil {
		return nil, err
	}
	if err := s.publishStatus(ctx, statusD, subject, "accepted", "accepted", ""); err != nil {
		return nil, err
	}
	subject, err = s.Resolver.ResolveWithLocator(ctx, subject, locator)
	if err != nil {
		_ = s.publishStatus(ctx, statusD, subject, "failed", "resolving_subject", err.Error())
		return nil, err
	}
	lock := s.lockFor(subject)
	lock.Lock()
	defer lock.Unlock()
	if err := s.publishStatus(ctx, statusD, subject, "running", "resolving_subject", ""); err != nil {
		return nil, err
	}
	produced, err := produce(subject)
	if err != nil {
		_ = s.publishStatus(ctx, statusD, subject, "failed", "generating", err.Error())
		return nil, err
	}
	result := SBOMRunResult{RunID: runID, Subject: subject, StatusDTag: statusD, PublishState: domain.SBOMPublishPublished}
	var availabilityEntries []domain.SBOMIndexEntry
	if existing, err := s.Repo.ListManifestsBySubject(ctx, subject, 500); err == nil {
		for _, manifest := range existing {
			if manifest.PublishState == domain.SBOMPublishPublished && manifest.ReferenceDTag != "" && manifest.StorageURI != "" && manifest.PayloadSHA256 != "" {
				availabilityEntries = append(availabilityEntries, manifestEntry(manifest, s.Pubkey))
			}
		}
	} else {
		_ = s.publishStatus(ctx, statusD, subject, "failed", "publishing_available_list", "load existing SBOM availability projection: "+err.Error())
		return nil, fmt.Errorf("load existing SBOM availability projection: %w", err)
	}
	type pendingProjection struct {
		manifest *domain.SBOMManifest
		packages []domain.SBOMManifestPackage
	}
	projected := make([]pendingProjection, 0, len(produced))
	for _, item := range produced {
		manifest, pkgs, entry, refID, err := s.processOne(ctx, statusD, item, sourceKind)
		if err != nil {
			_ = s.publishStatus(ctx, statusD, subject, "failed", "publishing_reference", err.Error())
			return nil, err
		}
		availabilityEntries = append(availabilityEntries, entry)
		projected = append(projected, pendingProjection{manifest: manifest, packages: pkgs})
		result.ManifestIDs = append(result.ManifestIDs, manifest.ID)
		result.ReferenceEventIDs = append(result.ReferenceEventIDs, refID)
		result.AvailabilityID = ""
	}
	if err := s.publishStatus(ctx, statusD, subject, "running", "publishing_available_list", ""); err != nil {
		return nil, err
	}
	availabilityEntries, err = s.mergeRelayAvailabilityEntries(ctx, subject, availabilityEntries)
	if err != nil {
		_ = s.publishStatus(ctx, statusD, subject, "failed", "publishing_available_list", err.Error())
		return nil, err
	}
	listEvent, listD, err := sbomadapter.BuildSBOMAvailabilityListEvent(sbomadapter.BuildSBOMAvailabilityListEventInput{Subject: subject, Entries: availabilityEntries, PublisherPubkey: s.Pubkey})
	if err != nil {
		_ = s.publishStatus(ctx, statusD, subject, "failed", "publishing_available_list", err.Error())
		return nil, err
	}
	listID, err := s.publishVerified(ctx, listEvent, "SBOM availability list")
	if err != nil {
		_ = s.publishStatus(ctx, statusD, subject, "failed", "publishing_available_list", err.Error())
		return nil, err
	}
	result.AvailabilityID = listID
	if err := s.publishStatus(ctx, statusD, subject, "running", "projecting", ""); err != nil {
		s.Logger.Warn("publish SBOM projecting status failed after canonical events were accepted", zap.Error(err))
	}
	for _, projection := range projected {
		projection.manifest.AvailabilityEventID = listID
		projection.manifest.AvailabilityDTag = listD
		if err := s.Repo.ProjectManifest(ctx, projection.manifest, projection.packages); err != nil {
			_ = s.publishAudit(ctx, subject, "sbom.projection_failed", err.Error())
			_ = s.publishStatus(ctx, statusD, subject, "failed", "projecting", err.Error())
			return nil, err
		}
	}
	if err := s.publishAudit(ctx, subject, auditAction(sourceKind), ""); err != nil {
		s.Logger.Warn("publish SBOM audit failed after canonical events were accepted", zap.Error(err))
	}
	if err := s.publishStatus(ctx, statusD, subject, "completed", "completed", ""); err != nil {
		s.Logger.Warn("publish SBOM completed status failed after canonical events were accepted", zap.Error(err))
	}
	s.remember(key, result)
	return &result, nil
}

func (s *SBOMOrchestrator) mergeRelayAvailabilityEntries(ctx context.Context, subject domain.SBOMSubject, local []domain.SBOMIndexEntry) ([]domain.SBOMIndexEntry, error) {
	filter, err := s.availabilityListFilter(subject)
	if err != nil {
		return nil, err
	}
	sub, err := s.Subscriber.SubscribeAllWithEOSE(ctx, []nostr.Filter{filter})
	if err != nil {
		return nil, fmt.Errorf("subscribe current SBOM availability list: %w", err)
	}
	defer sub.Close()

	merged := append([]domain.SBOMIndexEntry(nil), local...)
	seenEvents := map[string]struct{}{}
	for {
		msg, ok, err := sub.Next(ctx)
		if err != nil {
			return nil, fmt.Errorf("read current SBOM availability list subscription: %w", err)
		}
		if !ok {
			return nil, fmt.Errorf("SBOM availability list subscription ended before EOSE")
		}
		switch {
		case msg.Event != nil:
			eventID := nostrutil.EventIDHex(msg.Event)
			if _, exists := seenEvents[eventID]; exists {
				continue
			}
			seenEvents[eventID] = struct{}{}
			entries, err := s.entriesFromRelayAvailabilityEvent(subject, msg.Event)
			if err != nil {
				return nil, fmt.Errorf("validate current SBOM availability event %s: %w", eventID, err)
			}
			merged = append(merged, entries...)
		case msg.EOSE:
			return merged, nil
		case msg.Auth.RelayURL != "" || msg.Auth.Challenge != "" || msg.Auth.ReasonHint != "":
			if msg.Auth.RelayURL == "" {
				return nil, fmt.Errorf("SBOM availability subscription AUTH challenge missing relay URL")
			}
			if err := s.Subscriber.AuthenticateRelay(ctx, msg.Auth.RelayURL); err != nil {
				return nil, fmt.Errorf("authenticate SBOM availability relay %s: %w", msg.Auth.RelayURL, err)
			}
		case msg.Closed.RelayURL != "" || msg.Closed.SubscriptionID != "" || msg.Closed.Reason != "":
			if sbomAvailabilityAuthRequiredReason(msg.Closed.Reason) && msg.Closed.RelayURL != "" {
				_ = s.Subscriber.AuthenticateRelay(ctx, msg.Closed.RelayURL)
			}
			return nil, fmt.Errorf("relay closed SBOM availability subscription before EOSE: relay=%s subscription=%s reason=%s", msg.Closed.RelayURL, msg.Closed.SubscriptionID, msg.Closed.Reason)
		case msg.RelayEOSE.RelayURL != "" || msg.RelayEOSE.SubscriptionID != "":
			continue
		}
	}
}

func (s *SBOMOrchestrator) availabilityListFilter(subject domain.SBOMSubject) (nostr.Filter, error) {
	dTag, err := sbomadapter.AvailabilityListDTag(subject)
	if err != nil {
		return nostr.Filter{}, err
	}
	pubkey, err := nostr.PubKeyFromHex(s.Pubkey)
	if err != nil {
		return nostr.Filter{}, fmt.Errorf("parse SBOM availability publisher pubkey: %w", err)
	}
	return nostr.Filter{
		Kinds:   []nostr.Kind{nostr.Kind(sbomadapter.KindSBOMAvailabilityList)},
		Authors: []nostr.PubKey{pubkey},
		Tags: nostr.TagMap{
			"d":            []string{dTag},
			"domain":       []string{"sbom"},
			"schema":       []string{"bahia.sbom.available-list.v1"},
			"subject_type": []string{string(subject.Type)},
			"subject":      []string{subject.Digest},
		},
		Limit: 20,
	}, nil
}

func (s *SBOMOrchestrator) entriesFromRelayAvailabilityEvent(subject domain.SBOMSubject, ev *nostr.Event) ([]domain.SBOMIndexEntry, error) {
	if err := validateSBOMAvailabilityInboundEvent(subject, ev, time.Now().UTC(), 10*time.Minute); err != nil {
		return nil, err
	}
	idx, err := sbomadapter.ParseIndexFromEvent(ev)
	if err != nil {
		return nil, err
	}
	entries := append([]domain.SBOMIndexEntry(nil), idx.Entries...)
	for i := range entries {
		if entries[i].ReferencePubkey == "" {
			entries[i].ReferencePubkey = nostrutil.EventPubKeyHex(ev)
		}
	}
	entries = append(entries, sbomTagEntries(subject, ev)...)
	for _, entry := range entries {
		if err := validateAvailabilityEntry(subject, entry); err != nil {
			return nil, err
		}
	}
	return entries, nil
}

func (s *SBOMOrchestrator) processOne(ctx context.Context, statusD string, item sbomadapter.GenerateResult, sourceKind domain.SBOMSourceKind) (*domain.SBOMManifest, []domain.SBOMManifestPackage, domain.SBOMIndexEntry, string, error) {
	if err := s.publishStatus(ctx, statusD, item.Subject, "running", "parsing", ""); err != nil {
		return nil, nil, domain.SBOMIndexEntry{}, "", err
	}
	parsed, err := sbomadapter.ParseManifest(item.Payload, item.Subject)
	if err != nil {
		return nil, nil, domain.SBOMIndexEntry{}, "", err
	}
	if err := s.publishStatus(ctx, statusD, item.Subject, "running", "uploading_to_blossom", ""); err != nil {
		return nil, nil, domain.SBOMIndexEntry{}, "", err
	}
	stored, err := s.Storage.Store(ctx, sbomadapter.StoreInput{Data: item.Payload, Format: parsed.Manifest.Format, BackendType: domain.SBOMStorageBlossom})
	if err != nil {
		return nil, nil, domain.SBOMIndexEntry{}, "", err
	}
	payloadSHA := sha256Hex(item.Payload)
	if !strings.EqualFold(stored.Hash, payloadSHA) {
		return nil, nil, domain.SBOMIndexEntry{}, "", fmt.Errorf("Blossom payload hash %s does not match local payload SHA-256 %s", stored.Hash, payloadSHA)
	}
	packages := manifestPackagesToLegacy(parsed.Packages)
	att, err := sbomadapter.NewAttestationBuilder(item.Generator.ID, item.Generator.Version, item.Generator.Pubkey).BuildAttestation(sbomadapter.BuildAttestationInput{Subject: &item.Subject, SBOMData: item.Payload, Format: parsed.Manifest.Format, Location: stored.Location, Generator: &item.Generator, ParsedPackages: packages})
	if err != nil {
		return nil, nil, domain.SBOMIndexEntry{}, "", err
	}
	if !sbomadapter.VerifySBOMSubjectDigest(att, item.Subject) || !sbomadapter.VerifyPayloadDigest(att, item.Payload) {
		return nil, nil, domain.SBOMIndexEntry{}, "", fmt.Errorf("SBOM attestation verification failed")
	}
	if err := s.publishStatus(ctx, statusD, item.Subject, "running", "publishing_reference", ""); err != nil {
		return nil, nil, domain.SBOMIndexEntry{}, "", err
	}
	refEvent, refD, err := sbomadapter.BuildSBOMReferenceEvent(sbomadapter.BuildSBOMReferenceEventInput{Subject: item.Subject, Attestation: att})
	if err != nil {
		return nil, nil, domain.SBOMIndexEntry{}, "", err
	}
	refID, err := s.publishVerified(ctx, refEvent, "SBOM reference")
	if err != nil {
		return nil, nil, domain.SBOMIndexEntry{}, "", err
	}
	parsed.Manifest.StorageType = domain.SBOMStorageBlossom
	parsed.Manifest.StorageURI = stored.Location.URI
	parsed.Manifest.PayloadSHA256 = payloadSHA
	parsed.Manifest.Generator = item.Generator
	parsed.Manifest.NTIA = att.Predicate.NTIA
	parsed.Manifest.NTIAStatus = ntiaStatus(att.Predicate.NTIA)
	parsed.Manifest.ReferenceEventID = refID
	parsed.Manifest.ReferenceDTag = refD
	parsed.Manifest.PublishState = domain.SBOMPublishPublished
	parsed.Manifest.SourceKind = sourceKind
	now := time.Now().UTC()
	parsed.Manifest.CreatedAt = now
	parsed.Manifest.UpdatedAt = now
	parsed.Manifest.PublishedAt = &now
	entry := domain.SBOMIndexEntry{SubjectDigest: item.Subject.Digest, AttestationID: fmt.Sprintf("%d:%s:%s", sbomadapter.KindSBOMReference, s.Pubkey, refD), ReferenceDTag: refD, Format: parsed.Manifest.Format, LocationURI: stored.Location.URI, StorageType: domain.SBOMStorageBlossom, PayloadSHA256: payloadSHA, GeneratorID: item.Generator.ID, Timestamp: now}
	return &parsed.Manifest, parsed.Packages, entry, refID, nil
}

func (s *SBOMOrchestrator) publishStatus(ctx context.Context, d string, subject domain.SBOMSubject, status, step, message string) error {
	content, _ := json.Marshal(map[string]any{"status": status, "step": step, "subject": subject, "message": message, "updated_at": time.Now().UTC()})
	ev := &nostr.Event{Kind: KindSBOMStatus, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"d", d}, {"domain", "sbom"}, {"status", status}, {"step", step}, {"subject_type", string(subject.Type)}, {"subject", subject.Digest}}, Content: string(content)}
	_, err := s.publishVerified(ctx, ev, "SBOM status")
	return err
}

func (s *SBOMOrchestrator) publishAudit(ctx context.Context, subject domain.SBOMSubject, action, message string) error {
	content, _ := json.Marshal(map[string]any{"action": action, "subject": subject, "message": message, "created_at": time.Now().UTC()})
	ev := &nostr.Event{Kind: KindSBOMAudit, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"domain", "sbom"}, {"action", action}, {"subject_type", string(subject.Type)}, {"subject", subject.Digest}}, Content: string(content)}
	_, err := s.publishVerified(ctx, ev, "SBOM audit")
	return err
}

func (s *SBOMOrchestrator) publishVerified(ctx context.Context, ev *nostr.Event, label string) (string, error) {
	results, err := s.Publisher.PublishSignedEventWithResults(ctx, ev)
	if err != nil {
		return "", fmt.Errorf("publishing %s event: %w", label, err)
	}
	if len(results) == 0 {
		return "", fmt.Errorf("publishing %s event: no relay OK results", label)
	}
	var rejections []string
	for _, result := range results {
		if result.Accepted {
			if s.Pubkey != "" && !strings.EqualFold(ev.PubKey.Hex(), s.Pubkey) {
				return "", fmt.Errorf("publishing %s event: signed pubkey %s does not match configured publisher pubkey %s", label, ev.PubKey.Hex(), s.Pubkey)
			}
			return nostrutil.EventIDHex(ev), nil
		}
		relay := result.RelayURL
		if relay == "" {
			relay = "unknown relay"
		}
		if result.Reason != "" {
			rejections = append(rejections, relay+" rejected event: "+result.Reason)
		} else if result.Error != nil {
			rejections = append(rejections, fmt.Sprintf("%s publish error: %v", relay, result.Error))
		} else {
			rejections = append(rejections, relay+" returned OK accepted=false without reason")
		}
	}
	return "", fmt.Errorf("publishing %s event: no relay accepted event: %s", label, strings.Join(rejections, "; "))
}

func (s *SBOMOrchestrator) validateRuntimeConfigured() error {
	if s == nil || s.Storage == nil || s.Repo == nil || s.Publisher == nil || s.Subscriber == nil || s.Pubkey == "" {
		return fmt.Errorf("SBOM orchestrator is not fully configured")
	}
	return nil
}

func (s *SBOMOrchestrator) cached(key string) (SBOMRunResult, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.results[key]
	return r, ok
}
func (s *SBOMOrchestrator) remember(key string, r SBOMRunResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.results[key] = r
}
func (s *SBOMOrchestrator) lockFor(subject domain.SBOMSubject) *sync.Mutex {
	return s.lockForKey("subject", string(subject.Type)+"\x00"+subject.ID+"\x00"+subject.Digest)
}
func (s *SBOMOrchestrator) lockForKey(namespace, key string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := namespace + "\x00" + key
	if s.locks[k] == nil {
		s.locks[k] = &sync.Mutex{}
	}
	return s.locks[k]
}

func manifestEntry(m domain.SBOMManifest, publisherPubkey string) domain.SBOMIndexEntry {
	return domain.SBOMIndexEntry{SubjectDigest: m.Subject.Digest, AttestationID: fmt.Sprintf("%d:%s:%s", sbomadapter.KindSBOMReference, publisherPubkey, m.ReferenceDTag), ReferenceDTag: m.ReferenceDTag, ReferencePubkey: publisherPubkey, Format: m.Format, LocationURI: m.StorageURI, StorageType: m.StorageType, PayloadSHA256: m.PayloadSHA256, GeneratorID: m.Generator.ID, Timestamp: m.CreatedAt}
}

func validateSBOMAvailabilityInboundEvent(subject domain.SBOMSubject, ev *nostr.Event, now time.Time, maxFutureSkew time.Duration) error {
	if ev == nil {
		return fmt.Errorf("nil event")
	}
	if int(ev.Kind) != sbomadapter.KindSBOMAvailabilityList {
		return fmt.Errorf("unexpected event kind %d", ev.Kind)
	}
	if !ev.CheckID() {
		return fmt.Errorf("event id does not match serialized event")
	}
	if !ev.VerifySignature() {
		return fmt.Errorf("invalid signature")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if maxFutureSkew <= 0 {
		maxFutureSkew = 10 * time.Minute
	}
	createdAt := time.Unix(int64(ev.CreatedAt), 0).UTC()
	if createdAt.After(now.Add(maxFutureSkew)) {
		return fmt.Errorf("event timestamp %s exceeds future skew", createdAt.Format(time.RFC3339))
	}
	dTag, err := sbomadapter.AvailabilityListDTag(subject)
	if err != nil {
		return err
	}
	if got := sbomAvailabilityTagValue(ev, "d"); got != dTag {
		return fmt.Errorf("d tag %q does not match subject availability list %q", got, dTag)
	}
	if got := sbomAvailabilityTagValue(ev, "domain"); got != "sbom" {
		return fmt.Errorf("domain tag %q is not sbom", got)
	}
	if got := sbomAvailabilityTagValue(ev, "schema"); got != "bahia.sbom.available-list.v1" {
		return fmt.Errorf("schema tag %q is not bahia.sbom.available-list.v1", got)
	}
	if got := sbomAvailabilityTagValue(ev, "subject_type"); got != string(subject.Type) {
		return fmt.Errorf("subject_type tag %q does not match %q", got, subject.Type)
	}
	if got := sbomAvailabilityTagValue(ev, "subject"); !strings.EqualFold(got, subject.Digest) {
		return fmt.Errorf("subject tag %q does not match %q", got, subject.Digest)
	}
	return nil
}

func sbomTagEntries(subject domain.SBOMSubject, ev *nostr.Event) []domain.SBOMIndexEntry {
	aPubkeysByRefD := map[string]string{}
	for _, tag := range ev.Tags {
		if len(tag) < 2 || tag[0] != "a" {
			continue
		}
		parts := strings.SplitN(tag[1], ":", 3)
		if len(parts) == 3 && parts[0] == fmt.Sprintf("%d", sbomadapter.KindSBOMReference) {
			aPubkeysByRefD[parts[2]] = parts[1]
		}
	}
	entries := make([]domain.SBOMIndexEntry, 0)
	for _, tag := range ev.Tags {
		if len(tag) < 8 || tag[0] != sbomadapter.TagSBOMRef {
			continue
		}
		if !strings.EqualFold(tag[1], subject.Digest) {
			continue
		}
		refD := tag[7]
		referencePubkey := aPubkeysByRefD[refD]
		if referencePubkey == "" {
			referencePubkey = nostrutil.EventPubKeyHex(ev)
		}
		entries = append(entries, domain.SBOMIndexEntry{
			SubjectDigest:   tag[1],
			AttestationID:   fmt.Sprintf("%d:%s:%s", sbomadapter.KindSBOMReference, referencePubkey, refD),
			ReferenceDTag:   refD,
			ReferencePubkey: referencePubkey,
			Format:          domain.SBOMFormat(tag[2]),
			StorageType:     domain.SBOMStorageType(tag[3]),
			LocationURI:     tag[4],
			PayloadSHA256:   tag[5],
			GeneratorID:     tag[6],
			Timestamp:       time.Unix(int64(ev.CreatedAt), 0).UTC(),
		})
	}
	return entries
}

func validateAvailabilityEntry(subject domain.SBOMSubject, entry domain.SBOMIndexEntry) error {
	if !strings.EqualFold(entry.SubjectDigest, subject.Digest) {
		return fmt.Errorf("availability entry subject digest %q does not match %q", entry.SubjectDigest, subject.Digest)
	}
	if entry.ReferenceDTag == "" {
		return fmt.Errorf("availability entry reference d tag is required")
	}
	if entry.Format != domain.SBOMFormatSPDX && entry.Format != domain.SBOMFormatCycloneDX {
		return fmt.Errorf("unsupported availability entry format %q", entry.Format)
	}
	if entry.StorageType != domain.SBOMStorageBlossom {
		return fmt.Errorf("availability entry storage %q is not blossom", entry.StorageType)
	}
	if entry.LocationURI == "" {
		return fmt.Errorf("availability entry location is required")
	}
	if len(entry.PayloadSHA256) != 64 {
		return fmt.Errorf("availability entry payload SHA-256 must be 64 hex characters")
	}
	if _, err := hex.DecodeString(entry.PayloadSHA256); err != nil {
		return fmt.Errorf("availability entry payload SHA-256 must be hex: %w", err)
	}
	if entry.GeneratorID == "" {
		return fmt.Errorf("availability entry generator ID is required")
	}
	return nil
}

func sbomAvailabilityTagValue(ev *nostr.Event, key string) string {
	if ev == nil {
		return ""
	}
	for _, tag := range ev.Tags {
		if len(tag) >= 2 && tag[0] == key {
			return tag[1]
		}
	}
	return ""
}

func sbomAvailabilityAuthRequiredReason(reason string) bool {
	reason = strings.ToLower(strings.TrimSpace(reason))
	return strings.Contains(reason, "auth-required") || strings.Contains(reason, "auth required") || strings.Contains(reason, "authentication required")
}

func manifestPackagesToLegacy(pkgs []domain.SBOMManifestPackage) []domain.SBOMPackage {
	out := make([]domain.SBOMPackage, len(pkgs))
	for i, p := range pkgs {
		out[i] = domain.SBOMPackage{Name: p.Name, Version: p.Version, Ecosystem: p.Ecosystem, License: p.License, PURL: p.PURL, CPE: p.CPE}
	}
	return out
}
func sha256Hex(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func SBOMStatusDTag(idempotencyKey string) (string, error) {
	key := strings.TrimSpace(idempotencyKey)
	if key == "" {
		return "", fmt.Errorf("idempotencyKey is required")
	}
	return "sbom:run:" + sanitizeDTag(key), nil
}

func sanitizeDTag(value string) string {
	return strings.NewReplacer(" ", "-", "/", "-", "#", "-", "?", "-").Replace(value)
}
func ntiaStatus(ntia *domain.NTIACompliance) string {
	if ntia == nil {
		return "unknown"
	}
	if ntia.IsCompliant {
		return "compliant"
	}
	return "partial"
}
func auditAction(kind domain.SBOMSourceKind) string {
	if kind == domain.SBOMSourceImported {
		return "sbom.imported"
	}
	return "sbom.generated"
}
