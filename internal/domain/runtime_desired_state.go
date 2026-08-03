// Package domain defines the core domain types for Bahia.
package domain

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/google/uuid"
)

// DesiredStateSchemaVersion is the current schema version for desired-state serialization.
// Bump this when the canonical hash inputs change.
const DesiredStateSchemaVersion = "4"

// ---------------------------------------------------------------------------
// Secret handling types
// ---------------------------------------------------------------------------

// DesiredSecretRef is a safe reference to a secret-backed environment variable
// in the desired state. Plaintext secret values are NEVER stored in persisted
// desired state; only the reference metadata and a redacted placeholder are kept.
type DesiredSecretRef struct {
	// EnvVar is the environment variable name this secret populates.
	EnvVar string `json:"env_var"`
	// Name is the secret's logical name (e.g. "DB_PASSWORD").
	Name string `json:"name"`
	// SecretID is the ID of the ServiceSecret this ref points to.
	SecretID uuid.UUID `json:"secret_id"`
	// RedactedValue is a safe placeholder shown in logs and projections.
	RedactedValue string `json:"redacted_value"`
}

// RedactedPlaceholder returns the canonical redacted placeholder for a secret name.
func RedactedPlaceholder(name string) string {
	return fmt.Sprintf("REDACTED(%s)", name)
}

// ---------------------------------------------------------------------------
// Healthcheck
// ---------------------------------------------------------------------------

// HealthcheckConfig is the portable healthcheck specification shared across
// renderers. Renderer-specific overrides live in the extension types.
type HealthcheckConfig struct {
	Test        []string `json:"test"`
	Protocol    string   `json:"protocol,omitempty"`
	Method      string   `json:"method,omitempty"`
	Path        string   `json:"path,omitempty"`
	Port        int      `json:"port,omitempty"`
	Interval    string   `json:"interval,omitempty"`
	Timeout     string   `json:"timeout,omitempty"`
	Retries     int      `json:"retries,omitempty"`
	StartPeriod string   `json:"start_period,omitempty"`
}

// ---------------------------------------------------------------------------
// Extension types
// ---------------------------------------------------------------------------

// ComposeExtension carries Compose-specific rendering metadata that is not
// portable across runtimes.
type ComposeExtension struct {
	// DependsOn maps service keys to dependency conditions for Compose depends_on.
	DependsOn map[string]ComposeDependency `json:"depends_on,omitempty"`
	// HealthcheckOverride allows Compose-specific healthcheck tuning.
	HealthcheckOverride *HealthcheckConfig `json:"healthcheck_override,omitempty"`
	// EnvFile records the generated env-file path relative to the compose dir.
	EnvFile string `json:"env_file,omitempty"`
	// ProjectName is the explicit Compose project name.
	ProjectName string `json:"project_name,omitempty"`
	// Networks lists Compose network declarations needed by this service.
	Networks []string `json:"networks,omitempty"`
	// VolumeDeclarations lists named volume declarations for the project.
	VolumeDeclarations []string `json:"volume_declarations,omitempty"`
}

// ComposeDependency describes a single depends_on entry with its condition.
type ComposeDependency struct {
	Condition string `json:"condition,omitempty"` // e.g. "service_healthy", "service_started"
}

// DockerExtension carries Docker Engine-specific configuration that is not
// portable across runtimes.
type DockerExtension struct {
	// HostConfig is raw Docker host-config fields (binds, resources, etc.).
	HostConfig map[string]any `json:"host_config,omitempty"`
	// NetworkingConfig is raw Docker networking config (endpoints, aliases).
	NetworkingConfig map[string]any `json:"networking_config,omitempty"`
	// VolumeOptions carries volume driver/label options for ensured volumes.
	VolumeOptions map[string]any `json:"volume_options,omitempty"`
	// Healthcheck carries Engine-specific healthcheck config that differs from
	// the portable HealthcheckConfig (e.g. []string test format differences).
	Healthcheck map[string]any `json:"healthcheck,omitempty"`
}

// ---------------------------------------------------------------------------
// Network and Volume specs for resource ensure
// ---------------------------------------------------------------------------

// NetworkSpec describes a Docker network that must exist before container
// creation. The ensure helpers inspect existing networks and create missing
// ones, erroring on incompatible existing resources.
type NetworkSpec struct {
	// Name is the Docker network name.
	Name string `json:"name"`
	// Driver is the network driver (e.g. "bridge", "overlay"). Empty means
	// the Docker daemon default (bridge).
	Driver string `json:"driver,omitempty"`
	// Options are driver-specific options (e.g. subnet, gateway).
	Options map[string]string `json:"options,omitempty"`
	// Labels are user-defined metadata applied to the network.
	Labels map[string]string `json:"labels,omitempty"`
}

// VolumeSpec describes a Docker named volume that must exist before container
// creation. The ensure helpers inspect existing volumes and create missing
// ones, erroring on incompatible existing resources.
type VolumeSpec struct {
	// Name is the Docker volume name.
	Name string `json:"name"`
	// Driver is the volume driver (e.g. "local"). Empty means the Docker
	// daemon default (local).
	Driver string `json:"driver,omitempty"`
	// DriverOpts are driver-specific options.
	DriverOpts map[string]string `json:"driver_opts,omitempty"`
	// Labels are user-defined metadata applied to the volume.
	Labels map[string]string `json:"labels,omitempty"`
}

