package kinds

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"

	cascadia "git.sharegap.net/cascadia/cascadia-go"
)

func TestCascadiaGeneratedKindAliasesStayCanonical(t *testing.T) {
	if CASAudit != cascadia.CAS_AUDIT {
		t.Fatalf("CASAudit = %d, want cascadia.CAS_AUDIT %d", CASAudit, cascadia.CAS_AUDIT)
	}
	if CASAudit != 4903 {
		t.Fatalf("CASAudit = %d, want 4903", CASAudit)
	}
	if ContextVMMessage != cascadia.CAS_INTENT {
		t.Fatalf("ContextVMMessage = %d, want cascadia.CAS_INTENT %d", ContextVMMessage, cascadia.CAS_INTENT)
	}
	if ContextVMMessage != 25910 {
		t.Fatalf("ContextVMMessage = %d, want 25910", ContextVMMessage)
	}
	if SoulFactoryRuntimeCapability != cascadia.CAS_AGENT_CAPABILITY {
		t.Fatalf("SoulFactoryRuntimeCapability = %d, want cascadia.CAS_AGENT_CAPABILITY %d", SoulFactoryRuntimeCapability, cascadia.CAS_AGENT_CAPABILITY)
	}
	if SoulFactoryRuntimeCapability != 30317 {
		t.Fatalf("SoulFactoryRuntimeCapability = %d, want 30317", SoulFactoryRuntimeCapability)
	}
	if NIP38Status != cascadia.NIP38_USER_STATUS || HeartbeatObservation != cascadia.NIP38_USER_STATUS {
		t.Fatalf("NIP-38 aliases = (%d, %d), want cascadia.NIP38_USER_STATUS %d", NIP38Status, HeartbeatObservation, cascadia.NIP38_USER_STATUS)
	}
	if CASControlState != cascadia.CAS_CP_STATE {
		t.Fatalf("CASControlState = %d, want cascadia.CAS_CP_STATE %d", CASControlState, cascadia.CAS_CP_STATE)
	}
}

func TestGeneratedFrontendKindsMatchCanonicalGoKinds(t *testing.T) {
	repo := repositoryRoot(t)
	goKinds := parseGoKindConstants(t, filepath.Join(repo, "internal", "kinds", "kinds.go"))
	jsKinds := parseGeneratedJSKindConstants(t, filepath.Join(repo, "web", "src", "lib", "nostr", "kinds.gen.js"))
	for name, goValue := range goKinds {
		jsName := goConstNameToJS(name)
		jsValue, ok := jsKinds[jsName]
		if !ok {
			t.Fatalf("web/src/lib/nostr/kinds.gen.js missing generated constant %s for internal/kinds.%s", jsName, name)
		}
		if jsValue != goValue {
			t.Fatalf("kind drift for %s: frontend %d, internal/kinds %d", jsName, jsValue, goValue)
		}
	}
}

// TestKindCatalogDefinesNoWorkerStateWireKinds guards C-43: worker read models
// are 30900 cp-state records whose family is a CPStateFamily discriminator, so
// the kind catalog must not reintroduce 32000-32004 as publishable kinds.
func TestKindCatalogDefinesNoWorkerStateWireKinds(t *testing.T) {
	goKinds := parseGoKindConstants(t, filepath.Join(repositoryRoot(t), "internal", "kinds", "kinds.go"))
	for name, value := range goKinds {
		for _, family := range []CPStateFamily{CPStateFamilyWorkerState, CPStateFamilyWorkerAssignment, CPStateFamilyWorkerDrain, CPStateFamilyWorkerEligibility, CPStateFamilyWorkerCleanup} {
			if value == family.LegacyKind() {
				t.Fatalf("internal/kinds.%s = %d reintroduces a worker cp-state family as a wire kind; use kinds.CPStateFamily", name, value)
			}
		}
	}
}

