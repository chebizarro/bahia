package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/pkg/client"
	"github.com/spf13/cobra"
)

type environmentTargetingFlags struct {
	defaultUnitKey       string
	failureDomainLabels  []string
	secretScopeMode      string
	defaultReconcileMode string
}

type deploymentUnitFlags struct {
	prefix            string
	key               string
	displayName       string
	runtimeType       string
	endpointRef       string
	composeDir        string
	namespace         string
	networkProfile    []string
	ownershipMode     string
	reconcileMode     string
	executionMode     string
	runtimeConfigFile string
}

func newEnvironmentsCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "environments", Short: "Manage environments", Aliases: []string{"env"}}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List all environments",
		RunE: func(cmd *cobra.Command, args []string) error {
			envs, err := apiClient.ListEnvironments(cmd.Context())
			if err != nil {
				return err
			}
			return output(envs, []string{"ID", "NAME", "STRATEGY", "PROTECTED"}, func(e domain.Environment) []string {
				protected := ""
				if e.Protected {
					protected = "yes"
				}
				return []string{e.ID.String(), e.Name, string(e.DeployStrategy), protected}
			})
		},
	}

	getCmd := &cobra.Command{
		Use:   "get [id]",
		Short: "Get an environment by ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			env, err := apiClient.GetEnvironmentDetails(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return outputSingle(env)
		},
	}

	createCmd := newEnvironmentCreateCommand()
	updateCmd := newEnvironmentUpdateCommand()
	cmd.AddCommand(listCmd, getCmd, createCmd, updateCmd, newEnvironmentUnitsCommand())
	return cmd
}

