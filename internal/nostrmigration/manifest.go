package nostrmigration

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	cascadia "git.sharegap.net/cascadia/cascadia-go"
	"github.com/openagentsinc/bahia/internal/kinds"
)

const (
	CanonicalContextVMMessage      = cascadia.CAS_INTENT
	CanonicalCEP4GiftWrap          = cascadia.NIP59_GIFT_WRAP
	CanonicalCEP19EphemeralWrap    = cascadia.NIP59_EPHEMERAL_GIFT_WRAP
	CanonicalNIP09Delete           = 5
	CanonicalNIP38OperationalState = cascadia.NIP38_USER_STATUS
	legacyHeartbeatObservation     = 30350
	CanonicalCASCPState            = cascadia.CAS_CP_STATE
	CanonicalCASAudit              = cascadia.CAS_AUDIT
	CanonicalContextVMDiscovery    = cascadia.CTXVM_SERVER_ANNOUNCEMENT
	CanonicalContextVMTools        = cascadia.CTXVM_TOOLS_ANNOUNCEMENT
	CanonicalContextVMResources    = cascadia.CTXVM_RESOURCES_ANNOUNCEMENT
	CanonicalContextVMTemplates    = cascadia.CTXVM_RESOURCE_TEMPLATES_ANNOUNCEMENT
	CanonicalContextVMPrompts      = cascadia.CTXVM_PROMPTS_ANNOUNCEMENT
	CanonicalNIP51RelaySet         = 30002
	CanonicalNIP51AvailabilityList = 30004
	CanonicalNIP78AppData          = 30078
)

// Retired worker read-model wire kinds (bahia-irsry.9.2). Older producers
// published worker state on these kinds; current producers publish only 30900
// cp-state records whose legacy_kind is the matching kinds.CPStateFamilyWorker*
// discriminator. They are decoded here, and nowhere else, so old events still
// migrate onto the canonical kind.
const (
	retiredWorkerStateKind              = 32000
	retiredWorkerAssignmentStateKind    = 32001
	retiredWorkerDrainStatusKind        = 32002
	retiredWorkerEligibilityPreviewKind = 32003
)

// Retired addressable audit kinds (bahia-irsry.37). Audits are regular 4903
// facts; older producers published one addressable kind per audit type in
// 31000-31024 (31000-31099 reserved), with d=<entity>, so each audit replaced
// the previous one. The kinds are decoded here, and nowhere else, so old audit
// events still migrate onto 4903.
const (
	retiredAuditFirstKind = 31000
	retiredAuditLastKind  = 31024
)

type EventLayer string

const (
	LayerIntent     EventLayer = "intent"
	LayerObservable EventLayer = "observable"
	LayerState      EventLayer = "state"
	LayerCollection EventLayer = "collection"
	LayerDiscovery  EventLayer = "discovery"
	LayerAppData    EventLayer = "app_data"
)

type Disposition struct {
	LegacyKind    int
	CanonicalKind int
	Layer         EventLayer
	Domain        string
	Operation     string
	Method        string
	Schema        string
	DTagPrefix    string
	Delete        bool
	Encrypted     bool
	// Topic is the single-letter t tag canonical consumers REQ on; set for
	// families whose readers filter on #t (worker state).
	Topic string
	// WorkerFamily is set for worker read models. Their migrated record is
	// addressed on the family's canonical coordinate (kinds.CPStateFamily
	// WorkerDTag) rather than a per-legacy-event d, so it competes with the
	// live record under NIP-01 replacement and never shadows current state.
	WorkerFamily kinds.CPStateFamily
}

func (d Disposition) DTag(legacyEventID string) string {
	prefix := d.DTagPrefix
	if prefix == "" {
		prefix = d.Domain
	}
	if prefix == "" {
		prefix = "event"
	}
	return fmt.Sprintf("%s:migrated:%s", prefix, legacyEventID)
}

// workerDTag returns the canonical family coordinate for a migrated worker
// record: the family's d prefix plus the record id (the preview id for
// eligibility, otherwise the worker pubkey) read from the legacy content or
// its worker tag. It returns false for non-worker dispositions and for records
// that name no id, which keep the per-legacy-event d.
func (d Disposition) workerDTag(content map[string]any, legacyTagsJSON []byte) (string, bool) {
	if d.WorkerFamily == 0 {
		return "", false
	}
	fields := []string{"worker_pubkey", "pubkey"}
	if d.WorkerFamily == kinds.CPStateFamilyWorkerEligibility {
		fields = []string{"preview_id"}
	}
	id := ""
	for _, field := range fields {
		if value, ok := content[field].(string); ok && strings.TrimSpace(value) != "" {
			id = strings.TrimSpace(value)
			break
		}
	}
	if id == "" && d.WorkerFamily != kinds.CPStateFamilyWorkerEligibility {
		var legacyTags [][]string
		if json.Unmarshal(legacyTagsJSON, &legacyTags) == nil {
			for _, tag := range legacyTags {
				if len(tag) >= 2 && tag[0] == "worker" && strings.TrimSpace(tag[1]) != "" {
					id = strings.TrimSpace(tag[1])
					break
				}
			}
		}
	}
	if id == "" {
		return "", false
	}
	return d.WorkerFamily.WorkerDTag(id)
}

func (d Disposition) Tags(legacyEventID string) [][]string {
	tags := [][]string{
		{"migrated-from", legacyEventID},
		{"legacy-kind", fmt.Sprint(d.LegacyKind)},
		{"migration", migrationID},
		{"schema", d.Schema},
		{"domain", d.Domain},
		{"layer", string(d.Layer)},
	}
	if d.Topic != "" {
		tags = append(tags, []string{"t", d.Topic})
	}
	if d.Method != "" {
		tags = append(tags, []string{"method", d.Method})
	}
	if d.Operation != "" {
		tags = append(tags, []string{"op", d.Operation})
	}
	if d.Delete {
		tags = append(tags, []string{"e", legacyEventID}, []string{"k", fmt.Sprint(d.LegacyKind)})
	}
	if d.CanonicalKind >= 30000 || d.CanonicalKind == CanonicalNIP38OperationalState || d.CanonicalKind == CanonicalContextVMDiscovery || d.CanonicalKind == CanonicalNIP51RelaySet || d.CanonicalKind == CanonicalNIP78AppData {
		tags = append(tags, []string{"d", d.DTag(legacyEventID)})
	}
	return tags
}