// TestGeneratedFrontendControlStateStringsMatchGo keeps the web's copy of the
// canonical control-state envelope strings in step with the producer's.
func TestGeneratedFrontendControlStateStringsMatchGo(t *testing.T) {
	path := filepath.Join(repositoryRoot(t), "web", "src", "lib", "nostr", "kinds.gen.js")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	re := regexp.MustCompile(`(?m)^export const ([A-Z0-9_]+) = '([^']*)';$`)
	jsStrings := map[string]string{}
	for _, match := range re.FindAllStringSubmatch(string(content), -1) {
		jsStrings[match[1]] = match[2]
	}
	for jsName, goValue := range map[string]string{
		"BAHIA_CP_STATE_SCHEMA": CASControlStateSchema,
		"DNS_STATE_DOMAIN":      DNSDomain,
		"DNS_ZONE_TOPIC":        DNSZoneTopic,
		"DNS_ENDPOINT_TOPIC":    DNSEndpointTopic,
		"DNS_POLICY_TOPIC":      DNSPolicyTopic,
		"DNS_BACKEND_TOPIC":     DNSBackendTopic,
	} {
		if got, ok := jsStrings[jsName]; !ok || got != goValue {
			t.Fatalf("kinds.gen.js %s = %q (present=%t), want internal/kinds value %q", jsName, got, ok, goValue)
		}
	}
}

// TestGeneratedFrontendWorkerCatalogKindsMatchGo keeps the web's legacy_kind
// values for the worker cp-state families equal to the CPStateFamily
// discriminators producers stamp.
func TestGeneratedFrontendWorkerCatalogKindsMatchGo(t *testing.T) {
	jsKinds := parseGeneratedJSKindConstants(t, filepath.Join(repositoryRoot(t), "web", "src", "lib", "nostr", "kinds.gen.js"))
	for jsName, family := range map[string]CPStateFamily{
		"WORKER_STATE_CATALOG_KIND":               CPStateFamilyWorkerState,
		"WORKER_ASSIGNMENT_STATE_CATALOG_KIND":    CPStateFamilyWorkerAssignment,
		"WORKER_DRAIN_STATUS_CATALOG_KIND":        CPStateFamilyWorkerDrain,
		"WORKER_ELIGIBILITY_PREVIEW_CATALOG_KIND": CPStateFamilyWorkerEligibility,
		"WORKER_CLEANUP_EXECUTION_CATALOG_KIND":   CPStateFamilyWorkerCleanup,
	} {
		if got, ok := jsKinds[jsName]; !ok || got != family.LegacyKind() {
			t.Fatalf("kinds.gen.js %s = %d (present=%t), want kinds.CPStateFamily %d", jsName, got, ok, family.LegacyKind())
		}
	}
}

// TestGeneratedFrontendWorkerTopicsMatchGo keeps the web's worker domain and
// #t topics equal to the ones the worker cp-state producers stamp.
func TestGeneratedFrontendWorkerTopicsMatchGo(t *testing.T) {
	path := filepath.Join(repositoryRoot(t), "web", "src", "lib", "nostr", "kinds.gen.js")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	re := regexp.MustCompile(`(?m)^export const ([A-Z0-9_]+) = '([^']*)';$`)
	jsStrings := map[string]string{}
	for _, match := range re.FindAllStringSubmatch(string(content), -1) {
		jsStrings[match[1]] = match[2]
	}
	for jsName, goValue := range map[string]string{
		"WORKER_STATE_DOMAIN":              WorkerDomain,
		"WORKER_STATE_TOPIC":               WorkerStateTopic,
		"WORKER_ASSIGNMENT_STATE_TOPIC":    WorkerAssignmentTopic,
		"WORKER_DRAIN_STATUS_TOPIC":        WorkerDrainTopic,
		"WORKER_ELIGIBILITY_PREVIEW_TOPIC": WorkerEligibilityTopic,
		"WORKER_CLEANUP_EXECUTION_TOPIC":   WorkerCleanupTopic,
	} {
		if got, ok := jsStrings[jsName]; !ok || got != goValue {
			t.Fatalf("kinds.gen.js %s = %q (present=%t), want internal/kinds value %q", jsName, got, ok, goValue)
		}
	}
}

