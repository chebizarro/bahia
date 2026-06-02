package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	gonostr "github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"go.uber.org/zap"
)

const relayFirstExtendedKind = kinds.CASControlState

type relayFirstExtendedBase struct {
	publisher RelayFirstPublisher
	signer    RelayFirstSigner
	logger    *zap.Logger
	family    string
}

func newRelayFirstExtendedBase(family string, publisher RelayFirstPublisher, signer RelayFirstSigner, logger *zap.Logger) relayFirstExtendedBase {
	if logger == nil {
		logger = zap.NewNop()
	}
	return relayFirstExtendedBase{publisher: publisher, signer: signer, logger: logger, family: family}
}

func (b relayFirstExtendedBase) publish(ctx context.Context, dTag, domain, entity string, tags gonostr.Tags, payload any, label string) error {
	if b.publisher == nil {
		return fmt.Errorf("nostr %s publisher is not configured", b.family)
	}
	if b.signer == nil {
		return fmt.Errorf("nostr %s signer is not configured", b.family)
	}
	dTag = strings.TrimSpace(dTag)
	if dTag != "" {
		tags = append(relayFirstCanonicalStateTags(domain, entity, dTag, false), tags...)
	} else {
		tags = append(gonostr.Tags{{"domain", domain}, {"entity", entity}, {"schema", relayFirstStateSchema}, {"deleted", "false"}}, tags...)
	}
	content, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode %s event: %w", label, err)
	}
	ev := gonostr.Event{Kind: relayFirstExtendedKind, CreatedAt: gonostr.Now(), Tags: tags, Content: string(content)}
	if err := b.signer.Sign(ctx, &ev); err != nil {
		return fmt.Errorf("sign %s event: %w", label, err)
	}
	published, err := b.publisher.Publish(ctx, ev)
	if err != nil {
		return fmt.Errorf("publish %s event: %w", label, err)
	}
	if published == 0 {
		return fmt.Errorf("publish %s event: no relay accepted the event", label)
	}
	b.logger.Debug("relay-first extended event published", zap.String("family", b.family), zap.Int("kind", relayFirstExtendedKind), zap.String("event_id", ev.ID), zap.Int("relays", published))
	return nil
}

type relayFirstLLMDelegate interface {
	CreateRoute(ctx context.Context, route *domain.LLMRoute) error
	CreateDeploymentIntent(ctx context.Context, intent *domain.LLMDeploymentIntent) error
	RollbackWithMetadata(ctx context.Context, routeID, envID uuid.UUID, requestedBy string, metadata map[string]any) (*domain.LLMDeploymentIntent, error)
}

// RelayFirstLLM publishes canonical LLM command events before delegating local cache writes.
type RelayFirstLLM struct {
	delegate relayFirstLLMDelegate
	base     relayFirstExtendedBase
}

func NewRelayFirstLLM(delegate relayFirstLLMDelegate, publisher RelayFirstPublisher, signer RelayFirstSigner, logger *zap.Logger) *RelayFirstLLM {
	return &RelayFirstLLM{delegate: delegate, base: newRelayFirstExtendedBase("llm", publisher, signer, logger)}
}

func (r *RelayFirstLLM) CreateRoute(ctx context.Context, route *domain.LLMRoute) error {
	if r.delegate == nil {
		return fmt.Errorf("LLM delegate is not configured")
	}
	if route == nil {
		return fmt.Errorf("LLM route is required")
	}
	if strings.TrimSpace(route.Name) == "" {
		return fmt.Errorf("LLM route name is required")
	}
	if err := r.base.publish(ctx, route.ID.String(), "llm", "route", gonostr.Tags{{"route", route.ID.String()}, {"name", strings.TrimSpace(route.Name)}}, route, "llm route create"); err != nil {
		return err
	}
	return r.delegate.CreateRoute(ctx, route)
}

