// Package main is the entrypoint for the Bahia CLI.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/pkg/client"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var (
	serverURL    string
	outputFormat string
	apiClient    *client.Client
	configDir    string
)

func main() {
	// Determine config directory
	home, _ := os.UserHomeDir()
	configDir = filepath.Join(home, ".bahia")

	rootCmd := &cobra.Command{
		Use:   "bahia",
		Short: "Bahia Deployment Registry CLI",
		Long:  "Command-line interface for the Bahia Nostr-Native Deployment Registry Service",
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			apiClient = client.New(serverURL)
			// Load saved auth token
			if token := loadAuthToken(); token != "" {
				apiClient.SetAuthToken(token)
			}
		},
	}

	rootCmd.PersistentFlags().StringVar(&serverURL, "server", getEnvOrDefault("BAHIA_SERVER", "http://localhost:8080"), "Bahia server URL")
	rootCmd.PersistentFlags().StringVarP(&outputFormat, "output", "o", "table", "Output format: table, json, yaml")

	// Add all command groups
	rootCmd.AddCommand(
		authCommands(),
		servicesCommands(),
		environmentsCommands(),
		stateCommands(),
		deployCommands(),
		adoptCommands(),
		workersCommands(),
		logsCommands(),
		policiesCommands(),
		secretsCommands(),
		orgsCommands(),
		eventsCommands(),
		soulFactoryCommands(),
	)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// --- Auth Commands ---

func authCommands() *cobra.Command {
	authCmd := &cobra.Command{Use: "auth", Short: "Authentication commands"}

	loginCmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate with the server",
		Long:  "Login using a JWT token, nsec key, or NIP-46 connection string",
		RunE: func(cmd *cobra.Command, args []string) error {
			token, _ := cmd.Flags().GetString("token")
			nsec, _ := cmd.Flags().GetString("nsec")
			nip46, _ := cmd.Flags().GetString("nip46")

			if token != "" {
				// Direct JWT token
				if err := saveAuthToken(token); err != nil {
					return fmt.Errorf("saving token: %w", err)
				}
				fmt.Println("✓ Logged in with token")
				return nil
			}

			if nsec != "" {
				// TODO: Generate NIP-98 auth event and exchange for token
				fmt.Println("⚠ nsec authentication not yet implemented")
				fmt.Println("  Use --token with a JWT token for now")
				return nil
			}

			if nip46 != "" {
				// TODO: NIP-46 Nostr Connect flow
				fmt.Println("⚠ NIP-46 authentication not yet implemented")
				fmt.Println("  Use --token with a JWT token for now")
				return nil
			}

			return fmt.Errorf("specify --token, --nsec, or --nip46")
		},
	}
	loginCmd.Flags().String("token", "", "JWT token")
	loginCmd.Flags().String("nsec", "", "Nostr secret key (nsec)")
	loginCmd.Flags().String("nip46", "", "NIP-46 connection string (bunker://...)")

	logoutCmd := &cobra.Command{
		Use:   "logout",
		Short: "Clear saved credentials",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := clearAuthToken(); err != nil {
				return err
			}
			fmt.Println("✓ Logged out")
			return nil
		},
	}

	whoamiCmd := &cobra.Command{
		Use:   "whoami",
		Short: "Show current authentication status",
		RunE: func(cmd *cobra.Command, args []string) error {
			token := loadAuthToken()
			if token == "" {
				fmt.Println("Not logged in")
				return nil
			}
			fmt.Println("Logged in (token saved)")
			// TODO: Decode JWT and show subject/pubkey
			return nil
		},
	}

	authCmd.AddCommand(loginCmd, logoutCmd, whoamiCmd)
	return authCmd
}

// --- Services Commands ---

