package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// k8sTestSpec returns a minimal DesiredServiceSpec for Kubernetes tests.
// Re-uses testServiceID / testEnvironmentID / testArtifactID from
// docker_desired_state_test.go (same package).
func k8sTestSpec() *domain.DesiredServiceSpec {
	spec := &domain.DesiredServiceSpec{
		SchemaVersion:    domain.DesiredStateSchemaVersion,
		ServiceID:        testServiceID,
		EnvironmentID:    testEnvironmentID,
		ArtifactID:       testArtifactID,
		StableServiceKey: "my-api",
		ImageRef:         "registry.example/api:v1.2.3",
		Command:          []string{"/app/server"},
		Env: map[string]string{
			"APP_ENV":   "production",
			"LOG_LEVEL": "info",
		},
		Ports:   []string{"8080:80"},
		Volumes: []string{"/data/api:/app/data:ro"},
		Labels: map[string]string{
			"bahia.managed":        "true",
			"bahia.service_id":     testServiceID.String(),
			"bahia.environment_id": testEnvironmentID.String(),
			"bahia.artifact_id":    testArtifactID.String(),
		},
		RestartPolicy:       "always",
		KubernetesExtension: &domain.KubernetesExtension{},
	}
	spec.ComputeDesiredHash()
	spec.Labels["bahia.desired_hash"] = spec.DesiredHash
	return spec
}

// mockK8sRunner is a test double that records all execCommand calls and
// returns queued responses in order. Once the queue is exhausted, it returns
// an empty Kubernetes deployment list as the default.
type mockK8sRunner struct {
	mu    sync.Mutex
	calls []mockK8sCall
	queue []mockK8sResponse
}

type mockK8sCall struct {
	args  []string
	stdin []byte
}

type mockK8sResponse struct {
	output string
	err    error
}

func (m *mockK8sRunner) run(ctx context.Context, args []string, stdin []byte) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, mockK8sCall{args: append([]string(nil), args...), stdin: stdin})
	if len(m.queue) > 0 {
		resp := m.queue[0]
		m.queue = m.queue[1:]
		return resp.output, resp.err
	}
	return emptyK8sDeploymentList, nil
}

func (m *mockK8sRunner) enqueue(output string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queue = append(m.queue, mockK8sResponse{output: output, err: err})
}

func (m *mockK8sRunner) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

// newTestK8sRuntime creates a KubernetesRuntime wired to the given mock runner.
func newTestK8sRuntime(runner *mockK8sRunner) *KubernetesRuntime {
	k := NewKubernetesRuntime("", "default", "", zap.NewNop())
	k.execCmd = runner.run
	return k
}

// emptyK8sDeploymentList is the kubectl JSON response for no deployments found.
const emptyK8sDeploymentList = `{"items":[]}`

// existingDeploymentResponse builds the kubectl JSON response simulating one
// existing Bahia-managed deployment with the specified desired hash.
func existingDeploymentResponse(spec *domain.DesiredServiceSpec, hash string) string {
	resp := map[string]any{
		"items": []any{
			map[string]any{
				"metadata": map[string]any{
					"name":      BahiaK8sDeploymentName(spec),
					"namespace": "default",
					"labels": map[string]string{
						"bahia.service_id":     spec.ServiceID.String(),
						"bahia.environment_id": spec.EnvironmentID.String(),
					},
					"annotations": map[string]string{
						"bahia.desired_hash": hash,
					},
				},
				"spec": map[string]any{"replicas": int32(1)},
			},
		},
	}
	data, _ := json.Marshal(resp)
	return string(data)
}

// ---------------------------------------------------------------------------
// BahiaK8sDeploymentName
// ---------------------------------------------------------------------------

func TestBahiaK8sDeploymentName(t *testing.T) {
	t.Parallel()
	spec := k8sTestSpec()
	got := BahiaK8sDeploymentName(spec)
	// testEnvironmentID = "22222222-2222-..." → prefix "22222222"
	want := "bahia-22222222-my-api"
	if got != want {
		t.Errorf("BahiaK8sDeploymentName() = %q, want %q", got, want)
	}
}

func TestBahiaK8sDeploymentName_NilSpec(t *testing.T) {
	t.Parallel()
	if got := BahiaK8sDeploymentName(nil); got != "" {
		t.Errorf("BahiaK8sDeploymentName(nil) = %q, want empty", got)
	}
}

