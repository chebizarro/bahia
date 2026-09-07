package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	canonicalnostr "fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	cascontextvm "git.sharegap.net/cascadia/cascadia-go/contextvm"
	"github.com/google/uuid"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

const (
	DefaultOperatorResultTimeout = 30 * time.Second
	DefaultOperatorResultRetries = 2
	operatorActivationTimeout    = 3 * time.Second
)

// OperatorControlPlaneConfig configures the signer-first operator Nostr client.
type OperatorControlPlaneConfig struct {
	Relays        []string
	PrivateKey    string // 64-character hex or nsec input
	Signer        canonicalnostr.Signer
	Pubkey        string
	CloseSigner   func() error
	ServicePubkey string // optional 64-character Bahia ContextVM service pubkey for #p/authors routing
	Encrypted     bool
	ResultTimeout time.Duration
	ResultRetries *int
}

// OperatorControlPlaneOption customizes optional client integrations.
type OperatorControlPlaneOption func(*operatorControlPlaneOptions)

type operatorControlPlaneOptions struct {
	logger *zap.Logger
}

// WithOperatorLogger surfaces relay warnings through the supplied logger.
// The library default remains quiet.
func WithOperatorLogger(logger *zap.Logger) OperatorControlPlaneOption {
	return func(options *operatorControlPlaneOptions) {
		if logger != nil {
			options.logger = logger
		}
	}
}

// OperatorStatusEvent is a correlated non-terminal operator progress event.
type OperatorStatusEvent struct {
	Kind      int
	EventID   string
	Status    string
	Step      string
	Action    string
	Operation string
	Message   string
	Tags      map[string][]string
}

// ControlPlaneRequestError describes whether a signer-first request was accepted
// by any relay before the error occurred. Callers may use RequestAccepted=false
// to decide whether an explicit compatibility fallback is safe.
type ControlPlaneRequestError struct {
	Phase               string
	RequestAccepted     bool
	PublishedRelays     int
	ConfiguredRelays    []string
	SubscribedRelays    []string
	FailedSubscriptions []string
	RequestEventID      string
	RequestDTag         string
	RequestMethod       string
	AttemptsMade        int
	PublishResults      []OperatorPublishResult
	Cause               error
}

func (e *ControlPlaneRequestError) Error() string {
	if e == nil {
		return ""
	}
	phase := strings.TrimSpace(e.Phase)
	if phase == "" {
		phase = "operator control-plane request"
	}
	if e.Cause == nil {
		return phase
	}
	message := fmt.Sprintf("%s: %v", phase, e.Cause)
	details := e.diagnosticDetails()
	if details == "" {
		return message
	}
	return message + " (" + details + ")"
}

func (e *ControlPlaneRequestError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *ControlPlaneRequestError) diagnosticDetails() string {
	if e == nil {
		return ""
	}
	parts := []string{}
	if e.RequestMethod != "" {
		parts = append(parts, "method="+e.RequestMethod)
	}
	if e.RequestEventID != "" {
		parts = append(parts, "request_event_id="+e.RequestEventID)
	}
	if e.RequestDTag != "" {
		parts = append(parts, "d="+e.RequestDTag)
	}
	if len(e.ConfiguredRelays) > 0 {
		parts = append(parts, "configured_relays="+strings.Join(e.ConfiguredRelays, ","))
	}
	if len(e.SubscribedRelays) > 0 {
		parts = append(parts, "subscribed_relays="+strings.Join(e.SubscribedRelays, ","))
	}
	if len(e.FailedSubscriptions) > 0 {
		parts = append(parts, "failed_subscriptions="+strings.Join(e.FailedSubscriptions, ","))
	}
	if e.AttemptsMade > 0 || e.PublishedRelays > 0 {
		parts = append(parts, fmt.Sprintf("published_relays=%d", e.PublishedRelays))
	}
	if e.AttemptsMade > 0 {
		parts = append(parts, fmt.Sprintf("attempts=%d", e.AttemptsMade))
	}
	if len(e.PublishResults) > 0 {
		parts = append(parts, "publish_results="+formatOperatorPublishResults(e.PublishResults))
	}
	return strings.Join(parts, " ")
}

// ErrEnvironmentRevisionConflict marks a retryable expected_updated_at mismatch.
var ErrEnvironmentRevisionConflict = errors.New("environment revision conflict")

// ContextVMRemoteError preserves the stable JSON-RPC error code returned by Bahia.
type ContextVMRemoteError struct {
	Code    int
	Message string
}

// OperatorPublishResult is a redacted per-relay publish outcome suitable for
// CLI diagnostics and task evidence.
type OperatorPublishResult struct {
	RelayURL  string
	Accepted  bool
	Duplicate bool
	Reason    string
	Error     string
}

