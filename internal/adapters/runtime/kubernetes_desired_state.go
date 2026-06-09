package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// Deployment naming
// ---------------------------------------------------------------------------

// BahiaK8sDeploymentName returns the stable Deployment name for a desired
// service spec. Pattern: bahia-<env_id_prefix>-<stable_service_key>.
// Uses the same 8-char environment-ID prefix convention as BahiaContainerName.
func BahiaK8sDeploymentName(spec *domain.DesiredServiceSpec) string {
	if spec == nil {
		return ""
	}
	envPrefix := spec.EnvironmentID.String()[:8]
	return fmt.Sprintf("bahia-%s-%s", envPrefix, spec.StableServiceKey)
}

// ---------------------------------------------------------------------------
// Manifest types
// ---------------------------------------------------------------------------

// K8sManifest holds the generated Kubernetes resource manifests for a desired
// service spec.
type K8sManifest struct {
	// Deployment is the Kubernetes Deployment manifest (always non-nil).
	Deployment map[string]any
	// Service is the Kubernetes Service manifest. It is nil unless the
	// KubernetesExtension specifies a non-empty ServiceType.
	Service map[string]any
}

// ---------------------------------------------------------------------------
// K8s extension accessor
// ---------------------------------------------------------------------------

// k8sExtension provides nil-safe accessors for KubernetesExtension fields.
// All methods return safe zero/default values while KubernetesExtension remains
// an empty struct. When the struct gains fields (per the KubernetesExtension
// contract), update each accessor to return the real value instead of its
// placeholder default.
type k8sExtension struct {
	ext *domain.KubernetesExtension
}

func newK8sExtension(spec *domain.DesiredServiceSpec) k8sExtension {
	if spec == nil {
		return k8sExtension{}
	}
	return k8sExtension{ext: spec.KubernetesExtension}
}

// Namespace returns the K8s namespace override, or "" if not set.
// TODO: return e.ext.Namespace when KubernetesExtension gains the Namespace field.
func (e k8sExtension) Namespace() string { return "" }

// Replicas returns the desired replica count (default 1).
// TODO: use *e.ext.Replicas when KubernetesExtension gains the Replicas field.
func (e k8sExtension) Replicas() int32 { return 1 }

// ServiceType returns the K8s Service type string, or "" if no Service should
// be created.
// TODO: return e.ext.ServiceType when KubernetesExtension gains the ServiceType field.
func (e k8sExtension) ServiceType() string { return "" }

// ServicePorts returns K8s Service port configuration entries.
// TODO: map e.ext.ServicePorts when KubernetesExtension gains the ServicePorts field.
func (e k8sExtension) ServicePorts() []map[string]any { return nil }

// Resources returns the K8s resource limits/requests map, or nil if not set.
// TODO: map ResourceLimits/ResourceRequests when KubernetesExtension gains those fields.
func (e k8sExtension) Resources() map[string]any { return nil }

// Annotations returns deployment metadata annotation overrides, or nil.
// TODO: return e.ext.Annotations when KubernetesExtension gains the Annotations field.
func (e k8sExtension) Annotations() map[string]string { return nil }

// NodeSelector returns the pod node selector map, or nil.
// TODO: return e.ext.NodeSelector when KubernetesExtension gains the NodeSelector field.
func (e k8sExtension) NodeSelector() map[string]string { return nil }

// Tolerations returns the pod tolerations slice, or nil.
// TODO: map e.ext.Tolerations when KubernetesExtension gains the Tolerations field.
func (e k8sExtension) Tolerations() []map[string]any { return nil }

// ImagePullSecrets returns imagePullSecrets entries for the pod spec, or nil.
// TODO: map e.ext.ImagePullSecrets when KubernetesExtension gains the field.
func (e k8sExtension) ImagePullSecrets() []map[string]any { return nil }

// LivenessProbe returns the liveness probe map, or nil if not set.
// TODO: map e.ext.LivenessProbe when KubernetesExtension gains the LivenessProbe field.
func (e k8sExtension) LivenessProbe() map[string]any { return nil }

// ReadinessProbe returns the readiness probe map, or nil if not set.
// TODO: map e.ext.ReadinessProbe when KubernetesExtension gains the ReadinessProbe field.
func (e k8sExtension) ReadinessProbe() map[string]any { return nil }

// ---------------------------------------------------------------------------
// Manifest generation
// ---------------------------------------------------------------------------

