package controlplane

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

type failingRotationPublisher struct {
	fakeOCKPublisher
	fail     atomic.Bool
	attempts atomic.Int64
}

func (p *failingRotationPublisher) PublishKeyEnvelope(ctx context.Context, dTag, content string) error {
	p.attempts.Add(1)
	if p.fail.Load() && strings.Contains(dTag, ":v2:") {
		return errors.New("envelope relay refused rotation")
	}
	return p.fakeOCKPublisher.PublishKeyEnvelope(ctx, dTag, content)
}

func TestOCKRotationFailureWithholdsEncryptionUntilRetry(t *testing.T) {
	ctx := t.Context()
	orgID := "test-org-pending"
	removed := pubkeyFromHex(t, memberBKeyHex)
	remaining := pubkeyFromHex(t, memberAKeyHex)
	service := pubkeyFromHex(t, serviceKeyHex)
	publisher := &failingRotationPublisher{}
	manager := NewOCKManager(OCKManagerConfig{
		Signer: newTestKeySigner(t, serviceKeyHex), ServicePubkey: service,
		Publisher: publisher, History: &fakeOCKHistory{},
		Members: &fakeOCKMemberSource{pubkeys: []string{remaining, removed}},
	})
	encryptor := NewConfidentialEncryptor(manager, nil)
	now := time.Unix(1000, 0)
	manager.now = func() time.Time { return now }
	if _, err := manager.EnsureKey(ctx, orgID); err != nil {
		t.Fatal(err)
	}
	oldRecord, err := encryptor.EncryptConfidential(ctx, orgID, []byte("historical"), 32005, "org:pending", "org", nil)
	if err != nil {
		t.Fatal(err)
	}
	publisher.fail.Store(true)
	if err := encryptor.RotateKeyExcluding(ctx, orgID, removed); err == nil {
		t.Fatal("rotation unexpectedly succeeded")
	}
	_, err = encryptor.EncryptConfidential(ctx, orgID, []byte("secret"), 32005, "org:pending", "org", nil)
	var pending *OCKRotationPendingError
	if !errors.As(err, &pending) || pending.OrgID != orgID {
		t.Fatalf("encrypt error = %v, want typed pending rotation", err)
	}
	if got := manager.PendingRotations(); len(got) != 1 || got[0] != orgID {
		t.Fatalf("pending rotations = %v", got)
	}
	if _, err := encryptor.DecryptConfidential(ctx, oldRecord, 32005, "org:pending", "org"); err != nil {
		t.Fatalf("pending rotation blocked historical reads: %v", err)
	}
	if err := encryptor.WrapKeyForMember(ctx, orgID, removed); !errors.As(err, &pending) {
		t.Fatalf("wrap did not respect rotation guard: %v", err)
	}
	if manager.pendingWraps[orgID][removed] != nil {
		t.Fatal("excluded recipient was queued for a later wrap")
	}
	publisher.fail.Store(false)
	if err := encryptor.RotateKeyExcluding(ctx, orgID, removed); err != nil {
		t.Fatalf("retry rotation: %v", err)
	}
	if got := manager.PendingRotations(); len(got) != 0 {
		t.Fatalf("rotation still pending: %v", got)
	}
	if _, err := encryptor.EncryptConfidential(ctx, orgID, []byte("secret"), 32005, "org:pending", "org", nil); err != nil {
		t.Fatalf("encrypt after retry: %v", err)
	}
	memberBSigner := newTestKeySigner(t, memberBKeyHex)
	if key := findMemberEnvelopeForVersion(t, ctx, publisher.envelopes, memberBSigner, service, removed, 2); key.Version != 0 {
		t.Fatal("removed member received the rotated key from stale relay membership")
	}
	memberASigner := newTestKeySigner(t, memberAKeyHex)
	if key := findMemberEnvelopeForVersion(t, ctx, publisher.envelopes, memberASigner, service, remaining, 2); key.Version != 2 {
		t.Fatal("remaining member did not receive the rotated key")
	}
}

