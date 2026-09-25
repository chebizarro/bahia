package controlplane

import (
	"context"
	"fmt"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	loomadapter "github.com/openagentsinc/bahia/internal/adapters/loom"
)

type loomJobClient interface {
	SubmitJob(ctx context.Context, job loomadapter.JobRequest) (string, error)
	CancelJob(ctx context.Context, jobEventID string, workerPubkey string) error
	RememberJobSubmitter(jobEventID, submitter string)
	JobSubmitter(jobEventID string) string
	CanonicalProjectionReady() bool
	StartCanonicalProjection(jobEventID string)
}

var _ loomJobClient = (*loomadapter.Client)(nil)

// RegisterLoomContextVMHandlers exposes Bahia's canonical ContextVM Loom methods.
// The adapter boundary is ContextVM kind 25910 using loom/submit and loom/cancel;
// this handler maps those intents to Loom's native 5100/5102 protocol through the
// existing Loom client. Durable job visibility is emitted by the Loom projection
// helpers as 30900 loom-job:<id> state plus 4903 audit events.
//
// Submission requires the global operator allowlist. Cancellation permits the
// recorded submitter or an explicitly configured Loom operator; missing
// ownership fails closed. Transport admission alone does not grant cancellation.
func RegisterLoomContextVMHandlers(transport *EncryptedRequestTransport, client loomJobClient, fleetOperatorPubkeys []string, gate *FleetOperatorGate) {
	if transport == nil || transport.responder == nil || client == nil {
		return
	}
	h := loomContextVMHandlers{
		client:               client,
		servicePubkey:        normalizeEncryptedPubkey(transport.responder.ServicePubkey()),
		fleetOperatorPubkeys: append([]string(nil), fleetOperatorPubkeys...),
	}
	transport.RegisterOperatorContextVMHandler(ContextVMMethodLoomSubmit, h.submit, gate)
	transport.RegisterContextVMHandler(ContextVMMethodLoomCancel, h.cancel)
}

type loomContextVMHandlers struct {
	client               loomJobClient
	fleetOperatorPubkeys []string
	servicePubkey        string
}

type loomSubmitContextVMPayload struct {
	ID                   string            `json:"id,omitempty"`
	Type                 string            `json:"type,omitempty"`
	Image                string            `json:"image,omitempty"`
	Digest               string            `json:"digest,omitempty"`
	Environment          string            `json:"environment,omitempty"`
	Service              string            `json:"service,omitempty"`
	WorkerPubkey         string            `json:"worker_pubkey,omitempty"`
	Cmd                  string            `json:"cmd,omitempty"`
	Args                 []string          `json:"args,omitempty"`
	Env                  map[string]string `json:"env,omitempty"`
	Secrets              map[string]string `json:"secrets,omitempty"`
	Params               map[string]string `json:"params,omitempty"`
	PaymentToken         string            `json:"payment_token,omitempty"`
	TimeoutSeconds       int               `json:"timeout_seconds,omitempty"`
	RequiredSoftware     []string          `json:"required_software,omitempty"`
	RequiredArchitecture string            `json:"required_architecture,omitempty"`
	AllowedWorkerPubkeys []string          `json:"allowed_worker_pubkeys,omitempty"`
	AgentID              string            `json:"agent_id,omitempty"`
}

type loomCancelContextVMPayload struct {
	JobEventID   string `json:"job_event_id,omitempty"`
	JobID        string `json:"job_id,omitempty"`
	WorkerPubkey string `json:"worker_pubkey,omitempty"`
	AgentID      string `json:"agent_id,omitempty"`
}