var manifest = buildManifest()

func Manifest() map[int]Disposition {
	out := make(map[int]Disposition, len(manifest))
	for k, v := range manifest {
		out[k] = v
	}
	return out
}

func Lookup(kind int) (Disposition, bool) {
	d, ok := manifest[kind]
	return d, ok
}

// KindJustification records why a kind constant is not present as a legacy
// migration input, or why a duplicate numeric alias needs event-aware handling.
type KindJustification struct {
	Name     string
	Kind     int
	Category string
	Reason   string
}

// ConstantJustification returns manifest coverage for kind constants that are
// intentionally not migrated by kind number. It also documents duplicate legacy
// aliases whose numeric value is shared with another migration disposition.
func ConstantJustification(name string, kind int) (KindJustification, bool) {
	j, ok := constantJustifications[name]
	if !ok || j.Kind != kind {
		return KindJustification{}, false
	}
	return j, true
}

func JustifiedConstantOmissions() map[string]KindJustification {
	out := make(map[string]KindJustification, len(constantJustifications))
	for name, justification := range constantJustifications {
		out[name] = justification
	}
	return out
}

// ResolveDisposition applies manifest coverage to a concrete legacy event. Most
// legacy kinds are resolved solely by kind number. The four legacy worker
// read-model aliases reused kind numbers that later became system/backup
// projections, so worker-shaped tags/content are resolved to worker schemas.
// Kind 30002 is likewise shared with Bahia's canonical NIP-51 topology events;
// those canonical d-tags must never be fed back through legacy translation.
// Otherwise the primary kind-number disposition remains in force.
func ResolveDisposition(kind int, tagsJSON []byte, content string) (Disposition, bool) {
	if kind == kinds.RelaySetDiscovery && hasCanonicalRelaySetDTag(tagsJSON) {
		return Disposition{}, false
	}
	if alias, ok := legacyWorkerAliasDisposition(kind); ok && hasLegacyWorkerEvidence(kind, tagsJSON, content) {
		return alias, true
	}
	return Lookup(kind)
}

func hasCanonicalRelaySetDTag(tagsJSON []byte) bool {
	var tags [][]string
	if len(tagsJSON) == 0 || json.Unmarshal(tagsJSON, &tags) != nil {
		return false
	}
	for _, tag := range tags {
		if len(tag) < 2 || tag[0] != "d" {
			continue
		}
		switch tag[1] {
		case "bahia-browser-v1", "bahia-contextvm-v1", "bahia-service-v1":
			return true
		}
	}
	return false
}

func LegacyKinds() []int {
	out := make([]int, 0, len(manifest))
	for kind := range manifest {
		out = append(out, kind)
	}
	sort.Ints(out)
	return out
}