func newEnvironmentCreateCommand() *cobra.Command {
	var targeting environmentTargetingFlags
	unit := deploymentUnitFlags{prefix: "unit-"}
	var orgID, name, strategy, reconcileMode string
	var protected bool
	var selectorFile, runtimeConfigFile, unitsFile string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create an environment through a signed ContextVM mutation",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			selector, err := readJSONObjectFile(selectorFile)
			if err != nil {
				return fmt.Errorf("read loom worker selector: %w", err)
			}
			runtimeConfig, err := readJSONObjectFile(runtimeConfigFile)
			if err != nil {
				return fmt.Errorf("read environment runtime config: %w", err)
			}
			targetingRequest, err := targeting.request(cmd, nil)
			if err != nil {
				return err
			}
			units, err := requestedUnits(cmd, unitsFile, &unit)
			if err != nil {
				return err
			}
			result, err := runEnvironmentCreateNostr(cmd, client.CreateEnvironmentNostrRequest{
				OrgID:              strings.TrimSpace(orgID),
				Name:               strings.TrimSpace(name),
				LoomWorkerSelector: selector,
				RuntimeConfig:      runtimeConfig,
				Targeting:          targetingRequest,
				ReconcileMode:      strings.TrimSpace(reconcileMode),
				DeploymentUnits:    units,
				DeployStrategy:     strings.TrimSpace(strategy),
				Protected:          protected,
			})
			if err != nil {
				return err
			}
			return outputSingle(result)
		},
	}
	cmd.Flags().StringVar(&orgID, "org", "", "Organization UUID")
	cmd.Flags().StringVar(&name, "name", "", "Environment name")
	cmd.Flags().StringVar(&strategy, "strategy", string(domain.DeployStrategyReplace), "Deploy strategy: replace, blue_green, canary")
	cmd.Flags().BoolVar(&protected, "protected", false, "Require extra protections for deployments")
	cmd.Flags().StringVar(&reconcileMode, "reconcile-mode", "", "Environment reconcile mode: observe_only, auto_apply, approval_required, disabled")
	cmd.Flags().StringVar(&selectorFile, "loom-worker-selector-file", "", "Read loom_worker_selector JSON object from this file")
	cmd.Flags().StringVar(&runtimeConfigFile, "runtime-config-file", "", "Read environment runtime_config JSON object from this file")
	cmd.Flags().StringVar(&unitsFile, "units-file", "", "Read the complete deployment_units JSON array from this file")
	targeting.bind(cmd)
	unit.bind(cmd)
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func newEnvironmentUpdateCommand() *cobra.Command {
	var targeting environmentTargetingFlags
	unit := deploymentUnitFlags{prefix: "unit-"}
	var orgID, name, strategy, reconcileMode string
	var protected bool
	var selectorFile, runtimeConfigFile, unitsFile, expectedUpdatedAt string

	cmd := &cobra.Command{
		Use:   "update [id]",
		Short: "Update an environment through a signed ContextVM mutation",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := client.UpdateEnvironmentNostrRequest{ID: strings.TrimSpace(args[0])}
			if cmd.Flags().Changed("org") {
				req.OrgID = stringPointer(strings.TrimSpace(orgID))
			}
			if cmd.Flags().Changed("name") {
				req.Name = stringPointer(strings.TrimSpace(name))
			}
			if cmd.Flags().Changed("strategy") {
				req.DeployStrategy = stringPointer(strings.TrimSpace(strategy))
			}
			if cmd.Flags().Changed("protected") {
				req.Protected = &protected
			}
			if cmd.Flags().Changed("reconcile-mode") {
				req.ReconcileMode = stringPointer(strings.TrimSpace(reconcileMode))
			}
			if cmd.Flags().Changed("loom-worker-selector-file") {
				value, err := readJSONObjectFile(selectorFile)
				if err != nil {
					return fmt.Errorf("read loom worker selector: %w", err)
				}
				req.LoomWorkerSelector = &value
			}
			if cmd.Flags().Changed("runtime-config-file") {
				value, err := readJSONObjectFile(runtimeConfigFile)
				if err != nil {
					return fmt.Errorf("read environment runtime config: %w", err)
				}
				req.RuntimeConfig = &value
			}
			targetingChanged := anyFlagChanged(cmd, "default-unit-key", "failure-domain-label", "secret-scope-mode", "default-reconcile-mode")
			units, err := requestedUnits(cmd, unitsFile, &unit)
			if err != nil {
				return err
			}
			if units != nil && strings.TrimSpace(unitsFile) != "" && cmd.Flags().Changed("expected-updated-at") {
				revision, parseErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(expectedUpdatedAt))
				if parseErr != nil {
					return fmt.Errorf("parse expected-updated-at: %w", parseErr)
				}
				req.DeploymentUnits = units
				req.ExpectedUpdatedAt = &revision
				if targetingChanged {
					targetingRequest, targetingErr := targeting.request(cmd, nil)
					if targetingErr != nil {
						return targetingErr
					}
					req.Targeting = targetingRequest
				}
				result, updateErr := runEnvironmentUpdateNostr(cmd, req)
				if updateErr != nil {
					return updateErr
				}
				return outputSingle(result)
			}
			if units == nil {
				if targetingChanged {
					details, loadErr := apiClient.GetEnvironmentDetails(cmd.Context(), args[0])
					if loadErr != nil {
						return loadErr
					}
					req.Targeting, err = targeting.request(cmd, &details.Targeting)
					if err != nil {
						return err
					}
				}
				result, updateErr := runEnvironmentUpdateNostr(cmd, req)
				if updateErr != nil {
					return updateErr
				}
				return outputSingle(result)
			}

			result, err := runEnvironmentCompleteSetUpdateWithRetry(cmd, args[0], func(details *client.EnvironmentDetails) (client.UpdateEnvironmentNostrRequest, error) {
				attempt := req
				if targetingChanged {
					var targetingErr error
					attempt.Targeting, targetingErr = targeting.request(cmd, &details.Targeting)
					if targetingErr != nil {
						return client.UpdateEnvironmentNostrRequest{}, targetingErr
					}
				}
				completeSet := append([]client.DeploymentUnitRequest(nil), (*units)...)
				if strings.TrimSpace(unitsFile) == "" {
					completeSet = explicitUnitRequests(details.DeploymentUnits)
					for _, rawRequested := range *units {
						requested := rawRequested
						if current, found := findDeploymentUnit(details.DeploymentUnits, requested.Key); found {
							var overlayErr error
							requested, overlayErr = unit.overlay(cmd, "", deploymentUnitRequestFromDomain(current))
							if overlayErr != nil {
								return client.UpdateEnvironmentNostrRequest{}, overlayErr
							}
						}
						replaced := false
						for i := range completeSet {
							if completeSet[i].Key == requested.Key {
								completeSet[i] = requested
								replaced = true
								break
							}
						}
						if !replaced {
							completeSet = append(completeSet, requested)
						}
					}
				}
				attempt.DeploymentUnits = &completeSet
				return attempt, nil
			})
			if err != nil {
				return err
			}
			return outputSingle(result)
		},
	}
	cmd.Flags().StringVar(&orgID, "org", "", "Organization UUID")
	cmd.Flags().StringVar(&name, "name", "", "Environment name")
	cmd.Flags().StringVar(&strategy, "strategy", "", "Deploy strategy: replace, blue_green, canary")
	cmd.Flags().BoolVar(&protected, "protected", false, "Require extra protections for deployments")
	cmd.Flags().StringVar(&reconcileMode, "reconcile-mode", "", "Environment reconcile mode: observe_only, auto_apply, approval_required, disabled")
	cmd.Flags().StringVar(&selectorFile, "loom-worker-selector-file", "", "Read loom_worker_selector JSON object from this file")
	cmd.Flags().StringVar(&runtimeConfigFile, "runtime-config-file", "", "Read environment runtime_config JSON object from this file")
	cmd.Flags().StringVar(&unitsFile, "units-file", "", "Read the complete deployment_units JSON array from this file")
	cmd.Flags().StringVar(&expectedUpdatedAt, "expected-updated-at", "", "Environment updated_at revision for signer-first complete deployment_units updates")
	targeting.bind(cmd)
	unit.bind(cmd)
	return cmd
}

func newEnvironmentUnitsCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "units", Short: "Manage environment deployment units"}
	cmd.AddCommand(newEnvironmentUnitsListCommand(), newEnvironmentUnitCreateCommand(), newEnvironmentUnitUpdateCommand())
	return cmd
}

func newEnvironmentUnitsListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list [environment-id]",
		Short: "List explicit deployment units or the resolved implicit default",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			env, err := apiClient.GetEnvironmentDetails(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return output(env.DeploymentUnits, []string{"KEY", "RUNTIME", "ENDPOINT", "COMPOSE_DIR", "OWNERSHIP", "RECONCILE", "IMPLICIT"}, func(unit domain.DeploymentUnit) []string {
				implicit := ""
				if unit.Implicit {
					implicit = "yes"
				}
				return []string{unit.Key, string(unit.RuntimeType), unit.EndpointRef, unit.ComposeDir, string(unit.OwnershipMode), string(unit.ReconcileMode), implicit}
			})
		},
	}
}

func newEnvironmentUnitCreateCommand() *cobra.Command {
	unit := deploymentUnitFlags{}
	var specFile, defaultUnitKey string
	cmd := &cobra.Command{
		Use:   "create [environment-id]",
		Short: "Add an explicit unit through a complete-set signed environment update",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			request, err := unit.request(cmd, specFile)
			if err != nil {
				return err
			}
			if strings.TrimSpace(request.Key) == "" {
				return fmt.Errorf("unit key is required via --key or --file")
			}
			result, err := runEnvironmentCompleteSetUpdateWithRetry(cmd, args[0], func(details *client.EnvironmentDetails) (client.UpdateEnvironmentNostrRequest, error) {
				units := explicitUnitRequests(details.DeploymentUnits)
				for _, existing := range units {
					if existing.Key == request.Key {
						return client.UpdateEnvironmentNostrRequest{}, fmt.Errorf("deployment unit %q already exists", request.Key)
					}
				}

				desiredDefault := details.Targeting.DefaultUnitKey
				targeting := (*client.EnvironmentTargetingRequest)(nil)
				if cmd.Flags().Changed("default-unit-key") {
					desiredDefault = strings.TrimSpace(defaultUnitKey)
					targeting = environmentTargetingRequestFromDomain(details.Targeting)
					targeting.DefaultUnitKey = desiredDefault
				}
				if len(units) == 0 && desiredDefault != request.Key {
					for _, current := range details.DeploymentUnits {
						if current.Implicit {
							units = append(units, deploymentUnitRequestFromDomain(current))
							break
						}
					}
				}
				units = append(units, request)
				return client.UpdateEnvironmentNostrRequest{
					ID:              args[0],
					Targeting:       targeting,
					DeploymentUnits: &units,
				}, nil
			})
			if err != nil {
				return err
			}
			return outputSingle(result)
		},
	}
	cmd.Flags().StringVar(&specFile, "file", "", "Read one deployment-unit JSON object from this file")
	cmd.Flags().StringVar(&defaultUnitKey, "default-unit-key", "", "Set the environment default deployment-unit key atomically")
	unit.bind(cmd)
	return cmd
}

