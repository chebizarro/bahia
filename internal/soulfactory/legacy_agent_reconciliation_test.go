package soulfactory

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
)

type legacyReconcileSoulSource struct{ current *domain.AgentSoul }

func (s *legacyReconcileSoulSource) GetSoul(_ context.Context, agentID string) (*domain.AgentSoul, error) {
	if s.current == nil || s.current.AgentID != agentID {
		return nil, nil
	}
	return cloneLegacySoul(s.current), nil
}

type legacyReconcileCapturePublisher struct {
	published []*domain.AgentSoul
	fail      error
	source    *legacyReconcileSoulSource
}

func (p *legacyReconcileCapturePublisher) PublishSoul(_ context.Context, soul *domain.AgentSoul) error {
	if p.fail != nil {
		return p.fail
	}
	copied := cloneLegacySoul(soul)
	copied.EventID = "superseding-" + soul.AgentID
	p.published = append(p.published, copied)
	soul.EventID = copied.EventID
	if p.source != nil {
		p.source.current = cloneLegacySoul(copied)
	}
	return nil
}

type legacyReconcileUnitStore struct {
	units   []domain.DeploymentUnit
	creates int
}

func (s *legacyReconcileUnitStore) Create(_ context.Context, unit *domain.DeploymentUnit) error {
	if unit.ID == uuid.Nil {
		unit.ID = uuid.New()
	}
	s.units = append(s.units, *unit)
	s.creates++
	return nil
}
func (s *legacyReconcileUnitStore) GetByEnvironmentKey(_ context.Context, environmentID uuid.UUID, key string) (*domain.DeploymentUnit, error) {
	for i := range s.units {
		if s.units[i].EnvironmentID == environmentID && s.units[i].Key == key {
			copy := s.units[i]
			return &copy, nil
		}
	}
	return nil, nil
}

func legacyReconcilePubkey(c byte) string { return strings.Repeat(string(c), 64) }
func legacyRuntime(agentID string, envID uuid.UUID) LegacyRunningAgent {
	return LegacyRunningAgent{
		InventoryID: "inv-" + agentID, Running: true, DocumentedAgentID: agentID,
		ManagedPubkey: legacyReconcilePubkey(agentID[0]), RuntimeBinding: "openclaw://agents/" + agentID,
		Workspace: "https://git/" + agentID, ContainerName: "runtime-" + agentID, RuntimeType: domain.RuntimeTypeDocker,
		AdoptedRuntime: &domain.AdoptedRuntimeConfig{
			TargetName: "runtime-" + agentID, ContainerID: "container-" + agentID,
			ImageDigest: "sha256:" + strings.Repeat("a", 64), SourceRuntime: "docker", HostAlias: "edge-1", EndpointRef: "edge-1-docker",
			Environment: map[string]string{"AGENT_ID": agentID}, Ports: []string{"8080:8080"}, Volumes: []string{"/srv/" + agentID + ":/data:ro"}, Restart: "unless-stopped",
		}, SourceRef: "runtime-inventory:" + agentID + ":" + envID.String(),
	}
}
func legacySoul(agentID, eventID string) *domain.AgentSoul {
	return &domain.AgentSoul{ID: uuid.New(), AgentID: agentID, Name: strings.ToUpper(agentID), Tier: domain.SoulTierStandard,
		Status: domain.SoulStatusActive, NostrPubkey: legacyReconcilePubkey(agentID[0]), NostrNpub: "npub1" + agentID,
		SoulMD: "soul read model", AllowedKinds: []int{1, domain.KindAgentSoul},
		ToolGrants:       []domain.ToolGrant{{MCPServer: "memory", Scopes: []string{"read", "write"}}},
		Runtime:          domain.SoulRuntimeSpec{Target: domain.RuntimeTargetOpenClaw, RuntimeBinding: "openclaw://agents/" + agentID, State: "running", RuntimePubkey: legacyReconcilePubkey(agentID[0])},
		WorkspaceRepoURL: "https://git/" + agentID, EventID: eventID, CreatedAt: time.Now().UTC()}
}
func legacyPlacement(envID uuid.UUID) LegacyReviewedPlacement {
	return LegacyReviewedPlacement{Ref: "operator-placement:edge-1", EnvironmentID: envID.String(), DeploymentUnitKey: "agent-bravo"}
}

