package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// RenderResult — output of the Compose renderer
// ---------------------------------------------------------------------------

// RenderResult holds the rendered Compose project output.
type RenderResult struct {
	// ComposeYAML is the canonical docker-compose.yml content.
	ComposeYAML []byte

	// EnvMaterial maps service keys to their generated .env file content.
	// Secret values are included here (for writing to protected env files)
	// but are NEVER included in metadata or logs.
	EnvMaterial map[string]string

	// Metadata records render provenance and state for .bahia/render-state.json.
	Metadata RenderMetadata
}

// RenderMetadata records provenance and state about a Compose render pass.
// Secret values are NEVER included in metadata.
type RenderMetadata struct {
	SchemaVersion     int       `json:"schema_version"`
	Renderer          string    `json:"renderer"`
	RenderedAt        time.Time `json:"rendered_at"`
	EnvironmentID     string    `json:"environment_id"`
	DeploymentUnitID  string    `json:"deployment_unit_id,omitempty"`
	DeploymentUnitKey string    `json:"deployment_unit_key,omitempty"`
	RevisionHash      string    `json:"revision_hash"`
	ProjectName       string    `json:"project_name"`
	ServiceCount      int       `json:"service_count"`
	ServiceKeys       []string  `json:"service_keys"`
	ContentHash       string    `json:"content_hash"`
	NetworksDeclared  []string  `json:"networks_declared,omitempty"`
	VolumesDeclared   []string  `json:"volumes_declared,omitempty"`

	// ServiceHashes maps service keys to their DesiredHash values at render time.
	// Used by fragment eligibility to determine which services changed.
	ServiceHashes map[string]string `json:"service_hashes,omitempty"`

	// ServiceDependsOn maps service keys to their effective sorted depends_on keys
	// at render time. Used by fragment eligibility to detect dependency changes.
	ServiceDependsOn map[string][]string `json:"service_depends_on,omitempty"`
}

// ---------------------------------------------------------------------------
// ComposeRenderer — deterministic full-project Compose renderer
// ---------------------------------------------------------------------------

// ComposeRenderer renders a DesiredEnvironmentPlan into canonical Compose YAML,
// generated env material, and render metadata. Output ordering is fully
// deterministic: services are sorted by key, and all maps use sorted keys.
type ComposeRenderer struct{}

// NewComposeRenderer creates a new ComposeRenderer.
func NewComposeRenderer() *ComposeRenderer {
	return &ComposeRenderer{}
}

// RenderEnvironmentPlan renders a full environment desired-state plan into
// canonical Compose YAML, generated env material, and render metadata.
//
// The renderer:
//   - Sorts services deterministically by StableServiceKey
//   - Uses explicit Compose project name
//   - Renders all service fields from the desired-state contracts
//   - Omits secret values from metadata and logs
//   - Produces stable, reproducible output for golden test comparison
func (r *ComposeRenderer) RenderEnvironmentPlan(ctx context.Context, plan *domain.DesiredEnvironmentPlan) (*RenderResult, error) {
	if plan == nil {
		return nil, fmt.Errorf("compose renderer: plan is nil")
	}
	unit := domain.DesiredDeploymentUnitPlan{
		DeploymentUnitKey: domain.DefaultDeploymentUnitKey,
		RevisionHash:      plan.RevisionHash,
		Services:          append([]domain.DesiredServiceSpec(nil), plan.Services...),
	}
	return r.RenderDeploymentUnitPlan(ctx, plan.EnvironmentID.String(), &unit)
}

