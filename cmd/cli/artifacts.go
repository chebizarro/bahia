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

	cmd.AddCommand(registerCmd)
	return cmd
}