func (h loomContextVMHandlers) submit(ctx context.Context, request ContextVMRequest) (any, error) {
	submitterPubkey, err := h.requester(request)
	if err != nil {
		return nil, err
	}
	if !h.client.CanonicalProjectionReady() {
		return nil, fmt.Errorf("canonical Loom projection is not configured")
	}
	var payload loomSubmitContextVMPayload
	if err := decodeContextVMParams(request.RPC.Params, &payload); err != nil {
		return nil, fmt.Errorf("invalid loom submit params: %w", err)
	}
	if err := validateLoomSubmitPayload(payload); err != nil {
		return nil, err
	}
	job := loomadapter.JobRequest{
		ID:                   strings.TrimSpace(payload.ID),
		Type:                 strings.TrimSpace(payload.Type),
		Image:                strings.TrimSpace(payload.Image),
		Digest:               strings.TrimSpace(payload.Digest),
		Environment:          strings.TrimSpace(payload.Environment),
		Service:              strings.TrimSpace(payload.Service),
		WorkerPubkey:         strings.TrimSpace(payload.WorkerPubkey),
		Cmd:                  strings.TrimSpace(payload.Cmd),
		Args:                 append([]string(nil), payload.Args...),
		Env:                  payload.Env,
		Params:               payload.Params,
		PaymentToken:         strings.TrimSpace(payload.PaymentToken),
		RequiredSoftware:     append([]string(nil), payload.RequiredSoftware...),
		RequiredArchitecture: strings.TrimSpace(payload.RequiredArchitecture),
		AllowedWorkerPubkeys: append([]string(nil), payload.AllowedWorkerPubkeys...),
	}
	if payload.TimeoutSeconds > 0 {
		job.Timeout = time.Duration(payload.TimeoutSeconds) * time.Second
	}
	jobID, err := h.client.SubmitJob(ctx, job)
	if err != nil {
		return nil, err
	}
	h.client.RememberJobSubmitter(jobID, submitterPubkey)
	h.client.StartCanonicalProjection(jobID)

	return map[string]any{
		"status":       "accepted",
		"schema":       ContextVMLoomSchema,
		"method":       ContextVMMethodLoomSubmit,
		"job_event_id": jobID,
		"state_d_tag":  "loom-job:" + jobID,
	}, nil
}

func validateLoomSubmitPayload(payload loomSubmitContextVMPayload) error {
	if len(payload.Secrets) > 0 || strings.TrimSpace(payload.PaymentToken) != "" {
		return fmt.Errorf("plaintext Loom secrets are forbidden; secret references are not yet supported")
	}
	if loomPayloadContainsBunkerURL(payload) {
		return fmt.Errorf("secret-bearing bunker URLs are forbidden in Loom events and command arguments")
	}
	return nil
}

func loomPayloadContainsBunkerURL(payload loomSubmitContextVMPayload) bool {
	values := []string{payload.Cmd}
	values = append(values, payload.Args...)
	for _, value := range payload.Env {
		values = append(values, value)
	}
	for _, value := range payload.Params {
		values = append(values, value)
	}
	for _, value := range values {
		normalized := strings.ToLower(strings.TrimSpace(value))
		if strings.Contains(normalized, "bunker://") || strings.Contains(normalized, "nostrconnect://") {
			return true
		}
	}
	return false
}

func (h loomContextVMHandlers) cancel(ctx context.Context, request ContextVMRequest) (any, error) {
	callerPubkey, err := h.requester(request)
	if err != nil {
		return nil, err
	}
	var payload loomCancelContextVMPayload
	if err := decodeContextVMParams(request.RPC.Params, &payload); err != nil {
		return nil, fmt.Errorf("invalid loom cancel params: %w", err)
	}
	jobID := strings.TrimSpace(payload.JobEventID)
	if jobID == "" {
		jobID = strings.TrimSpace(payload.JobID)
	}
	if jobID == "" {
		return nil, fmt.Errorf("job_event_id is required")
	}

	submitterPubkey := h.client.JobSubmitter(jobID)
	if !authorizedForLoomCancel(callerPubkey, submitterPubkey, h.fleetOperatorPubkeys) {
		return nil, fmt.Errorf("caller not authorized to cancel loom job")
	}

	if err := h.client.CancelJob(ctx, jobID, strings.TrimSpace(payload.WorkerPubkey)); err != nil {
		return nil, err
	}
	return map[string]any{
		"status":       "accepted",
		"schema":       ContextVMLoomSchema,
		"method":       ContextVMMethodLoomCancel,
		"job_event_id": jobID,
		"state_d_tag":  "loom-job:" + jobID,
	}, nil
}

func (h loomContextVMHandlers) requester(request ContextVMRequest) (string, error) {
	if request.Event == nil || request.Event.PubKey == (nostr.PubKey{}) {
		return "", fmt.Errorf("signed Loom requester is required")
	}
	if h.servicePubkey == "" {
		return "", fmt.Errorf("loom service identity is not configured")
	}
	requester := request.Event.PubKey.Hex()
	if requester == h.servicePubkey {
		return "", fmt.Errorf("service signer cannot supply Loom requester authority")
	}
	return requester, nil
}

func authorizedForLoomCancel(callerPubkey, jobSubmitter string, fleetOperatorPubkeys []string) bool {
	callerPubkey = strings.TrimSpace(callerPubkey)
	if callerPubkey == "" {
		return false
	}
	jobSubmitter = strings.TrimSpace(jobSubmitter)
	if jobSubmitter == "" {
		return false
	}
	if callerPubkey == jobSubmitter {
		return true
	}
	for _, op := range fleetOperatorPubkeys {
		if callerPubkey == strings.TrimSpace(op) {
			return true
		}
	}
	return false
}
