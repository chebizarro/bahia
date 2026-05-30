package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	canonicalnostr "fiatjaf.com/nostr"
	"github.com/nbd-wtf/go-nostr"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"go.uber.org/zap"
)

// OperatorControlPlaneConfig configures the signer-first operator Nostr client.
type OperatorControlPlaneConfig struct {
	Relays             []string
	PrivateKey         string // 64-character hex or nsec input
	PublishWaitTimeout time.Duration
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
	Phase           string
	RequestAccepted bool
	PublishedRelays int
	Cause           error
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
	return fmt.Sprintf("%s: %v", phase, e.Cause)
}

func (e *ControlPlaneRequestError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

type operatorRelayTransport interface {
	Publish(context.Context, nostr.Event) (int, error)
	SubscribeAllWithEOSE(context.Context, []nostr.Filter) (*nostrpool.MergedSubscription, error)
	Close()
}

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

func (t *relayPoolOperatorTransport) SubscribeAllWithEOSE(ctx context.Context, filters []nostr.Filter) (*nostrpool.MergedSubscription, error) {
	t.ensureConnected(ctx)
	return t.pool.SubscribeAllWithEOSE(ctx, filters)
}

func (t *relayPoolOperatorTransport) Close() {
	if t != nil && t.pool != nil {
		t.pool.Close()
	}
}

// OperatorControlPlaneClient publishes signed operator request events and waits
// for correlated status/result replies over Nostr subscriptions.
type OperatorControlPlaneClient struct {
	relays     []string
	privateKey string
	signer     canonicalnostr.Signer
	pubkey     string
	transport  operatorRelayTransport
	timeout    time.Duration
}

// NewOperatorControlPlaneClient builds a signer-first operator control-plane client.
func NewOperatorControlPlaneClient(cfg OperatorControlPlaneConfig) (*OperatorControlPlaneClient, error) {
	privateKey, err := NormalizeNostrPrivateKey(cfg.PrivateKey)
	if err != nil {
		return nil, err
	}
	signer, err := controlplane.NewPrivateKeySigner(privateKey)
	if err != nil {
		return nil, err
	}
	if signer == nil {
		return nil, fmt.Errorf("nostr private key is required")
	}
	pubkey, err := nostr.GetPublicKey(privateKey)
	if err != nil {
		return nil, fmt.Errorf("derive Nostr public key: %w", err)
	}
	relays := normalizeOperatorRelays(cfg.Relays)
	if len(relays) == 0 {
		return nil, fmt.Errorf("at least one operator relay is required")
	}
	pool := nostrpool.NewRelayPool(relays, zap.NewNop(), nostrpool.WithPrivateKey(privateKey))
	timeout := cfg.PublishWaitTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &OperatorControlPlaneClient{
		relays:     relays,
		privateKey: privateKey,
		signer:     signer,
		pubkey:     pubkey,
		transport:  &relayPoolOperatorTransport{pool: pool},
		timeout:    timeout,
	}, nil
}

// Close releases relay resources owned by the client.
func (c *OperatorControlPlaneClient) Close() {
	if c != nil && c.transport != nil {
		c.transport.Close()
	}
}

// DeployServiceRuntimeNostr requests a direct runtime deploy over Nostr.
func (c *OperatorControlPlaneClient) DeployServiceRuntimeNostr(ctx context.Context, serviceID string, envID string, artifactID *string, onStatus func(OperatorStatusEvent)) (*RuntimeActionResult, error) {
	return c.runtimeAction(ctx, "deploy", serviceID, envID, artifactID, onStatus)
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
		Kind:       controlplane.KindAdoptionScanRequest,
		StatusKind: controlplane.KindAdoptionStatus,
		ResultKind: controlplane.KindAdoptionScanResult,
		Tags:       adoptionRequestTags("scan", req.Targets),
		Payload:    payload,
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
	}
	event, err := c.publishAndAwait(ctx, operatorRequest{
		Kind:       controlplane.KindAdoptionImportRequest,
		StatusKind: controlplane.KindAdoptionStatus,
		ResultKind: controlplane.KindAdoptionImportResult,
		Tags:       adoptionRequestTags("import", req.Targets),
		Payload:    payload,
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
		Kind:       controlplane.KindServiceAction,
		StatusKind: controlplane.KindActionStatus,
		ResultKind: controlplane.KindActionResult,
		Tags:       tags,
		Payload:    payload,
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
	Kind       int
	StatusKind int
	ResultKind int
	Tags       nostr.Tags
	Payload    any
}

func (c *OperatorControlPlaneClient) publishAndAwait(ctx context.Context, req operatorRequest, onStatus func(OperatorStatusEvent)) (*nostr.Event, error) {
	if c == nil || c.transport == nil || c.signer == nil || c.pubkey == "" {
		return nil, &ControlPlaneRequestError{Phase: "configure operator control-plane client", RequestAccepted: false, Cause: fmt.Errorf("operator control-plane client is not configured")}
	}
	content, err := json.Marshal(req.Payload)
	if err != nil {
		return nil, &ControlPlaneRequestError{Phase: "encode operator request", RequestAccepted: false, Cause: err}
	}
	tags := req.Tags
	if !tagHasValue(tags, "d", "") && firstTagValue(tags, "d") == "" {
		tags = append(nostr.Tags{{"d", deterministicOperatorIdempotencyKey(req.Kind, tags, content)}}, tags...)
	}
	event := &nostr.Event{Kind: req.Kind, CreatedAt: nostr.Now(), Tags: tags, Content: string(content)}
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	if err := controlplane.SignGoNostrEvent(ctx, c.signer, event); err != nil {
		return nil, &ControlPlaneRequestError{Phase: "sign operator request", RequestAccepted: false, Cause: err}
	}

	filters := []nostr.Filter{{
		Kinds: []int{req.StatusKind, req.ResultKind},
		Tags:  nostr.TagMap{"e": []string{event.ID}, "p": []string{c.pubkey}},
	}}
	sub, err := c.transport.SubscribeAllWithEOSE(ctx, filters)
	if err != nil {
		return nil, &ControlPlaneRequestError{Phase: "subscribe for operator replies", RequestAccepted: false, Cause: err}
	}

	published, err := c.transport.Publish(ctx, *event)
	if published == 0 {
		if err == nil {
			err = fmt.Errorf("request was not accepted by any relay")
		}
		return nil, &ControlPlaneRequestError{Phase: "publish operator request", RequestAccepted: false, PublishedRelays: published, Cause: err}
	}

	seen := map[string]struct{}{}
	eose := sub.EndOfStoredEvents
	for {
		select {
		case <-ctx.Done():
			return nil, &ControlPlaneRequestError{Phase: "await operator result", RequestAccepted: true, PublishedRelays: published, Cause: ctx.Err()}
		case <-eose:
			eose = nil
		case reply, ok := <-sub.Events:
			if !ok {
				return nil, &ControlPlaneRequestError{Phase: "await operator result", RequestAccepted: true, PublishedRelays: published, Cause: fmt.Errorf("reply subscription closed before terminal result")}
			}
			if reply == nil || !validSignedEvent(reply) || !correlatesTo(reply, event.ID, c.pubkey) {
				continue
			}
			if _, duplicate := seen[reply.ID]; duplicate {
				continue
			}
			seen[reply.ID] = struct{}{}
			switch reply.Kind {
			case req.StatusKind:
				if onStatus != nil {
					onStatus(statusEventFromNostr(reply))
				}
			case req.ResultKind:
				return reply, nil
			}
		}
	}
}

func deterministicOperatorIdempotencyKey(kind int, tags nostr.Tags, content []byte) string {
	h := sha256.New()
	_, _ = h.Write([]byte(fmt.Sprintf("operator:%d", kind)))
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] != "d" {
			_, _ = h.Write([]byte{0})
			_, _ = h.Write([]byte(strings.Join(tag, "=")))
		}
	}
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(content)
	return fmt.Sprintf("operator:%d:%s", kind, hex.EncodeToString(h.Sum(nil))[:24])
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
	ok, err := event.CheckSignature()
	return err == nil && ok
}

func correlatesTo(event *nostr.Event, requestID, pubkey string) bool {
	return tagHasValue(event.Tags, "e", requestID) && tagHasValue(event.Tags, "p", pubkey)
}

func statusEventFromNostr(event *nostr.Event) OperatorStatusEvent {
	tags := tagMap(event.Tags)
	return OperatorStatusEvent{
		Kind:      event.Kind,
		EventID:   event.ID,
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
