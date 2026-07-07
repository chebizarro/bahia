package nostrmigration

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	cascadia "git.sharegap.net/cascadia/cascadia-go"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
)

func TestManifestCoversBahiaInventoryLegacyKinds(t *testing.T) {
	want := []int{}
	appendRange := func(start, end int) {
		for kind := start; kind <= end; kind++ {
			want = append(want, kind)
		}
	}
	appendRange(5941, 5945)
	want = append(want, 5980, 6006, 6941, 6981, 6982, 6983, 6984, 7941, 7942, 7943, 7944, 7945, 7980)
	for _, kind := range []int{5961, 5962, 5963, 5964, 5965, 5966, 5967, 5968, 5971, 5972, 5973, 5974, 5975, 5976, 5977, 5978, 5979, 5981, 5982, 5983, 5984, 5985, 5986, 5987, 5988, 5989, 5991, 5992, 5993, 5994, 5995, 5996, 5997, 5998, 5999, 6000, 6001, 6002, 6003, 6004, 6005} {
		want = append(want, kind)
	}
	for _, kind := range []int{6961, 6962, 6963, 6973, 6976, 6978, 6991, 6997} {
		want = append(want, kind)
	}
	for _, kind := range []int{7961, 7962, 7963, 7964, 7965, 7966, 7971, 7972, 7973, 7976, 7977, 7978, 7979, 7991, 7992, 7997} {
		want = append(want, kind)
	}
	appendRange(31000, 31024)
	appendRange(31100, 31105)
	want = append(want, 31310, 31311, 31400, 31401, 31402, 31403, 31404, 31410, 31411, 30350, 30351, 30352, 30353, 30360)
	appendRange(31961, 31978)
	appendRange(31980, 32003)
	appendRange(38390, 38399)
	appendRange(38400, 38423)
	want = append(want, 38430, 38431, 30002, 30078, 30079)

	for _, kind := range want {
		disp, ok := Lookup(kind)
		require.Truef(t, ok, "missing migration disposition for kind %d", kind)
		require.NotZero(t, disp.CanonicalKind, "canonical kind missing for %d", kind)
		require.NotEmpty(t, disp.Layer, "layer missing for %d", kind)
		require.NotEmpty(t, disp.Domain, "domain missing for %d", kind)
		require.NotEmpty(t, disp.Schema, "schema missing for %d", kind)
	}
}

func TestManifestCanonicalTargets(t *testing.T) {
	deploy, ok := Lookup(kinds.DeployRequest)
	require.True(t, ok)
	require.Equal(t, CanonicalContextVMMessage, deploy.CanonicalKind)
	require.Equal(t, "service/deploy", deploy.Method)

	serviceDelete, ok := Lookup(kinds.ServiceDelete)
	require.True(t, ok)
	require.Equal(t, CanonicalNIP09Delete, serviceDelete.CanonicalKind)
	require.True(t, serviceDelete.Delete)

	state, ok := Lookup(kinds.ServiceState)
	require.True(t, ok)
	require.Equal(t, CanonicalCASCPState, state.CanonicalKind)
	require.Equal(t, LayerState, state.Layer)

	audit, ok := Lookup(kinds.DeploymentCreated)
	require.True(t, ok)
	require.Equal(t, CanonicalCASAudit, audit.CanonicalKind)

	discovery, ok := Lookup(kinds.SystemDiscovery)
	require.True(t, ok)
	require.Equal(t, CanonicalContextVMDiscovery, discovery.CanonicalKind)

	legacySBOMIndex, ok := Lookup(kinds.LegacySBOMIndex)
	require.True(t, ok)
	require.Equal(t, CanonicalNIP51AvailabilityList, legacySBOMIndex.CanonicalKind)
	require.Equal(t, LayerCollection, legacySBOMIndex.Layer)
	require.Equal(t, "bahia.sbom.available-list.v1", legacySBOMIndex.Schema)
}

func TestResolveDispositionDisambiguatesConflictingLegacyWorkerAliases(t *testing.T) {
	testCases := []struct {
		name       string
		kind       int
		tags       [][]string
		content    string
		wantSchema string
		wantDomain string
		wantLayer  EventLayer
		wantKind   int
	}{
		{
			name:       "legacy worker state via worker tag",
			kind:       kinds.LegacyWorkerState,
			tags:       [][]string{{"worker", "worker-pubkey"}},
			content:    `{"pubkey":"worker-pubkey","name":"w1"}`,
			wantSchema: "bahia.state.worker.v1",
			wantDomain: "worker",
			wantLayer:  LayerState,
			wantKind:   CanonicalCASCPState,
		},
		{
			name:       "legacy assignment state via JSON shape",
			kind:       kinds.LegacyWorkerAssignmentState,
			content:    `{"worker_pubkey":"worker-pubkey","active_assignments":[],"updated_at":"2026-06-02T00:00:00Z"}`,
			wantSchema: "bahia.state.worker-assignment.v1",
			wantDomain: "worker",
			wantLayer:  LayerState,
			wantKind:   CanonicalCASCPState,
		},
		{
			name:       "legacy drain state via JSON shape",
			kind:       kinds.LegacyWorkerDrainStatus,
			content:    `{"worker_pubkey":"worker-pubkey","remaining_assignments":[],"safe_to_disable":true}`,
			wantSchema: "bahia.state.worker-drain.v1",
			wantDomain: "worker",
			wantLayer:  LayerState,
			wantKind:   CanonicalCASCPState,
		},
		{
			name:       "legacy eligibility preview via JSON shape",
			kind:       kinds.LegacyWorkerEligibilityPreview,
			content:    `{"preview_id":"preview-1","eligible_workers":[],"rejected_workers":[],"ranking_scores":[]}`,
			wantSchema: "bahia.state.worker-eligibility.v1",
			wantDomain: "worker",
			wantLayer:  LayerState,
			wantKind:   CanonicalCASCPState,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			disp, ok := ResolveDisposition(tc.kind, tagsJSON(t, tc.tags), tc.content)
			require.True(t, ok)
			require.Equal(t, tc.wantKind, disp.CanonicalKind)
			require.Equal(t, tc.wantLayer, disp.Layer)
			require.Equal(t, tc.wantDomain, disp.Domain)
			require.Equal(t, tc.wantSchema, disp.Schema)
		})
	}
}