func (r *RelayFirstLLM) CreateDeploymentIntent(ctx context.Context, intent *domain.LLMDeploymentIntent) error {
	if r.delegate == nil {
		return fmt.Errorf("LLM delegate is not configured")
	}
	if intent == nil {
		return fmt.Errorf("LLM deployment intent is required")
	}
	if strings.TrimSpace(intent.RequestedBy) == "" {
		return fmt.Errorf("requested_by must not be empty")
	}
	tags := gonostr.Tags{{"route", intent.RouteID.String()}, {"environment", intent.EnvironmentID.String()}, {"release", intent.ReleaseID.String()}}
	if err := r.base.publish(ctx, intent.ID.String(), "llm", "deployment_intent", tags, intent, "llm deployment intent"); err != nil {
		return err
	}
	return r.delegate.CreateDeploymentIntent(ctx, intent)
}

func (r *RelayFirstLLM) RollbackWithMetadata(ctx context.Context, routeID, envID uuid.UUID, requestedBy string, metadata map[string]any) (*domain.LLMDeploymentIntent, error) {
	if r.delegate == nil {
		return nil, fmt.Errorf("LLM delegate is not configured")
	}
	if routeID == uuid.Nil || envID == uuid.Nil {
		return nil, fmt.Errorf("route_id and environment_id are required")
	}
	if strings.TrimSpace(requestedBy) == "" {
		return nil, fmt.Errorf("requested_by must not be empty")
	}
	payload := map[string]any{"route_id": routeID.String(), "environment_id": envID.String(), "requested_by": strings.TrimSpace(requestedBy), "metadata": metadata}
	tags := gonostr.Tags{{"route", routeID.String()}, {"environment", envID.String()}}
	if err := r.base.publish(ctx, fmt.Sprintf("%s:%s:%s", routeID, envID, requestedBy), "llm", "rollback_intent", tags, payload, "llm rollback intent"); err != nil {
		return nil, err
	}
	return r.delegate.RollbackWithMetadata(ctx, routeID, envID, requestedBy, metadata)
}

func (r *RelayFirstLLM) Rollback(ctx context.Context, routeID, envID uuid.UUID, requestedBy string) (*domain.LLMDeploymentIntent, error) {
	return r.RollbackWithMetadata(ctx, routeID, envID, requestedBy, nil)
}

type relayFirstBackupDelegate interface {
	CreateOrUpdateRecipe(ctx context.Context, recipe *domain.BackupRecipe) error
}

// RelayFirstBackup currently gates recipe mutations with a canonical event; other backup operations remain delegate-only until their codec paths are reusable outside the projector.
type RelayFirstBackup struct {
	delegate relayFirstBackupDelegate
	base     relayFirstExtendedBase
}

func NewRelayFirstBackup(delegate relayFirstBackupDelegate, publisher RelayFirstPublisher, signer RelayFirstSigner, logger *zap.Logger) *RelayFirstBackup {
	return &RelayFirstBackup{delegate: delegate, base: newRelayFirstExtendedBase("backup", publisher, signer, logger)}
}

func (r *RelayFirstBackup) CreateOrUpdateRecipe(ctx context.Context, recipe *domain.BackupRecipe) error {
	if r.delegate == nil {
		return fmt.Errorf("backup delegate is not configured")
	}
	if recipe == nil {
		return fmt.Errorf("backup recipe is required")
	}
	dTag := "backup-recipe:" + recipe.Name + ":" + recipe.Version
	if err := r.base.publish(ctx, dTag, "backup", "recipe", gonostr.Tags{{"recipe", dTag}, {"recipe_id", recipe.ID.String()}, {"name", recipe.Name}}, recipe, "backup recipe registry"); err != nil {
		return err
	}
	return r.delegate.CreateOrUpdateRecipe(ctx, recipe)
}

type relayFirstMLDelegate interface {
	CreateOrUpdateModel(ctx context.Context, model *domain.MLModel) error
}

// RelayFirstML is a pass-through wrapper until ML command/result codecs are extracted from the projector and control-plane handlers.
type RelayFirstML struct{ delegate relayFirstMLDelegate }

func NewRelayFirstML(delegate relayFirstMLDelegate, _ RelayFirstPublisher, _ RelayFirstSigner, _ *zap.Logger) *RelayFirstML {
	return &RelayFirstML{delegate: delegate}
}

