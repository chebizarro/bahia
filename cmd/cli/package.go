package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/nbd-wtf/go-nostr"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/pkg/client"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

type cliPackageClient interface {
	Close()
	PublishRepositoryApply(context.Context, controlplane.PackageRepositoryApplyCommand) (*controlplane.PackageCommandReceipt, error)
	PublishRepositoryDelete(context.Context, controlplane.PackageRepositoryDeleteCommand) (*controlplane.PackageCommandReceipt, error)
	PublishPackageUpload(context.Context, controlplane.PackagePublishCommand) (*controlplane.PackageCommandReceipt, error)
	PublishPackagePromote(context.Context, controlplane.PackagePromotionCommand) (*controlplane.PackageCommandReceipt, error)
	PublishPackageYank(context.Context, controlplane.PackageYankCommand) (*controlplane.PackageCommandReceipt, error)
	PublishDriftDetect(context.Context, controlplane.PackageDriftDetectCommand) (*controlplane.PackageCommandReceipt, error)
	AwaitPackageResult(context.Context, *controlplane.PackageCommandReceipt, func(packageStatusEvent)) (*nostr.Event, error)
}

type packageCLIClient struct {
	pool      *nostrpool.RelayPool
	publisher *controlplane.PackageCommandPublisher
}

type packageStatusEvent struct {
	Kind    int                 `json:"kind"`
	EventID string              `json:"event_id"`
	Status  string              `json:"status,omitempty"`
	Step    string              `json:"step,omitempty"`
	Message string              `json:"message,omitempty"`
	Tags    map[string][]string `json:"tags,omitempty"`
}

var newCLIPackageClient = func(relays []string, privateKey string) (cliPackageClient, error) {
	normalized, err := client.NormalizeNostrPrivateKey(privateKey)
	if err != nil {
		return nil, err
	}
	signer, err := controlplane.NewPrivateKeySigner(normalized)
	if err != nil {
		return nil, err
	}
	pool := nostrpool.NewRelayPool(relays, zap.NewNop(), nostrpool.WithPrivateKey(normalized))
	pool.Connect(context.Background())
	return &packageCLIClient{pool: pool, publisher: controlplane.NewPackageCommandPublisher(pool, signer)}, nil
}

func (c *packageCLIClient) Close() {
	if c != nil && c.pool != nil {
		c.pool.Close()
	}
}

func (c *packageCLIClient) PublishRepositoryApply(ctx context.Context, req controlplane.PackageRepositoryApplyCommand) (*controlplane.PackageCommandReceipt, error) {
	return c.publisher.PublishPackageRepositoryApplyRequest(ctx, req)
}

func (c *packageCLIClient) PublishRepositoryDelete(ctx context.Context, req controlplane.PackageRepositoryDeleteCommand) (*controlplane.PackageCommandReceipt, error) {
	return c.publisher.PublishPackageRepositoryDeleteRequest(ctx, req)
}

func (c *packageCLIClient) PublishPackageUpload(ctx context.Context, req controlplane.PackagePublishCommand) (*controlplane.PackageCommandReceipt, error) {
	return c.publisher.PublishPackagePublishRequest(ctx, req)
}

func (c *packageCLIClient) PublishPackagePromote(ctx context.Context, req controlplane.PackagePromotionCommand) (*controlplane.PackageCommandReceipt, error) {
	return c.publisher.PublishPackagePromotionRequest(ctx, req)
}

func (c *packageCLIClient) PublishPackageYank(ctx context.Context, req controlplane.PackageYankCommand) (*controlplane.PackageCommandReceipt, error) {
	return c.publisher.PublishPackageYankRequest(ctx, req)
}

func (c *packageCLIClient) PublishDriftDetect(ctx context.Context, req controlplane.PackageDriftDetectCommand) (*controlplane.PackageCommandReceipt, error) {
	return c.publisher.PublishPackageDriftDetectRequest(ctx, req)
}

