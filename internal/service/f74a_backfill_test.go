package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
)

type f74aMemoryMarker struct {
	mu           sync.Mutex
	value        []byte
	writes       int
	failCursor   bool
	mutateBefore func([]byte) []byte
}

func (m *f74aMemoryMarker) GetControlRecord(_, _ string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]byte(nil), m.value...), nil
}
func (m *f74aMemoryMarker) PutControlRecord(_, _ string, v []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.value = append([]byte(nil), v...)
	m.writes++
	return nil
}
func (m *f74aMemoryMarker) UpdateControlRecord(_, _ string, fn func([]byte) ([]byte, error)) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.mutateBefore != nil {
		m.value = m.mutateBefore(m.value)
		m.mutateBefore = nil
	}
	v, err := fn(append([]byte(nil), m.value...))
	if err != nil {
		return nil, err
	}
	if m.failCursor {
		var progress F74aBackfillProgress
		if json.Unmarshal(v, &progress) == nil && progress.Cursor != uuid.Nil {
			m.failCursor = false
			return nil, errors.New("cursor commit interrupted")
		}
	}
	m.value = append([]byte(nil), v...)
	m.writes++
	return append([]byte(nil), v...), nil
}

type f74aSource struct {
	releases     []domain.LLMRelease
	signatures   []domain.ArtifactSignature
	sboms        []domain.ArtifactSBOM
	semantic     []domain.SBOMPackage
	legacy       []domain.SBOMPackage
	observations []domain.RuntimeObservation
	visits       []string
}

func (a *f74aSource) ListReleasesAfter(_ context.Context, after uuid.UUID, _ int) ([]domain.LLMRelease, error) {
	a.visits = append(a.visits, "releases")
	var out []domain.LLMRelease
	for _, x := range a.releases {
		if x.ID.String() > after.String() {
			out = append(out, x)
		}
	}
	return out, nil
}
func (a *f74aSource) ListSignaturesAfter(_ context.Context, after uuid.UUID, _ int) ([]domain.ArtifactSignature, error) {
	a.visits = append(a.visits, "signatures")
	var out []domain.ArtifactSignature
	for _, x := range a.signatures {
		if x.ID.String() > after.String() {
			out = append(out, x)
		}
	}
	return out, nil
}
func (a *f74aSource) ListSBOMsAfter(_ context.Context, after uuid.UUID, _ int) ([]domain.ArtifactSBOM, error) {
	a.visits = append(a.visits, "sboms")
	var out []domain.ArtifactSBOM
	for _, x := range a.sboms {
		if x.ID.String() > after.String() {
			out = append(out, x)
		}
	}
	return out, nil
}
func (a *f74aSource) ListSemanticPackagesAfter(_ context.Context, after uuid.UUID, _ int) ([]domain.SBOMPackage, error) {
	a.visits = append(a.visits, "semantic")
	var out []domain.SBOMPackage
	for _, x := range a.semantic {
		if x.ID.String() > after.String() {
			out = append(out, x)
		}
	}
	return out, nil
}
func (a *f74aSource) ListLegacyPackagesAfter(_ context.Context, after uuid.UUID, _ int) ([]domain.SBOMPackage, error) {
	a.visits = append(a.visits, "legacy")
	var out []domain.SBOMPackage
	for _, x := range a.legacy {
		if x.ID.String() > after.String() {
			out = append(out, x)
		}
	}
	return out, nil
}
func (a *f74aSource) ListLinkedObservationsAfter(_ context.Context, after repository.F74aStateCursor, _ int) ([]domain.RuntimeObservation, error) {
	a.visits = append(a.visits, "observations")
	var out []domain.RuntimeObservation
	for _, x := range a.observations {
		if x.ServiceID.String() > after.ServiceID.String() || (x.ServiceID == after.ServiceID && x.EnvironmentID.String() > after.EnvironmentID.String()) {
			out = append(out, x)
		}
	}
	return out, nil
}

