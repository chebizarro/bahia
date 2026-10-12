package app

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/secretstore"
	"go.uber.org/zap"
)

// systemKeyring builds the keyring of the daemon's own data keys from
// secret_store.system_keys (docs/architecture/secret-store.md). It is empty
// when none are configured.
func systemKeyring(cfg *config.Config) (*secretstore.Keyring, error) {
	var refs []secretstore.SystemKeyRef
	add := func(purpose string, keys []config.SystemKeyConfig) {
		for _, key := range keys {
			refs = append(refs, secretstore.SystemKeyRef{Purpose: purpose, KeyID: key.KeyID, Credential: domain.SecretCredentialRef{
				Store: secretstore.BackendSignet, CredentialID: key.CredentialID, Version: key.Version,
			}})
		}
	}
	add(secretstore.PurposeAssistantTranscript, cfg.SecretStore.SystemKeys.AssistantTranscript)
	add(secretstore.PurposeConfidentialDedupe, cfg.SecretStore.SystemKeys.ConfidentialDedupe)
	ring, err := secretstore.NewKeyring(refs)
	if err != nil {
		return nil, fmt.Errorf("secret_store.system_keys: %w", err)
	}
	return ring, nil
}

// systemKeysRunner resolves the system keys once at startup. Signet being
// unreachable, or a credential that is missing, refused, rotated or of the
// wrong size, keeps every consumer failing closed; the runner retries with
// backoff so fixing Signet needs no restart.
type systemKeysRunner struct {
	ring       *secretstore.Keyring
	store      secretstore.SecretStore
	backoffMin time.Duration
	backoffMax time.Duration
	logger     *zap.Logger

	mu       sync.Mutex
	attempts int
}

func (r *systemKeysRunner) Name() string { return "system-keys" }

func (r *systemKeysRunner) Run(ctx context.Context) error {
	backoff := r.backoffMin
	for {
		err := r.ring.Load(ctx, r.store)
		r.mu.Lock()
		r.attempts++
		r.mu.Unlock()
		if err == nil {
			r.logger.Info("system keys resolved from the secret store", zap.Strings("keys", systemKeyLabels(r.ring)))
			return nil
		}
		if ctx.Err() != nil {
			return nil
		}
		r.logger.Error("system keys are not resolved; their consumers fail closed; retrying", zap.Error(err), zap.Duration("backoff", backoff))
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		backoff = min(backoff*2, r.backoffMax)
	}
}

func systemKeyLabels(ring *secretstore.Keyring) []string {
	var labels []string
	for _, purpose := range []string{secretstore.PurposeAssistantTranscript, secretstore.PurposeConfidentialDedupe} {
		for _, ref := range ring.Refs(purpose) {
			labels = append(labels, ref.Label())
		}
	}
	return labels
}

// systemKeysHealthCheck fails readiness until every configured system key is
// resolved, and warns while confidential publishing has no dedupe key.
func systemKeysHealthCheck(ring *secretstore.Keyring, attempts func() int) func() HealthCheck {
	return func() HealthCheck {
		check := HealthCheck{Name: "system_keys", Details: map[string]string{"keys": strings.Join(systemKeyLabels(ring), ",")}}
		loaded, lastErr := ring.Status()
		switch {
		case ring.Empty():
			check.Status = HealthStatusWarn
		case loaded:
			check.Status, check.Message = HealthStatusPass, "system keys resolved from the secret store"
		case lastErr != nil:
			check.Status = HealthStatusFail
			check.Message = "system keys are not resolved, so their consumers fail closed; retrying: " + lastErr.Error()
			check.Details["attempts"] = fmt.Sprintf("%d", attempts())
		default:
			check.Status, check.Message = HealthStatusFail, "resolving system keys from the secret store"
		}
		if len(ring.Refs(secretstore.PurposeConfidentialDedupe)) == 0 {
			if check.Status == HealthStatusPass {
				check.Status = HealthStatusWarn
			}
			check.Message = strings.TrimPrefix(check.Message+"; ", "; ") + "confidential records are not published: secret_store.system_keys.confidential_dedupe is not configured"
		}
		return check
	}
}

func (r *systemKeysRunner) attemptCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.attempts
}
