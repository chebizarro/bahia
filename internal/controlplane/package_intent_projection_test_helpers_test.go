package controlplane

import (
	"context"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

type memoryPackageProjection struct {
	reposByID              map[uuid.UUID]*domain.PackageRepository
	reposByName            map[string]*domain.PackageRepository
	artifacts              map[string]*domain.PackageArtifact
	publications           map[uuid.UUID]*domain.PackagePublication
	intentsByID            map[uuid.UUID]*domain.PackageIntent
	intentsByReq           map[string]*domain.PackageIntent
	getRepositoryByNameErr error
	getIntentErr           error
	upsertIntentErrStatus  domain.PackageIntentStatus
	upsertIntentErr        error
}

func newMemoryPackageProjection() *memoryPackageProjection {
	return &memoryPackageProjection{reposByID: map[uuid.UUID]*domain.PackageRepository{}, reposByName: map[string]*domain.PackageRepository{}, artifacts: map[string]*domain.PackageArtifact{}, publications: map[uuid.UUID]*domain.PackagePublication{}, intentsByID: map[uuid.UUID]*domain.PackageIntent{}, intentsByReq: map[string]*domain.PackageIntent{}}
}

func (m *memoryPackageProjection) UpsertRepository(_ context.Context, repo *domain.PackageRepository) error {
	cp := *repo
	m.reposByID[cp.ID] = &cp
	m.reposByName[cp.Name] = &cp
	return nil
}
func (m *memoryPackageProjection) GetRepository(_ context.Context, id uuid.UUID) (*domain.PackageRepository, error) {
	return m.reposByID[id], nil
}
func (m *memoryPackageProjection) GetRepositoryByName(_ context.Context, name string) (*domain.PackageRepository, error) {
	if m.getRepositoryByNameErr != nil {
		return nil, m.getRepositoryByNameErr
	}
	return m.reposByName[name], nil
}
func (m *memoryPackageProjection) ListRepositories(_ context.Context, includeDeleted bool) ([]domain.PackageRepository, error) {
	out := []domain.PackageRepository{}
	for _, repo := range m.reposByID {
		if includeDeleted || !repo.Deleted {
			out = append(out, *repo)
		}
	}
	return out, nil
}
func (m *memoryPackageProjection) UpsertArtifact(_ context.Context, artifact *domain.PackageArtifact) error {
	cp := *artifact
	m.artifacts[artifactKey(cp.RepositoryID, cp.Namespace, cp.PackageName, cp.Version, cp.Filename)] = &cp
	return nil
}
func (m *memoryPackageProjection) GetArtifact(_ context.Context, repositoryID uuid.UUID, namespace, packageName, version, filename string) (*domain.PackageArtifact, error) {
	return m.artifacts[artifactKey(repositoryID, namespace, packageName, version, filename)], nil
}
func (m *memoryPackageProjection) ListArtifacts(_ context.Context, repositoryID uuid.UUID, _, _ int) ([]domain.PackageArtifact, error) {
	out := []domain.PackageArtifact{}
	for _, artifact := range m.artifacts {
		if artifact.RepositoryID == repositoryID && !artifact.Deleted {
			out = append(out, *artifact)
		}
	}
	return out, nil
}
func (m *memoryPackageProjection) UpsertPublication(_ context.Context, publication *domain.PackagePublication) error {
	cp := *publication
	m.publications[cp.ID] = &cp
	return nil
}
func (m *memoryPackageProjection) GetPublication(_ context.Context, id uuid.UUID) (*domain.PackagePublication, error) {
	return m.publications[id], nil
}
func (m *memoryPackageProjection) ListPublicationsByArtifact(_ context.Context, artifactID uuid.UUID) ([]domain.PackagePublication, error) {
	out := []domain.PackagePublication{}
	for _, p := range m.publications {
		if p.ArtifactID == artifactID {
			out = append(out, *p)
		}
	}
	return out, nil
}
func (m *memoryPackageProjection) ListPublicationsByRepository(_ context.Context, repositoryID uuid.UUID, _ bool) ([]domain.PackagePublication, error) {
	out := []domain.PackagePublication{}
	for _, p := range m.publications {
		if p.RepositoryID == repositoryID {
			out = append(out, *p)
		}
	}
	return out, nil
}
func (m *memoryPackageProjection) UpsertIntent(_ context.Context, intent *domain.PackageIntent) error {
	if intent.Status == m.upsertIntentErrStatus && m.upsertIntentErr != nil {
		return m.upsertIntentErr
	}
	cp := *intent
	m.intentsByID[cp.ID] = &cp
	m.intentsByReq[cp.RequestEventID] = &cp
	return nil
}
func (m *memoryPackageProjection) GetIntent(_ context.Context, id uuid.UUID) (*domain.PackageIntent, error) {
	return m.intentsByID[id], nil
}
func (m *memoryPackageProjection) GetIntentByRequestEventID(_ context.Context, requestEventID string) (*domain.PackageIntent, error) {
	if m.getIntentErr != nil {
		return nil, m.getIntentErr
	}
	return m.intentsByReq[requestEventID], nil
}
func (m *memoryPackageProjection) ListNonTerminalIntents(_ context.Context, _ int) ([]domain.PackageIntent, error) {
	return nil, nil
}

func artifactKey(repoID uuid.UUID, namespace, packageName, version, filename string) string {
	return repoID.String() + ":" + namespace + ":" + packageName + ":" + version + ":" + filename
}
