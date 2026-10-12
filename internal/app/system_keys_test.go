package app

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/secretstore"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

// systemKeyStore delivers labelled system keys, enforcing scope by label as
// Signet does, and can be switched between failing and answering.
type systemKeyStore struct {
	byID map[string]labelled
	mu   sync.Mutex
	err  error
}

func (s *systemKeyStore) setErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

type labelled struct {
	label string
	key   []byte
}

func (s *systemKeyStore) Resolve(_ context.Context, ref domain.SecretCredentialRef, scope secretstore.Scope) (*secretstore.Value, error) {
	s.mu.Lock()
	err := s.err
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	c, ok := s.byID[ref.CredentialID]
	if !ok {
		return nil, secretstore.ErrNotFound
	}
	if !scope.Admits(c.label) {
		return nil, secretstore.ErrDenied
	}
	return secretstore.NewValue(append([]byte(nil), c.key...), time.Now().Add(time.Minute)), nil
}

func (s *systemKeyStore) Inspect(context.Context, string, secretstore.Scope) (secretstore.Metadata, error) {
	return secretstore.Metadata{}, secretstore.ErrNotFound
}

func (s *systemKeyStore) Health(context.Context) error { return nil }

func credentialID(c byte) string { return "cred_" + strings.Repeat(string(c), 64) }

// testTranscriptKeys is a loaded assistant-transcript key provider.
func testTranscriptKeys(t *testing.T) service.KeyringTranscriptKeys {
	t.Helper()
	cfg := config.Defaults()
	cfg.SecretStore.SystemKeys.AssistantTranscript = []config.SystemKeyConfig{{KeyID: "k1", CredentialID: credentialID('a'), Version: 1}}
	ring, err := systemKeyring(cfg)
	if err != nil {
		t.Fatal(err)
	}
	store := &systemKeyStore{byID: map[string]labelled{credentialID('a'): {"bahia-system/assistant-transcript/k1", bytes.Repeat([]byte{9}, secretstore.SystemKeySize)}}}
	if err := ring.Load(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ring.Zeroize)
	return service.KeyringTranscriptKeys{Ring: ring}
}

func TestSystemKeysRunnerRetriesUntilResolvedAndHealthFailsClosed(t *testing.T) {
	cfg := config.Defaults()
	cfg.SecretStore.SystemKeys = config.SystemKeysConfig{
		AssistantTranscript: []config.SystemKeyConfig{{KeyID: "k2", CredentialID: credentialID('a'), Version: 2}},
		ConfidentialDedupe:  []config.SystemKeyConfig{{KeyID: "k1", CredentialID: credentialID('b'), Version: 1}},
	}
	ring, err := systemKeyring(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if refs := ring.Refs(secretstore.PurposeAssistantTranscript); len(refs) != 1 || refs[0].Label() != "bahia-system/assistant-transcript/k2" || refs[0].Credential.Version != 2 {
		t.Fatalf("transcript refs = %+v", refs)
	}
	store := &systemKeyStore{err: secretstore.ErrUnavailable, byID: map[string]labelled{
		credentialID('a'): {"bahia-system/assistant-transcript/k2", bytes.Repeat([]byte{1}, secretstore.SystemKeySize)},
		credentialID('b'): {"bahia-system/confidential-dedupe/k1", bytes.Repeat([]byte{2}, secretstore.SystemKeySize)},
	}}
	runner := &systemKeysRunner{ring: ring, store: store, backoffMin: time.Millisecond, backoffMax: time.Millisecond, logger: zap.NewNop()}
	health := systemKeysHealthCheck(ring, runner.attemptCount)
	if check := health(); check.Status != HealthStatusFail {
		t.Fatalf("before resolution: %+v", check)
	}

	// The keys resolve on the first attempt after Signet answers.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer waitCancel()
	err = ring.Use(waitCtx, secretstore.PurposeConfidentialDedupe, "", func(secretstore.SystemKeyRef, []byte) error { return nil })
	if !errors.Is(err, secretstore.ErrUnavailable) || !strings.Contains(err.Error(), "secret store is unavailable") {
		t.Fatalf("Use while Signet is down = %v", err)
	}
	if check := health(); check.Status != HealthStatusFail || !strings.Contains(check.Message, "retrying") {
		t.Fatalf("while failing: %+v", check)
	}
	store.setErr(nil)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := ring.Use(context.Background(), secretstore.PurposeConfidentialDedupe, "", func(secretstore.SystemKeyRef, []byte) error { return nil }); err != nil {
		t.Fatalf("Use after recovery: %v", err)
	}
	if check := health(); check.Status != HealthStatusPass {
		t.Fatalf("after resolution: %+v", check)
	}
	ring.Zeroize()
	if check := health(); check.Status != HealthStatusFail {
		t.Fatalf("after shutdown zeroize: %+v", check)
	}
}

func TestSystemKeysHealthWarnsWithoutDedupeKey(t *testing.T) {
	ring, err := systemKeyring(config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	check := systemKeysHealthCheck(ring, func() int { return 0 })()
	if check.Status != HealthStatusWarn || !strings.Contains(check.Message, "secret_store.system_keys.confidential_dedupe is not configured") {
		t.Fatalf("no keys: %+v", check)
	}
}
