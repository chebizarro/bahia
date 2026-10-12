package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type markerSecretRepo struct {
	repository.SecretRepository
	secret domain.ServiceSecret
}

func (r *markerSecretRepo) GetByID(context.Context, uuid.UUID) (*domain.ServiceSecret, error) {
	v := r.secret
	return &v, nil
}

// A VM plane secret is admitted when its value lives in the secret store and
// refused by name when it still holds a value sealed with the retired
// service key.
func TestVirtualizationPlaneSecretAdmitsStoreBackedAndRefusesLegacy(t *testing.T) {
	org, secretID, serviceID := uuid.New(), uuid.New(), uuid.New()
	key := strings.Repeat("ab", 32)
	secrets := &markerSecretRepo{secret: domain.ServiceSecret{ID: secretID, ServiceID: serviceID, Name: "PLANE_TOKEN", EncryptionMethod: domain.EncryptionStoreRef}}
	policy := &virtualizationPolicy{
		config:   config.VirtualizationConfig{Hosts: []config.VirtualizationHostPolicy{{OrgID: org, PlaneSecretRefs: []uuid.UUID{secretID}}}},
		rbac:     auth.NewRBAC(vmAppMembers{org}),
		secrets:  secrets,
		services: vmOneService{svc: domain.Service{ID: serviceID, OrgID: org}},
	}
	principal := &auth.Principal{Subject: key, PubKey: key, Method: auth.MethodNIP98}
	require.NoError(t, policy.AuthorizePlaneSecret(context.Background(), principal, org, domain.SecretRef{ID: secretID}))

	secrets.secret.EncryptionMethod = domain.EncryptionMethod("aes256gcm")
	err := policy.AuthorizePlaneSecret(context.Background(), principal, org, domain.SecretRef{ID: secretID})
	require.True(t, errors.Is(err, domain.ErrLegacySecretValue), "err = %v", err)
	require.ErrorContains(t, err, "PLANE_TOKEN")
}

type vmOneService struct {
	repository.ServiceRepository
	svc domain.Service
}

func (s vmOneService) GetByID(context.Context, uuid.UUID) (*domain.Service, error) {
	v := s.svc
	return &v, nil
}

type storeBackedList []domain.SecretRef

func (l storeBackedList) StoreBackedSecrets(context.Context) ([]domain.SecretRef, error) {
	return l, nil
}

// collidingIndex reports a name collision for one secret until the
// colliding row is deleted (here: after it has been blocked once).
type collidingIndex struct {
	colliding    uuid.UUID
	resolved     bool
	marked       []uuid.UUID
	blocked      int
	duringRetry  func()
	markAttempts int
}

func (c *collidingIndex) MarkStoreBacked(_ context.Context, ref domain.SecretRef) error {
	c.markAttempts++
	if ref.ID == c.colliding && c.blocked > 0 && c.duringRetry != nil {
		c.duringRetry()
		c.duringRetry = nil
	}
	if ref.ID == c.colliding && !c.resolved {
		if c.blocked > 0 {
			// The operator deleted the other secret after it was blocked.
			c.resolved = true
		} else {
			return repository.ErrSecretNameTaken
		}
	}
	c.marked = append(c.marked, ref.ID)
	return nil
}

func (c *collidingIndex) BlockSameNameSecrets(context.Context, domain.SecretRef) ([]uuid.UUID, error) {
	c.blocked++
	return []uuid.UUID{uuid.New()}, nil
}

// A store-backed secret whose name another row holds is never served from
// that row: the row is blocked, readiness fails with the reason while the
// collision lasts, the pass retries, and it passes once resolved.
func TestSecretIndexRunnerBlocksNameCollisionAndFailsReadinessUntilResolved(t *testing.T) {
	ok, colliding := uuid.New(), uuid.New()
	index := &collidingIndex{colliding: colliding}
	warm := make(chan struct{})
	runner := &secretIndexRunner{view: storeBackedList{{ID: ok, Name: "A"}, {ID: colliding, Name: "DB_PASSWORD"}}, index: index, warm: warm,
		backoffMin: time.Millisecond, backoffMax: time.Millisecond, logger: zap.NewNop()}
	require.Equal(t, HealthStatusWarn, runner.healthCheck().Status, "before warm start")

	var during HealthCheck
	index.duringRetry = func() { during = runner.healthCheck() }
	close(warm)
	require.NoError(t, runner.Run(context.Background()))

	require.Equal(t, 1, index.blocked, "the colliding row was blocked, not served")
	require.Equal(t, HealthStatusFail, during.Status, "readiness fails while the collision lasts")
	require.Contains(t, during.Message, "DB_PASSWORD")
	require.Contains(t, during.Message, "blocked")
	require.Equal(t, HealthStatusPass, runner.healthCheck().Status)
	require.Contains(t, index.marked, ok)
	require.Contains(t, index.marked, colliding)
}