// K8sServicePort describes a Kubernetes Service port mapping.
type K8sServicePort struct {
	Name       string `json:"name,omitempty"`
	Port       int32  `json:"port"`
	TargetPort int32  `json:"target_port,omitempty"`
	Protocol   string `json:"protocol,omitempty"` // TCP, UDP
	NodePort   int32  `json:"node_port,omitempty"`
}

// K8sResources describes CPU/memory resource quantities.
type K8sResources struct {
	CPU    string `json:"cpu,omitempty"`    // e.g. "500m", "2"
	Memory string `json:"memory,omitempty"` // e.g. "256Mi", "1Gi"
}

// K8sToleration describes a Kubernetes toleration.
type K8sToleration struct {
	Key      string `json:"key,omitempty"`
	Operator string `json:"operator,omitempty"` // Exists, Equal
	Value    string `json:"value,omitempty"`
	Effect   string `json:"effect,omitempty"` // NoSchedule, PreferNoSchedule, NoExecute
}

// K8sHTTPGet describes an HTTP probe action.
type K8sHTTPGet struct {
	Path   string `json:"path"`
	Port   int32  `json:"port"`
	Scheme string `json:"scheme,omitempty"` // HTTP, HTTPS
}

// K8sProbe describes a Kubernetes liveness/readiness probe.
type K8sProbe struct {
	HTTPGet             *K8sHTTPGet `json:"http_get,omitempty"`
	Exec                []string    `json:"exec,omitempty"`
	InitialDelaySeconds int32       `json:"initial_delay_seconds,omitempty"`
	PeriodSeconds       int32       `json:"period_seconds,omitempty"`
	TimeoutSeconds      int32       `json:"timeout_seconds,omitempty"`
	FailureThreshold    int32       `json:"failure_threshold,omitempty"`
}

// KubernetesExtension carries Kubernetes-specific rendering metadata for the
// desired-state renderer. It maps DesiredServiceSpec fields to Kubernetes
// Deployment and optional Service resources.
type KubernetesExtension struct {
	// Namespace overrides the runtime's default namespace for this service.
	Namespace string `json:"namespace,omitempty"`
	// Replicas is the desired replica count. Nil means 1.
	Replicas *int32 `json:"replicas,omitempty"`
	// ServiceType is the Kubernetes Service type (ClusterIP, NodePort, LoadBalancer).
	// Empty means no Service resource is created.
	ServiceType string `json:"service_type,omitempty"`
	// ServicePorts are the Service port mappings. Used only when ServiceType is set.
	ServicePorts []K8sServicePort `json:"service_ports,omitempty"`
	// ResourceLimits are container resource limits.
	ResourceLimits *K8sResources `json:"resource_limits,omitempty"`
	// ResourceRequests are container resource requests.
	ResourceRequests *K8sResources `json:"resource_requests,omitempty"`
	// Annotations are added to the Deployment metadata.
	Annotations map[string]string `json:"annotations,omitempty"`
	// NodeSelector constrains pod scheduling.
	NodeSelector map[string]string `json:"node_selector,omitempty"`
	// Tolerations allow pods to schedule on tainted nodes.
	Tolerations []K8sToleration `json:"tolerations,omitempty"`
	// LivenessProbe overrides the portable healthcheck for Kubernetes liveness.
	LivenessProbe *K8sProbe `json:"liveness_probe,omitempty"`
	// ReadinessProbe configures Kubernetes readiness checking.
	ReadinessProbe *K8sProbe `json:"readiness_probe,omitempty"`
	// ImagePullSecrets are the names of Kubernetes secrets for pulling images.
	ImagePullSecrets []string `json:"image_pull_secrets,omitempty"`
}