var constantJustifications = map[string]KindJustification{
	"CASAudit":                       omitted("CASAudit", kinds.CASAudit, "canonical-target", "canonical CAS audit output; migration never treats already-canonical audit events as legacy input"),
	"NIP38Status":                    omitted("NIP38Status", kinds.NIP38Status, "canonical-target", "canonical NIP-38 operational status output; legacy status kinds map to this kind"),
	"HeartbeatObservation":           omitted("HeartbeatObservation", kinds.HeartbeatObservation, "canonical-alias", "semantic alias for NIP-38 status kind 30315; continuity heartbeats are identified by #domain=continuity and heartbeat schema/d/worker tags"),
	"AssistantTranscript":            omitted("AssistantTranscript", kinds.AssistantTranscript, "canonical-alias", "semantic alias for CAS agent heartbeat kind 30316 used by assistant transcript sidecar reads; not a Bahia legacy migration input"),
	"CASControlState":                omitted("CASControlState", kinds.CASControlState, "canonical-target", "canonical CAS control-plane state output; legacy read models map to this kind"),
	"ConfigACLList":                  omitted("ConfigACLList", kinds.ConfigACLList, "config-fabric", "canonical NIP-51 config desired state consumed directly, not a Bahia legacy migration input"),
	"ConfigPolicy":                   omitted("ConfigPolicy", kinds.ConfigPolicy, "config-fabric", "canonical NIP-78 config desired state consumed directly; schema tags distinguish it from other app-data documents"),
	"LoomWorkerAdvertisement":        omitted("LoomWorkerAdvertisement", kinds.LoomWorkerAdvertisement, "interop", "open Loom protocol event consumed directly, not a Bahia legacy kind to rewrite"),
	"LoomJobRequest":                 omitted("LoomJobRequest", kinds.LoomJobRequest, "interop", "open Loom protocol request consumed directly, not a Bahia legacy kind to rewrite"),
	"LoomJobStatusUpdate":            omitted("LoomJobStatusUpdate", kinds.LoomJobStatusUpdate, "interop", "open Loom protocol event consumed directly, not a Bahia legacy kind to rewrite"),
	"LoomJobResult":                  omitted("LoomJobResult", kinds.LoomJobResult, "interop", "open Loom protocol event consumed directly, not a Bahia legacy kind to rewrite"),
	"LoomJobCancellation":            omitted("LoomJobCancellation", kinds.LoomJobCancellation, "interop", "open Loom protocol event consumed directly, not a Bahia legacy kind to rewrite"),
	"HiveCIWorkflowRun":              omitted("HiveCIWorkflowRun", kinds.HiveCIWorkflowRun, "interop", "open Hive-CI protocol event consumed directly, not a Bahia legacy kind to rewrite"),
	"HiveCIWorkflowResult":           omitted("HiveCIWorkflowResult", kinds.HiveCIWorkflowResult, "interop", "open Hive-CI protocol event consumed directly, not a Bahia legacy kind to rewrite"),
	"NIP22Comment":                   omitted("NIP22Comment", kinds.NIP22Comment, "standard", "standard NIP-22 comment event consumed directly, not a Bahia legacy kind to rewrite"),
	"NIP34UserGraspList":             omitted("NIP34UserGraspList", kinds.NIP34UserGraspList, "standard", "standard NIP-34 repository collaboration event consumed directly, not a Bahia legacy kind to rewrite"),
	"NIP34Patch":                     omitted("NIP34Patch", kinds.NIP34Patch, "standard", "standard NIP-34 patch event consumed directly, not a Bahia legacy kind to rewrite"),
	"NIP34PullRequest":               omitted("NIP34PullRequest", kinds.NIP34PullRequest, "standard", "standard NIP-34 pull request event consumed directly, not a Bahia legacy kind to rewrite"),
	"NIP34PullRequestUpdate":         omitted("NIP34PullRequestUpdate", kinds.NIP34PullRequestUpdate, "standard", "standard NIP-34 pull request update event consumed directly, not a Bahia legacy kind to rewrite"),
	"NIP34Issue":                     omitted("NIP34Issue", kinds.NIP34Issue, "standard", "standard NIP-34 issue event consumed directly, not a Bahia legacy kind to rewrite"),
	"NIP34StatusOpen":                omitted("NIP34StatusOpen", kinds.NIP34StatusOpen, "standard", "standard NIP-34 open status event consumed directly, not a Bahia legacy kind to rewrite"),
	"NIP34StatusAppliedOrMerged":     omitted("NIP34StatusAppliedOrMerged", kinds.NIP34StatusAppliedOrMerged, "standard", "standard NIP-34 applied/merged status event consumed directly, not a Bahia legacy kind to rewrite"),
	"NIP34StatusClosed":              omitted("NIP34StatusClosed", kinds.NIP34StatusClosed, "standard", "standard NIP-34 closed status event consumed directly, not a Bahia legacy kind to rewrite"),
	"NIP34StatusDraft":               omitted("NIP34StatusDraft", kinds.NIP34StatusDraft, "standard", "standard NIP-34 draft status event consumed directly, not a Bahia legacy kind to rewrite"),
	"NIP34RepositoryAnnouncement":    omitted("NIP34RepositoryAnnouncement", kinds.NIP34RepositoryAnnouncement, "standard", "standard NIP-34 repository announcement consumed directly, not a Bahia legacy kind to rewrite"),
	"NIP34RepositoryState":           omitted("NIP34RepositoryState", kinds.NIP34RepositoryState, "standard", "standard NIP-34 repository state consumed directly, not a Bahia legacy kind to rewrite"),
	"SoulFactoryAction":              omitted("SoulFactoryAction", kinds.SoulFactoryAction, "interop", "SoulFactory lifecycle action event consumed directly by SoulFactory, not a Bahia legacy kind to rewrite"),
	"SoulFactoryActionLegacyResult":  omitted("SoulFactoryActionLegacyResult", kinds.SoulFactoryActionLegacyResult, "interop", "SoulFactory legacy result alias remains direct SoulFactory interop, not Bahia migration input"),
	"SoulFactoryProvisioningRequest": omitted("SoulFactoryProvisioningRequest", kinds.SoulFactoryProvisioningRequest, "interop", "SoulFactory provisioning request consumed directly by SoulFactory, not a Bahia legacy kind to rewrite"),
	"SoulFactoryProvisioningStatus":  omitted("SoulFactoryProvisioningStatus", kinds.SoulFactoryProvisioningStatus, "interop", "SoulFactory provisioning status consumed directly by clients, not a Bahia legacy kind to rewrite"),
	"SoulFactoryProvisioningResult":  omitted("SoulFactoryProvisioningResult", kinds.SoulFactoryProvisioningResult, "interop", "SoulFactory provisioning result consumed directly by clients, not a Bahia legacy kind to rewrite"),
	"SoulFactoryRuntimeCapability":   omitted("SoulFactoryRuntimeCapability", kinds.SoulFactoryRuntimeCapability, "interop", "SoulFactory runtime capability announcement consumed directly by clients, not a Bahia legacy kind to rewrite"),
	"SoulFactoryRuntimeControl":      omitted("SoulFactoryRuntimeControl", kinds.SoulFactoryRuntimeControl, "interop", "SoulFactory runtime control event consumed directly by runtimes, not a Bahia legacy kind to rewrite"),
	"SoulFactoryRuntimeResult":       omitted("SoulFactoryRuntimeResult", kinds.SoulFactoryRuntimeResult, "interop", "SoulFactory runtime result consumed directly by SoulFactory, not a Bahia legacy kind to rewrite"),
	"SoulFactoryTemplate":            omitted("SoulFactoryTemplate", kinds.SoulFactoryTemplate, "interop", "SoulFactory template event consumed directly by clients, not a Bahia legacy kind to rewrite"),
	"SoulFactoryFleetConfig":         omitted("SoulFactoryFleetConfig", kinds.SoulFactoryFleetConfig, "interop", "SoulFactory fleet configuration consumed directly by provisioning and clients, not a Bahia legacy kind to rewrite"),
	"SoulFactoryAgentSoul":           omitted("SoulFactoryAgentSoul", kinds.SoulFactoryAgentSoul, "interop", "SoulFactory agent soul event consumed directly by clients, not a Bahia legacy kind to rewrite"),
	"SoulFactoryDraft":               omitted("SoulFactoryDraft", kinds.SoulFactoryDraft, "interop", "SoulFactory draft event consumed directly by clients, not a Bahia legacy kind to rewrite"),
	"ContextVMMessage":               omitted("ContextVMMessage", kinds.ContextVMMessage, "canonical-transport", "canonical ContextVM request transport; legacy requests map to this kind and should not be re-migrated"),
	"ContextVMGiftWrap":              omitted("ContextVMGiftWrap", kinds.ContextVMGiftWrap, "canonical-transport", "canonical CEP-4 encrypted request transport; legacy encrypted requests map to this kind"),
	"ContextVMEphemeralGiftWrap":     omitted("ContextVMEphemeralGiftWrap", kinds.ContextVMEphemeralGiftWrap, "canonical-transport", "canonical CEP-19 ephemeral wrapper; not a legacy Bahia event kind"),
	"ContextVMServerAnnouncement":    omitted("ContextVMServerAnnouncement", kinds.ContextVMServerAnnouncement, "canonical-discovery", "canonical ContextVM discovery event; not a legacy Bahia event kind"),
	"ContextVMToolsList":             omitted("ContextVMToolsList", kinds.ContextVMToolsList, "canonical-discovery", "canonical ContextVM tools list; not a legacy Bahia event kind"),
	"ContextVMResourcesList":         omitted("ContextVMResourcesList", kinds.ContextVMResourcesList, "canonical-discovery", "canonical ContextVM resources list; not a legacy Bahia event kind"),
	"ContextVMResourceTemplatesList": omitted("ContextVMResourceTemplatesList", kinds.ContextVMResourceTemplatesList, "canonical-discovery", "canonical ContextVM resource templates list; not a legacy Bahia event kind"),
	"ContextVMPromptsList":           omitted("ContextVMPromptsList", kinds.ContextVMPromptsList, "canonical-discovery", "canonical ContextVM prompts list; not a legacy Bahia event kind"),
	"NIP65RelayList":                 omitted("NIP65RelayList", kinds.NIP65RelayList, "standard", "standard NIP-65 relay list consumed directly, not rewritten by Bahia migration"),
	"NIP51DMRelayList":               omitted("NIP51DMRelayList", kinds.NIP51DMRelayList, "standard", "standard NIP-51 DM relay list consumed directly for configured receive relays, not rewritten by Bahia migration"),
	"SBOMAvailabilityList":           omitted("SBOMAvailabilityList", kinds.SBOMAvailabilityList, "canonical-target", "canonical NIP-51 SBOM availability list output; legacy SBOM index events map to this kind"),
	"SBOMAttestation":                omitted("SBOMAttestation", kinds.SBOMAttestation, "canonical-alias", "compatibility alias for SBOMReference; canonical schema is bahia.sbom.ref.v1"),
	"SBOMIndex":                      omitted("SBOMIndex", kinds.SBOMIndex, "legacy-alias", "legacy SBOM index alias retained for read-only migration compatibility; production availability lists use SBOMAvailabilityList"),
	"LongFormContent":                omitted("LongFormContent", kinds.LongFormContent, "standard", "standard NIP-23 long-form content event consumed directly; not a Bahia legacy control-plane/read-model kind to rewrite"),
	"LongFormDraft":                  omitted("LongFormDraft", kinds.LongFormDraft, "standard", "standard NIP-23 long-form draft event consumed directly; not a Bahia legacy control-plane/read-model kind to rewrite"),
	"NostrSignature":                 omitted("NostrSignature", kinds.NostrSignature, "custom-support", "signature support event is not part of the legacy control-plane/read-model migration inventory"),
	"FIPSOverlayAdvert":              omitted("FIPSOverlayAdvert", kinds.FIPSOverlayAdvert, "custom-interop", "FIPS overlay advertisement is handled by the FIPS overlay path, not the Bahia legacy migration"),
	"DashboardWidget":                omitted("DashboardWidget", kinds.DashboardWidget, "interop", "NIP-CAS-0009 agent-published dashboard widget consumed read-only by the web widgets wall; never a Bahia legacy control-plane/read-model kind to rewrite"),
	"HTTPAuth":                       omitted("HTTPAuth", kinds.HTTPAuth, "standard", "standard NIP-98 HTTP auth event; never a Bahia legacy migration input"),
	"LegacyWorkerState":              omitted("LegacyWorkerState", kinds.LegacyWorkerState, "conflicting-alias", "shares 31974 with SystemDiscovery; ResolveDisposition maps worker-tagged/worker-shaped events to worker state"),
	"LegacyWorkerAssignmentState":    omitted("LegacyWorkerAssignmentState", kinds.LegacyWorkerAssignmentState, "conflicting-alias", "shares 31991 with BackupDefinitionRegistry; ResolveDisposition maps worker assignment events to worker state"),
	"LegacyWorkerDrainStatus":        omitted("LegacyWorkerDrainStatus", kinds.LegacyWorkerDrainStatus, "conflicting-alias", "shares 31992 with BackupPolicyRegistry; ResolveDisposition maps worker drain events to worker state"),
	"LegacyWorkerEligibilityPreview": omitted("LegacyWorkerEligibilityPreview", kinds.LegacyWorkerEligibilityPreview, "conflicting-alias", "shares 31993 with BackupRepositoryRegistry; ResolveDisposition maps worker eligibility events to worker state"),
	"OrgRegistry":                    omitted("OrgRegistry", kinds.OrgRegistry, "cp-state-family", "canonical cp-state output for org registry; not a legacy migration input"),
	"OrgMemberRegistry":              omitted("OrgMemberRegistry", kinds.OrgMemberRegistry, "cp-state-family", "canonical cp-state output for org member registry; not a legacy migration input"),
	"OrgInviteRegistry":              omitted("OrgInviteRegistry", kinds.OrgInviteRegistry, "cp-state-family", "canonical cp-state output for org invite registry; not a legacy migration input"),
	"SecretRegistry":                 omitted("SecretRegistry", kinds.SecretRegistry, "canonical-target", "canonical cp-state secret registry output; Phase 3 N1 produces secrets as NIP-44-encrypted 30900 records with this legacy_kind discriminator"),
	"NotificationChannelRegistry":    omitted("NotificationChannelRegistry", kinds.NotificationChannelRegistry, "canonical-target", "canonical cp-state notification channel registry output; Phase 3 N1 produces channels as NIP-44-encrypted 30900 records with this legacy_kind discriminator"),
	"OrgKeyEnvelope":                 omitted("OrgKeyEnvelope", kinds.OrgKeyEnvelope, "cp-state-family", "canonical cp-state output for per-org content key envelopes (Phase 3 C1); NIP-44-encrypted OCK wraps distributed to org members via the shared signing/outbox pipeline"),
	"PaymentRecord":                  omitted("PaymentRecord", kinds.PaymentRecord, "cp-state-family", "canonical cp-state output for payment records (bahia-irsry.60); OCK-encrypted 30900 records published from PaymentService mutation sites"),
	"SecurityFindingRecord":          omitted("SecurityFindingRecord", kinds.SecurityFindingRecord, "cp-state-family", "canonical cp-state output for individual security findings (bahia-irsry.60); OCK-encrypted 30900 records published from SecurityScanner mutation sites"),
	"SecurityScheduleRecord":         omitted("SecurityScheduleRecord", kinds.SecurityScheduleRecord, "cp-state-family", "canonical cp-state output for security scan schedules (bahia-irsry.60); OCK-encrypted 30900 records published from PolicyService schedule derivation"),
	"SecurityFindingDetailRecord":    omitted("SecurityFindingDetailRecord", kinds.SecurityFindingDetailRecord, "cp-state-family", "canonical cp-state output for security finding details (bahia-irsry.60); OCK-encrypted 30900 records, one per finding, with full detail text"),
	"ManagedInstanceHealthRecord":    omitted("ManagedInstanceHealthRecord", kinds.ManagedInstanceHealthRecord, "cp-state-family", "canonical managed instance health state on 30900"),
	"RouteCanaryRecord":              omitted("RouteCanaryRecord", kinds.RouteCanaryRecord, "cp-state-family", "canonical route canary state on 30900"),
	"SoulRuntimePolicyRecord":        omitted("SoulRuntimePolicyRecord", kinds.SoulRuntimePolicyRecord, "cp-state-family", "canonical enabled Soul Factory runtime policy on 30900"),
	"BlossomAdminRecord":             omitted("BlossomAdminRecord", kinds.BlossomAdminRecord, "cp-state-family", "canonical confidential Blossom server administration on 30900"),
	"BlossomBlobRecord":              omitted("BlossomBlobRecord", kinds.BlossomBlobRecord, "cp-state-family", "canonical confidential Blossom blob metadata on 30900"),
	"PackageIntentState":             omitted("PackageIntentState", kinds.PackageIntentState, "cp-state-family", "fleet-OCK encrypted package intent and approval lifecycle state on 30900"),
	"ToolProvisionIntentState":       omitted("ToolProvisionIntentState", kinds.ToolProvisionIntentState, "cp-state-family", "fleet-OCK encrypted tool provisioning intent state on 30900"),
	"ToolDenylistState":              omitted("ToolDenylistState", kinds.ToolDenylistState, "cp-state-family", "fleet-OCK encrypted operator denylist policy on 30900"),
	"ToolProfileState":               omitted("ToolProfileState", kinds.ToolProfileState, "cp-state-family", "fleet-OCK encrypted tool profile state on 30900"),
	"NotificationLogState":           omitted("NotificationLogState", kinds.NotificationLogState, "cp-state-family", "fleet-OCK encrypted bounded per-channel notification log index on 30900"),
}