func TestResolveDispositionKeepsPrimaryMappingsWithoutWorkerEvidence(t *testing.T) {
	discovery, ok := ResolveDisposition(kinds.SystemDiscovery, nil, `{"server":"bahia"}`)
	require.True(t, ok)
	require.Equal(t, CanonicalContextVMDiscovery, discovery.CanonicalKind)
	require.Equal(t, "system", discovery.Domain)

	backupDefinition, ok := ResolveDisposition(kinds.BackupDefinitionRegistry, nil, `{"backup_definition_id":"backup-def-1"}`)
	require.True(t, ok)
	require.Equal(t, CanonicalCASCPState, backupDefinition.CanonicalKind)
	require.Equal(t, "backup", backupDefinition.Domain)
	require.Equal(t, "bahia.registry.backup-definition.v1", backupDefinition.Schema)

	discoveryWithCapabilities, ok := ResolveDisposition(kinds.SystemDiscovery, nil, `{"server":"bahia","capabilities":["worker_read_models"]}`)
	require.True(t, ok)
	require.Equal(t, CanonicalContextVMDiscovery, discoveryWithCapabilities.CanonicalKind)
	require.Equal(t, "system", discoveryWithCapabilities.Domain)
}

func TestManifestDocumentsRequestedOmissionsAndAliases(t *testing.T) {
	required := map[string]int{
		"HiveCIWorkflowRun":              kinds.HiveCIWorkflowRun,
		"HiveCIWorkflowResult":           kinds.HiveCIWorkflowResult,
		"LoomWorkerAdvertisement":        kinds.LoomWorkerAdvertisement,
		"LoomJobStatusUpdate":            kinds.LoomJobStatusUpdate,
		"LoomJobResult":                  kinds.LoomJobResult,
		"LoomJobCancellation":            kinds.LoomJobCancellation,
		"NostrSignature":                 kinds.NostrSignature,
		"FIPSOverlayAdvert":              kinds.FIPSOverlayAdvert,
		"HTTPAuth":                       kinds.HTTPAuth,
		"NIP65RelayList":                 kinds.NIP65RelayList,
		"LongFormContent":                kinds.LongFormContent,
		"LongFormDraft":                  kinds.LongFormDraft,
		"ContextVMMessage":               kinds.ContextVMMessage,
		"ContextVMGiftWrap":              kinds.ContextVMGiftWrap,
		"ContextVMEphemeralGiftWrap":     kinds.ContextVMEphemeralGiftWrap,
		"ContextVMServerAnnouncement":    kinds.ContextVMServerAnnouncement,
		"ContextVMToolsList":             kinds.ContextVMToolsList,
		"ContextVMResourcesList":         kinds.ContextVMResourcesList,
		"ContextVMResourceTemplatesList": kinds.ContextVMResourceTemplatesList,
		"ContextVMPromptsList":           kinds.ContextVMPromptsList,
		"LegacyWorkerState":              kinds.LegacyWorkerState,
		"LegacyWorkerAssignmentState":    kinds.LegacyWorkerAssignmentState,
		"LegacyWorkerDrainStatus":        kinds.LegacyWorkerDrainStatus,
		"LegacyWorkerEligibilityPreview": kinds.LegacyWorkerEligibilityPreview,
	}

	for name, kind := range required {
		justification, ok := ConstantJustification(name, kind)
		require.Truef(t, ok, "missing manifest justification for %s=%d", name, kind)
		require.NotEmpty(t, justification.Category, "category missing for %s", name)
		require.NotEmpty(t, justification.Reason, "reason missing for %s", name)
	}
}