// KubernetesExtensionFromDeploymentUnit derives a KubernetesExtension from a
// deployment unit's namespace and runtime_config. It always returns a non-nil
// extension so K8s specs carry a stable (possibly empty) extension. Recognized
// runtime_config keys match the KubernetesExtension JSON tags:
//
//	replicas           -> Replicas          (integer)
//	service_type       -> ServiceType       (string: ClusterIP|NodePort|LoadBalancer)
//	service_ports      -> ServicePorts      ([]object)
//	resource_limits    -> ResourceLimits    (object: cpu, memory)
//	resource_requests  -> ResourceRequests  (object: cpu, memory)
//	liveness_probe     -> LivenessProbe     (object)
//	readiness_probe    -> ReadinessProbe    (object)
//	node_selector      -> NodeSelector      (map[string]string)
//	annotations        -> Annotations       (map[string]string)
//	tolerations        -> Tolerations       ([]object)
//	image_pull_secrets -> ImagePullSecrets  ([]string)
//
// Unknown/absent keys and malformed shapes leave the corresponding field at its
// zero value. The constructor is intentionally best-effort because it cannot
// return a validation error.
func KubernetesExtensionFromDeploymentUnit(unit *DeploymentUnit) *KubernetesExtension {
	ext := &KubernetesExtension{}
	if unit == nil {
		return ext
	}
	ext.Namespace = unit.Namespace
	cfg := unit.RuntimeConfig
	if cfg == nil {
		return ext
	}
	if r, ok := int32FromRuntimeConfig(cfg, "replicas"); ok {
		ext.Replicas = &r
	}
	ext.ServiceType = stringFromRuntimeConfig(cfg, "service_type")
	if ports := k8sServicePortsFromRuntimeConfig(cfg, "service_ports"); len(ports) > 0 {
		ext.ServicePorts = ports
	}
	if limits := k8sResourcesFromRuntimeConfig(cfg, "resource_limits"); limits != nil {
		ext.ResourceLimits = limits
	}
	if requests := k8sResourcesFromRuntimeConfig(cfg, "resource_requests"); requests != nil {
		ext.ResourceRequests = requests
	}
	if probe := k8sProbeFromRuntimeConfig(cfg, "liveness_probe"); probe != nil {
		ext.LivenessProbe = probe
	}
	if probe := k8sProbeFromRuntimeConfig(cfg, "readiness_probe"); probe != nil {
		ext.ReadinessProbe = probe
	}
	if ns := stringMapFromRuntimeConfig(cfg, "node_selector"); len(ns) > 0 {
		ext.NodeSelector = ns
	}
	if ann := stringMapFromRuntimeConfig(cfg, "annotations"); len(ann) > 0 {
		ext.Annotations = ann
	}
	if tolerations := k8sTolerationsFromRuntimeConfig(cfg, "tolerations"); len(tolerations) > 0 {
		ext.Tolerations = tolerations
	}
	if secrets := stringSliceFromRuntimeConfig(cfg, "image_pull_secrets"); len(secrets) > 0 {
		ext.ImagePullSecrets = secrets
	}
	return ext
}

// int32FromRuntimeConfig extracts an integer value from a runtime_config map,
// tolerating the numeric types produced by JSON decoding (float64, json.Number)
// as well as native integer types. The second return value reports whether a
// usable integer was found.
func int32FromRuntimeConfig(config map[string]any, key string) (int32, bool) {
	raw, ok := config[key]
	if !ok || raw == nil {
		return 0, false
	}
	return int32FromRuntimeConfigValue(raw)
}

func int32FromRuntimeConfigValue(raw any) (int32, bool) {
	switch v := raw.(type) {
	case int:
		return int32(v), true
	case int32:
		return v, true
	case int64:
		return int32(v), true
	case float64:
		return int32(v), true
	case float32:
		return int32(v), true
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			return 0, false
		}
		return int32(n), true
	default:
		return 0, false
	}
}

func k8sServicePortsFromRuntimeConfig(config map[string]any, key string) []K8sServicePort {
	items := objectSliceFromRuntimeConfig(config, key)
	if len(items) == 0 {
		return nil
	}
	ports := make([]K8sServicePort, 0, len(items))
	for _, item := range items {
		port, ok := k8sServicePortFromRuntimeConfigValue(item)
		if ok {
			ports = append(ports, port)
		}
	}
	if len(ports) == 0 {
		return nil
	}
	return ports
}

func k8sServicePortFromRuntimeConfigValue(raw any) (K8sServicePort, bool) {
	m, ok := stringAnyMap(raw)
	if !ok {
		return K8sServicePort{}, false
	}
	port, ok := int32FromMap(m, "port")
	if !ok || port < 1 {
		return K8sServicePort{}, false
	}
	out := K8sServicePort{
		Name:     stringFromMap(m, "name"),
		Port:     port,
		Protocol: stringFromMap(m, "protocol"),
	}
	if targetPort, ok := int32FromMap(m, "target_port"); ok {
		out.TargetPort = targetPort
	}
	if nodePort, ok := int32FromMap(m, "node_port"); ok {
		out.NodePort = nodePort
	}
	return out, true
}

func k8sResourcesFromRuntimeConfig(config map[string]any, key string) *K8sResources {
	if config == nil {
		return nil
	}
	raw, ok := config[key]
	if !ok || raw == nil {
		return nil
	}
	m, ok := stringAnyMap(raw)
	if !ok {
		return nil
	}
	resources := &K8sResources{
		CPU:    stringFromMap(m, "cpu"),
		Memory: stringFromMap(m, "memory"),
	}
	if resources.CPU == "" && resources.Memory == "" {
		return nil
	}
	return resources
}

func k8sProbeFromRuntimeConfig(config map[string]any, key string) *K8sProbe {
	raw, ok := config[key]
	if !ok || raw == nil {
		return nil
	}
	m, ok := stringAnyMap(raw)
	if !ok {
		return nil
	}
	probe := &K8sProbe{}
	if httpGetRaw, ok := m["http_get"]; ok {
		if httpGet := k8sHTTPGetFromRuntimeConfigValue(httpGetRaw); httpGet != nil {
			probe.HTTPGet = httpGet
		}
	}
	if exec := stringSliceFromMap(m, "exec"); len(exec) > 0 {
		probe.Exec = exec
	}
	if probe.HTTPGet == nil && len(probe.Exec) == 0 {
		return nil
	}
	if value, ok := int32FromMap(m, "initial_delay_seconds"); ok {
		probe.InitialDelaySeconds = value
	}
	if value, ok := int32FromMap(m, "period_seconds"); ok {
		probe.PeriodSeconds = value
	}
	if value, ok := int32FromMap(m, "timeout_seconds"); ok {
		probe.TimeoutSeconds = value
	}
	if value, ok := int32FromMap(m, "failure_threshold"); ok {
		probe.FailureThreshold = value
	}
	return probe
}