func TestOCKRotationRetriesOnEncryptWithBoundedBackoff(t *testing.T) {
	ctx := t.Context()
	manager, _, _ := newTestOCKManager(t, nil)
	publisher := &failingRotationPublisher{}
	manager.publisher = publisher
	now := time.Unix(1000, 0)
	manager.now = func() time.Time { return now }
	encryptor := NewConfidentialEncryptor(manager, nil)
	encrypt := func(org string) error {
		_, err := encryptor.EncryptConfidential(ctx, org, []byte("secret"), 32005, "org:retry", "org", nil)
		return err
	}
	if err := encrypt("pending"); err != nil {
		t.Fatal(err)
	}
	publisher.fail.Store(true)
	if err := encryptor.RotateKey(ctx, "pending"); err == nil {
		t.Fatal("rotation unexpectedly succeeded")
	}
	candidate := *manager.pending["pending"].key
	for _, delay := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second, time.Minute, time.Minute} {
		before := publisher.attempts.Load()
		var pending *OCKRotationPendingError
		if err := encrypt("pending"); !errors.As(err, &pending) {
			t.Fatalf("expected pending error during cooldown: %v", err)
		}
		if publisher.attempts.Load() != before {
			t.Fatal("retry attempted before cooldown elapsed")
		}
		now = now.Add(delay)
		if err := encrypt("pending"); !errors.As(err, &pending) {
			t.Fatalf("expected pending error after failed retry: %v", err)
		}
		if publisher.attempts.Load() != before+1 {
			t.Fatal("relevant event did not retry rotation")
		}
		if *manager.pending["pending"].key != candidate {
			t.Fatal("retry replaced the candidate key at the same version")
		}
	}
	if err := encrypt("unaffected"); err != nil {
		t.Fatalf("guard leaked into another org: %v", err)
	}
	publisher.fail.Store(false)
	now = now.Add(time.Minute)
	if err := encrypt("pending"); err != nil {
		t.Fatalf("successful event-triggered retry did not release encryption: %v", err)
	}
	if len(manager.PendingRotations()) != 0 {
		t.Fatal("successful retry did not clear readiness condition")
	}
	key, err := manager.GetKey(ctx, "pending")
	if err != nil || key != candidate {
		t.Fatalf("activated key = %+v, err = %v", key, err)
	}
}

type ockPublisherFunc func(context.Context, string, string) error

func (f ockPublisherFunc) PublishKeyEnvelope(ctx context.Context, dTag, content string) error {
	return f(ctx, dTag, content)
}

type ockHistoryFunc func(context.Context, string) ([]domain.KeyEnvelopeRecord, error)

func (f ockHistoryFunc) FindKeyEnvelopes(ctx context.Context, orgID string) ([]domain.KeyEnvelopeRecord, error) {
	return f(ctx, orgID)
}

func TestOCKRecoveryFailureDoesNotResetEpoch(t *testing.T) {
	ctx := t.Context()
	manager, sink, _ := newTestOCKManager(t, nil)
	manager.now = func() time.Time { return time.Unix(1000, 0) }
	recoveryErr := errors.New("history unavailable")
	manager.history = ockHistoryFunc(func(context.Context, string) ([]domain.KeyEnvelopeRecord, error) { return nil, recoveryErr })
	if _, err := manager.EnsureKey(ctx, "recover"); !errors.Is(err, recoveryErr) {
		t.Fatalf("history error was hidden by creating v1: %v", err)
	}
	if _, err := manager.RotateKey(ctx, "recover"); !errors.Is(err, recoveryErr) {
		t.Fatalf("rotation ignored history error: %v", err)
	}
	var pending *OCKRotationPendingError
	if _, err := manager.EnsureKey(ctx, "recover"); !errors.As(err, &pending) {
		t.Fatalf("failed cold rotation did not guard encryption: %v", err)
	}
	if len(sink.envelopes) != 0 {
		t.Fatal("history failure published a replacement epoch")
	}
	manager.history = &fakeOCKHistory{records: []domain.KeyEnvelopeRecord{{DTag: "org-key:recover:v7:opaque", Content: "unreadable"}}}
	if _, err := manager.RotateKey(ctx, "recover"); err == nil {
		t.Fatal("missing service wrap silently reset existing epoch to v1")
	}
	if len(sink.envelopes) != 0 {
		t.Fatal("unreadable existing envelopes caused key publication")
	}
	previous, published, history := newTestOCKManager(t, nil)
	if _, err := previous.EnsureKey(ctx, "recover"); err != nil {
		t.Fatal(err)
	}
	for _, record := range published.envelopes {
		history.records = append(history.records, domain.KeyEnvelopeRecord{DTag: record.DTag, Content: record.Content})
	}
	manager.history = history
	key, err := manager.RotateKey(ctx, "recover")
	if err != nil || key.Version != 2 {
		t.Fatalf("recovered rotation: version=%d err=%v", key.Version, err)
	}
}