// RenderDeploymentUnitPlan renders the full Compose project owned by a single
// deployment unit. Compose output is unit-scoped even when the source desired
// state was assembled at environment scope.
func (r *ComposeRenderer) RenderDeploymentUnitPlan(ctx context.Context, environmentID string, unitPlan *domain.DesiredDeploymentUnitPlan) (*RenderResult, error) {
	_ = ctx
	if unitPlan == nil {
		return nil, fmt.Errorf("compose renderer: unit plan is nil")
	}
	if len(unitPlan.Services) == 0 {
		return nil, fmt.Errorf("compose renderer: unit plan has no services")
	}
	if unitPlan.RuntimeType != "" && unitPlan.RuntimeType != domain.RuntimeTypeCompose {
		return nil, fmt.Errorf("compose renderer: unit %q runtime type %q is not compose", unitPlan.DeploymentUnitKey, unitPlan.RuntimeType)
	}

	unitPlan.SortServices()
	plan := &domain.DesiredEnvironmentPlan{
		Services: append([]domain.DesiredServiceSpec(nil), unitPlan.Services...),
	}
	if parsedEnvironmentID, err := uuid.Parse(environmentID); err == nil {
		plan.EnvironmentID = parsedEnvironmentID
	}
	projectName := r.projectName(plan)
	doc := r.buildComposeDocument(plan, projectName)
	composeYAML, err := marshalDeterministicYAML(doc)
	if err != nil {
		return nil, fmt.Errorf("compose renderer: marshal YAML: %w", err)
	}
	envMaterial := r.buildEnvMaterial(plan)
	networks, volumes := r.collectDeclarations(plan)
	serviceKeys := make([]string, 0, len(plan.Services))
	for _, svc := range plan.Services {
		serviceKeys = append(serviceKeys, svc.StableServiceKey)
	}
	contentHash := fmt.Sprintf("sha256:%x", sha256.Sum256(composeYAML))

	// Populate service hash and dependency maps for fragment eligibility.
	serviceHashes := make(map[string]string, len(plan.Services))
	serviceDependsOn := make(map[string][]string)
	for _, svc := range plan.Services {
		serviceHashes[svc.StableServiceKey] = svc.DesiredHash
		if deps := collectEffectiveDependsOn(svc); len(deps) > 0 {
			serviceDependsOn[svc.StableServiceKey] = deps
		}
	}

	unitID := ""
	if unitPlan.DeploymentUnitID != nil {
		unitID = unitPlan.DeploymentUnitID.String()
	}
	metadata := RenderMetadata{
		SchemaVersion:     1,
		Renderer:          "compose",
		RenderedAt:        time.Now().UTC(),
		EnvironmentID:     environmentID,
		DeploymentUnitID:  unitID,
		DeploymentUnitKey: unitPlan.DeploymentUnitKey,
		RevisionHash:      unitPlan.RevisionHash,
		ProjectName:       projectName,
		ServiceCount:      len(plan.Services),
		ServiceKeys:       serviceKeys,
		ContentHash:       contentHash,
		NetworksDeclared:  networks,
		VolumesDeclared:   volumes,
		ServiceHashes:     serviceHashes,
		ServiceDependsOn:  serviceDependsOn,
	}
	return &RenderResult{ComposeYAML: composeYAML, EnvMaterial: envMaterial, Metadata: metadata}, nil
}

// ---------------------------------------------------------------------------
// Project name derivation
// ---------------------------------------------------------------------------

// projectName derives the explicit Compose project name from the environment.
// It uses the ComposeExtension.ProjectName from the first service that has one,
// falling back to a normalized environment ID prefix.
func (r *ComposeRenderer) projectName(plan *domain.DesiredEnvironmentPlan) string {
	for _, svc := range plan.Services {
		if svc.ComposeExtension != nil && svc.ComposeExtension.ProjectName != "" {
			return domain.NormalizeServiceKey(svc.ComposeExtension.ProjectName)
		}
	}
	// Fallback: bahia-<short-env-id>
	envID := plan.EnvironmentID.String()
	if len(envID) >= 8 {
		return "bahia-" + envID[:8]
	}
	return "bahia-project"
}

// ---------------------------------------------------------------------------
// Compose document building
// ---------------------------------------------------------------------------

// composeDocument is the top-level Compose file structure.
// We use an ordered representation to ensure deterministic YAML output.
type composeDocument struct {
	Name     string
	Services map[string]composeService
	Networks map[string]composeNetwork
	Volumes  map[string]composeVolume
}

type composeService struct {
	Image         string
	ContainerName string
	Command       []string
	Entrypoint    []string
	WorkingDir    string
	Environment   map[string]string
	EnvFile       []string
	Ports         []string
	Volumes       []string
	Labels        map[string]string
	Healthcheck   *composeHealthcheck
	DependsOn     map[string]composeDependsOn
	NetworkMode   string
	Networks      []string
	Restart       string
	PullPolicy    string
}

type composeHealthcheck struct {
	Test        []string `yaml:"test,flow"`
	Interval    string   `yaml:"interval,omitempty"`
	Timeout     string   `yaml:"timeout,omitempty"`
	Retries     int      `yaml:"retries,omitempty"`
	StartPeriod string   `yaml:"start_period,omitempty"`
}

type composeDependsOn struct {
	Condition string
}