func servicesCommands() *cobra.Command {
	cmd := &cobra.Command{Use: "services", Short: "Manage services", Aliases: []string{"svc"}}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List all services",
		RunE: func(cmd *cobra.Command, args []string) error {
			services, err := apiClient.ListServices(cmd.Context())
			if err != nil {
				return err
			}
			return output(services, []string{"ID", "NAME", "ARTIFACT_REPO", "RUNTIME"}, func(s domain.Service) []string {
				return []string{s.ID.String(), s.Name, s.ArtifactRepo, string(s.RuntimeType)}
			})
		},
	}

	getCmd := &cobra.Command{
		Use:   "get [id]",
		Short: "Get a service by ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := apiClient.GetService(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return outputSingle(svc)
		},
	}

	createCmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new service",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			name, _ := cmd.Flags().GetString("name")
			artifactRepo, _ := cmd.Flags().GetString("artifact-repo")
			runtimeType, _ := cmd.Flags().GetString("runtime-type")

			svc, err := apiClient.CreateService(cmd.Context(), name, artifactRepo, domain.RuntimeType(runtimeType))
			if err != nil {
				return err
			}
			return outputSingle(svc)
		},
	}
	createCmd.Flags().String("name", "", "Service name")
	createCmd.Flags().String("artifact-repo", "", "Artifact repository")
	createCmd.Flags().String("runtime-type", string(domain.RuntimeTypeCompose), "Runtime type: docker, compose, kubernetes")
	_ = createCmd.MarkFlagRequired("name")
	_ = createCmd.MarkFlagRequired("artifact-repo")

	actionsCmd := serviceActionsCommands()

	cmd.AddCommand(listCmd, getCmd, createCmd, actionsCmd)
	return cmd
}

func serviceActionsCommands() *cobra.Command {
	cmd := &cobra.Command{Use: "actions", Short: "Direct runtime lifecycle actions"}

	deployCmd := &cobra.Command{
		Use:   "deploy",
		Short: "Deploy a service directly through its resolved runtime",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			serviceID, _ := cmd.Flags().GetString("service")
			envID, _ := cmd.Flags().GetString("environment")
			artifactID, _ := cmd.Flags().GetString("artifact")
			var artifact *string
			if artifactID != "" {
				artifact = &artifactID
			}
			result, err := apiClient.DeployServiceRuntime(cmd.Context(), serviceID, envID, artifact)
			if err != nil {
				return err
			}
			return outputSingle(result)
		},
	}
	deployCmd.Flags().String("service", "", "Service ID")
	deployCmd.Flags().String("environment", "", "Environment ID")
	deployCmd.Flags().String("artifact", "", "Artifact ID (optional; defaults to desired artifact)")
	_ = deployCmd.MarkFlagRequired("service")
	_ = deployCmd.MarkFlagRequired("environment")

	restartCmd := &cobra.Command{
		Use:   "restart",
		Short: "Restart a service directly through its resolved runtime",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			serviceID, _ := cmd.Flags().GetString("service")
			envID, _ := cmd.Flags().GetString("environment")
			result, err := apiClient.RestartServiceRuntime(cmd.Context(), serviceID, envID)
			if err != nil {
				return err
			}
			return outputSingle(result)
		},
	}
	restartCmd.Flags().String("service", "", "Service ID")
	restartCmd.Flags().String("environment", "", "Environment ID")
	_ = restartCmd.MarkFlagRequired("service")
	_ = restartCmd.MarkFlagRequired("environment")

	stopCmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop a service directly through its resolved runtime",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			serviceID, _ := cmd.Flags().GetString("service")
			envID, _ := cmd.Flags().GetString("environment")
			result, err := apiClient.StopServiceRuntime(cmd.Context(), serviceID, envID)
			if err != nil {
				return err
			}
			return outputSingle(result)
		},
	}
	stopCmd.Flags().String("service", "", "Service ID")
	stopCmd.Flags().String("environment", "", "Environment ID")
	_ = stopCmd.MarkFlagRequired("service")
	_ = stopCmd.MarkFlagRequired("environment")

	cmd.AddCommand(deployCmd, restartCmd, stopCmd)
	return cmd
}

// --- Environments Commands ---

func environmentsCommands() *cobra.Command {
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
				prot := ""
				if e.Protected {
					prot = "yes"
				}
				return []string{e.ID.String(), e.Name, string(e.DeployStrategy), prot}
			})
		},
	}

	getCmd := &cobra.Command{
		Use:   "get [id]",
		Short: "Get an environment by ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			env, err := apiClient.GetEnvironment(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return outputSingle(env)
		},
	}

	createCmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new environment",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			name, _ := cmd.Flags().GetString("name")
			strategy, _ := cmd.Flags().GetString("strategy")
			protected, _ := cmd.Flags().GetBool("protected")

			env, err := apiClient.CreateEnvironment(cmd.Context(), name, domain.DeployStrategy(strategy), protected)
			if err != nil {
				return err
			}
			return outputSingle(env)
		},
	}
	createCmd.Flags().String("name", "", "Environment name")
	createCmd.Flags().String("strategy", string(domain.DeployStrategyReplace), "Deploy strategy: replace, blue_green, canary")
	createCmd.Flags().Bool("protected", false, "Require extra protections for deployments")
	_ = createCmd.MarkFlagRequired("name")

	cmd.AddCommand(listCmd, getCmd, createCmd)
	return cmd
}