func TestOCKWrapRetriesOnEncryptWithoutRotating(t *testing.T) {
	for _, removed := range []bool{false, true} {
		t.Run(map[bool]string{false: "member", true: "removed"}[removed], func(t *testing.T) {
			ctx := t.Context()
			member := pubkeyFromHex(t, memberAKeyHex)
			manager, publisher, _ := newTestOCKManager(t, nil)
			now := time.Unix(1000, 0)
			manager.now = func() time.Time { return now }
			initial, err := manager.EnsureKey(ctx, "wrap")
			if err != nil {
				t.Fatal(err)
			}
			source := &fakeOCKMemberSource{pubkeys: []string{member}}
			manager.members = source
			manager.publisher = ockPublisherFunc(func(context.Context, string, string) error { return errors.New("refused wrap") })
			if err := manager.WrapForRecipient(ctx, "wrap", member); err == nil {
				t.Fatal("wrap unexpectedly succeeded")
			}
			manager.publisher = publisher
			if removed {
				source.pubkeys = nil
			}
			encryptor := NewConfidentialEncryptor(manager, nil)
			encrypt := func() {
				t.Helper()
				if _, err := encryptor.EncryptConfidential(ctx, "wrap", []byte("secret"), 32005, "org:wrap", "org", nil); err != nil {
					t.Fatal(err)
				}
			}
			encrypt()
			if len(publisher.envelopes) != 1 {
				t.Fatal("wrap retried before cooldown")
			}
			now = now.Add(time.Second)
			encrypt()
			want := 2
			if removed {
				want = 1
			}
			if len(publisher.envelopes) != want {
				t.Fatalf("envelopes = %d, want %d", len(publisher.envelopes), want)
			}
			if len(manager.pendingWraps) != 0 {
				t.Fatal("wrap retry not cleared")
			}
			current, err := manager.GetKey(ctx, "wrap")
			if err != nil || current != initial {
				t.Fatalf("add rotated the key: %v", err)
			}
		})
	}
}

func TestOCKColdRemovalExcludesTargetAndRecoversVersion(t *testing.T) {
	ctx := t.Context()
	removed := pubkeyFromHex(t, memberBKeyHex)
	manager, publisher, history := newTestOCKManager(t, []string{removed})
	key, err := manager.EnsureKey(ctx, "cold")
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range publisher.envelopes {
		history.records = append(history.records, domain.KeyEnvelopeRecord{DTag: record.DTag, Content: record.Content})
	}
	for _, warm := range []bool{false, true} {
		fresh, sink, _ := newTestOCKManager(t, []string{removed})
		wantVersion := 1
		if warm {
			fresh.history = history
			wantVersion = key.Version + 1
		}
		rotated, err := fresh.RotateKeyExcluding(ctx, "cold", removed)
		if err != nil {
			t.Fatal(err)
		}
		if rotated.Version != wantVersion {
			t.Fatalf("version = %d, want %d", rotated.Version, wantVersion)
		}
		if len(sink.envelopes) != 1 {
			t.Fatal("cold removal distributed a key to its target")
		}
	}
}

func TestOCKRotationAndEncryptionAreSerialized(t *testing.T) {
	ctx := t.Context()
	manager, publisher, _ := newTestOCKManager(t, nil)
	// A fixed clock makes every competing encryption remain within cooldown.
	manager.now = func() time.Time { return time.Unix(1000, 0) }
	if _, err := manager.EnsureKey(ctx, "concurrent"); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	manager.publisher = ockPublisherFunc(func(context.Context, string, string) error {
		close(entered)
		<-release
		return errors.New("rotation rejected")
	})
	encryptor := NewConfidentialEncryptor(manager, nil)
	rotated := make(chan error, 1)
	go func() { rotated <- encryptor.RotateKey(ctx, "concurrent") }()
	<-entered
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := encryptor.EncryptConfidential(ctx, "concurrent", []byte("secret"), 32005, "org:concurrent", "org", nil)
			var pending *OCKRotationPendingError
			if !errors.As(err, &pending) {
				t.Errorf("encryption crossed failed rotation: %v", err)
			}
			if len(manager.PendingRotations()) != 1 {
				t.Error("missing pending health condition")
			}
		}()
	}
	close(release)
	if err := <-rotated; err == nil {
		t.Fatal("rotation unexpectedly succeeded")
	}
	wg.Wait()
	manager.publisher = publisher
	if err := encryptor.RotateKey(ctx, "concurrent"); err != nil {
		t.Fatal(err)
	}
}
