package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// AssistantContextVMRequestEvidenceResolver proves that one operator-named
// ContextVM request event is the submission of an uncertain work item. It
// fetches exactly that event (ID, kind and author scoped) through EOSE and
// requires: a valid ID and signature from the command-signing identity, the
// executor-issued idempotency key as its d tag, a JSON-RPC method the tool
// publishes, and params equal to every effective work argument. Missing or
// mismatched evidence is an error; absence never proves non-submission.
type AssistantContextVMRequestEvidenceResolver struct {
	Subscriber AssistantRelaySubscriber
	// RequestAuthor is the hex pubkey that signs assistant ContextVM commands.
	RequestAuthor string
	// Methods maps tool names to accepted JSON-RPC methods; production wiring
	// uses mcp.AssistantAsyncToolRequestMethods.
	Methods map[string][]string
}

var _ AssistantRequestEvidenceResolver = (*AssistantContextVMRequestEvidenceResolver)(nil)

// Arguments carried in tags or used as the dispatch key rather than params.
var assistantEvidenceNonParamArgs = map[string]bool{"idempotency_key": true, "request_id": true, "d": true, "tags": true}

func (r *AssistantContextVMRequestEvidenceResolver) ResolveAssistantRequestEvidence(ctx context.Context, requestEventID string, x domain.AssistantExecution, w domain.AssistantWorkItem) (*domain.AsyncToolReceipt, error) {
	if r == nil || r.Subscriber == nil || strings.TrimSpace(r.RequestAuthor) == "" {
		return nil, errors.New("request evidence resolver is not configured")
	}
	methods := r.Methods[w.ToolName]
	if len(methods) == 0 {
		return nil, fmt.Errorf("tool %q has no reconcilable request method", w.ToolName)
	}
	id, err := nostr.IDFromHex(strings.TrimSpace(requestEventID))
	if err != nil {
		return nil, fmt.Errorf("request event id: %w", err)
	}
	author, err := nostr.PubKeyFromHex(strings.TrimSpace(r.RequestAuthor))
	if err != nil {
		return nil, fmt.Errorf("request author: %w", err)
	}
	ev, err := r.fetch(ctx, id, author)
	if err != nil {
		return nil, err
	}
	key := assistantExpectedIdempotencyKey(x, w)
	if tagValue(ev.Tags, "d") != key {
		return nil, errors.New("request event idempotency key does not match work item")
	}
	method := tagValue(ev.Tags, "method")
	allowed := false
	for _, m := range methods {
		allowed = allowed || m == method
	}
	if !allowed {
		return nil, fmt.Errorf("request method %q is not published by tool %s", method, w.ToolName)
	}
	var rpc struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err = json.Unmarshal([]byte(ev.Content), &rpc); err != nil || rpc.Method != method {
		return nil, errors.New("request event is not the expected JSON-RPC request")
	}
	params := map[string]any{}
	decoder := json.NewDecoder(bytes.NewReader(rpc.Params))
	decoder.UseNumber()
	if err = decoder.Decode(&params); err != nil {
		return nil, errors.New("request params are not an object")
	}
	for name, value := range w.Arguments {
		if assistantEvidenceNonParamArgs[name] {
			continue
		}
		if !assistantEvidenceValueEqual(value, params[name]) {
			return nil, fmt.Errorf("request param %q differs from the effective work argument", name)
		}
	}
	return &domain.AsyncToolReceipt{ToolName: w.ToolName, RequestEventID: ev.ID.Hex(), RequestKind: int(kinds.ContextVMMessage), ResultKinds: []int{int(kinds.ContextVMMessage)}, DTag: key, IdempotencyKey: key}, nil
}

func (r *AssistantContextVMRequestEvidenceResolver) fetch(ctx context.Context, id nostr.ID, author nostr.PubKey) (*nostr.Event, error) {
	sub, err := r.Subscriber.SubscribeAllWithEOSE(ctx, []nostr.Filter{{IDs: []nostr.ID{id}, Kinds: []nostr.Kind{nostr.Kind(kinds.ContextVMMessage)}, Authors: []nostr.PubKey{author}}})
	if err != nil {
		return nil, err
	}
	defer sub.Close()
	events, closed, eose := sub.EventChan(), sub.ClosedChan(), sub.EOSEChan()
	var found *nostr.Event
	notFound := errors.New("request event not found at EOSE; absence does not prove the request was not submitted")
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case c, ok := <-closed:
			if !ok {
				closed = nil
				continue
			}
			return nil, fmt.Errorf("request evidence subscription closed by %s: %s", c.RelayURL, c.Reason)
		case ev, ok := <-events:
			if !ok {
				if found != nil && assistantEOSEReached(eose) {
					return found, nil
				}
				if assistantEOSEReached(eose) {
					return nil, notFound
				}
				return nil, errors.New("request evidence subscription ended before EOSE")
			}
			if ev != nil && ev.ID == id && ev.PubKey == author && ev.Kind == nostr.Kind(kinds.ContextVMMessage) && ev.CheckID() && ev.VerifySignature() {
				copyEv := *ev
				found = &copyEv
			}
		case <-eose:
			if found == nil {
				return nil, notFound
			}
			return found, nil
		}
	}
}

// assistantEvidenceValueEqual compares values by RFC 8785 canonical form, so
// number spelling and key order do not matter. An omitted param equals an
// empty argument because publishers omit empty optional fields.
func assistantEvidenceValueEqual(expected, actual any) bool {
	if actual == nil {
		s, isString := expected.(string)
		return expected == nil || (isString && strings.TrimSpace(s) == "")
	}
	left, errL := domain.ComputeAssistantArgumentsDigest(map[string]any{"v": expected})
	right, errR := domain.ComputeAssistantArgumentsDigest(map[string]any{"v": actual})
	return errL == nil && errR == nil && left == right
}