// MapDesiredSpecToK8sManifest deterministically maps a DesiredServiceSpec and
// resolved secrets into Kubernetes Deployment (and optional Service) manifests
// suitable for kubectl apply.
//
// The output is deterministic: given the same spec and secrets, the structural
// output is identical (env vars are sorted; labels are stable).
//
// A Service manifest is included only when the KubernetesExtension specifies a
// non-empty ServiceType.
func MapDesiredSpecToK8sManifest(spec *domain.DesiredServiceSpec, secrets map[string]string) (*K8sManifest, error) {
	if spec == nil {
		return nil, fmt.Errorf("spec is nil")
	}

	ext := newK8sExtension(spec)

	namespace := ext.Namespace()
	if namespace == "" {
		namespace = "default"
	}

	deploymentName := BahiaK8sDeploymentName(spec)
	labels := buildK8sLabels(spec)

	// Metadata annotations: desired_hash + any extension overrides.
	annotations := map[string]string{
		"bahia.desired_hash": spec.DesiredHash,
	}
	for k, v := range ext.Annotations() {
		annotations[k] = v
	}

	// Build primary container definition.
	container := map[string]any{
		"name":  spec.StableServiceKey,
		"image": spec.ImageRef,
	}
	if env := buildK8sEnv(spec, secrets); len(env) > 0 {
		container["env"] = env
	}
	if len(spec.Command) > 0 {
		container["command"] = spec.Command
	}

	vm := buildK8sVolumeMappings(spec.Volumes)
	if len(vm.VolumeMounts) > 0 {
		container["volumeMounts"] = vm.VolumeMounts
	}
	if ports := buildK8sContainerPorts(spec.Ports); len(ports) > 0 {
		container["ports"] = ports
	}
	if resources := ext.Resources(); resources != nil {
		container["resources"] = resources
	}
	if lp := ext.LivenessProbe(); lp != nil {
		container["livenessProbe"] = lp
	}
	if rp := ext.ReadinessProbe(); rp != nil {
		container["readinessProbe"] = rp
	}

	// K8s Deployment pod templates must always use restartPolicy=Always.
	podSpec := map[string]any{
		"containers":    []any{container},
		"restartPolicy": "Always",
	}
	if ns := ext.NodeSelector(); len(ns) > 0 {
		podSpec["nodeSelector"] = ns
	}
	if tols := ext.Tolerations(); len(tols) > 0 {
		podSpec["tolerations"] = tols
	}
	if ips := ext.ImagePullSecrets(); len(ips) > 0 {
		podSpec["imagePullSecrets"] = ips
	}
	if len(vm.Volumes) > 0 {
		podSpec["volumes"] = vm.Volumes
	}

	replicas := ext.Replicas()

	deployment := map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata": map[string]any{
			"name":        deploymentName,
			"namespace":   namespace,
			"labels":      labels,
			"annotations": annotations,
		},
		"spec": map[string]any{
			"replicas": replicas,
			"selector": map[string]any{
				"matchLabels": map[string]string{
					"bahia.service": spec.StableServiceKey,
				},
			},
			"template": map[string]any{
				"metadata": map[string]any{
					"labels": labels,
				},
				"spec": podSpec,
			},
		},
	}

	manifest := &K8sManifest{Deployment: deployment}

	// Optionally emit a Service manifest when the extension specifies ServiceType.
	if svcType := ext.ServiceType(); svcType != "" {
		manifest.Service = buildK8sService(
			deploymentName, namespace,
			spec.StableServiceKey, svcType,
			ext.ServicePorts(), labels,
		)
	}

	return manifest, nil
}

// ---------------------------------------------------------------------------
// Label helpers
// ---------------------------------------------------------------------------

// buildK8sLabels returns the label map for Deployment/Service resources. It
// merges spec labels with the mandatory Bahia tracking labels.
func buildK8sLabels(spec *domain.DesiredServiceSpec) map[string]string {
	labels := make(map[string]string, len(spec.Labels)+5)
	for k, v := range spec.Labels {
		labels[k] = v
	}
	labels["bahia.managed"] = "true"
	labels["bahia.service_id"] = spec.ServiceID.String()
	labels["bahia.environment_id"] = spec.EnvironmentID.String()
	labels["bahia.desired_hash"] = spec.DesiredHash
	if _, ok := labels["bahia.service"]; !ok {
		labels["bahia.service"] = spec.StableServiceKey
	}
	return labels
}

// ---------------------------------------------------------------------------
// Env helpers
// ---------------------------------------------------------------------------

