package domain

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const DefaultDeploymentUnitKey = "default"

// ReconcileMode defines how Bahia responds when observed runtime state differs from desired state.
type ReconcileMode string

const (
	ReconcileModeObserveOnly      ReconcileMode = "observe_only"
	ReconcileModeAutoApply        ReconcileMode = "auto_apply"
	ReconcileModeApprovalRequired ReconcileMode = "approval_required"
	ReconcileModeDisabled         ReconcileMode = "disabled"
)

// OwnershipMode defines the boundary Bahia owns inside a deployment unit.
type OwnershipMode string

const (
	OwnershipModeBahiaManaged OwnershipMode = "bahia_managed"
	OwnershipModeAdopted      OwnershipMode = "adopted"
	OwnershipModeExternal     OwnershipMode = "external"
)

// DeploymentUnit is a runtime ownership boundary inside one environment.
type DeploymentUnit struct {
	ID             uuid.UUID         `json:"id"`
	EnvironmentID  uuid.UUID         `json:"environment_id"`
	Key            string            `json:"key"`
	DisplayName    string            `json:"display_name,omitempty"`
	RuntimeType    RuntimeType       `json:"runtime_type"`
	EndpointRef    string            `json:"endpoint_ref,omitempty"`
	ComposeDir     string            `json:"compose_dir,omitempty"`
	Namespace      string            `json:"namespace,omitempty"`
	NetworkProfile map[string]string `json:"network_profile,omitempty"`
	GitSource      *GitSourceBinding `json:"git_source,omitempty"`
	ReconcileMode  ReconcileMode     `json:"reconcile_mode"`
	OwnershipMode  OwnershipMode     `json:"ownership_mode"`
	RuntimeConfig  map[string]any    `json:"runtime_config,omitempty"`
	Implicit       bool              `json:"implicit"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
}

// GitSourceBinding identifies the git checkout that backs a deployment unit.
type GitSourceBinding struct {
	RepositoryURL string `json:"repository_url,omitempty"`
	Ref           string `json:"ref,omitempty"`
	Branch        string `json:"branch,omitempty"`
	CommitSHA     string `json:"commit_sha,omitempty"`
}

// NewImplicitDefaultDeploymentUnit resolves the in-memory default unit for an environment.
// The implicit unit is not persisted by apply, observe, or ordinary write paths; it becomes
// durable only through the explicit unit creation API or the first multi-unit config change.
func NewImplicitDefaultDeploymentUnit(env *Environment) (*DeploymentUnit, error) {
	if env == nil {
		return nil, fmt.Errorf("%w: environment must not be nil", ErrInvalidValue)
	}
	if env.ID == uuid.Nil {
		return nil, fmt.Errorf("%w: environment_id", ErrNilUUID)
	}

	NormalizeEnvironmentTargeting(env)
	runtimeType := RuntimeTypeFromRuntimeConfig(env.RuntimeConfig)
	if runtimeType == "" {
		runtimeType = RuntimeTypeDocker
	}
	if err := ValidateRuntimeType(runtimeType); err != nil {
		return nil, err
	}

	unit := &DeploymentUnit{
		EnvironmentID: env.ID,
		Key:           env.Targeting.DefaultUnitKey,
		DisplayName:   "Default",
		RuntimeType:   runtimeType,
		ReconcileMode: env.Targeting.DefaultReconcileMode,
		OwnershipMode: OwnershipModeBahiaManaged,
		RuntimeConfig: copyRuntimeConfig(env.RuntimeConfig),
		Implicit:      true,
	}
	NormalizeDeploymentUnitTargeting(unit)
	return unit, nil
}

func RuntimeTypeFromRuntimeConfig(config map[string]any) RuntimeType {
	if config == nil {
		return ""
	}
	if raw, ok := config["type"]; ok {
		if s, ok := raw.(string); ok {
			return RuntimeType(strings.TrimSpace(s))
		}
	}
	return ""
}

func NormalizeEnvironmentTargeting(env *Environment) {
	if env == nil {
		return
	}
	if strings.TrimSpace(env.Targeting.DefaultUnitKey) == "" {
		env.Targeting.DefaultUnitKey = stringFromRuntimeConfig(env.RuntimeConfig, "default_unit_key")
	}
	if strings.TrimSpace(env.Targeting.DefaultUnitKey) == "" {
		env.Targeting.DefaultUnitKey = DefaultDeploymentUnitKey
	}
	if env.Targeting.FailureDomainLabels == nil {
		env.Targeting.FailureDomainLabels = stringMapFromRuntimeConfig(env.RuntimeConfig, "failure_domain_labels")
	}
	if env.Targeting.SecretScopeMode == "" {
		env.Targeting.SecretScopeMode = SecretScopeMode(stringFromRuntimeConfig(env.RuntimeConfig, "secret_scope_mode"))
	}
	if env.Targeting.DefaultReconcileMode == "" {
		env.Targeting.DefaultReconcileMode = ReconcileMode(firstNonEmpty(
			stringFromRuntimeConfig(env.RuntimeConfig, "default_reconcile_mode"),
			stringFromRuntimeConfig(env.RuntimeConfig, "reconcile_mode"),
		))
	}
	if env.Targeting.DefaultReconcileMode == "" {
		env.Targeting.DefaultReconcileMode = ReconcileModeObserveOnly
	}
}

func NormalizeDeploymentUnitTargeting(unit *DeploymentUnit) {
	if unit == nil {
		return
	}
	if unit.RuntimeType == "" {
		unit.RuntimeType = RuntimeTypeFromRuntimeConfig(unit.RuntimeConfig)
	}
	if strings.TrimSpace(unit.EndpointRef) == "" {
		unit.EndpointRef = stringFromRuntimeConfig(unit.RuntimeConfig, "endpoint_ref")
	}
	if strings.TrimSpace(unit.ComposeDir) == "" {
		unit.ComposeDir = stringFromRuntimeConfig(unit.RuntimeConfig, "compose_dir")
	}
	if strings.TrimSpace(unit.Namespace) == "" {
		unit.Namespace = firstNonEmpty(stringFromRuntimeConfig(unit.RuntimeConfig, "namespace"), stringFromRuntimeConfig(unit.RuntimeConfig, "kube_namespace"))
	}
	if unit.NetworkProfile == nil {
		unit.NetworkProfile = stringMapFromRuntimeConfig(unit.RuntimeConfig, "network_profile")
	}
	unit.GitSource = normalizeGitSourceBinding(unit.GitSource, unit.RuntimeConfig)
	if unit.GitSource != nil {
		if unit.RuntimeConfig == nil {
			unit.RuntimeConfig = map[string]any{}
		}
		unit.RuntimeConfig["git_source"] = unit.GitSource.runtimeConfigValue()
	}
	if unit.ReconcileMode == "" {
		unit.ReconcileMode = ReconcileModeObserveOnly
	}
	if unit.OwnershipMode == "" {
		unit.OwnershipMode = OwnershipModeBahiaManaged
	}
}

func stringFromRuntimeConfig(config map[string]any, key string) string {
	if config == nil {
		return ""
	}
	if raw, ok := config[key]; ok {
		if s, ok := raw.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func stringMapFromRuntimeConfig(config map[string]any, key string) map[string]string {
	if config == nil {
		return nil
	}
	raw, ok := config[key]
	if !ok {
		return nil
	}
	out := map[string]string{}
	switch m := raw.(type) {
	case map[string]string:
		for k, v := range m {
			if strings.TrimSpace(k) != "" && strings.TrimSpace(v) != "" {
				out[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
	case map[string]any:
		for k, v := range m {
			if s, ok := v.(string); ok && strings.TrimSpace(k) != "" && strings.TrimSpace(s) != "" {
				out[strings.TrimSpace(k)] = strings.TrimSpace(s)
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeGitSourceBinding(binding *GitSourceBinding, config map[string]any) *GitSourceBinding {
	if binding == nil {
		binding = gitSourceFromRuntimeConfig(config)
	}
	if binding == nil {
		return nil
	}
	out := &GitSourceBinding{
		RepositoryURL: strings.TrimSpace(binding.RepositoryURL),
		Ref:           strings.TrimSpace(binding.Ref),
		Branch:        strings.TrimSpace(binding.Branch),
		CommitSHA:     strings.TrimSpace(binding.CommitSHA),
	}
	if out.RepositoryURL == "" && out.Ref == "" && out.Branch == "" && out.CommitSHA == "" {
		return nil
	}
	return out
}

func gitSourceFromRuntimeConfig(config map[string]any) *GitSourceBinding {
	if config == nil {
		return nil
	}
	if raw, ok := config["git_source"]; ok {
		switch value := raw.(type) {
		case map[string]any:
			return &GitSourceBinding{
				RepositoryURL: firstNonEmpty(stringFromAny(value["repository_url"]), stringFromAny(value["repo_url"]), stringFromAny(value["repository"]), stringFromAny(value["url"])),
				Ref:           firstNonEmpty(stringFromAny(value["ref"]), stringFromAny(value["git_ref"])),
				Branch:        firstNonEmpty(stringFromAny(value["branch"]), stringFromAny(value["default_branch"])),
				CommitSHA:     firstNonEmpty(stringFromAny(value["commit_sha"]), stringFromAny(value["commit"]), stringFromAny(value["commit_hash"])),
			}
		case map[string]string:
			return &GitSourceBinding{
				RepositoryURL: firstNonEmpty(value["repository_url"], value["repo_url"], value["repository"], value["url"]),
				Ref:           firstNonEmpty(value["ref"], value["git_ref"]),
				Branch:        firstNonEmpty(value["branch"], value["default_branch"]),
				CommitSHA:     firstNonEmpty(value["commit_sha"], value["commit"], value["commit_hash"]),
			}
		}
	}
	return &GitSourceBinding{
		RepositoryURL: firstNonEmpty(
			stringFromRuntimeConfig(config, "git_repository_url"),
			stringFromRuntimeConfig(config, "repository_url"),
			stringFromRuntimeConfig(config, "repo_url"),
		),
		Ref:       firstNonEmpty(stringFromRuntimeConfig(config, "git_ref"), stringFromRuntimeConfig(config, "source_ref")),
		Branch:    firstNonEmpty(stringFromRuntimeConfig(config, "git_branch"), stringFromRuntimeConfig(config, "branch"), stringFromRuntimeConfig(config, "default_branch")),
		CommitSHA: firstNonEmpty(stringFromRuntimeConfig(config, "git_commit_sha"), stringFromRuntimeConfig(config, "commit_sha"), stringFromRuntimeConfig(config, "commit_hash")),
	}
}

func (g GitSourceBinding) runtimeConfigValue() map[string]any {
	out := map[string]any{}
	if g.RepositoryURL != "" {
		out["repository_url"] = g.RepositoryURL
	}
	if g.Ref != "" {
		out["ref"] = g.Ref
	}
	if g.Branch != "" {
		out["branch"] = g.Branch
	}
	if g.CommitSHA != "" {
		out["commit_sha"] = g.CommitSHA
	}
	return out
}

func stringFromAny(raw any) string {
	if s, ok := raw.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func copyRuntimeConfig(config map[string]any) map[string]any {
	if len(config) == 0 {
		return nil
	}
	out := make(map[string]any, len(config))
	for k, v := range config {
		out[k] = v
	}
	return out
}

// ValidateReconcileMode checks that a reconcile mode is known.
func ValidateReconcileMode(mode ReconcileMode) error {
	switch mode {
	case ReconcileModeObserveOnly, ReconcileModeAutoApply, ReconcileModeApprovalRequired, ReconcileModeDisabled:
		return nil
	case "":
		return nil
	default:
		return fmt.Errorf("%w: reconcile mode %q is not valid (allowed: observe_only, auto_apply, approval_required, disabled)", ErrInvalidValue, mode)
	}
}

// ValidateOwnershipMode checks that an ownership mode is known.
func ValidateOwnershipMode(mode OwnershipMode) error {
	switch mode {
	case OwnershipModeBahiaManaged, OwnershipModeAdopted, OwnershipModeExternal:
		return nil
	case "":
		return nil
	default:
		return fmt.Errorf("%w: ownership mode %q is not valid (allowed: bahia_managed, adopted, external)", ErrInvalidValue, mode)
	}
}

// ValidateDeploymentUnit checks required deployment unit identity and policy fields.
func ValidateDeploymentUnit(unit *DeploymentUnit) error {
	if unit == nil {
		return fmt.Errorf("%w: deployment unit must not be nil", ErrInvalidValue)
	}
	if unit.EnvironmentID == uuid.Nil {
		return fmt.Errorf("%w: environment_id", ErrNilUUID)
	}
	if strings.TrimSpace(unit.Key) == "" {
		return fmt.Errorf("%w: key must not be empty", ErrEmptyField)
	}
	if err := ValidateRuntimeType(unit.RuntimeType); err != nil {
		return err
	}
	if unit.RuntimeType == "" {
		return fmt.Errorf("%w: runtime_type must not be empty", ErrEmptyField)
	}
	if err := ValidateReconcileMode(unit.ReconcileMode); err != nil {
		return err
	}
	if unit.ReconcileMode == "" {
		return fmt.Errorf("%w: reconcile_mode must not be empty", ErrEmptyField)
	}
	if err := ValidateOwnershipMode(unit.OwnershipMode); err != nil {
		return err
	}
	if unit.OwnershipMode == "" {
		return fmt.Errorf("%w: ownership_mode must not be empty", ErrEmptyField)
	}
	if unit.GitSource != nil && unit.GitSource.RepositoryURL == "" {
		return fmt.Errorf("%w: git_source.repository_url must not be empty", ErrEmptyField)
	}
	return nil
}
