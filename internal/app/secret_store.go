package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	signetAdapter "github.com/openagentsinc/bahia/internal/adapters/signet"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/nostrout"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// secretStoreClientKeyEnv is refused: the requester key comes only from a
// mounted file (secret_store.signet.client_key_file).
const secretStoreClientKeyEnv = "BAHIA_SECRET_STORE_SIGNET_CLIENT_KEY"

// openSecretStore opens the external secret store selected by
// secret_store.backend, or returns nil when none is configured. The store's
// requester identity must differ from the service identity: a leaked
// service key must not be able to request secret values
// (docs/architecture/secret-store.md).
func openSecretStore(ctx context.Context, cfg *config.Config, servicePubkey string, admission *nostrout.Admission, logger *zap.Logger) (*signetAdapter.CredentialStore, error) {
	if cfg.SecretStore.Backend != config.SecretStoreBackendSignet {
		return nil, nil
	}
	sc := cfg.SecretStore.Signet
	info, err := os.Lstat(sc.ClientKeyFile)
	if err != nil {
		return nil, fmt.Errorf("secret_store.signet.client_key_file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("secret_store.signet.client_key_file must be a regular file, not a symlink or device")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("secret_store.signet.client_key_file must not be accessible by group or others (mode %#o)", info.Mode().Perm())
	}
	raw, err := config.LoadPrivateKey(sc.ClientKeyFile, secretStoreClientKeyEnv)
	if err != nil {
		return nil, fmt.Errorf("read secret_store.signet.client_key_file: %w", err)
	}
	clientKey, err := nostrutil.SecretKeyFromHex(raw)
	if err != nil {
		return nil, fmt.Errorf("secret_store.signet.client_key_file: %w", err)
	}
	if servicePubkey != "" && clientKey.Public().Hex() == servicePubkey {
		return nil, fmt.Errorf("secret_store.signet.client_key_file holds the service key; the secret store requester must be a separate identity")
	}
	signetPK, err := nostr.PubKeyFromHex(sc.Pubkey)
	if err != nil {
		return nil, fmt.Errorf("secret_store.signet.pubkey: %w", err)
	}
	store, err := signetAdapter.OpenCredentialStore(ctx, signetAdapter.CredentialStoreConfig{
		SignetPubkey:      signetPK,
		Relays:            sc.Relays,
		ClientKey:         clientKey,
		Owner:             sc.Owner,
		RequestTimeout:    sc.RequestTimeout,
		ClosedRetryBudget: cfg.Nostr.ClosedRetryBudget,
		OutboundAdmission: admission,
	}, logger)
	if err != nil {
		return nil, err
	}
	logger.Info("secret store configured", zap.String("backend", cfg.SecretStore.Backend),
		zap.String("requester_pubkey", clientKey.Public().Hex()), zap.String("owner", sc.Owner))
	return store, nil
}

// secretStoreHealthCheck reports the last Signet round trip. Signet being
// unreachable warns rather than fails: secret-requiring operations fail
// closed on their own, and everything else keeps working.
func secretStoreHealthCheck(store *signetAdapter.CredentialStore) func() HealthCheck {
	return func() HealthCheck {
		check := HealthCheck{Name: "secret_store", Details: map[string]string{"backend": config.SecretStoreBackendSignet}}
		at, ok, err := store.LastOutcome()
		switch {
		case !ok:
			check.Status = HealthStatusWarn
			check.Message = "no Signet round trip yet; secret operations fail closed until Signet answers"
		case err != nil:
			check.Status = HealthStatusWarn
			check.Message = "secret operations fail closed: " + err.Error()
			check.Details["last_round_trip"] = at.UTC().Format(time.RFC3339)
		default:
			check.Status = HealthStatusPass
			check.Message = "Signet secret store answered"
			check.Details["last_round_trip"] = at.UTC().Format(time.RFC3339)
		}
		return check
	}
}

// secretStoreProbeRunner makes one authenticated round trip at startup so a
// misconfigured or unreachable Signet shows in health before the first
// secret operation needs it. It is not required for readiness.
type secretStoreProbeRunner struct {
	store   *signetAdapter.CredentialStore
	timeout time.Duration
	logger  *zap.Logger
}

func (r *secretStoreProbeRunner) Name() string { return "secret-store-probe" }

func (r *secretStoreProbeRunner) Run(ctx context.Context) error {
	probeCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	if err := r.store.Health(probeCtx); err != nil {
		r.logger.Warn("secret store startup probe failed; secret operations fail closed until Signet answers", zap.Error(err))
		return nil
	}
	r.logger.Info("secret store startup probe succeeded")
	return nil
}

// secretIndexOrNil keeps a nil repository from becoming a non-nil interface.
func secretIndexOrNil(index *repository.PgSecretRepository) controlplane.SecretStoreIndex {
	if index == nil {
		return nil
	}
	return index
}

// storeBackedIndex is what the startup index pass needs from PostgreSQL;
// repository.PgSecretRepository implements it.
type storeBackedIndex interface {
	MarkStoreBacked(ctx context.Context, ref domain.SecretRef) error
	BlockSameNameSecrets(ctx context.Context, ref domain.SecretRef) ([]uuid.UUID, error)
}

// secretIndexRunner gives every canonical store-backed secret its
// value-less PostgreSQL marker once the projector has warm-started, so
// PostgreSQL consumers refuse such secrets instead of omitting them or
// serving another row. A row of another secret holding the same service,
// environment and name is blocked (made a scrubbed marker) rather than
// served. Until every secret is safely indexed the pass retries with
// backoff and its health check fails readiness with the reason.
type secretIndexRunner struct {
	view interface {
		StoreBackedSecrets(context.Context) ([]domain.SecretRef, error)
	}
	index      storeBackedIndex
	warm       <-chan struct{}
	backoffMin time.Duration
	backoffMax time.Duration
	logger     *zap.Logger

	mu       sync.Mutex
	attempts int
	lastErr  error
	indexed  bool
}

func (r *secretIndexRunner) Name() string { return "secret-store-index" }

func (r *secretIndexRunner) Run(ctx context.Context) error {
	select {
	case <-r.warm:
	case <-ctx.Done():
		return nil
	}
	backoff := r.backoffMin
	for {
		err := r.pass(ctx)
		r.mu.Lock()
		r.attempts++
		r.lastErr, r.indexed = err, err == nil
		r.mu.Unlock()
		if err == nil {
			r.logger.Info("store-backed secrets indexed in PostgreSQL")
			return nil
		}
		r.logger.Error("store-backed secrets are not safely indexed in PostgreSQL; retrying", zap.Error(err), zap.Duration("backoff", backoff))
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

func (r *secretIndexRunner) pass(ctx context.Context) error {
	refs, err := r.view.StoreBackedSecrets(ctx)
	if err != nil {
		return fmt.Errorf("read canonical store-backed secrets: %w", err)
	}
	var failed []error
	for _, ref := range refs {
		err := r.index.MarkStoreBacked(ctx, ref)
		if errors.Is(err, repository.ErrSecretNameTaken) {
			blocked, blockErr := r.index.BlockSameNameSecrets(ctx, ref)
			if blockErr != nil {
				failed = append(failed, blockErr)
				continue
			}
			failed = append(failed, fmt.Errorf("secret %s (%s): %w; rows %v are blocked (no consumer serves them) until one of these secrets is deleted", ref.ID, ref.Name, repository.ErrSecretNameTaken, blocked))
			continue
		}
		if err != nil {
			failed = append(failed, err)
		}
	}
	return errors.Join(failed...)
}

func (r *secretIndexRunner) healthCheck() HealthCheck {
	r.mu.Lock()
	defer r.mu.Unlock()
	check := HealthCheck{Name: "secret_store_index", Details: map[string]string{"attempts": fmt.Sprintf("%d", r.attempts)}}
	switch {
	case r.indexed:
		check.Status, check.Message = HealthStatusPass, "store-backed secrets are indexed in PostgreSQL"
	case r.lastErr != nil:
		check.Status = HealthStatusFail
		check.Message = "store-backed secrets are not safely indexed in PostgreSQL, so PostgreSQL consumers could omit them; retrying: " + r.lastErr.Error()
	default:
		check.Status, check.Message = HealthStatusWarn, "waiting for the projector warm start to index store-backed secrets"
	}
	return check
}
