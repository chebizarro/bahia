package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/secrets"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/secretstore"
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

// SecretStoreIndex keeps the optional PostgreSQL index consistent with
// store-backed secrets: a value-less marker row with every retained legacy
// ciphertext scrubbed, so PostgreSQL consumers fail with
// domain.ErrSecretInStore instead of omitting the secret.
// repository.PgSecretRepository implements it.
type SecretStoreIndex interface {
	MarkStoreBacked(ctx context.Context, ref domain.SecretRef) error
}

// SecretRecordView reads canonical secret and service state from the
// daemon's local event store (never PostgreSQL).
type SecretRecordView interface {
	// CurrentSecretRef is the secret's current canonical record, or nil when
	// it has none or was deleted.
	CurrentSecretRef(ctx context.Context, secretID uuid.UUID) (*domain.SecretRef, error)
	// ServiceOrgID is the org of the service's canonical record.
	ServiceOrgID(ctx context.Context, serviceID uuid.UUID) (string, error)
}

// SecretIntentHandler processes kind-30900 intents for the "secret" domain.
// It is level-triggered: the intent's content is the full desired state, and
// the handler reconciles the entity toward it regardless of whether prior
// events for the coordinate have been seen.
//
// A reference-only intent names a credential in the external secret store
// (content credential_ref, docs/architecture/secret-store.md). The handler
// checks the credential with the store, then publishes the canonical record
// with the reference; no value passes through Bahia and PostgreSQL is not
// involved. When the store is configured, intents carrying a value
// (encrypted_value or value) are refused.
//
// Without a configured store the legacy path applies: the value in the intent
// content is NIP-44 encrypted to the daemon's service pubkey by the client;
// the handler seals it into PostgreSQL and publishes a canonical record
// containing ONLY the SecretRef metadata — never the plaintext value.
//
// Secret REVEAL stays ContextVM — this handler does not decrypt secrets.
//
// See docs/architecture/intents-and-authority.md.
type SecretIntentHandler struct {
	registry      SecretIntentCRUD
	encryptor     SecretIntentEncryptor
	versionedKey  *secrets.DataKey
	serviceKeyer  nostr.Keyer
	servicePubkey nostr.PubKey
	store         secretstore.SecretStore
	records       SecretRecordView
	storeIndex    SecretStoreIndex
	publisher     SecretIntentPublisher
	status        *IntentStatusPublisher
	logger        *zap.Logger
}

// SecretIntentHandlerConfig configures the secret intent handler. Store and
// Records enable reference-only intents; Registry (PostgreSQL) serves only
// the legacy value path.
type SecretIntentHandlerConfig struct {
	Registry      SecretIntentCRUD
	Encryptor     SecretIntentEncryptor
	VersionedKey  *secrets.DataKey
	ServiceKeyer  nostr.Keyer
	ServicePubkey nostr.PubKey
	Store         secretstore.SecretStore
	Records       SecretRecordView
	StoreIndex    SecretStoreIndex
	Publisher     SecretIntentPublisher
	Status        *IntentStatusPublisher
	Logger        *zap.Logger
}