func (c *packageCLIClient) AwaitPackageResult(ctx context.Context, receipt *controlplane.PackageCommandReceipt, onStatus func(packageStatusEvent)) (*nostr.Event, error) {
	if c == nil || c.pool == nil || receipt == nil {
		return nil, fmt.Errorf("package client is not configured")
	}
	filters := []nostr.Filter{{
		Kinds: []int{receipt.StatusKind, receipt.ResultKind},
		Tags:  nostr.TagMap{"e": []string{receipt.RequestEventID}, "p": []string{receipt.RequestPubkey}},
	}}
	sub, err := c.pool.SubscribeAllWithEOSE(ctx, filters)
	if err != nil {
		return nil, err
	}
	defer sub.Close()
	seen := map[string]struct{}{}
	eose := sub.EndOfStoredEvents
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-eose:
			eose = nil
		case ev, ok := <-sub.Events:
			if !ok {
				return nil, fmt.Errorf("package result subscription closed before terminal result")
			}
			if ev == nil || !validPackageReply(ev, receipt) {
				continue
			}
			if _, duplicate := seen[ev.ID]; duplicate {
				continue
			}
			seen[ev.ID] = struct{}{}
			if ev.Kind == receipt.StatusKind {
				if onStatus != nil {
					onStatus(packageStatusFromEvent(ev))
				}
				continue
			}
			if ev.Kind == receipt.ResultKind {
				return ev, nil
			}
		}
	}
}

func packageCommands() *cobra.Command {
	cmd := &cobra.Command{Use: "package", Short: "Publish package control-plane commands"}
	repoCmd := &cobra.Command{Use: "repo", Short: "Manage package repositories"}
	repoCmd.AddCommand(packageRepoApplyCommand(), packageRepoDeleteCommand())
	cmd.AddCommand(repoCmd, packageUploadCommand(), packagePromoteCommand(), packageYankCommand(), packageDriftCommand())
	return cmd
}

func packageRepoApplyCommand() *cobra.Command {
	var repositoryID, name, format, backendRef, backendType, externalName, description, namespacePrefix, policyJSON, configJSON, metadataJSON string
	var wait bool
	cmd := &cobra.Command{Use: "apply", Short: "Create or update a package repository", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		policy, err := parsePackagePolicy(policyJSON)
		if err != nil {
			return err
		}
		metadata, err := parsePackageMetadata(metadataJSON, configJSON)
		if err != nil {
			return err
		}
		repoID, err := parseOptionalUUID(repositoryID, "repository-id")
		if err != nil {
			return err
		}
		req := controlplane.PackageRepositoryApplyCommand{RepositoryID: repoID, Name: name, Format: domain.PackageRepositoryFormat(format), BackendRef: backendRef, BackendType: domain.PackageBackendType(backendType), ExternalRepositoryName: firstNonEmpty(externalName, name), Description: description, NamespacePrefix: namespacePrefix, Policy: policy, Metadata: metadata}
		return runPackageCommand(cmd, wait, func(ctx context.Context, cli cliPackageClient) (*controlplane.PackageCommandReceipt, error) {
			return cli.PublishRepositoryApply(ctx, req)
		})
	}}
	cmd.Flags().StringVar(&repositoryID, "repository-id", "", "Repository UUID for updates")
	cmd.Flags().StringVar(&name, "name", "", "Repository name")
	cmd.Flags().StringVar(&format, "format", "", "Package format: npm, pypi, conan, deb, rpm, pub, go_modules, gradle")
	cmd.Flags().StringVar(&backendRef, "backend-ref", "", "Configured backend reference")
	cmd.Flags().StringVar(&backendType, "backend-type", "", "Backend type: nexus, pulp, filesystem_mock")
	cmd.Flags().StringVar(&externalName, "external-name", "", "External backend repository name (default: --name)")
	cmd.Flags().StringVar(&description, "description", "", "Repository description")
	cmd.Flags().StringVar(&namespacePrefix, "namespace-prefix", "", "Namespace prefix")
	cmd.Flags().StringVar(&policyJSON, "policy", "{}", "Repository policy as JSON")
	cmd.Flags().StringVar(&configJSON, "config", "{}", "Backend config as JSON, stored in metadata.config")
	cmd.Flags().StringVar(&metadataJSON, "metadata", "{}", "Additional metadata as JSON")
	cmd.Flags().BoolVar(&wait, "wait", false, "Subscribe for the correlated package result event")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("format")
	_ = cmd.MarkFlagRequired("backend-ref")
	_ = cmd.MarkFlagRequired("backend-type")
	return cmd
}