type composeNetwork struct {
	// Empty struct means use defaults; fields can be added later.
	External bool
}

type composeVolume struct {
	// Empty struct means use defaults; fields can be added later.
	External bool
}

// buildComposeDocument constructs the full Compose document from a plan.
func (r *ComposeRenderer) buildComposeDocument(plan *domain.DesiredEnvironmentPlan, projectName string) composeDocument {
	doc := composeDocument{
		Name:     projectName,
		Services: make(map[string]composeService, len(plan.Services)),
		Networks: make(map[string]composeNetwork),
		Volumes:  make(map[string]composeVolume),
	}

	for _, svc := range plan.Services {
		cs := r.buildComposeService(svc)
		doc.Services[svc.StableServiceKey] = cs

		// Collect network declarations from the service extension.
		if svc.ComposeExtension != nil {
			for _, net := range svc.ComposeExtension.Networks {
				doc.Networks[net] = composeNetwork{}
			}
			for _, vol := range svc.ComposeExtension.VolumeDeclarations {
				doc.Volumes[vol] = composeVolume{}
			}
		}
	}

	return doc
}

// buildComposeService maps a DesiredServiceSpec to a Compose service definition.
func (r *ComposeRenderer) buildComposeService(svc domain.DesiredServiceSpec) composeService {
	cs := composeService{
		Image:      svc.ImageRef,
		Command:    svc.Command,
		Entrypoint: svc.Entrypoint,
		WorkingDir: svc.WorkDir,
		Ports:      sortedCopy(svc.Ports),
		Volumes:    sortedCopy(svc.Volumes),
		Restart:    svc.RestartPolicy,
		PullPolicy: svc.PullPolicy,
	}

	// Container name — use stable service key for deterministic naming.
	cs.ContainerName = svc.StableServiceKey

	// Environment — only literal (non-secret) values go into the Compose file.
	if len(svc.Env) > 0 {
		cs.Environment = make(map[string]string, len(svc.Env))
		for k, v := range svc.Env {
			cs.Environment[k] = v
		}
	}

	// Env file reference — if the extension specifies one.
	if svc.ComposeExtension != nil && svc.ComposeExtension.EnvFile != "" {
		cs.EnvFile = []string{svc.ComposeExtension.EnvFile}
	}

	// Labels — merge Bahia labels with service labels.
	if len(svc.Labels) > 0 {
		cs.Labels = make(map[string]string, len(svc.Labels))
		for k, v := range svc.Labels {
			cs.Labels[k] = v
		}
	}

	// Healthcheck.
	if svc.Healthcheck != nil {
		hc := svc.Healthcheck
		// Allow Compose extension to override the portable healthcheck.
		if svc.ComposeExtension != nil && svc.ComposeExtension.HealthcheckOverride != nil {
			hc = svc.ComposeExtension.HealthcheckOverride
		}
		cs.Healthcheck = &composeHealthcheck{
			Test:        hc.Test,
			Interval:    hc.Interval,
			Timeout:     hc.Timeout,
			Retries:     hc.Retries,
			StartPeriod: hc.StartPeriod,
		}
	}

	// DependsOn — from extension or fallback to simple depends_on list.
	if svc.ComposeExtension != nil && len(svc.ComposeExtension.DependsOn) > 0 {
		cs.DependsOn = make(map[string]composeDependsOn, len(svc.ComposeExtension.DependsOn))
		for depKey, dep := range svc.ComposeExtension.DependsOn {
			cs.DependsOn[depKey] = composeDependsOn{Condition: dep.Condition}
		}
	} else if len(svc.DependsOn) > 0 {
		cs.DependsOn = make(map[string]composeDependsOn, len(svc.DependsOn))
		for _, depKey := range svc.DependsOn {
			cs.DependsOn[depKey] = composeDependsOn{Condition: "service_started"}
		}
	}

	// Network mode.
	cs.NetworkMode = svc.NetworkMode

	// Networks from extension.
	if svc.ComposeExtension != nil && len(svc.ComposeExtension.Networks) > 0 {
		cs.Networks = sortedCopy(svc.ComposeExtension.Networks)
	}

	return cs
}

// ---------------------------------------------------------------------------
// Env material generation
// ---------------------------------------------------------------------------

