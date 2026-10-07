package controlplane

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

// retirableOverrideStore is a minimal DNSPersistenceOperator stand-in whose
// override read path mirrors the production SQL predicate
// "expires_at IS NULL OR expires_at > now()", so retirement is observed exactly
// as the reconciler would observe it.
type retirableOverrideStore struct {
	*recordingDNSOperator
	overrides     map[uuid.UUID]*domain.DNSRecordOverride
	expireCalls   int
	expireReasons []string
	now           time.Time
}

func newRetirableOverrideStore(now time.Time) *retirableOverrideStore {
	return &retirableOverrideStore{
		recordingDNSOperator: &recordingDNSOperator{zones: map[string]bool{"sharegap.net": true}},
		overrides:            map[uuid.UUID]*domain.DNSRecordOverride{},
		now:                  now,
	}
}

func (s *retirableOverrideStore) CreateZone(context.Context, domain.DNSZone) error { return nil }

func (s *retirableOverrideStore) CreateOverride(_ context.Context, override domain.DNSRecordOverride) error {
	clone := override
	s.overrides[override.ID] = &clone
	return nil
}

func (s *retirableOverrideStore) GetOverride(_ context.Context, id uuid.UUID) (*domain.DNSRecordOverride, error) {
	found, ok := s.overrides[id]
	if !ok {
		return nil, nil
	}
	clone := *found
	return &clone, nil
}

func (s *retirableOverrideStore) ExpireOverride(_ context.Context, id uuid.UUID, at time.Time, reason string) error {
	s.expireCalls++
	s.expireReasons = append(s.expireReasons, reason)
	found, ok := s.overrides[id]
	if !ok {
		return errors.New("not found")
	}
	expiry := at.UTC()
	found.ExpiresAt = &expiry
	return nil
}

// ListOverridesByZone applies the same active-window predicate as the database.
func (s *retirableOverrideStore) ListOverridesByZone(_ context.Context, zoneName string) ([]domain.DNSRecordOverride, error) {
	var active []domain.DNSRecordOverride
	for _, override := range s.overrides {
		if override.ZoneName != zoneName {
			continue
		}
		if override.ExpiresAt != nil && !override.ExpiresAt.After(s.now) {
			continue
		}
		active = append(active, *override)
	}
	return active, nil
}

func seedAstilleroOverride(t *testing.T, store *retirableOverrideStore, id uuid.UUID, created time.Time) {
	t.Helper()
	if err := store.CreateOverride(context.Background(), domain.DNSRecordOverride{
		ID:             id,
		ZoneName:       "sharegap.net",
		RecordName:     "astillero",
		RecordType:     domain.DNSRecordTypeA,
		Value:          "192.168.40.104",
		TTL:            60,
		Reason:         "temporary pin during rollout",
		OperatorPubkey: "operator-pubkey",
		CreatedAt:      created,
	}); err != nil {
		t.Fatalf("seeding override: %v", err)
	}
}

func activeOverrideIDs(t *testing.T, store *retirableOverrideStore) []uuid.UUID {
	t.Helper()
	active, err := store.ListOverridesByZone(context.Background(), "sharegap.net")
	if err != nil {
		t.Fatalf("listing overrides: %v", err)
	}
	ids := make([]uuid.UUID, 0, len(active))
	for _, override := range active {
		ids = append(ids, override.ID)
	}
	return ids
}

func TestRetireDNSOverrideMakesOverrideInactive(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	store := newRetirableOverrideStore(now)
	id := uuid.MustParse("1273e277-dfa7-4459-a452-89598eeca4a2")
	seedAstilleroOverride(t, store, id, now.Add(-24*time.Hour))

	if got := activeOverrideIDs(t, store); len(got) != 1 {
		t.Fatalf("expected the override to start active, got %d", len(got))
	}

	retirement, err := retireDNSOverride(context.Background(), store, id, now, "Bahia now projects sharegap.net authoritatively")
	if err != nil {
		t.Fatalf("retireDNSOverride() error = %v", err)
	}
	if retirement.AlreadyInactive {
		t.Fatal("first retirement must not report already-inactive")
	}
	if !retirement.RetiredAt.Equal(now) {
		t.Fatalf("RetiredAt = %v, want %v", retirement.RetiredAt, now)
	}
	if got := activeOverrideIDs(t, store); len(got) != 0 {
		t.Fatalf("override still active after retirement: %v", got)
	}
	if len(store.expireReasons) != 1 || store.expireReasons[0] == "" {
		t.Fatalf("retirement reason not passed to persistence: %v", store.expireReasons)
	}
}

