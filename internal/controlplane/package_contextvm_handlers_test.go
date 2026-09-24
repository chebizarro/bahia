package controlplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/backends/filesystem_mock"
	"github.com/openagentsinc/bahia/internal/backends/packagebackend"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type packageWireStore struct {
	*memoryPackageProjection
	claims    map[string]repository.PackageRequestClaim
	approvals map[uuid.UUID]repository.PackageApproval
	consumed  map[uuid.UUID]bool
	calls     int
	fail      string
}

func (s *packageWireStore) ClaimPackageRequest(_ context.Context, c repository.PackageRequestClaim) (*repository.PackageRequestClaim, bool, error) {
	s.calls++
	if s.fail == "claim" {
		return nil, false, errors.New("claim unavailable")
	}
	k := c.Requester + c.Method + c.Token
	if old, ok := s.claims[k]; ok {
		if old.Fingerprint != c.Fingerprint {
			return nil, false, errors.New("fingerprint conflict")
		}
		return &old, false, nil
	}
	s.claims[k] = c
	return &c, true, nil
}
func (s *packageWireStore) CompletePackageRequest(_ context.Context, id string) error {
	if s.fail == "confirmation" {
		return errors.New("confirmation unavailable")
	}
	for key, c := range s.claims {
		if c.EventID == id {
			c.Completed = true
			s.claims[key] = c
		}
	}
	return nil
}
func (s *packageWireStore) CreatePackageApproval(_ context.Context, a repository.PackageApproval) error {
	s.calls++
	s.approvals[a.ID] = a
	return nil
}
func (s *packageWireStore) ConsumePackageApproval(_ context.Context, id uuid.UUID, requester, method, hash string, allowed []string) (string, error) {
	s.calls++
	a, ok := s.approvals[id]
	if !ok || s.consumed[id] || a.Requester != requester || a.Method != method || a.PlanHash != hash || a.Requester == a.Approver || !slices.Contains(allowed, a.Approver) || time.Now().Before(a.CreatedAt) || !time.Now().Before(a.ExpiresAt) {
		return "", repository.ErrPackageApprovalInvalid
	}
	s.consumed[id] = true
	return a.Approver, nil
}
func (s *packageWireStore) GetRepository(ctx context.Context, id uuid.UUID) (*domain.PackageRepository, error) {
	s.calls++
	return s.memoryPackageProjection.GetRepository(ctx, id)
}
func (s *packageWireStore) GetRepositoryByName(ctx context.Context, name string) (*domain.PackageRepository, error) {
	s.calls++
	return s.memoryPackageProjection.GetRepositoryByName(ctx, name)
}
func (s *packageWireStore) GetArtifact(ctx context.Context, id uuid.UUID, namespace, name, version, filename string) (*domain.PackageArtifact, error) {
	s.calls++
	if s.fail == "lookup" {
		return nil, errors.New("artifact lookup unavailable")
	}
	return s.memoryPackageProjection.GetArtifact(ctx, id, namespace, name, version, filename)
}
func (s *packageWireStore) UpsertArtifact(ctx context.Context, a *domain.PackageArtifact) error {
	if s.fail == "artifact" {
		return errors.New("artifact projection unavailable")
	}
	return s.memoryPackageProjection.UpsertArtifact(ctx, a)
}
func (s *packageWireStore) UpsertPublication(ctx context.Context, p *domain.PackagePublication) error {
	if s.fail == "promotion" {
		return errors.New("promotion projection unavailable")
	}
	return s.memoryPackageProjection.UpsertPublication(ctx, p)
}

type packageWireBackend struct {
	packagebackend.Backend
	stores, promotions, yanks, observations int
}