func k8sHTTPGetFromRuntimeConfigValue(raw any) *K8sHTTPGet {
	m, ok := stringAnyMap(raw)
	if !ok {
		return nil
	}
	path := stringFromMap(m, "path")
	port, ok := int32FromMap(m, "port")
	if path == "" || !ok || port < 1 {
		return nil
	}
	return &K8sHTTPGet{
		Path:   path,
		Port:   port,
		Scheme: stringFromMap(m, "scheme"),
	}
}

func k8sTolerationsFromRuntimeConfig(config map[string]any, key string) []K8sToleration {
	items := objectSliceFromRuntimeConfig(config, key)
	if len(items) == 0 {
		return nil
	}
	tolerations := make([]K8sToleration, 0, len(items))
	for _, item := range items {
		m, ok := stringAnyMap(item)
		if !ok {
			continue
		}
		toleration := K8sToleration{
			Key:      stringFromMap(m, "key"),
			Operator: stringFromMap(m, "operator"),
			Value:    stringFromMap(m, "value"),
			Effect:   stringFromMap(m, "effect"),
		}
		if toleration.Key == "" && toleration.Operator == "" && toleration.Value == "" && toleration.Effect == "" {
			continue
		}
		tolerations = append(tolerations, toleration)
	}
	if len(tolerations) == 0 {
		return nil
	}
	return tolerations
}

func objectSliceFromRuntimeConfig(config map[string]any, key string) []any {
	if config == nil {
		return nil
	}
	raw, ok := config[key]
	if !ok || raw == nil {
		return nil
	}
	switch values := raw.(type) {
	case []any:
		return values
	case []map[string]any:
		items := make([]any, 0, len(values))
		for _, value := range values {
			items = append(items, value)
		}
		return items
	default:
		return nil
	}
}

func stringSliceFromRuntimeConfig(config map[string]any, key string) []string {
	if config == nil {
		return nil
	}
	raw, ok := config[key]
	if !ok || raw == nil {
		return nil
	}
	return stringSliceFromRuntimeConfigValue(raw)
}

func stringSliceFromMap(m map[string]any, key string) []string {
	raw, ok := m[key]
	if !ok || raw == nil {
		return nil
	}
	return stringSliceFromRuntimeConfigValue(raw)
}

func stringSliceFromRuntimeConfigValue(raw any) []string {
	var rawItems []any
	switch values := raw.(type) {
	case []string:
		items := make([]string, 0, len(values))
		for _, value := range values {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				items = append(items, trimmed)
			}
		}
		if len(items) == 0 {
			return nil
		}
		return items
	case []any:
		rawItems = values
	default:
		return nil
	}
	items := make([]string, 0, len(rawItems))
	for _, rawItem := range rawItems {
		value, ok := rawItem.(string)
		if !ok {
			continue
		}
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			items = append(items, trimmed)
		}
	}
	if len(items) == 0 {
		return nil
	}
	return items
}

func stringAnyMap(raw any) (map[string]any, bool) {
	switch m := raw.(type) {
	case map[string]any:
		return m, true
	case map[string]string:
		out := make(map[string]any, len(m))
		for k, v := range m {
			out[k] = v
		}
		return out, true
	default:
		return nil, false
	}
}