func (e *ContextVMRemoteError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func (e *ContextVMRemoteError) Is(target error) bool {
	return target == ErrEnvironmentRevisionConflict && e != nil && e.Code == controlplane.ContextVMEnvironmentConflictErrorCode
}

// ContextVMPublishResult is one relay's publish acknowledgement.
type ContextVMPublishResult = nostrpool.PublishResult

// ContextVMRelayEOSE reports that one relay completed stored-event delivery.
type ContextVMRelayEOSE = nostrpool.RelayEOSE

// ContextVMRelayClosed reports that one relay closed a request subscription.
type ContextVMRelayClosed = nostrpool.RelayClosed

// ContextVMSubscription exposes the event and relay lifecycle channels required
// by ContextVMRelayTransport implementations.
type ContextVMSubscription struct {
	Events            <-chan *nostr.Event
	EndOfStoredEvents <-chan struct{}
	RelayEOSE         <-chan ContextVMRelayEOSE
	Closed            <-chan ContextVMRelayClosed
	relayURLs         []string
	closeFn           func()
}

// NewContextVMSubscription adapts an existing relay subscription for use by a
// ContextVMRequestClient.
func NewContextVMSubscription(events <-chan *nostr.Event, endOfStoredEvents <-chan struct{}, relayEOSE <-chan ContextVMRelayEOSE, closed <-chan ContextVMRelayClosed, relayURLs []string, closeFn func()) *ContextVMSubscription {
	return &ContextVMSubscription{
		Events:            events,
		EndOfStoredEvents: endOfStoredEvents,
		RelayEOSE:         relayEOSE,
		Closed:            closed,
		relayURLs:         append([]string(nil), relayURLs...),
		closeFn:           closeFn,
	}
}

type operatorSubscription = ContextVMSubscription

func (s *ContextVMSubscription) RelayURLs() []string {
	if s == nil {
		return nil
	}
	return append([]string(nil), s.relayURLs...)
}

func (s *ContextVMSubscription) Close() {
	if s != nil && s.closeFn != nil {
		s.closeFn()
	}
}

// ContextVMRelayTransport is the relay surface used by ContextVMRequestClient.
// Injected transports remain owned by the caller and are not closed by the client.
type ContextVMRelayTransport interface {
	Publish(context.Context, nostr.Event) (int, error)
	PublishWithResults(context.Context, nostr.Event) ([]ContextVMPublishResult, error)
	SubscribeOperator(context.Context, []nostr.Filter) (*ContextVMSubscription, error)
	AuthenticateRelay(context.Context, string) error
	Close()
}

type operatorRelayTransport = ContextVMRelayTransport

type relayPoolOperatorTransport struct {
	pool      *nostrpool.RelayPool
	mu        sync.Mutex
	connected bool
}

func (t *relayPoolOperatorTransport) ensureConnected(ctx context.Context) {
	if t == nil || t.pool == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.connected {
		return
	}
	t.pool.Connect(ctx)
	if ctx.Err() == nil {
		t.connected = true
	}
}

func (t *relayPoolOperatorTransport) Publish(ctx context.Context, ev nostr.Event) (int, error) {
	t.ensureConnected(ctx)
	return t.pool.Publish(ctx, ev)
}

func (t *relayPoolOperatorTransport) PublishWithResults(ctx context.Context, ev nostr.Event) ([]nostrpool.PublishResult, error) {
	t.ensureConnected(ctx)
	return t.pool.PublishWithResults(ctx, ev)
}

func (t *relayPoolOperatorTransport) SubscribeAllWithEOSE(ctx context.Context, filters []nostr.Filter) (*nostrpool.MergedSubscription, error) {
	t.ensureConnected(ctx)
	return t.pool.SubscribeAllWithEOSE(ctx, filters)
}

func (t *relayPoolOperatorTransport) SubscribeOperator(ctx context.Context, filters []nostr.Filter) (*operatorSubscription, error) {
	merged, err := t.SubscribeAllWithEOSE(ctx, filters)
	if err != nil {
		return nil, err
	}
	return &operatorSubscription{
		Events:            merged.Events,
		EndOfStoredEvents: merged.EndOfStoredEvents,
		RelayEOSE:         merged.RelayEOSE,
		Closed:            merged.Closed,
		relayURLs:         merged.RelayURLs(),
		closeFn:           merged.Close,
	}, nil
}

func (t *relayPoolOperatorTransport) AuthenticateRelay(ctx context.Context, relayURL string) error {
	t.ensureConnected(ctx)
	return t.pool.AuthenticateRelay(ctx, relayURL)
}

func (t *relayPoolOperatorTransport) Close() {
	if t != nil && t.pool != nil {
		t.pool.Close()
	}
}

// OperatorControlPlaneClient publishes signed ContextVM JSON-RPC operator
// requests and waits for correlated ContextVM replies over Nostr subscriptions.
type OperatorControlPlaneClient struct {
	relays            []string
	privateKey        string
	signer            canonicalnostr.Signer
	cipher            contextVMCipherSigner
	pubkey            string
	transport         operatorRelayTransport
	servicePubkey     string
	closeSigner       func() error
	encrypted         bool
	resultTimeout     time.Duration
	resultRetries     int
	activationTimeout time.Duration
}

// NewOperatorControlPlaneClient builds a signer-first operator control-plane client.
func NewOperatorControlPlaneClient(cfg OperatorControlPlaneConfig, clientOptions ...OperatorControlPlaneOption) (*OperatorControlPlaneClient, error) {
	options := operatorControlPlaneOptions{logger: zap.NewNop()}
	for _, apply := range clientOptions {
		if apply != nil {
			apply(&options)
		}
	}
	privateKey := strings.TrimSpace(cfg.PrivateKey)
	signer := cfg.Signer
	var cipherSigner contextVMCipherSigner
	pubkey := strings.TrimSpace(cfg.Pubkey)
	var poolOptions []nostrpool.RelayPoolOption
	if signer != nil {
		if privateKey != "" {
			return nil, fmt.Errorf("configure either an operator signer or a private key, not both")
		}
		if len(pubkey) != 64 {
			return nil, fmt.Errorf("operator signer pubkey must be a 64-character hex pubkey")
		}
		if cfg.Encrypted {
			var ok bool
			cipherSigner, ok = signer.(contextVMCipherSigner)
			if !ok {
				return nil, fmt.Errorf("encrypted operator requests require a signer with NIP-44 encrypt and decrypt support")
			}
		}
		poolOptions = append(poolOptions, nostrpool.WithAuthSigner(signer))
	} else {
		var err error
		privateKey, err = NormalizeNostrPrivateKey(privateKey)
		if err != nil {
			return nil, err
		}
		secret, err := nostr.SecretKeyFromHex(privateKey)
		if err != nil {
			return nil, fmt.Errorf("parse Nostr private key: %w", err)
		}
		localKeyer := keyer.NewPlainKeySigner(secret)
		signer = localKeyer
		cipherSigner = localKeyer
		pubkey = secret.Public().Hex()
		poolOptions = append(poolOptions, nostrpool.WithPrivateKey(privateKey))
	}
	relays := normalizeOperatorRelays(cfg.Relays)
	if len(relays) == 0 {
		return nil, fmt.Errorf("at least one operator relay is required")
	}
	pool := nostrpool.NewRelayPool(relays, options.logger, poolOptions...)
	servicePubkey := strings.TrimSpace(cfg.ServicePubkey)
	if servicePubkey != "" && len(servicePubkey) != 64 {
		return nil, fmt.Errorf("service pubkey must be a 64-character hex pubkey")
	}
	if cfg.Encrypted && servicePubkey == "" {
		return nil, fmt.Errorf("encrypted operator requests require ServicePubkey")
	}
	resultTimeout := cfg.ResultTimeout
	if resultTimeout == 0 {
		resultTimeout = DefaultOperatorResultTimeout
	}
	if resultTimeout < 0 {
		return nil, fmt.Errorf("operator result timeout must be positive")
	}
	resultRetries := DefaultOperatorResultRetries
	if cfg.ResultRetries != nil {
		resultRetries = *cfg.ResultRetries
	}
	if resultRetries < 0 {
		return nil, fmt.Errorf("operator result retries cannot be negative")
	}
	return &OperatorControlPlaneClient{
		relays:            relays,
		privateKey:        privateKey,
		signer:            signer,
		cipher:            cipherSigner,
		pubkey:            pubkey,
		transport:         &relayPoolOperatorTransport{pool: pool},
		servicePubkey:     servicePubkey,
		closeSigner:       cfg.CloseSigner,
		encrypted:         cfg.Encrypted,
		resultTimeout:     resultTimeout,
		resultRetries:     resultRetries,
		activationTimeout: operatorActivationTimeout,
	}, nil
}

// Close releases relay resources owned by the client.
func (c *OperatorControlPlaneClient) Close() {
	if c != nil && c.transport != nil {
		c.transport.Close()
	}
	if c != nil && c.closeSigner != nil {
		_ = c.closeSigner()
	}
}

// EnvironmentTargetingRequest configures environment-level placement defaults.
type EnvironmentTargetingRequest struct {
	DefaultUnitKey       string            `json:"default_unit_key,omitempty"`
	FailureDomainLabels  map[string]string `json:"failure_domain_labels,omitempty"`
	SecretScopeMode      string            `json:"secret_scope_mode,omitempty"`
	DefaultReconcileMode string            `json:"default_reconcile_mode,omitempty"`
}

// DeploymentUnitRequest is one desired explicit environment deployment unit.
type DeploymentUnitRequest struct {
	Key            string            `json:"key"`
	DisplayName    string            `json:"display_name,omitempty"`
	RuntimeType    string            `json:"runtime_type,omitempty"`
	EndpointRef    string            `json:"endpoint_ref,omitempty"`
	ComposeDir     string            `json:"compose_dir,omitempty"`
	Namespace      string            `json:"namespace,omitempty"`
	NetworkProfile map[string]string `json:"network_profile,omitempty"`
	GitSource      *GitSourceRequest `json:"git_source,omitempty"`
	OwnershipMode  string            `json:"ownership_mode,omitempty"`
	ReconcileMode  string            `json:"reconcile_mode,omitempty"`
	RuntimeConfig  map[string]any    `json:"runtime_config,omitempty"`
}

// GitSourceRequest identifies the git checkout backing a deployment unit.
type GitSourceRequest struct {
	RepositoryURL string `json:"repository_url,omitempty"`
	Ref           string `json:"ref,omitempty"`
	Branch        string `json:"branch,omitempty"`
	CommitSHA     string `json:"commit_sha,omitempty"`
}

// RepositoryRefRequest is signer-first structured source repository metadata.
type RepositoryRefRequest struct {
	Source         string                  `json:"source,omitempty"`
	RepoCoordinate string                  `json:"repo_coordinate,omitempty"`
	CloneURL       string                  `json:"clone_url,omitempty"`
	WebURL         string                  `json:"web_url,omitempty"`
	RelayURLs      []string                `json:"relay_urls,omitempty"`
	CI             *ServiceCIConfigRequest `json:"ci,omitempty"`
}

// ServiceCIConfigRequest describes the build workflow attached to a service repository.
type ServiceCIConfigRequest struct {
	Provider     string `json:"provider,omitempty"`
	WorkflowPath string `json:"workflow_path,omitempty"`
}

// CreateServiceNostrRequest is the signer-first service/create payload.
type CreateServiceNostrRequest struct {
	OrgID                string                       `json:"org_id,omitempty"`
	Name                 string                       `json:"name"`
	RepoURL              string                       `json:"repo_url,omitempty"`
	Repository           *RepositoryRefRequest        `json:"repository,omitempty"`
	ArtifactRepo         string                       `json:"artifact_repo"`
	DefaultBranch        string                       `json:"default_branch,omitempty"`
	RuntimeType          string                       `json:"runtime_type,omitempty"`
	ManagedRuntimeConfig *domain.ManagedRuntimeConfig `json:"managed_runtime_config,omitempty"`
	IdempotencyKey       string                       `json:"idempotency_key,omitempty"`
}

// UpdateServiceNostrRequest is the signer-first service/update payload.
type UpdateServiceNostrRequest struct {
	ID                       string                       `json:"id"`
	OrgID                    *string                      `json:"org_id,omitempty"`
	Name                     *string                      `json:"name,omitempty"`
	RepoURL                  *string                      `json:"repo_url,omitempty"`
	Repository               *RepositoryRefRequest        `json:"repository,omitempty"`
	ArtifactRepo             *string                      `json:"artifact_repo,omitempty"`
	DefaultBranch            *string                      `json:"default_branch,omitempty"`
	RuntimeType              *string                      `json:"runtime_type,omitempty"`
	ManagedRuntimeConfig     *domain.ManagedRuntimeConfig `json:"managed_runtime_config,omitempty"`
	AdoptedPublicEnvironment map[string]string            `json:"adopted_public_environment,omitempty"`
	IdempotencyKey           string                       `json:"idempotency_key,omitempty"`
}

// ServiceCommandResult is the terminal acknowledgment for signer-first service mutations.
type ServiceCommandResult struct {
	Status         string          `json:"status,omitempty"`
	Service        *domain.Service `json:"service,omitempty"`
	ServiceID      string          `json:"service_id,omitempty"`
	IdempotencyKey string          `json:"idempotency_key,omitempty"`
	Message        string          `json:"message,omitempty"`
}

// RegisterArtifactNostrRequest is the signer-first artifact/register payload.
type RegisterArtifactNostrRequest struct {
	BuildID           string         `json:"build_id"`
	ServiceID         string         `json:"service_id"`
	ImageRepo         string         `json:"image_repo"`
	ImageTag          string         `json:"image_tag"`
	ImageDigest       string         `json:"image_digest"`
	ManifestMediaType string         `json:"manifest_media_type,omitempty"`
	SizeBytes         *int64         `json:"size_bytes,omitempty"`
	SBOMURL           string         `json:"sbom_url,omitempty"`
	SignatureRef      string         `json:"signature_ref,omitempty"`
	ScanStatus        string         `json:"scan_status,omitempty"`
	Metadata          map[string]any `json:"metadata,omitempty"`
	IdempotencyKey    string         `json:"idempotency_key,omitempty"`
}

// ImportObservedArtifactNostrRequest imports an already-running,
// observation-verified image as governed lineage. There is no build id: Bahia
// creates the lineage, which is the reason this path exists.
type ImportObservedArtifactNostrRequest struct {
	ServiceID        string `json:"service_id"`
	EnvironmentID    string `json:"environment_id"`
	DeploymentUnitID string `json:"deployment_unit_id,omitempty"`
	ImageRepo        string `json:"image_repo"`
	ImageTag         string `json:"image_tag"`
	ImageDigest      string `json:"image_digest"`
	GitSHA           string `json:"git_sha,omitempty"`
	GitRef           string `json:"git_ref,omitempty"`
	IdempotencyKey   string `json:"idempotency_key,omitempty"`
}

// ImportObservedArtifactResult reports imported lineage. DesiredState restates
// that importing provenance never promotes it.
type ImportObservedArtifactResult struct {
	Status           string           `json:"status,omitempty"`
	Artifact         *domain.Artifact `json:"artifact,omitempty"`
	ArtifactID       string           `json:"artifact_id,omitempty"`
	BuildID          string           `json:"build_id,omitempty"`
	ObservedDigest   string           `json:"observed_digest,omitempty"`
	ObservationID    string           `json:"observation_id,omitempty"`
	RegistryVerified bool             `json:"registry_verified,omitempty"`
	VerifiedLabels   []string         `json:"verified_labels,omitempty"`
	DesiredState     string           `json:"desired_state,omitempty"`
}

// ArtifactCommandResult is the terminal acknowledgment for signer-first artifact registration.
type ArtifactCommandResult struct {
	Status     string           `json:"status,omitempty"`
	Artifact   *domain.Artifact `json:"artifact,omitempty"`
	ArtifactID string           `json:"artifact_id,omitempty"`
	BuildID    string           `json:"build_id,omitempty"`
	ServiceID  string           `json:"service_id,omitempty"`
	Message    string           `json:"message,omitempty"`
}

// DNSZoneCreateRequest is the signer-first dns/zone-create payload.
type DNSZoneCreateRequest struct {
	Name          string                `json:"name"`
	Visibility    domain.ZoneVisibility `json:"visibility"`
	BackendRef    string                `json:"backend_ref"`
	TTL           int                   `json:"ttl"`
	Authoritative bool                  `json:"authoritative"`
}

// DNSPolicyApplyRequest is the signer-first dns/policy-apply payload.
type DNSPolicyApplyRequest struct {
	ID            uuid.UUID              `json:"id"`
	Name          string                 `json:"name"`
	ZoneID        *uuid.UUID             `json:"zone_id,omitempty"`
	EnvironmentID *uuid.UUID             `json:"environment_id,omitempty"`
	Rules         []domain.DNSPolicyRule `json:"rules"`
	Enabled       bool                   `json:"enabled"`
	Metadata      map[string]any         `json:"metadata,omitempty"`
	CreatedAt     time.Time              `json:"created_at"`
	UpdatedAt     time.Time              `json:"updated_at"`
}

// DNSRecordSetRequest is the signer-first dns/record-set payload. Operator
// attribution and creation metadata are derived by the server from the signed event.
type DNSRecordSetRequest struct {
	ZoneName   string               `json:"zone_name"`
	RecordName string               `json:"record_name"`
	RecordType domain.DNSRecordType `json:"record_type"`
	Value      string               `json:"value"`
	TTL        int                  `json:"ttl"`
	Reason     string               `json:"reason"`
	ExpiresAt  *time.Time           `json:"expires_at,omitempty"`
}

// DNSDriftRemediateRequest is the signer-first dns/drift-remediate payload.
// An empty zone requests reconciliation of all configured zones.
type DNSDriftRemediateRequest struct {
	Zone string `json:"zone,omitempty"`
}

// DNSCommandResult is the terminal acknowledgment for signer-first DNS mutations.
type DNSCommandResult struct {
	Action     string `json:"action,omitempty"`
	Status     string `json:"status,omitempty"`
	Step       string `json:"step,omitempty"`
	Message    string `json:"message,omitempty"`
	RecordedAt string `json:"recorded_at,omitempty"`
	Zone       string `json:"zone,omitempty"`
	Policy     string `json:"policy,omitempty"`
	PolicyID   string `json:"policy_id,omitempty"`
	RuleCount  int    `json:"rule_count,omitempty"`
	OverrideID string `json:"override_id,omitempty"`
}

// CreateEnvironmentNostrRequest is the signer-first environment/create payload.
type CreateEnvironmentNostrRequest struct {
	OrgID              string                       `json:"org_id,omitempty"`
	Name               string                       `json:"name"`
	LoomWorkerSelector map[string]any               `json:"loom_worker_selector,omitempty"`
	RuntimeConfig      map[string]any               `json:"runtime_config,omitempty"`
	Targeting          *EnvironmentTargetingRequest `json:"targeting,omitempty"`
	ReconcileMode      string                       `json:"reconcile_mode,omitempty"`
	DeploymentUnits    *[]DeploymentUnitRequest     `json:"deployment_units,omitempty"`
	DeployStrategy     string                       `json:"deploy_strategy,omitempty"`
	Protected          bool                         `json:"protected"`
}

// UpdateEnvironmentNostrRequest is the signer-first environment/update payload.
type UpdateEnvironmentNostrRequest struct {
	ID                 string                       `json:"id"`
	OrgID              *string                      `json:"org_id,omitempty"`
	ExpectedUpdatedAt  *time.Time                   `json:"expected_updated_at,omitempty"`
	Name               *string                      `json:"name,omitempty"`
	LoomWorkerSelector *map[string]any              `json:"loom_worker_selector,omitempty"`
	RuntimeConfig      *map[string]any              `json:"runtime_config,omitempty"`
	Targeting          *EnvironmentTargetingRequest `json:"targeting,omitempty"`
	ReconcileMode      *string                      `json:"reconcile_mode,omitempty"`
	DeploymentUnits    *[]DeploymentUnitRequest     `json:"deployment_units,omitempty"`
	DeployStrategy     *string                      `json:"deploy_strategy,omitempty"`
	Protected          *bool                        `json:"protected,omitempty"`
}

// EnvironmentCommandResult is the terminal acknowledgment for signer-first environment mutations.
type EnvironmentCommandResult struct {
	Status          string                  `json:"status,omitempty"`
	Environment     *domain.Environment     `json:"environment,omitempty"`
	EnvironmentID   string                  `json:"environment_id,omitempty"`
	DeploymentUnits []domain.DeploymentUnit `json:"deployment_units,omitempty"`
	Message         string                  `json:"message,omitempty"`
}

// RouteAttachRequest is the signer-first service/route-attach payload.
type RouteAttachRequest struct {
	ServiceID        string                    `json:"service_id"`
	EnvironmentID    string                    `json:"environment_id"`
	DeploymentUnitID string                    `json:"deployment_unit_id,omitempty"`
	PublicRoute      domain.PublicRouteRequest `json:"public_route"`
	Internal         *bool                     `json:"internal,omitempty"`
	IdempotencyKey   string                    `json:"idempotency_key,omitempty"`
}

// RollbackDeploymentNostrRequest is the explicit signer-first rollback target.
// Requester attribution is derived from the signed event, never caller payload.
type RollbackDeploymentNostrRequest struct {
	ServiceID          string `json:"service_id"`
	EnvironmentID      string `json:"environment_id"`
	DeploymentUnitID   string `json:"deployment_unit_id,omitempty"`
	TargetArtifactID   string `json:"target_artifact_id"`
	SupersedesIntentID string `json:"supersedes_intent_id"`
	IdempotencyKey     string `json:"idempotency_key,omitempty"`
}

// DeploymentIntentNostrRequest is the signer-first deployment intent target.
// Requester attribution is derived from the signed event, never caller payload.
type DeploymentIntentNostrRequest struct {
	ServiceID                string `json:"service_id"`
	EnvironmentID            string `json:"environment_id"`
	DeploymentUnitID         string `json:"deployment_unit_id,omitempty"`
	ArtifactID               string `json:"artifact_id"`
	ExpectedDesiredStateHash string `json:"expected_desired_state_hash,omitempty"`
	RequestedBy              string `json:"-"`
	IdempotencyKey           string `json:"idempotency_key,omitempty"`
}

// DeploymentPreviewNostrRequest builds a reviewed managed desired-state hash
// for a subsequent signer-first deployment request.
type DeploymentPreviewNostrRequest struct {
	ServiceID            string         `json:"service_id"`
	EnvironmentID        string         `json:"environment_id"`
	DeploymentUnitID     string         `json:"deployment_unit_id,omitempty"`
	ArtifactID           string         `json:"artifact_id"`
	ManagedRuntimeConfig map[string]any `json:"managed_runtime_config"`
	IdempotencyKey       string         `json:"idempotency_key,omitempty"`
}

// DeploymentApprovalNostrRequest is the signer-first approval/rejection target.
type DeploymentApprovalNostrRequest struct {
	IntentID       string `json:"intent_id"`
	Decision       string `json:"decision"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

// DeploymentCommandResult is the terminal acknowledgment returned for signer-first deployment intent mutations.
type DeploymentCommandResult struct {
	Status           string                         `json:"status,omitempty"`
	IntentID         string                         `json:"intent_id,omitempty"`
	ServiceID        string                         `json:"service_id,omitempty"`
	EnvironmentID    string                         `json:"environment_id,omitempty"`
	DeploymentUnitID string                         `json:"deployment_unit_id,omitempty"`
	ArtifactID       string                         `json:"artifact_id,omitempty"`
	DesiredStateHash string                         `json:"desired_state_hash,omitempty"`
	PublicRoute      *domain.DesiredPublicRoutePlan `json:"public_route,omitempty"`
	Message          string                         `json:"message,omitempty"`
}

// PublishPolicyCreateNostr publishes a signed public PolicyCreate request and returns relay/follow correlation metadata.
func (c *OperatorControlPlaneClient) PublishPolicyCreateNostr(ctx context.Context, cmd controlplane.PolicyMutationCommand) (*controlplane.PolicyCommandReceipt, error) {
	if c == nil || c.transport == nil || c.signer == nil {
		return nil, &ControlPlaneRequestError{Phase: "configure policy command client", RequestAccepted: false, Cause: fmt.Errorf("operator control-plane client is not configured")}
	}
	publisher := controlplane.NewPolicyCommandPublisher(c.transport, c.signer)
	receipt, err := publisher.PublishPolicyCreateRequest(ctx, cmd)
	if err != nil {
		return nil, &ControlPlaneRequestError{Phase: "publish PolicyCreate request", RequestAccepted: false, Cause: err}
	}
	return receipt, nil
}

// CreateServiceNostr publishes a signer-first service/create mutation and awaits its correlated acknowledgment.
func (c *OperatorControlPlaneClient) CreateServiceNostr(ctx context.Context, req CreateServiceNostrRequest, onStatus func(OperatorStatusEvent)) (*ServiceCommandResult, error) {
	req.Name = strings.TrimSpace(req.Name)
	req.ArtifactRepo = strings.TrimSpace(req.ArtifactRepo)
	req.RuntimeType = strings.TrimSpace(req.RuntimeType)
	req.DefaultBranch = strings.TrimSpace(req.DefaultBranch)
	req.RepoURL = strings.TrimSpace(req.RepoURL)
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	if req.Name == "" {
		return nil, &ControlPlaneRequestError{Phase: "validate service create request", RequestAccepted: false, Cause: fmt.Errorf("name is required")}
	}
	if req.ArtifactRepo == "" {
		return nil, &ControlPlaneRequestError{Phase: "validate service create request", RequestAccepted: false, Cause: fmt.Errorf("artifact_repo is required")}
	}
	tags := nostr.Tags{{"service", req.Name}}
	if req.IdempotencyKey != "" {
		tags = append(nostr.Tags{{"d", req.IdempotencyKey}}, tags...)
	}
	event, err := c.publishAndAwait(ctx, operatorRequest{
		Method:  controlplane.ContextVMMethodServiceCreate,
		Tags:    tags,
		Payload: req,
	}, onStatus)
	if err != nil {
		return nil, err
	}
	var result ServiceCommandResult
	if err := json.Unmarshal([]byte(event.Content), &result); err != nil {
		return nil, fmt.Errorf("decode service create result: %w", err)
	}
	if result.Status == "" {
		result.Status = "created"
	}
	if result.ServiceID == "" && result.Service != nil {
		result.ServiceID = result.Service.ID.String()
	}
	return &result, nil
}

// UpdateServiceNostr publishes a signer-first service/update mutation and awaits its correlated acknowledgment.
func (c *OperatorControlPlaneClient) UpdateServiceNostr(ctx context.Context, req UpdateServiceNostrRequest, onStatus func(OperatorStatusEvent)) (*ServiceCommandResult, error) {
	req.ID = strings.TrimSpace(req.ID)
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	if req.OrgID != nil {
		trimmed := strings.TrimSpace(*req.OrgID)
		req.OrgID = &trimmed
	}
	if req.ID == "" {
		return nil, &ControlPlaneRequestError{Phase: "validate service update request", RequestAccepted: false, Cause: fmt.Errorf("id is required")}
	}
	tags := nostr.Tags{{"service", req.ID}}
	if req.IdempotencyKey != "" {
		tags = append(nostr.Tags{{"d", req.IdempotencyKey}}, tags...)
	}
	event, err := c.publishAndAwait(ctx, operatorRequest{
		Method:  controlplane.ContextVMMethodServiceUpdate,
		Tags:    tags,
		Payload: req,
	}, onStatus)
	if err != nil {
		return nil, err
	}
	var result ServiceCommandResult
	if err := json.Unmarshal([]byte(event.Content), &result); err != nil {
		return nil, fmt.Errorf("decode service update result: %w", err)
	}
	if result.Status == "" {
		result.Status = "updated"
	}
	if result.ServiceID == "" {
		result.ServiceID = req.ID
	}
	return &result, nil
}

// RegisterArtifactNostr publishes a signer-first artifact/register mutation and awaits its correlated acknowledgment.
func (c *OperatorControlPlaneClient) RegisterArtifactNostr(ctx context.Context, req RegisterArtifactNostrRequest, onStatus func(OperatorStatusEvent)) (*ArtifactCommandResult, error) {
	req.BuildID = strings.TrimSpace(req.BuildID)
	req.ServiceID = strings.TrimSpace(req.ServiceID)
	req.ImageRepo = strings.TrimSpace(req.ImageRepo)
	req.ImageTag = strings.TrimSpace(req.ImageTag)
	req.ImageDigest = strings.TrimSpace(req.ImageDigest)
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	for _, field := range []struct {
		name  string
		value string
	}{
		{"build_id", req.BuildID},
		{"service_id", req.ServiceID},
		{"image_repo", req.ImageRepo},
		{"image_tag", req.ImageTag},
		{"image_digest", req.ImageDigest},
	} {
		if field.value == "" {
			return nil, &ControlPlaneRequestError{Phase: "validate artifact register request", RequestAccepted: false, Cause: fmt.Errorf("%s is required", field.name)}
		}
	}
	tags := nostr.Tags{{"service", req.ServiceID}, {"build", req.BuildID}, {"digest", req.ImageDigest}}
	if req.IdempotencyKey != "" {
		tags = append(nostr.Tags{{"d", req.IdempotencyKey}}, tags...)
	}
	event, err := c.publishAndAwait(ctx, operatorRequest{
		Method:  controlplane.ContextVMMethodArtifactRegister,
		Tags:    tags,
		Payload: req,
	}, onStatus)
	if err != nil {
		return nil, err
	}
	var result ArtifactCommandResult
	if err := json.Unmarshal([]byte(event.Content), &result); err != nil {
		return nil, fmt.Errorf("decode artifact register result: %w", err)
	}
	if result.Status == "" {
		result.Status = "registered"
	}
	if result.ArtifactID == "" && result.Artifact != nil {
		result.ArtifactID = result.Artifact.ID.String()
	}
	return &result, nil
}

// ImportObservedArtifactNostr publishes a signed artifact/import-observed
// request. The digest is proven by Bahia's own runtime observation, so the
// operator cannot assert an image the control plane has not seen running.
func (c *OperatorControlPlaneClient) ImportObservedArtifactNostr(ctx context.Context, req ImportObservedArtifactNostrRequest, onStatus func(OperatorStatusEvent)) (*ImportObservedArtifactResult, error) {
	req.ServiceID = strings.TrimSpace(req.ServiceID)
	req.EnvironmentID = strings.TrimSpace(req.EnvironmentID)
	req.DeploymentUnitID = strings.TrimSpace(req.DeploymentUnitID)
	req.ImageRepo = strings.TrimSpace(req.ImageRepo)
	req.ImageTag = strings.TrimSpace(req.ImageTag)
	req.ImageDigest = strings.TrimSpace(req.ImageDigest)
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	for _, field := range []struct {
		name  string
		value string
	}{
		{"service_id", req.ServiceID},
		{"environment_id", req.EnvironmentID},
		{"image_repo", req.ImageRepo},
		{"image_tag", req.ImageTag},
		{"image_digest", req.ImageDigest},
	} {
		if field.value == "" {
			return nil, &ControlPlaneRequestError{Phase: "validate artifact import request", RequestAccepted: false, Cause: fmt.Errorf("%s is required", field.name)}
		}
	}
	tags := nostr.Tags{{"service", req.ServiceID}, {"environment", req.EnvironmentID}, {"digest", req.ImageDigest}}
	if req.IdempotencyKey != "" {
		tags = append(nostr.Tags{{"d", req.IdempotencyKey}}, tags...)
	}
	event, err := c.publishAndAwait(ctx, operatorRequest{
		Method:  controlplane.ContextVMMethodArtifactImportObserved,
		Tags:    tags,
		Payload: req,
	}, onStatus)
	if err != nil {
		return nil, err
	}
	var result ImportObservedArtifactResult
	if err := json.Unmarshal([]byte(event.Content), &result); err != nil {
		return nil, fmt.Errorf("decode artifact import result: %w", err)
	}
	if result.ArtifactID == "" && result.Artifact != nil {
		result.ArtifactID = result.Artifact.ID.String()
	}
	return &result, nil
}

// DNSZoneCreate publishes a signer-first dns/zone-create mutation and awaits its correlated acknowledgment.
func (c *OperatorControlPlaneClient) DNSZoneCreate(ctx context.Context, req DNSZoneCreateRequest, onStatus func(OperatorStatusEvent)) (*DNSCommandResult, error) {
	zone := domain.DNSZone{Name: req.Name, Visibility: req.Visibility, BackendRef: req.BackendRef, TTL: req.TTL, Authoritative: req.Authoritative}
	if err := domain.ValidateDNSZone(&zone); err != nil {
		return nil, &ControlPlaneRequestError{Phase: "validate DNS zone-create request", RequestAccepted: false, Cause: err}
	}
	req.Name, req.BackendRef = zone.Name, zone.BackendRef
	return c.publishDNSCommand(ctx, controlplane.ContextVMMethodDNSZoneCreate, nostr.Tags{{"zone", req.Name}}, req, onStatus)
}

// DNSPolicyApply publishes a signer-first dns/policy-apply mutation and awaits its correlated acknowledgment.
func (c *OperatorControlPlaneClient) DNSPolicyApply(ctx context.Context, req DNSPolicyApplyRequest, onStatus func(OperatorStatusEvent)) (*DNSCommandResult, error) {
	policy := domain.DNSPolicy{
		ID: req.ID, Name: req.Name, ZoneID: req.ZoneID, EnvironmentID: req.EnvironmentID,
		Rules: req.Rules, Enabled: req.Enabled, Metadata: req.Metadata, CreatedAt: req.CreatedAt, UpdatedAt: req.UpdatedAt,
	}
	if err := domain.ValidateDNSPolicy(&policy); err != nil {
		return nil, &ControlPlaneRequestError{Phase: "validate DNS policy-apply request", RequestAccepted: false, Cause: err}
	}
	req.Name = policy.Name
	return c.publishDNSCommand(ctx, controlplane.ContextVMMethodDNSPolicyApply, nostr.Tags{{"policy", req.Name}}, req, onStatus)
}

// DNSRecordSet publishes a signer-first dns/record-set mutation and awaits its correlated acknowledgment.
func (c *OperatorControlPlaneClient) DNSRecordSet(ctx context.Context, req DNSRecordSetRequest, onStatus func(OperatorStatusEvent)) (*DNSCommandResult, error) {
	req.ZoneName = strings.TrimSpace(req.ZoneName)
	req.RecordName = strings.TrimSpace(req.RecordName)
	req.Value = strings.TrimSpace(req.Value)
	req.Reason = strings.TrimSpace(req.Reason)
	if req.ZoneName == "" || req.RecordName == "" || req.Value == "" || req.Reason == "" || !req.RecordType.IsValid() || req.TTL <= 0 {
		return nil, &ControlPlaneRequestError{Phase: "validate DNS record-set request", RequestAccepted: false, Cause: fmt.Errorf("zone_name, record_name, valid record_type, value, ttl > 0, and reason are required")}
	}
	return c.publishDNSCommand(ctx, controlplane.ContextVMMethodDNSRecordSet, nostr.Tags{{"zone", req.ZoneName}, {"record", req.RecordName}}, req, onStatus)
}

// DNSDriftRemediate publishes a signer-first dns/drift-remediate mutation and awaits its correlated acknowledgment.
func (c *OperatorControlPlaneClient) DNSDriftRemediate(ctx context.Context, req DNSDriftRemediateRequest, onStatus func(OperatorStatusEvent)) (*DNSCommandResult, error) {
	req.Zone = strings.TrimSpace(req.Zone)
	tags := nostr.Tags{}
	if req.Zone != "" {
		tags = append(tags, nostr.Tag{"zone", req.Zone})
	}
	return c.publishDNSCommand(ctx, controlplane.ContextVMMethodDNSDriftRemediate, tags, req, onStatus)
}

func (c *OperatorControlPlaneClient) publishDNSCommand(ctx context.Context, method string, tags nostr.Tags, payload any, onStatus func(OperatorStatusEvent)) (*DNSCommandResult, error) {
	event, err := c.publishAndAwait(ctx, operatorRequest{Method: method, Tags: tags, Payload: payload}, onStatus)
	if err != nil {
		return nil, err
	}
	var result DNSCommandResult
	if err := json.Unmarshal([]byte(event.Content), &result); err != nil {
		return nil, fmt.Errorf("decode %s result: %w", method, err)
	}
	status := strings.TrimSpace(result.Status)
	if status != "success" {
		message := strings.TrimSpace(result.Message)
		if message == "" {
			message = "DNS mutation returned no failure message"
		}
		return nil, fmt.Errorf("%s failed with status %q: %s", method, status, message)
	}
	return &result, nil
}

// CreateEnvironmentNostr publishes a signer-first environment/create mutation and awaits its correlated acknowledgment.
func (c *OperatorControlPlaneClient) CreateEnvironmentNostr(ctx context.Context, req CreateEnvironmentNostrRequest, onStatus func(OperatorStatusEvent)) (*EnvironmentCommandResult, error) {
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return nil, &ControlPlaneRequestError{Phase: "validate environment create request", RequestAccepted: false, Cause: fmt.Errorf("name is required")}
	}
	req.OrgID = strings.TrimSpace(req.OrgID)
	event, err := c.publishAndAwait(ctx, operatorRequest{
		Method:  controlplane.ContextVMMethodEnvironmentCreate,
		Tags:    nostr.Tags{{"environment_name", req.Name}},
		Payload: req,
	}, onStatus)
	if err != nil {
		return nil, err
	}
	var result EnvironmentCommandResult
	if err := json.Unmarshal([]byte(event.Content), &result); err != nil {
		return nil, fmt.Errorf("decode environment create result: %w", err)
	}
	if result.Status == "" {
		result.Status = "created"
	}
	if result.EnvironmentID == "" && result.Environment != nil {
		result.EnvironmentID = result.Environment.ID.String()
	}
	return &result, nil
}

// GetEnvironmentDetailsNostr publishes a signer-first environment/get-details read and awaits its correlated result.
func (c *OperatorControlPlaneClient) GetEnvironmentDetailsNostr(ctx context.Context, environmentID string, onStatus func(OperatorStatusEvent)) (*EnvironmentDetails, error) {
	environmentID = strings.TrimSpace(environmentID)
	if environmentID == "" {
		return nil, &ControlPlaneRequestError{Phase: "validate environment get-details request", RequestAccepted: false, Cause: fmt.Errorf("id is required")}
	}
	event, err := c.publishAndAwait(ctx, operatorRequest{
		Method:  controlplane.ContextVMMethodEnvironmentGetDetails,
		Tags:    nostr.Tags{{"environment", environmentID}},
		Payload: map[string]string{"id": environmentID},
	}, onStatus)
	if err != nil {
		return nil, err
	}
	var result EnvironmentDetails
	if err := json.Unmarshal([]byte(event.Content), &result); err != nil {
		return nil, fmt.Errorf("decode environment get-details result: %w", err)
	}
	return &result, nil
}

// UpdateEnvironmentNostr publishes a signer-first environment/update mutation and awaits its correlated acknowledgment.
func (c *OperatorControlPlaneClient) UpdateEnvironmentNostr(ctx context.Context, req UpdateEnvironmentNostrRequest, onStatus func(OperatorStatusEvent)) (*EnvironmentCommandResult, error) {
	req.ID = strings.TrimSpace(req.ID)
	if req.ID == "" {
		return nil, &ControlPlaneRequestError{Phase: "validate environment update request", RequestAccepted: false, Cause: fmt.Errorf("id is required")}
	}
	if req.OrgID != nil {
		trimmed := strings.TrimSpace(*req.OrgID)
		req.OrgID = &trimmed
	}
	event, err := c.publishAndAwait(ctx, operatorRequest{
		Method:  controlplane.ContextVMMethodEnvironmentUpdate,
		Tags:    nostr.Tags{{"environment", req.ID}},
		Payload: req,
	}, onStatus)
	if err != nil {
		return nil, err
	}
	var result EnvironmentCommandResult
	if err := json.Unmarshal([]byte(event.Content), &result); err != nil {
		return nil, fmt.Errorf("decode environment update result: %w", err)
	}
	if result.Status == "" {
		result.Status = "updated"
	}
	if result.EnvironmentID == "" {
		result.EnvironmentID = req.ID
	}
	return &result, nil
}

// DeployServiceRuntimeNostr requests a direct runtime deploy over Nostr.
func (c *OperatorControlPlaneClient) DeployServiceRuntimeNostr(ctx context.Context, serviceID string, envID string, artifactID *string, onStatus func(OperatorStatusEvent)) (*RuntimeActionResult, error) {
	return c.runtimeAction(ctx, "deploy", serviceID, envID, artifactID, onStatus)
}

// CreateDeploymentIntentNostr publishes a signer-first service/deploy intent and awaits the correlated ContextVM acknowledgment.
func (c *OperatorControlPlaneClient) CreateDeploymentIntentNostr(ctx context.Context, serviceID, envID, artifactID, requestedBy string, onStatus func(OperatorStatusEvent)) (*DeploymentCommandResult, error) {
	return c.CreateDeploymentIntentWithRequestNostr(ctx, DeploymentIntentNostrRequest{
		ServiceID:     serviceID,
		EnvironmentID: envID,
		ArtifactID:    artifactID,
		RequestedBy:   requestedBy,
	}, onStatus)
}

// PreviewDeploymentNostr publishes signer-first service/deploy-preview and
// returns the reviewed desired-state preview payload.
func (c *OperatorControlPlaneClient) PreviewDeploymentNostr(ctx context.Context, req DeploymentPreviewNostrRequest, onStatus func(OperatorStatusEvent)) (map[string]any, error) {
	req.ServiceID = strings.TrimSpace(req.ServiceID)
	req.EnvironmentID = strings.TrimSpace(req.EnvironmentID)
	req.DeploymentUnitID = strings.TrimSpace(req.DeploymentUnitID)
	req.ArtifactID = strings.TrimSpace(req.ArtifactID)
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	if req.ServiceID == "" {
		return nil, &ControlPlaneRequestError{Phase: "validate deployment preview request", RequestAccepted: false, Cause: fmt.Errorf("service_id is required")}
	}
	if req.EnvironmentID == "" {
		return nil, &ControlPlaneRequestError{Phase: "validate deployment preview request", RequestAccepted: false, Cause: fmt.Errorf("environment_id is required")}
	}
	if req.ArtifactID == "" {
		return nil, &ControlPlaneRequestError{Phase: "validate deployment preview request", RequestAccepted: false, Cause: fmt.Errorf("artifact_id is required")}
	}
	if len(req.ManagedRuntimeConfig) == 0 {
		return nil, &ControlPlaneRequestError{Phase: "validate deployment preview request", RequestAccepted: false, Cause: fmt.Errorf("managed_runtime_config is required")}
	}
	payload := map[string]any{
		"service_id":             req.ServiceID,
		"environment_id":         req.EnvironmentID,
		"artifact_id":            req.ArtifactID,
		"managed_runtime_config": req.ManagedRuntimeConfig,
	}
	tags := nostr.Tags{{"service", req.ServiceID}, {"environment", req.EnvironmentID}, {"artifact", req.ArtifactID}}
	if req.DeploymentUnitID != "" {
		payload["deployment_unit_id"] = req.DeploymentUnitID
		tags = append(tags, nostr.Tag{"deployment-unit", req.DeploymentUnitID})
	}
	if req.IdempotencyKey != "" {
		payload["idempotency_key"] = req.IdempotencyKey
		tags = append(nostr.Tags{{"d", req.IdempotencyKey}}, tags...)
	}
	event, err := c.publishAndAwait(ctx, operatorRequest{Method: controlplane.ContextVMMethodServiceDeployPreview, Tags: tags, Payload: payload}, onStatus)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(event.Content), &result); err != nil {
		return nil, fmt.Errorf("decode deployment preview result: %w", err)
	}
	return result, nil
}

// CreateDeploymentIntentWithRequestNostr publishes a signer-first service/deploy intent with explicit correlation options.
func (c *OperatorControlPlaneClient) CreateDeploymentIntentWithRequestNostr(ctx context.Context, req DeploymentIntentNostrRequest, onStatus func(OperatorStatusEvent)) (*DeploymentCommandResult, error) {
	req.ServiceID = strings.TrimSpace(req.ServiceID)
	req.EnvironmentID = strings.TrimSpace(req.EnvironmentID)
	req.DeploymentUnitID = strings.TrimSpace(req.DeploymentUnitID)
	req.ArtifactID = strings.TrimSpace(req.ArtifactID)
	req.ExpectedDesiredStateHash = strings.TrimSpace(req.ExpectedDesiredStateHash)
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	if req.ServiceID == "" {
		return nil, &ControlPlaneRequestError{Phase: "validate deployment intent request", RequestAccepted: false, Cause: fmt.Errorf("service_id is required")}
	}
	if req.EnvironmentID == "" {
		return nil, &ControlPlaneRequestError{Phase: "validate deployment intent request", RequestAccepted: false, Cause: fmt.Errorf("environment_id is required")}
	}
	if req.ArtifactID == "" {
		return nil, &ControlPlaneRequestError{Phase: "validate deployment intent request", RequestAccepted: false, Cause: fmt.Errorf("artifact_id is required")}
	}
	// Attribution is signer-first: the server derives requested_by from the
	// verified ContextVM event pubkey. Keep the parameter for API compatibility,
	// but never serialize caller-provided attribution.
	_ = req.RequestedBy
	payload := map[string]any{"service_id": req.ServiceID, "environment_id": req.EnvironmentID, "artifact_id": req.ArtifactID}
	tags := nostr.Tags{{"service", req.ServiceID}, {"environment", req.EnvironmentID}, {"artifact", req.ArtifactID}}
	if req.DeploymentUnitID != "" {
		payload["deployment_unit_id"] = req.DeploymentUnitID
		tags = append(tags, nostr.Tag{"deployment-unit", req.DeploymentUnitID})
	}
	if req.ExpectedDesiredStateHash != "" {
		payload["expected_desired_state_hash"] = req.ExpectedDesiredStateHash
		tags = append(tags, nostr.Tag{"desired-hash", req.ExpectedDesiredStateHash})
	}
	if req.IdempotencyKey != "" {
		payload["idempotency_key"] = req.IdempotencyKey
		tags = append(nostr.Tags{{"d", req.IdempotencyKey}}, tags...)
	}
	event, err := c.publishAndAwait(ctx, operatorRequest{Method: controlplane.ContextVMMethodServiceDeploy, Tags: tags, Payload: payload}, onStatus)
	if err != nil {
		return nil, err
	}
	var result DeploymentCommandResult
	if err := json.Unmarshal([]byte(event.Content), &result); err != nil {
		return nil, fmt.Errorf("decode deployment intent result: %w", err)
	}
	if result.Status == "" {
		result.Status = "submitted"
	}
	if result.ServiceID == "" {
		result.ServiceID = req.ServiceID
	}
	if result.EnvironmentID == "" {
		result.EnvironmentID = req.EnvironmentID
	}
	if result.DeploymentUnitID == "" {
		result.DeploymentUnitID = req.DeploymentUnitID
	}
	if result.ArtifactID == "" {
		result.ArtifactID = req.ArtifactID
	}
	return &result, nil
}

// RouteAttach publishes a signer-first service/route-attach intent for the current deployed artifact.
func (c *OperatorControlPlaneClient) RouteAttach(ctx context.Context, req RouteAttachRequest, onStatus func(OperatorStatusEvent)) (*DeploymentCommandResult, error) {
	req.ServiceID = strings.TrimSpace(req.ServiceID)
	req.EnvironmentID = strings.TrimSpace(req.EnvironmentID)
	req.DeploymentUnitID = strings.TrimSpace(req.DeploymentUnitID)
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	if req.ServiceID == "" {
		return nil, &ControlPlaneRequestError{Phase: "validate route attach request", RequestAccepted: false, Cause: fmt.Errorf("service_id is required")}
	}
	if req.EnvironmentID == "" {
		return nil, &ControlPlaneRequestError{Phase: "validate route attach request", RequestAccepted: false, Cause: fmt.Errorf("environment_id is required")}
	}
	normalized, err := domain.NormalizePublicRouteRequest(req.PublicRoute)
	if err != nil {
		return nil, &ControlPlaneRequestError{Phase: "validate route attach request", RequestAccepted: false, Cause: err}
	}
	req.PublicRoute = normalized
	tags := nostr.Tags{{"service", req.ServiceID}, {"environment", req.EnvironmentID}, {"hostname", req.PublicRoute.Hostname}}
	if req.DeploymentUnitID != "" {
		tags = append(tags, nostr.Tag{"deployment_unit", req.DeploymentUnitID})
	}
	if req.IdempotencyKey != "" {
		tags = append(nostr.Tags{{"d", req.IdempotencyKey}}, tags...)
	}
	event, err := c.publishAndAwait(ctx, operatorRequest{Method: controlplane.ContextVMMethodServiceRouteAttach, Tags: tags, Payload: req}, onStatus)
	if err != nil {
		return nil, err
	}
	var result DeploymentCommandResult
	if err := json.Unmarshal([]byte(event.Content), &result); err != nil {
		return nil, fmt.Errorf("decode route attach result: %w", err)
	}
	if result.Status == "" {
		result.Status = "submitted"
	}
	if result.ServiceID == "" {
		result.ServiceID = req.ServiceID
	}
	if result.EnvironmentID == "" {
		result.EnvironmentID = req.EnvironmentID
	}
	if result.DeploymentUnitID == "" {
		result.DeploymentUnitID = req.DeploymentUnitID
	}
	return &result, nil
}

// RollbackDeploymentNostr publishes a signer-first service/rollback intent and awaits the correlated ContextVM acknowledgment.
func (c *OperatorControlPlaneClient) RollbackDeploymentNostr(ctx context.Context, req RollbackDeploymentNostrRequest, onStatus func(OperatorStatusEvent)) (*DeploymentCommandResult, error) {
	req.ServiceID = strings.TrimSpace(req.ServiceID)
	req.EnvironmentID = strings.TrimSpace(req.EnvironmentID)
	req.DeploymentUnitID = strings.TrimSpace(req.DeploymentUnitID)
	req.TargetArtifactID = strings.TrimSpace(req.TargetArtifactID)
	req.SupersedesIntentID = strings.TrimSpace(req.SupersedesIntentID)
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	for field, value := range map[string]string{
		"service_id":           req.ServiceID,
		"environment_id":       req.EnvironmentID,
		"target_artifact_id":   req.TargetArtifactID,
		"supersedes_intent_id": req.SupersedesIntentID,
	} {
		if value == "" {
			return nil, &ControlPlaneRequestError{Phase: "validate rollback request", RequestAccepted: false, Cause: fmt.Errorf("%s is required", field)}
		}
	}
	tags := nostr.Tags{{"service", req.ServiceID}, {"environment", req.EnvironmentID}, {"artifact", req.TargetArtifactID}, {"supersedes", req.SupersedesIntentID}}
	if req.DeploymentUnitID != "" {
		tags = append(tags, nostr.Tag{"deployment_unit", req.DeploymentUnitID})
	}
	payload := map[string]any{
		"service_id":           req.ServiceID,
		"environment_id":       req.EnvironmentID,
		"target_artifact_id":   req.TargetArtifactID,
		"supersedes_intent_id": req.SupersedesIntentID,
	}
	if req.DeploymentUnitID != "" {
		payload["deployment_unit_id"] = req.DeploymentUnitID
	}
	if req.IdempotencyKey != "" {
		tags = append(nostr.Tags{{"d", req.IdempotencyKey}}, tags...)
	}
	event, err := c.publishAndAwait(ctx, operatorRequest{Method: "service/rollback", Tags: tags, Payload: payload}, onStatus)
	if err != nil {
		return nil, err
	}
	var result DeploymentCommandResult
	if err := json.Unmarshal([]byte(event.Content), &result); err != nil {
		return nil, fmt.Errorf("decode rollback result: %w", err)
	}
	if result.Status == "" {
		result.Status = "submitted"
	}
	if result.ServiceID == "" {
		result.ServiceID = req.ServiceID
	}
	if result.EnvironmentID == "" {
		result.EnvironmentID = req.EnvironmentID
	}
	if result.DeploymentUnitID == "" {
		result.DeploymentUnitID = req.DeploymentUnitID
	}
	if result.ArtifactID == "" {
		result.ArtifactID = req.TargetArtifactID
	}
	return &result, nil
}

// ApproveDeploymentNostr publishes a signer-first approval or rejection for a pending deployment intent.
func (c *OperatorControlPlaneClient) ApproveDeploymentNostr(ctx context.Context, req DeploymentApprovalNostrRequest, onStatus func(OperatorStatusEvent)) (*DeploymentCommandResult, error) {
	req.IntentID = strings.TrimSpace(req.IntentID)
	req.Decision = strings.ToLower(strings.TrimSpace(req.Decision))
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	if req.IntentID == "" {
		return nil, &ControlPlaneRequestError{Phase: "validate deployment approval request", RequestAccepted: false, Cause: fmt.Errorf("intent_id is required")}
	}
	if req.Decision != "approve" && req.Decision != "reject" {
		return nil, &ControlPlaneRequestError{Phase: "validate deployment approval request", RequestAccepted: false, Cause: fmt.Errorf("decision must be approve or reject")}
	}
	method := controlplane.ContextVMMethodApprovalApprove
	if req.Decision == "reject" {
		method = controlplane.ContextVMMethodApprovalReject
	}
	payload := map[string]any{"intent_id": req.IntentID, "decision": req.Decision}
	tags := nostr.Tags{{"intent", req.IntentID}, {"decision", req.Decision}}
	if req.IdempotencyKey != "" {
		tags = append(nostr.Tags{{"d", req.IdempotencyKey}}, tags...)
	}
	event, err := c.publishAndAwait(ctx, operatorRequest{Method: method, Tags: tags, Payload: payload}, onStatus)
	if err != nil {
		return nil, err
	}
	var result DeploymentCommandResult
	if err := json.Unmarshal([]byte(event.Content), &result); err != nil {
		return nil, fmt.Errorf("decode deployment approval result: %w", err)
	}
	if result.Status == "" {
		result.Status = "submitted"
	}
	if result.IntentID == "" {
		result.IntentID = req.IntentID
	}
	return &result, nil
}

// RestartServiceRuntimeNostr requests a direct runtime restart over Nostr.
func (c *OperatorControlPlaneClient) RestartServiceRuntimeNostr(ctx context.Context, serviceID string, envID string, onStatus func(OperatorStatusEvent)) (*RuntimeActionResult, error) {
	return c.runtimeAction(ctx, "restart", serviceID, envID, nil, onStatus)
}

// StopServiceRuntimeNostr requests a direct runtime stop over Nostr.
func (c *OperatorControlPlaneClient) StopServiceRuntimeNostr(ctx context.Context, serviceID string, envID string, onStatus func(OperatorStatusEvent)) (*RuntimeActionResult, error) {
	return c.runtimeAction(ctx, "stop", serviceID, envID, nil, onStatus)
}

// ScanAdoptionNostr requests adoption scan previews over Nostr.
func (c *OperatorControlPlaneClient) ScanAdoptionNostr(ctx context.Context, req AdoptionScanRequest, onStatus func(OperatorStatusEvent)) ([]AdoptionPreview, error) {
	if err := validateSignerFirstAdoptionTargets(req.Targets); err != nil {
		return nil, &ControlPlaneRequestError{Phase: "validate adoption scan request", RequestAccepted: false, Cause: err}
	}
	payload := adoptionScanEventRequest{Targets: adoptionEventTargetsFromClient(req.Targets)}
	event, err := c.publishAndAwait(ctx, operatorRequest{
		Method:  "adoption/scan",
		Tags:    adoptionRequestTags("scan", req.Targets),
		Payload: payload,
	}, onStatus)
	if err != nil {
		return nil, err
	}
	var previews []AdoptionPreview
	if err := json.Unmarshal([]byte(event.Content), &previews); err == nil {
		return previews, nil
	}
	return nil, terminalEventError("adoption scan", event)
}

// ImportAdoptionNostr requests adoption import over Nostr.
func (c *OperatorControlPlaneClient) ImportAdoptionNostr(ctx context.Context, req AdoptionImportRequest, onStatus func(OperatorStatusEvent)) ([]AdoptionImportResult, error) {
	if err := validateSignerFirstAdoptionTargets(req.Targets); err != nil {
		return nil, &ControlPlaneRequestError{Phase: "validate adoption import request", RequestAccepted: false, Cause: err}
	}
	if !req.ImportAll && len(req.Selections) == 0 {
		return nil, &ControlPlaneRequestError{Phase: "validate adoption import request", RequestAccepted: false, Cause: fmt.Errorf("import requires import_all=true or at least one selection")}
	}
	payload := adoptionImportEventRequest{
		Targets:    adoptionEventTargetsFromClient(req.Targets),
		Selections: adoptionEventSelectionsFromClient(req.Selections),
		ImportAll:  req.ImportAll,
		OrgID:      strings.TrimSpace(req.OrgID),
	}
	event, err := c.publishAndAwait(ctx, operatorRequest{
		Method:  "adoption/import",
		Tags:    adoptionRequestTags("import", req.Targets),
		Payload: payload,
	}, onStatus)
	if err != nil {
		return nil, err
	}
	var results []AdoptionImportResult
	if err := json.Unmarshal([]byte(event.Content), &results); err == nil {
		return results, nil
	}
	return nil, terminalEventError("adoption import", event)
}

func (c *OperatorControlPlaneClient) runtimeAction(ctx context.Context, action string, serviceID string, envID string, artifactID *string, onStatus func(OperatorStatusEvent)) (*RuntimeActionResult, error) {
	serviceID = strings.TrimSpace(serviceID)
	envID = strings.TrimSpace(envID)
	action = strings.ToLower(strings.TrimSpace(action))
	if action != "deploy" && action != "restart" && action != "stop" {
		return nil, &ControlPlaneRequestError{Phase: "validate runtime action request", RequestAccepted: false, Cause: fmt.Errorf("unsupported runtime action %q", action)}
	}
	if serviceID == "" {
		return nil, &ControlPlaneRequestError{Phase: "validate runtime action request", RequestAccepted: false, Cause: fmt.Errorf("service_id is required")}
	}
	if envID == "" {
		return nil, &ControlPlaneRequestError{Phase: "validate runtime action request", RequestAccepted: false, Cause: fmt.Errorf("environment_id is required")}
	}
	payload := directRuntimeActionEventRequest{Action: action, ServiceID: serviceID, EnvironmentID: envID}
	tags := nostr.Tags{{"action", action}, {"service", serviceID}, {"environment", envID}}
	if artifactID != nil && strings.TrimSpace(*artifactID) != "" {
		if action != "deploy" {
			return nil, &ControlPlaneRequestError{Phase: "validate runtime action request", RequestAccepted: false, Cause: fmt.Errorf("artifact_id is only valid for deploy actions")}
		}
		payload.ArtifactID = strings.TrimSpace(*artifactID)
		tags = append(tags, nostr.Tag{"artifact", payload.ArtifactID})
	}
	event, err := c.publishAndAwait(ctx, operatorRequest{
		Method:  "service/action",
		Tags:    tags,
		Payload: payload,
	}, onStatus)
	if err != nil {
		return nil, err
	}
	status := firstTagValue(event.Tags, "status")
	if status != "" && status != "success" {
		return nil, terminalEventError("runtime action", event)
	}
	var result RuntimeActionResult
	if err := json.Unmarshal([]byte(event.Content), &result); err != nil {
		return nil, fmt.Errorf("decode runtime action result: %w", err)
	}
	return &result, nil
}

type operatorRequest struct {
	Method  string
	Tags    nostr.Tags
	Payload any
}

type contextVMRPCRequest struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      string         `json:"id"`
	Method  string         `json:"method"`
	Params  map[string]any `json:"params"`
}

type contextVMRPCResponse struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      json.RawMessage  `json:"id,omitempty"`
	Result  *json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (c *OperatorControlPlaneClient) publishAndAwait(ctx context.Context, req operatorRequest, onStatus func(OperatorStatusEvent)) (*nostr.Event, error) {
	if c == nil {
		return nil, &ControlPlaneRequestError{Phase: "configure operator control-plane client", RequestAccepted: false, Cause: fmt.Errorf("operator control-plane client is not configured")}
	}
	core := &ContextVMRequestClient{
		relays:            c.relays,
		signer:            c.signer,
		cipher:            c.cipher,
		pubkey:            c.pubkey,
		transport:         c.transport,
		servicePubkey:     c.servicePubkey,
		encrypted:         c.encrypted,
		resultTimeout:     c.resultTimeout,
		resultRetries:     c.resultRetries,
		activationTimeout: c.activationTimeout,
	}
	return core.Request(ctx, req.Method, req.Payload, req.Tags, onStatus)
}

func (c *ContextVMRequestClient) prepareOperatorAttempt(ctx context.Context, inner *nostr.Event, priorOuterIDs []string) (*nostr.Event, []nostr.Filter, []string, error) {
	if !c.encrypted {
		filter := nostr.Filter{
			Kinds: []nostr.Kind{nostr.Kind(controlplane.KindContextVMMessage)},
			Tags:  nostr.TagMap{"e": []string{inner.ID.Hex()}, "p": []string{c.pubkey}},
		}
		if c.servicePubkey != "" {
			serviceAuthor, err := nostr.PubKeyFromHex(c.servicePubkey)
			if err != nil {
				return nil, nil, priorOuterIDs, fmt.Errorf("parse service pubkey: %w", err)
			}
			filter.Authors = []nostr.PubKey{serviceAuthor}
		}
		return inner, []nostr.Filter{filter}, priorOuterIDs, nil
	}
	outer, rumor, err := cascontextvm.WrapEventNIP59(ctx, nip59KeyerAdapter{c.cipher}, c.servicePubkey, inner, cascontextvm.StoredGiftWrap)
	if err != nil {
		return nil, nil, priorOuterIDs, err
	}
	if rumor.ID != inner.ID {
		return nil, nil, priorOuterIDs, fmt.Errorf("NIP-59 wrapper changed inner request correlation id")
	}
	outerIDs := append(append([]string(nil), priorOuterIDs...), outer.ID.Hex())
	filter := nostr.Filter{
		Kinds: []nostr.Kind{nostr.Kind(controlplane.KindContextVMGiftWrap), nostr.Kind(controlplane.KindContextVMEphemeralWrap)},
		Tags:  nostr.TagMap{"e": outerIDs, "p": []string{c.pubkey}},
	}
	return outer, []nostr.Filter{filter}, outerIDs, nil
}

func (c *ContextVMRequestClient) waitForOperatorSubscriptionActivation(ctx context.Context, sub *operatorSubscription, filters []nostr.Filter, timeout time.Duration) (*operatorSubscription, error) {
	activationCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	current := sub
	active := operatorRelaySet(current.RelayURLs())
	eosed := map[string]struct{}{}
	closedRelays := map[string]string{}
	authAttempted := map[string]struct{}{}
	for {
		select {
		case <-ctx.Done():
			return current, ctx.Err()
		case <-activationCtx.Done():
			if len(eosed) > 0 {
				return current, nil
			}
			if len(active) > 0 {
				return current, nil
			}
			return current, fmt.Errorf("no subscribed relay reached EOSE before activation deadline: %w", activationCtx.Err())
		case _, ok := <-current.EndOfStoredEvents:
			if !ok {
				current.EndOfStoredEvents = nil
			}
		case info, ok := <-current.RelayEOSE:
			if !ok {
				current.RelayEOSE = nil
				continue
			}
			if _, subscribed := active[info.RelayURL]; subscribed {
				eosed[info.RelayURL] = struct{}{}
			}
			if len(active) > 0 && len(eosed) >= len(active) {
				return current, nil
			}
		case relayClosed, ok := <-current.Closed:
			if !ok {
				current.Closed = nil
				continue
			}
			relayURL := strings.TrimSpace(relayClosed.RelayURL)
			reason := strings.TrimSpace(relayClosed.Reason)
			if reason == "" {
				reason = "subscription closed"
			}
			if relayURL == "" {
				return current, fmt.Errorf("reply subscription closed before EOSE: %s", reason)
			}
			if _, subscribed := active[relayURL]; !subscribed {
				continue
			}
			closedRelays[relayURL] = reason
			if nostrpool.IsAuthRequiredReason(reason) {
				if _, attempted := authAttempted[relayURL]; !attempted {
					authAttempted[relayURL] = struct{}{}
					if authErr := c.transport.AuthenticateRelay(activationCtx, relayURL); authErr == nil {
						resub, subErr := c.transport.SubscribeOperator(activationCtx, filters)
						if subErr == nil && len(resub.RelayURLs()) > 0 {
							current.Close()
							current = resub
							active = operatorRelaySet(current.RelayURLs())
							eosed = map[string]struct{}{}
							closedRelays = map[string]string{}
							continue
						}
						if resub != nil {
							resub.Close()
						}
					}
				}
			}
			delete(active, relayURL)
			delete(eosed, relayURL)
			current.relayURLs = removeRelayURL(current.relayURLs, relayURL)
			if len(active) == 0 {
				return current, fmt.Errorf("all reply subscriptions closed before EOSE: %s", formatOperatorClosedRelays(closedRelays))
			}
			if len(eosed) >= len(active) {
				return current, nil
			}
		}
	}
}

func operatorRelaySet(relays []string) map[string]struct{} {
	set := make(map[string]struct{}, len(relays))
	for _, relay := range relays {
		if relay = strings.TrimSpace(relay); relay != "" {
			set[relay] = struct{}{}
		}
	}
	return set
}

func (c *ContextVMRequestClient) awaitOperatorResult(ctx context.Context, sub *operatorSubscription, filters []nostr.Filter, inner *nostr.Event, outerRequestIDs []string, requestID string, onStatus func(OperatorStatusEvent)) (*nostr.Event, error) {
	seen := map[string]struct{}{}
	pendingRelays := sub.RelayURLs()
	closedRelays := map[string]string{}
	authAttempted := map[string]struct{}{}
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case relayClosed, ok := <-sub.Closed:
			if !ok {
				sub.Closed = nil
				continue
			}
			reason := strings.TrimSpace(relayClosed.Reason)
			if reason == "" {
				reason = "subscription closed"
			}
			if relayClosed.RelayURL == "" {
				return nil, fmt.Errorf("reply subscription closed before terminal result: %s", reason)
			}
			if nostrpool.IsAuthRequiredReason(reason) {
				if _, attempted := authAttempted[relayClosed.RelayURL]; !attempted {
					authAttempted[relayClosed.RelayURL] = struct{}{}
					if authErr := c.transport.AuthenticateRelay(ctx, relayClosed.RelayURL); authErr == nil {
						sub.Close()
						resub, subErr := c.transport.SubscribeOperator(ctx, filters)
						if subErr != nil {
							return nil, fmt.Errorf("re-open reply subscription after NIP-42 AUTH: %w", subErr)
						}
						if len(resub.RelayURLs()) == 0 {
							resub.Close()
							return nil, fmt.Errorf("re-open reply subscription after NIP-42 AUTH: no relay established a subscription")
						}
						activationTimeout := c.activationTimeout
						if activationTimeout <= 0 {
							activationTimeout = operatorActivationTimeout
						}
						activatedSub, activationErr := c.waitForOperatorSubscriptionActivation(ctx, resub, filters, activationTimeout)
						if activationErr != nil {
							activatedSub.Close()
							return nil, fmt.Errorf("activate reply subscription after NIP-42 AUTH: %w", activationErr)
						}
						defer activatedSub.Close()
						sub = activatedSub
						pendingRelays = sub.RelayURLs()
						closedRelays = map[string]string{}
						continue
					}
				}
			}
			closedRelays[relayClosed.RelayURL] = reason
			pendingRelays = removeRelayURL(pendingRelays, relayClosed.RelayURL)
			if len(pendingRelays) == 0 {
				return nil, fmt.Errorf("reply subscription closed before result from all relays: %s", formatOperatorClosedRelays(closedRelays))
			}
		case reply, ok := <-sub.Events:
			if !ok {
				return nil, fmt.Errorf("reply subscription closed before terminal result")
			}
			validated := c.validateOperatorReply(ctx, reply, inner, outerRequestIDs)
			if validated == nil {
				continue
			}
			replyID := validated.ID.Hex()
			if _, duplicate := seen[replyID]; duplicate {
				continue
			}
			seen[replyID] = struct{}{}
			var rpc contextVMRPCResponse
			if err := json.Unmarshal([]byte(validated.Content), &rpc); err != nil || rpc.JSONRPC != "2.0" || !contextVMResponseIDMatches(rpc.ID, requestID) {
				continue
			}
			if rpc.Error != nil {
				message := strings.TrimSpace(rpc.Error.Message)
				if message == "" {
					message = fmt.Sprintf("ContextVM error code %d", rpc.Error.Code)
				}
				return nil, &ContextVMRemoteError{Code: rpc.Error.Code, Message: message}
			}
			if rpc.Result == nil {
				continue
			}
			synthetic := *validated
			synthetic.Content = string(*rpc.Result)
			synthetic.Tags = append(nostr.Tags{}, validated.Tags...)
			annotateContextVMResultTags(&synthetic)
			if onStatus != nil && contextVMResultIsProgress(synthetic.Content) {
				onStatus(statusEventFromNostr(&synthetic))
				continue
			}
			unwrapSuccessfulContextVMResult(&synthetic)
			return &synthetic, nil
		}
	}
}

func (c *ContextVMRequestClient) validateOperatorReply(ctx context.Context, reply, inner *nostr.Event, outerRequestIDs []string) *nostr.Event {
	if reply == nil || !validSignedEvent(reply) {
		return nil
	}
	if !c.encrypted {
		if reply.Kind != nostr.Kind(controlplane.KindContextVMMessage) || !correlatesTo(reply, inner.ID.Hex(), c.pubkey) {
			return nil
		}
		if c.servicePubkey != "" && reply.PubKey.Hex() != c.servicePubkey {
			return nil
		}
		return reply
	}
	if (reply.Kind != nostr.Kind(controlplane.KindContextVMGiftWrap) && reply.Kind != nostr.Kind(controlplane.KindContextVMEphemeralWrap)) || !correlatesToAny(reply, outerRequestIDs, c.pubkey) {
		return nil
	}
	plaintext, err := c.cipher.Decrypt(ctx, reply.Content, reply.PubKey)
	if err != nil {
		return nil
	}
	var response nostr.Event
	if err := json.Unmarshal([]byte(plaintext), &response); err != nil {
		return nil
	}
	if response.Kind != nostr.Kind(controlplane.KindContextVMMessage) || !validSignedEvent(&response) || response.PubKey.Hex() != c.servicePubkey || !correlatesTo(&response, inner.ID.Hex(), c.pubkey) {
		return nil
	}
	return &response
}

func operatorFailedSubscriptions(configured, subscribed []string) []string {
	active := make(map[string]struct{}, len(subscribed))
	for _, relay := range subscribed {
		active[relay] = struct{}{}
	}
	failed := make([]string, 0, len(configured))
	for _, relay := range configured {
		if _, ok := active[relay]; !ok {
			failed = append(failed, relay)
		}
	}
	return failed
}

func (c *ContextVMRequestClient) publishOperatorEvent(ctx context.Context, event nostr.Event) (int, []OperatorPublishResult, error) {
	results, err := c.transport.PublishWithResults(ctx, event)
	diagnostics := operatorPublishDiagnostics(results)
	if len(results) == 0 {
		return 0, diagnostics, err
	}
	published := 0
	for _, result := range results {
		if result.Accepted || result.IsDuplicate() {
			published++
		}
	}
	return published, diagnostics, err
}

func operatorPublishDiagnostics(results []nostrpool.PublishResult) []OperatorPublishResult {
	if len(results) == 0 {
		return nil
	}
	out := make([]OperatorPublishResult, 0, len(results))
	for _, result := range results {
		item := OperatorPublishResult{
			RelayURL:  result.RelayURL,
			Accepted:  result.Accepted,
			Duplicate: result.IsDuplicate(),
			Reason:    strings.TrimSpace(result.Reason),
		}
		if result.Error != nil {
			item.Error = result.Error.Error()
		}
		out = append(out, item)
	}
	return out
}

func formatOperatorPublishResults(results []OperatorPublishResult) string {
	if len(results) == 0 {
		return ""
	}
	parts := make([]string, 0, len(results))
	for _, result := range results {
		status := "rejected"
		if result.Accepted {
			status = "accepted"
		} else if result.Duplicate {
			status = "duplicate"
		}
		detail := strings.TrimSpace(result.Reason)
		if detail == "" {
			detail = strings.TrimSpace(result.Error)
		}
		if detail != "" {
			status += ":" + detail
		}
		if result.RelayURL != "" {
			status = result.RelayURL + "=" + status
		}
		parts = append(parts, status)
	}
	return strings.Join(parts, ",")
}

func removeRelayURL(relays []string, relayURL string) []string {
	if relayURL == "" || len(relays) == 0 {
		return relays
	}
	out := relays[:0]
	for _, relay := range relays {
		if relay != relayURL {
			out = append(out, relay)
		}
	}
	return out
}

func formatOperatorClosedRelays(closed map[string]string) string {
	if len(closed) == 0 {
		return "all relays closed"
	}
	parts := make([]string, 0, len(closed))
	for relay, reason := range closed {
		if strings.TrimSpace(reason) == "" {
			parts = append(parts, relay)
			continue
		}
		parts = append(parts, fmt.Sprintf("%s (%s)", relay, reason))
	}
	return strings.Join(parts, "; ")
}

func contextVMParams(payload any, progressToken string) (map[string]any, error) {
	if payload == nil {
		return map[string]any{"_meta": map[string]any{"progressToken": progressToken}}, nil
	}
	content, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	params := map[string]any{}
	if len(content) > 0 && string(content) != "null" {
		decoder := json.NewDecoder(strings.NewReader(string(content)))
		decoder.UseNumber()
		if err := decoder.Decode(&params); err != nil {
			params["value"] = payload
		}
	}
	meta, _ := params["_meta"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
	}
	meta["progressToken"] = progressToken
	params["_meta"] = meta
	return params, nil
}

func contextVMResponseIDMatches(id json.RawMessage, want string) bool {
	if len(id) == 0 || strings.TrimSpace(want) == "" {
		return true
	}
	var s string
	if err := json.Unmarshal(id, &s); err == nil {
		return s == want
	}
	var v any
	if err := json.Unmarshal(id, &v); err != nil {
		return false
	}
	return fmt.Sprint(v) == want
}

func contextVMResultIsProgress(content string) bool {
	var envelope map[string]any
	if err := json.Unmarshal([]byte(content), &envelope); err != nil {
		return false
	}
	status := strings.ToLower(strings.TrimSpace(fmt.Sprint(envelope["status"])))
	return status == "processing" || status == "pending" || status == "running"
}

func unwrapSuccessfulContextVMResult(event *nostr.Event) {
	if event == nil {
		return
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(event.Content), &envelope); err != nil {
		return
	}
	var status string
	if raw, ok := envelope["status"]; ok {
		_ = json.Unmarshal(raw, &status)
	}
	status = strings.ToLower(strings.TrimSpace(status))
	if status != "ok" && status != "success" {
		return
	}
	for _, key := range []string{"payload", "result"} {
		if raw, ok := envelope[key]; ok && len(raw) > 0 && string(raw) != "null" {
			event.Content = string(raw)
			return
		}
	}
}

func annotateContextVMResultTags(event *nostr.Event) {
	if event == nil {
		return
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(event.Content), &envelope); err != nil {
		return
	}
	if status := strings.TrimSpace(fmt.Sprint(envelope["status"])); status != "" && status != "<nil>" {
		event.Tags = append(event.Tags, nostr.Tag{"status", strings.ToLower(status)})
	}
	if step := strings.TrimSpace(fmt.Sprint(envelope["step"])); step != "" && step != "<nil>" {
		event.Tags = append(event.Tags, nostr.Tag{"step", step})
	}
	if action := strings.TrimSpace(fmt.Sprint(envelope["action"])); action != "" && action != "<nil>" {
		event.Tags = append(event.Tags, nostr.Tag{"action", action})
	}
	if operation := strings.TrimSpace(fmt.Sprint(envelope["operation"])); operation != "" && operation != "<nil>" {
		event.Tags = append(event.Tags, nostr.Tag{"operation", operation})
	}
	if message := strings.TrimSpace(fmt.Sprint(envelope["message"])); message != "" && message != "<nil>" {
		event.Tags = append(event.Tags, nostr.Tag{"message", message})
	}
	if errMessage := strings.TrimSpace(fmt.Sprint(envelope["error"])); errMessage != "" && errMessage != "<nil>" {
		event.Tags = append(event.Tags, nostr.Tag{"error", errMessage})
	}
}

func deterministicOperatorIdempotencyKey(method string, tags nostr.Tags, content []byte) string {
	h := sha256.New()
	_, _ = h.Write([]byte("operator:" + method))
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] != "d" && tag[0] != "method" && tag[0] != controlplane.ContextVMRoutingTag {
			_, _ = h.Write([]byte{0})
			_, _ = h.Write([]byte(strings.Join(tag, "=")))
		}
	}
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(content)
	safeMethod := strings.NewReplacer("/", "-", " ", "-").Replace(method)
	return fmt.Sprintf("operator:%s:%s", safeMethod, hex.EncodeToString(h.Sum(nil))[:24])
}

func validSignedEvent(event *nostr.Event) bool {
	if event == nil || !event.CheckID() {
		return false
	}
	now := int64(nostr.Now())
	createdAt := int64(event.CreatedAt)
	if createdAt > now+600 || createdAt < now-365*24*60*60 {
		return false
	}
	return event.VerifySignature()
}

func correlatesTo(event *nostr.Event, requestID, pubkey string) bool {
	return tagHasValue(event.Tags, "e", requestID) && tagHasValue(event.Tags, "p", pubkey)
}

func correlatesToAny(event *nostr.Event, requestIDs []string, pubkey string) bool {
	if event == nil || !tagHasValue(event.Tags, "p", pubkey) {
		return false
	}
	for _, requestID := range requestIDs {
		if tagHasValue(event.Tags, "e", requestID) {
			return true
		}
	}
	return false
}

func statusEventFromNostr(event *nostr.Event) OperatorStatusEvent {
	tags := tagMap(event.Tags)
	return OperatorStatusEvent{
		Kind:      int(event.Kind),
		EventID:   event.ID.Hex(),
		Status:    firstValue(tags, "status"),
		Step:      firstValue(tags, "step"),
		Action:    firstValue(tags, "action"),
		Operation: firstValue(tags, "operation"),
		Message:   event.Content,
		Tags:      tags,
	}
}

func terminalEventError(operation string, event *nostr.Event) error {
	message := firstTagValue(event.Tags, "error")
	if message == "" {
		var envelope map[string]any
		if err := json.Unmarshal([]byte(event.Content), &envelope); err == nil {
			if v, ok := envelope["error"].(string); ok {
				message = v
			} else if v, ok := envelope["message"].(string); ok {
				message = v
			}
		}
	}
	if message == "" {
		message = strings.TrimSpace(event.Content)
	}
	if message == "" {
		message = "terminal result was not successful"
	}
	return fmt.Errorf("%s failed: %s", operation, message)
}

func validateSignerFirstAdoptionTargets(targets []AdoptionTarget) error {
	if len(targets) == 0 {
		return fmt.Errorf("at least one target is required")
	}
	for _, target := range targets {
		if strings.TrimSpace(target.DockerHost) != "" {
			return fmt.Errorf("target docker_host is forbidden for signer-first adoption; use endpoint_ref")
		}
	}
	return nil
}

type directRuntimeActionEventRequest struct {
	Action        string `json:"action"`
	ServiceID     string `json:"service_id"`
	EnvironmentID string `json:"environment_id"`
	ArtifactID    string `json:"artifact_id,omitempty"`
}

type adoptionScanEventRequest struct {
	Targets []adoptionEventTarget `json:"targets"`
}

type adoptionImportEventRequest struct {
	Targets    []adoptionEventTarget    `json:"targets"`
	Selections []adoptionEventSelection `json:"selections,omitempty"`
	ImportAll  bool                     `json:"import_all,omitempty"`
	OrgID      string                   `json:"org_id,omitempty"`
}

type adoptionEventTarget struct {
	Name            string `json:"name"`
	EndpointRef     string `json:"endpoint_ref"`
	EnvironmentName string `json:"environment_name,omitempty"`
}

type adoptionEventSelection struct {
	TargetName          string `json:"target_name"`
	ContainerID         string `json:"container_id"`
	ServiceNameOverride string `json:"service_name_override,omitempty"`
}

func adoptionEventTargetsFromClient(targets []AdoptionTarget) []adoptionEventTarget {
	out := make([]adoptionEventTarget, 0, len(targets))
	for _, target := range targets {
		out = append(out, adoptionEventTarget{
			Name:            strings.TrimSpace(target.Name),
			EndpointRef:     strings.TrimSpace(target.EndpointRef),
			EnvironmentName: strings.TrimSpace(target.EnvironmentName),
		})
	}
	return out
}

func adoptionEventSelectionsFromClient(selections []AdoptionSelection) []adoptionEventSelection {
	out := make([]adoptionEventSelection, 0, len(selections))
	for _, selection := range selections {
		out = append(out, adoptionEventSelection{
			TargetName:          strings.TrimSpace(selection.TargetName),
			ContainerID:         strings.TrimSpace(selection.ContainerID),
			ServiceNameOverride: strings.TrimSpace(selection.ServiceNameOverride),
		})
	}
	return out
}

func adoptionRequestTags(operation string, targets []AdoptionTarget) nostr.Tags {
	tags := nostr.Tags{{"operation", operation}}
	for _, target := range targets {
		if name := strings.TrimSpace(target.Name); name != "" {
			tags = append(tags, nostr.Tag{"target", name})
		}
		if endpointRef := strings.TrimSpace(target.EndpointRef); endpointRef != "" {
			tags = append(tags, nostr.Tag{"endpoint_ref", endpointRef})
		}
		if environmentName := strings.TrimSpace(target.EnvironmentName); environmentName != "" {
			tags = append(tags, nostr.Tag{"environment_name", environmentName})
		}
	}
	return tags
}

func normalizeOperatorRelays(relays []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(relays))
	for _, relay := range relays {
		relay = strings.TrimSpace(relay)
		if relay == "" {
			continue
		}
		normalized := nostr.NormalizeURL(relay)
		if normalized == "" {
			normalized = relay
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}

func tagMap(tags nostr.Tags) map[string][]string {
	out := map[string][]string{}
	for _, tag := range tags {
		if len(tag) < 2 || tag[0] == "" {
			continue
		}
		out[tag[0]] = append(out[tag[0]], tag[1])
	}
	return out
}

func firstTagValue(tags nostr.Tags, name string) string {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == name {
			return tag[1]
		}
	}
	return ""
}

func tagHasValue(tags nostr.Tags, name, value string) bool {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == name && tag[1] == value {
			return true
		}
	}
	return false
}

func firstValue(values map[string][]string, name string) string {
	if len(values[name]) == 0 {
		return ""
	}
	return values[name][0]
}