func (b *packageWireBackend) StoreArtifact(ctx context.Context, repo domain.PackageRepository, req packagebackend.StoreArtifactRequest) (packagebackend.ArtifactObservation, error) {
	b.stores++
	return b.Backend.StoreArtifact(ctx, repo, req)
}
func (b *packageWireBackend) PromoteArtifact(ctx context.Context, source, target domain.PackageRepository, a domain.PackageArtifact, req packagebackend.PromoteArtifactRequest) (packagebackend.ArtifactObservation, error) {
	b.promotions++
	return b.Backend.PromoteArtifact(ctx, source, target, a, req)
}
func (b *packageWireBackend) YankArtifact(ctx context.Context, repo domain.PackageRepository, a domain.PackageArtifact, reason string) (packagebackend.ArtifactObservation, error) {
	b.yanks++
	return b.Backend.YankArtifact(ctx, repo, a, reason)
}
func (b *packageWireBackend) ObserveRepository(ctx context.Context, repo domain.PackageRepository) (packagebackend.RepositoryObservation, error) {
	b.observations++
	return b.Backend.ObserveRepository(ctx, repo)
}

type packageWirePublisher struct {
	mockEncryptedPublisher
	failSchema                string
	zero                      bool
	store                     *packageWireStore
	terminalBeforePersistence bool
}

func (p *packageWirePublisher) Publish(ctx context.Context, event nostr.Event) (int, error) {
	if tagValueNostr(event.Tags, "schema") == "bahia.result.package.v1" {
		intent, _ := p.store.GetIntentByRequestEventID(ctx, tagValueNostr(event.Tags, "e"))
		if intent == nil || !intent.Status.Terminal() {
			p.terminalBeforePersistence = true
		}
	}
	if p.failSchema != "" && tagValueNostr(event.Tags, "schema") == p.failSchema {
		if p.zero {
			return 0, nil
		}
		return 0, errors.New("relay rejected publication")
	}
	return p.mockEncryptedPublisher.Publish(ctx, event)
}

type packageWireFixture struct {
	r            *Reactor
	transport    *EncryptedRequestTransport
	publisher    *packageWirePublisher
	store        *packageWireStore
	backend      *packageWireBackend
	repo, target *domain.PackageRepository
	params       map[string]any
	gate         *FleetOperatorGate
}

func newPackageWireFixture(t *testing.T, method string, gate *FleetOperatorGate) *packageWireFixture {
	t.Helper()
	ctx := t.Context()
	store := &packageWireStore{memoryPackageProjection: newMemoryPackageProjection(), claims: map[string]repository.PackageRequestClaim{}, approvals: map[uuid.UUID]repository.PackageApproval{}, consumed: map[uuid.UUID]bool{}}
	backend, err := filesystem_mock.New(filesystem_mock.Config{RootDir: t.TempDir()})
	require.NoError(t, err)
	b := &packageWireBackend{Backend: backend}
	svc, err := service.NewPackageRegistryService(config.PackageControlplaneConfig{AllowFileSource: true}, packagebackend.Registry{"test": b}, store, nil, zap.NewNop())
	require.NoError(t, err)
	repo, err := svc.EnsureRepository(ctx, &domain.PackageRepository{Name: "source", Format: domain.PackageRepositoryFormatNPM, BackendRef: "test", ExternalRepositoryName: "source"}, nil)
	require.NoError(t, err)
	target, err := svc.EnsureRepository(ctx, &domain.PackageRepository{Name: "target", Format: domain.PackageRepositoryFormatNPM, BackendRef: "test", ExternalRepositoryName: "target"}, nil)
	require.NoError(t, err)
	require.NoError(t, store.UpsertRepository(ctx, repo))
	require.NoError(t, store.UpsertRepository(ctx, target))
	data := []byte("signed package payload")
	path := filepath.Join(t.TempDir(), "artifact.tgz")
	require.NoError(t, os.WriteFile(path, data, 0600))
	digest := sha256.Sum256(data)
	sha := hex.EncodeToString(digest[:])
	if method != "package/publish" {
		a, err := svc.PublishPackage(ctx, repo, nil, service.PackagePublishRequest{PackageName: "demo", Version: "1.0.0", Filename: "demo.tgz", SourceURL: "file://" + path, SHA256: sha, SizeBytes: int64(len(data))})
		require.NoError(t, err)
		require.NoError(t, store.UpsertArtifact(ctx, a))
	}
	b.stores = 0
	publisher := &packageWirePublisher{store: store}
	responder := newResponder(t, publisher)
	r := NewReactor(Config{}, nil, nil, responder.signer, zap.NewNop(), WithControlPlanePublisher(publisher), WithPackageRegistryService(svc), WithPackageProjectionRepository(store))
	f := &packageWireFixture{r: r, publisher: publisher, store: store, backend: b, repo: repo, target: target, gate: gate}
	f.restart(t)
	f.params = map[string]any{"idempotency_key": uuid.NewString(), "repository_id": repo.ID.String(), "package_name": "demo", "version": "1.0.0", "filename": "demo.tgz"}
	switch method {
	case "package/publish":
		f.params["source_url"], f.params["sha256"], f.params["size_bytes"] = "file://"+path, sha, len(data)
	case ContextVMMethodPackagePromote:
		f.params["source_repository_id"], f.params["target_repository_id"] = repo.ID.String(), target.ID.String()
	case "package/drift-detect":
		f.params["include_artifacts"] = true
	}
	return f
}