// TestGeneratedFrontendWorkerCoordinatesMatchGo keeps the web's worker d
// prefixes equal to the canonical worker d builder's (bahia-irsry.36), so web
// consumers read the record id off the same coordinate producers publish on.
func TestGeneratedFrontendWorkerCoordinatesMatchGo(t *testing.T) {
	jsStrings := parseGeneratedJSStringConstants(t)
	for jsName, family := range map[string]CPStateFamily{
		"WORKER_STATE_D_PREFIX":               CPStateFamilyWorkerState,
		"WORKER_ASSIGNMENT_STATE_D_PREFIX":    CPStateFamilyWorkerAssignment,
		"WORKER_DRAIN_STATUS_D_PREFIX":        CPStateFamilyWorkerDrain,
		"WORKER_ELIGIBILITY_PREVIEW_D_PREFIX": CPStateFamilyWorkerEligibility,
		"WORKER_CLEANUP_EXECUTION_D_PREFIX":   CPStateFamilyWorkerCleanup,
	} {
		prefix, ok := family.WorkerDPrefix()
		if !ok {
			t.Fatalf("family %d has no worker d prefix", family)
		}
		if got, present := jsStrings[jsName]; !present || got != prefix {
			t.Fatalf("kinds.gen.js %s = %q (present=%t), want internal/kinds value %q", jsName, got, present, prefix)
		}
	}
}

// TestWorkerDTagGivesEveryWorkerFamilyItsOwnCoordinate pins bahia-irsry.36:
// the same record id (a worker pubkey) yields a distinct d per worker family,
// and non-worker families have no worker coordinate.
func TestWorkerDTagGivesEveryWorkerFamilyItsOwnCoordinate(t *testing.T) {
	const pubkey = "0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f"
	seen := map[string]CPStateFamily{}
	for _, family := range []CPStateFamily{CPStateFamilyWorkerState, CPStateFamilyWorkerAssignment, CPStateFamilyWorkerDrain, CPStateFamilyWorkerEligibility, CPStateFamilyWorkerCleanup} {
		d, ok := family.WorkerDTag(pubkey)
		if !ok || !strings.HasPrefix(d, "worker:") || !strings.HasSuffix(d, ":"+pubkey) {
			t.Fatalf("family %d WorkerDTag = %q, %t", family, d, ok)
		}
		if other, dup := seen[d]; dup {
			t.Fatalf("families %d and %d share d=%q", other, family, d)
		}
		seen[d] = family
	}
	if want := "worker:assignment:" + pubkey; mustWorkerDTag(t, CPStateFamilyWorkerAssignment, pubkey) != want {
		t.Fatalf("assignment d = %q, want %q", mustWorkerDTag(t, CPStateFamilyWorkerAssignment, pubkey), want)
	}
	if want := "worker:drain:" + pubkey; mustWorkerDTag(t, CPStateFamilyWorkerDrain, pubkey) != want {
		t.Fatalf("drain d = %q, want %q", mustWorkerDTag(t, CPStateFamilyWorkerDrain, pubkey), want)
	}
	if _, ok := CPStateFamilyDNSEndpoint.WorkerDTag(pubkey); ok {
		t.Fatal("DNS endpoint family must not get a worker coordinate")
	}
}

func mustWorkerDTag(t *testing.T, family CPStateFamily, id string) string {
	t.Helper()
	d, ok := family.WorkerDTag(id)
	if !ok {
		t.Fatalf("family %d has no worker coordinate", family)
	}
	return d
}

// TestGeneratedFrontendAssistantAndRelaySettingsTopicsMatchGo keeps the web's
// single-letter topics for assistant transcript/status and relay settings
// equal to the ones the producers stamp (bahia-irsry.37).
func TestGeneratedFrontendAssistantAndRelaySettingsTopicsMatchGo(t *testing.T) {
	jsStrings := parseGeneratedJSStringConstants(t)
	for jsName, goValue := range map[string]string{
		"ASSISTANT_TRANSCRIPT_TOPIC":                AssistantTranscriptTopic,
		"ASSISTANT_TRANSCRIPT_SESSION_TOPIC_PREFIX": AssistantTranscriptSessionTopicPrefix,
		"ASSISTANT_STATUS_TOPIC":                    AssistantStatusTopic,
		"RELAY_SETTINGS_TOPIC":                      RelaySettingsTopic,
	} {
		if got, ok := jsStrings[jsName]; !ok || got != goValue {
			t.Fatalf("kinds.gen.js %s = %q (present=%t), want internal/kinds value %q", jsName, got, ok, goValue)
		}
	}
	if got := AssistantTranscriptSessionTopic("s-1"); got != AssistantTranscriptSessionTopicPrefix+"s-1" {
		t.Fatalf("AssistantTranscriptSessionTopic = %q", got)
	}
}

