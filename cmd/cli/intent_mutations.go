package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/pkg/client"
	"github.com/spf13/cobra"
)

// IntentExitError carries the published intent outcome to the process entrypoint.
type IntentExitError struct {
	Code    int
	Message string
}

func (e *IntentExitError) Error() string { return e.Message }

var newCLIIntentPublisher = client.NewIntentPublisher
var readIntentCanonicalEvents = readNostrEvents

func buildCLIIntentPublisher(cmd *cobra.Command) (*client.IntentPublisher, []string, error) {
	key, err := resolveNostrPrivateKeyInput(cmd)
	if err != nil {
		return nil, nil, err
	}
	bunkerURI, clientKey, err := resolveNIP46OperatorInput(cmd)
	if err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(key) != "" && bunkerURI != "" {
		return nil, nil, fmt.Errorf("configure either a NIP-46 bunker signer or a local private key, not both")
	}
	if strings.TrimSpace(key) == "" && bunkerURI == "" {
		return nil, nil, fmt.Errorf("provide --nostr-key-file or --nostr-bunker-file with --nostr-client-key-file")
	}
	relays, err := resolveOperatorRelays(cmd)
	if err != nil {
		return nil, nil, err
	}
	servicePubkey := resolveOperatorServicePubkey(cmd)
	if servicePubkey == "" {
		return nil, nil, fmt.Errorf("--service-pubkey or BAHIA_NOSTR_SERVICE_PUBKEY is required for intent status")
	}
	timeout := operatorResultTimeout
	if !cmd.Root().PersistentFlags().Changed("result-timeout") {
		if raw := strings.TrimSpace(os.Getenv("BAHIA_RESULT_TIMEOUT")); raw != "" {
			timeout, err = time.ParseDuration(raw)
			if err != nil {
				return nil, nil, fmt.Errorf("invalid BAHIA_RESULT_TIMEOUT: %w", err)
			}
		}
	}
	if timeout <= 0 {
		return nil, nil, fmt.Errorf("--result-timeout must be positive")
	}
	cfg := client.IntentPublisherConfig{Relays: relays, PrivateKey: key, ServicePubkey: servicePubkey, ResultTimeout: timeout}
	if bunkerURI != "" {
		signer, pubkey, closeSigner, signerErr := newCLINIP46Signer(cmd.Context(), bunkerURI, clientKey)
		if signerErr != nil {
			return nil, nil, signerErr
		}
		cfg.PrivateKey, cfg.Signer, cfg.Pubkey, cfg.CloseSigner = "", signer, pubkey, closeSigner
	}
	publisher, err := newCLIIntentPublisher(cfg)
	if err != nil && cfg.CloseSigner != nil {
		_ = cfg.CloseSigner()
	}
	return publisher, relays, err
}

func intentUUID(raw string) (string, error) {
	if raw == "" {
		id, err := uuid.NewV7()
		if err != nil {
			return "", err
		}
		return id.String(), nil
	}
	id, err := uuid.Parse(raw)
	if err != nil || id.Version() != 7 || id.String() != raw {
		return "", fmt.Errorf("intent id must be a canonical UUIDv7")
	}
	return raw, nil
}

func requireIntentOrg(raw string) (string, error) {
	id, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil || id == uuid.Nil {
		return "", fmt.Errorf("--org must be a non-nil organization UUID for intents")
	}
	return id.String(), nil
}