func (f *packageWireFixture) restart(t *testing.T) {
	t.Helper()
	responder := newResponder(t, f.publisher)
	f.transport = NewEncryptedRequestTransport(nil, responder, []string{testNostrPubKeyHexFromPrivateKey(t, testRequesterKey), testNostrPubKeyHexFromPrivateKey(t, testOtherKey)}, zap.NewNop())
	f.r.RegisterPackageContextVMHandlers(f.transport, f.gate)
}

func (f *packageWireFixture) send(t *testing.T, key, method string, params any, wrapped bool) ContextVMJSONRPCResponse {
	t.Helper()
	inner := makeRouteRequest(t, method, params)
	inner = makeContextVMEvent(t, key, inner.Content)
	event := inner
	if wrapped {
		event = wrapContextVMEvent(t, inner, KindContextVMEphemeralWrap)
	}
	f.transport.HandleEvent(t.Context(), event)
	require.NotEmpty(t, f.publisher.events)
	last := f.publisher.events[len(f.publisher.events)-1]
	if wrapped {
		return unwrapContextVMResponse(t, last, key)
	}
	return contextVMResponse(t, last)
}

func packageMethods() []string {
	return []string{"package/publish", ContextVMMethodPackagePromote, "package/yank", "package/drift-detect", packageApprovePlanMethod}
}

func TestPackageContextVMReachabilityGateAndReplay(t *testing.T) {
	for _, method := range packageMethods() {
		for _, wrapped := range []bool{false, true} {
			for _, access := range []string{"allowed", "outsider", "empty", "nil"} {
				t.Run(method+"/"+access+"/"+map[bool]string{false: "plain", true: "wrapped"}[wrapped], func(t *testing.T) {
					gate := NewFleetOperatorGate([]string{testNostrPubKeyHexFromPrivateKey(t, testRequesterKey), testNostrPubKeyHexFromPrivateKey(t, testOtherKey)})
					key := testRequesterKey
					switch access {
					case "outsider":
						gate = NewFleetOperatorGate([]string{testNostrPubKeyHexFromPrivateKey(t, testOtherKey)})
					case "empty":
						gate = NewFleetOperatorGate(nil)
					case "nil":
						gate = nil
					}
					f := newPackageWireFixture(t, method, gate)
					if method == packageApprovePlanMethod {
						f.params = map[string]any{"idempotency_key": uuid.NewString(), "requester_pubkey": testNostrPubKeyHexFromPrivateKey(t, testOtherKey), "method": ContextVMMethodPackagePromote, "params": map[string]any{"source_repository_id": f.repo.ID, "target_repository_id": f.target.ID, "package_name": "demo", "version": "1.0.0", "filename": "demo.tgz"}}
					}
					response := f.send(t, key, method, f.params, wrapped)
					require.Equal(t, `"route-test"`, string(response.ID))
					if access != "allowed" {
						require.NotNil(t, response.Error)
						want := fleetOperatorNotConfiguredError
						if access == "outsider" {
							want = fleetOperatorUnauthorizedError
						}
						require.Equal(t, want, response.Error.Message)
						require.Zero(t, f.store.calls, "gate must precede all package storage")
						return
					}
					require.Nil(t, response.Error)
					switch method {
					case "package/publish":
						require.Equal(t, 1, f.backend.stores)
					case ContextVMMethodPackagePromote:
						require.Equal(t, 1, f.backend.promotions)
					case "package/yank":
						require.Equal(t, 1, f.backend.yanks)
					case "package/drift-detect":
						require.Equal(t, 1, f.backend.observations)
					case packageApprovePlanMethod:
						require.Len(t, f.store.approvals, 1)
					}
					mutations := f.backend.stores + f.backend.promotions + f.backend.yanks + f.backend.observations
					calls := f.store.calls
					response = f.send(t, key, method, f.params, wrapped)
					require.Nil(t, response.Error)
					require.Equal(t, calls, f.store.calls)
					if method != packageApprovePlanMethod {
						f.restart(t) // durable claims, not the transport cache, stop replay
						response = f.send(t, key, method, f.params, wrapped)
						require.Nil(t, response.Error)
					}
					require.Equal(t, mutations, f.backend.stores+f.backend.promotions+f.backend.yanks+f.backend.observations)
					require.False(t, f.publisher.terminalBeforePersistence)
				})
			}
		}
	}
}

