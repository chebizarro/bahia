package main

import (
	"github.com/openagentsinc/bahia/pkg/client"
	"github.com/spf13/cobra"
)

func artifactsCommands() *cobra.Command {
	cmd := &cobra.Command{Use: "artifacts", Short: "Manage artifacts"}

	registerCmd := &cobra.Command{
		Use:   "register",
		Short: "Register an immutable artifact through signer-first Nostr control plane",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			buildID, _ := cmd.Flags().GetString("build")
			serviceID, _ := cmd.Flags().GetString("service")
			imageRepo, _ := cmd.Flags().GetString("image-repo")
			imageTag, _ := cmd.Flags().GetString("image-tag")
			imageDigest, _ := cmd.Flags().GetString("image-digest")
			manifestMediaType, _ := cmd.Flags().GetString("manifest-media-type")
			sbomURL, _ := cmd.Flags().GetString("sbom-url")
			signatureRef, _ := cmd.Flags().GetString("signature-ref")
			scanStatus, _ := cmd.Flags().GetString("scan-status")
			metadataFile, _ := cmd.Flags().GetString("metadata-file")
			idempotencyKey, _ := cmd.Flags().GetString("idempotency-key")
			metadata, err := readJSONObjectFile(metadataFile)
			if err != nil {
				return err
			}
			result, err := runArtifactRegisterNostr(cmd, client.RegisterArtifactNostrRequest{
				BuildID: buildID, ServiceID: serviceID, ImageRepo: imageRepo, ImageTag: imageTag,
				ImageDigest: imageDigest, ManifestMediaType: manifestMediaType, SBOMURL: sbomURL,
				SignatureRef: signatureRef, ScanStatus: scanStatus, Metadata: metadata, IdempotencyKey: idempotencyKey,
			})
			if err != nil {
				return err
			}
			return outputSingle(result)
		},
	}
	registerCmd.Flags().String("build", "", "Build ID")
	registerCmd.Flags().String("service", "", "Service ID")
	registerCmd.Flags().String("image-repo", "", "Image repository")
	registerCmd.Flags().String("image-tag", "", "Image tag")
	registerCmd.Flags().String("image-digest", "", "Immutable image digest")
	registerCmd.Flags().String("manifest-media-type", "", "Manifest media type")
	registerCmd.Flags().String("sbom-url", "", "SBOM URL")
	registerCmd.Flags().String("signature-ref", "", "Signature reference")
	registerCmd.Flags().String("scan-status", "unknown", "Scan status")
	registerCmd.Flags().String("metadata-file", "", "Read metadata JSON object from this file")
	registerCmd.Flags().String("idempotency-key", "", "Explicit idempotency key for the signed request")
	_ = registerCmd.MarkFlagRequired("build")
	_ = registerCmd.MarkFlagRequired("service")
	_ = registerCmd.MarkFlagRequired("image-repo")
	_ = registerCmd.MarkFlagRequired("image-tag")
	_ = registerCmd.MarkFlagRequired("image-digest")

	importCmd := &cobra.Command{
		Use:   "import-observed",
		Short: "Import an already-running, observation-verified image as governed artifact lineage",
		Long: "Records the image Bahia currently observes running for a service as a build/artifact\n" +
			"lineage, so live reality can be reconciled without ever editing the database.\n\n" +
			"The digest must match what Bahia observes running: the control plane, not the\n" +
			"operator, is the authority for what exists. Importing provenance does not deploy or\n" +
			"promote it — align desired state afterwards with a reviewed deployments preview and\n" +
			"deploy using the expected desired-state hash.\n\n" +
			"Prefer the Hive CI path (a signed 5402 carrying BAHIA_ARTIFACT) whenever the image\n" +
			"came from CI; this path is governed by hiveci.allow_live_artifact_import.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			serviceID, _ := cmd.Flags().GetString("service")
			environmentID, _ := cmd.Flags().GetString("environment")
			deploymentUnitID, _ := cmd.Flags().GetString("deployment-unit")
			imageRepo, _ := cmd.Flags().GetString("image-repo")
			imageTag, _ := cmd.Flags().GetString("image-tag")
			imageDigest, _ := cmd.Flags().GetString("image-digest")
			gitSHA, _ := cmd.Flags().GetString("git-sha")
			gitRef, _ := cmd.Flags().GetString("git-ref")
			idempotencyKey, _ := cmd.Flags().GetString("idempotency-key")
			result, err := runArtifactImportObservedNostr(cmd, client.ImportObservedArtifactNostrRequest{
				ServiceID: serviceID, EnvironmentID: environmentID, DeploymentUnitID: deploymentUnitID,
				ImageRepo: imageRepo, ImageTag: imageTag, ImageDigest: imageDigest,
				GitSHA: gitSHA, GitRef: gitRef, IdempotencyKey: idempotencyKey,
			})
			if err != nil {
				return err
			}
			return outputSingle(result)
		},
	}
	importCmd.Flags().String("service", "", "Service ID")
	importCmd.Flags().String("environment", "", "Environment ID")
	importCmd.Flags().String("deployment-unit", "", "Deployment unit ID (verified against the observation when supplied)")
	importCmd.Flags().String("image-repo", "", "Image repository of the running image")
	importCmd.Flags().String("image-tag", "", "Image tag of the running image")
	importCmd.Flags().String("image-digest", "", "Immutable digest of the running image; must match what Bahia observes")
	importCmd.Flags().String("git-sha", "", "Source commit (defaults to the observed image revision label when present)")
	importCmd.Flags().String("git-ref", "", "Source ref (defaults to the image tag)")
	importCmd.Flags().String("idempotency-key", "", "Explicit idempotency key for the signed request")
	_ = importCmd.MarkFlagRequired("service")
	_ = importCmd.MarkFlagRequired("environment")
	_ = importCmd.MarkFlagRequired("image-repo")
	_ = importCmd.MarkFlagRequired("image-tag")
	_ = importCmd.MarkFlagRequired("image-digest")

	cmd.AddCommand(registerCmd, importCmd)
	return cmd
}