// --- State Commands ---

func stateCommands() *cobra.Command {
	cmd := &cobra.Command{Use: "state", Short: "View deployment state"}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List all environment service states",
		RunE: func(cmd *cobra.Command, args []string) error {
			states, err := apiClient.ListStates(cmd.Context())
			if err != nil {
				return err
			}
			return output(states, []string{"SERVICE", "ENVIRONMENT", "DRIFT"}, func(s domain.EnvironmentServiceState) []string {
				return []string{s.ServiceID.String(), s.EnvironmentID.String(), string(s.DriftStatus)}
			})
		},
	}

	driftedCmd := &cobra.Command{
		Use:   "drifted",
		Short: "List drifted deployments",
		RunE: func(cmd *cobra.Command, args []string) error {
			states, err := apiClient.ListDriftedStates(cmd.Context())
			if err != nil {
				return err
			}
			if len(states) == 0 {
				fmt.Println("No drifted deployments found.")
				return nil
			}
			return output(states, []string{"SERVICE", "ENVIRONMENT"}, func(s domain.EnvironmentServiceState) []string {
				return []string{s.ServiceID.String(), s.EnvironmentID.String()}
			})
		},
	}

	cmd.AddCommand(listCmd, driftedCmd)
	return cmd
}

// --- Deploy Commands ---

func deployCommands() *cobra.Command {
	deployCmd := &cobra.Command{
		Use:   "deploy",
		Short: "Create a deployment intent",
		RunE: func(cmd *cobra.Command, args []string) error {
			serviceID, _ := cmd.Flags().GetString("service")
			envID, _ := cmd.Flags().GetString("environment")
			artifactID, _ := cmd.Flags().GetString("artifact")
			requestedBy, _ := cmd.Flags().GetString("requested-by")
			follow, _ := cmd.Flags().GetBool("follow")

			intent, err := apiClient.CreateDeploymentIntent(cmd.Context(), serviceID, envID, artifactID, requestedBy)
			if err != nil {
				return err
			}
			fmt.Printf("✓ Deployment intent created: %s (status: %s)\n", intent.ID, intent.Status)

			if follow {
				fmt.Println("\nFollowing deployment events (Ctrl+C to stop)...")
				return apiClient.StreamEvents(cmd.Context(), []string{"deployment.run.started", "deployment.run.completed"}, func(ev client.Event) {
					fmt.Printf("[%s] %s: %v\n", ev.Time, ev.Type, ev.Data)
				})
			}
			return nil
		},
	}
	deployCmd.Flags().String("service", "", "Service ID")
	deployCmd.Flags().String("environment", "", "Environment ID")
	deployCmd.Flags().String("artifact", "", "Artifact ID")
	deployCmd.Flags().String("requested-by", "", "Who requested the deployment")
	deployCmd.Flags().BoolP("follow", "f", false, "Follow deployment progress via SSE")
	_ = deployCmd.MarkFlagRequired("service")
	_ = deployCmd.MarkFlagRequired("environment")
	_ = deployCmd.MarkFlagRequired("artifact")

	rollbackCmd := &cobra.Command{
		Use:   "rollback",
		Short: "Roll back a deployment",
		RunE: func(cmd *cobra.Command, args []string) error {
			serviceID, _ := cmd.Flags().GetString("service")
			envID, _ := cmd.Flags().GetString("environment")
			requestedBy, _ := cmd.Flags().GetString("requested-by")

			intent, err := apiClient.Rollback(cmd.Context(), serviceID, envID, requestedBy)
			if err != nil {
				return err
			}
			fmt.Printf("✓ Rollback intent created: %s\n", intent.ID)
			return nil
		},
	}
	rollbackCmd.Flags().String("service", "", "Service ID")
	rollbackCmd.Flags().String("environment", "", "Environment ID")
	rollbackCmd.Flags().String("requested-by", "", "Who requested the rollback")
	_ = rollbackCmd.MarkFlagRequired("service")
	_ = rollbackCmd.MarkFlagRequired("environment")

	deploymentsCmd := &cobra.Command{
		Use:   "deployments",
		Short: "Deployment commands",
		Run: func(cmd *cobra.Command, args []string) {
			cmd.Help()
		},
	}
	deploymentsCmd.AddCommand(deployCmd, rollbackCmd)
	return deploymentsCmd
}