// buildK8sEnv produces a deterministically-sorted slice of Kubernetes env var
// entries ({name, value}) from the spec's literal env and resolved secrets.
// Secret env vars are injected at apply time from the secrets map; redacted
// placeholders are never emitted.
func buildK8sEnv(spec *domain.DesiredServiceSpec, secrets map[string]string) []map[string]string {
	envMap := make(map[string]string, len(spec.Env)+len(spec.SecretRefs))
	for k, v := range spec.Env {
		envMap[k] = v
	}
	for _, ref := range spec.SecretRefs {
		if val, ok := secrets[ref.EnvVar]; ok {
			envMap[ref.EnvVar] = val
		}
	}
	if len(envMap) == 0 {
		return nil
	}
	keys := make([]string, 0, len(envMap))
	for k := range envMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	result := make([]map[string]string, 0, len(keys))
	for _, k := range keys {
		result = append(result, map[string]string{"name": k, "value": envMap[k]})
	}
	return result
}

// ---------------------------------------------------------------------------
// Port helpers
// ---------------------------------------------------------------------------

// buildK8sContainerPorts converts Docker-style port specs ("hostPort:containerPort"
// or bare "containerPort", optionally with "/udp" suffix) into K8s containerPort
// entries. Duplicate container ports are silently de-duplicated.
func buildK8sContainerPorts(ports []string) []map[string]any {
	var result []map[string]any
	seen := make(map[string]bool)
	for _, p := range ports {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// Extract optional protocol suffix: "8080:80/udp" → proto="UDP".
		proto := "TCP"
		if idx := strings.LastIndex(p, "/"); idx > 0 {
			protoStr := strings.ToUpper(p[idx+1:])
			if protoStr == "UDP" || protoStr == "SCTP" {
				proto = protoStr
			}
			p = p[:idx]
		}
		// Extract container port from "hostPort:containerPort" or bare "containerPort".
		containerPort := p
		if idx := strings.LastIndex(p, ":"); idx > 0 {
			containerPort = p[idx+1:]
		}
		portNum, err := strconv.Atoi(strings.TrimSpace(containerPort))
		if err != nil || portNum < 1 || portNum > 65535 {
			continue
		}
		key := fmt.Sprintf("%d/%s", portNum, proto)
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, map[string]any{
			"containerPort": portNum,
			"protocol":      proto,
		})
	}
	return result
}

// ---------------------------------------------------------------------------
// Volume helpers
// ---------------------------------------------------------------------------

// k8sVolumeMapping holds the parallel pod-spec volumes and container
// volumeMounts entries derived from a spec's Volumes slice.
type k8sVolumeMapping struct {
	Volumes      []map[string]any // pod spec .spec.volumes entries
	VolumeMounts []map[string]any // container .volumeMounts entries
}

// buildK8sVolumeMappings converts Docker-style volume specs into hostPath
// Volumes and corresponding VolumeMounts.
//
// Only host bind mounts (paths starting with "/") are supported for now;
// named volumes are skipped since they require PersistentVolume infrastructure
// beyond the scope of this renderer.
func buildK8sVolumeMappings(volumes []string) k8sVolumeMapping {
	var podVols []map[string]any
	var mounts []map[string]any
	seen := make(map[string]bool)

	for _, v := range volumes {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		parts := strings.SplitN(v, ":", 3)
		if len(parts) < 2 {
			continue
		}
		hostPath := parts[0]
		containerPath := parts[1]
		if !strings.HasPrefix(hostPath, "/") {
			continue // skip named volumes
		}

		// Derive a stable K8s-safe volume name from the host path.
		// e.g. "/data/api" → "voldata-api"
		volName := "vol" + strings.ReplaceAll(strings.TrimPrefix(hostPath, "/"), "/", "-")
		if volName == "vol" || volName == "" {
			continue
		}
		if seen[volName] {
			continue
		}
		seen[volName] = true

		readOnly := len(parts) == 3 && strings.EqualFold(parts[2], "ro")

		podVols = append(podVols, map[string]any{
			"name": volName,
			"hostPath": map[string]any{
				"path": hostPath,
			},
		})
		mount := map[string]any{
			"name":      volName,
			"mountPath": containerPath,
		}
		if readOnly {
			mount["readOnly"] = true
		}
		mounts = append(mounts, mount)
	}

	return k8sVolumeMapping{Volumes: podVols, VolumeMounts: mounts}
}