func packageRepoDeleteCommand() *cobra.Command {
	var repositoryID, name, reason string
	var force, wait bool
	cmd := &cobra.Command{Use: "delete", Short: "Delete a package repository", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		repoID, err := parseOptionalUUID(repositoryID, "repository-id")
		if err != nil {
			return err
		}
		if repoID == uuid.Nil && strings.TrimSpace(name) == "" {
			return fmt.Errorf("specify --repository-id or --name")
		}
		req := controlplane.PackageRepositoryDeleteCommand{RepositoryID: repoID, RepositoryName: name, Force: force, Reason: reason}
		return runPackageCommand(cmd, wait, func(ctx context.Context, cli cliPackageClient) (*controlplane.PackageCommandReceipt, error) {
			return cli.PublishRepositoryDelete(ctx, req)
		})
	}}
	cmd.Flags().StringVar(&repositoryID, "repository-id", "", "Repository UUID")
	cmd.Flags().StringVar(&name, "name", "", "Repository name")
	cmd.Flags().BoolVar(&force, "force", false, "Force deletion")
	cmd.Flags().StringVar(&reason, "reason", "", "Deletion reason")
	cmd.Flags().BoolVar(&wait, "wait", false, "Subscribe for the correlated package result event")
	return cmd
}

func packageUploadCommand() *cobra.Command {
	var repositoryID, repositoryName, namespace, packageName, version, filename, sourceURL, filePath, sha, contentType, approvedBy, policyRef, metadataJSON string
	var size int64
	var wait bool
	cmd := &cobra.Command{Use: "upload", Short: "Upload a package artifact", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		repoID, err := parseOptionalUUID(repositoryID, "repository-id")
		if err != nil {
			return err
		}
		if repoID == uuid.Nil && strings.TrimSpace(repositoryName) == "" {
			return fmt.Errorf("specify --repository-id or --repository")
		}
		if (sourceURL == "") == (filePath == "") {
			return fmt.Errorf("specify exactly one of --source-url or --file")
		}
		if filePath != "" {
			filename, sourceURL, sha, size, err = packageFileSource(filePath, filename, sha, size)
			if err != nil {
				return err
			}
		}
		metadata, err := parsePackageMetadata(metadataJSON, "")
		if err != nil {
			return err
		}
		req := controlplane.PackagePublishCommand{RepositoryID: repoID, RepositoryName: repositoryName, Namespace: namespace, PackageName: packageName, Version: version, Filename: filename, SourceURL: sourceURL, SHA256: sha, SizeBytes: size, ContentType: contentType, ApprovedBy: approvedBy, PolicyRef: policyRef, Metadata: metadata}
		return runPackageCommand(cmd, wait, func(ctx context.Context, cli cliPackageClient) (*controlplane.PackageCommandReceipt, error) {
			return cli.PublishPackageUpload(ctx, req)
		})
	}}
	addPackageArtifactFlags(cmd, &repositoryID, &repositoryName, &namespace, &packageName, &version, &filename)
	cmd.Flags().StringVar(&sourceURL, "source-url", "", "Artifact source URL")
	cmd.Flags().StringVar(&filePath, "file", "", "Local artifact file; converted to file:// source URL and hashed")
	cmd.Flags().StringVar(&sha, "sha256", "", "Artifact SHA-256 hex (computed for --file when omitted)")
	cmd.Flags().Int64Var(&size, "size", 0, "Artifact size in bytes (computed for --file when omitted)")
	cmd.Flags().StringVar(&contentType, "content-type", "", "Artifact content type")
	cmd.Flags().StringVar(&approvedBy, "approved-by", "", "Approver identity")
	cmd.Flags().StringVar(&policyRef, "policy-ref", "", "Policy reference")
	cmd.Flags().StringVar(&metadataJSON, "metadata", "{}", "Metadata as JSON")
	cmd.Flags().BoolVar(&wait, "wait", false, "Subscribe for the correlated package result event")
	_ = cmd.MarkFlagRequired("package")
	_ = cmd.MarkFlagRequired("version")
	return cmd
}