func newEnvironmentUnitUpdateCommand() *cobra.Command {
	unit := deploymentUnitFlags{}
	var specFile, defaultUnitKey string
	cmd := &cobra.Command{
		Use:   "update [environment-id] [key]",
		Short: "Update one unit through a read-merge complete-set signed environment update",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := strings.TrimSpace(args[1])
			result, err := runEnvironmentCompleteSetUpdateWithRetry(cmd, args[0], func(details *client.EnvironmentDetails) (client.UpdateEnvironmentNostrRequest, error) {
				current, found := findDeploymentUnit(details.DeploymentUnits, key)
				if !found {
					return client.UpdateEnvironmentNostrRequest{}, fmt.Errorf("deployment unit %q not found", key)
				}
				request := deploymentUnitRequestFromDomain(current)
				updated, err := unit.overlay(cmd, specFile, request)
				if err != nil {
					return client.UpdateEnvironmentNostrRequest{}, err
				}
				if updated.Key == "" {
					updated.Key = key
				}
				if updated.Key != key {
					return client.UpdateEnvironmentNostrRequest{}, fmt.Errorf("deployment unit update cannot change key %q to %q", key, updated.Key)
				}
				units := explicitUnitRequests(details.DeploymentUnits)
				replaced := false
				for i := range units {
					if units[i].Key == key {
						units[i] = updated
						replaced = true
						break
					}
				}
				if !replaced {
					units = append(units, updated)
				}
				var targeting *client.EnvironmentTargetingRequest
				if cmd.Flags().Changed("default-unit-key") {
					targeting = environmentTargetingRequestFromDomain(details.Targeting)
					targeting.DefaultUnitKey = strings.TrimSpace(defaultUnitKey)
				}
				return client.UpdateEnvironmentNostrRequest{
					ID:              args[0],
					Targeting:       targeting,
					DeploymentUnits: &units,
				}, nil
			})
			if err != nil {
				return err
			}
			return outputSingle(result)
		},
	}
	cmd.Flags().StringVar(&specFile, "file", "", "Read one deployment-unit JSON object from this file and overlay changed flags")
	cmd.Flags().StringVar(&defaultUnitKey, "default-unit-key", "", "Set the environment default deployment-unit key atomically")
	unit.bind(cmd)
	return cmd
}

const environmentCompleteSetUpdateMaxAttempts = 3

func runEnvironmentCompleteSetUpdateWithRetry(
	cmd *cobra.Command,
	environmentID string,
	build func(*client.EnvironmentDetails) (client.UpdateEnvironmentNostrRequest, error),
) (*client.EnvironmentCommandResult, error) {
	for attempt := 1; attempt <= environmentCompleteSetUpdateMaxAttempts; attempt++ {
		details, err := apiClient.GetEnvironmentDetails(cmd.Context(), environmentID)
		if err != nil {
			return nil, err
		}
		if details == nil || details.UpdatedAt.IsZero() {
			return nil, fmt.Errorf("environment %s read response is missing updated_at", environmentID)
		}
		req, err := build(details)
		if err != nil {
			return nil, err
		}
		revision := details.UpdatedAt
		req.ID = environmentID
		req.ExpectedUpdatedAt = &revision
		result, err := runEnvironmentUpdateNostr(cmd, req)
		if err == nil {
			return result, nil
		}
		if !errors.Is(err, client.ErrEnvironmentRevisionConflict) {
			return nil, err
		}
		if attempt == environmentCompleteSetUpdateMaxAttempts {
			return nil, fmt.Errorf(
				"environment %s changed during complete-set update after %d attempts: %w",
				environmentID,
				environmentCompleteSetUpdateMaxAttempts,
				client.ErrEnvironmentRevisionConflict,
			)
		}
	}
	return nil, fmt.Errorf("environment %s complete-set update failed", environmentID)
}