type legacyReconcileHarness struct {
	reconciler *LegacyAgentReconciler
	registry   LegacyServiceStore
	units      *legacyReconcileUnitStore
	source     *legacyReconcileSoulSource
	publisher  *legacyReconcileCapturePublisher
	runtime    LegacyRunningAgent
	request    LegacyAgentReconciliationRequest
}

func newLegacyReconcileHarness(t *testing.T) *legacyReconcileHarness {
	t.Helper()
	registry, _, _, _, _, _ := newSoulFactoryRegistryHarness()
	integration, err := NewBahiaIntegration(registry, BahiaIntegrationConfig{}, slogDefaultLogger())
	if err != nil {
		t.Fatal(err)
	}
	envID := uuid.New()
	source := &legacyReconcileSoulSource{current: legacySoul("bravo", strings.Repeat("a", 64))}
	publisher := &legacyReconcileCapturePublisher{source: source}
	units := &legacyReconcileUnitStore{}
	reconciler, err := NewLegacyAgentReconciler(integration, registry, units, source, publisher, slogDefaultLogger())
	if err != nil {
		t.Fatal(err)
	}
	runtime := legacyRuntime("bravo", envID)
	return &legacyReconcileHarness{reconciler: reconciler, registry: registry, units: units, source: source, publisher: publisher, runtime: runtime,
		request: LegacyAgentReconciliationRequest{AgentID: "bravo", RuntimeAgents: []LegacyRunningAgent{runtime}, ReviewedPlacement: legacyPlacement(envID)}}
}
func (h *legacyReconcileHarness) preview(t *testing.T) LegacyAgentReconciliationClassification {
	t.Helper()
	report, err := h.reconciler.Preview(t.Context(), h.request)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Classifications) != 1 {
		t.Fatalf("classifications=%d", len(report.Classifications))
	}
	return report.Classifications[0]
}
func legacyApproval(c LegacyAgentReconciliationClassification) LegacyReconcileApproval {
	principal := &auth.Principal{Subject: "operator-pubkey", PubKey: "operator-pubkey", Method: auth.MethodNIP98}
	return LegacyReconcileApproval{AgentID: c.AgentID, Action: LegacyAgentReconcileActionLink, SoulEventID: c.SoulEventID,
		SoulContentHash: c.SoulContentHash, ApprovedBy: principal.Subject, ApprovalRef: "nip98-request", Principal: principal}
}

func TestClassifyLegacyAgentReconciliationClassifiesAllStates(t *testing.T) {
	envID := uuid.New()
	linkedID := uuid.New()
	alphaRuntime := legacyRuntime("alpha", envID)
	bravoRuntime := legacyRuntime("bravo", envID)
	input := LegacyAgentReconciliationInput{Schema: LegacyAgentReconciliationInputSchemaV1,
		Souls: []LegacyReconcileSoul{
			{EventID: strings.Repeat("1", 64), AgentID: "alpha", Status: string(domain.SoulStatusActive), AgentPubkey: alphaRuntime.ManagedPubkey, RuntimeBinding: alphaRuntime.RuntimeBinding, BahiaServiceID: linkedID.String(), SourceRef: "soul:alpha"},
			{EventID: strings.Repeat("2", 64), AgentID: "bravo", Status: string(domain.SoulStatusActive), AgentPubkey: bravoRuntime.ManagedPubkey, RuntimeBinding: bravoRuntime.RuntimeBinding, SourceRef: "soul:bravo"},
			{EventID: strings.Repeat("3", 64), AgentID: "charlie", Status: string(domain.SoulStatusActive), AgentPubkey: legacyReconcilePubkey('c'), SourceRef: "soul:charlie"}},
		RuntimeAgents:     []LegacyRunningAgent{alphaRuntime, bravoRuntime},
		Services:          []LegacyReconcileService{{ID: linkedID.String(), Name: "agent-alpha", ArtifactRepo: "agents/alpha", RuntimeType: string(alphaRuntime.RuntimeType), RuntimeConfig: &domain.ServiceRuntimeConfig{Adopted: cloneAdoptedRuntime(alphaRuntime.AdoptedRuntime)}, SourceRef: "bahia:alpha"}},
		ReviewedPlacement: map[string]LegacyReviewedPlacement{"bravo": legacyPlacement(envID)}}
	report, err := ClassifyLegacyAgentReconciliation(input)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]LegacyAgentReconciliationClassification{}
	for _, c := range report.Classifications {
		by[c.AgentID] = c
	}
	if by["alpha"].Status != legacyReconcileStatusLinked || by["bravo"].Status != legacyReconcileStatusUnlinked || by["charlie"].Status != legacyReconcileStatusOrphaned {
		t.Fatalf("unexpected classifications: %+v", by)
	}
	if by["bravo"].SoulContentHash == "" || by["bravo"].Plan == nil || by["bravo"].Plan.Placement.EnvironmentID != envID.String() {
		t.Fatalf("unbound plan: %+v", by["bravo"])
	}
	if report.Summary.Linked != 1 || report.Summary.Unlinked != 1 || report.Summary.Orphaned != 1 {
		t.Fatalf("summary=%+v", report.Summary)
	}
}