// --- Adoption Commands ---

func adoptCommands() *cobra.Command {
	cmd := &cobra.Command{Use: "adopt", Short: "Scan and import existing Docker workloads"}

	var scanTargets []string
	var scanRawTargets []string
	var scanEnvironments []string
	scanCmd := &cobra.Command{
		Use:   "scan",
		Short: "Preview adoptable containers from Docker targets",
		RunE: func(cmd *cobra.Command, args []string) error {
			targets, err := parseAdoptionTargets(scanTargets, scanRawTargets, scanEnvironments)
			if err != nil {
				return err
			}
			previews, err := apiClient.ScanAdoption(cmd.Context(), client.AdoptionScanRequest{Targets: targets})
			if err != nil {
				return err
			}
			if outputFormat != "table" {
				return outputJSONorYAML(previews)
			}
			rows := flattenAdoptionPreviewRows(previews)
			return output(rows, []string{"TARGET", "CONTAINER", "SERVICE", "IMAGE", "HEALTH", "ADOPTABLE", "WARNINGS"}, func(row adoptionPreviewRow) []string {
				return []string{row.Target, row.Container, row.Service, row.Image, row.Health, row.Adoptable, row.Warnings}
			})
		},
	}
	scanCmd.Flags().StringArrayVar(&scanTargets, "target", nil, "Server-managed endpoint target as endpointRef or alias=endpointRef (repeatable)")
	scanCmd.Flags().StringArrayVar(&scanRawTargets, "raw-target", nil, "Compatibility raw Docker target as alias=dockerHost (requires server allow_raw_docker_hosts)")
	scanCmd.Flags().StringArrayVar(&scanEnvironments, "environment", nil, "Environment name as alias=environmentName (repeatable)")

	var importTargets []string
	var importRawTargets []string
	var importEnvironments []string
	var importSelections []string
	var importAll bool
	importCmd := &cobra.Command{
		Use:   "import",
		Short: "Import selected or all discovered containers into Bahia",
		RunE: func(cmd *cobra.Command, args []string) error {
			targets, err := parseAdoptionTargets(importTargets, importRawTargets, importEnvironments)
			if err != nil {
				return err
			}
			selections, err := parseAdoptionSelections(importSelections)
			if err != nil {
				return err
			}
			if !importAll && len(selections) == 0 {
				return fmt.Errorf("specify --all or at least one --select alias/containerID")
			}
			results, err := apiClient.ImportAdoption(cmd.Context(), client.AdoptionImportRequest{Targets: targets, Selections: selections, ImportAll: importAll})
			if err != nil {
				return err
			}
			return output(results, []string{"TARGET", "CONTAINER", "SERVICE", "STATUS", "ERROR"}, func(row client.AdoptionImportResult) []string {
				return []string{row.TargetName, firstNonEmpty(row.ContainerName, row.ContainerID), row.ServiceName, row.Status, row.Error}
			})
		},
	}
	importCmd.Flags().StringArrayVar(&importTargets, "target", nil, "Server-managed endpoint target as endpointRef or alias=endpointRef (repeatable)")
	importCmd.Flags().StringArrayVar(&importRawTargets, "raw-target", nil, "Compatibility raw Docker target as alias=dockerHost (requires server allow_raw_docker_hosts)")
	importCmd.Flags().StringArrayVar(&importEnvironments, "environment", nil, "Environment name as alias=environmentName (repeatable)")
	importCmd.Flags().StringArrayVar(&importSelections, "select", nil, "Container selection as alias/containerID[=serviceName] (repeatable)")
	importCmd.Flags().BoolVar(&importAll, "all", false, "Import all adoptable containers from the scanned targets")

	cmd.AddCommand(scanCmd, importCmd)
	return cmd
}

type adoptionPreviewRow struct {
	Target    string
	Container string
	Service   string
	Image     string
	Health    string
	Adoptable string
	Warnings  string
}

