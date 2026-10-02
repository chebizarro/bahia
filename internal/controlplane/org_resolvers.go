package controlplane

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/repository"
)

// ServiceBackedSecretOrgResolver resolves org IDs for secrets by looking up the
// secret's parent service. The secret → service → org chain follows the existing
// EncryptedRouteHandlers.resolveSecretOrgID pattern.
type ServiceBackedSecretOrgResolver struct {
	services repository.ServiceRepository
	secrets  repository.SecretRepository
}

// NewServiceBackedSecretOrgResolver creates a resolver backed by service and
// secret repositories.
func NewServiceBackedSecretOrgResolver(services repository.ServiceRepository, secrets repository.SecretRepository) *ServiceBackedSecretOrgResolver {
	return &ServiceBackedSecretOrgResolver{services: services, secrets: secrets}
}

// ServiceOrgID resolves the org ID from a service ID.
func (r *ServiceBackedSecretOrgResolver) ServiceOrgID(ctx context.Context, serviceID uuid.UUID) (string, error) {
	if r.services == nil {
		return "", fmt.Errorf("service repository not configured")
	}
	svc, err := r.services.GetByID(ctx, serviceID)
	if err != nil {
		return "", fmt.Errorf("resolve service %s: %w", serviceID, err)
	}
	if svc == nil {
		return "", fmt.Errorf("service %s not found", serviceID)
	}
	return svc.OrgID.String(), nil
}

// SecretOrgID resolves the org ID from a secret ID by looking up the secret's
// parent service.
func (r *ServiceBackedSecretOrgResolver) SecretOrgID(ctx context.Context, secretID uuid.UUID) (string, error) {
	if r.secrets == nil {
		return "", fmt.Errorf("secret repository not configured")
	}
	secret, err := r.secrets.GetByID(ctx, secretID)
	if err != nil {
		return "", fmt.Errorf("resolve secret %s: %w", secretID, err)
	}
	if secret == nil {
		return "", fmt.Errorf("secret %s not found", secretID)
	}
	return r.ServiceOrgID(ctx, secret.ServiceID)
}

// RepoBackedNotificationOrgResolver resolves org IDs for notification channels
// by looking up the channel record.
type RepoBackedNotificationOrgResolver struct {
	notifications repository.NotificationRepository
}

// NewRepoBackedNotificationOrgResolver creates a resolver backed by the
// notification repository.
func NewRepoBackedNotificationOrgResolver(notifications repository.NotificationRepository) *RepoBackedNotificationOrgResolver {
	return &RepoBackedNotificationOrgResolver{notifications: notifications}
}

// ChannelOrgID resolves the org ID for a notification channel.
func (r *RepoBackedNotificationOrgResolver) ChannelOrgID(ctx context.Context, channelID uuid.UUID) (uuid.UUID, error) {
	if r.notifications == nil {
		return uuid.Nil, fmt.Errorf("notification repository not configured")
	}
	ch, err := r.notifications.GetChannelByID(ctx, channelID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("resolve channel %s: %w", channelID, err)
	}
	if ch == nil {
		return uuid.Nil, fmt.Errorf("channel %s not found", channelID)
	}
	return ch.OrgID, nil
}
