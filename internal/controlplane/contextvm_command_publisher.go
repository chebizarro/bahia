package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"fiatjaf.com/nostr"
	canonicalnostr "fiatjaf.com/nostr"
	cascontextvm "git.sharegap.net/cascadia/cascadia-go/contextvm"
	casnostr "git.sharegap.net/cascadia/cascadia-go/nostr"
)

func publishContextVMCommand(ctx context.Context, publisher NostrEventPublisher, signer canonicalnostr.Signer, method, dTag, agentID string, tags nostr.Tags, params map[string]any, label string) (*nostr.Event, int, string, error) {
	if publisher == nil {
		return nil, 0, "", fmt.Errorf("%s publisher is not configured", label)
	}
	ev, dTag, err := buildContextVMCommand(ctx, signer, method, dTag, agentID, tags, params, label)
	if err != nil {
		return nil, 0, dTag, err
	}
	published, err := publisher.Publish(ctx, *ev)
	if err != nil {
		return ev, published, dTag, fmt.Errorf("publish %s ContextVM request: %w", label, err)
	}
	if published == 0 {
		return ev, published, dTag, fmt.Errorf("publish %s ContextVM request: no relay accepted the request; retry after relay reconnect", label)
	}
	return ev, published, dTag, nil
}

func publishContextVMCommandNIP59(ctx context.Context, publisher NostrEventPublisher, signer canonicalnostr.Signer, recipientPubkey, method, dTag, agentID string, tags nostr.Tags, params map[string]any, label string) (*nostr.Event, int, string, error) {
	if publisher == nil {
		return nil, 0, "", fmt.Errorf("%s publisher is not configured", label)
	}
	inner, dTag, err := buildContextVMCommand(ctx, signer, method, dTag, agentID, tags, params, label)
	if err != nil {
		return nil, 0, dTag, err
	}
	envelopeSigner, ok := signer.(casnostr.Signer)
	if !ok {
		return nil, 0, dTag, fmt.Errorf("wrap %s ContextVM request with NIP-59: signer does not support encryption", label)
	}
	outer, rumor, err := cascontextvm.WrapEventNIP59(ctx, envelopeSigner, recipientPubkey, inner, cascontextvm.StoredGiftWrap)
	if err != nil {
		return nil, 0, dTag, fmt.Errorf("wrap %s ContextVM request with NIP-59: %w", label, err)
	}
	published, err := publisher.Publish(ctx, *outer)
	if err != nil {
		return rumor, published, dTag, fmt.Errorf("publish %s NIP-59 ContextVM request: %w", label, err)
	}
	if published == 0 {
		return rumor, published, dTag, fmt.Errorf("publish %s NIP-59 ContextVM request: no relay accepted the request; retry after relay reconnect", label)
	}
	return rumor, published, dTag, nil
}

func buildContextVMCommand(ctx context.Context, signer canonicalnostr.Signer, method, dTag, agentID string, tags nostr.Tags, params map[string]any, label string) (*nostr.Event, string, error) {
	method = strings.TrimSpace(method)
	if method == "" {
		return nil, "", fmt.Errorf("%s ContextVM method is required", label)
	}
	dTag = strings.TrimSpace(dTag)
	if dTag == "" {
		return nil, "", fmt.Errorf("%s idempotency key is required", label)
	}
	params = cloneContextVMParams(params)
	meta, _ := params["_meta"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
	}
	meta["progressToken"] = dTag
	params["_meta"] = meta
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return nil, dTag, fmt.Errorf("marshal %s ContextVM params: %w", label, err)
	}
	rpc := ContextVMJSONRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(strconv.Quote(dTag)),
		Method:  method,
		Params:  paramsJSON,
	}
	contentJSON, err := json.Marshal(rpc)
	if err != nil {
		return nil, dTag, fmt.Errorf("marshal %s ContextVM request: %w", label, err)
	}
	eventTags := nostr.Tags{{"d", dTag}, {"method", method}, {ContextVMRoutingTag, ContextVMWireVersion}}
	eventTags = append(eventTags, compactTags(tags)...)
	if agentID = strings.TrimSpace(agentID); agentID != "" {
		eventTags = append(eventTags, nostr.Tag{"agent", agentID})
	}
	ev := &nostr.Event{Kind: KindContextVMMessage, CreatedAt: nostr.Now(), Tags: eventTags, Content: string(contentJSON)}
	if err := SignGoNostrEvent(ctx, signer, ev); err != nil {
		return nil, dTag, fmt.Errorf("sign %s ContextVM request: %w", label, err)
	}
	return ev, dTag, nil
}

func cloneContextVMParams(in map[string]any) map[string]any {
	out := make(map[string]any, len(in)+1)
	for k, v := range in {
		if strings.TrimSpace(k) == "" {
			continue
		}
		out[k] = v
	}
	return out
}
