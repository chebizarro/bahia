package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
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

// SecretIntentCRUD is what the secret intent handler needs from the optional
// PostgreSQL index beyond SecretStoreIndex: deleting a secret's row.
// repository.SecretRepository satisfies it.
type SecretIntentCRUD interface {
	Delete(ctx context.Context, id uuid.UUID) error
}

// SecretStoreIndex keeps the optional PostgreSQL index consistent with
// store-backed secrets: a value-less marker row with every retained legacy
// ciphertext scrubbed, so PostgreSQL consumers list the secret and resolve
// it through the store, and never serve a legacy value under its name.
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
// A secret intent names a credential in the external secret store (content
// credential_ref, docs/architecture/secret-store.md); it never carries a
// value, and an intent carrying encrypted_value or value is refused. The
// handler checks the credential with the store, then publishes the canonical
// record with the reference; no value passes through Bahia. Without a
// configured store every create and update is refused
// (secretstore.ErrNotConfigured).
//
// Secret REVEAL stays ContextVM — this handler resolves no value.
//
// See docs/architecture/intents-and-authority.md.
type SecretIntentHandler struct {
	registry   SecretIntentCRUD
	store      secretstore.SecretStore
	records    SecretRecordView
	storeIndex SecretStoreIndex
	publisher  SecretIntentPublisher
	status     *IntentStatusPublisher
	logger     *zap.Logger
}

// SecretIntentHandlerConfig configures the secret intent handler. Store and
// Records apply secret intents; Registry (PostgreSQL, optional) is the
// derived index kept consistent with them.
type SecretIntentHandlerConfig struct {
	Registry   SecretIntentCRUD
	Store      secretstore.SecretStore
	Records    SecretRecordView
	StoreIndex SecretStoreIndex
	Publisher  SecretIntentPublisher
	Status     *IntentStatusPublisher
	Logger     *zap.Logger
}

// NewSecretIntentHandler constructs the handler.
func NewSecretIntentHandler(cfg SecretIntentHandlerConfig) *SecretIntentHandler {
	logger := cfg.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	return &SecretIntentHandler{
		registry:   cfg.Registry,
		store:      cfg.Store,
		records:    cfg.Records,
		storeIndex: cfg.StoreIndex,
		publisher:  cfg.Publisher,
		status:     cfg.Status,
		logger:     logger.Named("secret-intent"),
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
	_, hasCiphertext := intent.Content["encrypted_value"]
	_, hasPlaintext := intent.Content["value"]
	if hasCiphertext || hasPlaintext {
		return fmt.Errorf("secret intents never carry a value: import it into the secret store labelled <org-id>/<service-id>/<name> and send credential_ref")
	}
	credentialRef, err := credentialRefFromIntent(intent)
	if err != nil {
		return err
	}
	if credentialRef == nil {
		return fmt.Errorf("a secret intent needs credential_ref {store, credential_id, version}")
	}
	return h.reconcileReference(ctx, intent, *credentialRef)
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
		return publicCredentialError(h.logger, err)
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
			if errors.Is(err, repository.ErrSecretNameTaken) {
				return fmt.Errorf("secret %s: %w: re-point that secret instead (bahia secrets set reuses its id) or delete it first", next.ID, err)
			}
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

// errCredentialNotAvailable is the one public reason for a credential the
// intent may not use: missing, another tenant's, or refused to this daemon.
// Intent status is a relay record, so it never tells those apart (that would
// let one tenant probe another's credential ids); the detail goes to the
// debug log.
var errCredentialNotAvailable = errors.New("credential is not available to this org and service")

func publicCredentialError(logger *zap.Logger, err error) error {
	if errors.Is(err, secretstore.ErrNotFound) || errors.Is(err, secretstore.ErrDenied) {
		logger.Debug("credential refused", zap.Error(err))
		return fmt.Errorf("check credential: %w", errCredentialNotAvailable)
	}
	return fmt.Errorf("check credential: %w", err)
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

	// The canonical record is the secret's only home, so the deletion must
	// reach it.
	if h.publisher == nil {
		return fmt.Errorf("canonical secret state is not configured")
	}
	if err := h.publisher.PublishSecretDeleted(ctx, secretID); err != nil {
		return fmt.Errorf("publish secret tombstone: %w", err)
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

	secret := &domain.ServiceSecret{}

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
