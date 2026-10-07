package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// SecretIntentPublisher publishes canonical 30900 cp-state records for
// secret entities. The intent handler uses this interface for all publish
// operations. The nostr.SecretCanonicalPublisher implements it.
type SecretIntentPublisher interface {
	PublishSecretRef(ctx context.Context, ref domain.SecretRef) error
	PublishSecretDeleted(ctx context.Context, secretID uuid.UUID) error
}

// SecretIntentCRUD is the read/write contract the secret intent handler uses
// for level-triggered reconciliation. repository.SecretRepository satisfies it.
type SecretIntentCRUD interface {
	Create(ctx context.Context, s *domain.ServiceSecret) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.ServiceSecret, error)
	ListByService(ctx context.Context, serviceID uuid.UUID) ([]domain.ServiceSecret, error)
	Update(ctx context.Context, s *domain.ServiceSecret) error
	Delete(ctx context.Context, id uuid.UUID) error
}

// SecretIntentEncryptor encrypts values supplied by the in-process MCP path
// when the value is not already NIP-44 encrypted.
type SecretIntentEncryptor interface {
	Encrypt(plaintext string, method domain.EncryptionMethod) ([]byte, error)
}

// SecretIntentHandler processes kind-30900 intents for the "secret" domain.
// It is level-triggered: the intent's content is the full desired state, and
// the handler reconciles the entity toward it regardless of whether prior
// events for the coordinate have been seen.
//
// Secret intents carry encrypted values: the secret value in the intent
// content is NIP-44 encrypted to the daemon's service pubkey by the client.
// The handler stores the encrypted value as-is and publishes a canonical
// record containing ONLY the SecretRef metadata — never the plaintext value.
//
// Secret REVEAL stays ContextVM — this handler does not decrypt secrets.
//
// See design §7 Wave 5 N1.
type SecretIntentHandler struct {
	registry  SecretIntentCRUD
	encryptor SecretIntentEncryptor
	publisher SecretIntentPublisher
	status    *IntentStatusPublisher
	logger    *zap.Logger
}

// SecretIntentHandlerConfig configures the secret intent handler.
type SecretIntentHandlerConfig struct {
	Registry  SecretIntentCRUD
	Encryptor SecretIntentEncryptor
	Publisher SecretIntentPublisher
	Status    *IntentStatusPublisher
	Logger    *zap.Logger
}

// NewSecretIntentHandler constructs the handler.
func NewSecretIntentHandler(cfg SecretIntentHandlerConfig) *SecretIntentHandler {
	logger := cfg.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	return &SecretIntentHandler{
		registry:  cfg.Registry,
		encryptor: cfg.Encryptor,
		publisher: cfg.Publisher,
		status:    cfg.Status,
		logger:    logger.Named("secret-intent"),
	}
}

// HandleIntent processes a single secret intent. The processor has already
// deduplicated, validated, and authorized the intent.
func (h *SecretIntentHandler) HandleIntent(ctx context.Context, intent *Intent) error {
	switch intent.Op {
	case "delete":
		return h.handleDelete(ctx, intent)
	default:
		// Level-triggered: create and update both reconcile toward desired state.
		return h.handleCreateOrUpdate(ctx, intent)
	}
}

// PermissionFor returns domain.PermWriteSecrets for all secret ops.
func (h *SecretIntentHandler) PermissionFor(_ string) domain.Permission {
	return domain.PermWriteSecrets
}

func (h *SecretIntentHandler) handleCreateOrUpdate(ctx context.Context, intent *Intent) error {
	parsed, err := secretFromIntentContent(intent)
	if err != nil {
		return fmt.Errorf("parse secret intent content: %w", err)
	}

	// The encrypted_value in the intent content is the NIP-44-encrypted
	// secret value (encrypted by the client to the daemon's service pubkey).
	// Store it as-is; never decrypt for storage.
	if len(parsed.EncryptedValue) == 0 {
		// Encrypt plaintext supplied by an in-process MCP caller.
		if plaintext, ok := intent.Content["value"].(string); ok && plaintext != "" {
			if h.encryptor == nil {
				return fmt.Errorf("secret value encryption is not configured")
			}
			method := parsed.EncryptionMethod
			if method == "" {
				method = domain.EncryptionNIP44
			}
			encrypted, encErr := h.encryptor.Encrypt(plaintext, method)
			if encErr != nil {
				return fmt.Errorf("failed to encrypt secret value: %w", encErr)
			}
			parsed.EncryptedValue = encrypted
		} else {
			return fmt.Errorf("secret value is required (either encrypted_value or value)")
		}
	}

	// Level-triggered: try to load existing.
	existing, _ := h.registry.GetByID(ctx, parsed.ID)
	if existing == nil {
		if intent.ExpectedUpdatedAt != nil {
			return &revisionConflictError{entityType: "secret", entityID: parsed.ID, expected: *intent.ExpectedUpdatedAt}
		}
		// Create.
		if parsed.Version == 0 {
			parsed.Version = 1
		}
		now := time.Now().UTC()
		if parsed.CreatedAt.IsZero() {
			parsed.CreatedAt = now
		}
		parsed.UpdatedAt = now
		parsed.CreatedBy = intent.Actor

		if err := h.registry.Create(ctx, parsed); err != nil {
			return fmt.Errorf("create secret: %w", err)
		}
	} else {
		if intent.ExpectedUpdatedAt != nil && !intent.RevisionMatches(existing.UpdatedAt) {
			return &revisionConflictError{entityType: "secret", entityID: parsed.ID, expected: *intent.ExpectedUpdatedAt, actual: existing.UpdatedAt}
		}
		// Update: keep existing metadata, update value.
		existing.EncryptedValue = parsed.EncryptedValue
		existing.EncryptionMethod = parsed.EncryptionMethod
		existing.UpdatedAt = time.Now().UTC()
		if parsed.Name != "" {
			existing.Name = parsed.Name
		}

		if err := h.registry.Update(ctx, existing); err != nil {
			return fmt.Errorf("update secret: %w", err)
		}
		parsed = existing
	}

	// Publish canonical state (SecretRef only — never the value).
	if h.publisher != nil {
		ref := parsed.ToRef()
		if err := h.publisher.PublishSecretRef(ctx, ref); err != nil {
			h.logger.Warn("failed to publish secret ref", zap.Error(err))
			// Non-fatal: the intent was applied, the publish is best-effort.
		}
	}

	h.logger.Info("secret created/updated via intent",
		zap.String("secret_id", parsed.ID.String()),
		zap.String("intent_id", intent.IntentID),
	)
	return nil
}