// ---------------------------------------------------------------------------
// Service manifest builder
// ---------------------------------------------------------------------------

// buildK8sService constructs a Kubernetes Service manifest.
func buildK8sService(name, namespace, serviceKey, serviceType string, ports []map[string]any, labels map[string]string) map[string]any {
	svcSpec := map[string]any{
		"type": serviceType,
		"selector": map[string]string{
			"bahia.service": serviceKey,
		},
	}
	if len(ports) > 0 {
		svcSpec["ports"] = ports
	}
	return map[string]any{
		"apiVersion": "v1",
		"kind":       "Service",
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
			"labels":    labels,
		},
		"spec": svcSpec,
	}
}

// ---------------------------------------------------------------------------
// Deployment lookup
// ---------------------------------------------------------------------------

// k8sDeploymentInfo holds metadata about an existing Bahia-managed Kubernetes
// Deployment located during desired-state convergence.
type k8sDeploymentInfo struct {
	Name        string
	Namespace   string
	DesiredHash string            // from annotations["bahia.desired_hash"]
	Labels      map[string]string
	Replicas    int32
}

// FindBahiaManagedDeployment locates an existing Bahia-managed Kubernetes
// Deployment by label selector (bahia.service_id + bahia.environment_id).
// Returns nil (no error) when no matching Deployment is found.
func (k *KubernetesRuntime) FindBahiaManagedDeployment(ctx context.Context, spec *domain.DesiredServiceSpec) (*k8sDeploymentInfo, error) {
	if spec == nil {
		return nil, nil
	}

	labelSelector := fmt.Sprintf(
		"bahia.service_id=%s,bahia.environment_id=%s",
		spec.ServiceID.String(), spec.EnvironmentID.String(),
	)
	args := k.baseArgs("get", "deployment", "-l", labelSelector, "-o", "json")

	output, err := k.runCommand(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("listing deployments: %w", err)
	}

	var list struct {
		Items []struct {
			Metadata struct {
				Name        string            `json:"name"`
				Namespace   string            `json:"namespace"`
				Labels      map[string]string `json:"labels"`
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
			Spec struct {
				Replicas *int32 `json:"replicas"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(output), &list); err != nil {
		return nil, fmt.Errorf("parsing deployment list: %w", err)
	}
	if len(list.Items) == 0 {
		return nil, nil
	}

	item := list.Items[0]
	var replicas int32
	if item.Spec.Replicas != nil {
		replicas = *item.Spec.Replicas
	}

	// Desired hash: prefer annotation (canonical), fall back to label for
	// deployments applied before annotations were introduced.
	desiredHash := ""
	if item.Metadata.Annotations != nil {
		desiredHash = item.Metadata.Annotations["bahia.desired_hash"]
	}
	if desiredHash == "" && item.Metadata.Labels != nil {
		desiredHash = item.Metadata.Labels["bahia.desired_hash"]
	}

	return &k8sDeploymentInfo{
		Name:        item.Metadata.Name,
		Namespace:   item.Metadata.Namespace,
		DesiredHash: desiredHash,
		Labels:      item.Metadata.Labels,
		Replicas:    replicas,
	}, nil
}

// ---------------------------------------------------------------------------
// Hash comparison
// ---------------------------------------------------------------------------

// DeploymentDesiredHashMatches checks whether an existing deployment's
// bahia.desired_hash annotation matches the spec's desired hash.
// Returns false if either argument is nil or if the existing hash is empty.
func DeploymentDesiredHashMatches(info *k8sDeploymentInfo, spec *domain.DesiredServiceSpec) bool {
	if info == nil || spec == nil {
		return false
	}
	return info.DesiredHash != "" && info.DesiredHash == spec.DesiredHash
}

// ---------------------------------------------------------------------------
// DesiredStateApplier implementation
// ---------------------------------------------------------------------------

// SupportsDesiredState returns true — the Kubernetes adapter supports
// desired-state convergence via kubectl apply.
func (k *KubernetesRuntime) SupportsDesiredState() bool { return true }

// ApplyDesiredState converges a single Kubernetes Deployment toward its
// desired runtime state using kubectl apply.
//
// Flow:
//  1. Validate request (nil spec check)
//  2. Resolve namespace: KubernetesExtension.Namespace or k.kubeNamespace
//  3. Find existing Bahia-managed Deployment via label selector
//  4. Hash match + non-always pull policy → no-op return
//  5. DryRun → preview result without mutation
//  6. Generate K8s Deployment (+ optional Service) manifests
//  7. kubectl apply -f - (JSON on stdin) for each manifest
//  8. Return DesiredStateApplyResult with renderer="kubernetes"
func (k *KubernetesRuntime) ApplyDesiredState(ctx context.Context, req DesiredStateApplyRequest) (*DesiredStateApplyResult, error) {
	if req.TargetService == nil {
		return nil, fmt.Errorf("kubernetes apply: target service spec is nil")
	}

	spec := req.TargetService
	deploymentName := BahiaK8sDeploymentName(spec)

	ext := newK8sExtension(spec)
	namespace := ext.Namespace()
	if namespace == "" {
		namespace = k.kubeNamespace
	}

	k.logger.Info("kubernetes apply: converging desired state",
		zap.String("service_key", spec.StableServiceKey),
		zap.String("deployment_name", deploymentName),
		zap.String("namespace", namespace),
		zap.String("desired_hash", spec.DesiredHash),
	)

	// Step 1: Find existing managed Deployment.
	existing, err := k.FindBahiaManagedDeployment(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("kubernetes apply: finding managed deployment: %w", err)
	}

	// Step 2: No-op when the desired hash already matches.
	if existing != nil && DeploymentDesiredHashMatches(existing, spec) {
		pullPolicy := normalizePullPolicy(req.PullPolicy, spec.PullPolicy)
		if pullPolicy != "always" {
			k.logger.Info("kubernetes apply: desired hash matches, no-op",
				zap.String("deployment", existing.Name),
				zap.String("desired_hash", spec.DesiredHash),
			)
			return &DesiredStateApplyResult{
				Renderer:            "kubernetes",
				ExecutionMode:       ExecutionModeCLI,
				DesiredHash:         spec.DesiredHash,
				EnvironmentRevision: environmentRevision(req.EnvironmentPlan),
				ResourceNames:       []string{deploymentName},
				ObservationHints:    &ObservationHints{},
			}, nil
		}
	}

	// Step 3: Dry run — report what would happen without mutating.
	if req.DryRun {
		action := "create"
		if existing != nil {
			action = "update"
		}
		return &DesiredStateApplyResult{
			Renderer:            "kubernetes",
			ExecutionMode:       ExecutionModeCLI,
			DesiredHash:         spec.DesiredHash,
			EnvironmentRevision: environmentRevision(req.EnvironmentPlan),
			Warnings: []string{
				fmt.Sprintf("dry-run: would %s deployment %s in namespace %s", action, deploymentName, namespace),
			},
		}, nil
	}

	// Step 4: Generate manifests.
	manifests, err := MapDesiredSpecToK8sManifest(spec, req.Secrets)
	if err != nil {
		return nil, fmt.Errorf("kubernetes apply: generating manifests: %w", err)
	}

	// Step 5: Apply Deployment.
	if err := k.applyManifest(ctx, manifests.Deployment); err != nil {
		return nil, fmt.Errorf("kubernetes apply: applying deployment: %w", err)
	}
	k.logger.Info("kubernetes apply: deployment applied",
		zap.String("deployment", deploymentName),
		zap.String("namespace", namespace),
	)

	// Step 6: Apply Service (if generated by extension).
	if manifests.Service != nil {
		if err := k.applyManifest(ctx, manifests.Service); err != nil {
			return nil, fmt.Errorf("kubernetes apply: applying service: %w", err)
		}
		k.logger.Info("kubernetes apply: service applied",
			zap.String("service", deploymentName),
			zap.String("namespace", namespace),
		)
	}

	return &DesiredStateApplyResult{
		Renderer:            "kubernetes",
		ExecutionMode:       ExecutionModeCLI,
		DesiredHash:         spec.DesiredHash,
		EnvironmentRevision: environmentRevision(req.EnvironmentPlan),
		ResourceNames:       []string{deploymentName},
		ObservationHints:    &ObservationHints{},
	}, nil
}

// applyManifest marshals a Kubernetes resource manifest to JSON and applies it
// by piping the JSON to kubectl apply -f -.
func (k *KubernetesRuntime) applyManifest(ctx context.Context, manifest map[string]any) error {
	data, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("marshaling manifest: %w", err)
	}
	args := k.baseArgs("apply", "-f", "-")
	output, err := k.execCommand(ctx, args, data)
	if err != nil {
		return fmt.Errorf("kubectl apply: %w (output: %s)", err, output)
	}
	return nil
}
