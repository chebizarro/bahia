package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/pkg/client"
	"github.com/spf13/cobra"
)

func buildsCommands() *cobra.Command {
	cmd := &cobra.Command{Use: "builds", Short: "Request and follow governed HiveCI builds"}

	requestCmd := &cobra.Command{
		Use:   "request",
		Short: "Request a governed HiveCI build through a signed intent",
		Long: "Requests a governed HiveCI build through the configured fleet Gitea mirror.\n\n" +
			"Build arguments are public in the signed CI request and require an approved service\n" +
			"allowlist. Services without one must omit --build-arg. Reusing --idempotency-key\n" +
			"replays the same signed intent without starting another build.\n\n" +
			"The server must enable hiveci.initiator. First-mirror builds can exceed the default\n" +
			"result timeout; use --result-timeout 120s when necessary. A successful request only\n" +
			"queues the build. The daemon registers verified HiveCI artifact results; follow\n" +
			"builds and artifacts get/list, then review a deployment preview before deployment.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			serviceID, _ := cmd.Flags().GetString("service")
			gitRef, _ := cmd.Flags().GetString("git-ref")
			credentialRef, _ := cmd.Flags().GetString("credential-ref")
			artifactRepo, _ := cmd.Flags().GetString("artifact-repo")
			buildArgValues, _ := cmd.Flags().GetStringArray("build-arg")
			idempotencyKey, _ := cmd.Flags().GetString("idempotency-key")
			buildArgs, err := parseBuildArgs(buildArgValues)
			if err != nil {
				return err
			}
			result, err := runBuildRequestIntent(cmd, client.BuildRequestNostrRequest{
				ServiceID: serviceID, GitRef: gitRef, RepositoryCredentialRef: credentialRef,
				ArtifactRepo: artifactRepo, BuildArgs: buildArgs, IdempotencyKey: idempotencyKey,
			})
			if err != nil {
				return err
			}
			return outputSingle(result)
		},
	}
	requestCmd.Flags().String("service", "", "Service ID")
	requestCmd.Flags().String("git-ref", "", "Git branch, tag, or 40-character commit SHA")
	requestCmd.Flags().String("credential-ref", "", "Opaque repository credential secret ID")
	requestCmd.Flags().String("artifact-repo", "", "Artifact repository; must match the registered service")
	requestCmd.Flags().StringArray("build-arg", nil, "Approved public build argument as KEY=VALUE (repeatable)")
	requestCmd.Flags().String("idempotency-key", "", "Explicit UUIDv7 intent ID for safe request replay")
	_ = requestCmd.MarkFlagRequired("service")
	_ = requestCmd.MarkFlagRequired("git-ref")
	_ = requestCmd.MarkFlagRequired("credential-ref")
	_ = requestCmd.MarkFlagRequired("artifact-repo")

	getCmd := &cobra.Command{
		Use:   "get",
		Short: "Get one governed build",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			buildID, _ := cmd.Flags().GetString("build")
			result, err := getCLIBuild(cmd, buildID)
			if err != nil {
				return err
			}
			return outputSingle(result)
		},
	}
	getCmd.Flags().String("build", "", "Build ID")
	_ = getCmd.MarkFlagRequired("build")

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List governed builds for a service",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			serviceID, _ := cmd.Flags().GetString("service")
			limit, _ := cmd.Flags().GetInt("limit")
			offset, _ := cmd.Flags().GetInt("offset")
			result, err := listCLIBuilds(cmd, serviceID, limit, offset)
			if err != nil {
				return err
			}
			return output(result.Builds, []string{"ID", "STATUS", "GIT SHA", "GIT REF", "CI RUN", "CREATED"}, func(build domain.Build) []string {
				created := ""
				if !build.CreatedAt.IsZero() {
					created = build.CreatedAt.UTC().Format(time.RFC3339)
				}
				return []string{build.ID.String(), string(build.Status), truncate(build.GitSHA, 12), build.GitRef, build.CIRunID, created}
			})
		},
	}
	listCmd.Flags().String("service", "", "Service ID")
	listCmd.Flags().Int("limit", 20, "Maximum builds to return (server cap 200)")
	listCmd.Flags().Int("offset", 0, "Number of builds to skip")
	_ = listCmd.MarkFlagRequired("service")

	cmd.AddCommand(requestCmd, getCmd, listCmd)
	return cmd
}

func parseBuildArgs(values []string) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	result := make(map[string]string, len(values))
	for _, value := range values {
		parts := strings.SplitN(value, "=", 2)
		if len(parts) != 2 || parts[0] == "" {
			return nil, fmt.Errorf("build argument %q must use KEY=VALUE", value)
		}
		if _, exists := result[parts[0]]; exists {
			return nil, fmt.Errorf("build argument %q is duplicated", parts[0])
		}
		result[parts[0]] = parts[1]
	}
	return result, nil
}