// buildEnvMaterial generates .env file content for each service, keyed by
// stable service key. This includes resolved secret refs (for file writing)
// and literal env vars. The caller is responsible for writing these to
// protected files; they are NEVER included in metadata or logs.
func (r *ComposeRenderer) buildEnvMaterial(plan *domain.DesiredEnvironmentPlan) map[string]string {
	material := make(map[string]string, len(plan.Services))

	for _, svc := range plan.Services {
		if len(svc.Env) == 0 && len(svc.SecretRefs) == 0 {
			continue
		}

		var lines []string

		// Literal env vars — sorted for determinism.
		envKeys := sortedKeys(svc.Env)
		for _, k := range envKeys {
			lines = append(lines, fmt.Sprintf("%s=%s", k, svc.Env[k]))
		}

		// Secret refs — write redacted placeholders only.
		// Actual secret resolution happens at apply time, not render time.
		// The env material serves as a template; the apply path fills in
		// real values when writing the file to disk.
		secretRefs := make([]domain.DesiredSecretRef, len(svc.SecretRefs))
		copy(secretRefs, svc.SecretRefs)
		sort.Slice(secretRefs, func(i, j int) bool {
			return secretRefs[i].EnvVar < secretRefs[j].EnvVar
		})
		for _, ref := range secretRefs {
			lines = append(lines, fmt.Sprintf("%s=%s", ref.EnvVar, ref.RedactedValue))
		}

		if len(lines) > 0 {
			material[svc.StableServiceKey] = strings.Join(lines, "\n") + "\n"
		}
	}

	return material
}

// ---------------------------------------------------------------------------
// Declaration collection
// ---------------------------------------------------------------------------

// collectDeclarations gathers unique sorted network and volume names from
// the plan for metadata.
func (r *ComposeRenderer) collectDeclarations(plan *domain.DesiredEnvironmentPlan) (networks, volumes []string) {
	netSet := make(map[string]struct{})
	volSet := make(map[string]struct{})

	for _, svc := range plan.Services {
		if svc.ComposeExtension == nil {
			continue
		}
		for _, n := range svc.ComposeExtension.Networks {
			netSet[n] = struct{}{}
		}
		for _, v := range svc.ComposeExtension.VolumeDeclarations {
			volSet[v] = struct{}{}
		}
	}

	networks = sortedKeysFromSet(netSet)
	volumes = sortedKeysFromSet(volSet)
	return networks, volumes
}

// ---------------------------------------------------------------------------
// Deterministic YAML marshaling
// ---------------------------------------------------------------------------