func packagePromoteCommand() *cobra.Command {
	var sourceID, sourceName, targetID, targetName, namespace, packageName, version, filename, environment, channel, approvedBy, policyRef, metadataJSON string
	var wait bool
	cmd := &cobra.Command{Use: "promote", Short: "Promote an artifact between repositories", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		sID, err := parseOptionalUUID(sourceID, "source-repository-id")
		if err != nil {
			return err
		}
		tID, err := parseOptionalUUID(targetID, "target-repository-id")
		if err != nil {
			return err
		}
		if sID == uuid.Nil && sourceName == "" {
			return fmt.Errorf("specify --source-repository-id or --source-repository")
		}
		if tID == uuid.Nil && targetName == "" {
			return fmt.Errorf("specify --target-repository-id or --target-repository")
		}
		metadata, err := parsePackageMetadata(metadataJSON, "")
		if err != nil {
			return err
		}
		req := controlplane.PackagePromotionCommand{SourceRepositoryID: sID, SourceRepositoryName: sourceName, TargetRepositoryID: tID, TargetRepositoryName: targetName, Namespace: namespace, PackageName: packageName, Version: version, Filename: filename, Environment: environment, Channel: channel, ApprovedBy: approvedBy, PolicyRef: policyRef, Metadata: metadata}
		return runPackageCommand(cmd, wait, func(ctx context.Context, cli cliPackageClient) (*controlplane.PackageCommandReceipt, error) {
			return cli.PublishPackagePromote(ctx, req)
		})
	}}
	cmd.Flags().StringVar(&sourceID, "source-repository-id", "", "Source repository UUID")
	cmd.Flags().StringVar(&sourceName, "source-repository", "", "Source repository name")
	cmd.Flags().StringVar(&targetID, "target-repository-id", "", "Target repository UUID")
	cmd.Flags().StringVar(&targetName, "target-repository", "", "Target repository name")
	addPackageIdentityFlags(cmd, &namespace, &packageName, &version, &filename)
	cmd.Flags().StringVar(&environment, "environment", "", "Target environment")
	cmd.Flags().StringVar(&channel, "channel", "", "Target channel")
	cmd.Flags().StringVar(&approvedBy, "approved-by", "", "Approver identity")
	cmd.Flags().StringVar(&policyRef, "policy-ref", "", "Policy reference")
	cmd.Flags().StringVar(&metadataJSON, "metadata", "{}", "Metadata as JSON")
	cmd.Flags().BoolVar(&wait, "wait", false, "Subscribe for the correlated package result event")
	_ = cmd.MarkFlagRequired("package")
	_ = cmd.MarkFlagRequired("version")
	_ = cmd.MarkFlagRequired("filename")
	return cmd
}

func packageYankCommand() *cobra.Command {
	var repositoryID, repositoryName, namespace, packageName, version, filename, reason, metadataJSON string
	var deprecated, wait bool
	cmd := &cobra.Command{Use: "yank", Short: "Yank or deprecate a package version", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		repoID, err := parseOptionalUUID(repositoryID, "repository-id")
		if err != nil {
			return err
		}
		if repoID == uuid.Nil && repositoryName == "" {
			return fmt.Errorf("specify --repository-id or --repository")
		}
		metadata, err := parsePackageMetadata(metadataJSON, "")
		if err != nil {
			return err
		}
		req := controlplane.PackageYankCommand{RepositoryID: repoID, RepositoryName: repositoryName, Namespace: namespace, PackageName: packageName, Version: version, Filename: filename, Reason: reason, Deprecated: deprecated, Metadata: metadata}
		return runPackageCommand(cmd, wait, func(ctx context.Context, cli cliPackageClient) (*controlplane.PackageCommandReceipt, error) {
			return cli.PublishPackageYank(ctx, req)
		})
	}}
	addPackageArtifactFlags(cmd, &repositoryID, &repositoryName, &namespace, &packageName, &version, &filename)
	cmd.Flags().StringVar(&reason, "reason", "", "Yank/deprecation reason")
	cmd.Flags().BoolVar(&deprecated, "deprecated", false, "Deprecate instead of yank")
	cmd.Flags().StringVar(&metadataJSON, "metadata", "{}", "Metadata as JSON")
	cmd.Flags().BoolVar(&wait, "wait", false, "Subscribe for the correlated package result event")
	_ = cmd.MarkFlagRequired("package")
	_ = cmd.MarkFlagRequired("version")
	_ = cmd.MarkFlagRequired("filename")
	return cmd
}