func stringFromMap(m map[string]any, key string) string {
	raw, ok := m[key]
	if !ok || raw == nil {
		return ""
	}
	value, ok := raw.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func int32FromMap(m map[string]any, key string) (int32, bool) {
	raw, ok := m[key]
	if !ok || raw == nil {
		return 0, false
	}
	return int32FromRuntimeConfigValue(raw)
}

// PodmanExtension carries Podman-specific renderer metadata. Podman reuses
// the Docker-compatible Engine API path where feasible.
type PodmanExtension struct {
	// Rootless indicates the Podman runtime is running in rootless mode.
	// When true, cgroup resource limits may be silently ignored or behave
	// differently depending on the cgroup version.
	Rootless bool `json:"rootless,omitempty"`
}

// ---------------------------------------------------------------------------
// DesiredServiceSpec — service-level desired state
// ---------------------------------------------------------------------------

// DesiredServiceSpec is the typed desired runtime state for a single managed
// service in an environment. It carries enough information to render the
// service for Compose, Docker Engine, and future runtimes.
type DesiredServiceSpec struct {
	// SchemaVersion tracks the serialization format for hash stability.
	SchemaVersion string `json:"schema_version"`

	// Identity
	ServiceID         uuid.UUID   `json:"service_id"`
	EnvironmentID     uuid.UUID   `json:"environment_id"`
	DeploymentUnitID  *uuid.UUID  `json:"deployment_unit_id,omitempty"`
	DeploymentUnitKey string      `json:"deployment_unit_key"`
	UnitRuntimeType   RuntimeType `json:"unit_runtime_type,omitempty"`
	ArtifactID        uuid.UUID   `json:"artifact_id"`

	// StableServiceKey is the normalized runtime name derived from
	// Service.RuntimeTargetName(). It is safe for Compose service names,
	// Docker container names, and filesystem paths.
	StableServiceKey string `json:"stable_service_key"`

	// Image
	ImageRef string `json:"image_ref"`

	// Process
	Command    []string `json:"command,omitempty"`
	Entrypoint []string `json:"entrypoint,omitempty"`
	WorkDir    string   `json:"work_dir,omitempty"`

	// Environment — literal values only; secrets are in SecretRefs.
	Env map[string]string `json:"env,omitempty"`
	// SecretRefs are references to secret-backed env vars. Plaintext is never persisted.
	SecretRefs []DesiredSecretRef `json:"secret_refs,omitempty"`

	// Resources
	Ports          []string               `json:"ports,omitempty"`
	Volumes        []string               `json:"volumes,omitempty"`
	Labels         map[string]string      `json:"labels,omitempty"`
	ResourceLimits *RuntimeResourceLimits `json:"resource_limits,omitempty"`

	// Health
	Healthcheck *HealthcheckConfig `json:"healthcheck,omitempty"`

	// Dependencies and policies
	DependsOn     []string `json:"depends_on,omitempty"`
	NetworkMode   string   `json:"network_mode,omitempty"`
	RestartPolicy string   `json:"restart_policy,omitempty"`
	PullPolicy    string   `json:"pull_policy,omitempty"`

	// PublicRoute is the exact non-secret edge plan approved with this runtime state.
	PublicRoute *DesiredPublicRoutePlan `json:"public_route,omitempty"`

	// DesiredHash is the deterministic hash of the canonical service state.
	// It is computed from the hash-relevant fields and used for drift detection.
	DesiredHash string `json:"desired_hash"`

	// Renderer extensions — at most one is populated per runtime target.
	ComposeExtension    *ComposeExtension    `json:"compose_extension,omitempty"`
	DockerExtension     *DockerExtension     `json:"docker_extension,omitempty"`
	KubernetesExtension *KubernetesExtension `json:"kubernetes_extension,omitempty"`
	PodmanExtension     *PodmanExtension     `json:"podman_extension,omitempty"`
}

// ---------------------------------------------------------------------------
// DesiredEnvironmentPlan — environment-level plan
// ---------------------------------------------------------------------------

// DesiredEnvironmentPlan is the environment-scoped desired runtime state
// containing all managed services. Services are sorted deterministically by
// StableServiceKey for hash stability.
type DesiredEnvironmentPlan struct {
	EnvironmentID uuid.UUID                   `json:"environment_id"`
	RevisionHash  string                      `json:"revision_hash"`
	Services      []DesiredServiceSpec        `json:"services"`
	UnitPlans     []DesiredDeploymentUnitPlan `json:"unit_plans"`
}

// DesiredDeploymentUnitPlan is the unit-scoped subset of an environment plan.
// Services are sorted deterministically by StableServiceKey for hash stability.
type DesiredDeploymentUnitPlan struct {
	DeploymentUnitID  *uuid.UUID           `json:"deployment_unit_id,omitempty"`
	DeploymentUnitKey string               `json:"deployment_unit_key"`
	RuntimeType       RuntimeType          `json:"runtime_type,omitempty"`
	RevisionHash      string               `json:"revision_hash"`
	Services          []DesiredServiceSpec `json:"services"`
}

// SortServices sorts the plan's services deterministically by unit identity and StableServiceKey.
func (p *DesiredEnvironmentPlan) SortServices() {
	sort.Slice(p.Services, func(i, j int) bool {
		left := serviceUnitSortKey(p.Services[i])
		right := serviceUnitSortKey(p.Services[j])
		if left != right {
			return left < right
		}
		return p.Services[i].StableServiceKey < p.Services[j].StableServiceKey
	})
}

// SortServices sorts a unit plan's services deterministically by StableServiceKey.
func (u *DesiredDeploymentUnitPlan) SortServices() {
	sort.Slice(u.Services, func(i, j int) bool {
		return u.Services[i].StableServiceKey < u.Services[j].StableServiceKey
	})
}

// ComputeRevisionHash computes a unit revision hash from sorted service desired hashes.
func (u *DesiredDeploymentUnitPlan) ComputeRevisionHash() string {
	u.SortServices()
	h := sha256.New()
	h.Write([]byte(unitIdentityHashKey(u.DeploymentUnitID, u.DeploymentUnitKey)))
	h.Write([]byte(string(u.RuntimeType)))
	for _, svc := range u.Services {
		h.Write([]byte(svc.DesiredHash))
	}
	u.RevisionHash = fmt.Sprintf("sha256:%x", h.Sum(nil))
	return u.RevisionHash
}

// ComputeRevisionHash computes the aggregate environment revision hash from
// sorted unit revision hashes. Services are sorted and grouped by deployment unit before hashing.
func (p *DesiredEnvironmentPlan) ComputeRevisionHash() string {
	p.NormalizeUnitIdentity()
	p.SortServices()
	p.GroupByDeploymentUnit()
	h := sha256.New()
	for i := range p.UnitPlans {
		u := &p.UnitPlans[i]
		u.ComputeRevisionHash()
		h.Write([]byte(unitIdentityHashKey(u.DeploymentUnitID, u.DeploymentUnitKey)))
		h.Write([]byte(u.RevisionHash))
	}
	p.RevisionHash = fmt.Sprintf("sha256:%x", h.Sum(nil))
	return p.RevisionHash
}

// NormalizeUnitIdentity fills backward-compatible default unit identity on services that predate deployment units.
func (p *DesiredEnvironmentPlan) NormalizeUnitIdentity() {
	for i := range p.Services {
		NormalizeDesiredServiceUnitIdentity(&p.Services[i], nil, "", "")
	}
}

// GroupByDeploymentUnit rebuilds UnitPlans from the flat Services slice.
func (p *DesiredEnvironmentPlan) GroupByDeploymentUnit() {
	units := make(map[string]*DesiredDeploymentUnitPlan)
	keys := make([]string, 0)
	for _, svc := range p.Services {
		key := serviceUnitSortKey(svc)
		unit, ok := units[key]
		if !ok {
			unit = &DesiredDeploymentUnitPlan{
				DeploymentUnitID:  copyUUIDPtr(svc.DeploymentUnitID),
				DeploymentUnitKey: svc.DeploymentUnitKey,
				RuntimeType:       svc.UnitRuntimeType,
			}
			units[key] = unit
			keys = append(keys, key)
		}
		unit.Services = append(unit.Services, svc)
	}
	sort.Strings(keys)
	p.UnitPlans = make([]DesiredDeploymentUnitPlan, 0, len(keys))
	for _, key := range keys {
		unit := units[key]
		unit.ComputeRevisionHash()
		p.UnitPlans = append(p.UnitPlans, *unit)
	}
}

// NormalizeDesiredServiceUnitIdentity applies explicit unit identity when supplied, or the implicit default unit otherwise.
func NormalizeDesiredServiceUnitIdentity(svc *DesiredServiceSpec, unitID *uuid.UUID, unitKey string, runtimeType RuntimeType) {
	if svc == nil {
		return
	}
	if unitID != nil && *unitID != uuid.Nil {
		svc.DeploymentUnitID = copyUUIDPtr(unitID)
	} else if svc.DeploymentUnitID != nil && *svc.DeploymentUnitID == uuid.Nil {
		svc.DeploymentUnitID = nil
	}
	if strings.TrimSpace(unitKey) != "" {
		svc.DeploymentUnitKey = strings.TrimSpace(unitKey)
	}
	if svc.DeploymentUnitKey == "" {
		svc.DeploymentUnitKey = DefaultDeploymentUnitKey
	}
	if runtimeType != "" {
		svc.UnitRuntimeType = runtimeType
	}
}

func canonicalDesiredLabels(labels map[string]string) map[string]string {
	if len(labels) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(labels))
	for k, v := range labels {
		if k == "bahia.desired_hash" {
			continue
		}
		out[k] = v
	}
	return out
}