func TestKindConstantsAreMappedOrJustified(t *testing.T) {
	constants := parseKindsGoConstants(t)
	for name, kind := range constants {
		if _, mapped := Lookup(kind); mapped {
			continue
		}
		justification, ok := ConstantJustification(name, kind)
		require.Truef(t, ok, "internal/kinds.%s=%d is neither mapped in the migration manifest nor explicitly justified", name, kind)
		require.NotEmpty(t, justification.Reason, "empty justification for internal/kinds.%s=%d", name, kind)
	}

	primaryDuplicateNames := map[int]string{
		kinds.BuildRegistered:          "BuildRegistered",
		kinds.SystemDiscovery:          "SystemDiscovery",
		kinds.BackupDefinitionRegistry: "BackupDefinitionRegistry",
		kinds.BackupPolicyRegistry:     "BackupPolicyRegistry",
		kinds.BackupRepositoryRegistry: "BackupRepositoryRegistry",
		kinds.SBOMReference:            "SBOMReference",
		kinds.LegacySBOMIndex:          "LegacySBOMIndex",
	}
	for kind, names := range constantsByKind(constants) {
		if len(names) < 2 {
			continue
		}
		primary := primaryDuplicateNames[kind]
		for _, name := range names {
			if name == primary {
				continue
			}
			justification, ok := ConstantJustification(name, kind)
			require.Truef(t, ok, "duplicate internal/kinds.%s=%d needs explicit manifest justification or primary duplicate declaration", name, kind)
			require.NotEmpty(t, justification.Reason, "empty duplicate-kind justification for internal/kinds.%s=%d", name, kind)
		}
	}

	for _, name := range []string{"LegacyWorkerState", "LegacyWorkerAssignmentState", "LegacyWorkerDrainStatus", "LegacyWorkerEligibilityPreview"} {
		justification, ok := ConstantJustification(name, constants[name])
		require.Truef(t, ok, "duplicate worker alias %s lacks explicit manifest coverage", name)
		require.Equal(t, "conflicting-alias", justification.Category)
	}
}

func constantsByKind(constants map[string]int) map[int][]string {
	out := map[int][]string{}
	for name, kind := range constants {
		out[kind] = append(out[kind], name)
	}
	for kind := range out {
		sort.Strings(out[kind])
	}
	return out
}

func tagsJSON(t *testing.T, tags [][]string) []byte {
	t.Helper()
	if tags == nil {
		return nil
	}
	out, err := json.Marshal(tags)
	require.NoError(t, err)
	return out
}

func parseKindsGoConstants(t *testing.T) map[string]int {
	t.Helper()
	fileSet := token.NewFileSet()
	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed")
	path := filepath.Join(filepath.Dir(filename), "..", "kinds", "kinds.go")
	file, err := parser.ParseFile(fileSet, path, nil, parser.ParseComments)
	require.NoError(t, err)

	cascadiaConstants := map[string]int{
		"CAS_AUDIT":                             cascadia.CAS_INTENT,
		"CAS_AGENT_HEARTBEAT":                   cascadia.CAS_AGENT_HEARTBEAT,
		"CAS_CP_STATE":                          cascadia.CAS_CP_STATE,
		"CAS_WORKER_AD":                         cascadia.CAS_WORKER_AD,
		"CAS_INTENT":                            cascadia.CAS_INTENT,
		"NIP59_GIFT_WRAP":                       cascadia.NIP59_GIFT_WRAP,
		"NIP59_EPHEMERAL_GIFT_WRAP":             cascadia.NIP59_EPHEMERAL_GIFT_WRAP,
		"CTXVM_SERVER_ANNOUNCEMENT":             cascadia.CTXVM_SERVER_ANNOUNCEMENT,
		"CTXVM_TOOLS_ANNOUNCEMENT":              cascadia.CTXVM_TOOLS_ANNOUNCEMENT,
		"CTXVM_RESOURCES_ANNOUNCEMENT":          cascadia.CTXVM_RESOURCES_ANNOUNCEMENT,
		"CTXVM_RESOURCE_TEMPLATES_ANNOUNCEMENT": cascadia.CTXVM_RESOURCE_TEMPLATES_ANNOUNCEMENT,
		"CTXVM_PROMPTS_ANNOUNCEMENT":            cascadia.CTXVM_PROMPTS_ANNOUNCEMENT,
	}

	out := map[string]int{}
	for _, decl := range file.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.CONST {
			continue
		}
		for _, spec := range genDecl.Specs {
			valueSpec, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range valueSpec.Names {
				if strings.TrimSpace(name.Name) == "" || i >= len(valueSpec.Values) {
					continue
				}
				switch expr := valueSpec.Values[i].(type) {
				case *ast.BasicLit:
					var value int
					_, err := fmt.Sscanf(expr.Value, "%d", &value)
					require.NoErrorf(t, err, "constant %s is not an integer literal", name.Name)
					out[name.Name] = value
				case *ast.SelectorExpr:
					pkg, ok := expr.X.(*ast.Ident)
					if ok && pkg.Name == "cascadia" {
						value, ok := cascadiaConstants[expr.Sel.Name]
						require.Truef(t, ok, "unmapped cascadia constant %s", expr.Sel.Name)
						out[name.Name] = value
					}
				case *ast.Ident:
					value, ok := out[expr.Name]
					if ok {
						out[name.Name] = value
					}
				}
			}
		}
	}
	require.NotEmpty(t, out, "parsed no constants from %s", path)
	return out
}