func TestPackageContextVMRemovedRegistrationIsMethodNotFound(t *testing.T) {
	for _, method := range packageMethods() {
		for _, wrapped := range []bool{false, true} {
			t.Run(method+map[bool]string{false: "plain", true: "wrapped"}[wrapped], func(t *testing.T) {
				f := newPackageWireFixture(t, method, NewFleetOperatorGate([]string{testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)}))
				delete(f.transport.contextVMHandlers, method)
				response := f.send(t, testRequesterKey, method, f.params, wrapped)
				require.NotNil(t, response.Error)
				require.Equal(t, -32601, response.Error.Code)
				require.Zero(t, f.store.calls)
			})
		}
	}
}

func (f *packageWireFixture) approve(t *testing.T, method string, wrapped bool) uuid.UUID {
	t.Helper()
	response := f.send(t, testOtherKey, packageApprovePlanMethod, map[string]any{"idempotency_key": uuid.NewString(), "requester_pubkey": testNostrPubKeyHexFromPrivateKey(t, testRequesterKey), "method": method, "params": f.params}, wrapped)
	require.Nil(t, response.Error)
	result := response.Result.(map[string]any)
	id, err := uuid.Parse(result["approval_id"].(string))
	require.NoError(t, err)
	return id
}

func TestPackageApprovalProvenanceBindingAndSingleUse(t *testing.T) {
	for _, method := range []string{"package/publish", ContextVMMethodPackagePromote} {
		for _, attack := range []string{"valid", "forged-name", "unknown-id", "requester", "command", "generation", "expiry", "revoked", "self", "replay"} {
			t.Run(method+"/"+attack, func(t *testing.T) {
				gate := NewFleetOperatorGate([]string{testNostrPubKeyHexFromPrivateKey(t, testRequesterKey), testNostrPubKeyHexFromPrivateKey(t, testOtherKey)})
				f := newPackageWireFixture(t, method, gate)
				f.repo.Policy.PublishRequiresApproval = true
				f.target.Policy.PromotionRequiresApproval = true
				require.NoError(t, f.store.UpsertRepository(t.Context(), f.repo))
				require.NoError(t, f.store.UpsertRepository(t.Context(), f.target))
				id := f.approve(t, method, true)
				f.params["approval_id"] = id.String()
				key := testRequesterKey
				switch attack {
				case "forged-name":
					delete(f.params, "approval_id")
					f.params["approved_by"] = testNostrPubKeyHexFromPrivateKey(t, testOtherKey)
				case "unknown-id":
					f.params["approval_id"] = uuid.NewString()
				case "requester":
					key = testOtherKey
				case "command":
					f.params["metadata"] = map[string]any{"changed": true}
				case "generation":
					f.repo.LastEventID = "new-policy-revision"
					require.NoError(t, f.store.UpsertRepository(t.Context(), f.repo))
				case "expiry":
					a := f.store.approvals[id]
					a.ExpiresAt = time.Now().Add(-time.Second)
					f.store.approvals[id] = a
				case "revoked":
					f.gate.authorizedPubkeys = []string{testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)}
				case "self":
					a := f.store.approvals[id]
					a.Approver = a.Requester
					f.store.approvals[id] = a
				}
				response := f.send(t, key, method, f.params, false)
				if attack != "valid" && attack != "replay" {
					require.NotNil(t, response.Error)
					require.Zero(t, f.backend.stores+f.backend.promotions)
					return
				}
				require.Nil(t, response.Error)
				require.Equal(t, 1, f.backend.stores+f.backend.promotions)
				for _, p := range f.store.publications {
					require.Equal(t, testNostrPubKeyHexFromPrivateKey(t, testOtherKey), p.ApprovedBy)
				}
				if attack == "replay" {
					// Return projections to the approved snapshot to isolate the
					// consumed-record check from the independent stale-plan check.
					if method == "package/publish" {
						f.store.artifacts = map[string]*domain.PackageArtifact{}
					} else {
						delete(f.store.artifacts, artifactKey(f.target.ID, "", "demo", "1.0.0", "demo.tgz"))
					}
					f.params["idempotency_key"] = uuid.NewString()
					f.restart(t)
					response = f.send(t, testRequesterKey, method, f.params, true)
					require.NotNil(t, response.Error)
					require.Equal(t, 1, f.backend.stores+f.backend.promotions)
				}
			})
		}
	}
}

