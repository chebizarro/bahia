package soulfactory

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/domain"
)

const (
	SoulFactoryFleetConfigSchema     = "soulfactory-fleet-config/v1"
	SoulFactoryFleetConfigIdentifier = SoulFactoryFleetConfigSchema
	maxFleetConfigContentBytes       = 256 * 1024
)

// FleetConfigDefaults replaces wrapper environment defaults when present.
// RequiredPlugins uses plugin-id=install-source entries so external plugins
// remain reproducibly installable inside a dedicated runtime.
type FleetConfigDefaults struct {
	Model           string   `json:"model,omitempty"`
	Bindings        []string `json:"bindings,omitempty"`
	RequiredPlugins []string `json:"required_plugins,omitempty"`
}

// FleetConfigDocument is the signed content of kind 31953. Template contains
// OpenClaw's openclaw.json object and may use ${VAR} placeholders.
type FleetConfigDocument struct {
	Schema   string                 `json:"schema"`
	Template map[string]interface{} `json:"template"`
	Defaults FleetConfigDefaults    `json:"defaults,omitempty"`
}

// FleetConfigSnapshot pins the exact trusted replaceable event used by one
// provisioning run.
type FleetConfigSnapshot struct {
	Coordinate string              `json:"coordinate"`
	EventID    string              `json:"event_id"`
	Author     string              `json:"author"`
	CreatedAt  int64               `json:"created_at"`
	Document   FleetConfigDocument `json:"document"`
}

var fleetConfigTopLevelSections = map[string]struct{}{
	"$comment": {}, "logging": {}, "auth": {}, "models": {}, "agents": {},
	"bindings": {}, "messages": {}, "commands": {}, "session": {}, "hooks": {},
	"channels": {}, "gateway": {}, "mcp": {}, "skills": {}, "plugins": {},
	"tools": {}, "diagnostics": {},
}

// FleetConfigAllowedSections returns the stable v1 OpenClaw top-level allowlist.
func FleetConfigAllowedSections() []string {
	sections := make([]string, 0, len(fleetConfigTopLevelSections))
	for section := range fleetConfigTopLevelSections {
		sections = append(sections, section)
	}
	slices.Sort(sections)
	return sections
}

// ValidateFleetConfigDocument validates the event envelope, OpenClaw top-level
// sections, environment defaults, and secret placeholder policy.
func ValidateFleetConfigDocument(document FleetConfigDocument) error {
	var violations []string
	if strings.TrimSpace(document.Schema) != SoulFactoryFleetConfigSchema {
		violations = append(violations, fmt.Sprintf("schema must be %q", SoulFactoryFleetConfigSchema))
	}
	if document.Template == nil {
		violations = append(violations, "template must be a JSON object")
	}
	for section := range document.Template {
		if _, ok := fleetConfigTopLevelSections[section]; !ok {
			violations = append(violations, fmt.Sprintf("template section %q is not allowed", section))
		}
	}
	for i, requirement := range document.Defaults.RequiredPlugins {
		parts := strings.SplitN(strings.TrimSpace(requirement), "=", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
			violations = append(violations, fmt.Sprintf("defaults.required_plugins[%d] must use plugin-id=install-source", i))
		}
	}
	validateFleetSecretPlaceholders(document.Template, "template", &violations)
	if len(violations) > 0 {
		slices.Sort(violations)
		return fmt.Errorf("invalid fleet config: %s", strings.Join(violations, "; "))
	}
	return nil
}

// BuildFleetConfigEvent builds an unsigned parameterized-replaceable event.
// The caller must sign it and verify relay OK acceptance.
func BuildFleetConfigEvent(document FleetConfigDocument) (*nostr.Event, error) {
	if err := ValidateFleetConfigDocument(document); err != nil {
		return nil, err
	}
	content, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("marshal fleet config: %w", err)
	}
	return &nostr.Event{
		Kind:      nostr.Kind(domain.KindSoulFleetConfig),
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{tagParameterizedD, SoulFactoryFleetConfigIdentifier},
			{"schema", SoulFactoryFleetConfigSchema},
			{"t", "soulfactory-fleet-config"},
		},
		Content: string(content),
	}, nil
}

