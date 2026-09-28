package telemetry

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	gonostr "fiatjaf.com/nostr"
	cascadia "git.sharegap.net/cascadia/cascadia-go"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// validateFleetHealthEvent rejects events outside the documented wire contract
// before their tags or content can influence fleet-health state. The NIP-01
// signature and ID are checked by Subscriber before this projection callback.
func validateFleetHealthEvent(ev *gonostr.Event) error {
	if len(ev.Content) > 1<<20 || len(ev.Tags) > 256 {
		return fmt.Errorf("fleet-health envelope exceeds size limit")
	}
	for _, tag := range ev.Tags {
		if len(tag) > 8 {
			return fmt.Errorf("fleet-health tag has too many fields")
		}
		for _, field := range tag {
			if len(field) > 4096 {
				return fmt.Errorf("fleet-health tag field exceeds size limit")
			}
		}
	}
	kind := int(ev.Kind)
	schema := tagValue(ev, "schema")
	if !fleetHealthSchemaMatchesKind(kind, schema) {
		return fmt.Errorf("unsupported schema %q for kind %d", schema, kind)
	}
	if schema == "" || tagValue(ev, "domain") == "" {
		return fmt.Errorf("kind %d requires schema and domain tags", kind)
	}
	for _, key := range []string{"schema", "domain", "d", "status", "type"} {
		if hasConflictingFleetTag(ev, key) {
			return fmt.Errorf("conflicting %s tags", key)
		}
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal([]byte(ev.Content), &body); err != nil || len(body) == 0 {
		return fmt.Errorf("kind %d schema %s requires nonempty JSON object content", kind, schema)
	}
	if raw, ok := body["schema"]; ok {
		var contentSchema string
		if json.Unmarshal(raw, &contentSchema) != nil || contentSchema != schema {
			return fmt.Errorf("schema tag/content mismatch")
		}
	}
	// Generated payload validators are authoritative where the registry provides
	// one. Other known families are explicitly envelope-only, never silently
	// represented as schema-validated.
	if err := validateCanonicalFleetPayload(schema, ev.Content); err != nil {
		return err
	}
	switch kind {
	case kinds.NIP38Status, kinds.CASControlState:
		if tagValue(ev, "d") == "" {
			return fmt.Errorf("kind %d requires d coordinate", kind)
		}
		if kind == kinds.NIP38Status && tagValue(ev, "status") == "" {
			return fmt.Errorf("status requires status tag")
		}
	case kinds.CASAudit:
		if tagValue(ev, "type") == "" {
			return fmt.Errorf("audit requires type tag")
		}
	case kinds.AssistantTranscript:
		if schema != domain.AssistantTranscriptSchema || tagValue(ev, domain.AssistantTranscriptTagEnvelope) != domain.AssistantTranscriptEnvelopeServiceHeldAEAD {
			return fmt.Errorf("invalid assistant transcript schema or envelope")
		}
		for _, key := range []string{"session", "turn", "role", "seq", "key_ref"} {
			if tagValue(ev, key) == "" {
				return fmt.Errorf("assistant transcript requires %s tag", key)
			}
		}
		var envelope domain.AssistantTranscriptAEADEnvelope
		if err := json.Unmarshal([]byte(ev.Content), &envelope); err != nil || envelope.Schema != schema || envelope.Envelope != domain.AssistantTranscriptEnvelopeServiceHeldAEAD || envelope.Algorithm != domain.AssistantTranscriptAEADAlgorithmXChaCha20 || envelope.KeyRef != tagValue(ev, "key_ref") || envelope.Nonce == "" || envelope.Ciphertext == "" {
			return fmt.Errorf("invalid assistant transcript payload")
		}
		if nonce, err := base64.RawStdEncoding.DecodeString(envelope.Nonce); err != nil || len(nonce) != 24 {
			return fmt.Errorf("invalid assistant transcript nonce")
		}
		if ciphertext, err := base64.RawStdEncoding.DecodeString(envelope.Ciphertext); err != nil || len(ciphertext) < 16 {
			return fmt.Errorf("invalid assistant transcript ciphertext")
		}
	case kinds.SoulFactoryRuntimeCapability:
		if schema != domain.SoulFactoryRuntimeCapabilitySchema || tagValue(ev, "d") == "" || tagValue(ev, "runtime") == "" || tagValue(ev, "control-schema") != domain.SoulFactoryRuntimeControlSchema {
			return fmt.Errorf("invalid runtime capability schema or tags")
		}
	default:
		return fmt.Errorf("unsupported fleet-health kind %d", kind)
	}
	return nil
}

func fleetHealthSchemaValidated(schema string) bool {
	switch schema {
	case "bahia.service-state.v2", "bahia.worker-state.v1", "bahia.audit.build.v1", "bahia.audit.deployment.v1":
		return true
	default:
		return false
	}
}

func validateCanonicalFleetPayload(schema, content string) error {
	var payload interface{ Validate() error }
	switch schema {
	case "bahia.service-state.v2":
		payload = &cascadia.BahiaServiceStateV2Payload{}
	case "bahia.worker-state.v1":
		payload = &cascadia.BahiaWorkerStateV1Payload{}
	case "bahia.audit.build.v1":
		payload = &cascadia.BahiaAuditBuildV1Payload{}
	case "bahia.audit.deployment.v1":
		payload = &cascadia.BahiaAuditDeploymentV1Payload{}
	default:
		return nil
	}
	if err := json.Unmarshal([]byte(content), payload); err != nil {
		return fmt.Errorf("invalid %s payload: %w", schema, err)
	}
	if err := payload.Validate(); err != nil {
		return fmt.Errorf("invalid %s payload: %w", schema, err)
	}
	return nil
}

func hasConflictingFleetTag(ev *gonostr.Event, key string) bool {
	seen := ""
	for _, tag := range ev.Tags {
		if len(tag) < 2 || tag[0] != key {
			continue
		}
		value := strings.TrimSpace(tag[1])
		if seen != "" && value != seen {
			return true
		}
		seen = value
	}
	return false
}

// This is the set of canonical schema tags for the semantic kinds consumed by
// the fleet-health projection. It is deliberately keyed by kind: a signer may
// not relabel arbitrary JSON as a known event family. New schemas must be
// reviewed with their producer contract before they affect fleet health.
func fleetHealthSchemaMatchesKind(kind int, schema string) bool {
	switch kind {
	case kinds.NIP38Status:
		return fleetHealthSchemaIn(schema,
			"bahia.status.continuity-heartbeat.v1", "bahia.status.dns.v1",
			"bahia.status.managed-instance-health.v1", "bahia.status.package.v1",
			"bahia.status.route-canary.v1", "bahia.status.security-scan.v1",
			"bahia.status.service.v1", "bahia.status.worker.v1",
			"bahia.agent-runtime-release.v1")
	case kinds.CASControlState:
		return fleetHealthSchemaIn(schema,
			"bahia.cp-state.v1", "bahia.service-state.v2", "bahia.worker-state.v1",
			"bahia.assistant-session.v2", "bahia.relay-settings.v1",
			"cascadia.config.status.v1", "cascadia.config.status.v2", "bahia.dnsagent.state.v1", "bahia.security.scan-summary.v1", "bahia.security.target-summary.v1",
			"bahia.state.assistant-session.v1", "bahia.state.backup-observation.v1",
			"bahia.state.backup-restore.v1", "bahia.state.backup-run.v1",
			"bahia.state.backup-verification.v1", "bahia.state.continuity-profile.v1",
			"bahia.state.dns-backend.v1", "bahia.state.dns-endpoint.v1",
			"bahia.state.dns-policy.v1", "bahia.state.dns-zone.v1",
			"bahia.state.failover-policy.v1", "bahia.state.llm-route.v1",
			"bahia.state.managed-instance-health.v1", "bahia.state.ml-evaluation.v1",
			"bahia.state.ml-inference-endpoint.v1", "bahia.state.ml-provenance.v1",
			"bahia.state.ml-recipe-run.v1", "bahia.state.ml-runtime-capability.v1",
			"bahia.state.package-artifact.v1", "bahia.state.package-promotion.v1",
			"bahia.state.package-repository.v1", "bahia.state.recovery-workflow.v1",
			"bahia.state.replication-policy.v1", "bahia.state.route-canary.v1",
			"bahia.state.service.v1", "bahia.state.soul-factory-provisioning.v1",
			"bahia.state.standby-node.v1", "bahia.state.virtualization.v1",
			"bahia.state.worker-assignment.v1", "bahia.state.worker-cleanup.v1",
			"bahia.state.worker-drain.v1", "bahia.state.worker-eligibility.v1",
			"bahia.state.worker.v1")
	case kinds.CASAudit:
		return fleetHealthSchemaIn(schema,
			"bahia.audit.v1", "bahia.audit.artifact-registration.v1",
			"bahia.audit.assistant-execution-checkpoint.v1", "bahia.audit.backup-run-attestation.v1",
			"bahia.audit.backup-verification-attestation.v1", "bahia.audit.build.v1",
			"bahia.audit.deployment.v1", "bahia.audit.managed-instance-health.v1",
			"bahia.audit.release-promotion.v1", "bahia.audit.release.v1",
			"bahia.audit.route-canary.v1", "bahia.audit.security.v1",
			"bahia.audit.virtualization.v1")
	case kinds.AssistantTranscript:
		return schema == domain.AssistantTranscriptSchema
	case kinds.SoulFactoryRuntimeCapability:
		return schema == domain.SoulFactoryRuntimeCapabilitySchema
	default:
		return false
	}
}

func fleetHealthSchemaIn(schema string, allowed ...string) bool {
	for _, candidate := range allowed {
		if schema == candidate {
			return true
		}
	}
	return false
}