func (h *SecretIntentHandler) handleDelete(ctx context.Context, intent *Intent) error {
	idStr, _ := intent.Content["id"].(string)
	secretID, err := uuid.Parse(idStr)
	if err != nil || secretID == uuid.Nil {
		secretID, err = uuid.Parse(intent.Coordinate)
		if err != nil || secretID == uuid.Nil {
			return fmt.Errorf("delete intent must carry entity id")
		}
	}

	if err := h.registry.Delete(ctx, secretID); err != nil {
		return fmt.Errorf("delete secret: %w", err)
	}

	// Publish tombstone.
	if h.publisher != nil {
		if err := h.publisher.PublishSecretDeleted(ctx, secretID); err != nil {
			h.logger.Warn("failed to publish secret tombstone", zap.Error(err))
		}
	}

	h.logger.Info("secret deleted via intent",
		zap.String("secret_id", secretID.String()),
		zap.String("intent_id", intent.IntentID),
	)
	return nil
}

// secretFromIntentContent parses intent content into a domain.ServiceSecret.
func secretFromIntentContent(intent *Intent) (*domain.ServiceSecret, error) {
	content := intent.Content
	if content == nil {
		return nil, fmt.Errorf("intent content is nil")
	}

	secret := &domain.ServiceSecret{
		EncryptionMethod: domain.EncryptionNIP44,
	}

	// Parse ID.
	if idStr, ok := content["id"].(string); ok && idStr != "" {
		id, err := uuid.Parse(idStr)
		if err != nil {
			return nil, fmt.Errorf("invalid secret id %q: %w", idStr, err)
		}
		secret.ID = id
	}
	if secret.ID == uuid.Nil {
		id, err := uuid.Parse(intent.Coordinate)
		if err != nil {
			secret.ID = domain.NewEntityID()
		} else {
			secret.ID = id
		}
	}

	// Name.
	if name, ok := content["name"].(string); ok {
		secret.Name = strings.TrimSpace(name)
	}

	// Service ID.
	if sid, ok := content["service_id"].(string); ok {
		serviceID, err := uuid.Parse(sid)
		if err == nil {
			secret.ServiceID = serviceID
		}
	}

	// Environment ID.
	if eid, ok := content["environment_id"].(string); ok && eid != "" {
		envID, err := uuid.Parse(eid)
		if err == nil {
			secret.EnvironmentID = &envID
		}
	}

	// Encryption method.
	if method, ok := content["encryption_method"].(string); ok && method != "" {
		secret.EncryptionMethod = domain.EncryptionMethod(method)
	}

	// Encrypted value (pre-encrypted by the client).
	if encVal, ok := content["encrypted_value"].(string); ok && encVal != "" {
		secret.EncryptedValue = []byte(encVal)
	}

	// Version.
	if v, ok := content["version"].(float64); ok {
		secret.Version = int(v)
	}

	// Created by.
	if cb, ok := content["created_by"].(string); ok {
		secret.CreatedBy = cb
	}

	return secret, nil
}

// secretIntentPayload is used for JSON marshal/unmarshal of secret intent content.
type secretIntentPayload struct {
	ID               string `json:"id"`
	ServiceID        string `json:"service_id"`
	EnvironmentID    string `json:"environment_id,omitempty"`
	Name             string `json:"name"`
	Value            string `json:"value,omitempty"`           // plaintext from in-process MCP
	EncryptedValue   string `json:"encrypted_value,omitempty"` // NIP-44 encrypted (relay intent path)
	EncryptionMethod string `json:"encryption_method,omitempty"`
	Version          int    `json:"version,omitempty"`
}

// BuildSecretIntentContent builds an in-process secret intent for MCP callers.
func BuildSecretIntentContent(serviceID, secretID uuid.UUID, name, value string, envID *uuid.UUID, encryptionMethod domain.EncryptionMethod) map[string]interface{} {
	id := secretID
	if id == uuid.Nil {
		id = domain.NewEntityID()
	}
	content := map[string]interface{}{
		"id":                id.String(),
		"service_id":        serviceID.String(),
		"name":              name,
		"value":             value,
		"encryption_method": string(encryptionMethod),
	}
	if envID != nil {
		content["environment_id"] = envID.String()
	}
	return content
}

// ensureSecretIntentJSON marshals any to JSON and back into a map.
func ensureSecretIntentJSON(v any) map[string]interface{} {
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out map[string]interface{}
	_ = json.Unmarshal(data, &out)
	return out
}
