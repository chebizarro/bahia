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
	cascontextvm "git.sharegap.net/cascadia/cascadia-go/contextvm"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"go.uber.org/zap"
)

const (
	DefaultOperatorResultTimeout = 30 * time.Second
	DefaultOperatorResultRetries = 2
	operatorActivationTimeout    = 3 * time.Second
)

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
// NIP-42 is the transport's concern: the default transport's RelayPool answers
// a relay's AUTH challenge and reissues the REQ that got "auth-required:" on
// that relay, so a CLOSED it delivers means the relay is out for this request.
type ContextVMRelayTransport interface {
	Publish(context.Context, nostr.Event) (int, error)
	PublishWithResults(context.Context, nostr.Event) ([]ContextVMPublishResult, error)
	SubscribeOperator(context.Context, []nostr.Filter) (*ContextVMSubscription, error)
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

func (t *relayPoolOperatorTransport) Close() {
	if t != nil && t.pool != nil {
		t.pool.Close()
	}
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

// maxStoredGiftWrapContentBytes is the largest event content Bahia's relay
// sidecar stores (NIP-11 limitation.max_content_length).
const maxStoredGiftWrapContentBytes = 65535

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
	if err == nil && len(outer.Content) > maxStoredGiftWrapContentBytes {
		// Bahia's relay stores at most maxStoredGiftWrapContentBytes of
		// content, so a larger request travels as an ephemeral 21059 wrap:
		// relayed live to the daemon, never stored. The reply filter below
		// already covers both wrap kinds.
		outer, rumor, err = cascontextvm.WrapEventNIP59(ctx, nip59KeyerAdapter{c.cipher}, c.servicePubkey, inner, cascontextvm.EphemeralGiftWrap)
	}
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

func (c *ContextVMRequestClient) waitForOperatorSubscriptionActivation(ctx context.Context, sub *operatorSubscription, timeout time.Duration) (*operatorSubscription, error) {
	activationCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	current := sub
	active := operatorRelaySet(current.RelayURLs())
	eosed := map[string]struct{}{}
	closedRelays := map[string]string{}
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

func (c *ContextVMRequestClient) awaitOperatorResult(ctx context.Context, sub *operatorSubscription, inner *nostr.Event, outerRequestIDs []string, requestID string, onStatus func(OperatorStatusEvent)) (*nostr.Event, error) {
	seen := map[string]struct{}{}
	pendingRelays := sub.RelayURLs()
	closedRelays := map[string]string{}
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
				if rpc.Error.Code == controlplane.ContextVMDuplicateRequestErrorCode {
					message += fmt.Sprintf("; retry with --idempotency-key %s, or choose a new key to re-execute", requestID)
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

// ContextVMRequestConfig configures a generic outbound ContextVM request client.
// Configure exactly one of Relays or Transport. A relay-backed client owns the
// pool it creates; an injected Transport remains owned by the caller.
type ContextVMRequestConfig struct {
	Relays          []string
	Transport       ContextVMRelayTransport
	Signer          nostr.Signer
	SenderPubkey    string
	RecipientPubkey string
	Encrypted       bool
	ResultTimeout   time.Duration
	ResultRetries   *int
}

// ContextVMRequestOption customizes optional request-client integrations.
type ContextVMRequestOption func(*contextVMRequestOptions)

type contextVMRequestOptions struct {
	logger *zap.Logger
}

// WithContextVMRequestLogger surfaces relay warnings through the supplied logger.
// The library default remains quiet.
func WithContextVMRequestLogger(logger *zap.Logger) ContextVMRequestOption {
	return func(options *contextVMRequestOptions) {
		if logger != nil {
			options.logger = logger
		}
	}
}

// ContextVMRequestClient publishes signed JSON-RPC 2.0 ContextVM requests and
// waits for correlated terminal responses.
type ContextVMRequestClient struct {
	relays            []string
	signer            nostr.Signer
	cipher            contextVMCipherSigner
	pubkey            string
	transport         ContextVMRelayTransport
	servicePubkey     string
	encrypted         bool
	resultTimeout     time.Duration
	resultRetries     int
	activationTimeout time.Duration
	ownsTransport     bool
}

// NewContextVMRequestClient constructs a generic ContextVM request client.
// When configured with Relays, Close closes the internally-created relay pool.
// When configured with Transport, Close does not close the injected transport.
func NewContextVMRequestClient(cfg ContextVMRequestConfig, clientOptions ...ContextVMRequestOption) (*ContextVMRequestClient, error) {
	options := contextVMRequestOptions{logger: zap.NewNop()}
	for _, apply := range clientOptions {
		if apply != nil {
			apply(&options)
		}
	}
	if cfg.Signer == nil {
		return nil, fmt.Errorf("ContextVM request signer is required")
	}
	senderPubkey := strings.TrimSpace(cfg.SenderPubkey)
	if len(senderPubkey) != 64 {
		return nil, fmt.Errorf("ContextVM sender pubkey must be a 64-character hex pubkey")
	}
	if _, err := nostr.PubKeyFromHex(senderPubkey); err != nil {
		return nil, fmt.Errorf("parse ContextVM sender pubkey: %w", err)
	}
	recipientPubkey := strings.TrimSpace(cfg.RecipientPubkey)
	if len(recipientPubkey) != 64 {
		return nil, fmt.Errorf("ContextVM recipient pubkey must be a 64-character hex pubkey")
	}
	if _, err := nostr.PubKeyFromHex(recipientPubkey); err != nil {
		return nil, fmt.Errorf("parse ContextVM recipient pubkey: %w", err)
	}
	var cipherSigner contextVMCipherSigner
	if cfg.Encrypted {
		var ok bool
		cipherSigner, ok = cfg.Signer.(contextVMCipherSigner)
		if !ok {
			return nil, fmt.Errorf("encrypted ContextVM requests require a signer with NIP-44 encrypt and decrypt support")
		}
	}
	relays := normalizeOperatorRelays(cfg.Relays)
	if cfg.Transport != nil && len(relays) > 0 {
		return nil, fmt.Errorf("configure either ContextVM relays or an injected transport, not both")
	}
	if cfg.Transport == nil && len(relays) == 0 {
		return nil, fmt.Errorf("ContextVM relays or an injected transport are required")
	}
	resultTimeout := cfg.ResultTimeout
	if resultTimeout == 0 {
		resultTimeout = DefaultOperatorResultTimeout
	}
	if resultTimeout < 0 {
		return nil, fmt.Errorf("ContextVM result timeout must be positive")
	}
	resultRetries := DefaultOperatorResultRetries
	if cfg.ResultRetries != nil {
		resultRetries = *cfg.ResultRetries
	}
	if resultRetries < 0 {
		return nil, fmt.Errorf("ContextVM result retries cannot be negative")
	}
	transport := cfg.Transport
	ownsTransport := false
	if transport == nil {
		pool := nostrpool.NewRelayPool(relays, options.logger, nostrpool.WithAuthSigner(cfg.Signer))
		transport = &relayPoolOperatorTransport{pool: pool}
		ownsTransport = true
	}
	return &ContextVMRequestClient{
		relays:            relays,
		signer:            cfg.Signer,
		cipher:            cipherSigner,
		pubkey:            senderPubkey,
		transport:         transport,
		servicePubkey:     recipientPubkey,
		encrypted:         cfg.Encrypted,
		resultTimeout:     resultTimeout,
		resultRetries:     resultRetries,
		activationTimeout: operatorActivationTimeout,
		ownsTransport:     ownsTransport,
	}, nil
}

// Close releases an internally-created relay pool. Injected transports are not
// closed because their lifecycle remains owned by the caller.
func (c *ContextVMRequestClient) Close() {
	if c != nil && c.ownsTransport && c.transport != nil {
		c.transport.Close()
	}
}

// Request publishes a signed ContextVM request and waits for its correlated
// terminal result. Progress results are delivered to onStatus.
func (c *ContextVMRequestClient) Request(ctx context.Context, method string, params any, tags nostr.Tags, onStatus func(OperatorStatusEvent)) (*nostr.Event, error) {
	if c == nil || c.transport == nil || c.signer == nil || c.pubkey == "" {
		return nil, &ControlPlaneRequestError{Phase: "configure operator control-plane client", RequestAccepted: false, Cause: fmt.Errorf("ContextVM request client is not configured")}
	}
	method = strings.TrimSpace(method)
	if method == "" {
		return nil, &ControlPlaneRequestError{Phase: "encode operator ContextVM request", RequestAccepted: false, Cause: fmt.Errorf("ContextVM method is required")}
	}
	if c.encrypted && (c.cipher == nil || c.servicePubkey == "") {
		return nil, &ControlPlaneRequestError{Phase: "configure encrypted operator control-plane client", RequestAccepted: false, Cause: fmt.Errorf("encrypted ContextVM requests require recipient pubkey and NIP-44 signer support")}
	}
	payloadContent, err := json.Marshal(params)
	if err != nil {
		return nil, &ControlPlaneRequestError{Phase: "encode operator ContextVM params", RequestAccepted: false, Cause: err}
	}
	tags = append(nostr.Tags(nil), tags...)
	requestID := firstTagValue(tags, "d")
	if requestID == "" {
		requestID = deterministicOperatorIdempotencyKey(method, tags, payloadContent)
		tags = append(nostr.Tags{{"d", requestID}}, tags...)
	}
	rpcParams, err := contextVMParams(params, requestID)
	if err != nil {
		return nil, &ControlPlaneRequestError{Phase: "encode operator ContextVM params", RequestAccepted: false, Cause: err}
	}
	tags = append(tags, nostr.Tag{"method", method}, nostr.Tag{controlplane.ContextVMRoutingTag, controlplane.ContextVMWireVersion})
	if c.servicePubkey != "" {
		tags = append(tags, nostr.Tag{"p", c.servicePubkey})
	}
	rpc := contextVMRPCRequest{JSONRPC: "2.0", ID: requestID, Method: method, Params: rpcParams}
	content, err := json.Marshal(rpc)
	if err != nil {
		return nil, &ControlPlaneRequestError{Phase: "encode operator ContextVM request", RequestAccepted: false, Cause: err}
	}
	inner := &nostr.Event{Kind: nostr.Kind(controlplane.KindContextVMMessage), CreatedAt: nostr.Now(), Tags: tags, Content: string(content)}
	if err := controlplane.SignGoNostrEvent(ctx, c.signer, inner); err != nil {
		return nil, &ControlPlaneRequestError{Phase: "sign operator ContextVM request", RequestAccepted: false, Cause: err}
	}

	attempts := c.resultRetries + 1
	if attempts < 1 {
		attempts = 1
	}
	resultTimeout := c.resultTimeout
	if resultTimeout <= 0 {
		resultTimeout = DefaultOperatorResultTimeout
	}
	activationTimeout := c.activationTimeout
	if activationTimeout <= 0 {
		activationTimeout = operatorActivationTimeout
	}
	var (
		everAccepted    bool
		publishedRelays int
		publishResults  []OperatorPublishResult
		subscribed      []string
		failed          []string
		requestEventID  = inner.ID.Hex()
		outerRequestIDs []string
	)
	requestError := func(phase string, attempt int, cause error) *ControlPlaneRequestError {
		return &ControlPlaneRequestError{
			Phase:               phase,
			RequestAccepted:     everAccepted,
			PublishedRelays:     publishedRelays,
			ConfiguredRelays:    append([]string(nil), c.relays...),
			SubscribedRelays:    append([]string(nil), subscribed...),
			FailedSubscriptions: append([]string(nil), failed...),
			RequestEventID:      requestEventID,
			RequestDTag:         requestID,
			RequestMethod:       method,
			AttemptsMade:        attempt,
			PublishResults:      append([]OperatorPublishResult(nil), publishResults...),
			Cause:               cause,
		}
	}

	for attempt := 1; attempt <= attempts; attempt++ {
		publishEvent, filters, attemptOuterIDs, prepareErr := c.prepareOperatorAttempt(ctx, inner, outerRequestIDs)
		if prepareErr != nil {
			phase := "prepare operator ContextVM request"
			if c.encrypted {
				phase = "wrap encrypted operator ContextVM request"
			}
			return nil, requestError(phase, attempt, prepareErr)
		}
		outerRequestIDs = attemptOuterIDs
		requestEventID = publishEvent.ID.Hex()
		sub, subErr := c.transport.SubscribeOperator(ctx, filters)
		if subErr != nil {
			subscribed = nil
			failed = append([]string(nil), c.relays...)
			return nil, requestError("subscribe for operator ContextVM replies", attempt, subErr)
		}
		subscribed = sub.RelayURLs()
		failed = operatorFailedSubscriptions(c.relays, subscribed)
		if len(subscribed) == 0 {
			sub.Close()
			return nil, requestError("subscribe for operator ContextVM replies", attempt, fmt.Errorf("no configured relay established a reply subscription"))
		}
		activatedSub, activationErr := c.waitForOperatorSubscriptionActivation(ctx, sub, activationTimeout)
		if activationErr != nil {
			activatedSub.Close()
			return nil, requestError("activate operator ContextVM reply subscription", attempt, activationErr)
		}
		sub = activatedSub
		subscribed = sub.RelayURLs()
		failed = operatorFailedSubscriptions(c.relays, subscribed)

		published, attemptResults, publishErr := c.publishOperatorEvent(ctx, *publishEvent)
		publishedRelays = published
		publishResults = append(publishResults, attemptResults...)
		if published == 0 {
			sub.Close()
			if publishErr == nil {
				publishErr = fmt.Errorf("request was not accepted by any relay")
			}
			return nil, requestError("publish operator ContextVM request", attempt, publishErr)
		}
		everAccepted = true

		attemptCtx, cancelAttempt := context.WithTimeout(ctx, resultTimeout)
		result, awaitErr := c.awaitOperatorResult(attemptCtx, sub, inner, outerRequestIDs, requestID, onStatus)
		cancelAttempt()
		sub.Close()
		if awaitErr == nil {
			return result, nil
		}
		if errors.Is(awaitErr, context.DeadlineExceeded) && ctx.Err() == nil && attempt < attempts {
			continue
		}
		return nil, requestError("await operator ContextVM result", attempt, awaitErr)
	}
	return nil, requestError("await operator ContextVM result", attempts, fmt.Errorf("result retry attempts exhausted"))
}