type f74aBackfillPub struct {
	calls     []string
	fail      string
	onPublish func()
}

func (p *f74aBackfillPub) emit(name string) error {
	p.calls = append(p.calls, name)
	if p.onPublish != nil {
		p.onPublish()
	}
	if p.fail == name {
		return errors.New("refused")
	}
	return nil
}
func (p *f74aBackfillPub) PublishLLMRelease(context.Context, *domain.LLMRelease) error {
	return p.emit("release")
}
func (p *f74aBackfillPub) PublishArtifactSignature(context.Context, *domain.ArtifactSignature) error {
	return p.emit("signature")
}
func (p *f74aBackfillPub) PublishArtifactSBOM(context.Context, *domain.ArtifactSBOM) error {
	return p.emit("sbom")
}
func (p *f74aBackfillPub) PublishSBOMPackage(context.Context, *domain.SBOMPackage) error {
	return p.emit("semantic")
}
func (p *f74aBackfillPub) PublishLegacySBOMPackageTombstone(context.Context, *domain.SBOMPackage) error {
	return p.emit("legacy")
}
func (p *f74aBackfillPub) PublishRuntimeObservation(context.Context, *domain.RuntimeObservation) error {
	return p.emit("observation")
}
func f74aID(n byte) uuid.UUID { var id uuid.UUID; id[15] = n; return id }
func f74aRunner(marker *f74aMemoryMarker, source *f74aSource, pub *f74aBackfillPub) *F74aBackfillRunner {
	return NewF74aBackfillRunner(F74aBackfillConfig{Marker: marker, Source: source, Publisher: pub, Pending: func(context.Context) (int64, error) { return 0, nil }, SemanticDelivered: func(context.Context, *domain.SBOMPackage) (bool, error) { return true, nil }, Delivered: func(context.Context, string, any) (bool, error) { return true, nil }, Rate: 1000000000})
}
func readF74aProgress(t *testing.T, marker *f74aMemoryMarker) F74aBackfillProgress {
	t.Helper()
	raw, _ := marker.GetControlRecord("bootstrap", f74aProgressID)
	var p F74aBackfillProgress
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestF74aCompletedMarkerIsScopedToRelayPolicy(t *testing.T) {
	ctx := context.Background()
	marker := &f74aMemoryMarker{}
	source := &f74aSource{signatures: []domain.ArtifactSignature{{ID: f74aID(1)}}}
	pub := &f74aBackfillPub{}
	runner := f74aRunner(marker, source, pub)
	runner.cfg.PolicyID = "policy-a"
	runner.cfg.Author = "signer-a"
	if err := runner.RunMigration(ctx); err != nil {
		t.Fatal(err)
	}
	if p := readF74aProgress(t, marker); !p.Completed || p.PolicyID != "policy-a" {
		t.Fatalf("marker not scoped: %+v", p)
	}
	source.visits = nil
	runner = f74aRunner(marker, source, pub)
	runner.cfg.PolicyID = "policy-b"
	runner.cfg.Author = "signer-a"
	runner.cfg.Delivered = func(context.Context, string, any) (bool, error) { return false, nil }
	if err := runner.RunMigration(ctx); err == nil {
		t.Fatal("changed policy reused completed marker")
	}
	if p := readF74aProgress(t, marker); p.Completed || p.PolicyID != "policy-b" {
		t.Fatalf("marker did not reopen: %+v", p)
	}
	if len(source.visits) == 0 {
		t.Fatal("policy change did not restart verification pass")
	}

	legacy := F74aBackfillProgress{Phase: "complete", Completed: true, RelayVerified: true, ProofVersion: 2, Author: "signer-a"}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	marker.value = raw
	runner = f74aRunner(marker, source, pub)
	runner.cfg.PolicyID = "policy-b"
	runner.cfg.Author = "signer-a"
	runner.cfg.Delivered = func(context.Context, string, any) (bool, error) { return false, nil }
	if err := runner.RunMigration(ctx); err == nil {
		t.Fatal("legacy unscoped marker reused completion")
	}
	if readF74aProgress(t, marker).Completed {
		t.Fatal("legacy unscoped marker remained complete")
	}
}

func TestF74aBackfillStagesSemanticBeforeLegacyAndV2OnlyAfterAll(t *testing.T) {
	marker := &f74aMemoryMarker{value: []byte(`{"phase":"releases"}`)}
	source := &f74aSource{releases: []domain.LLMRelease{{ID: f74aID(1)}}, signatures: []domain.ArtifactSignature{{ID: f74aID(2)}}, sboms: []domain.ArtifactSBOM{{ID: f74aID(3)}}, semantic: []domain.SBOMPackage{{ID: f74aID(4)}}, legacy: []domain.SBOMPackage{{ID: f74aID(4)}, {ID: f74aID(5)}}, observations: []domain.RuntimeObservation{{ID: f74aID(6), ServiceID: f74aID(7), EnvironmentID: f74aID(8)}}}
	pub := &f74aBackfillPub{}
	runner := f74aRunner(marker, source, pub)
	if err := runner.runPass(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"release", "signature", "sbom", "semantic", "legacy", "legacy", "observation"}
	if len(pub.calls) != len(want) {
		t.Fatalf("calls %v", pub.calls)
	}
	for i, x := range want {
		if pub.calls[i] != x {
			t.Fatalf("calls %v", pub.calls)
		}
	}
	p := readF74aProgress(t, marker)
	if !p.Completed || p.Phase != "complete" {
		t.Fatalf("not complete: %+v", p)
	}
	if err := runner.runPass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(pub.calls) != len(want) {
		t.Fatal("completed pass replayed")
	}
}
func TestF74aBackfillRefusalRetainsCursorAndResumes(t *testing.T) {
	marker := &f74aMemoryMarker{}
	source := &f74aSource{semantic: []domain.SBOMPackage{{ID: f74aID(1)}, {ID: f74aID(2)}}}
	pub := &f74aBackfillPub{fail: "semantic"}
	runner := f74aRunner(marker, source, pub)
	if err := runner.runPass(context.Background()); err == nil {
		t.Fatal("expected refusal")
	}
	p := readF74aProgress(t, marker)
	if p.Completed || p.Phase != "semantic_packages" || p.Cursor != uuid.Nil {
		t.Fatalf("advanced on refusal: %+v", p)
	}
	pub.fail = ""
	runner = f74aRunner(marker, source, pub)
	if err := runner.runPass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !readF74aProgress(t, marker).Completed {
		t.Fatal("retry did not complete")
	}
}
func TestF74aBackfillDirtyGenerationRestartsKeysetPass(t *testing.T) {
	marker := &f74aMemoryMarker{}
	source := &f74aSource{releases: []domain.LLMRelease{{ID: f74aID(2)}}}
	pub := &f74aBackfillPub{}
	runner := f74aRunner(marker, source, pub)
	pub.onPublish = func() {
		if len(pub.calls) == 1 {
			source.releases = append([]domain.LLMRelease{{ID: f74aID(1)}}, source.releases...)
			if err := runner.markDirty(false); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := runner.runPass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(pub.calls) != 3 {
		t.Fatalf("concurrent behind-cursor insert missed: %v", pub.calls)
	}
	p := readF74aProgress(t, marker)
	if !p.Completed || p.Generation != 1 {
		t.Fatalf("dirty catch-up not complete: %+v", p)
	}
}
func TestF74aBackfillAdmissionCountErrorNeverBypasses(t *testing.T) {
	marker := &f74aMemoryMarker{}
	source := &f74aSource{releases: []domain.LLMRelease{{ID: f74aID(1)}}}
	pub := &f74aBackfillPub{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := NewF74aBackfillRunner(F74aBackfillConfig{Marker: marker, Source: source, Publisher: pub, Pending: func(context.Context) (int64, error) { cancel(); return 0, errors.New("count unavailable") }, Rate: 1000000000})
	if err := runner.runPass(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if len(pub.calls) != 0 {
		t.Fatal("published without outbox admission")
	}
	raw, _ := marker.GetControlRecord("bootstrap", f74aProgressID)
	if len(raw) > 0 && readF74aProgress(t, marker).Completed {
		t.Fatal("completed without outbox admission")
	}
}
func TestF74aLegacyTombstoneRequiresRelayAcceptedSemantic(t *testing.T) {
	marker := &f74aMemoryMarker{}
	pkg := domain.SBOMPackage{ID: f74aID(1)}
	source := &f74aSource{semantic: []domain.SBOMPackage{pkg}, legacy: []domain.SBOMPackage{pkg}}
	pub := &f74aBackfillPub{}
	runner := f74aRunner(marker, source, pub)
	accepted := false
	runner.cfg.SemanticDelivered = func(context.Context, *domain.SBOMPackage) (bool, error) { return accepted, nil }
	if err := runner.RunMigration(context.Background()); err == nil {
		t.Fatal("pending semantic was treated as relay accepted")
	}
	if got := pub.calls; len(got) != 1 || got[0] != "semantic" {
		t.Fatalf("premature tombstone: %v", got)
	}
	if readF74aProgress(t, marker).Completed {
		t.Fatal("completed on pending semantic")
	}
	accepted = true
	if err := runner.RunMigration(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := pub.calls; len(got) != 2 || got[1] != "legacy" {
		t.Fatalf("late acceptance did not permit tombstone: %v", got)
	}
}

func TestF74aCompletionRequiresSemanticProofEvenWithoutLegacy(t *testing.T) {
	marker := &f74aMemoryMarker{}
	source := &f74aSource{semantic: []domain.SBOMPackage{{ID: f74aID(1)}}}
	pub := &f74aBackfillPub{}
	runner := f74aRunner(marker, source, pub)
	runner.cfg.SemanticDelivered = func(context.Context, *domain.SBOMPackage) (bool, error) { return false, nil }
	if err := runner.RunMigration(context.Background()); err == nil {
		t.Fatal("missing proof completed migration")
	}
	if readF74aProgress(t, marker).Completed {
		t.Fatal("complete marker set without relay proof")
	}
}

func TestF74aBackfillCrashAfterPublishBeforeCursorReplaysItem(t *testing.T) {
	marker := &f74aMemoryMarker{failCursor: true}
	source := &f74aSource{releases: []domain.LLMRelease{{ID: f74aID(1)}}}
	pub := &f74aBackfillPub{}
	runner := f74aRunner(marker, source, pub)
	if err := runner.runPass(context.Background()); err == nil {
		t.Fatal("expected simulated cursor crash")
	}
	if len(pub.calls) != 1 || pub.calls[0] != "release" {
		t.Fatalf("publish did not stage before crash: %v", pub.calls)
	}
	raw, _ := marker.GetControlRecord("bootstrap", f74aProgressID)
	if len(raw) > 0 {
		p, err := decodeF74aProgress(raw)
		if err != nil {
			t.Fatal(err)
		}
		if p.Cursor != uuid.Nil {
			t.Fatalf("cursor advanced after failed commit: %+v", p)
		}
	}
	runner = f74aRunner(marker, source, pub)
	if err := runner.runPass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(pub.calls) != 2 || !readF74aProgress(t, marker).Completed {
		t.Fatalf("retry did not replay and complete: %v", pub.calls)
	}
	// The canonical publisher's durable coordinate test separately proves the
	// second call reuses a queued signed event instead of enqueuing a duplicate.
}

func TestF74aCompletionCASDoesNotSealReopenedPhase(t *testing.T) {
	original, _ := json.Marshal(F74aBackfillProgress{Phase: "complete", Generation: 5, PassGeneration: 5, ProofVersion: 2})
	marker := &f74aMemoryMarker{value: original}
	marker.mutateBefore = func(_ []byte) []byte {
		reopened, _ := json.Marshal(F74aBackfillProgress{Phase: "releases", Generation: 6, PassGeneration: 6, ProofVersion: 2})
		return reopened
	}
	runner := f74aRunner(marker, &f74aSource{}, &f74aBackfillPub{})
	if err := runner.runPass(context.Background()); err != nil {
		t.Fatal(err)
	}
	p := readF74aProgress(t, marker)
	if !p.Completed || p.Phase != "complete" || p.Generation != 6 {
		t.Fatalf("completion CAS lost dirty reopen: %+v", p)
	}
}

func TestF74aBackfillAdmissionUsesHighLowHysteresis(t *testing.T) {
	counts := []int64{2000, 1600, 1499}
	calls := 0
	runner := NewF74aBackfillRunner(F74aBackfillConfig{Pending: func(context.Context) (int64, error) {
		if calls >= len(counts) {
			t.Fatal("admission read beyond expected sequence")
		}
		n := counts[calls]
		calls++
		return n, nil
	}})
	var last time.Time
	paused := false
	if err := runner.admit(context.Background(), &last, &paused); err != nil {
		t.Fatal(err)
	}
	if calls != 3 || paused || runner.Snapshot().Pending != 1499 || runner.Snapshot().Paused {
		t.Fatalf("admission calls=%d paused=%t snapshot=%+v", calls, paused, runner.Snapshot())
	}
}

func TestF74aOldCompletedMarkerWithoutRelayProofReplaysSemanticPhase(t *testing.T) {
	raw, _ := json.Marshal(F74aBackfillProgress{Phase: "complete", Completed: true, Generation: 2, PassGeneration: 2})
	marker := &f74aMemoryMarker{value: raw}
	pkg := domain.SBOMPackage{ID: f74aID(1)}
	source := &f74aSource{semantic: []domain.SBOMPackage{pkg}, legacy: []domain.SBOMPackage{pkg}}
	pub := &f74aBackfillPub{}
	runner := f74aRunner(marker, source, pub)
	runner.cfg.SemanticDelivered = func(context.Context, *domain.SBOMPackage) (bool, error) { return false, nil }
	if err := runner.RunMigration(context.Background()); err == nil {
		t.Fatal("old unproven marker was trusted")
	}
	if len(pub.calls) != 1 || pub.calls[0] != "semantic" {
		t.Fatalf("old marker did not replay semantic before tombstone: %v", pub.calls)
	}
	if readF74aProgress(t, marker).Completed {
		t.Fatal("old marker remained complete")
	}
}

func TestF74aEachFamilyRequiresDurableRelayProofBeforeCursor(t *testing.T) {
	cases := []struct {
		name   string
		source f74aSource
		phase  string
	}{
		{"release", f74aSource{releases: []domain.LLMRelease{{ID: f74aID(1)}}}, "releases"},
		{"signature", f74aSource{signatures: []domain.ArtifactSignature{{ID: f74aID(1)}}}, "signatures"},
		{"sbom", f74aSource{sboms: []domain.ArtifactSBOM{{ID: f74aID(1)}}}, "sboms"},
		{"semantic", f74aSource{semantic: []domain.SBOMPackage{{ID: f74aID(1)}}}, "semantic_packages"},
		{"legacy", f74aSource{legacy: []domain.SBOMPackage{{ID: f74aID(1)}}}, "legacy_packages"},
		{"observation", f74aSource{observations: []domain.RuntimeObservation{{ID: f74aID(1), ServiceID: f74aID(2), EnvironmentID: f74aID(3)}}}, "observations"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			marker := &f74aMemoryMarker{}
			pub := &f74aBackfillPub{}
			source := tc.source
			runner := f74aRunner(marker, &source, pub)
			accepted := false
			runner.cfg.Delivered = func(_ context.Context, phase string, _ any) (bool, error) {
				if phase != tc.phase {
					t.Fatalf("unexpected proof phase %s", phase)
				}
				return accepted, nil
			}
			if err := runner.RunMigration(context.Background()); err == nil {
				t.Fatal("pending delivery advanced cursor")
			}
			p, err := runner.load()
			if err != nil {
				t.Fatal(err)
			}
			if p.Completed || p.Phase != tc.phase || p.Cursor != uuid.Nil || p.StateCursor.ServiceID != uuid.Nil {
				t.Fatalf("cursor advanced without proof: %+v", p)
			}
			accepted = true
			if err := runner.RunMigration(context.Background()); err != nil {
				t.Fatal(err)
			}
			if !readF74aProgress(t, marker).Completed {
				t.Fatal("accepted retry did not complete")
			}
		})
	}
}

func TestF74aCompletionRechecksBehindCursorWithoutDirtySignal(t *testing.T) {
	marker := &f74aMemoryMarker{}
	source := &f74aSource{releases: []domain.LLMRelease{{ID: f74aID(2)}}}
	pub := &f74aBackfillPub{}
	runner := f74aRunner(marker, source, pub)
	pub.onPublish = func() {
		if len(pub.calls) == 1 {
			source.releases = append([]domain.LLMRelease{{ID: f74aID(1)}}, source.releases...)
		}
	}
	runner.cfg.Delivered = func(_ context.Context, _ string, item any) (bool, error) {
		return item.(*domain.LLMRelease).ID != f74aID(1), nil
	}
	if err := runner.RunMigration(context.Background()); err == nil {
		t.Fatal("behind-cursor row was not checked at completion")
	}
	if readF74aProgress(t, marker).Completed {
		t.Fatal("completed with unproved behind-cursor row")
	}
}

func TestF74aCompletedMarkerCannotCrossSigningAuthor(t *testing.T) {
	raw, _ := json.Marshal(F74aBackfillProgress{Phase: "complete", Completed: true, RelayVerified: true, ProofVersion: 2, Author: "old"})
	marker := &f74aMemoryMarker{value: raw}
	source := &f74aSource{releases: []domain.LLMRelease{{ID: f74aID(1)}}}
	pub := &f74aBackfillPub{}
	runner := f74aRunner(marker, source, pub)
	runner.cfg.Author = "new"
	if err := runner.RunMigration(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(pub.calls) != 1 || pub.calls[0] != "release" {
		t.Fatalf("old author's completion skipped new signer: %v", pub.calls)
	}
	p := readF74aProgress(t, marker)
	if !p.Completed || p.Author != "new" {
		t.Fatalf("wrong author marker: %+v", p)
	}
}

func TestF74aCompletedMarkerRechecksSourceAndReopensOnChange(t *testing.T) {
	raw, _ := json.Marshal(F74aBackfillProgress{Phase: "complete", Completed: true, RelayVerified: true, ProofVersion: 2, Author: "same"})
	marker := &f74aMemoryMarker{value: raw}
	source := &f74aSource{releases: []domain.LLMRelease{{ID: f74aID(1)}}}
	pub := &f74aBackfillPub{}
	runner := f74aRunner(marker, source, pub)
	runner.cfg.Author = "same"
	accepted := false
	runner.cfg.Delivered = func(_ context.Context, phase string, _ any) (bool, error) {
		if phase != "releases" {
			t.Fatalf("unexpected proof phase %s", phase)
		}
		return accepted, nil
	}
	if err := runner.RunMigration(context.Background()); err == nil {
		t.Fatal("completed marker trusted after source changed")
	}
	p := readF74aProgress(t, marker)
	if p.Completed || p.RelayVerified || p.Phase != "releases" || p.Cursor != uuid.Nil {
		t.Fatalf("changed source did not reopen import: %+v", p)
	}
	if len(pub.calls) != 0 {
		t.Fatalf("verification unexpectedly published: %v", pub.calls)
	}
	accepted = true
	if err := runner.RunMigration(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !readF74aProgress(t, marker).Completed {
		t.Fatal("verified retry did not complete")
	}
}