func copyUUIDPtr(id *uuid.UUID) *uuid.UUID {
	if id == nil || *id == uuid.Nil {
		return nil
	}
	cp := *id
	return &cp
}

func serviceUnitSortKey(svc DesiredServiceSpec) string {
	return unitIdentityHashKey(svc.DeploymentUnitID, svc.DeploymentUnitKey)
}

func unitIdentityHashKey(id *uuid.UUID, key string) string {
	if id != nil && *id != uuid.Nil {
		return "id:" + id.String()
	}
	if strings.TrimSpace(key) == "" {
		key = DefaultDeploymentUnitKey
	}
	return "key:" + strings.TrimSpace(key)
}

// ---------------------------------------------------------------------------
// Service key normalization
// ---------------------------------------------------------------------------

// serviceKeyRegexp matches characters that are NOT alphanumeric, hyphen, or underscore.
var serviceKeyRegexp = regexp.MustCompile(`[^a-z0-9_-]+`)

// NormalizeServiceKey normalizes a runtime target name into a string safe for
// Compose service names, Docker container names, and filesystem paths.
// It lowercases, replaces unsafe characters with hyphens, collapses runs,
// and trims leading/trailing hyphens.
func NormalizeServiceKey(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = serviceKeyRegexp.ReplaceAllString(s, "-")
	// Collapse multiple hyphens.
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	s = strings.Trim(s, "-_")
	if s == "" {
		return "unnamed"
	}
	return s
}

// NormalizeServiceKeyWithSuffix normalizes a service key and appends a short
// ID suffix when the normalized form is lossy (collides with another key) or
// empty before fallback.
func NormalizeServiceKeyWithSuffix(name string, serviceID uuid.UUID) string {
	base := NormalizeServiceKey(name)
	if base == "unnamed" || base == "" {
		// Use short ID fragment for unnamed services.
		return fmt.Sprintf("svc-%s", serviceID.String()[:8])
	}
	return base
}

// ---------------------------------------------------------------------------
// Canonical hashing
// ---------------------------------------------------------------------------