func (r *RelayFirstML) CreateOrUpdateModel(ctx context.Context, model *domain.MLModel) error {
	if r.delegate == nil {
		return fmt.Errorf("ML delegate is not configured")
	}
	return r.delegate.CreateOrUpdateModel(ctx, model)
}

type relayFirstPackageDelegate interface {
	EnsureRepository(ctx context.Context, repo *domain.PackageRepository, existing *domain.PackageRepository) (*domain.PackageRepository, error)
}

// RelayFirstPackage gates package repository registration with a canonical event before local/backend mutation.
type RelayFirstPackage struct {
	delegate relayFirstPackageDelegate
	base     relayFirstExtendedBase
}

func NewRelayFirstPackage(delegate relayFirstPackageDelegate, publisher RelayFirstPublisher, signer RelayFirstSigner, logger *zap.Logger) *RelayFirstPackage {
	return &RelayFirstPackage{delegate: delegate, base: newRelayFirstExtendedBase("package", publisher, signer, logger)}
}

func (r *RelayFirstPackage) EnsureRepository(ctx context.Context, repo *domain.PackageRepository, existing *domain.PackageRepository) (*domain.PackageRepository, error) {
	if r.delegate == nil {
		return nil, fmt.Errorf("package delegate is not configured")
	}
	if repo == nil {
		return nil, fmt.Errorf("package repository is required")
	}
	dTag := "package-repository:" + repo.Name
	if repo.ID != uuid.Nil {
		dTag = "package-repository:" + repo.ID.String()
	}
	if err := r.base.publish(ctx, dTag, "package", "repository", gonostr.Tags{{"repository", dTag}, {"name", repo.Name}, {"format", string(repo.Format)}}, repo, "package repository state"); err != nil {
		return nil, err
	}
	return r.delegate.EnsureRepository(ctx, repo, existing)
}

type relayFirstDNSDelegate interface {
	CreateZone(ctx context.Context, zone domain.DNSZone) error
	CreateOverride(ctx context.Context, override domain.DNSRecordOverride) error
}

// RelayFirstDNS gates durable DNS zone and override mutations before delegating persistence.
type RelayFirstDNS struct {
	delegate relayFirstDNSDelegate
	base     relayFirstExtendedBase
}

func NewRelayFirstDNS(delegate relayFirstDNSDelegate, publisher RelayFirstPublisher, signer RelayFirstSigner, logger *zap.Logger) *RelayFirstDNS {
	return &RelayFirstDNS{delegate: delegate, base: newRelayFirstExtendedBase("dns", publisher, signer, logger)}
}

func (r *RelayFirstDNS) CreateZone(ctx context.Context, zone domain.DNSZone) error {
	if r.delegate == nil {
		return fmt.Errorf("DNS delegate is not configured")
	}
	if strings.TrimSpace(zone.Name) == "" {
		return fmt.Errorf("DNS zone name is required")
	}
	if err := r.base.publish(ctx, "dns-zone:"+zone.Name, "dns", "zone", gonostr.Tags{{"zone", zone.Name}, {"backend", zone.BackendRef}}, zone, "dns zone state"); err != nil {
		return err
	}
	return r.delegate.CreateZone(ctx, zone)
}

func (r *RelayFirstDNS) CreateOverride(ctx context.Context, override domain.DNSRecordOverride) error {
	if r.delegate == nil {
		return fmt.Errorf("DNS delegate is not configured")
	}
	if err := domain.ValidateDNSRecordOverride(&override); err != nil {
		return err
	}
	dTag := fmt.Sprintf("dns-override:%s:%s:%s", override.ZoneName, override.RecordName, override.RecordType)
	if override.ID != uuid.Nil {
		dTag = "dns-override:" + override.ID.String()
	}
	tags := gonostr.Tags{{"zone", override.ZoneName}, {"record", override.RecordName}, {"type", string(override.RecordType)}}
	if err := r.base.publish(ctx, dTag, "dns", "record_override", tags, override, "dns record override state"); err != nil {
		return err
	}
	return r.delegate.CreateOverride(ctx, override)
}