func environmentTargetingRequestFromDomain(targeting domain.EnvironmentTargeting) *client.EnvironmentTargetingRequest {
	return &client.EnvironmentTargetingRequest{
		DefaultUnitKey:       targeting.DefaultUnitKey,
		FailureDomainLabels:  targeting.FailureDomainLabels,
		SecretScopeMode:      string(targeting.SecretScopeMode),
		DefaultReconcileMode: string(targeting.DefaultReconcileMode),
	}
}

func (f *environmentTargetingFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.defaultUnitKey, "default-unit-key", "", "Default deployment-unit key")
	cmd.Flags().StringArrayVar(&f.failureDomainLabels, "failure-domain-label", nil, "Targeting failure-domain label as key=value (repeatable)")
	cmd.Flags().StringVar(&f.secretScopeMode, "secret-scope-mode", "", "Secret scope mode: service, environment, unit")
	cmd.Flags().StringVar(&f.defaultReconcileMode, "default-reconcile-mode", "", "Default reconcile mode: observe_only, auto_apply, approval_required, disabled")
}

func (f environmentTargetingFlags) request(cmd *cobra.Command, current *domain.EnvironmentTargeting) (*client.EnvironmentTargetingRequest, error) {
	if !anyFlagChanged(cmd, "default-unit-key", "failure-domain-label", "secret-scope-mode", "default-reconcile-mode") {
		return nil, nil
	}
	request := &client.EnvironmentTargetingRequest{}
	if current != nil {
		request.DefaultUnitKey = current.DefaultUnitKey
		request.FailureDomainLabels = current.FailureDomainLabels
		request.SecretScopeMode = string(current.SecretScopeMode)
		request.DefaultReconcileMode = string(current.DefaultReconcileMode)
	}
	if cmd.Flags().Changed("default-unit-key") {
		request.DefaultUnitKey = strings.TrimSpace(f.defaultUnitKey)
	}
	if cmd.Flags().Changed("failure-domain-label") {
		labels, err := parseKeyValueFlags(f.failureDomainLabels, "failure-domain-label")
		if err != nil {
			return nil, err
		}
		request.FailureDomainLabels = labels
	}
	if cmd.Flags().Changed("secret-scope-mode") {
		request.SecretScopeMode = strings.TrimSpace(f.secretScopeMode)
	}
	if cmd.Flags().Changed("default-reconcile-mode") {
		request.DefaultReconcileMode = strings.TrimSpace(f.defaultReconcileMode)
	}
	return request, nil
}

func (f *deploymentUnitFlags) bind(cmd *cobra.Command) {
	name := func(value string) string { return f.prefix + value }
	cmd.Flags().StringVar(&f.key, name("key"), "", "Deployment-unit key")
	cmd.Flags().StringVar(&f.displayName, name("display-name"), "", "Deployment-unit display name")
	cmd.Flags().StringVar(&f.runtimeType, name("runtime-type"), "", "Runtime type: docker, compose, kubernetes, podman, vm-firecracker, vm-qemu")
	cmd.Flags().StringVar(&f.endpointRef, name("endpoint-ref"), "", "Managed runtime endpoint reference")
	cmd.Flags().StringVar(&f.composeDir, name("compose-dir"), "", "Compose project directory on the managed endpoint")
	cmd.Flags().StringVar(&f.namespace, name("namespace"), "", "Runtime namespace")
	cmd.Flags().StringArrayVar(&f.networkProfile, name("network-profile"), nil, "Network profile entry as key=value (repeatable)")
	cmd.Flags().StringVar(&f.ownershipMode, name("ownership-mode"), "", "Ownership mode: bahia_managed, adopted, external")
	cmd.Flags().StringVar(&f.reconcileMode, name("reconcile-mode"), "", "Unit reconcile mode: observe_only, auto_apply, approval_required, disabled")
	cmd.Flags().StringVar(&f.executionMode, name("execution-mode"), "", "Runtime execution mode stored in runtime_config: sdk or cli")
	cmd.Flags().StringVar(&f.runtimeConfigFile, name("runtime-config-file"), "", "Read unit runtime_config JSON object from this file")
}

