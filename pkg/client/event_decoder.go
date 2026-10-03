package client

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"

	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// ErrNotDecryptable is returned when a 30900 event's content is encrypted
// and the NostrClient has no signer capable of decrypting it.
type ErrNotDecryptable struct {
	Domain  string
	EventID string
}

func (e *ErrNotDecryptable) Error() string {
	return fmt.Sprintf("event %s in domain %q is encrypted and cannot be decrypted without a signer", e.EventID, e.Domain)
}

// DecodedEvent wraps a 30900 event with its decoded domain, family, and
// parsed content.
type DecodedEvent struct {
	Event      nostr.Event
	Domain     string
	Entity     string
	Topic      string
	LegacyKind int
	DTag       string
	Deleted    bool
	Content    json.RawMessage // the raw JSON content of the event
}

// DecodeControlStateEvent decodes a single 30900 event into its domain, entity,
// and raw content. It uses the exported CPStateFamilyTopics table to map
// legacy_kind → family, ensuring no hand-rolled topic duplication.
func DecodeControlStateEvent(ev nostr.Event) (*DecodedEvent, error) {
	if int(ev.Kind) != kinds.CASControlState {
		return nil, fmt.Errorf("event kind %d is not cp-state kind %d", ev.Kind, kinds.CASControlState)
	}

	// Extract tags.
	d := tagValue(ev.Tags, kinds.CASControlStateTagD)
	legacyKindStr := tagValue(ev.Tags, kinds.CASControlStateTagLegacyKind)
	deletedStr := tagValue(ev.Tags, kinds.CASControlStateTagDeleted)
	topic := tagValue(ev.Tags, "t")

	legacyKind, _ := strconv.Atoi(legacyKindStr)
	deleted := deletedStr == "true"

	// Resolve family from the canonical table.
	info := lookupFamily(legacyKind)

	decoded := &DecodedEvent{
		Event:      ev,
		Domain:     info.Domain,
		Entity:     info.Entity,
		Topic:      topic,
		LegacyKind: legacyKind,
		DTag:       d,
		Deleted:    deleted,
		Content:    json.RawMessage(ev.Content),
	}
	return decoded, nil
}

// DecodeService decodes a 30900 service-registry event into a domain.Service.
func DecodeService(ev nostr.Event) (*domain.Service, error) {
	decoded, err := DecodeControlStateEvent(ev)
	if err != nil {
		return nil, err
	}
	if decoded.LegacyKind != kinds.ServiceRegistry {
		return nil, fmt.Errorf("event legacy_kind %d is not service-registry (%d)", decoded.LegacyKind, kinds.ServiceRegistry)
	}
	if decoded.Deleted {
		return nil, nil // tombstone
	}
	var content serviceContent
	if err := json.Unmarshal(decoded.Content, &content); err != nil {
		return nil, fmt.Errorf("decode service content: %w", err)
	}
	svc := &domain.Service{
		ID:            parseUUID(content.ID),
		OrgID:         parseUUID(content.OrgID),
		Name:          content.Name,
		RepoURL:       content.RepoURL,
		ArtifactRepo:  content.ArtifactRepo,
		DefaultBranch: content.DefaultBranch,
		RuntimeType:   domain.RuntimeType(content.RuntimeType),
		RuntimeConfig: content.RuntimeConfig,
		CreatedAt:     parseTime(content.CreatedAt),
		UpdatedAt:     parseTime(content.UpdatedAt),
	}
	if content.Repository != nil {
		svc.Repository = content.Repository
	}
	return svc, nil
}

// DecodeEnvironment decodes a 30900 environment-registry event into a domain.Environment.
func DecodeEnvironment(ev nostr.Event) (*domain.Environment, error) {
	details, err := DecodeEnvironmentDetails(ev)
	if err != nil || details == nil {
		return nil, err
	}
	return &details.Environment, nil
}