func flattenAdoptionPreviewRows(previews []client.AdoptionPreview) []adoptionPreviewRow {
	var rows []adoptionPreviewRow
	for _, preview := range previews {
		if preview.Error != "" {
			rows = append(rows, adoptionPreviewRow{Target: preview.Target.Name, Health: "error", Adoptable: "no", Warnings: preview.Error})
			continue
		}
		for _, container := range preview.Containers {
			adoptable := "no"
			if container.Adoptable {
				adoptable = "yes"
			}
			rows = append(rows, adoptionPreviewRow{
				Target:    preview.Target.Name,
				Container: firstNonEmpty(container.Discovered.ContainerName, container.Discovered.ContainerID),
				Service:   container.ProposedServiceName,
				Image:     firstNonEmpty(container.Discovered.ImageRef, container.Discovered.ImageRepo),
				Health:    string(container.Discovered.HealthStatus),
				Adoptable: adoptable,
				Warnings:  strings.Join(container.Warnings, "; "),
			})
		}
	}
	return rows
}

func parseAdoptionTargets(targetFlags, rawTargetFlags, environmentFlags []string) ([]client.AdoptionTarget, error) {
	environments := map[string]string{}
	for _, raw := range environmentFlags {
		alias, envName, err := parseKeyValueFlag(raw, "environment")
		if err != nil {
			return nil, err
		}
		alias = normalizeAdoptionFlagName(alias)
		envName = normalizeAdoptionFlagName(envName)
		if alias == "" || envName == "" {
			return nil, fmt.Errorf("environment must contain a valid alias and environment name")
		}
		environments[alias] = envName
	}
	if len(targetFlags) == 0 && len(rawTargetFlags) == 0 {
		return nil, fmt.Errorf("at least one --target endpointRef or --raw-target alias=dockerHost is required")
	}
	targets := make([]client.AdoptionTarget, 0, len(targetFlags)+len(rawTargetFlags))
	seen := map[string]struct{}{}
	for _, raw := range targetFlags {
		aliasRaw := strings.TrimSpace(raw)
		endpointRef := aliasRaw
		if before, after, ok := strings.Cut(aliasRaw, "="); ok {
			aliasRaw = strings.TrimSpace(before)
			endpointRef = strings.TrimSpace(after)
		}
		alias := normalizeAdoptionFlagName(aliasRaw)
		if alias == "" || endpointRef == "" {
			return nil, fmt.Errorf("target must be endpointRef or alias=endpointRef")
		}
		if _, ok := seen[alias]; ok {
			return nil, fmt.Errorf("duplicate target alias %q", alias)
		}
		seen[alias] = struct{}{}
		targets = append(targets, client.AdoptionTarget{Name: alias, EndpointRef: endpointRef, EnvironmentName: environments[alias]})
	}
	for _, raw := range rawTargetFlags {
		alias, host, err := parseKeyValueFlag(raw, "raw-target")
		if err != nil {
			return nil, err
		}
		alias = normalizeAdoptionFlagName(alias)
		if alias == "" {
			return nil, fmt.Errorf("raw target alias is invalid")
		}
		if _, ok := seen[alias]; ok {
			return nil, fmt.Errorf("duplicate target alias %q", alias)
		}
		seen[alias] = struct{}{}
		targets = append(targets, client.AdoptionTarget{Name: alias, DockerHost: host, EnvironmentName: environments[alias]})
	}
	for alias := range environments {
		if _, ok := seen[alias]; !ok {
			return nil, fmt.Errorf("environment alias %q has no matching target", alias)
		}
	}
	return targets, nil
}

func parseAdoptionSelections(selectionFlags []string) ([]client.AdoptionSelection, error) {
	selections := make([]client.AdoptionSelection, 0, len(selectionFlags))
	seen := map[string]struct{}{}
	for _, raw := range selectionFlags {
		selection := strings.TrimSpace(raw)
		if selection == "" {
			return nil, fmt.Errorf("select value cannot be empty")
		}
		serviceName := ""
		if before, after, ok := strings.Cut(selection, "="); ok {
			selection = strings.TrimSpace(before)
			serviceName = normalizeAdoptionFlagName(after)
			if serviceName == "" {
				return nil, fmt.Errorf("select service name override cannot be empty or invalid")
			}
		}
		alias, containerID, ok := strings.Cut(selection, "/")
		alias = normalizeAdoptionFlagName(alias)
		containerID = strings.TrimSpace(containerID)
		if !ok || alias == "" || containerID == "" {
			return nil, fmt.Errorf("select must be alias/containerID or alias/containerID=serviceName")
		}
		key := alias + "/" + containerID
		if _, ok := seen[key]; ok {
			return nil, fmt.Errorf("duplicate selection %q", key)
		}
		seen[key] = struct{}{}
		selections = append(selections, client.AdoptionSelection{TargetName: alias, ContainerID: containerID, ServiceNameOverride: serviceName})
	}
	return selections, nil
}