// TestRetireDNSOverrideIsIdempotent is the regression that matters most for an
// operator retrying a governed command: a second retirement must neither write
// again nor move the recorded retirement instant.
func TestRetireDNSOverrideIsIdempotent(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	store := newRetirableOverrideStore(now)
	id := uuid.New()
	seedAstilleroOverride(t, store, id, now.Add(-24*time.Hour))

	first, err := retireDNSOverride(context.Background(), store, id, now, "initial retirement")
	if err != nil {
		t.Fatalf("first retire error = %v", err)
	}

	later := now.Add(90 * time.Minute)
	second, err := retireDNSOverride(context.Background(), store, id, later, "retry")
	if err != nil {
		t.Fatalf("second retire error = %v", err)
	}
	if !second.AlreadyInactive {
		t.Fatal("second retirement must report already-inactive")
	}
	if !second.RetiredAt.Equal(first.RetiredAt) {
		t.Fatalf("retirement instant moved on retry: %v -> %v", first.RetiredAt, second.RetiredAt)
	}
	if store.expireCalls != 1 {
		t.Fatalf("expected exactly one persistence write, got %d", store.expireCalls)
	}
	if got := len(store.overrides); got != 1 {
		t.Fatalf("retirement duplicated rows: %d", got)
	}
}

// TestRetireDNSOverridePreservesProvenance guards the audit trail: retirement
// withdraws an override but must not rewrite why or by whom it was created.
func TestRetireDNSOverridePreservesProvenance(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	store := newRetirableOverrideStore(now)
	id := uuid.New()
	created := now.Add(-48 * time.Hour)
	seedAstilleroOverride(t, store, id, created)

	if _, err := retireDNSOverride(context.Background(), store, id, now, "superseded"); err != nil {
		t.Fatalf("retire error = %v", err)
	}
	stored := store.overrides[id]
	if !stored.CreatedAt.Equal(created) {
		t.Fatalf("CreatedAt rewritten: %v want %v", stored.CreatedAt, created)
	}
	if stored.Reason != "temporary pin during rollout" {
		t.Fatalf("original reason rewritten: %q", stored.Reason)
	}
	if stored.OperatorPubkey != "operator-pubkey" {
		t.Fatalf("original operator pubkey rewritten: %q", stored.OperatorPubkey)
	}
}

func TestRetireDNSOverrideNotFound(t *testing.T) {
	now := time.Now().UTC()
	store := newRetirableOverrideStore(now)
	if _, err := retireDNSOverride(context.Background(), store, uuid.New(), now, "reason"); !errors.Is(err, errDNSOverrideNotFound) {
		t.Fatalf("expected errDNSOverrideNotFound, got %v", err)
	}
	if store.expireCalls != 0 {
		t.Fatalf("missing override must not be written: %d", store.expireCalls)
	}
}

// TestRetireDNSOverrideBringsForwardFutureExpiry covers a scheduled override:
// an expiry still in the future means the override is ACTIVE and must be
// retired now, not treated as already inactive.
func TestRetireDNSOverrideBringsForwardFutureExpiry(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	store := newRetirableOverrideStore(now)
	id := uuid.New()
	seedAstilleroOverride(t, store, id, now.Add(-time.Hour))
	future := now.Add(72 * time.Hour)
	store.overrides[id].ExpiresAt = &future

	retirement, err := retireDNSOverride(context.Background(), store, id, now, "retire early")
	if err != nil {
		t.Fatalf("retire error = %v", err)
	}
	if retirement.AlreadyInactive {
		t.Fatal("an override expiring in the future is still active")
	}
	if got := activeOverrideIDs(t, store); len(got) != 0 {
		t.Fatalf("override still active: %v", got)
	}
}

// TestDNSPersistenceOperatorWithoutRetirementKeepsOtherOperations guards the
// interface-widening hazard. Retirement is an optional capability: a persistence
// operator that cannot retire must lose ONLY retirement. If retirement were
// folded into DNSPersistenceOperator, this operator would fail the handlers'
// type assertion and silently degrade zone-create and record-set to
// "unsupported" as well.
func TestDNSPersistenceOperatorWithoutRetirementKeepsOtherOperations(t *testing.T) {
	var legacy any = &recordingDNSPersistentOperator{
		recordingDNSOperator: &recordingDNSOperator{zones: map[string]bool{"sharegap.net": true}},
	}

	if _, ok := legacy.(DNSPersistenceOperator); !ok {
		t.Fatal("an operator without retirement must still satisfy DNSPersistenceOperator")
	}
	if _, ok := legacy.(DNSOverrideRetirementOperator); ok {
		t.Fatal("an operator without retirement must not satisfy DNSOverrideRetirementOperator")
	}
}

// TestRetiringOperatorSatisfiesBothCapabilities is the positive counterpart.
func TestRetiringOperatorSatisfiesBothCapabilities(t *testing.T) {
	var full any = newRetirableOverrideStore(time.Now().UTC())
	if _, ok := full.(DNSPersistenceOperator); !ok {
		t.Fatal("retirable store must satisfy DNSPersistenceOperator")
	}
	if _, ok := full.(DNSOverrideRetirementOperator); !ok {
		t.Fatal("retirable store must satisfy DNSOverrideRetirementOperator")
	}
}