// ParseFleetConfigEvent validates a kind 31953 event from a trusted operator.
// Signature verification is performed by SoulFactoryRelayBus before delivery.
func ParseFleetConfigEvent(event *nostr.Event, trustedOperators []string) (*FleetConfigSnapshot, error) {
	if event == nil {
		return nil, fmt.Errorf("nil fleet config event")
	}
	if event.Kind != nostr.Kind(domain.KindSoulFleetConfig) {
		return nil, fmt.Errorf("unexpected fleet config kind %d", event.Kind)
	}
	author := strings.ToLower(strings.TrimSpace(event.PubKey.Hex()))
	trusted := false
	for _, candidate := range trustedOperators {
		if strings.ToLower(strings.TrimSpace(candidate)) == author {
			trusted = true
			break
		}
	}
	if !trusted {
		return nil, fmt.Errorf("fleet config author is not a trusted operator")
	}
	if tagValue(event.Tags, tagParameterizedD) != SoulFactoryFleetConfigIdentifier {
		return nil, fmt.Errorf("fleet config d tag must be %q", SoulFactoryFleetConfigIdentifier)
	}
	if tagValue(event.Tags, "schema") != SoulFactoryFleetConfigSchema {
		return nil, fmt.Errorf("fleet config schema tag must be %q", SoulFactoryFleetConfigSchema)
	}
	if len(event.Content) > maxFleetConfigContentBytes {
		return nil, fmt.Errorf("fleet config content exceeds %d bytes", maxFleetConfigContentBytes)
	}

	decoder := json.NewDecoder(bytes.NewBufferString(event.Content))
	decoder.DisallowUnknownFields()
	var document FleetConfigDocument
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("parse fleet config content: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, fmt.Errorf("parse fleet config content: %w", err)
	}
	if err := ValidateFleetConfigDocument(document); err != nil {
		return nil, err
	}
	return &FleetConfigSnapshot{
		Coordinate: parameterizedCoordinate(domain.KindSoulFleetConfig, author, SoulFactoryFleetConfigIdentifier),
		EventID:    event.ID.Hex(),
		Author:     author,
		CreatedAt:  event.CreatedAt.Time().Unix(),
		Document:   document,
	}, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

func validateFleetSecretPlaceholders(value interface{}, path string, violations *[]string) {
	switch typed := value.(type) {
	case map[string]interface{}:
		for key, nested := range typed {
			nextPath := path + "." + key
			if isFleetSecretField(key) {
				if text, ok := nested.(string); ok && !isEnvironmentPlaceholder(text) {
					*violations = append(*violations, nextPath+" must use a ${VAR} placeholder")
					continue
				}
			}
			validateFleetSecretPlaceholders(nested, nextPath, violations)
		}
	case []interface{}:
		for index, nested := range typed {
			validateFleetSecretPlaceholders(nested, fmt.Sprintf("%s[%d]", path, index), violations)
		}
	}
}

func isFleetSecretField(key string) bool {
	normalized := strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(strings.TrimSpace(key)))
	switch normalized {
	case "apikey", "password", "token", "secret", "secretkey", "privatekey", "clientsecret", "accesskey":
		return true
	default:
		return false
	}
}

func isEnvironmentPlaceholder(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 4 || !strings.HasPrefix(value, "${") || !strings.HasSuffix(value, "}") {
		return false
	}
	name := value[2 : len(value)-1]
	for index, char := range name {
		if !(char == '_' || char >= 'A' && char <= 'Z' || index > 0 && char >= '0' && char <= '9') {
			return false
		}
	}
	return name != ""
}

// newestFleetConfigEvent implements deterministic replaceable ordering.
func newestFleetConfigEvent(events []*nostr.Event) *nostr.Event {
	var latest *nostr.Event
	for _, event := range events {
		if event == nil || event.Kind != nostr.Kind(domain.KindSoulFleetConfig) {
			continue
		}
		if latest == nil || event.CreatedAt > latest.CreatedAt ||
			(event.CreatedAt == latest.CreatedAt && event.ID.Hex() > latest.ID.Hex()) {
			latest = event
		}
	}
	return latest
}

func fleetConfigCreatedAt(snapshot *FleetConfigSnapshot) time.Time {
	if snapshot == nil {
		return time.Time{}
	}
	return time.Unix(snapshot.CreatedAt, 0)
}