func TestPackageDeprecationPreservesBytesAndAvailability(t *testing.T) {
	f := newPackageWireFixture(t, "package/yank", NewFleetOperatorGate([]string{testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)}))
	f.params["deprecated"], f.params["reason"] = true, "use v2"
	response := f.send(t, testRequesterKey, "package/yank", f.params, true)
	require.Nil(t, response.Error)
	require.Zero(t, f.backend.yanks)
	a, err := f.store.GetArtifact(t.Context(), f.repo.ID, "", "demo", "1.0.0", "demo.tgz")
	require.NoError(t, err)
	require.False(t, a.Deleted)
	require.Equal(t, domain.PackageArtifactStatusAvailable, a.Status)
	require.Equal(t, true, a.Metadata["deprecated"])
	stream, err := f.backend.GetArtifact(t.Context(), *f.repo, *a)
	require.NoError(t, err)
	data, err := io.ReadAll(stream.ReadCloser)
	require.NoError(t, err)
	require.NoError(t, stream.ReadCloser.Close())
	require.Equal(t, "signed package payload", string(data))
	f.params["deprecated"], f.params["idempotency_key"] = false, uuid.NewString()
	response = f.send(t, testRequesterKey, "package/yank", f.params, false)
	require.Nil(t, response.Error)
	require.Equal(t, 1, f.backend.yanks)
	_, err = f.backend.GetArtifact(t.Context(), *f.repo, *a)
	require.Error(t, err)
}

func TestPackagePublicationAndProjectionFailuresAreTerminalErrors(t *testing.T) {
	for _, method := range []string{"package/publish", ContextVMMethodPackagePromote, "package/yank", "package/drift-detect"} {
		for _, failure := range []string{"zero-artifact", "error-artifact", "zero-promotion", "zero-drift", "zero-terminal", "artifact", "promotion", "lookup", "claim", "terminal", "confirmation"} {
			if strings.Contains(failure, "artifact") && method == "package/drift-detect" {
				continue
			}
			if strings.Contains(failure, "promotion") && method != "package/publish" && method != ContextVMMethodPackagePromote {
				continue
			}
			if failure == "zero-drift" && method != "package/drift-detect" {
				continue
			}
			if failure == "lookup" && method == "package/drift-detect" {
				continue
			}
			t.Run(method+"/"+failure, func(t *testing.T) {
				f := newPackageWireFixture(t, method, NewFleetOperatorGate([]string{testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)}))
				if strings.HasPrefix(failure, "zero-") || strings.HasPrefix(failure, "error-") {
					schema := strings.SplitN(failure, "-", 2)[1]
					f.publisher.failSchema = map[string]string{"artifact": "bahia.state.package-artifact.v1", "promotion": "bahia.state.package-promotion.v1", "drift": "bahia.result.package-drift.v1", "terminal": "bahia.result.package.v1"}[schema]
					f.publisher.zero = strings.HasPrefix(failure, "zero-")
				} else if failure == "terminal" {
					f.store.upsertIntentErrStatus = domain.PackageIntentStatusSucceeded
					f.store.upsertIntentErr = errors.New("terminal persistence failed")
				} else {
					f.store.fail = failure
				}
				response := f.send(t, testRequesterKey, method, f.params, true)
				require.NotNil(t, response.Error)
				mutations := f.backend.stores + f.backend.promotions + f.backend.yanks + f.backend.observations
				f.restart(t)
				response = f.send(t, testRequesterKey, method, f.params, false)
				require.NotNil(t, response.Error, "restart must not turn failed publication into success")
				require.Equal(t, mutations, f.backend.stores+f.backend.promotions+f.backend.yanks+f.backend.observations)
				require.False(t, f.publisher.terminalBeforePersistence)
			})
		}
	}
}