func (f deploymentUnitFlags) request(cmd *cobra.Command, specFile string) (client.DeploymentUnitRequest, error) {
	return f.overlay(cmd, specFile, client.DeploymentUnitRequest{})
}

func (f deploymentUnitFlags) overlay(cmd *cobra.Command, specFile string, request client.DeploymentUnitRequest) (client.DeploymentUnitRequest, error) {
	if strings.TrimSpace(specFile) != "" {
		fromFile, err := readDeploymentUnitFile(specFile)
		if err != nil {
			return request, err
		}
		request = fromFile
	}
	name := func(value string) string { return f.prefix + value }
	if cmd.Flags().Changed(name("key")) {
		request.Key = strings.TrimSpace(f.key)
	}
	if cmd.Flags().Changed(name("display-name")) {
		request.DisplayName = strings.TrimSpace(f.displayName)
	}
	if cmd.Flags().Changed(name("runtime-type")) {
		request.RuntimeType = strings.TrimSpace(f.runtimeType)
	}
	if cmd.Flags().Changed(name("endpoint-ref")) {
		request.EndpointRef = strings.TrimSpace(f.endpointRef)
	}
	if cmd.Flags().Changed(name("compose-dir")) {
		request.ComposeDir = strings.TrimSpace(f.composeDir)
	}
	if cmd.Flags().Changed(name("namespace")) {
		request.Namespace = strings.TrimSpace(f.namespace)
	}
	if cmd.Flags().Changed(name("network-profile")) {
		value, err := parseKeyValueFlags(f.networkProfile, name("network-profile"))
		if err != nil {
			return request, err
		}
		request.NetworkProfile = value
	}
	if cmd.Flags().Changed(name("ownership-mode")) {
		request.OwnershipMode = strings.TrimSpace(f.ownershipMode)
	}
	if cmd.Flags().Changed(name("reconcile-mode")) {
		request.ReconcileMode = strings.TrimSpace(f.reconcileMode)
	}
	if cmd.Flags().Changed(name("runtime-config-file")) {
		value, err := readJSONObjectFile(f.runtimeConfigFile)
		if err != nil {
			return request, fmt.Errorf("read unit runtime config: %w", err)
		}
		request.RuntimeConfig = value
	}
	if cmd.Flags().Changed(name("execution-mode")) {
		if request.RuntimeConfig == nil {
			request.RuntimeConfig = map[string]any{}
		}
		request.RuntimeConfig["execution_mode"] = strings.TrimSpace(f.executionMode)
	}
	return request, nil
}

func requestedUnits(cmd *cobra.Command, unitsFile string, unit *deploymentUnitFlags) (*[]client.DeploymentUnitRequest, error) {
	unitFlagsChanged := unit.anyChanged(cmd)
	if strings.TrimSpace(unitsFile) != "" && unitFlagsChanged {
		return nil, fmt.Errorf("--units-file cannot be combined with individual --unit-* flags")
	}
	if strings.TrimSpace(unitsFile) != "" {
		units, err := readDeploymentUnitsFile(unitsFile)
		if err != nil {
			return nil, err
		}
		return &units, nil
	}
	if !unitFlagsChanged {
		return nil, nil
	}
	request, err := unit.request(cmd, "")
	if err != nil {
		return nil, err
	}
	if request.Key == "" {
		return nil, fmt.Errorf("--unit-key is required when individual unit flags are used")
	}
	units := []client.DeploymentUnitRequest{request}
	return &units, nil
}