func omitted(name string, kind int, category, reason string) KindJustification {
	return KindJustification{Name: name, Kind: kind, Category: category, Reason: reason}
}

func legacyWorkerAliasDisposition(kind int) (Disposition, bool) {
	schema, topic, family := "", "", kinds.CPStateFamily(0)
	switch kind {
	case kinds.LegacyWorkerState:
		schema, topic, family = "bahia.state.worker.v1", kinds.WorkerStateTopic, kinds.CPStateFamilyWorkerState
	case kinds.LegacyWorkerAssignmentState:
		schema, topic, family = "bahia.state.worker-assignment.v1", kinds.WorkerAssignmentTopic, kinds.CPStateFamilyWorkerAssignment
	case kinds.LegacyWorkerDrainStatus:
		schema, topic, family = "bahia.state.worker-drain.v1", kinds.WorkerDrainTopic, kinds.CPStateFamilyWorkerDrain
	case kinds.LegacyWorkerEligibilityPreview:
		schema, topic, family = "bahia.state.worker-eligibility.v1", kinds.WorkerEligibilityTopic, kinds.CPStateFamilyWorkerEligibility
	default:
		return Disposition{}, false
	}
	return Disposition{LegacyKind: kind, CanonicalKind: CanonicalCASCPState, Layer: LayerState, Domain: kinds.WorkerDomain, Operation: "state", Schema: schema, DTagPrefix: "worker", Topic: topic, WorkerFamily: family}, true
}