// NewSecretIntentHandler constructs the handler.
func NewSecretIntentHandler(cfg SecretIntentHandlerConfig) *SecretIntentHandler {
	logger := cfg.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	return &SecretIntentHandler{
		registry:      cfg.Registry,
		encryptor:     cfg.Encryptor,
		versionedKey:  cfg.VersionedKey,
		serviceKeyer:  cfg.ServiceKeyer,
		servicePubkey: cfg.ServicePubkey,
		store:         cfg.Store,
		records:       cfg.Records,
		storeIndex:    cfg.StoreIndex,
		publisher:     cfg.Publisher,
		status:        cfg.Status,
		logger:        logger.Named("secret-intent"),
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
	credentialRef, err := credentialRefFromIntent(intent)
	if err != nil {
		return err
	}
	_, hasCiphertext := intent.Content["encrypted_value"]
	_, hasPlaintext := intent.Content["value"]
	if credentialRef != nil {
		if hasCiphertext || hasPlaintext {
			return fmt.Errorf("a secret intent carries either credential_ref or a value, not both")
		}
		return h.reconcileReference(ctx, intent, *credentialRef)
	}
	if h.store != nil {
		return fmt.Errorf("secret values are not accepted in intents while the secret store is configured: import the value into the store and send credential_ref")
	}
	if h.registry == nil {
		return fmt.Errorf("secret value intents need the PostgreSQL secret store; configure secret_store and send credential_ref instead")
	}
	parsed, err := secretFromIntentContent(intent)
	if err != nil {
		return fmt.Errorf("parse secret intent content: %w", err)
	}

	// Level-triggered: try to load existing.
	existing, err := h.registry.GetByID(ctx, parsed.ID)
	if err != nil {
		return fmt.Errorf("look up secret before writing: %w", err)
	}
	targetVersion := 1
	if existing != nil {
		if existing.Version <= 0 || existing.Version == int(^uint(0)>>1) {
			return fmt.Errorf("current secret version is invalid")
		}
		targetVersion = existing.Version + 1
	}
	if h.versionedKey != nil {
		if err := h.sealVersionedIntentValue(ctx, intent, parsed, targetVersion); err != nil {
			return err
		}
	} else if len(parsed.EncryptedValue) == 0 {
		// Legacy local mode encrypts plaintext supplied by an in-process caller.
		if plaintext, ok := intent.Content["value"].(string); ok && plaintext != "" {
			if h.encryptor == nil {
				return fmt.Errorf("secret value encryption is not configured")
			}
			encrypted, encErr := h.encryptor.Encrypt(plaintext, parsed.EncryptionMethod)
			if encErr != nil {
				return fmt.Errorf("failed to encrypt secret value: %w", encErr)
			}
			parsed.EncryptedValue = encrypted
		} else {
			return fmt.Errorf("secret value is required (either encrypted_value or value)")
		}
	}

	if existing == nil {
		if intent.ExpectedUpdatedAt != nil {
			return &revisionConflictError{entityType: "secret", entityID: parsed.ID, expected: *intent.ExpectedUpdatedAt}
		}
		// Create.
		if h.versionedKey != nil || parsed.Version == 0 {
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

// reconcileReference applies a reference-only intent. The credential must
// exist in the store, be active and be at exactly the pinned version; the
// canonical record (the only home of the secret's metadata) is then
// published with the reference. The record is control-plane truth, so a
// failed publish fails the intent.
func (h *SecretIntentHandler) reconcileReference(ctx context.Context, intent *Intent, ref domain.SecretCredentialRef) error {
	if h.store == nil {
		return fmt.Errorf("%w: reference-only secret intents need secret_store.backend", secretstore.ErrNotConfigured)
	}
	if h.records == nil || h.publisher == nil {
		return fmt.Errorf("canonical secret state is not configured")
	}
	desired, err := secretFromIntentContent(intent)
	if err != nil {
		return fmt.Errorf("parse secret intent content: %w", err)
	}
	existing, err := h.records.CurrentSecretRef(ctx, desired.ID)
	if err != nil {
		return fmt.Errorf("read canonical secret %s: %w", desired.ID, err)
	}
	now := time.Now().UTC()
	var next domain.SecretRef
	if existing == nil {
		if intent.ExpectedUpdatedAt != nil {
			return &revisionConflictError{entityType: "secret", entityID: desired.ID, expected: *intent.ExpectedUpdatedAt}
		}
		if desired.ServiceID == uuid.Nil || desired.Name == "" {
			return fmt.Errorf("a new secret needs service_id and name")
		}
		next = domain.SecretRef{ID: desired.ID, ServiceID: desired.ServiceID, EnvironmentID: desired.EnvironmentID, Name: desired.Name,
			Version: 1, CreatedBy: intent.Actor, CreatedAt: now, UpdatedAt: now}
	} else {
		if intent.ExpectedUpdatedAt != nil && !intent.RevisionMatches(existing.UpdatedAt) {
			return &revisionConflictError{entityType: "secret", entityID: desired.ID, expected: *intent.ExpectedUpdatedAt, actual: existing.UpdatedAt}
		}
		if desired.ServiceID != uuid.Nil && desired.ServiceID != existing.ServiceID {
			return fmt.Errorf("secret %s belongs to service %s; service_id cannot change", desired.ID, existing.ServiceID)
		}
		if existing.Version <= 0 || existing.Version == int(^uint(0)>>1) {
			return fmt.Errorf("current secret version is invalid")
		}
		next = *existing
		next.Version = existing.Version + 1
		next.UpdatedAt = now
		if desired.Name != "" {
			next.Name = desired.Name
		}
	}
	// The record is encrypted for the service's org; the intent's org (which
	// authorized it) must be that org.
	orgID, err := h.records.ServiceOrgID(ctx, next.ServiceID)
	if err != nil {
		return fmt.Errorf("resolve org for secret %s: %w", next.ID, err)
	}
	if orgID != intent.OrgID.String() {
		return fmt.Errorf("service %s does not belong to org %s", next.ServiceID, intent.OrgID)
	}
	// The credential must be this org's and this service's: the store holds
	// every tenant's credentials under one owner and checks the label
	// namespace. Errors name no credential id: status events are public.
	scope := secretstore.Scope{OrgID: intent.OrgID, ServiceID: next.ServiceID}
	meta, err := h.store.Inspect(ctx, ref.CredentialID, scope)
	if err != nil {
		return fmt.Errorf("check credential: %w", err)
	}
	switch {
	case meta.Status == secretstore.StatusRevoked:
		return fmt.Errorf("check credential: %w", secretstore.ErrRevoked)
	case meta.Status == secretstore.StatusExpired:
		return fmt.Errorf("check credential: %w", secretstore.ErrExpired)
	case meta.Version != ref.Version:
		return fmt.Errorf("check credential: %w: active version is %d, the intent pins %d", secretstore.ErrVersionMismatch, meta.Version, ref.Version)
	}
	next.EncryptionMethod = ""
	next.CredentialRef = &ref
	if h.registry != nil {
		// With PostgreSQL the index row becomes a value-less marker and any
		// legacy ciphertext for this id is scrubbed, whatever the canonical
		// record says. It goes first: if the publish below fails, the secret
		// is unreadable until the intent is retried, never readable with a
		// value that passed through a compromised path.
		if h.storeIndex == nil {
			return fmt.Errorf("the PostgreSQL secret index cannot record store-backed secrets")
		}
		if err := h.storeIndex.MarkStoreBacked(ctx, next); err != nil {
			return fmt.Errorf("index secret %s as store-backed: %w", next.ID, err)
		}
	}
	if err := h.publisher.PublishSecretRef(ctx, next); err != nil {
		return fmt.Errorf("publish canonical secret %s: %w", next.ID, err)
	}
	h.logger.Info("secret reference applied via intent",
		zap.String("secret_id", next.ID.String()),
		zap.Int("credential_version", ref.Version),
		zap.String("intent_id", intent.IntentID),
	)
	return nil
}

// credentialRefFromIntent parses content credential_ref, or returns nil
// when the intent has none.
func credentialRefFromIntent(intent *Intent) (*domain.SecretCredentialRef, error) {
	raw, ok := intent.Content["credential_ref"]
	if !ok {
		return nil, nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("credential_ref: %w", err)
	}
	var ref domain.SecretCredentialRef
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ref); err != nil {
		return nil, fmt.Errorf("credential_ref must be {store, credential_id, version}: %w", err)
	}
	if err := secretstore.ValidateRef(ref); err != nil {
		return nil, fmt.Errorf("credential_ref: %w", err)
	}
	return &ref, nil
}

func (h *SecretIntentHandler) sealVersionedIntentValue(ctx context.Context, intent *Intent, parsed *domain.ServiceSecret, version int) error {
	if h.serviceKeyer == nil || h.servicePubkey == nostr.ZeroPK {
		return fmt.Errorf("versioned service-secret writer is not configured")
	}
	actual, err := h.serviceKeyer.GetPublicKey(ctx)
	if err != nil || actual != h.servicePubkey {
		return fmt.Errorf("fenced service key is unavailable or changed")
	}
	value, hasPlaintext := intent.Content["value"].(string)
	hasCiphertext := len(parsed.EncryptedValue) > 0
	if hasCiphertext == hasPlaintext || hasPlaintext && value == "" {
		return fmt.Errorf("exactly one nonempty secret value representation is required")
	}
	var plaintext string
	if hasPlaintext {
		if parsed.EncryptionMethod != domain.EncryptionNIP44 && parsed.EncryptionMethod != domain.EncryptionAES256 {
			return fmt.Errorf("unsupported plaintext secret input method")
		}
		plaintext = value
	} else {
		if parsed.EncryptionMethod != domain.EncryptionNIP44 {
			return fmt.Errorf("encrypted secret input must be NIP-44 to the existing service pubkey")
		}
		plaintext, err = h.serviceKeyer.Decrypt(ctx, string(parsed.EncryptedValue), h.servicePubkey)
		if err != nil || plaintext == "" {
			return fmt.Errorf("fenced client secret decrypt failed")
		}
	}
	sealed, err := h.versionedKey.Seal(parsed.ID, version, []byte(plaintext))
	if err != nil {
		return fmt.Errorf("versioned service-secret seal failed")
	}
	actual, err = h.serviceKeyer.GetPublicKey(ctx)
	if err != nil || actual != h.servicePubkey {
		return fmt.Errorf("fenced service key became unavailable during secret seal")
	}
	parsed.EncryptedValue = sealed
	parsed.EncryptionMethod = domain.EncryptionAES256V2
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

	if h.registry != nil {
		if err := h.registry.Delete(ctx, secretID); err != nil {
			return fmt.Errorf("delete secret: %w", err)
		}
	}

	// Publish tombstone. With the secret store configured the canonical
	// record is the secret's only home, so the deletion must reach it.
	if h.publisher != nil {
		if err := h.publisher.PublishSecretDeleted(ctx, secretID); err != nil {
			if h.store != nil || h.registry == nil {
				return fmt.Errorf("publish secret tombstone: %w", err)
			}
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

// BuildSecretReferenceIntentContent builds a reference-only secret intent:
// the value lives in the secret store and the intent names its credential.
func BuildSecretReferenceIntentContent(serviceID, secretID uuid.UUID, name string, envID *uuid.UUID, ref domain.SecretCredentialRef) map[string]interface{} {
	id := secretID
	if id == uuid.Nil {
		id = domain.NewEntityID()
	}
	content := map[string]interface{}{
		"id":         id.String(),
		"service_id": serviceID.String(),
		"name":       name,
		"credential_ref": map[string]interface{}{
			"store":         ref.Store,
			"credential_id": ref.CredentialID,
			"version":       ref.Version,
		},
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