// hashInput is the canonical structure used for deterministic JSON
// serialization. It contains only hash-relevant fields with sorted keys
// (struct field order is stable in encoding/json). Volatile fields like
// DesiredHash itself, extensions, and timestamps are excluded.
type hashInput struct {
	SchemaVersion     string                  `json:"schema_version"`
	ServiceID         uuid.UUID               `json:"service_id"`
	EnvironmentID     uuid.UUID               `json:"environment_id"`
	DeploymentUnitID  *uuid.UUID              `json:"deployment_unit_id,omitempty"`
	DeploymentUnitKey string                  `json:"deployment_unit_key"`
	UnitRuntimeType   RuntimeType             `json:"unit_runtime_type,omitempty"`
	ArtifactID        uuid.UUID               `json:"artifact_id"`
	StableServiceKey  string                  `json:"stable_service_key"`
	ImageRef          string                  `json:"image_ref"`
	Command           []string                `json:"command"`
	Entrypoint        []string                `json:"entrypoint"`
	WorkDir           string                  `json:"work_dir"`
	Env               map[string]string       `json:"env"`
	SecretRefKeys     []string                `json:"secret_ref_keys"`
	Ports             []string                `json:"ports"`
	Volumes           []string                `json:"volumes"`
	Labels            map[string]string       `json:"labels"`
	ResourceLimits    *RuntimeResourceLimits  `json:"resource_limits"`
	Healthcheck       *HealthcheckConfig      `json:"healthcheck"`
	DependsOn         []string                `json:"depends_on"`
	NetworkMode       string                  `json:"network_mode"`
	RestartPolicy     string                  `json:"restart_policy"`
	PullPolicy        string                  `json:"pull_policy"`
	PublicRoute       *DesiredPublicRoutePlan `json:"public_route,omitempty"`
}

// ComputeDesiredHash computes the deterministic hash of a DesiredServiceSpec.
// The hash covers identity, image, process, environment (keys only for secrets),
// resources, health, and policies. Extensions and the hash field itself are excluded.
//
// Map keys are sorted by encoding/json; slice fields should already be in
// canonical order (ports, volumes sorted lexicographically by the caller).
func (s *DesiredServiceSpec) ComputeDesiredHash() string {
	NormalizeDesiredServiceUnitIdentity(s, nil, "", "")

	// Collect secret ref env var names (sorted) — only presence matters for hash.
	secretKeys := make([]string, 0, len(s.SecretRefs))
	for _, ref := range s.SecretRefs {
		secretKeys = append(secretKeys, ref.EnvVar)
	}
	sort.Strings(secretKeys)

	// Normalize nil slices to empty for stable JSON.
	cmd := s.Command
	if cmd == nil {
		cmd = []string{}
	}
	ep := s.Entrypoint
	if ep == nil {
		ep = []string{}
	}
	env := s.Env
	if env == nil {
		env = map[string]string{}
	}
	ports := s.Ports
	if ports == nil {
		ports = []string{}
	}
	sort.Strings(ports)
	volumes := s.Volumes
	if volumes == nil {
		volumes = []string{}
	}
	sort.Strings(volumes)
	labels := canonicalDesiredLabels(s.Labels)
	deps := s.DependsOn
	if deps == nil {
		deps = []string{}
	}
	sort.Strings(deps)

	input := hashInput{
		SchemaVersion:     s.SchemaVersion,
		ServiceID:         s.ServiceID,
		EnvironmentID:     s.EnvironmentID,
		DeploymentUnitID:  s.DeploymentUnitID,
		DeploymentUnitKey: s.DeploymentUnitKey,
		UnitRuntimeType:   s.UnitRuntimeType,
		ArtifactID:        s.ArtifactID,
		StableServiceKey:  s.StableServiceKey,
		ImageRef:          s.ImageRef,
		Command:           cmd,
		Entrypoint:        ep,
		WorkDir:           s.WorkDir,
		Env:               env,
		SecretRefKeys:     secretKeys,
		Ports:             ports,
		Volumes:           volumes,
		Labels:            labels,
		ResourceLimits:    s.ResourceLimits,
		Healthcheck:       s.Healthcheck,
		DependsOn:         deps,
		NetworkMode:       s.NetworkMode,
		RestartPolicy:     s.RestartPolicy,
		PullPolicy:        s.PullPolicy,
	}
	// Route plans were introduced in schema v4. Preserve historical v3 hashes.
	if s.SchemaVersion != "3" {
		input.PublicRoute = s.PublicRoute
	}

	data, err := json.Marshal(input)
	if err != nil {
		// hashInput is fully controlled; marshal should never fail.
		panic(fmt.Sprintf("domain: failed to marshal hash input: %v", err))
	}

	sum := sha256.Sum256(data)
	s.DesiredHash = fmt.Sprintf("sha256:%x", sum[:])
	return s.DesiredHash
}