func TestClassifyLegacyAgentReconciliationQuarantinesAmbiguityAndWeakLinkedService(t *testing.T) {
	envID := uuid.New()
	linkedID := uuid.New()
	runtime := legacyRuntime("bravo", envID)
	input := LegacyAgentReconciliationInput{Schema: LegacyAgentReconciliationInputSchemaV1,
		Souls:         []LegacyReconcileSoul{{EventID: strings.Repeat("b", 64), AgentID: "bravo", Status: string(domain.SoulStatusActive), AgentPubkey: runtime.ManagedPubkey, RuntimeBinding: runtime.RuntimeBinding, BahiaServiceID: linkedID.String(), SourceRef: "soul:bravo"}},
		RuntimeAgents: []LegacyRunningAgent{runtime}, Services: []LegacyReconcileService{{ID: linkedID.String(), Name: "agent-bravo", ArtifactRepo: "agents/bravo", RuntimeType: string(domain.RuntimeTypeDocker), SourceRef: "bahia:bravo"}}}
	report, err := ClassifyLegacyAgentReconciliation(input)
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Classifications[0]; got.Status != legacyReconcileStatusOrphaned || got.ReasonCode != "linked_service_runtime_mismatch" {
		t.Fatalf("weak linked service accepted: %+v", got)
	}

	input.Souls[0].BahiaServiceID = ""
	input.RuntimeAgents = append(input.RuntimeAgents, runtime)
	input.RuntimeAgents[1].InventoryID = "inv-bravo-2"
	_, err = ClassifyLegacyAgentReconciliation(input)
	if !errors.Is(err, ErrLegacyReconciliationAmbiguous) {
		t.Fatalf("error=%v", err)
	}
}

func TestLegacyAgentReconciliationRejectsStaleClassification(t *testing.T) {
	h := newLegacyReconcileHarness(t)
	current := h.preview(t)
	staleSoul := legacySoul("bravo", strings.Repeat("9", 64))
	staleInput := LegacyAgentReconciliationInput{Schema: LegacyAgentReconciliationInputSchemaV1,
		Souls: []LegacyReconcileSoul{legacySoulProjection(staleSoul)}, RuntimeAgents: []LegacyRunningAgent{h.runtime}, ReviewedPlacement: map[string]LegacyReviewedPlacement{"bravo": h.request.ReviewedPlacement}}
	staleReport, err := ClassifyLegacyAgentReconciliation(staleInput)
	if err != nil {
		t.Fatal(err)
	}
	stale := staleReport.Classifications[0]
	if stale.SoulContentHash != current.SoulContentHash {
		t.Fatalf("test requires same content hash; stale=%s current=%s", stale.SoulContentHash, current.SoulContentHash)
	}
	_, err = h.reconciler.ReconcileApprovedLink(t.Context(), LegacyAgentReconcileApplyRequest{LegacyAgentReconciliationRequest: h.request, Classification: stale}, legacyApproval(stale))
	if !errors.Is(err, ErrLegacyReconciliationRefused) {
		t.Fatalf("stale classification error=%v", err)
	}
	services, _ := h.registry.ListServices(t.Context())
	if len(services) != 0 || len(h.units.units) != 0 || len(h.publisher.published) != 0 {
		t.Fatalf("stale apply mutated state")
	}
}

func TestLegacyAgentReconciliationRejectsForgedUnauthenticatedApproval(t *testing.T) {
	h := newLegacyReconcileHarness(t)
	classification := h.preview(t)
	approval := legacyApproval(classification)
	approval.Principal = nil
	approval.ApprovedBy = "forged-operator"
	_, err := h.reconciler.ReconcileApprovedLink(t.Context(), LegacyAgentReconcileApplyRequest{LegacyAgentReconciliationRequest: h.request, Classification: classification}, approval)
	if !errors.Is(err, ErrLegacyReconciliationRefused) {
		t.Fatalf("forged approval error=%v", err)
	}
}