// DecodeEnvironmentDetails decodes an environment registry record, including
// the deployment-unit read model carried by its canonical state event.
func DecodeEnvironmentDetails(ev nostr.Event) (*EnvironmentDetails, error) {
	decoded, err := DecodeControlStateEvent(ev)
	if err != nil {
		return nil, err
	}
	if decoded.LegacyKind != kinds.EnvironmentRegistry {
		return nil, fmt.Errorf("event legacy_kind %d is not environment-registry (%d)", decoded.LegacyKind, kinds.EnvironmentRegistry)
	}
	if decoded.Deleted {
		return nil, nil // tombstone
	}
	var content environmentContent
	if err := json.Unmarshal(decoded.Content, &content); err != nil {
		return nil, fmt.Errorf("decode environment content: %w", err)
	}
	env := domain.Environment{
		ID:                 parseUUID(content.ID),
		OrgID:              parseUUID(content.OrgID),
		Name:               content.Name,
		LoomWorkerSelector: content.LoomWorkerSelector,
		RuntimeConfig:      content.RuntimeConfig,
		Targeting:          content.Targeting,
		Protected:          content.Protected,
		CreatedAt:          parseTime(content.CreatedAt),
		UpdatedAt:          parseTime(content.UpdatedAt),
	}
	if content.DeployStrategy != "" {
		env.DeployStrategy = domain.DeployStrategy(content.DeployStrategy)
	}
	units := make([]domain.DeploymentUnit, 0, len(content.DeploymentUnits))
	for _, unit := range content.DeploymentUnits {
		if unit.Implicit {
			envCopy := env
			implicit, err := domain.NewImplicitDefaultDeploymentUnit(&envCopy)
			if err != nil {
				return nil, fmt.Errorf("decode implicit deployment unit: %w", err)
			}
			units = append(units, *implicit)
			continue
		}
		unit.EnvironmentID = env.ID
		units = append(units, unit)
	}
	return &EnvironmentDetails{Environment: env, DeploymentUnits: units}, nil
}

// serviceContent is the JSON shape of a service-registry 30900 content.
type serviceContent struct {
	Deleted       bool                         `json:"deleted"`
	ID            string                       `json:"id"`
	OrgID         string                       `json:"org_id,omitempty"`
	Name          string                       `json:"name"`
	RepoURL       string                       `json:"repo_url,omitempty"`
	Repository    *domain.RepositoryRef        `json:"repository,omitempty"`
	ArtifactRepo  string                       `json:"artifact_repo"`
	DefaultBranch string                       `json:"default_branch"`
	RuntimeType   string                       `json:"runtime_type"`
	RuntimeConfig *domain.ServiceRuntimeConfig `json:"runtime_config,omitempty"`
	CreatedAt     string                       `json:"created_at,omitempty"`
	UpdatedAt     string                       `json:"updated_at,omitempty"`
}

// environmentContent is the JSON shape of an environment-registry 30900 content.
type environmentContent struct {
	Deleted            bool                        `json:"deleted"`
	ID                 string                      `json:"id"`
	OrgID              string                      `json:"org_id,omitempty"`
	Name               string                      `json:"name"`
	LoomWorkerSelector map[string]any              `json:"loom_worker_selector"`
	RuntimeConfig      map[string]any              `json:"runtime_config"`
	Targeting          domain.EnvironmentTargeting `json:"targeting"`
	DeploymentUnits    []domain.DeploymentUnit     `json:"deployment_units"`
	Protected          bool                        `json:"protected"`
	DeployStrategy     string                      `json:"deploy_strategy,omitempty"`
	CreatedAt          string                      `json:"created_at,omitempty"`
	UpdatedAt          string                      `json:"updated_at,omitempty"`
}

// familyIndex is the lazily-built reverse index from legacy kind to family info.
var familyIndex map[int]nostrpool.CPStateFamilyInfo

func lookupFamily(legacyKind int) nostrpool.CPStateFamilyInfo {
	if familyIndex == nil {
		familyIndex = map[int]nostrpool.CPStateFamilyInfo{}
		for _, f := range nostrpool.CPStateFamilyTopics() {
			familyIndex[f.LegacyKind] = f
		}
	}
	if info, ok := familyIndex[legacyKind]; ok {
		return info
	}
	return nostrpool.CPStateFamilyInfo{LegacyKind: legacyKind}
}

func tagValue(tags nostr.Tags, key string) string {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == key {
			return tag[1]
		}
	}
	return ""
}

func parseUUID(s string) uuid.UUID {
	id, _ := uuid.Parse(s)
	return id
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	// Try RFC3339Nano first (what putRecordTime writes), then RFC3339.
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t, _ = time.Parse(time.RFC3339, s)
	}
	return t
}