func hasLegacyWorkerEvidence(kind int, tagsJSON []byte, content string) bool {
	var tags [][]string
	if err := json.Unmarshal(tagsJSON, &tags); err == nil {
		for _, tag := range tags {
			if len(tag) < 2 {
				continue
			}
			key := strings.ToLower(strings.TrimSpace(tag[0]))
			value := strings.ToLower(strings.TrimSpace(tag[1]))
			if key == "worker" || (key == "domain" && value == "worker") || (key == "schema" && strings.Contains(value, ".worker")) {
				return true
			}
		}
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		return false
	}
	has := func(key string) bool {
		_, ok := payload[key]
		return ok
	}
	switch kind {
	case kinds.LegacyWorkerState:
		return has("max_concurrent_jobs") || has("current_queue_depth") || has("runtime_target")
	case kinds.LegacyWorkerAssignmentState:
		return has("worker_pubkey") && has("active_assignments")
	case kinds.LegacyWorkerDrainStatus:
		return has("worker_pubkey") && (has("remaining_assignments") || has("safe_to_enter_maintenance") || has("safe_to_disable") || has("drain_started_at"))
	case kinds.LegacyWorkerEligibilityPreview:
		return has("preview_id") && (has("eligible_workers") || has("rejected_workers") || has("ranking_scores"))
	default:
		return false
	}
}