func packageDriftCommand() *cobra.Command {
	var repositoryID, repositoryName string
	var includeArtifacts, wait bool
	cmd := &cobra.Command{Use: "drift", Short: "Trigger package repository drift detection", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		repoID, err := parseOptionalUUID(repositoryID, "repository-id")
		if err != nil {
			return err
		}
		if repoID == uuid.Nil && repositoryName == "" {
			return fmt.Errorf("specify --repository-id or --repository")
		}
		req := controlplane.PackageDriftDetectCommand{RepositoryID: repoID, RepositoryName: repositoryName, IncludeArtifacts: includeArtifacts}
		return runPackageCommand(cmd, wait, func(ctx context.Context, cli cliPackageClient) (*controlplane.PackageCommandReceipt, error) {
			return cli.PublishDriftDetect(ctx, req)
		})
	}}
	cmd.Flags().StringVar(&repositoryID, "repository-id", "", "Repository UUID")
	cmd.Flags().StringVar(&repositoryName, "repository", "", "Repository name")
	cmd.Flags().BoolVar(&includeArtifacts, "include-artifacts", false, "Include artifacts in drift detection")
	cmd.Flags().BoolVar(&wait, "wait", false, "Subscribe for the correlated package result event")
	return cmd
}

func runPackageCommand(cmd *cobra.Command, wait bool, publish func(context.Context, cliPackageClient) (*controlplane.PackageCommandReceipt, error)) error {
	cli, err := buildCLIPackageClient(cmd)
	if err != nil {
		return err
	}
	defer cli.Close()
	receipt, err := publish(cmd.Context(), cli)
	if err != nil {
		return err
	}
	if !wait {
		return outputSingle(receipt)
	}
	result, err := cli.AwaitPackageResult(cmd.Context(), receipt, packageStatusCallback(cmd))
	if err != nil {
		return err
	}
	return outputSingle(map[string]any{"receipt": receipt, "result_event_id": result.ID, "result_kind": result.Kind, "result_tags": tagMapFromNostr(result.Tags), "result": decodeJSONContent(result.Content)})
}

func buildCLIPackageClient(cmd *cobra.Command) (cliPackageClient, error) {
	key, err := resolveNostrPrivateKeyInput(cmd)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(key) == "" {
		return nil, fmt.Errorf("provide --nsec, --privkey, BAHIA_NOSTR_NSEC, or BAHIA_NOSTR_PRIVATE_KEY for package commands")
	}
	relays, err := resolveOperatorRelays(cmd)
	if err != nil {
		return nil, err
	}
	return newCLIPackageClient(relays, key)
}

func addPackageArtifactFlags(cmd *cobra.Command, repositoryID, repositoryName, namespace, packageName, version, filename *string) {
	cmd.Flags().StringVar(repositoryID, "repository-id", "", "Repository UUID")
	cmd.Flags().StringVar(repositoryName, "repository", "", "Repository name")
	addPackageIdentityFlags(cmd, namespace, packageName, version, filename)
}

func addPackageIdentityFlags(cmd *cobra.Command, namespace, packageName, version, filename *string) {
	cmd.Flags().StringVar(namespace, "namespace", "", "Package namespace")
	cmd.Flags().StringVar(packageName, "package", "", "Package name")
	cmd.Flags().StringVar(version, "version", "", "Package version")
	cmd.Flags().StringVar(filename, "filename", "", "Artifact filename")
}