func (f deploymentUnitFlags) anyChanged(cmd *cobra.Command) bool {
	name := func(value string) string { return f.prefix + value }
	return anyFlagChanged(cmd, name("key"), name("display-name"), name("runtime-type"), name("endpoint-ref"), name("compose-dir"), name("namespace"), name("network-profile"), name("ownership-mode"), name("reconcile-mode"), name("execution-mode"), name("runtime-config-file"))
}

func anyFlagChanged(cmd *cobra.Command, names ...string) bool {
	for _, name := range names {
		if cmd.Flags().Changed(name) {
			return true
		}
	}
	return false
}

func parseKeyValueFlags(values []string, flagName string) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	result := make(map[string]string, len(values))
	for _, value := range values {
		key, item, ok := strings.Cut(value, "=")
		key = strings.TrimSpace(key)
		item = strings.TrimSpace(item)
		if !ok || key == "" || item == "" {
			return nil, fmt.Errorf("--%s value must be key=value", flagName)
		}
		if _, exists := result[key]; exists {
			return nil, fmt.Errorf("duplicate --%s key %q", flagName, key)
		}
		result[key] = item
	}
	return result, nil
}

func readJSONObjectFile(path string) (map[string]any, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	var value map[string]any
	if err := decodeJSONFile(path, &value, false); err != nil {
		return nil, err
	}
	if value == nil {
		return nil, fmt.Errorf("%s must contain a JSON object", path)
	}
	return value, nil
}

func readManagedRuntimeConfigFile(path string) (*domain.ManagedRuntimeConfig, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	var value domain.ManagedRuntimeConfig
	if err := decodeJSONFile(path, &value, true); err != nil {
		return nil, err
	}
	return &value, nil
}

func readDeploymentUnitFile(path string) (client.DeploymentUnitRequest, error) {
	var unit client.DeploymentUnitRequest
	if err := decodeJSONFile(path, &unit, true); err != nil {
		return unit, fmt.Errorf("read deployment unit: %w", err)
	}
	return unit, nil
}

func readDeploymentUnitsFile(path string) ([]client.DeploymentUnitRequest, error) {
	var units []client.DeploymentUnitRequest
	if err := decodeJSONFile(path, &units, true); err != nil {
		return nil, fmt.Errorf("read deployment units: %w", err)
	}
	if units == nil {
		return nil, fmt.Errorf("%s must contain a JSON array", path)
	}
	return units, nil
}

func decodeJSONFile(path string, target any, strict bool) error {
	content, err := os.ReadFile(strings.TrimSpace(path))
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	if strict {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

func explicitUnitRequests(units []domain.DeploymentUnit) []client.DeploymentUnitRequest {
	result := make([]client.DeploymentUnitRequest, 0, len(units))
	for _, unit := range units {
		if unit.Implicit {
			continue
		}
		result = append(result, deploymentUnitRequestFromDomain(unit))
	}
	return result
}

func deploymentUnitRequestFromDomain(unit domain.DeploymentUnit) client.DeploymentUnitRequest {
	return client.DeploymentUnitRequest{
		Key:            unit.Key,
		DisplayName:    unit.DisplayName,
		RuntimeType:    string(unit.RuntimeType),
		EndpointRef:    unit.EndpointRef,
		ComposeDir:     unit.ComposeDir,
		Namespace:      unit.Namespace,
		NetworkProfile: unit.NetworkProfile,
		OwnershipMode:  string(unit.OwnershipMode),
		ReconcileMode:  string(unit.ReconcileMode),
		RuntimeConfig:  unit.RuntimeConfig,
	}
}

func findDeploymentUnit(units []domain.DeploymentUnit, key string) (domain.DeploymentUnit, bool) {
	for _, unit := range units {
		if unit.Key == key {
			return unit, true
		}
	}
	return domain.DeploymentUnit{}, false
}

func stringPointer(value string) *string {
	return &value
}