// ContainsPlaintextSecret returns true if any SecretRef contains a value that
// is not a REDACTED(...) placeholder. This is a safety check to prevent
// accidental persistence of plaintext secrets.
func (s *DesiredServiceSpec) ContainsPlaintextSecret() bool {
	for _, ref := range s.SecretRefs {
		if ref.RedactedValue != "" && !strings.HasPrefix(ref.RedactedValue, "REDACTED(") {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Normalized observation — comparable runtime state snapshot
// ---------------------------------------------------------------------------

// NormalizedObservation is a renderer-agnostic snapshot of observed runtime
// state, normalized into a comparable subset for drift detection. It uses the
// same canonical serialization approach as desired-state hashing.
//
// Included: image ref/digest, command, entrypoint, working dir, non-secret
// env, secret env key presence, ports, volumes, restart policy, network
// attachments, and Bahia labels.
//
// Excluded: container IDs, timestamps, ephemeral IPs, Compose-generated
// non-semantic labels, and secret plaintext.
type NormalizedObservation struct {
	// SchemaVersion tracks the serialization format for hash stability.
	SchemaVersion string `json:"schema_version"`

	// Image
	ImageRef    string `json:"image_ref"`
	ImageDigest string `json:"image_digest,omitempty"`

	// Process
	Command    []string `json:"command,omitempty"`
	Entrypoint []string `json:"entrypoint,omitempty"`
	WorkDir    string   `json:"work_dir,omitempty"`

	// Environment — non-secret literal values only.
	Env map[string]string `json:"env,omitempty"`
	// SecretEnvKeys lists env var names known to be secret-backed.
	// Only key presence is tracked; plaintext is never stored.
	SecretEnvKeys []string `json:"secret_env_keys,omitempty"`

	// Resources
	Ports   []string `json:"ports,omitempty"`
	Volumes []string `json:"volumes,omitempty"`

	// Policies
	RestartPolicy string `json:"restart_policy,omitempty"`

	// Network
	NetworkAttachments []string `json:"network_attachments,omitempty"`

	// Labels — only Bahia-managed labels (bahia.* prefix).
	BahiaLabels map[string]string `json:"bahia_labels,omitempty"`

	// ObservationHash is the deterministic hash of the normalized observation.
	ObservationHash string `json:"observation_hash,omitempty"`
}

// observationHashInput is the canonical structure for deterministic observation
// hashing. It mirrors NormalizedObservation but with explicit nil-to-empty
// normalization. The hash excludes the ObservationHash field itself.
type observationHashInput struct {
	SchemaVersion      string            `json:"schema_version"`
	ImageRef           string            `json:"image_ref"`
	ImageDigest        string            `json:"image_digest"`
	Command            []string          `json:"command"`
	Entrypoint         []string          `json:"entrypoint"`
	WorkDir            string            `json:"work_dir"`
	Env                map[string]string `json:"env"`
	SecretEnvKeys      []string          `json:"secret_env_keys"`
	Ports              []string          `json:"ports"`
	Volumes            []string          `json:"volumes"`
	RestartPolicy      string            `json:"restart_policy"`
	NetworkAttachments []string          `json:"network_attachments"`
	BahiaLabels        map[string]string `json:"bahia_labels"`
}

// ComputeObservationHash computes the deterministic hash of a NormalizedObservation.
// It uses the same canonical serialization approach as ComputeDesiredHash:
// sorted map keys (via encoding/json), sorted slices, and nil-to-empty normalization.
func (o *NormalizedObservation) ComputeObservationHash() string {
	cmd := o.Command
	if cmd == nil {
		cmd = []string{}
	}
	ep := o.Entrypoint
	if ep == nil {
		ep = []string{}
	}
	env := o.Env
	if env == nil {
		env = map[string]string{}
	}

	secretKeys := make([]string, len(o.SecretEnvKeys))
	copy(secretKeys, o.SecretEnvKeys)
	sort.Strings(secretKeys)

	ports := make([]string, len(o.Ports))
	copy(ports, o.Ports)
	sort.Strings(ports)

	volumes := make([]string, len(o.Volumes))
	copy(volumes, o.Volumes)
	sort.Strings(volumes)

	networks := make([]string, len(o.NetworkAttachments))
	copy(networks, o.NetworkAttachments)
	sort.Strings(networks)

	labels := o.BahiaLabels
	if labels == nil {
		labels = map[string]string{}
	}

	input := observationHashInput{
		SchemaVersion:      o.SchemaVersion,
		ImageRef:           o.ImageRef,
		ImageDigest:        o.ImageDigest,
		Command:            cmd,
		Entrypoint:         ep,
		WorkDir:            o.WorkDir,
		Env:                env,
		SecretEnvKeys:      secretKeys,
		Ports:              ports,
		Volumes:            volumes,
		RestartPolicy:      o.RestartPolicy,
		NetworkAttachments: networks,
		BahiaLabels:        labels,
	}

	data, err := json.Marshal(input)
	if err != nil {
		// observationHashInput is fully controlled; marshal should never fail.
		panic(fmt.Sprintf("domain: failed to marshal observation hash input: %v", err))
	}

	sum := sha256.Sum256(data)
	o.ObservationHash = fmt.Sprintf("sha256:%x", sum[:])
	return o.ObservationHash
}

// FilterBahiaLabels returns a new map containing only labels with the "bahia."
// prefix. This is used to strip Compose-generated and other non-semantic labels
// from runtime observations before normalization.
func FilterBahiaLabels(labels map[string]string) map[string]string {
	result := make(map[string]string)
	for k, v := range labels {
		if strings.HasPrefix(k, "bahia.") {
			result[k] = v
		}
	}
	return result
}

// FilterNonSecretEnv returns a new map with only non-secret env vars and a
// sorted slice of secret env var keys. The secretNames set identifies which
// env var names are secret-backed.
func FilterNonSecretEnv(env map[string]string, secretNames map[string]bool) (nonSecret map[string]string, secretKeys []string) {
	nonSecret = make(map[string]string)
	for k, v := range env {
		if secretNames[k] {
			secretKeys = append(secretKeys, k)
		} else {
			nonSecret[k] = v
		}
	}
	sort.Strings(secretKeys)
	return nonSecret, secretKeys
}