func parsePackagePolicy(raw string) (domain.PackageRepositoryPolicy, error) {
	var policy domain.PackageRepositoryPolicy
	if strings.TrimSpace(raw) == "" {
		return policy, nil
	}
	if err := json.Unmarshal([]byte(raw), &policy); err != nil {
		return policy, fmt.Errorf("invalid policy JSON: %w", err)
	}
	return policy, nil
}

func parsePackageMetadata(metadataRaw, configRaw string) (map[string]any, error) {
	metadata := map[string]any{}
	if strings.TrimSpace(metadataRaw) != "" {
		if err := json.Unmarshal([]byte(metadataRaw), &metadata); err != nil {
			return nil, fmt.Errorf("invalid metadata JSON: %w", err)
		}
	}
	if strings.TrimSpace(configRaw) != "" && strings.TrimSpace(configRaw) != "{}" {
		var config map[string]any
		if err := json.Unmarshal([]byte(configRaw), &config); err != nil {
			return nil, fmt.Errorf("invalid config JSON: %w", err)
		}
		metadata["config"] = config
	}
	if len(metadata) == 0 {
		return nil, nil
	}
	return metadata, nil
}

func parseOptionalUUID(raw, flagName string) (uuid.UUID, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return uuid.Nil, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid %s: %w", flagName, err)
	}
	return id, nil
}

func packageFileSource(path, filename, sha string, size int64) (string, string, string, int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", "", "", 0, fmt.Errorf("stat package file: %w", err)
	}
	if info.IsDir() {
		return "", "", "", 0, fmt.Errorf("package file cannot be a directory")
	}
	if filename == "" {
		filename = filepath.Base(path)
	}
	if size == 0 {
		size = info.Size()
	}
	if sha == "" {
		file, err := os.Open(path)
		if err != nil {
			return "", "", "", 0, fmt.Errorf("open package file: %w", err)
		}
		defer file.Close()
		h := sha256.New()
		if _, err := io.Copy(h, file); err != nil {
			return "", "", "", 0, fmt.Errorf("hash package file: %w", err)
		}
		sha = hex.EncodeToString(h.Sum(nil))
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", "", "", 0, fmt.Errorf("resolve package file path: %w", err)
	}
	return filename, (&url.URL{Scheme: "file", Path: abs}).String(), sha, size, nil
}

func packageStatusCallback(cmd *cobra.Command) func(packageStatusEvent) {
	if outputFormat != "table" {
		return nil
	}
	return func(status packageStatusEvent) {
		message := strings.TrimSpace(status.Message)
		if message == "" {
			message = firstNonEmpty(status.Step, status.Status)
		}
		if message == "" {
			message = "status update"
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "→ package: %s\n", message)
	}
}

func validPackageReply(event *nostr.Event, receipt *controlplane.PackageCommandReceipt) bool {
	if !event.CheckID() {
		return false
	}
	ok, err := event.CheckSignature()
	if err != nil || !ok {
		return false
	}
	return tagHasValueLocal(event.Tags, "e", receipt.RequestEventID) && tagHasValueLocal(event.Tags, "p", receipt.RequestPubkey)
}

func packageStatusFromEvent(event *nostr.Event) packageStatusEvent {
	tags := tagMapFromNostr(event.Tags)
	return packageStatusEvent{Kind: event.Kind, EventID: event.ID, Status: firstTagMapValue(tags, "status"), Step: firstTagMapValue(tags, "step"), Message: firstTagMapValue(tags, "message"), Tags: tags}
}

func tagMapFromNostr(tags nostr.Tags) map[string][]string {
	out := map[string][]string{}
	for _, tag := range tags {
		if len(tag) >= 2 {
			out[tag[0]] = append(out[tag[0]], tag[1])
		}
	}
	return out
}

func firstTagMapValue(tags map[string][]string, key string) string {
	if values := tags[key]; len(values) > 0 {
		return values[0]
	}
	return ""
}

func tagHasValueLocal(tags nostr.Tags, key, value string) bool {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == key && tag[1] == value {
			return true
		}
	}
	return false
}