func TestLegacyAgentReconciliationCoreRequiresBoundNIP98Principal(t *testing.T) {
	tests := []struct {
		name      string
		principal *auth.Principal
	}{
		{name: "system method", principal: &auth.Principal{Subject: "operator-pubkey", PubKey: "operator-pubkey", Method: auth.MethodSystem}},
		{name: "missing subject", principal: &auth.Principal{PubKey: "operator-pubkey", Method: auth.MethodNIP98}},
		{name: "missing pubkey", principal: &auth.Principal{Subject: "operator-pubkey", Method: auth.MethodNIP98}},
		{name: "mismatched subject and pubkey", principal: &auth.Principal{Subject: "operator-pubkey", PubKey: "different-pubkey", Method: auth.MethodNIP98}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newLegacyReconcileHarness(t)
			classification := h.preview(t)
			approval := legacyApproval(classification)
			approval.Principal = test.principal
			approval.ApprovedBy = test.principal.Subject
			_, err := h.reconciler.ReconcileApprovedLink(t.Context(), LegacyAgentReconcileApplyRequest{LegacyAgentReconciliationRequest: h.request, Classification: classification}, approval)
			if !errors.Is(err, ErrLegacyReconciliationRefused) {
				t.Fatalf("core approval error=%v", err)
			}
			services, listErr := h.registry.ListServices(t.Context())
			if listErr != nil {
				t.Fatal(listErr)
			}
			if len(services) != 0 || len(h.units.units) != 0 || len(h.publisher.published) != 0 {
				t.Fatalf("invalid core approval mutated state")
			}
		})
	}
}

