package secretstore

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

func TestSystemAndTenantScopesNeverAdmitEachOther(t *testing.T) {
	tenant := Scope{OrgID: uuid.New(), ServiceID: uuid.New()}
	system, err := SystemScope(PurposeAssistantTranscript, "k1")
	if err != nil {
		t.Fatal(err)
	}
	const systemLabel = "bahia-system/assistant-transcript/k1"
	if !system.Admits(systemLabel) || !system.Valid() {
		t.Fatalf("system scope refuses its own label")
	}
	for _, label := range []string{
		"bahia-system/assistant-transcript/k2",
		"bahia-system/confidential-dedupe/k1",
		"bahia-system/assistant-transcript/k1/extra",
		tenant.LabelPrefix() + "DB_PASSWORD",
		"bahia-system/assistant-transcript/",
	} {
		if system.Admits(label) {
			t.Errorf("system scope admits %q", label)
		}
	}
	if !tenant.Admits(tenant.LabelPrefix() + "DB_PASSWORD") {
		t.Fatal("tenant scope refuses its own label")
	}
	if tenant.Admits(systemLabel) {
		t.Fatal("tenant scope admits a system label")
	}
	// A tenant scope can never reach the system namespace, not even by an
	// org or service id chosen to look like it.
	if (Scope{OrgID: uuid.New()}).Valid() || (Scope{}).Admits(systemLabel) {
		t.Fatal("incomplete tenant scope is valid")
	}
	if !IsSystemLabel(systemLabel) || IsSystemLabel(tenant.LabelPrefix()+"x") {
		t.Fatal("IsSystemLabel misclassifies")
	}
	for _, bad := range [][2]string{{"", "k1"}, {"Assistant", "k1"}, {"a/b", "k1"}, {"assistant-transcript", ""}, {"assistant-transcript", "../k"}, {"assistant-transcript", strings.Repeat("k", 65)}} {
		if _, err := SystemScope(bad[0], bad[1]); !errors.Is(err, ErrInvalidRef) {
			t.Errorf("SystemScope(%q, %q) = %v, want ErrInvalidRef", bad[0], bad[1], err)
		}
	}
}

// labelStore resolves credentials by id and enforces scope by label, as the
// Signet store does.
type labelStore struct {
	creds    map[string]labelledValue
	resolved int
	err      error
}

type labelledValue struct {
	label string
	value []byte
}

func (s *labelStore) Resolve(_ context.Context, ref domain.SecretCredentialRef, scope Scope) (*Value, error) {
	s.resolved++
	if s.err != nil {
		return nil, s.err
	}
	c, ok := s.creds[ref.CredentialID]
	if !ok {
		return nil, ErrNotFound
	}
	if !scope.Admits(c.label) {
		return nil, ErrDenied
	}
	return NewValue(append([]byte(nil), c.value...), time.Now().Add(time.Minute)), nil
}

func (s *labelStore) Inspect(context.Context, string, Scope) (Metadata, error) {
	return Metadata{}, errors.New("unused")
}
func (s *labelStore) Health(context.Context) error { return nil }

