package controlplane

import (
	"context"
	"strconv"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/backends/filesystem_mock"
	"github.com/openagentsinc/bahia/internal/backends/packagebackend"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

func TestPackageRepositoryApplyPublishesLifecycleAndProjection(t *testing.T) {
	ctx := context.Background()
	privateKey := nostr.Generate().Hex()
	signer, err := NewPrivateKeySigner(privateKey)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	pubkey := testNostrPubKeyHexFromPrivateKey(t, privateKey)
	pubkeyValue := testNostrPubKeyFromPrivateKey(t, privateKey)
	capture := &captureNostrPublisher{published: 1}
	projection := newMemoryPackageProjection()
	backend, err := filesystem_mock.New(filesystem_mock.Config{RootDir: t.TempDir()})
	if err != nil {
		t.Fatalf("backend: %v", err)
	}
	pkgSvc, err := service.NewPackageRegistryService(config.PackageControlplaneConfig{}, packagebackend.Registry{"mock": backend}, projection, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	reactor := NewReactor(Config{AuthorizedPubkeys: []string{pubkey}}, nil, nil, signer, zap.NewNop(), WithControlPlanePublisher(capture), WithPackageRegistryService(pkgSvc), WithPackageProjectionRepository(projection))
	content := mustJSON(PackageRepositoryApplyCommand{Name: "libs", Format: domain.PackageRepositoryFormatNPM, BackendRef: "mock", BackendType: domain.PackageBackendFilesystemMock, ExternalRepositoryName: "libs"})
	event := &nostr.Event{ID: testNostrID("package-repo-apply"), PubKey: pubkeyValue, Kind: KindPackageRepositoryApply, Content: content}
	reactor.handlePackageRepositoryApply(ctx, event)

	if len(capture.events) < 5 {
		t.Fatalf("expected lifecycle/status/registry/result events, got %d", len(capture.events))
	}
	assertPublishedKind(t, capture.events, KindPackageStatus)
	assertPublishedKind(t, capture.events, KindPackageRepositoryRegistry)
	assertPublishedKind(t, capture.events, KindPackageResult)
	repo, err := projection.GetRepositoryByName(ctx, "libs")
	if err != nil || repo == nil {
		t.Fatalf("expected repository projection, repo=%#v err=%v", repo, err)
	}
	if repo.Status != domain.PackageRepositoryStatusReady || repo.LastEventID == "" {
		t.Fatalf("unexpected repo projection: %#v", repo)
	}
	intent, _ := projection.GetIntentByRequestEventID(ctx, event.ID.Hex())
	if intent == nil || intent.Status != domain.PackageIntentStatusSucceeded {
		t.Fatalf("expected succeeded intent, got %#v", intent)
	}
}

func TestPackageRepositoryApplyDuplicateTerminalReplaysResultOnly(t *testing.T) {
	ctx := context.Background()
	privateKey := nostr.Generate().Hex()
	signer, _ := NewPrivateKeySigner(privateKey)
	pubkey := testNostrPubKeyHexFromPrivateKey(t, privateKey)
	pubkeyValue := testNostrPubKeyFromPrivateKey(t, privateKey)
	capture := &captureNostrPublisher{published: 1}
	projection := newMemoryPackageProjection()
	requestID := testNostrID("duplicate")
	intent := &domain.PackageIntent{ID: uuid.New(), RequestEventID: requestID.Hex(), Operation: domain.PackageOperationRepositoryApply, RequesterPubkey: pubkey, Status: domain.PackageIntentStatusSucceeded, ResultPayload: map[string]any{"status": "succeeded"}}
	_ = projection.UpsertIntent(ctx, intent)
	backend, _ := filesystem_mock.New(filesystem_mock.Config{RootDir: t.TempDir()})
	pkgSvc, _ := service.NewPackageRegistryService(config.PackageControlplaneConfig{}, packagebackend.Registry{"mock": backend}, projection, nil, zap.NewNop())
	reactor := NewReactor(Config{AuthorizedPubkeys: []string{pubkey}}, nil, nil, signer, zap.NewNop(), WithControlPlanePublisher(capture), WithPackageRegistryService(pkgSvc), WithPackageProjectionRepository(projection))

	reactor.handlePackageRepositoryApply(ctx, &nostr.Event{ID: requestID, PubKey: pubkeyValue, Kind: KindPackageRepositoryApply, Content: mustJSON(PackageRepositoryApplyCommand{Name: "libs", Format: domain.PackageRepositoryFormatNPM, BackendRef: "mock"})})
	if len(capture.events) != 1 {
		t.Fatalf("expected one replayed package result, got %#v", capture.events)
	}
	assertPublishedKind(t, capture.events, KindPackageResult)
}

func assertPublishedKind(t *testing.T, events []nostr.Event, kind int) {
	t.Helper()
	legacyKind := strconv.Itoa(kind)
	if isLegacyRuntimeObservableKind(kind) {
		for _, ev := range events {
			if ev.Kind == nostr.Kind(kind) {
				t.Fatalf("legacy runtime kind %d was published directly; events=%#v", kind, events)
			}
		}
		for _, ev := range events {
			if tagValueNostr(ev.Tags, "legacy_kind") == legacyKind {
				if !ev.VerifySignature() {
					t.Fatalf("canonical event for legacy kind %d signature invalid", kind)
				}
				return
			}
		}
		t.Fatalf("canonical event carrying legacy_kind %d not published; events=%#v", kind, events)
	}
	for _, ev := range events {
		if ev.Kind == nostr.Kind(kind) {
			if !ev.VerifySignature() {
				t.Fatalf("kind %d signature invalid", kind)
			}
			return
		}
	}
	t.Fatalf("kind %d not published; events=%#v", kind, events)
}

func isLegacyRuntimeObservableKind(kind int) bool {
	switch kind {
	case KindPackageStatus, KindPackageResult, KindPackageDriftEvent, KindPackageRepositoryRegistry, KindPackageArtifactRegistry, KindPackagePromotionRegistry, KindWorkerStatus, KindWorkerResult:
		return true
	default:
		return false
	}
}

type memoryPackageProjection struct {
	reposByID    map[uuid.UUID]*domain.PackageRepository
	reposByName  map[string]*domain.PackageRepository
	artifacts    map[string]*domain.PackageArtifact
	publications map[uuid.UUID]*domain.PackagePublication
	intentsByID  map[uuid.UUID]*domain.PackageIntent
	intentsByReq map[string]*domain.PackageIntent
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
	cp := *intent
	m.intentsByID[cp.ID] = &cp
	m.intentsByReq[cp.RequestEventID] = &cp
	return nil
}
func (m *memoryPackageProjection) GetIntent(_ context.Context, id uuid.UUID) (*domain.PackageIntent, error) {
	return m.intentsByID[id], nil
}
func (m *memoryPackageProjection) GetIntentByRequestEventID(_ context.Context, requestEventID string) (*domain.PackageIntent, error) {
	return m.intentsByReq[requestEventID], nil
}
func (m *memoryPackageProjection) ListNonTerminalIntents(_ context.Context, _ int) ([]domain.PackageIntent, error) {
	return nil, nil
}

func artifactKey(repoID uuid.UUID, namespace, packageName, version, filename string) string {
	return repoID.String() + ":" + namespace + ":" + packageName + ":" + version + ":" + filename
}