// marshalDeterministicYAML produces canonical YAML from a composeDocument
// with fully deterministic key ordering. We build the YAML node tree manually
// to guarantee map key order, which encoding/yaml.v3 preserves when using
// yaml.Node.
func marshalDeterministicYAML(doc composeDocument) ([]byte, error) {
	root := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}

	// name:
	addScalarPair(root, "name", doc.Name)

	// services:
	servicesNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	serviceKeys := sortedKeys(doc.Services)
	for _, key := range serviceKeys {
		svc := doc.Services[key]
		svcNode := buildServiceNode(svc)
		addNodePair(servicesNode, key, svcNode)
	}
	addNodePair(root, "services", servicesNode)

	// networks: (only if non-empty)
	if len(doc.Networks) > 0 {
		networksNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for _, key := range sortedKeys(doc.Networks) {
			net := doc.Networks[key]
			netNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			if net.External {
				addScalarPair(netNode, "external", "true")
			}
			addNodePair(networksNode, key, netNode)
		}
		addNodePair(root, "networks", networksNode)
	}

	// volumes: (only if non-empty)
	if len(doc.Volumes) > 0 {
		volumesNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for _, key := range sortedKeys(doc.Volumes) {
			vol := doc.Volumes[key]
			volNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			if vol.External {
				addScalarPair(volNode, "external", "true")
			}
			addNodePair(volumesNode, key, volNode)
		}
		addNodePair(root, "volumes", volumesNode)
	}

	docNode := &yaml.Node{
		Kind:    yaml.DocumentNode,
		Content: []*yaml.Node{root},
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(docNode); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// buildServiceNode constructs a yaml.Node for a single Compose service
// with deterministic field ordering.
func buildServiceNode(svc composeService) *yaml.Node {
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}

	// image (required)
	if svc.Image != "" {
		addScalarPair(node, "image", svc.Image)
	}

	// container_name
	if svc.ContainerName != "" {
		addScalarPair(node, "container_name", svc.ContainerName)
	}

	// command
	if len(svc.Command) > 0 {
		addSequencePair(node, "command", svc.Command)
	}

	// entrypoint
	if len(svc.Entrypoint) > 0 {
		addSequencePair(node, "entrypoint", svc.Entrypoint)
	}

	// working_dir
	if svc.WorkingDir != "" {
		addScalarPair(node, "working_dir", svc.WorkingDir)
	}

	// environment (sorted keys)
	if len(svc.Environment) > 0 {
		envNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for _, k := range sortedKeys(svc.Environment) {
			addScalarPair(envNode, k, svc.Environment[k])
		}
		addNodePair(node, "environment", envNode)
	}

	// env_file
	if len(svc.EnvFile) > 0 {
		addSequencePair(node, "env_file", svc.EnvFile)
	}

	// ports (sorted)
	if len(svc.Ports) > 0 {
		addSequencePair(node, "ports", svc.Ports)
	}

	// volumes (sorted)
	if len(svc.Volumes) > 0 {
		addSequencePair(node, "volumes", svc.Volumes)
	}

	// labels (sorted keys)
	if len(svc.Labels) > 0 {
		labelsNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for _, k := range sortedKeys(svc.Labels) {
			addScalarPair(labelsNode, k, svc.Labels[k])
		}
		addNodePair(node, "labels", labelsNode)
	}

	// healthcheck
	if svc.Healthcheck != nil {
		hcNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		if len(svc.Healthcheck.Test) > 0 {
			addSequencePair(hcNode, "test", svc.Healthcheck.Test)
		}
		if svc.Healthcheck.Interval != "" {
			addScalarPair(hcNode, "interval", svc.Healthcheck.Interval)
		}
		if svc.Healthcheck.Timeout != "" {
			addScalarPair(hcNode, "timeout", svc.Healthcheck.Timeout)
		}
		if svc.Healthcheck.Retries > 0 {
			addScalarPair(hcNode, "retries", fmt.Sprintf("%d", svc.Healthcheck.Retries))
		}
		if svc.Healthcheck.StartPeriod != "" {
			addScalarPair(hcNode, "start_period", svc.Healthcheck.StartPeriod)
		}
		addNodePair(node, "healthcheck", hcNode)
	}

	// depends_on (sorted keys)
	if len(svc.DependsOn) > 0 {
		depsNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		depKeys := make([]string, 0, len(svc.DependsOn))
		for k := range svc.DependsOn {
			depKeys = append(depKeys, k)
		}
		sort.Strings(depKeys)
		for _, k := range depKeys {
			dep := svc.DependsOn[k]
			depNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			if dep.Condition != "" {
				addScalarPair(depNode, "condition", dep.Condition)
			}
			addNodePair(depsNode, k, depNode)
		}
		addNodePair(node, "depends_on", depsNode)
	}

	// network_mode
	if svc.NetworkMode != "" {
		addScalarPair(node, "network_mode", svc.NetworkMode)
	}

	// networks
	if len(svc.Networks) > 0 {
		addSequencePair(node, "networks", svc.Networks)
	}

	// restart
	if svc.Restart != "" {
		addScalarPair(node, "restart", svc.Restart)
	}

	// pull_policy
	if svc.PullPolicy != "" {
		addScalarPair(node, "pull_policy", svc.PullPolicy)
	}

	return node
}

// ---------------------------------------------------------------------------
// YAML node helpers — guarantee key ordering
// ---------------------------------------------------------------------------

func addScalarPair(parent *yaml.Node, key, value string) {
	parent.Content = append(parent.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value},
	)
}

func addNodePair(parent *yaml.Node, key string, value *yaml.Node) {
	parent.Content = append(parent.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		value,
	)
}

func addSequencePair(parent *yaml.Node, key string, items []string) {
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, item := range items {
		seq.Content = append(seq.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: item},
		)
	}
	parent.Content = append(parent.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		seq,
	)
}

// ---------------------------------------------------------------------------
// Utility helpers
// ---------------------------------------------------------------------------

// sortedKeys returns the sorted keys from a map with string keys.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedKeysFromSet(m map[string]struct{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedCopy(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	cp := make([]string, len(s))
	copy(cp, s)
	sort.Strings(cp)
	return cp
}

// MetadataJSON serializes RenderMetadata to indented JSON, suitable for
// writing to .bahia/render-state.json. Secret values are never present
// in the metadata structure by design.
func (m *RenderMetadata) MetadataJSON() ([]byte, error) {
	return json.MarshalIndent(m, "", "  ")
}
