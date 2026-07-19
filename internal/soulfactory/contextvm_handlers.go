package soulfactory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
)

const (
	ContextVMMethodProvision = "soul-factory/provision"
	ContextVMMethodAction    = "soul-factory/action"
)

// RegisterContextVMHandlers exposes Soul Factory mutations on Bahia's canonical
// kind:25910 transport. The ContextVM response acknowledges dispatch only;
// provisioning progress and terminal truth remain relay-backed Soul Factory
// events until their canonical 30900 projection is enabled.
func RegisterContextVMHandlers(transport *controlplane.EncryptedRequestTransport, reactor *Reactor) {
	if transport == nil || reactor == nil {
		return
	}
	adapter := contextVMAdapter{reactor: reactor}
	transport.RegisterContextVMHandler(ContextVMMethodProvision, adapter.provision)
	transport.RegisterContextVMHandler(ContextVMMethodAction, adapter.action)
}

type contextVMAdapter struct {
	reactor *Reactor
}

func (a contextVMAdapter) provision(ctx context.Context, request controlplane.ContextVMRequest) (any, error) {
	event, err := contextVMProvisioningEvent(request)
	if err != nil {
		return nil, err
	}
	a.reactor.handleEvent(ctx, event)
	return contextVMAck(request, event), nil
}

func (a contextVMAdapter) action(ctx context.Context, request controlplane.ContextVMRequest) (any, error) {
	event, err := contextVMActionEvent(request)
	if err != nil {
		return nil, err
	}
	a.reactor.handleEvent(ctx, event)
	return contextVMAck(request, event), nil
}

func contextVMProvisioningEvent(request controlplane.ContextVMRequest) (*nostr.Event, error) {
	event, err := contextVMInteropEvent(request, domain.KindProvisioningRequest)
	if err != nil {
		return nil, err
	}
	if _, err := ParseProvisioningRequestEvent(event); err != nil {
		return nil, fmt.Errorf("invalid %s params: %w", ContextVMMethodProvision, err)
	}
	return event, nil
}

func contextVMActionEvent(request controlplane.ContextVMRequest) (*nostr.Event, error) {
	event, err := contextVMInteropEvent(request, domain.KindSoulAction)
	if err != nil {
		return nil, err
	}
	var params struct {
		SoulRef string                `json:"soul_ref"`
		Action  domain.SoulActionType `json:"action"`
	}
	if err := json.Unmarshal(request.RPC.Params, &params); err != nil {
		return nil, fmt.Errorf("decode %s params: %w", ContextVMMethodAction, err)
	}
	params.SoulRef = strings.TrimSpace(params.SoulRef)
	params.Action = domain.SoulActionType(strings.TrimSpace(string(params.Action)))
	if params.SoulRef != "" {
		event.Tags = append(event.Tags, nostr.Tag{tagSoul, params.SoulRef})
	}
	if params.Action != "" {
		event.Tags = append(event.Tags, nostr.Tag{tagAction, string(params.Action)})
	}
	if _, err := ParseSoulActionEvent(event); err != nil {
		return nil, fmt.Errorf("invalid %s params: %w", ContextVMMethodAction, err)
	}
	return event, nil
}

func contextVMInteropEvent(request controlplane.ContextVMRequest, kind int) (*nostr.Event, error) {
	if request.Event == nil {
		return nil, fmt.Errorf("ContextVM request event is required")
	}
	if !json.Valid(request.RPC.Params) {
		return nil, fmt.Errorf("ContextVM params must be valid JSON")
	}
	return &nostr.Event{
		ID:        request.Event.ID,
		PubKey:    request.Event.PubKey,
		CreatedAt: request.Event.CreatedAt,
		Kind:      nostr.Kind(kind),
		Content:   string(request.RPC.Params),
		Tags: nostr.Tags{
			{tagEvent, request.Event.ID.Hex(), "contextvm-request"},
			{"request-kind", fmt.Sprint(controlplane.KindContextVMMessage)},
			{"method", request.RPC.Method},
		},
	}, nil
}

func contextVMAck(request controlplane.ContextVMRequest, event *nostr.Event) map[string]any {
	return map[string]any{
		"status":           "accepted",
		"request_event_id": request.Event.ID.Hex(),
		"request_kind":     controlplane.KindContextVMMessage,
		"method":           request.RPC.Method,
		"interop_kind":     int(event.Kind),
	}
}