func jsonObject(v any) (map[string]interface{}, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out map[string]interface{}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func publishCLIIntent(cmd *cobra.Command, request client.PublishIntentRequest) error {
	publisher, relays, err := buildCLIIntentPublisher(cmd)
	if err != nil {
		return err
	}
	defer publisher.Close()
	prepared, err := publisher.PrepareIntent(cmd.Context(), request)
	if err != nil {
		return err
	}
	outbox, err := localstore.OpenOutbox(cliOutboxDefaultPath())
	if err != nil {
		return fmt.Errorf("open CLI outbox: %w", err)
	}
	defer outbox.Close()
	if _, err := outbox.Enqueue(localstore.OutboxEntry{
		Event: prepared.Event, Target: strings.Join(relays, ","),
		EntityType: request.Domain, EntityID: request.Coordinate,
	}); err != nil {
		return fmt.Errorf("enqueue intent: %w", err)
	}
	fmt.Fprintln(cmd.ErrOrStderr(), "→ intent queued; publishing and waiting for status...")
	result, err := publisher.PublishAndWait(cmd.Context(), prepared)
	if err != nil {
		return fmt.Errorf("publish intent_id=%s event_id=%s: %w", prepared.IntentID, prepared.EventID, err)
	}
	relayStates := make(map[string]localstore.RelayDelivery, len(result.PublishResults))
	for _, r := range result.PublishResults {
		delivery := localstore.RelayDelivery{Accepted: r.Accepted, LastError: r.Error}
		if !r.Accepted && r.Error == "" {
			delivery.Rejected = r.Reason
		}
		relayStates[r.RelayURL] = delivery
	}
	state := localstore.OutboxPending
	if result.ExitCode == client.ExitCodeNoRelay {
		allRejected := len(result.PublishResults) > 0
		for _, relay := range result.PublishResults {
			if relay.Accepted || relay.Error != "" {
				allRejected = false
				break
			}
		}
		if allRejected {
			state = localstore.OutboxFailed
		}
	}
	if result.ExitCode == client.ExitCodeAccepted || result.ExitCode == client.ExitCodeRejected {
		state = localstore.OutboxPublished
	}
	if _, err := outbox.CommitRound(prepared.Event.ID, localstore.OutboxRound{
		Rounds: 1, Relays: relayStates, Delivered: result.ExitCode != client.ExitCodeNoRelay,
		State: state, Detail: result.Reason,
	}); err != nil {
		return fmt.Errorf("record intent delivery: %w", err)
	}
	if result.ExitCode != client.ExitCodeAccepted {
		cmd.SilenceUsage = true
	}
	switch result.ExitCode {
	case client.ExitCodeAccepted:
		return nil
	case client.ExitCodeTimeout:
		return &IntentExitError{Code: 2, Message: fmt.Sprintf("intent published but no status within timeout: intent_id=%s event_id=%s; inspect with bahia outbox list", result.IntentID, result.EventID)}
	case client.ExitCodeNoRelay:
		return &IntentExitError{Code: 3, Message: fmt.Sprintf("no relay accepted intent_id=%s event_id=%s: %s", result.IntentID, result.EventID, result.Reason)}
	default:
		return &IntentExitError{Code: 1, Message: fmt.Sprintf("intent %s: %s (intent_id=%s event_id=%s)", result.Status, result.Reason, result.IntentID, result.EventID)}
	}
}

func canonicalService(cmd *cobra.Command, id string) (*domain.Service, error) {
	events, err := readIntentCanonicalEvents(cmd, "service", kinds.ServiceRegistry)
	if err != nil {
		return nil, err
	}
	want, err := uuid.Parse(id)
	if err != nil {
		return nil, fmt.Errorf("invalid service id: %w", err)
	}
	var latest *domain.Service
	for _, event := range events {
		svc, err := client.DecodeService(event)
		if err != nil {
			return nil, err
		}
		if svc != nil && svc.ID == want && (latest == nil || svc.UpdatedAt.After(latest.UpdatedAt)) {
			latest = svc
		}
	}
	if latest != nil {
		return latest, nil
	}
	return nil, fmt.Errorf("canonical service %s not found", id)
}

func canonicalEnvironment(cmd *cobra.Command, id string) (*client.EnvironmentDetails, error) {
	events, err := readIntentCanonicalEvents(cmd, "environment", kinds.EnvironmentRegistry)
	if err != nil {
		return nil, err
	}
	want, err := uuid.Parse(id)
	if err != nil {
		return nil, fmt.Errorf("invalid environment id: %w", err)
	}
	var latest *client.EnvironmentDetails
	for _, event := range events {
		env, err := client.DecodeEnvironmentDetails(event)
		if err != nil {
			return nil, err
		}
		if env != nil && env.ID == want && (latest == nil || env.UpdatedAt.After(latest.UpdatedAt)) {
			latest = env
		}
	}
	if latest != nil {
		return latest, nil
	}
	return nil, fmt.Errorf("canonical environment %s not found", id)
}

func runServiceCreateIntent(cmd *cobra.Command, req client.CreateServiceNostrRequest) (*client.ServiceCommandResult, error) {
	org, err := requireIntentOrg(req.OrgID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.ArtifactRepo) == "" {
		return nil, fmt.Errorf("name and artifact_repo are required")
	}
	if req.ID == "" {
		req.ID, err = cliCreateEntityID(cmd, "service", "")
		if err != nil {
			return nil, err
		}
	}
	content, err := jsonObject(req)
	if err != nil {
		return nil, err
	}
	delete(content, "idempotency_key")
	if req.ManagedRuntimeConfig != nil {
		content["runtime_config"] = &domain.ServiceRuntimeConfig{Managed: req.ManagedRuntimeConfig}
		delete(content, "managed_runtime_config")
	}
	intentID, err := intentUUID(req.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	if err := publishCLIIntent(cmd, client.PublishIntentRequest{Domain: "service", Op: "create", Coordinate: req.ID, OrgID: org, IntentID: intentID, Content: content}); err != nil {
		return nil, err
	}
	svc, err := canonicalService(cmd, req.ID)
	if err != nil {
		return nil, err
	}
	return &client.ServiceCommandResult{Status: "created", ServiceID: req.ID, Service: svc}, nil
}

func runServiceUpdateIntent(cmd *cobra.Command, req client.UpdateServiceNostrRequest) (*client.ServiceCommandResult, error) {
	svc, err := canonicalService(cmd, req.ID)
	if err != nil {
		return nil, err
	}
	if svc.UpdatedAt.IsZero() {
		return nil, fmt.Errorf("canonical service %s is missing updated_at", req.ID)
	}
	if req.OrgID != nil {
		org, err := requireIntentOrg(*req.OrgID)
		if err != nil {
			return nil, err
		}
		if org != svc.OrgID.String() {
			return nil, fmt.Errorf("service organization cannot be changed by update")
		}
	}
	if req.Name != nil {
		svc.Name = *req.Name
	}
	if req.RepoURL != nil {
		svc.RepoURL = *req.RepoURL
		if svc.Repository != nil && !cmd.Flags().Changed("clone-url") {
			svc.Repository.CloneURL = *req.RepoURL
		}
	}
	if req.Repository != nil {
		if svc.Repository == nil {
			svc.Repository = &domain.RepositoryRef{}
		}
		if cmd.Flags().Changed("repo-source") {
			svc.Repository.Source = req.Repository.Source
		}
		if cmd.Flags().Changed("repo-coordinate") {
			svc.Repository.RepoCoordinate = req.Repository.RepoCoordinate
		}
		if cmd.Flags().Changed("clone-url") {
			svc.Repository.CloneURL = req.Repository.CloneURL
			svc.RepoURL = req.Repository.CloneURL
		}
		if cmd.Flags().Changed("web-url") {
			svc.Repository.WebURL = req.Repository.WebURL
		}
		if req.Repository.CI != nil {
			if svc.Repository.CI == nil {
				svc.Repository.CI = &domain.ServiceCIConfig{}
			}
			if cmd.Flags().Changed("ci-provider") {
				svc.Repository.CI.Provider = req.Repository.CI.Provider
			}
			if cmd.Flags().Changed("ci-workflow") {
				svc.Repository.CI.WorkflowPath = req.Repository.CI.WorkflowPath
			}
		}
	}
	if req.ArtifactRepo != nil {
		svc.ArtifactRepo = *req.ArtifactRepo
	}
	if req.DefaultBranch != nil {
		svc.DefaultBranch = *req.DefaultBranch
	}
	if req.RuntimeType != nil {
		svc.RuntimeType = domain.RuntimeType(*req.RuntimeType)
	}
	if req.ManagedRuntimeConfig != nil {
		svc.RuntimeConfig = &domain.ServiceRuntimeConfig{Managed: req.ManagedRuntimeConfig}
	}
	org, err := requireIntentOrg(svc.OrgID.String())
	if err != nil {
		return nil, err
	}
	content, err := jsonObject(svc)
	if err != nil {
		return nil, err
	}
	intentID, err := intentUUID(req.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	revision := svc.UpdatedAt
	if err := publishCLIIntent(cmd, client.PublishIntentRequest{Domain: "service", Op: "update", Coordinate: req.ID, OrgID: org, IntentID: intentID, Content: content, ExpectedUpdatedAt: &revision}); err != nil {
		return nil, err
	}
	updated, err := canonicalService(cmd, req.ID)
	if err != nil {
		return nil, err
	}
	return &client.ServiceCommandResult{Status: "updated", ServiceID: req.ID, Service: updated}, nil
}

func runEnvironmentCreateIntent(cmd *cobra.Command, req client.CreateEnvironmentNostrRequest) (*client.EnvironmentCommandResult, error) {
	org, err := requireIntentOrg(req.OrgID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Name) == "" {
		return nil, fmt.Errorf("name is required")
	}
	if req.ID == "" {
		req.ID, err = cliCreateEntityID(cmd, "environment", "")
		if err != nil {
			return nil, err
		}
	}
	content, err := jsonObject(req)
	if err != nil {
		return nil, err
	}
	intentID, err := intentUUID("")
	if err != nil {
		return nil, err
	}
	if err := publishCLIIntent(cmd, client.PublishIntentRequest{Domain: "environment", Op: "create", Coordinate: req.ID, OrgID: org, IntentID: intentID, Content: content}); err != nil {
		return nil, err
	}
	env, err := canonicalEnvironment(cmd, req.ID)
	if err != nil {
		return nil, err
	}
	return &client.EnvironmentCommandResult{Status: "created", EnvironmentID: req.ID, Environment: &env.Environment, DeploymentUnits: env.DeploymentUnits}, nil
}

func runEnvironmentUpdateIntent(cmd *cobra.Command, req client.UpdateEnvironmentNostrRequest) (*client.EnvironmentCommandResult, error) {
	current, err := canonicalEnvironment(cmd, req.ID)
	if err != nil {
		return nil, err
	}
	if current.UpdatedAt.IsZero() {
		return nil, fmt.Errorf("canonical environment %s is missing updated_at", req.ID)
	}
	if req.ExpectedUpdatedAt != nil && !domain.SameRevision(current.UpdatedAt, *req.ExpectedUpdatedAt) {
		return nil, &IntentExitError{Code: 1, Message: "environment revision conflict: re-read and retry"}
	}
	env := current.Environment
	if req.OrgID != nil {
		org, err := requireIntentOrg(*req.OrgID)
		if err != nil {
			return nil, err
		}
		if org != env.OrgID.String() {
			return nil, fmt.Errorf("environment organization cannot be changed by update")
		}
	}
	if req.Name != nil {
		env.Name = *req.Name
	}
	if req.LoomWorkerSelector != nil {
		env.LoomWorkerSelector = *req.LoomWorkerSelector
	}
	if req.RuntimeConfig != nil {
		env.RuntimeConfig = *req.RuntimeConfig
	}
	if req.Targeting != nil {
		env.Targeting = domain.EnvironmentTargeting{DefaultUnitKey: req.Targeting.DefaultUnitKey, FailureDomainLabels: req.Targeting.FailureDomainLabels, SecretScopeMode: domain.SecretScopeMode(req.Targeting.SecretScopeMode), DefaultReconcileMode: domain.ReconcileMode(req.Targeting.DefaultReconcileMode)}
	}
	if req.ReconcileMode != nil {
		env.Targeting.DefaultReconcileMode = domain.ReconcileMode(*req.ReconcileMode)
	}
	if req.DeployStrategy != nil {
		env.DeployStrategy = domain.DeployStrategy(*req.DeployStrategy)
	}
	if req.Protected != nil {
		env.Protected = *req.Protected
	}
	content, err := jsonObject(env)
	if err != nil {
		return nil, err
	}
	if req.DeploymentUnits != nil {
		content["deployment_units"] = *req.DeploymentUnits
	} else {
		units := explicitUnitRequests(current.DeploymentUnits)
		content["deployment_units"] = append([]client.DeploymentUnitRequest{}, units...)
	}
	org, err := requireIntentOrg(env.OrgID.String())
	if err != nil {
		return nil, err
	}
	intentID, err := intentUUID("")
	if err != nil {
		return nil, err
	}
	revision := current.UpdatedAt
	if err := publishCLIIntent(cmd, client.PublishIntentRequest{Domain: "environment", Op: "update", Coordinate: req.ID, OrgID: org, IntentID: intentID, Content: content, ExpectedUpdatedAt: &revision}); err != nil {
		return nil, err
	}
	updated, err := canonicalEnvironment(cmd, req.ID)
	if err != nil {
		return nil, err
	}
	return &client.EnvironmentCommandResult{Status: "updated", EnvironmentID: req.ID, Environment: &updated.Environment, DeploymentUnits: updated.DeploymentUnits}, nil
}
