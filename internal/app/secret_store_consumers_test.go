package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
)

type markerSecretRepo struct {
	repository.SecretRepository
	secret domain.ServiceSecret
}

func (r markerSecretRepo) GetByID(context.Context, uuid.UUID) (*domain.ServiceSecret, error) {
	v := r.secret
	return &v, nil
}

// A VM plane secret whose value lives in the secret store is refused by name;
// the plane still reads PostgreSQL until bahia-cd0wr.4.7.
func TestVirtualizationPlaneSecretRefusesStoreBackedSecret(t *testing.T) {
	org, secretID := uuid.New(), uuid.New()
	key := strings.Repeat("ab", 32)
	policy := &virtualizationPolicy{
		config:   config.VirtualizationConfig{Hosts: []config.VirtualizationHostPolicy{{OrgID: org, PlaneSecretRefs: []uuid.UUID{secretID}}}},
		rbac:     auth.NewRBAC(vmAppMembers{org}),
		secrets:  markerSecretRepo{secret: domain.ServiceSecret{ID: secretID, ServiceID: uuid.New(), Name: "PLANE_TOKEN", EncryptionMethod: domain.EncryptionStoreRef}},
		services: vmNoServices{},
	}
	err := policy.AuthorizePlaneSecret(context.Background(), &auth.Principal{Subject: key, PubKey: key, Method: auth.MethodNIP98}, org, domain.SecretRef{ID: secretID})
	require.True(t, errors.Is(err, domain.ErrSecretInStore), "err = %v", err)
	require.ErrorContains(t, err, "PLANE_TOKEN")
}

type vmNoServices struct{ repository.ServiceRepository }

type storeBackedList []domain.SecretRef

func (l storeBackedList) StoreBackedSecrets(context.Context) ([]domain.SecretRef, error) {
	return l, nil
}

type recordingIndex struct {
	marked []uuid.UUID
	fail   uuid.UUID
}

func (r *recordingIndex) MarkStoreBacked(_ context.Context, ref domain.SecretRef) error {
	if ref.ID == r.fail {
		return errors.New("index row belongs to another service")
	}
	r.marked = append(r.marked, ref.ID)
	return nil
}

func TestIndexStoreBackedSecretsMarksEveryRecordAndReportsFailures(t *testing.T) {
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	index := &recordingIndex{fail: b}
	err := indexStoreBackedSecrets(context.Background(), storeBackedList{{ID: a}, {ID: b}, {ID: c}}, index)
	require.Error(t, err, "a record that could not be indexed is reported")
	require.Equal(t, []uuid.UUID{a, c}, index.marked, "one failure does not stop the others")
}