func (s *labelStore) ResolveAll(ctx context.Context, reqs []Request) ([]*Value, error) {
	values := make([]*Value, 0, len(reqs))
	for _, req := range reqs {
		value, err := s.Resolve(ctx, req.Ref, req.Scope)
		if err != nil {
			for _, v := range values {
				v.Zeroize()
			}
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

func credRef(c byte) domain.SecretCredentialRef {
	return domain.SecretCredentialRef{Store: BackendSignet, CredentialID: "cred_" + strings.Repeat(string(c), 64), Version: 1}
}

func TestKeyringLoadsUsesAndZeroizes(t *testing.T) {
	active := bytes.Repeat([]byte{1}, SystemKeySize)
	older := bytes.Repeat([]byte{2}, SystemKeySize)
	store := &labelStore{creds: map[string]labelledValue{
		credRef('a').CredentialID: {"bahia-system/assistant-transcript/k2", active},
		credRef('b').CredentialID: {"bahia-system/assistant-transcript/k1", older},
	}}
	ring, err := NewKeyring([]SystemKeyRef{
		{Purpose: PurposeAssistantTranscript, KeyID: "k2", Credential: credRef('a')},
		{Purpose: PurposeAssistantTranscript, KeyID: "k1", Credential: credRef('b')},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := ring.Load(ctx, store); err != nil {
		t.Fatal(err)
	}
	var got []byte
	if err := ring.Use(ctx, PurposeAssistantTranscript, "", func(ref SystemKeyRef, key []byte) error {
		if ref.KeyID != "k2" {
			t.Fatalf("active key = %s, want the first configured", ref.KeyID)
		}
		got = key
		return nil
	}); err != nil || !bytes.Equal(got, active) {
		t.Fatalf("active key: %v", err)
	}
	if err := ring.Use(ctx, PurposeAssistantTranscript, "k1", func(_ SystemKeyRef, key []byte) error {
		if !bytes.Equal(key, older) {
			t.Fatal("wrong older key")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := ring.Use(ctx, PurposeConfidentialDedupe, "", func(SystemKeyRef, []byte) error { return nil }); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("unconfigured purpose: %v", err)
	}
	if err := ring.Use(ctx, PurposeAssistantTranscript, "k9", func(SystemKeyRef, []byte) error { return nil }); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("unconfigured key id: %v", err)
	}
	if err := ring.Load(ctx, store); err != nil || store.resolved != 2 {
		t.Fatalf("reload resolved again: %v, %d resolves", err, store.resolved)
	}
	held := got
	ring.Zeroize()
	if !bytes.Equal(held, make([]byte, SystemKeySize)) {
		t.Fatal("Zeroize left key bytes in memory")
	}
	if err := ring.Use(ctx, PurposeAssistantTranscript, "", func(SystemKeyRef, []byte) error { return nil }); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Use after Zeroize: %v", err)
	}
	if loaded, _ := ring.Status(); loaded {
		t.Fatal("zeroized keyring reports loaded")
	}
}

func TestKeyringFailsClosed(t *testing.T) {
	ref := SystemKeyRef{Purpose: PurposeConfidentialDedupe, KeyID: "k1", Credential: credRef('a')}
	cases := map[string]labelledValue{
		"tenant label":   {uuid.NewString() + "/" + uuid.NewString() + "/k1", bytes.Repeat([]byte{1}, SystemKeySize)},
		"other purpose":  {"bahia-system/assistant-transcript/k1", bytes.Repeat([]byte{1}, SystemKeySize)},
		"short key":      {"bahia-system/confidential-dedupe/k1", []byte("short")},
		"passphrase key": {"bahia-system/confidential-dedupe/k1", bytes.Repeat([]byte("p"), 64)},
	}
	for name, cred := range cases {
		t.Run(name, func(t *testing.T) {
			ring, err := NewKeyring([]SystemKeyRef{ref})
			if err != nil {
				t.Fatal(err)
			}
			store := &labelStore{creds: map[string]labelledValue{ref.Credential.CredentialID: cred}}
			if err := ring.Load(context.Background(), store); err == nil {
				t.Fatal("Load accepted a bad credential")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
			defer cancel()
			err = ring.Use(ctx, PurposeConfidentialDedupe, "", func(SystemKeyRef, []byte) error {
				t.Fatal("Use served a key from a failed load")
				return nil
			})
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("Use = %v, want ErrUnavailable", err)
			}
		})
	}
	ring, _ := NewKeyring([]SystemKeyRef{ref})
	if err := ring.Load(context.Background(), &labelStore{err: ErrUnavailable}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("store outage: %v", err)
	}
	if _, last := ring.Status(); !errors.Is(last, ErrUnavailable) {
		t.Fatalf("Status lastErr = %v", last)
	}
	if err := ring.Load(context.Background(), nil); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("no store: %v", err)
	}
}

// Once a load has failed, Use does not wait: callers without a deadline
// fail at once with the reason until a later load succeeds.
func TestKeyringUseFailsFastAfterAFailedLoad(t *testing.T) {
	ref := SystemKeyRef{Purpose: PurposeConfidentialDedupe, KeyID: "k1", Credential: credRef('a')}
	ring, _ := NewKeyring([]SystemKeyRef{ref})
	if err := ring.Load(context.Background(), &labelStore{err: ErrDenied}); !errors.Is(err, ErrDenied) {
		t.Fatalf("load: %v", err)
	}
	served := make(chan error, 1)
	go func() {
		served <- ring.Use(context.Background(), PurposeConfidentialDedupe, "", func(SystemKeyRef, []byte) error { return nil })
	}()
	select {
	case err := <-served:
		if !errors.Is(err, ErrUnavailable) || !errors.Is(err, ErrDenied) {
			t.Fatalf("Use = %v, want ErrUnavailable carrying ErrDenied", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Use waited after a failed load")
	}
	store := &labelStore{creds: map[string]labelledValue{ref.Credential.CredentialID: {"bahia-system/confidential-dedupe/k1", bytes.Repeat([]byte{3}, SystemKeySize)}}}
	if err := ring.Load(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	if err := ring.Use(context.Background(), PurposeConfidentialDedupe, "", func(SystemKeyRef, []byte) error { return nil }); err != nil {
		t.Fatalf("Use after recovery: %v", err)
	}
}

func TestKeyringUseWaitsForLoad(t *testing.T) {
	ref := SystemKeyRef{Purpose: PurposeConfidentialDedupe, KeyID: "k1", Credential: credRef('a')}
	ring, _ := NewKeyring([]SystemKeyRef{ref})
	served := make(chan error, 1)
	go func() {
		served <- ring.Use(context.Background(), PurposeConfidentialDedupe, "", func(SystemKeyRef, []byte) error { return nil })
	}()
	store := &labelStore{creds: map[string]labelledValue{ref.Credential.CredentialID: {"bahia-system/confidential-dedupe/k1", bytes.Repeat([]byte{7}, SystemKeySize)}}}
	if err := ring.Load(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	if err := <-served; err != nil {
		t.Fatalf("waiting Use: %v", err)
	}
}

// Each store request costs outbound budget: a retry asks only for the keys
// an earlier attempt did not resolve, and nothing is served until all are.
func TestKeyringRetryResolvesOnlyMissingKeys(t *testing.T) {
	a := SystemKeyRef{Purpose: PurposeAssistantTranscript, KeyID: "k1", Credential: credRef('a')}
	b := SystemKeyRef{Purpose: PurposeConfidentialDedupe, KeyID: "k1", Credential: credRef('b')}
	ring, _ := NewKeyring([]SystemKeyRef{a, b})
	store := &labelStore{creds: map[string]labelledValue{a.Credential.CredentialID: {a.Label(), bytes.Repeat([]byte{1}, SystemKeySize)}}}
	if err := ring.Load(context.Background(), store); !errors.Is(err, ErrNotFound) {
		t.Fatalf("first load: %v", err)
	}
	if err := ring.Use(context.Background(), PurposeAssistantTranscript, "", func(SystemKeyRef, []byte) error { return nil }); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("a partly resolved keyring served a key: %v", err)
	}
	store.creds[b.Credential.CredentialID] = labelledValue{b.Label(), bytes.Repeat([]byte{2}, SystemKeySize)}
	if err := ring.Load(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	if store.resolved != 3 {
		t.Fatalf("store asked %d times, want 3 (a, b, then only b)", store.resolved)
	}
}

func TestKeyringConcurrentLoads(t *testing.T) {
	ref := SystemKeyRef{Purpose: PurposeConfidentialDedupe, KeyID: "k1", Credential: credRef('a')}
	ring, _ := NewKeyring([]SystemKeyRef{ref})
	done := make(chan error, 8)
	for range 8 {
		go func() {
			store := &labelStore{creds: map[string]labelledValue{ref.Credential.CredentialID: {"bahia-system/confidential-dedupe/k1", bytes.Repeat([]byte{3}, SystemKeySize)}}}
			done <- ring.Load(context.Background(), store)
		}()
	}
	for range 8 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if loaded, _ := ring.Status(); !loaded {
		t.Fatal("not loaded")
	}
}

func TestNewKeyringRejectsDuplicates(t *testing.T) {
	a := SystemKeyRef{Purpose: PurposeAssistantTranscript, KeyID: "k1", Credential: credRef('a')}
	if _, err := NewKeyring([]SystemKeyRef{a, a}); !errors.Is(err, ErrInvalidRef) {
		t.Fatalf("duplicate label: %v", err)
	}
	b := SystemKeyRef{Purpose: PurposeConfidentialDedupe, KeyID: "k1", Credential: credRef('a')}
	if _, err := NewKeyring([]SystemKeyRef{a, b}); !errors.Is(err, ErrInvalidRef) {
		t.Fatalf("shared credential: %v", err)
	}
	if _, err := NewKeyring([]SystemKeyRef{{Purpose: "Bad", KeyID: "k", Credential: credRef('c')}}); !errors.Is(err, ErrInvalidRef) {
		t.Fatalf("bad purpose: %v", err)
	}
}