var invalidAdoptionFlagNameChars = regexp.MustCompile(`[^a-z0-9-]+`)

func normalizeAdoptionFlagName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = invalidAdoptionFlagNameChars.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-")
	for strings.Contains(name, "--") {
		name = strings.ReplaceAll(name, "--", "-")
	}
	return name
}

func parseKeyValueFlag(raw, flagName string) (string, string, error) {
	key, value, ok := strings.Cut(strings.TrimSpace(raw), "=")
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if !ok || key == "" || value == "" {
		return "", "", fmt.Errorf("%s must be alias=value", flagName)
	}
	return key, value, nil
}

func outputJSONorYAML(v any) error {
	if outputFormat == "yaml" {
		return outputYAML(v)
	}
	return outputJSON(v)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// --- Workers Commands ---

func workersCommands() *cobra.Command {
	cmd := &cobra.Command{Use: "workers", Short: "Manage Loom workers"}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List discovered workers",
		RunE: func(cmd *cobra.Command, args []string) error {
			workers, err := apiClient.ListWorkers(cmd.Context())
			if err != nil {
				return err
			}
			return output(workers, []string{"PUBKEY", "NAME", "PRICE/SEC", "CAPABILITIES"}, func(w client.Worker) []string {
				caps := strings.Join(w.Capabilities, ", ")
				if len(caps) > 30 {
					caps = caps[:27] + "..."
				}
				return []string{truncate(w.Pubkey, 16), w.Name, fmt.Sprintf("%d sats", w.PricePerSec), caps}
			})
		},
	}

	showCmd := &cobra.Command{
		Use:   "show [pubkey]",
		Short: "Show worker details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			worker, err := apiClient.GetWorker(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return outputSingle(worker)
		},
	}

	cmd.AddCommand(listCmd, showCmd)
	return cmd
}

// --- Logs Commands ---

func logsCommands() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "View deployment and container logs",
	}

	runLogsCmd := &cobra.Command{
		Use:   "run [run-id]",
		Short: "Get logs for a completed deployment run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			tail, _ := cmd.Flags().GetInt("tail")
			stream, _ := cmd.Flags().GetString("stream")

			logs, err := apiClient.GetRunLogs(cmd.Context(), args[0], tail, stream)
			if err != nil {
				return err
			}

			if outputFormat != "table" {
				return outputSingle(logs)
			}

			if logs.Stdout != "" {
				fmt.Println("=== STDOUT ===")
				fmt.Println(logs.Stdout)
			}
			if logs.Stderr != "" {
				fmt.Println("=== STDERR ===")
				fmt.Println(logs.Stderr)
			}
			if logs.ExitCode != nil {
				fmt.Printf("\nExit code: %d\n", *logs.ExitCode)
			}
			if logs.Duration != "" {
				fmt.Printf("Duration: %s\n", logs.Duration)
			}
			return nil
		},
	}
	runLogsCmd.Flags().Int("tail", 0, "Number of lines from end (0 = all)")
	runLogsCmd.Flags().String("stream", "", "Stream filter: stdout, stderr, or merged")

	liveCmd := &cobra.Command{
		Use:   "live [service-id] [env-id]",
		Short: "Stream live container logs",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			tail, _ := cmd.Flags().GetInt("tail")

			fmt.Println("Streaming logs (Ctrl+C to stop)...")
			return apiClient.StreamLiveLogs(cmd.Context(), args[0], args[1], tail, func(line client.LogLine) {
				ts := line.Timestamp
				if len(ts) > 19 {
					ts = ts[:19]
				}
				stream := line.Stream
				if stream == "stderr" {
					stream = "ERR"
				} else {
					stream = "OUT"
				}
				fmt.Printf("[%s] [%s] %s\n", ts, stream, line.Message)
			})
		},
	}
	liveCmd.Flags().Int("tail", 100, "Number of historical lines")

	cmd.AddCommand(runLogsCmd, liveCmd)
	return cmd
}

// --- Policies Commands ---

