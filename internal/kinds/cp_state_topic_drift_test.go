package kinds

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// goCPStateTopics maps each CP_STATE_TOPICS key in kinds.gen.js to the Go
// constant the projector stamps.
var goCPStateTopics = map[string]string{
	"SERVICE_STATE":              CPStateTopicServiceState,
	"SERVICE_REGISTRY":           CPStateTopicServiceRegistry,
	"ENVIRONMENT_REGISTRY":       CPStateTopicEnvironmentRegistry,
	"LLM_ROUTE":                  CPStateTopicLLMRoute,
	"LLM_STATE":                  CPStateTopicLLMState,
	"ARTIFACT_REGISTRY":          CPStateTopicArtifactRegistry,
	"DEPLOYMENT_INTENT":          CPStateTopicDeploymentIntent,
	"DEPLOYMENT_RUN":             CPStateTopicDeploymentRun,
	"BUILD_REGISTRY":             CPStateTopicBuildRegistry,
	"POLICY_REGISTRY":            CPStateTopicPolicyRegistry,
	"PACKAGE_REPOSITORY":         CPStateTopicPackageRepository,
	"PACKAGE_ARTIFACT":           CPStateTopicPackageArtifact,
	"PACKAGE_PROMOTION":          CPStateTopicPackagePromotion,
	"ML_MODEL":                   CPStateTopicMLModel,
	"ML_MODEL_VERSION":           CPStateTopicMLModelVersion,
	"ML_DATASET":                 CPStateTopicMLDataset,
	"ML_RECIPE":                  CPStateTopicMLRecipe,
	"ML_RECIPE_RUN":              CPStateTopicMLRecipeRun,
	"ML_ENDPOINT":                CPStateTopicMLEndpoint,
	"ML_ENDPOINT_STATE":          CPStateTopicMLEndpointState,
	"ML_EVALUATION":              CPStateTopicMLEvaluation,
	"ML_PROVENANCE":              CPStateTopicMLProvenance,
	"ML_RUNTIME_CAPABILITY":      CPStateTopicMLRuntimeCapability,
	"BACKUP_DEFINITION":          CPStateTopicBackupDefinition,
	"BACKUP_POLICY":              CPStateTopicBackupPolicy,
	"BACKUP_REPOSITORY":          CPStateTopicBackupRepository,
	"BACKUP_RETENTION":           CPStateTopicBackupRetention,
	"BACKUP_RECIPE":              CPStateTopicBackupRecipe,
	"BACKUP_RUN":                 CPStateTopicBackupRun,
	"BACKUP_VERIFICATION":        CPStateTopicBackupVerification,
	"BACKUP_RESTORE":             CPStateTopicBackupRestore,
	"BACKUP_RUNTIME_OBSERVATION": CPStateTopicBackupRuntimeObservation,
	"SECRET_REGISTRY":            CPStateTopicSecretRegistry,
	"NOTIFICATION_CHANNEL":       CPStateTopicNotificationChannelRegistry,
	"ORG_KEY_ENVELOPE":           CPStateTopicOrgKeyEnvelope,
	"PAYMENT_RECORD":             CPStateTopicPaymentRecord,
	"SECURITY_FINDING":           CPStateTopicSecurityFinding,
	"SECURITY_SCHEDULE":          CPStateTopicSecuritySchedule,
	"SECURITY_FINDING_DETAIL":    CPStateTopicSecurityFindingDetail,
	"MANAGED_INSTANCE_HEALTH":    CPStateTopicManagedInstanceHealth,
	"ROUTE_CANARY":               CPStateTopicRouteCanary,
	"SOUL_RUNTIME_POLICY":        CPStateTopicSoulRuntimePolicy,
	"BLOSSOM_ADMIN":              CPStateTopicBlossomAdmin,
	"BLOSSOM_BLOB":               CPStateTopicBlossomBlob,
}

func readGeneratedKindsJS(t *testing.T) string {
	t.Helper()
	path := filepath.Join(repositoryRoot(t), "web", "src", "lib", "nostr", "kinds.gen.js")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content)
}

// TestGeneratedFrontendCPStateTopicsMatchGo keeps the web's #t filter values
// equal to the topics the projector stamps (bahia-irsry.9.3).
func TestGeneratedFrontendCPStateTopicsMatchGo(t *testing.T) {
	content := readGeneratedKindsJS(t)
	block := regexp.MustCompile(`(?s)export const CP_STATE_TOPICS = Object\.freeze\(\{(.*?)\n\}\);`).FindStringSubmatch(content)
	if block == nil {
		t.Fatal("kinds.gen.js has no CP_STATE_TOPICS block")
	}
	jsTopics := map[string]string{}
	for _, m := range regexp.MustCompile(`(?m)^\s+([A-Z0-9_]+): '([^']*)',?$`).FindAllStringSubmatch(block[1], -1) {
		jsTopics[m[1]] = m[2]
	}
	if len(jsTopics) != len(goCPStateTopics) {
		t.Fatalf("kinds.gen.js CP_STATE_TOPICS has %d entries, internal/kinds declares %d", len(jsTopics), len(goCPStateTopics))
	}
	seen := map[string]string{}
	for key, goValue := range goCPStateTopics {
		if got, ok := jsTopics[key]; !ok || got != goValue {
			t.Fatalf("kinds.gen.js CP_STATE_TOPICS.%s = %q (present=%t), want %q", key, got, ok, goValue)
		}
		if other, dup := seen[goValue]; dup {
			t.Fatalf("topic %q declared for both %s and %s", goValue, other, key)
		}
		seen[goValue] = key
	}
}

func TestGeneratedFrontendCPAuditStringsMatchGo(t *testing.T) {
	content := readGeneratedKindsJS(t)
	for jsName, goValue := range map[string]string{
		"CP_AUDIT_TOPIC":     CPAuditTopic,
		"CP_AUDIT_TAG_STATE": CPAuditTagState,
		"CP_AUDIT_TAG_FACT":  CPAuditTagFact,
	} {
		m := regexp.MustCompile(`(?m)^export const ` + jsName + ` = '([^']*)';$`).FindStringSubmatch(content)
		if m == nil || m[1] != goValue {
			t.Fatalf("kinds.gen.js %s = %v, want %q", jsName, m, goValue)
		}
	}
}
