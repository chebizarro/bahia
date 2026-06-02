package nostrmigration

import (
	"fmt"
	"sort"

	"github.com/openagentsinc/bahia/internal/kinds"
)

const (
	CanonicalContextVMMessage      = 25910
	CanonicalCEP4GiftWrap          = 1059
	CanonicalCEP19EphemeralWrap    = 21059
	CanonicalNIP09Delete           = 5
	CanonicalNIP38OperationalState = 30315
	CanonicalNIP90Feedback         = 7000
	CanonicalCASCPState            = 30900
	CanonicalCASAudit              = 4903
	CanonicalContextVMDiscovery    = 11316
	CanonicalContextVMTools        = 11317
	CanonicalContextVMResources    = 11318
	CanonicalContextVMTemplates    = 11319
	CanonicalContextVMPrompts      = 11320
	CanonicalNIP51RelaySet         = 30002
	CanonicalNIP78AppData          = 30078
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

func (d Disposition) Tags(legacyEventID string) [][]string {
	tags := [][]string{
		{"migrated-from", legacyEventID},
		{"legacy-kind", fmt.Sprint(d.LegacyKind)},
		{"migration", "bahia-nostr-native-v1"},
		{"schema", d.Schema},
		{"domain", d.Domain},
		{"layer", string(d.Layer)},
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

func LegacyKinds() []int {
	out := make([]int, 0, len(manifest))
	for kind := range manifest {
		out = append(out, kind)
	}
	sort.Ints(out)
	return out
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
		m[kind] = Disposition{LegacyKind: kind, CanonicalKind: CanonicalNIP90Feedback, Layer: LayerObservable, Domain: domain, Operation: "progress", Schema: "bahia.feedback." + domain + ".v1", DTagPrefix: domain}
	}
	addOperational := func(kind int, domain string) {
		m[kind] = Disposition{LegacyKind: kind, CanonicalKind: CanonicalNIP38OperationalState, Layer: LayerObservable, Domain: domain, Operation: "status", Schema: "bahia.status." + domain + ".v1", DTagPrefix: "cascadia:" + domain}
	}
	addResult := func(kind int, domain string) {
		m[kind] = Disposition{LegacyKind: kind, CanonicalKind: CanonicalNIP90Feedback, Layer: LayerObservable, Domain: domain, Operation: "result", Schema: "bahia.result." + domain + ".v1", DTagPrefix: domain}
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
	addStatus(kinds.DNSOperationStatus, "dns")
	for _, kind := range []int{kinds.DNSZoneCreateResult, kinds.DNSPolicyApplyResult, kinds.DNSRecordOverrideResult, kinds.DNSDriftRemediateResult, kinds.DNSBackendRegisterResult} {
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
	addOperational(kinds.HeartbeatObservation, "continuity")
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
	}{{kinds.ServiceState, "service", "bahia.state.service.v1"}, {kinds.ServiceRegistry, "service", "bahia.registry.service.v1"}, {kinds.EnvironmentRegistry, "environment", "bahia.registry.environment.v1"}, {kinds.LLMRouteRegistry, "llm", "bahia.registry.llm-route.v1"}, {kinds.LLMRouteState, "llm", "bahia.state.llm-route.v1"}, {kinds.ArtifactRegistry, "artifact", "bahia.registry.artifact.v1"}, {kinds.DeploymentIntentRegistry, "deployment", "bahia.registry.deployment-intent.v1"}, {kinds.DeploymentRunRegistry, "deployment", "bahia.registry.deployment-run.v1"}, {kinds.BuildRegistry, "build", "bahia.registry.build.v1"}, {kinds.PolicyRegistry, "policy", "bahia.registry.policy.v1"}, {kinds.PackageRepositoryRegistry, "package", "bahia.registry.package-repository.v1"}, {kinds.PackageArtifactRegistry, "package", "bahia.registry.package-artifact.v1"}, {kinds.PackagePromotionRegistry, "package", "bahia.registry.package-promotion.v1"}, {kinds.DNSZoneState, "dns", "bahia.state.dns-zone.v1"}, {kinds.DNSEndpointState, "dns", "bahia.state.dns-endpoint.v1"}, {kinds.DNSPolicyState, "dns", "bahia.state.dns-policy.v1"}, {kinds.DNSBackendState, "dns", "bahia.state.dns-backend.v1"}, {kinds.MLModelRegistry, "ml", "bahia.registry.ml-model.v1"}, {kinds.MLModelVersionRegistry, "ml", "bahia.registry.ml-model-version.v1"}, {kinds.MLDatasetRegistry, "ml", "bahia.registry.ml-dataset.v1"}, {kinds.MLRecipeRegistry, "ml", "bahia.registry.ml-recipe.v1"}, {kinds.MLRecipeRunState, "ml", "bahia.state.ml-recipe-run.v1"}, {kinds.MLInferenceEndpointRegistry, "ml", "bahia.registry.ml-inference-endpoint.v1"}, {kinds.MLInferenceEndpointState, "ml", "bahia.state.ml-inference-endpoint.v1"}, {kinds.MLEvaluationExperimentState, "ml", "bahia.state.ml-evaluation.v1"}, {kinds.MLArtifactProvenanceGraph, "ml", "bahia.state.ml-provenance.v1"}, {kinds.MLRuntimeCapabilityProfile, "ml", "bahia.state.ml-runtime-capability.v1"}, {kinds.AssistantSession, "assistant", "bahia.state.assistant-session.v1"}, {kinds.BackupDefinitionRegistry, "backup", "bahia.registry.backup-definition.v1"}, {kinds.BackupPolicyRegistry, "backup", "bahia.registry.backup-policy.v1"}, {kinds.BackupRepositoryRegistry, "backup", "bahia.registry.backup-repository.v1"}, {kinds.BackupRetentionRegistry, "backup", "bahia.registry.backup-retention.v1"}, {kinds.BackupRecipeRegistry, "backup", "bahia.registry.backup-recipe.v1"}, {kinds.BackupRunState, "backup", "bahia.state.backup-run.v1"}, {kinds.BackupVerificationState, "backup", "bahia.state.backup-verification.v1"}, {kinds.BackupRestoreState, "backup", "bahia.state.backup-restore.v1"}, {kinds.BackupRuntimeObservationState, "backup", "bahia.state.backup-observation.v1"}, {kinds.WorkerState, "worker", "bahia.state.worker.v1"}, {kinds.WorkerAssignmentState, "worker", "bahia.state.worker-assignment.v1"}, {kinds.WorkerDrainStatus, "worker", "bahia.state.worker-drain.v1"}, {kinds.WorkerEligibilityPreview, "worker", "bahia.state.worker-eligibility.v1"}, {kinds.ContinuityProfile, "continuity", "bahia.state.continuity-profile.v1"}, {kinds.FailoverPolicy, "continuity", "bahia.state.failover-policy.v1"}, {kinds.StandbyNodeDefinition, "continuity", "bahia.state.standby-node.v1"}, {kinds.ReplicationPolicy, "continuity", "bahia.state.replication-policy.v1"}, {kinds.RecoveryWorkflow, "continuity", "bahia.state.recovery-workflow.v1"}, {kinds.BahiaIdentityDefinition, "system", "bahia.identity.v1"}, {kinds.BahiaReplayCheckpoint, "system", "bahia.replay-checkpoint.v1"}} {
		addState(item.kind, item.domain, item.schema)
	}

	m[kinds.SystemDiscovery] = Disposition{LegacyKind: kinds.SystemDiscovery, CanonicalKind: CanonicalContextVMDiscovery, Layer: LayerDiscovery, Domain: "system", Operation: "discover", Schema: "bahia.system-discovery.v1", DTagPrefix: "bahia-system-v1"}
	m[kinds.RelaySetDiscovery] = Disposition{LegacyKind: kinds.RelaySetDiscovery, CanonicalKind: CanonicalNIP51RelaySet, Layer: LayerCollection, Domain: "relay", Operation: "relay-set", Schema: "bahia.relay-set.v1", DTagPrefix: "relays"}
	m[kinds.SBOMAttestation] = Disposition{LegacyKind: kinds.SBOMAttestation, CanonicalKind: CanonicalNIP78AppData, Layer: LayerAppData, Domain: "sbom", Operation: "app-data", Schema: "bahia.sbom.attestation.v1", DTagPrefix: "sbom"}
	m[kinds.SBOMIndex] = Disposition{LegacyKind: kinds.SBOMIndex, CanonicalKind: CanonicalNIP78AppData, Layer: LayerAppData, Domain: "sbom", Operation: "app-data", Schema: "bahia.sbom.index.v1", DTagPrefix: "sbom"}

	for kind := kinds.AuditMin; kind <= kinds.DNSEndpointDeregisteredAudit; kind++ {
		addAudit(kind, "audit", "bahia.audit.v1")
	}
	addAudit(kinds.BackupRunAttestation, "backup", "bahia.audit.backup-run-attestation.v1")
	addAudit(kinds.BackupVerificationAttestation, "backup", "bahia.audit.backup-verification-attestation.v1")
	return m
}
