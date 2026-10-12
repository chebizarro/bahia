package controlplane

import (
	"context"
	"fmt"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

// A pipeline-policy mutation must match either the signed NIP-01 event or
// the NIP-59 rumor authenticated by the gift-wrap ingress's signed seal.
func validateHiveCIPolicySignedEnvelope(intent *Intent) error {
	if intent == nil || intent.Event == nil || intent.Actor != intent.Event.PubKey.Hex() ||
		(!intent.authenticatedByIngress && (!intent.Event.CheckID() || !intent.Event.VerifySignature())) {
		return fmt.Errorf("Hive-CI pipeline policy operator signature is missing or invalid")
	}
	parsed, err := ParseIntent(intent.Event)
	if err != nil || parsed.Domain != "hiveci-policy" || parsed.Op != intent.Op || parsed.Schema != intent.Schema ||
		parsed.Coordinate != intent.Coordinate || parsed.IntentID != intent.IntentID || parsed.OrgID != intent.OrgID ||
		intentContentHash(parsed.Content) != intentContentHash(intent.Content) {
		return fmt.Errorf("Hive-CI pipeline policy envelope differs from signed intent")
	}
	for _, field := range []string{"d", "domain", "schema", "op", "intent_id"} {
		if !hiveCIPolicyTagExactlyOnce(intent.Event.Tags, field, signedHiveCIPolicyTagValue(intent, field)) {
			return fmt.Errorf("Hive-CI pipeline policy envelope has duplicate or mismatched %s tag", field)
		}
	}
	return nil
}

func hiveCIPolicyTagExactlyOnce(tags nostr.Tags, key, value string) bool {
	count := 0
	for _, tag := range tags {
		if len(tag) > 0 && tag[0] == key {
			if len(tag) < 2 || tag[1] != value {
				return false
			}
			count++
		}
	}
	return count == 1
}

func signedHiveCIPolicyTagValue(intent *Intent, field string) string {
	switch field {
	case "d":
		return intent.Coordinate
	case "domain":
		return intent.Domain
	case "schema":
		return intent.Schema
	case "op":
		return intent.Op
	case "intent_id":
		return intent.IntentID
	default:
		return ""
	}
}

// HiveCIPolicyStore reads and writes relay-canonical pipeline policies. Its
// implementation publishes through the daemon outbox before mirroring SQL.
type HiveCIPolicyStore interface {
	ListPolicies(context.Context) ([]domain.HiveCIPipelinePolicy, error)
	EnsurePipelinePolicy(context.Context, domain.HiveCIPipelinePolicy) error
	DeletePipelinePolicy(context.Context, uuid.UUID) error
}

type HiveCIPolicyIntentHandler struct{ store HiveCIPolicyStore }

func NewHiveCIPolicyIntentHandler(store HiveCIPolicyStore) *HiveCIPolicyIntentHandler {
	return &HiveCIPolicyIntentHandler{store: store}
}

func (*HiveCIPolicyIntentHandler) IsFleetScoped() bool { return true }
func (*HiveCIPolicyIntentHandler) PermissionFor(string) domain.Permission {
	return domain.PermWritePolicies
}

// HandleIntent reconciles a complete desired state; op is not a precondition
// on whether the daemon observed an earlier create.
func (h *HiveCIPolicyIntentHandler) HandleIntent(ctx context.Context, intent *Intent) error {
	if err := validateHiveCIPolicySignedEnvelope(intent); err != nil {
		return err
	}
	if intent == nil || intent.Content == nil {
		return fmt.Errorf("Hive-CI pipeline policy intent content is required")
	}
	if h == nil || h.store == nil {
		return fmt.Errorf("Hive-CI pipeline policy canonical store is unavailable")
	}
	if intent.Schema != "bahia.intent.hiveci-policy.v1" {
		return fmt.Errorf("unsupported Hive-CI pipeline policy intent schema %q", intent.Schema)
	}
	idText, ok := intent.Content["id"].(string)
	if !ok {
		return fmt.Errorf("Hive-CI pipeline policy id must be a UUIDv7 string")
	}
	id, err := uuid.Parse(idText)
	if err != nil || id.Version() != 7 || id.String() != idText {
		return fmt.Errorf("Hive-CI pipeline policy id must be a canonical UUIDv7")
	}
	if intent.Coordinate != "hiveci:policy:"+idText {
		return fmt.Errorf("Hive-CI pipeline policy coordinate must match id")
	}
	attemptID, err := uuid.Parse(intent.IntentID)
	if err != nil || attemptID.Version() != 7 || attemptID.String() != intent.IntentID {
		return fmt.Errorf("Hive-CI pipeline policy intent_id must be a canonical UUIDv7")
	}
	if intent.Content["intent_id"] != intent.IntentID {
		return fmt.Errorf("content intent_id must match intent_id tag")
	}
	if intent.Op != "create" && intent.Op != "update" && intent.Op != "delete" {
		return fmt.Errorf("unsupported Hive-CI pipeline policy operation %q", intent.Op)
	}
	allowed := map[string]bool{"id": true, "intent_id": true, "expected_updated_at": true}
	if intent.Op == "delete" {
		allowed["deleted"] = true
	} else {
		for _, field := range []string{"repo_coordinate", "workflow_path", "branch_pattern", "service_id", "environment_id", "enabled", "metadata"} {
			allowed[field] = true
		}
	}
	for field := range intent.Content {
		if !allowed[field] {
			return fmt.Errorf("unexpected Hive-CI pipeline policy field %q", field)
		}
	}
	policies, err := h.store.ListPolicies(ctx)
	if err != nil {
		return fmt.Errorf("read canonical Hive-CI pipeline policies: %w", err)
	}
	var existing *domain.HiveCIPipelinePolicy
	for i := range policies {
		if policies[i].ID == id {
			existing = &policies[i]
			break
		}
	}
	if intent.ExpectedUpdatedAt != nil {
		actual := time.Time{}
		if existing != nil {
			actual = existing.UpdatedAt
		}
		if existing == nil || !intent.RevisionMatches(actual) {
			return &revisionConflictError{entityType: "Hive-CI pipeline policy", entityID: id, expected: *intent.ExpectedUpdatedAt, actual: actual}
		}
	}
	if intent.Op == "delete" {
		if intent.Content["deleted"] != true {
			return fmt.Errorf("delete intent must contain deleted=true")
		}
		return h.store.DeletePipelinePolicy(ctx, id)
	}
	stringField := func(name string) (string, error) {
		value, ok := intent.Content[name].(string)
		if !ok || strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
			return "", fmt.Errorf("%s must be a nonempty trimmed string", name)
		}
		return value, nil
	}
	repo, err := stringField("repo_coordinate")
	if err != nil {
		return err
	}
	workflow, err := stringField("workflow_path")
	if err != nil {
		return err
	}
	parseID := func(name string) (uuid.UUID, error) {
		value, err := stringField(name)
		if err != nil {
			return uuid.Nil, err
		}
		parsed, err := uuid.Parse(value)
		if err != nil || parsed == uuid.Nil || parsed.String() != value {
			return uuid.Nil, fmt.Errorf("%s must be a canonical non-nil UUID", name)
		}
		return parsed, nil
	}
	serviceID, err := parseID("service_id")
	if err != nil {
		return err
	}
	environmentID, err := parseID("environment_id")
	if err != nil {
		return err
	}
	enabled, ok := intent.Content["enabled"].(bool)
	if !ok {
		return fmt.Errorf("enabled must be a boolean")
	}
	metadata, ok := intent.Content["metadata"].(map[string]any)
	if !ok {
		return fmt.Errorf("metadata must be a JSON object")
	}
	branch, _ := intent.Content["branch_pattern"].(string)
	if raw, exists := intent.Content["branch_pattern"]; exists {
		if _, ok := raw.(string); !ok {
			return fmt.Errorf("branch_pattern must be a string")
		}
	}
	policy := domain.HiveCIPipelinePolicy{ID: id, RepoCoordinate: repo, WorkflowPath: workflow,
		BranchPattern: branch, ServiceID: serviceID, EnvironmentID: environmentID,
		Enabled: enabled, Metadata: metadata}
	return h.store.EnsurePipelinePolicy(ctx, policy)
}