func TestPackageDriftHasOneTerminalResponseAfterPersistence(t *testing.T) {
	f := newPackageWireFixture(t, "package/drift-detect", NewFleetOperatorGate([]string{testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)}))
	response := f.send(t, testRequesterKey, "package/drift-detect", f.params, false)
	require.Nil(t, response.Error)
	terminal, drift := 0, 0
	for _, event := range f.publisher.events {
		if event.Kind == KindContextVMMessage {
			var rpc map[string]any
			require.NoError(t, json.Unmarshal([]byte(event.Content), &rpc))
			if _, ok := rpc["result"]; ok {
				terminal++
			}
		}
		if tagValueNostr(event.Tags, "schema") == "bahia.result.package-drift.v1" {
			require.Equal(t, nostr.Kind(KindNIP38Status), event.Kind)
			drift++
		}
	}
	require.Equal(t, 1, terminal)
	require.Equal(t, 1, drift)
	require.False(t, f.publisher.terminalBeforePersistence)
}

func TestPackageApprovalRequiresAuthenticDistinctSigner(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		f := newPackageWireFixture(t, "package/publish", NewFleetOperatorGate([]string{testNostrPubKeyHexFromPrivateKey(t, testRequesterKey), testNostrPubKeyHexFromPrivateKey(t, testOtherKey)}))
		params := map[string]any{"requester_pubkey": testNostrPubKeyHexFromPrivateKey(t, testRequesterKey), "method": "package/publish", "params": f.params}
		response := f.send(t, testRequesterKey, packageApprovePlanMethod, params, wrapped)
		require.NotNil(t, response.Error)
		require.Empty(t, f.store.approvals)
		require.Zero(t, f.store.calls)
		f.publisher.events = nil
		request := makeRouteRequest(t, packageApprovePlanMethod, params)
		// Replacing the author without signing must never record provenance.
		request.PubKey = testNostrPubKeyFromPrivateKey(t, testOtherKey)
		if wrapped {
			request = wrapContextVMEvent(t, request, KindContextVMGiftWrap)
		}
		f.transport.HandleEvent(t.Context(), request)
		require.Empty(t, f.store.approvals)
		require.Zero(t, f.store.calls)
	}
}

func TestPackageArtifactRegistryAdvancesWireRevision(t *testing.T) {
	f := newPackageWireFixture(t, "package/yank", NewFleetOperatorGate([]string{testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)}))
	a, err := f.store.GetArtifact(t.Context(), f.repo.ID, "", "demo", "1.0.0", "demo.tgz")
	require.NoError(t, err)
	a.LastEventCreatedAt = time.Now().Add(time.Second).UTC()
	prior := a.LastEventCreatedAt
	f.params["deprecated"] = true
	response := f.send(t, testRequesterKey, "package/yank", f.params, false)
	require.Nil(t, response.Error)
	updated, err := f.store.GetArtifact(t.Context(), f.repo.ID, "", "demo", "1.0.0", "demo.tgz")
	require.NoError(t, err)
	require.True(t, updated.LastEventCreatedAt.After(prior))
}