func policiesCommands() *cobra.Command {
	cmd := &cobra.Command{Use: "policies", Short: "Manage deployment policies"}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List all policies",
		RunE: func(cmd *cobra.Command, args []string) error {
			policies, err := apiClient.ListPolicies(cmd.Context())
			if err != nil {
				return err
			}
			return output(policies, []string{"ID", "NAME", "ENFORCEMENT", "ENABLED"}, func(p domain.DeploymentPolicy) []string {
				enabled := "no"
				if p.Enabled {
					enabled = "yes"
				}
				return []string{p.ID.String(), p.Name, string(p.Enforcement), enabled}
			})
		},
	}

	getCmd := &cobra.Command{
		Use:   "get [id]",
		Short: "Get a policy by ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			policy, err := apiClient.GetPolicy(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return outputSingle(policy)
		},
	}

	createCmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new policy",
		RunE: func(cmd *cobra.Command, args []string) error {
			name, _ := cmd.Flags().GetString("name")
			envID, _ := cmd.Flags().GetString("environment")
			enforcement, _ := cmd.Flags().GetString("enforcement")
			rulesJSON, _ := cmd.Flags().GetString("rules")

			var rules map[string]any
			if rulesJSON != "" {
				if err := json.Unmarshal([]byte(rulesJSON), &rules); err != nil {
					return fmt.Errorf("invalid rules JSON: %w", err)
				}
			}

			policy, err := apiClient.CreatePolicy(cmd.Context(), name, envID, rules, enforcement, true)
			if err != nil {
				return err
			}
			fmt.Printf("✓ Policy created: %s\n", policy.ID)
			return nil
		},
	}
	createCmd.Flags().String("name", "", "Policy name")
	createCmd.Flags().String("environment", "", "Environment ID (optional)")
	createCmd.Flags().String("enforcement", "warn", "Enforcement: warn, block")
	createCmd.Flags().String("rules", "{}", "Rules as JSON")
	_ = createCmd.MarkFlagRequired("name")

	cmd.AddCommand(listCmd, getCmd, createCmd)
	return cmd
}

// --- Secrets Commands ---

func secretsCommands() *cobra.Command {
	cmd := &cobra.Command{Use: "secrets", Short: "Manage service secrets"}

	listCmd := &cobra.Command{
		Use:   "list [service-id]",
		Short: "List secrets for a service",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			secrets, err := apiClient.ListSecrets(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return output(secrets, []string{"ID", "NAME", "ENV", "ENCRYPTION", "VERSION"}, func(s client.SecretRef) []string {
				env := s.EnvironmentID
				if env == "" {
					env = "(all)"
				}
				return []string{s.ID, s.Name, env, s.EncryptionMethod, fmt.Sprintf("%d", s.Version)}
			})
		},
	}

	setCmd := &cobra.Command{
		Use:   "set [service-id] [name] [value]",
		Short: "Set a secret",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			envID, _ := cmd.Flags().GetString("environment")
			secret, err := apiClient.SetSecret(cmd.Context(), args[0], args[1], args[2], envID)
			if err != nil {
				return err
			}
			fmt.Printf("✓ Secret set: %s (version %d)\n", secret.Name, secret.Version)
			return nil
		},
	}
	setCmd.Flags().String("environment", "", "Environment ID (optional, for env-specific secret)")

	deleteCmd := &cobra.Command{
		Use:   "delete [service-id] [secret-id]",
		Short: "Delete a secret",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := apiClient.DeleteSecret(cmd.Context(), args[0], args[1]); err != nil {
				return err
			}
			fmt.Println("✓ Secret deleted")
			return nil
		},
	}

	cmd.AddCommand(listCmd, setCmd, deleteCmd)
	return cmd
}

// --- Orgs Commands ---

