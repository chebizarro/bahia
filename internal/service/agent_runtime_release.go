package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
)

var (
	ErrRuntimeReleaseConflict = errors.New("runtime release digest conflicts with existing provenance")
	runtimeReleaseDigest      = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// AgentRuntimeReleaseService is the Wave B interface for registering one
// verified runtime digest once, binding it to multiple agents, and resolving
// the exact prior binding for rollback.
type AgentRuntimeReleaseService struct {
	releases repository.AgentRuntimeReleaseRepository
	services repository.ServiceRepository
}

func NewAgentRuntimeReleaseService(releases repository.AgentRuntimeReleaseRepository, services repository.ServiceRepository) *AgentRuntimeReleaseService {
	return &AgentRuntimeReleaseService{releases: releases, services: services}
}

func (s *AgentRuntimeReleaseService) RegisterSource(ctx context.Context, source *domain.AgentRuntimeSource) error {
	if s == nil || s.releases == nil {
		return fmt.Errorf("agent runtime release repository is not configured")
	}
	if source == nil || source.OrgID == uuid.Nil || strings.TrimSpace(source.Repository) == "" || strings.TrimSpace(source.Branch) == "" || strings.TrimSpace(source.ReleaseChannel) == "" {
		return fmt.Errorf("runtime source requires tenant, repository, branch, and release channel")
	}
	source.Repository = strings.TrimSpace(source.Repository)
	source.Branch = strings.TrimSpace(source.Branch)
	source.ReleaseChannel = strings.TrimSpace(source.ReleaseChannel)
	return s.releases.CreateSource(ctx, source)
}

func (s *AgentRuntimeReleaseService) RegisterVerifiedRelease(ctx context.Context, release *domain.AgentRuntimeRelease) error {
	if s == nil || s.releases == nil {
		return fmt.Errorf("agent runtime release repository is not configured")
	}
	if err := validateAgentRuntimeRelease(release); err != nil {
		return err
	}
	source, err := s.releases.GetSource(ctx, release.OrgID, release.SourceID)
	if err != nil {
		return err
	}
	if source == nil {
		return fmt.Errorf("runtime source is not in release tenant")
	}
	existing, err := s.releases.GetReleaseByDigest(ctx, release.OrgID, release.ImageRepo, release.ImageDigest)
	if err != nil {
		return err
	}
	if existing != nil {
		if existing.SourceID != release.SourceID || !existing.VerifiedAt.Equal(release.VerifiedAt) || !reflect.DeepEqual(existing.Provenance, release.Provenance) {
			return fmt.Errorf("%w: %s", ErrRuntimeReleaseConflict, release.ImageDigest)
		}
		*release = *existing
		return nil
	}
	return s.releases.CreateRelease(ctx, release)
}

func validateAgentRuntimeRelease(release *domain.AgentRuntimeRelease) error {
	if release == nil || release.OrgID == uuid.Nil || release.SourceID == uuid.Nil || strings.TrimSpace(release.ImageRepo) == "" || release.VerifiedAt.IsZero() {
		return fmt.Errorf("verified runtime release requires tenant, source, image repository, and verification time")
	}
	p := release.Provenance
	for label, value := range map[string]string{
		"image digest": release.ImageDigest, "manifest digest": p.ManifestDigest,
		"SBOM digest": p.SBOMDigest, "provenance digest": p.ProvenanceDigest,
	} {
		if !runtimeReleaseDigest.MatchString(value) {
			return fmt.Errorf("invalid %s", label)
		}
	}
	if p.ManifestDigest != release.ImageDigest || strings.TrimSpace(p.Provider) == "" || strings.TrimSpace(p.ReleaseEventID) == "" || strings.TrimSpace(p.WorkflowRunEventID) == "" || strings.TrimSpace(p.AttestorPubkey) == "" {
		return fmt.Errorf("complete immutable runtime release provenance is required")
	}
	return nil
}

func (s *AgentRuntimeReleaseService) BindRelease(ctx context.Context, binding *domain.AgentServiceReleaseBinding) error {
	if s == nil || s.releases == nil || s.services == nil {
		return fmt.Errorf("agent runtime release binding is not configured")
	}
	if binding == nil || binding.OrgID == uuid.Nil || binding.ServiceID == uuid.Nil || binding.ReleaseID == uuid.Nil || strings.TrimSpace(binding.AgentID) == "" || strings.TrimSpace(binding.ReleaseChannel) == "" || strings.TrimSpace(binding.SourceEventID) == "" {
		return fmt.Errorf("binding requires tenant, agent, service, release, channel, and source event")
	}
	svc, err := s.services.GetByID(ctx, binding.ServiceID)
	if err != nil {
		return err
	}
	if svc == nil || svc.OrgID != binding.OrgID {
		return fmt.Errorf("service is not in binding tenant")
	}
	release, err := s.releases.GetRelease(ctx, binding.OrgID, binding.ReleaseID)
	if err != nil {
		return err
	}
	if release == nil {
		return fmt.Errorf("runtime release is not in binding tenant")
	}
	source, err := s.releases.GetSource(ctx, binding.OrgID, release.SourceID)
	if err != nil {
		return err
	}
	if source == nil || source.ReleaseChannel != strings.TrimSpace(binding.ReleaseChannel) {
		return fmt.Errorf("binding release channel does not match runtime source")
	}
	binding.AgentID = strings.TrimSpace(binding.AgentID)
	binding.ReleaseChannel = strings.TrimSpace(binding.ReleaseChannel)
	binding.SourceEventID = strings.TrimSpace(binding.SourceEventID)
	return s.releases.BindRelease(ctx, binding)
}

func (s *AgentRuntimeReleaseService) ListServiceReleases(ctx context.Context, orgID, serviceID uuid.UUID) ([]domain.AgentServiceRuntimeRelease, error) {
	if s == nil || s.releases == nil {
		return nil, fmt.Errorf("agent runtime release repository is not configured")
	}
	return s.releases.ListServiceReleases(ctx, orgID, serviceID)
}

func (s *AgentRuntimeReleaseService) GetRollbackRelease(ctx context.Context, orgID uuid.UUID, agentID string, serviceID uuid.UUID, channel string) (*domain.AgentServiceRuntimeRelease, error) {
	if s == nil || s.releases == nil {
		return nil, fmt.Errorf("agent runtime release repository is not configured")
	}
	if orgID == uuid.Nil || serviceID == uuid.Nil || strings.TrimSpace(agentID) == "" || strings.TrimSpace(channel) == "" {
		return nil, fmt.Errorf("rollback lookup requires tenant, agent, service, and channel")
	}
	return s.releases.GetRollbackRelease(ctx, orgID, strings.TrimSpace(agentID), serviceID, strings.TrimSpace(channel))
}