func TestLegacyAgentReconciliationQuarantinesManagedServiceWithoutOverwrite(t *testing.T) {
	h := newLegacyReconcileHarness(t)
	managed := domain.NormalizeManagedRuntimeConfig(&domain.ManagedRuntimeConfig{
		ServiceName: "agent-bravo",
		Environment: map[string]string{"PRESERVE": "managed"},
	})
	serviceID := uuid.New()
	creator, ok := h.registry.(interface {
		CreateService(context.Context, *domain.Service) error
	})
	if !ok {
		t.Fatal("test registry cannot create services")
	}
	if err := creator.CreateService(t.Context(), &domain.Service{
		ID: serviceID, Name: "agent-bravo", ArtifactRepo: "agents/bravo", RuntimeType: domain.RuntimeTypeDocker,
		RuntimeConfig: &domain.ServiceRuntimeConfig{Managed: managed}, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	managedBefore := domain.NormalizeManagedRuntimeConfig(managed)

	report, err := h.reconciler.Preview(t.Context(), h.request)
	if !errors.Is(err, ErrLegacyReconciliationAmbiguous) {
		t.Fatalf("preview error=%v", err)
	}
	if len(report.Classifications) != 1 {
		t.Fatalf("classifications=%d", len(report.Classifications))
	}
	classification := report.Classifications[0]
	if classification.Status != legacyReconcileStatusAmbiguous || classification.ReasonCode != "managed_service_reuse_refused" || classification.ExistingServiceID != serviceID.String() {
		t.Fatalf("managed service was not quarantined: %+v", classification)
	}

	_, err = h.reconciler.ReconcileApprovedLink(t.Context(), LegacyAgentReconcileApplyRequest{LegacyAgentReconciliationRequest: h.request, Classification: classification}, legacyApproval(classification))
	if !errors.Is(err, ErrLegacyReconciliationRefused) {
		t.Fatalf("managed service apply error=%v", err)
	}
	services, listErr := h.registry.ListServices(t.Context())
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(services) != 1 || services[0].ID != serviceID || services[0].RuntimeConfig == nil || !reflect.DeepEqual(services[0].RuntimeConfig.Managed, managedBefore) || services[0].RuntimeConfig.Adopted != nil {
		t.Fatalf("managed service changed: %+v", services)
	}
	if len(h.units.units) != 0 || len(h.publisher.published) != 0 {
		t.Fatalf("managed service refusal created units or published a Soul")
	}
}

func TestLegacyAgentReconciliationAdoptsExactRuntimeAndIsIdempotent(t *testing.T) {
	h := newLegacyReconcileHarness(t)
	classification := h.preview(t)
	before := snapshotLegacySoulIdentity(h.source.current)
	apply := LegacyAgentReconcileApplyRequest{LegacyAgentReconciliationRequest: h.request, Classification: classification}
	receipt, err := h.reconciler.ReconcileApprovedLink(t.Context(), apply, legacyApproval(classification))
	if err != nil {
		t.Fatal(err)
	}
	if !receipt.ServiceCreated || !receipt.RuntimeAdopted || receipt.DeploymentUnitID == uuid.Nil {
		t.Fatalf("receipt=%+v", receipt)
	}
	services, _ := h.registry.ListServices(t.Context())
	if len(services) != 1 || services[0].RuntimeConfig == nil || !reflect.DeepEqual(services[0].RuntimeConfig.Adopted, h.runtime.AdoptedRuntime) {
		t.Fatalf("exact runtime not adopted: %+v", services)
	}
	if h.units.creates != 1 || len(h.units.units) != 1 {
		t.Fatalf("units=%d creates=%d", len(h.units.units), h.units.creates)
	}
	unit := h.units.units[0]
	if unit.OwnershipMode != domain.OwnershipModeAdopted || unit.ReconcileMode != domain.ReconcileModeObserveOnly || unit.EndpointRef != h.runtime.AdoptedRuntime.EndpointRef {
		t.Fatalf("unit=%+v", unit)
	}
	if !reflect.DeepEqual(before, snapshotLegacySoulIdentity(h.source.current)) {
		t.Fatalf("Soul identity/runtime changed")
	}
	if len(h.publisher.published) != 1 {
		t.Fatalf("publishes=%d", len(h.publisher.published))
	}
	event := BuildAgentSoulEvent(h.publisher.published[0])
	if findTag(event, tagService) != receipt.ServiceID.String() || findTag(event, tagRuntimeBinding) != h.runtime.RuntimeBinding || !eventHasTagValue(event, tagAllowedKind, "31951") {
		t.Fatalf("superseding Soul did not only add link: %+v", event.Tags)
	}

	classification2 := h.preview(t)
	receipt2, err := h.reconciler.ReconcileApprovedLink(t.Context(), LegacyAgentReconcileApplyRequest{LegacyAgentReconciliationRequest: h.request, Classification: classification2}, legacyApproval(classification2))
	if err != nil {
		t.Fatal(err)
	}
	if !receipt2.NoOp || receipt2.ServiceID != receipt.ServiceID || h.units.creates != 1 || len(h.publisher.published) != 1 {
		t.Fatalf("idempotent replay failed: %+v", receipt2)
	}
	services, _ = h.registry.ListServices(t.Context())
	if len(services) != 1 {
		t.Fatalf("duplicate services=%d", len(services))
	}
}

func TestLegacyAgentReconciliationRecoversAfterSoulPublishFailure(t *testing.T) {
	h := newLegacyReconcileHarness(t)
	classification := h.preview(t)
	apply := LegacyAgentReconcileApplyRequest{LegacyAgentReconciliationRequest: h.request, Classification: classification}
	h.publisher.fail = errors.New("relay refused")
	_, err := h.reconciler.ReconcileApprovedLink(t.Context(), apply, legacyApproval(classification))
	if err == nil || !strings.Contains(err.Error(), "relay refused") {
		t.Fatalf("publish error=%v", err)
	}
	services, _ := h.registry.ListServices(t.Context())
	if len(services) != 1 || h.units.creates != 1 || h.source.current.BahiaServiceID != nil {
		t.Fatalf("partial failure state wrong")
	}
	h.publisher.fail = nil
	classification = h.preview(t)
	apply.Classification = classification
	receipt, err := h.reconciler.ReconcileApprovedLink(t.Context(), apply, legacyApproval(classification))
	if err != nil {
		t.Fatal(err)
	}
	services, _ = h.registry.ListServices(t.Context())
	if receipt.ServiceCreated || len(services) != 1 || h.units.creates != 1 || len(h.publisher.published) != 1 {
		t.Fatalf("recovery duplicated state: receipt=%+v services=%d units=%d publishes=%d", receipt, len(services), h.units.creates, len(h.publisher.published))
	}
}

func eventHasTagValue(event *nostr.Event, name, value string) bool {
	for _, tag := range event.Tags {
		if len(tag) >= 2 && tag[0] == name && tag[1] == value {
			return true
		}
	}
	return false
}