func TestBahiaK8sDeploymentName_VariousKeys(t *testing.T) {
	t.Parallel()
	cases := []struct {
		key  string
		want string
	}{
		{"web-frontend", "bahia-22222222-web-frontend"},
		{"db", "bahia-22222222-db"},
		{"worker-v2", "bahia-22222222-worker-v2"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.key, func(t *testing.T) {
			t.Parallel()
			spec := k8sTestSpec()
			spec.StableServiceKey = tc.key
			got := BahiaK8sDeploymentName(spec)
			if got != tc.want {
				t.Errorf("key=%q: got %q, want %q", tc.key, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// MapDesiredSpecToK8sManifest — basic structure
// ---------------------------------------------------------------------------

func TestMapDesiredSpecToK8sManifest_Basic(t *testing.T) {
	t.Parallel()
	spec := k8sTestSpec()
	m, err := MapDesiredSpecToK8sManifest(spec, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m == nil {
		t.Fatal("manifest is nil")
	}

	d := m.Deployment
	if d["apiVersion"] != "apps/v1" {
		t.Errorf("apiVersion = %v, want apps/v1", d["apiVersion"])
	}
	if d["kind"] != "Deployment" {
		t.Errorf("kind = %v, want Deployment", d["kind"])
	}

	meta := d["metadata"].(map[string]any)
	if meta["name"] != "bahia-22222222-my-api" {
		t.Errorf("metadata.name = %v", meta["name"])
	}
	if meta["namespace"] != "default" {
		t.Errorf("metadata.namespace = %v", meta["namespace"])
	}

	labels := meta["labels"].(map[string]string)
	if labels["bahia.managed"] != "true" {
		t.Error("bahia.managed label missing")
	}
	if labels["bahia.service_id"] != testServiceID.String() {
		t.Errorf("bahia.service_id = %q", labels["bahia.service_id"])
	}
	if labels["bahia.environment_id"] != testEnvironmentID.String() {
		t.Errorf("bahia.environment_id = %q", labels["bahia.environment_id"])
	}
	if labels["bahia.service"] != "my-api" {
		t.Errorf("bahia.service = %q", labels["bahia.service"])
	}
	if labels["bahia.desired_hash"] == "" {
		t.Error("bahia.desired_hash label missing")
	}

	annotations := meta["annotations"].(map[string]string)
	if annotations["bahia.desired_hash"] != spec.DesiredHash {
		t.Errorf("annotation bahia.desired_hash = %q, want %q", annotations["bahia.desired_hash"], spec.DesiredHash)
	}

	dspec := d["spec"].(map[string]any)
	if dspec["replicas"].(int32) != 1 {
		t.Errorf("spec.replicas = %v, want 1", dspec["replicas"])
	}

	sel := dspec["selector"].(map[string]any)
	ml := sel["matchLabels"].(map[string]string)
	if ml["bahia.service"] != "my-api" {
		t.Errorf("selector.matchLabels[bahia.service] = %q", ml["bahia.service"])
	}

	tmpl := dspec["template"].(map[string]any)
	podSpec := tmpl["spec"].(map[string]any)
	containers := podSpec["containers"].([]any)
	if len(containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(containers))
	}
	c := containers[0].(map[string]any)
	if c["image"] != "registry.example/api:v1.2.3" {
		t.Errorf("container.image = %q", c["image"])
	}
	if c["name"] != "my-api" {
		t.Errorf("container.name = %q", c["name"])
	}
	if podSpec["restartPolicy"] != "Always" {
		t.Errorf("restartPolicy = %q, want Always", podSpec["restartPolicy"])
	}
}

// ---------------------------------------------------------------------------
// MapDesiredSpecToK8sManifest — env sorted
// ---------------------------------------------------------------------------

func TestMapDesiredSpecToK8sManifest_EnvSorted(t *testing.T) {
	t.Parallel()
	spec := k8sTestSpec()
	// Spec has APP_ENV and LOG_LEVEL; alphabetically APP_ENV < LOG_LEVEL.
	m, err := MapDesiredSpecToK8sManifest(spec, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	containers := m.Deployment["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)
	c := containers[0].(map[string]any)
	envs := c["env"].([]map[string]string)

	if len(envs) < 2 {
		t.Fatalf("expected >= 2 env vars, got %d", len(envs))
	}
	if envs[0]["name"] != "APP_ENV" {
		t.Errorf("env[0].name = %q, want APP_ENV", envs[0]["name"])
	}
	if envs[1]["name"] != "LOG_LEVEL" {
		t.Errorf("env[1].name = %q, want LOG_LEVEL", envs[1]["name"])
	}
}

// ---------------------------------------------------------------------------
// MapDesiredSpecToK8sManifest — secrets injected
// ---------------------------------------------------------------------------

func TestMapDesiredSpecToK8sManifest_WithSecrets(t *testing.T) {
	t.Parallel()
	spec := k8sTestSpec()
	spec.SecretRefs = []domain.DesiredSecretRef{
		{
			EnvVar:        "DB_PASSWORD",
			Name:          "DB_PASSWORD",
			SecretID:      uuid.MustParse("44444444-4444-4444-4444-444444444444"),
			RedactedValue: "REDACTED(DB_PASSWORD)",
		},
	}
	secrets := map[string]string{"DB_PASSWORD": "supersecret"}

	m, err := MapDesiredSpecToK8sManifest(spec, secrets)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	containers := m.Deployment["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)
	c := containers[0].(map[string]any)
	envs := c["env"].([]map[string]string)

	var dbPass string
	for _, e := range envs {
		if e["name"] == "DB_PASSWORD" {
			dbPass = e["value"]
		}
	}
	if dbPass != "supersecret" {
		t.Errorf("DB_PASSWORD = %q, want supersecret", dbPass)
	}
	// Ensure no REDACTED placeholders leaked into the manifest.
	for _, e := range envs {
		if strings.Contains(e["value"], "REDACTED") {
			t.Errorf("redacted placeholder leaked into env: %v", e)
		}
	}
}

// ---------------------------------------------------------------------------
// MapDesiredSpecToK8sManifest — nil spec error
// ---------------------------------------------------------------------------

func TestMapDesiredSpecToK8sManifest_NilSpec(t *testing.T) {
	t.Parallel()
	_, err := MapDesiredSpecToK8sManifest(nil, nil)
	if err == nil {
		t.Fatal("expected error for nil spec")
	}
}

// ---------------------------------------------------------------------------
// MapDesiredSpecToK8sManifest — deterministic output
// ---------------------------------------------------------------------------

func TestMapDesiredSpecToK8sManifest_Deterministic(t *testing.T) {
	t.Parallel()
	spec := k8sTestSpec()
	secrets := map[string]string{"EXTRA": "value"}

	m1, err := MapDesiredSpecToK8sManifest(spec, secrets)
	if err != nil {
		t.Fatalf("first call error: %v", err)
	}
	m2, err := MapDesiredSpecToK8sManifest(spec, secrets)
	if err != nil {
		t.Fatalf("second call error: %v", err)
	}

	data1, _ := json.Marshal(m1.Deployment)
	data2, _ := json.Marshal(m2.Deployment)
	if string(data1) != string(data2) {
		t.Errorf("manifest is not deterministic:\nfirst:  %s\nsecond: %s", data1, data2)
	}
}

// ---------------------------------------------------------------------------
// MapDesiredSpecToK8sManifest — no Service when ServiceType empty
// ---------------------------------------------------------------------------

func TestMapDesiredSpecToK8sManifest_NoServiceWithoutType(t *testing.T) {
	t.Parallel()
	spec := k8sTestSpec()
	m, err := MapDesiredSpecToK8sManifest(spec, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Service != nil {
		t.Error("expected nil Service when no ServiceType is set")
	}
}

// ---------------------------------------------------------------------------
// MapDesiredSpecToK8sManifest — Service generation (via buildK8sService)
//
// KubernetesExtension currently lacks the ServiceType field, so we test the
// buildK8sService helper directly. When KubernetesExtension gains ServiceType,
// this can be promoted to a full MapDesiredSpecToK8sManifest integration test.
// ---------------------------------------------------------------------------

func TestMapDesiredSpecToK8sManifest_ServiceGeneration(t *testing.T) {
	t.Parallel()
	spec := k8sTestSpec()
	deploymentName := BahiaK8sDeploymentName(spec)
	labels := buildK8sLabels(spec)

	ports := []map[string]any{
		{"port": 80, "protocol": "TCP", "targetPort": 8080},
	}
	svc := buildK8sService(deploymentName, "default", spec.StableServiceKey, "LoadBalancer", ports, labels)

	if svc["apiVersion"] != "v1" {
		t.Errorf("apiVersion = %v, want v1", svc["apiVersion"])
	}
	if svc["kind"] != "Service" {
		t.Errorf("kind = %v, want Service", svc["kind"])
	}

	meta := svc["metadata"].(map[string]any)
	if meta["name"] != deploymentName {
		t.Errorf("metadata.name = %v, want %q", meta["name"], deploymentName)
	}
	if meta["namespace"] != "default" {
		t.Errorf("metadata.namespace = %v", meta["namespace"])
	}

	svcSpec := svc["spec"].(map[string]any)
	if svcSpec["type"] != "LoadBalancer" {
		t.Errorf("spec.type = %v, want LoadBalancer", svcSpec["type"])
	}
	sel := svcSpec["selector"].(map[string]string)
	if sel["bahia.service"] != "my-api" {
		t.Errorf("selector[bahia.service] = %q", sel["bahia.service"])
	}
	svcPorts := svcSpec["ports"].([]map[string]any)
	if len(svcPorts) != 1 {
		t.Errorf("ports count = %d, want 1", len(svcPorts))
	}
}

// ---------------------------------------------------------------------------
// buildK8sContainerPorts
// ---------------------------------------------------------------------------

func TestBuildK8sContainerPorts_Basic(t *testing.T) {
	t.Parallel()
	ports := buildK8sContainerPorts([]string{"8080:80", "9090:9090"})
	if len(ports) != 2 {
		t.Fatalf("expected 2 ports, got %d", len(ports))
	}
	if ports[0]["containerPort"].(int) != 80 {
		t.Errorf("port[0].containerPort = %v, want 80", ports[0]["containerPort"])
	}
	if ports[0]["protocol"] != "TCP" {
		t.Errorf("port[0].protocol = %v, want TCP", ports[0]["protocol"])
	}
	if ports[1]["containerPort"].(int) != 9090 {
		t.Errorf("port[1].containerPort = %v, want 9090", ports[1]["containerPort"])
	}
}

func TestBuildK8sContainerPorts_UDP(t *testing.T) {
	t.Parallel()
	ports := buildK8sContainerPorts([]string{"5353:53/udp"})
	if len(ports) != 1 {
		t.Fatalf("expected 1 port, got %d", len(ports))
	}
	if ports[0]["protocol"] != "UDP" {
		t.Errorf("protocol = %v, want UDP", ports[0]["protocol"])
	}
	if ports[0]["containerPort"].(int) != 53 {
		t.Errorf("containerPort = %v, want 53", ports[0]["containerPort"])
	}
}

func TestBuildK8sContainerPorts_Dedup(t *testing.T) {
	t.Parallel()
	// Both map to containerPort 80/TCP → should deduplicate.
	ports := buildK8sContainerPorts([]string{"8080:80", "9999:80"})
	if len(ports) != 1 {
		t.Errorf("expected 1 port (dedup), got %d", len(ports))
	}
}

func TestBuildK8sContainerPorts_BarePort(t *testing.T) {
	t.Parallel()
	ports := buildK8sContainerPorts([]string{"3000"})
	if len(ports) != 1 {
		t.Fatalf("expected 1 port, got %d", len(ports))
	}
	if ports[0]["containerPort"].(int) != 3000 {
		t.Errorf("containerPort = %v, want 3000", ports[0]["containerPort"])
	}
}

func TestBuildK8sContainerPorts_Empty(t *testing.T) {
	t.Parallel()
	ports := buildK8sContainerPorts(nil)
	if len(ports) != 0 {
		t.Errorf("expected 0 ports for nil input, got %d", len(ports))
	}
}

// ---------------------------------------------------------------------------
// buildK8sVolumeMappings
// ---------------------------------------------------------------------------

func TestBuildK8sVolumeMappings_HostPathReadOnly(t *testing.T) {
	t.Parallel()
	vm := buildK8sVolumeMappings([]string{"/data/api:/app/data:ro"})
	if len(vm.Volumes) != 1 {
		t.Fatalf("expected 1 volume, got %d", len(vm.Volumes))
	}
	if len(vm.VolumeMounts) != 1 {
		t.Fatalf("expected 1 volumeMount, got %d", len(vm.VolumeMounts))
	}

	vol := vm.Volumes[0]
	hp := vol["hostPath"].(map[string]any)
	if hp["path"] != "/data/api" {
		t.Errorf("hostPath.path = %v, want /data/api", hp["path"])
	}

	mount := vm.VolumeMounts[0]
	if mount["mountPath"] != "/app/data" {
		t.Errorf("mountPath = %v, want /app/data", mount["mountPath"])
	}
	if mount["readOnly"] != true {
		t.Errorf("readOnly = %v, want true", mount["readOnly"])
	}
}

func TestBuildK8sVolumeMappings_HostPathReadWrite(t *testing.T) {
	t.Parallel()
	vm := buildK8sVolumeMappings([]string{"/tmp/cache:/cache"})
	if len(vm.VolumeMounts) != 1 {
		t.Fatalf("expected 1 mount, got %d", len(vm.VolumeMounts))
	}
	mount := vm.VolumeMounts[0]
	if _, ok := mount["readOnly"]; ok {
		t.Error("readOnly should not be set for rw mount")
	}
}

func TestBuildK8sVolumeMappings_SkipsNamedVolumes(t *testing.T) {
	t.Parallel()
	vm := buildK8sVolumeMappings([]string{"myvolume:/data"})
	if len(vm.Volumes) != 0 {
		t.Errorf("expected 0 pod volumes for named volume, got %d", len(vm.Volumes))
	}
}

func TestBuildK8sVolumeMappings_Empty(t *testing.T) {
	t.Parallel()
	vm := buildK8sVolumeMappings(nil)
	if len(vm.Volumes) != 0 || len(vm.VolumeMounts) != 0 {
		t.Errorf("expected empty result for nil input")
	}
}

// ---------------------------------------------------------------------------
// DeploymentDesiredHashMatches
// ---------------------------------------------------------------------------

func TestDeploymentDesiredHashMatches_Match(t *testing.T) {
	t.Parallel()
	spec := k8sTestSpec()
	info := &k8sDeploymentInfo{
		Name:        BahiaK8sDeploymentName(spec),
		DesiredHash: spec.DesiredHash,
	}
	if !DeploymentDesiredHashMatches(info, spec) {
		t.Error("expected hash match to return true")
	}
}

func TestDeploymentDesiredHashMatches_Mismatch(t *testing.T) {
	t.Parallel()
	spec := k8sTestSpec()
	info := &k8sDeploymentInfo{
		Name:        BahiaK8sDeploymentName(spec),
		DesiredHash: "sha256:different",
	}
	if DeploymentDesiredHashMatches(info, spec) {
		t.Error("expected hash mismatch to return false")
	}
}

func TestDeploymentDesiredHashMatches_EmptyHash(t *testing.T) {
	t.Parallel()
	spec := k8sTestSpec()
	info := &k8sDeploymentInfo{Name: "test", DesiredHash: ""}
	if DeploymentDesiredHashMatches(info, spec) {
		t.Error("empty existing hash should not match")
	}
}

func TestDeploymentDesiredHashMatches_NilCases(t *testing.T) {
	t.Parallel()
	spec := k8sTestSpec()
	info := &k8sDeploymentInfo{DesiredHash: spec.DesiredHash}

	if DeploymentDesiredHashMatches(nil, spec) {
		t.Error("nil info should return false")
	}
	if DeploymentDesiredHashMatches(info, nil) {
		t.Error("nil spec should return false")
	}
	if DeploymentDesiredHashMatches(nil, nil) {
		t.Error("both nil should return false")
	}
}

// ---------------------------------------------------------------------------
// KubernetesRuntime capability
// ---------------------------------------------------------------------------

func TestKubernetesRuntime_SupportsDesiredState(t *testing.T) {
	t.Parallel()
	k := NewKubernetesRuntime("", "default", "", zap.NewNop())
	if !k.SupportsDesiredState() {
		t.Error("expected SupportsDesiredState() = true")
	}
}

func TestKubernetesRuntime_AsDesiredStateApplier(t *testing.T) {
	t.Parallel()
	k := NewKubernetesRuntime("", "default", "", zap.NewNop())
	applier, ok := AsDesiredStateApplier(k)
	if !ok {
		t.Fatal("KubernetesRuntime should be recognised as DesiredStateApplier")
	}
	if applier == nil {
		t.Fatal("applier must not be nil")
	}
}

// ---------------------------------------------------------------------------
// FindBahiaManagedDeployment
// ---------------------------------------------------------------------------

func TestFindBahiaManagedDeployment_NotFound(t *testing.T) {
	t.Parallel()
	runner := &mockK8sRunner{}
	runner.enqueue(emptyK8sDeploymentList, nil)
	k := newTestK8sRuntime(runner)

	info, err := k.FindBahiaManagedDeployment(context.Background(), k8sTestSpec())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info != nil {
		t.Errorf("expected nil info, got %+v", info)
	}
}

func TestFindBahiaManagedDeployment_Found(t *testing.T) {
	t.Parallel()
	spec := k8sTestSpec()
	runner := &mockK8sRunner{}
	runner.enqueue(existingDeploymentResponse(spec, spec.DesiredHash), nil)
	k := newTestK8sRuntime(runner)

	info, err := k.FindBahiaManagedDeployment(context.Background(), spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info == nil {
		t.Fatal("expected non-nil info")
	}
	if info.Name != BahiaK8sDeploymentName(spec) {
		t.Errorf("info.Name = %q, want %q", info.Name, BahiaK8sDeploymentName(spec))
	}
	if info.DesiredHash != spec.DesiredHash {
		t.Errorf("info.DesiredHash = %q, want %q", info.DesiredHash, spec.DesiredHash)
	}
	if info.Replicas != 1 {
		t.Errorf("info.Replicas = %d, want 1", info.Replicas)
	}
}

func TestFindBahiaManagedDeployment_NilSpec(t *testing.T) {
	t.Parallel()
	k := NewKubernetesRuntime("", "default", "", zap.NewNop())
	info, err := k.FindBahiaManagedDeployment(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error for nil spec: %v", err)
	}
	if info != nil {
		t.Error("expected nil info for nil spec")
	}
}

func TestFindBahiaManagedDeployment_LabelSelector(t *testing.T) {
	t.Parallel()
	spec := k8sTestSpec()
	runner := &mockK8sRunner{}
	runner.enqueue(emptyK8sDeploymentList, nil)
	k := newTestK8sRuntime(runner)

	_, _ = k.FindBahiaManagedDeployment(context.Background(), spec)

	if runner.callCount() != 1 {
		t.Fatalf("expected exactly 1 kubectl call, got %d", runner.callCount())
	}
	argsStr := strings.Join(runner.calls[0].args, " ")
	if !contains(argsStr, spec.ServiceID.String()) {
		t.Errorf("label selector missing service_id; args = %q", argsStr)
	}
	if !contains(argsStr, spec.EnvironmentID.String()) {
		t.Errorf("label selector missing environment_id; args = %q", argsStr)
	}
}

// ---------------------------------------------------------------------------
// ApplyDesiredState — nil spec
// ---------------------------------------------------------------------------

func TestKubernetesApplyDesiredState_NilSpec(t *testing.T) {
	t.Parallel()
	k := newTestK8sRuntime(&mockK8sRunner{})
	_, err := k.ApplyDesiredState(context.Background(), DesiredStateApplyRequest{})
	if err == nil {
		t.Fatal("expected error for nil TargetService")
	}
	if !contains(err.Error(), "nil") {
		t.Errorf("error = %q, expected mention of nil", err)
	}
}

// ---------------------------------------------------------------------------
// ApplyDesiredState — new deployment
// ---------------------------------------------------------------------------

func TestKubernetesApplyDesiredState_NewDeployment(t *testing.T) {
	t.Parallel()
	spec := k8sTestSpec()
	runner := &mockK8sRunner{}
	runner.enqueue(emptyK8sDeploymentList, nil)                              // get deployment → not found
	runner.enqueue("deployment.apps/bahia-22222222-my-api created\n", nil)  // apply deployment

	k := newTestK8sRuntime(runner)
	result, err := k.ApplyDesiredState(context.Background(), DesiredStateApplyRequest{
		TargetService: spec,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Renderer != "kubernetes" {
		t.Errorf("Renderer = %q, want kubernetes", result.Renderer)
	}
	if result.ExecutionMode != ExecutionModeCLI {
		t.Errorf("ExecutionMode = %q, want %q", result.ExecutionMode, ExecutionModeCLI)
	}
	if result.DesiredHash != spec.DesiredHash {
		t.Errorf("DesiredHash = %q, want %q", result.DesiredHash, spec.DesiredHash)
	}
	wantName := BahiaK8sDeploymentName(spec)
	if len(result.ResourceNames) != 1 || result.ResourceNames[0] != wantName {
		t.Errorf("ResourceNames = %v, want [%q]", result.ResourceNames, wantName)
	}
	if runner.callCount() != 2 {
		t.Errorf("expected 2 kubectl calls (get + apply), got %d", runner.callCount())
	}
	// Verify the apply call sent JSON on stdin.
	applyCall := runner.calls[1]
	if len(applyCall.stdin) == 0 {
		t.Error("apply call should have manifest JSON on stdin")
	}
	var manifest map[string]any
	if err := json.Unmarshal(applyCall.stdin, &manifest); err != nil {
		t.Errorf("apply stdin is not valid JSON: %v", err)
	}
	if manifest["kind"] != "Deployment" {
		t.Errorf("applied manifest kind = %v, want Deployment", manifest["kind"])
	}
}

// ---------------------------------------------------------------------------
// ApplyDesiredState — no-op when hash matches
// ---------------------------------------------------------------------------

func TestKubernetesApplyDesiredState_NoOp_HashMatch(t *testing.T) {
	t.Parallel()
	spec := k8sTestSpec()
	runner := &mockK8sRunner{}
	runner.enqueue(existingDeploymentResponse(spec, spec.DesiredHash), nil)

	k := newTestK8sRuntime(runner)
	result, err := k.ApplyDesiredState(context.Background(), DesiredStateApplyRequest{
		TargetService: spec,
		PullPolicy:    "if-not-present",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Renderer != "kubernetes" {
		t.Errorf("Renderer = %q, want kubernetes", result.Renderer)
	}
	if result.DesiredHash != spec.DesiredHash {
		t.Errorf("DesiredHash = %q", result.DesiredHash)
	}
	// Only one call: the get-deployment lookup. No apply call.
	if runner.callCount() != 1 {
		t.Errorf("expected 1 kubectl call (no-op), got %d", runner.callCount())
	}
}

// ---------------------------------------------------------------------------
// ApplyDesiredState — always-pull forces re-apply even on hash match
// ---------------------------------------------------------------------------

func TestKubernetesApplyDesiredState_AlwaysPull_Reapplies(t *testing.T) {
	t.Parallel()
	spec := k8sTestSpec()
	runner := &mockK8sRunner{}
	runner.enqueue(existingDeploymentResponse(spec, spec.DesiredHash), nil) // same hash
	runner.enqueue("deployment.apps/bahia-22222222-my-api configured\n", nil)

	k := newTestK8sRuntime(runner)
	result, err := k.ApplyDesiredState(context.Background(), DesiredStateApplyRequest{
		TargetService: spec,
		PullPolicy:    "always",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Renderer != "kubernetes" {
		t.Errorf("Renderer = %q", result.Renderer)
	}
	if runner.callCount() != 2 {
		t.Errorf("expected 2 calls (always-pull forces re-apply), got %d", runner.callCount())
	}
}

// ---------------------------------------------------------------------------
// ApplyDesiredState — hash drift triggers re-apply
// ---------------------------------------------------------------------------

func TestKubernetesApplyDesiredState_HashDrift_Reapplies(t *testing.T) {
	t.Parallel()
	spec := k8sTestSpec()
	runner := &mockK8sRunner{}
	runner.enqueue(existingDeploymentResponse(spec, "sha256:old-hash"), nil) // stale hash
	runner.enqueue("deployment.apps/bahia-22222222-my-api configured\n", nil)

	k := newTestK8sRuntime(runner)
	result, err := k.ApplyDesiredState(context.Background(), DesiredStateApplyRequest{
		TargetService: spec,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Renderer != "kubernetes" {
		t.Errorf("Renderer = %q", result.Renderer)
	}
	if runner.callCount() != 2 {
		t.Errorf("expected 2 calls (get + apply), got %d", runner.callCount())
	}
}

// ---------------------------------------------------------------------------
// ApplyDesiredState — dry run
// ---------------------------------------------------------------------------

func TestKubernetesApplyDesiredState_DryRun_NoExisting(t *testing.T) {
	t.Parallel()
	runner := &mockK8sRunner{}
	runner.enqueue(emptyK8sDeploymentList, nil)

	k := newTestK8sRuntime(runner)
	spec := k8sTestSpec()
	result, err := k.ApplyDesiredState(context.Background(), DesiredStateApplyRequest{
		TargetService: spec,
		DryRun:        true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Warnings) == 0 {
		t.Fatal("expected dry-run warning")
	}
	if !contains(result.Warnings[0], "dry-run") {
		t.Errorf("warning = %q, expected mention of dry-run", result.Warnings[0])
	}
	if !contains(result.Warnings[0], "create") {
		t.Errorf("warning = %q, expected mention of create", result.Warnings[0])
	}
	// Only the get-deployment call — no apply.
	if runner.callCount() != 1 {
		t.Errorf("expected 1 kubectl call for dry-run, got %d", runner.callCount())
	}
}

func TestKubernetesApplyDesiredState_DryRun_WithExisting(t *testing.T) {
	t.Parallel()
	spec := k8sTestSpec()
	runner := &mockK8sRunner{}
	runner.enqueue(existingDeploymentResponse(spec, "sha256:old"), nil)

	k := newTestK8sRuntime(runner)
	result, err := k.ApplyDesiredState(context.Background(), DesiredStateApplyRequest{
		TargetService: spec,
		DryRun:        true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Warnings) == 0 {
		t.Fatal("expected dry-run warning")
	}
	if !contains(result.Warnings[0], "update") {
		t.Errorf("warning = %q, expected mention of update", result.Warnings[0])
	}
}

// ---------------------------------------------------------------------------
// ApplyDesiredState — environment revision propagation
// ---------------------------------------------------------------------------

func TestKubernetesApplyDesiredState_EnvironmentRevision(t *testing.T) {
	t.Parallel()
	spec := k8sTestSpec()
	runner := &mockK8sRunner{}
	runner.enqueue(emptyK8sDeploymentList, nil)
	runner.enqueue("", nil)

	k := newTestK8sRuntime(runner)
	plan := &domain.DesiredEnvironmentPlan{
		EnvironmentID: testEnvironmentID,
		RevisionHash:  "sha256:env-plan-rev",
		Services:      []domain.DesiredServiceSpec{*spec},
	}
	result, err := k.ApplyDesiredState(context.Background(), DesiredStateApplyRequest{
		TargetService:   spec,
		EnvironmentPlan: plan,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.EnvironmentRevision != "sha256:env-plan-rev" {
		t.Errorf("EnvironmentRevision = %q, want sha256:env-plan-rev", result.EnvironmentRevision)
	}
}

// ---------------------------------------------------------------------------
// ApplyDesiredState — observation hints always populated
// ---------------------------------------------------------------------------

func TestKubernetesApplyDesiredState_ObservationHints(t *testing.T) {
	t.Parallel()
	spec := k8sTestSpec()
	runner := &mockK8sRunner{}
	runner.enqueue(emptyK8sDeploymentList, nil)
	runner.enqueue("", nil)

	k := newTestK8sRuntime(runner)
	result, err := k.ApplyDesiredState(context.Background(), DesiredStateApplyRequest{
		TargetService: spec,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ObservationHints == nil {
		t.Error("ObservationHints must not be nil")
	}
}

// ---------------------------------------------------------------------------
// Manifest labels include all bahia.* keys
// ---------------------------------------------------------------------------

func TestMapDesiredSpecToK8sManifest_AllBahiaLabels(t *testing.T) {
	t.Parallel()
	spec := k8sTestSpec()
	m, err := MapDesiredSpecToK8sManifest(spec, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	required := []string{
		"bahia.managed",
		"bahia.service_id",
		"bahia.environment_id",
		"bahia.desired_hash",
		"bahia.service",
	}
	meta := m.Deployment["metadata"].(map[string]any)
	labels := meta["labels"].(map[string]string)
	for _, key := range required {
		if labels[key] == "" {
			t.Errorf("label %q is missing or empty", key)
		}
	}
}

// ---------------------------------------------------------------------------
// Volumes appear in both pod spec and container volumeMounts
// ---------------------------------------------------------------------------

func TestMapDesiredSpecToK8sManifest_VolumesWired(t *testing.T) {
	t.Parallel()
	spec := k8sTestSpec()
	// spec already has /data/api:/app/data:ro
	m, err := MapDesiredSpecToK8sManifest(spec, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	dspec := m.Deployment["spec"].(map[string]any)
	tmpl := dspec["template"].(map[string]any)
	podSpec := tmpl["spec"].(map[string]any)

	vols, ok := podSpec["volumes"].([]map[string]any)
	if !ok || len(vols) == 0 {
		t.Fatal("expected at least one volume in pod spec")
	}
	containers := podSpec["containers"].([]any)
	c := containers[0].(map[string]any)
	mounts, ok := c["volumeMounts"].([]map[string]any)
	if !ok || len(mounts) == 0 {
		t.Fatal("expected at least one volumeMount in container")
	}
	// Volume name should be consistent between pod spec and mount.
	if vols[0]["name"] != mounts[0]["name"] {
		t.Errorf("volume name %q != mount name %q", vols[0]["name"], mounts[0]["name"])
	}
}