// TestRetiredAuditKindsAreNotDeclared guards bahia-irsry.37: the retired
// addressable audit kinds 31000-31099 are decoded only by
// internal/nostrmigration, so neither kinds.go nor kinds.gen.js declares them.
func TestRetiredAuditKindsAreNotDeclared(t *testing.T) {
	repo := repositoryRoot(t)
	for name, value := range parseGoKindConstants(t, filepath.Join(repo, "internal", "kinds", "kinds.go")) {
		if value >= 31000 && value <= 31099 {
			t.Errorf("internal/kinds.%s = %d redeclares a retired audit kind; audits are 4903", name, value)
		}
	}
	for name, value := range parseGeneratedJSKindConstants(t, filepath.Join(repo, "web", "src", "lib", "nostr", "kinds.gen.js")) {
		if value >= 31000 && value <= 31099 {
			t.Errorf("kinds.gen.js %s = %d redeclares a retired audit kind; audits are 4903", name, value)
		}
	}
}

func parseGeneratedJSStringConstants(t *testing.T) map[string]string {
	t.Helper()
	path := filepath.Join(repositoryRoot(t), "web", "src", "lib", "nostr", "kinds.gen.js")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	re := regexp.MustCompile(`(?m)^export const ([A-Z0-9_]+) = '([^']*)';$`)
	out := map[string]string{}
	for _, match := range re.FindAllStringSubmatch(string(content), -1) {
		out[match[1]] = match[2]
	}
	return out
}

func parseGoKindConstants(t *testing.T, path string) map[string]int {
	t.Helper()
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, path, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	out := map[string]int{}
	for _, decl := range file.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.CONST {
			continue
		}
		for _, spec := range genDecl.Specs {
			valueSpec := spec.(*ast.ValueSpec)
			for i, ident := range valueSpec.Names {
				if len(valueSpec.Values) <= i {
					continue
				}
				literal, ok := valueSpec.Values[i].(*ast.BasicLit)
				if !ok || literal.Kind != token.INT {
					continue
				}
				value, err := strconv.Atoi(literal.Value)
				if err != nil {
					t.Fatalf("parse int for %s: %v", ident.Name, err)
				}
				out[ident.Name] = value
			}
		}
	}
	return out
}

func parseGeneratedJSKindConstants(t *testing.T, path string) map[string]int {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	re := regexp.MustCompile(`(?m)^export const ([A-Z0-9_]+) = ([0-9]+);$`)
	out := map[string]int{}
	for _, match := range re.FindAllStringSubmatch(string(content), -1) {
		value, err := strconv.Atoi(match[2])
		if err != nil {
			t.Fatalf("parse generated value for %s: %v", match[1], err)
		}
		out[match[1]] = value
	}
	return out
}

func goConstNameToJS(name string) string {
	var out []rune
	runes := []rune(name)
	for i, r := range runes {
		if i > 0 && shouldInsertUnderscore(runes, i) {
			out = append(out, '_')
		}
		out = append(out, toUpperASCII(r))
	}
	return string(out)
}

func shouldInsertUnderscore(runes []rune, i int) bool {
	prev := runes[i-1]
	cur := runes[i]
	if isDigitASCII(cur) {
		return isLowerASCII(prev)
	}
	if !isUpperASCII(cur) {
		return false
	}
	if isLowerASCII(prev) || isDigitASCII(prev) {
		return true
	}
	return i+1 < len(runes) && isUpperASCII(prev) && isLowerASCII(runes[i+1])
}

func toUpperASCII(r rune) rune {
	if r >= 'a' && r <= 'z' {
		return r - ('a' - 'A')
	}
	return r
}

func isUpperASCII(r rune) bool { return r >= 'A' && r <= 'Z' }
func isLowerASCII(r rune) bool { return r >= 'a' && r <= 'z' }
func isDigitASCII(r rune) bool { return r >= '0' && r <= '9' }

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(filename)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir || strings.TrimSpace(parent) == "" {
			t.Fatalf("could not find repository root from %s", filename)
		}
		dir = parent
	}
}