func orgsCommands() *cobra.Command {
	cmd := &cobra.Command{Use: "orgs", Short: "Manage organizations"}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List your organizations",
		RunE: func(cmd *cobra.Command, args []string) error {
			orgs, err := apiClient.ListOrgs(cmd.Context())
			if err != nil {
				return err
			}
			return output(orgs, []string{"ID", "NAME", "DISPLAY_NAME"}, func(o domain.Organization) []string {
				return []string{o.ID.String(), o.Name, o.DisplayName}
			})
		},
	}

	getCmd := &cobra.Command{
		Use:   "get [id-or-name]",
		Short: "Get an organization",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			org, err := apiClient.GetOrg(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return outputSingle(org)
		},
	}

	createCmd := &cobra.Command{
		Use:   "create [name]",
		Short: "Create an organization",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			displayName, _ := cmd.Flags().GetString("display-name")
			if displayName == "" {
				displayName = args[0]
			}
			org, err := apiClient.CreateOrg(cmd.Context(), args[0], displayName)
			if err != nil {
				return err
			}
			fmt.Printf("✓ Organization created: %s (%s)\n", org.Name, org.ID)
			return nil
		},
	}
	createCmd.Flags().String("display-name", "", "Display name")

	// Members subcommand
	membersCmd := &cobra.Command{Use: "members", Short: "Manage organization members"}

	membersListCmd := &cobra.Command{
		Use:   "list [org-id]",
		Short: "List organization members",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			members, err := apiClient.ListOrgMembers(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return output(members, []string{"PUBKEY", "ROLE", "NIP05", "JOINED"}, func(m domain.OrgMember) []string {
				return []string{truncate(m.Pubkey, 16), string(m.Role), m.NIP05, m.JoinedAt.Format(time.RFC3339)}
			})
		},
	}

	membersAddCmd := &cobra.Command{
		Use:   "add [org-id] [pubkey]",
		Short: "Add a member to an organization",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			role, _ := cmd.Flags().GetString("role")
			member, err := apiClient.AddOrgMember(cmd.Context(), args[0], args[1], domain.Role(role))
			if err != nil {
				return err
			}
			fmt.Printf("✓ Member added: %s (%s)\n", truncate(member.Pubkey, 16), member.Role)
			return nil
		},
	}
	membersAddCmd.Flags().String("role", "viewer", "Role: viewer, deployer, admin, owner")

	membersRemoveCmd := &cobra.Command{
		Use:   "remove [org-id] [pubkey]",
		Short: "Remove a member from an organization",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := apiClient.RemoveOrgMember(cmd.Context(), args[0], args[1]); err != nil {
				return err
			}
			fmt.Println("✓ Member removed")
			return nil
		},
	}

	membersCmd.AddCommand(membersListCmd, membersAddCmd, membersRemoveCmd)
	cmd.AddCommand(listCmd, getCmd, createCmd, membersCmd)
	return cmd
}

// --- Events Commands ---

func eventsCommands() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "events",
		Short: "Stream real-time events",
		RunE: func(cmd *cobra.Command, args []string) error {
			types, _ := cmd.Flags().GetStringSlice("types")
			fmt.Println("Streaming events (Ctrl+C to stop)...")
			return apiClient.StreamEvents(cmd.Context(), types, func(ev client.Event) {
				if outputFormat == "json" {
					b, _ := json.Marshal(ev)
					fmt.Println(string(b))
				} else {
					fmt.Printf("[%s] %s: %s %v\n", ev.Time, ev.Type, ev.EntityID, ev.Data)
				}
			})
		},
	}
	cmd.Flags().StringSlice("types", nil, "Event types to filter (comma-separated)")
	return cmd
}

// --- Output Helpers ---

func output[T any](items []T, headers []string, rowFn func(T) []string) error {
	switch outputFormat {
	case "json":
		return outputJSON(items)
	case "yaml":
		return outputYAML(items)
	default:
		return outputTable(items, headers, rowFn)
	}
}

func outputSingle(item any) error {
	switch outputFormat {
	case "json":
		return outputJSON(item)
	case "yaml":
		return outputYAML(item)
	default:
		return outputJSON(item) // Default to JSON for single items
	}
}

func outputTable[T any](items []T, headers []string, rowFn func(T) []string) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, strings.Join(headers, "\t"))
	for _, item := range items {
		fmt.Fprintln(w, strings.Join(rowFn(item), "\t"))
	}
	return w.Flush()
}

func outputJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func outputYAML(v any) error {
	enc := yaml.NewEncoder(os.Stdout)
	enc.SetIndent(2)
	return enc.Encode(v)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}

func getEnvOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// --- Auth Token Storage ---

func loadAuthToken() string {
	path := filepath.Join(configDir, "token")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func saveAuthToken(token string) error {
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return err
	}
	path := filepath.Join(configDir, "token")
	return os.WriteFile(path, []byte(token), 0600)
}

func clearAuthToken() error {
	path := filepath.Join(configDir, "token")
	return os.Remove(path)
}