func buildManifest() map[int]Disposition {
	m := map[int]Disposition{}
	addIntent := func(kind int, domain, op string) {
		m[kind] = Disposition{LegacyKind: kind, CanonicalKind: CanonicalContextVMMessage, Layer: LayerIntent, Domain: domain, Operation: op, Method: domain + "/" + op, Schema: "bahia.intent." + domain + "." + op + ".v1", DTagPrefix: domain}
	}
	addEncryptedIntent := func(kind int, domain, op string) {
		d := Disposition{LegacyKind: kind, CanonicalKind: CanonicalCEP4GiftWrap, Layer: LayerIntent, Domain: domain, Operation: op, Method: domain + "/" + op, Schema: "bahia-encrypted-v1", DTagPrefix: domain, Encrypted: true}
		m[kind] = d
	}
	addDelete := func(kind int, domain, op string) {
		m[kind] = Disposition{LegacyKind: kind, CanonicalKind: CanonicalNIP09Delete, Layer: LayerIntent, Domain: domain, Operation: op, Schema: "bahia.delete." + domain + ".v1", DTagPrefix: domain, Delete: true}
	}
	addStatus := func(kind int, domain string) {
		m[kind] = Disposition{LegacyKind: kind, CanonicalKind: CanonicalNIP38OperationalState, Layer: LayerObservable, Domain: domain, Operation: "progress", Schema: "bahia.feedback." + domain + ".v1", DTagPrefix: "cascadia:" + domain}
	}
	addOperational := func(kind int, domain string) {
		m[kind] = Disposition{LegacyKind: kind, CanonicalKind: CanonicalNIP38OperationalState, Layer: LayerObservable, Domain: domain, Operation: "status", Schema: "bahia.status." + domain + ".v1", DTagPrefix: "cascadia:" + domain}
	}
	addResult := func(kind int, domain string) {
		m[kind] = Disposition{LegacyKind: kind, CanonicalKind: CanonicalContextVMMessage, Layer: LayerObservable, Domain: domain, Operation: "result", Schema: "bahia.result." + domain + ".v1", DTagPrefix: domain}
	}
	addState := func(kind int, domain, schema string) {
		m[kind] = Disposition{LegacyKind: kind, CanonicalKind: CanonicalCASCPState, Layer: LayerState, Domain: domain, Operation: "state", Schema: schema, DTagPrefix: domain}
	}
	addAudit := func(kind int, domain, schema string) {
		m[kind] = Disposition{LegacyKind: kind, CanonicalKind: CanonicalCASAudit, Layer: LayerObservable, Domain: domain, Operation: "audit", Schema: schema, DTagPrefix: domain}
	}

	addIntent(kinds.DNSZoneCreateRequest, "dns", "zone-create")
	addIntent(kinds.DNSPolicyApplyRequest, "dns", "policy-apply")
	addIntent(kinds.DNSRecordOverrideRequest, "dns", "record-set")
	addIntent(kinds.DNSDriftRemediateRequest, "dns", "drift-remediate")
	addIntent(kinds.DNSBackendRegisterRequest, "dns", "backend-register")
	addIntent(kinds.DNSOverrideRetireRequest, "dns", "override-retire")
	addStatus(kinds.DNSOperationStatus, "dns")
	for _, kind := range []int{kinds.DNSZoneCreateResult, kinds.DNSPolicyApplyResult, kinds.DNSRecordOverrideResult, kinds.DNSDriftRemediateResult, kinds.DNSBackendRegisterResult, kinds.DNSOverrideRetireResult} {
		addResult(kind, "dns")
	}

	requestMethods := map[int][2]string{
		kinds.DeployRequest: {"service", "deploy"}, kinds.RollbackRequest: {"service", "rollback"}, kinds.ServiceAction: {"service", "action"}, kinds.ServiceCreate: {"service", "create"}, kinds.EnvironmentCreate: {"environment", "create"}, kinds.DeploymentApproval: {"approval", "approve"}, kinds.ObservationSubmit: {"service", "observe"}, kinds.DriftRemediate: {"service", "drift-remediate"}, kinds.LLMRouteCreate: {"llm", "route-create"}, kinds.LLMReleaseRegister: {"llm", "release-register"}, kinds.LLMDeployRequest: {"llm", "deploy"}, kinds.LLMDeploymentApproval: {"approval", "llm-approve"}, kinds.LLMRollbackRequest: {"llm", "rollback"}, kinds.ToolProvisionRequest: {"tool", "provision"}, kinds.ToolApprovalRequest: {"approval", "request"}, kinds.AdoptionScanRequest: {"adoption", "scan"}, kinds.AdoptionImportRequest: {"adoption", "import"}, kinds.ServiceUpdate: {"service", "update"}, kinds.ServiceDelete: {"service", "delete"}, kinds.EnvironmentUpdate: {"environment", "update"}, kinds.EnvironmentDelete: {"environment", "delete"}, kinds.ArtifactRegister: {"artifact", "register"}, kinds.PolicyCreate: {"policy", "create"}, kinds.PolicyUpdate: {"policy", "update"}, kinds.PolicyDelete: {"policy", "delete"}, kinds.PolicyEvaluate: {"policy", "evaluate"}, kinds.PackageRepositoryApply: {"package", "repository-apply"}, kinds.PackageRepositoryDelete: {"package", "repository-delete"}, kinds.PackagePublishIntent: {"package", "publish"}, kinds.PackagePromotionRequest: {"package", "promote"}, kinds.PackageYankRequest: {"package", "yank"}, kinds.PackageDriftDetect: {"package", "drift-detect"}, kinds.WorkerCordonRequest: {"worker", "cordon"}, kinds.WorkerUncordonRequest: {"worker", "uncordon"}, kinds.WorkerDrainRequest: {"worker", "drain"}, kinds.WorkerUndrainRequest: {"worker", "undrain"}, kinds.WorkerMaintenanceEnter: {"worker", "maintenance-enter"}, kinds.WorkerMaintenanceExit: {"worker", "maintenance-exit"}, kinds.WorkerLabelsUpdate: {"worker", "labels-update"}, kinds.WorkerPolicyApplyRequest: {"worker", "policy-apply"}, kinds.WorkloadPinRequest: {"worker", "workload-pin"}, kinds.WorkerCleanupRequest: {"worker", "cleanup"}, kinds.MLRecipeRunRequest: {"ml", "recipe-run"}, kinds.MLInferenceDeployRequest: {"ml", "inference-deploy"}, kinds.MLInferenceDeploymentApproval: {"approval", "ml-inference-approve"}, kinds.MLInferenceRollbackRequest: {"ml", "inference-rollback"}, kinds.MLModelImportRequest: {"ml", "model-import"}, kinds.BackupRunRequest: {"backup", "run"}, kinds.BackupVerificationRequest: {"backup", "verify"}, kinds.BackupRestoreRequest: {"backup", "restore"}, kinds.BackupRestoreApproval: {"approval", "backup-restore-approve"}, kinds.BackupRetentionEnforce: {"backup", "retention-enforce"}, kinds.BackupRepositoryRegister: {"backup", "repository-register"}, kinds.BackupPolicyApply: {"backup", "policy-apply"}, kinds.BackupRecipeApply: {"backup", "recipe-apply"}, kinds.BackupDefinitionApply: {"backup", "definition-apply"}, kinds.BackupRepositoryProbe: {"backup", "repository-probe"}, kinds.AssistantPromptRequest: {"assistant", "prompt"}, kinds.AssistantApproval: {"approval", "assistant-approve"}, kinds.FailoverRequest: {"continuity", "failover"}, kinds.RecoveryRequest: {"continuity", "recovery"}, kinds.CmdBuildRegister: {"build", "register"}, kinds.CmdArtifactRegister: {"artifact", "register"}, kinds.CmdIntentCreate: {"service", "deploy"}, kinds.CmdIntentApprove: {"approval", "approve"}, kinds.CmdIntentReject: {"approval", "reject"}, kinds.CmdRollbackRequest: {"service", "rollback"},
	}
	for kind, parts := range requestMethods {
		addIntent(kind, parts[0], parts[1])
	}
	addDelete(kinds.ServiceDelete, "service", "delete")
	addDelete(kinds.EnvironmentDelete, "environment", "delete")
	addDelete(kinds.PolicyDelete, "policy", "delete")
	addDelete(kinds.PackageRepositoryDelete, "package", "repository-delete")
	addEncryptedIntent(kinds.EncryptedRequest, "contextvm", "encrypted-request")
	addEncryptedIntent(kinds.EncryptedResult, "contextvm", "encrypted-result")

	for _, item := range []struct {
		kind   int
		domain string
	}{{kinds.DeploymentStatus, "deployment"}, {kinds.ActionStatus, "service"}, {kinds.LLMDeploymentStatus, "llm"}, {kinds.ToolProvisionStatus, "tool"}, {kinds.AdoptionStatus, "adoption"}, {kinds.PackageStatus, "package"}, {kinds.BackupRunStatus, "backup"}, {kinds.BackupRestoreStatus, "backup"}, {kinds.BackupVerificationStatus, "backup"}, {kinds.BackupObservation, "backup"}, {kinds.AssistantStatus, "assistant"}, {kinds.RecoveryProgress, "continuity"}} {
		addStatus(item.kind, item.domain)
	}
	addOperational(kinds.ServiceStatus, "service")
	addOperational(kinds.WorkerStatus, "worker")
	m[legacyHeartbeatObservation] = Disposition{LegacyKind: legacyHeartbeatObservation, CanonicalKind: CanonicalNIP38OperationalState, Layer: LayerObservable, Domain: "continuity", Operation: "status", Schema: "bahia.status.continuity-heartbeat.v1", DTagPrefix: "continuity:heartbeat"}
	addOperational(kinds.ContinuityStatus, "continuity")
	addOperational(kinds.DegradedModeActivation, "continuity")
	addOperational(kinds.BahiaReadinessStatus, "system")

	for _, item := range []struct {
		kind   int
		domain string
	}{{kinds.DeploymentResult, "deployment"}, {kinds.ActionResult, "service"}, {kinds.ServiceCreateResult, "service"}, {kinds.EnvironmentCreateResult, "environment"}, {kinds.ObservationResult, "service"}, {kinds.RemediationResult, "service"}, {kinds.LLMRouteCreateResult, "llm"}, {kinds.LLMReleaseRegisterResult, "llm"}, {kinds.LLMDeploymentResult, "llm"}, {kinds.ToolProvisionResult, "tool"}, {kinds.ToolApprovalResponse, "approval"}, {kinds.AdoptionScanResult, "adoption"}, {kinds.AdoptionImportResult, "adoption"}, {kinds.PackageResult, "package"}, {kinds.PackageDriftEvent, "package"}, {kinds.WorkerResult, "worker"}, {kinds.MLRecipeRunResult, "ml"}, {kinds.MLInferenceDeployResult, "ml"}, {kinds.MLInferenceApprovalResult, "approval"}, {kinds.MLInferenceRollbackResult, "ml"}, {kinds.MLModelImportResult, "ml"}, {kinds.BackupRunResult, "backup"}, {kinds.BackupVerificationResult, "backup"}, {kinds.BackupRestoreResult, "backup"}, {kinds.BackupRestoreApprovalResult, "backup"}, {kinds.BackupRetentionResult, "backup"}, {kinds.BackupRepositoryRegisterResult, "backup"}, {kinds.BackupPolicyApplyResult, "backup"}, {kinds.BackupRecipeApplyResult, "backup"}, {kinds.BackupDefinitionApplyResult, "backup"}, {kinds.BackupRepositoryProbeResult, "backup"}, {kinds.AssistantResult, "assistant"}} {
		addResult(item.kind, item.domain)
	}

	for _, item := range []struct {
		kind           int
		domain, schema string
	}{{kinds.ServiceState, "service", "bahia.state.service.v1"}, {kinds.ServiceRegistry, "service", "bahia.registry.service.v1"}, {kinds.EnvironmentRegistry, "environment", "bahia.registry.environment.v1"}, {kinds.LLMRouteRegistry, "llm", "bahia.registry.llm-route.v1"}, {kinds.LLMRouteState, "llm", "bahia.state.llm-route.v1"}, {kinds.ArtifactRegistry, "artifact", "bahia.registry.artifact.v1"}, {kinds.DeploymentIntentRegistry, "deployment", "bahia.registry.deployment-intent.v1"}, {kinds.DeploymentRunRegistry, "deployment", "bahia.registry.deployment-run.v1"}, {kinds.BuildRegistry, "build", "bahia.registry.build.v1"}, {kinds.PolicyRegistry, "policy", "bahia.registry.policy.v1"}, {kinds.PackageRepositoryRegistry, "package", "bahia.registry.package-repository.v1"}, {kinds.PackageArtifactRegistry, "package", "bahia.registry.package-artifact.v1"}, {kinds.PackagePromotionRegistry, "package", "bahia.registry.package-promotion.v1"}, {kinds.DNSZoneState, "dns", "bahia.state.dns-zone.v1"}, {kinds.DNSEndpointState, "dns", "bahia.state.dns-endpoint.v1"}, {kinds.DNSPolicyState, "dns", "bahia.state.dns-policy.v1"}, {kinds.DNSBackendState, "dns", "bahia.state.dns-backend.v1"}, {kinds.MLModelRegistry, "ml", "bahia.registry.ml-model.v1"}, {kinds.MLModelVersionRegistry, "ml", "bahia.registry.ml-model-version.v1"}, {kinds.MLDatasetRegistry, "ml", "bahia.registry.ml-dataset.v1"}, {kinds.MLRecipeRegistry, "ml", "bahia.registry.ml-recipe.v1"}, {kinds.MLRecipeRunState, "ml", "bahia.state.ml-recipe-run.v1"}, {kinds.MLInferenceEndpointRegistry, "ml", "bahia.registry.ml-inference-endpoint.v1"}, {kinds.MLInferenceEndpointState, "ml", "bahia.state.ml-inference-endpoint.v1"}, {kinds.MLEvaluationExperimentState, "ml", "bahia.state.ml-evaluation.v1"}, {kinds.MLArtifactProvenanceGraph, "ml", "bahia.state.ml-provenance.v1"}, {kinds.MLRuntimeCapabilityProfile, "ml", "bahia.state.ml-runtime-capability.v1"}, {kinds.AssistantSession, "assistant", "bahia.state.assistant-session.v1"}, {kinds.BackupDefinitionRegistry, "backup", "bahia.registry.backup-definition.v1"}, {kinds.BackupPolicyRegistry, "backup", "bahia.registry.backup-policy.v1"}, {kinds.BackupRepositoryRegistry, "backup", "bahia.registry.backup-repository.v1"}, {kinds.BackupRetentionRegistry, "backup", "bahia.registry.backup-retention.v1"}, {kinds.BackupRecipeRegistry, "backup", "bahia.registry.backup-recipe.v1"}, {kinds.BackupRunState, "backup", "bahia.state.backup-run.v1"}, {kinds.BackupVerificationState, "backup", "bahia.state.backup-verification.v1"}, {kinds.BackupRestoreState, "backup", "bahia.state.backup-restore.v1"}, {kinds.BackupRuntimeObservationState, "backup", "bahia.state.backup-observation.v1"}, {kinds.ContinuityProfile, "continuity", "bahia.state.continuity-profile.v1"}, {kinds.FailoverPolicy, "continuity", "bahia.state.failover-policy.v1"}, {kinds.StandbyNodeDefinition, "continuity", "bahia.state.standby-node.v1"}, {kinds.ReplicationPolicy, "continuity", "bahia.state.replication-policy.v1"}, {kinds.RecoveryWorkflow, "continuity", "bahia.state.recovery-workflow.v1"}, {kinds.BahiaIdentityDefinition, "system", "bahia.identity.v1"}, {kinds.BahiaReplayCheckpoint, "system", "bahia.replay-checkpoint.v1"}, {kinds.LLMReleaseRegistry, "llm", "bahia.registry.llm-release.v1"}, {kinds.ArtifactSignatureRegistry, "artifact", "bahia.registry.artifact-signature.v1"}, {kinds.ArtifactSBOMRegistry, "artifact", "bahia.registry.artifact-sbom.v1"}, {kinds.SBOMPackageRegistry, "artifact", "bahia.registry.sbom-package.v1"}, {kinds.RuntimeObservationState, "runtime", "bahia.state.runtime-observation.v1"}} {
		addState(item.kind, item.domain, item.schema)
	}

	for _, item := range []struct {
		kind          int
		schema, topic string
		family        kinds.CPStateFamily
	}{{retiredWorkerStateKind, "bahia.state.worker.v1", kinds.WorkerStateTopic, kinds.CPStateFamilyWorkerState}, {retiredWorkerAssignmentStateKind, "bahia.state.worker-assignment.v1", kinds.WorkerAssignmentTopic, kinds.CPStateFamilyWorkerAssignment}, {retiredWorkerDrainStatusKind, "bahia.state.worker-drain.v1", kinds.WorkerDrainTopic, kinds.CPStateFamilyWorkerDrain}, {retiredWorkerEligibilityPreviewKind, "bahia.state.worker-eligibility.v1", kinds.WorkerEligibilityTopic, kinds.CPStateFamilyWorkerEligibility}} {
		addState(item.kind, kinds.WorkerDomain, item.schema)
		disposition := m[item.kind]
		disposition.Topic = item.topic
		disposition.WorkerFamily = item.family
		m[item.kind] = disposition
	}

	m[kinds.SystemDiscovery] = Disposition{LegacyKind: kinds.SystemDiscovery, CanonicalKind: CanonicalContextVMDiscovery, Layer: LayerDiscovery, Domain: "system", Operation: "discover", Schema: "bahia.system-discovery.v1", DTagPrefix: "bahia-system-v1"}
	m[kinds.RelaySetDiscovery] = Disposition{LegacyKind: kinds.RelaySetDiscovery, CanonicalKind: CanonicalNIP51RelaySet, Layer: LayerCollection, Domain: "relay", Operation: "relay-set", Schema: "bahia.relay-set.v1", DTagPrefix: "relays"}
	m[kinds.SBOMReference] = Disposition{LegacyKind: kinds.SBOMReference, CanonicalKind: CanonicalNIP78AppData, Layer: LayerAppData, Domain: "sbom", Operation: "reference", Schema: "bahia.sbom.ref.v1", DTagPrefix: "sbom:ref"}
	m[kinds.LegacySBOMIndex] = Disposition{LegacyKind: kinds.LegacySBOMIndex, CanonicalKind: CanonicalNIP51AvailabilityList, Layer: LayerCollection, Domain: "sbom", Operation: "available-list", Schema: "bahia.sbom.available-list.v1", DTagPrefix: "sbom:available"}

	for kind := retiredAuditFirstKind; kind <= retiredAuditLastKind; kind++ {
		addAudit(kind, "audit", "bahia.audit.v1")
	}
	addAudit(kinds.BackupRunAttestation, "backup", "bahia.audit.backup-run-attestation.v1")
	addAudit(kinds.BackupVerificationAttestation, "backup", "bahia.audit.backup-verification-attestation.v1")
	return m
}
