package controlplane

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	canonicalnostr "fiatjaf.com/nostr"
	"github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/relayadmin"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/kinds"
	"go.uber.org/zap"
)

const (
	ContextVMMethodRelayPolicyGet   = "settings/relay-policy.get"
	ContextVMMethodRelayPolicyApply = "settings/relay-policy.apply"
	ContextVMMethodRelayAdminCall   = "settings/relay-admin.call"

	RelaySettingsSchema = "bahia.relay-settings.v1"
	RelaySettingsDomain = "relay-settings"
	RelaySettingsDTag   = "relay-settings:operator"
)

type RelayAdminCaller interface {
	Call(ctx context.Context, targetRef, method string, params []any) (*relayadmin.Response, error)
	SupportedMethods(ctx context.Context, targetRef string) ([]string, error)
}

type RelaySettingsHandlerConfig struct {
	Config      *config.Config
	AdminClient RelayAdminCaller
	Logger      *zap.Logger
}

type RelaySettingsHandlers struct {
	cfg       *config.Config
	admin     RelayAdminCaller
	publisher NostrEventPublisher
	signer    canonicalnostr.Signer
	logger    *zap.Logger
}

type RelayPolicyState struct {
	Schema                     string                    `json:"schema"`
	UpdatedAt                  string                    `json:"updated_at"`
	UpdatedBy                  string                    `json:"updated_by,omitempty"`
	BrowserRelays              []string                  `json:"browser_relays"`
	ContextVMRelays            []string                  `json:"contextvm_relays"`
	ServiceRelays              []string                  `json:"service_relays"`
	TrustedRelayMonitorPubkeys []string                  `json:"trusted_relay_monitor_pubkeys,omitempty"`
	DMRelayLists               []RelayPolicyDMRelayList  `json:"dm_relay_lists,omitempty"`
	RelayAdministration        RelayPolicyAdministration `json:"relay_administration"`
}

type RelayPolicyDMRelayList struct {
	Enabled  bool     `json:"enabled"`
	Feature  string   `json:"feature"`
	Identity string   `json:"identity"`
	Relays   []string `json:"relays"`
}

type RelayPolicyAdministration struct {
	Enabled bool                     `json:"enabled"`
	Targets []RelayPolicyAdminTarget `json:"targets,omitempty"`
}

type RelayPolicyAdminTarget struct {
	Ref                  string   `json:"ref"`
	RelayURL             string   `json:"relay_url"`
	HTTPURL              string   `json:"http_url,omitempty"`
	Authorization        string   `json:"authorization"`
	AdministratorPubkeys []string `json:"administrator_pubkeys"`
}

type relayAdminCallPayload struct {
	TargetRef string `json:"target_ref"`
	Method    string `json:"method"`
	Params    []any  `json:"params,omitempty"`
}

func NewRelaySettingsHandlers(cfg RelaySettingsHandlerConfig) *RelaySettingsHandlers {
	logger := cfg.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	return &RelaySettingsHandlers{cfg: cfg.Config, admin: cfg.AdminClient, logger: logger.Named("relay-settings-contextvm")}
}

func RegisterRelaySettingsContextVMHandlers(transport *EncryptedRequestTransport, cfg RelaySettingsHandlerConfig) {
	h := NewRelaySettingsHandlers(cfg)
	h.Register(transport)
}

func (h *RelaySettingsHandlers) Register(transport *EncryptedRequestTransport) {
	if h == nil || transport == nil {
		return
	}
	if transport.responder != nil {
		h.publisher = transport.responder.publisher
		h.signer = transport.responder.signer
	}
	transport.RegisterContextVMHandler(ContextVMMethodRelayPolicyGet, h.GetPolicy)
	transport.RegisterContextVMHandler(ContextVMMethodRelayPolicyApply, h.ApplyPolicy)
	transport.RegisterContextVMHandler(ContextVMMethodRelayAdminCall, h.CallRelayAdmin)
}

func (h *RelaySettingsHandlers) GetPolicy(ctx context.Context, req ContextVMRequest) (any, error) {
	state := h.currentState(req.Event.PubKey)
	return map[string]any{"status": "ok", "state": state}, nil
}

func (h *RelaySettingsHandlers) ApplyPolicy(ctx context.Context, req ContextVMRequest) (any, error) {
	if h.cfg == nil {
		return nil, fmt.Errorf("relay settings config is not available")
	}
	var state RelayPolicyState
	if err := json.Unmarshal(req.RPC.Params, &state); err != nil {
		return nil, fmt.Errorf("decode relay policy settings: %w", err)
	}
	state.Schema = RelaySettingsSchema
	state.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	state.UpdatedBy = req.Event.PubKey
	if err := normalizeAndValidateRelayPolicy(&state); err != nil {
		return nil, err
	}
	applyRelayPolicyToRuntimeConfig(h.cfg, state)
	if err := h.publishState(ctx, req, state, "updated"); err != nil {
		return nil, err
	}
	if err := h.publishRelayTopology(ctx, req, state); err != nil {
		return nil, err
	}
	if err := h.publishAudit(ctx, req, state, "relay-settings.updated"); err != nil {
		return nil, err
	}
	return map[string]any{"status": "accepted", "state": state}, nil
}

func (h *RelaySettingsHandlers) CallRelayAdmin(ctx context.Context, req ContextVMRequest) (any, error) {
	var payload relayAdminCallPayload
	if err := json.Unmarshal(req.RPC.Params, &payload); err != nil {
		return nil, fmt.Errorf("decode relay admin request: %w", err)
	}
	payload.TargetRef = strings.TrimSpace(payload.TargetRef)
	payload.Method = strings.TrimSpace(payload.Method)
	if payload.TargetRef == "" {
		return nil, fmt.Errorf("target_ref is required")
	}
	if !relayAdministrationTargetConfigured(h.cfg, payload.TargetRef) {
		return nil, fmt.Errorf("nip-86 target %q is not configured as Bahia-owned or Bahia-authorized", payload.TargetRef)
	}
	if payload.Method == "" {
		return nil, fmt.Errorf("method is required")
	}
	if h.admin == nil {
		return nil, fmt.Errorf("nip-86 relay administration client is not configured")
	}
	if payload.Method == relayadmin.MethodSupportedMethods {
		methods, err := h.admin.SupportedMethods(ctx, payload.TargetRef)
		if err != nil {
			return nil, err
		}
		return map[string]any{"status": "ok", "target_ref": payload.TargetRef, "methods": methods}, nil
	}
	resp, err := h.admin.Call(ctx, payload.TargetRef, payload.Method, payload.Params)
	if err != nil {
		return nil, err
	}
	if err := h.publishAudit(ctx, req, h.currentState(req.Event.PubKey), "relay-admin."+payload.Method); err != nil {
		return nil, err
	}
	return map[string]any{"status": "ok", "target_ref": payload.TargetRef, "method": payload.Method, "result": json.RawMessage(resp.Result)}, nil
}

func (h *RelaySettingsHandlers) currentState(pubkey string) RelayPolicyState {
	state := RelayPolicyState{Schema: RelaySettingsSchema, UpdatedAt: time.Now().UTC().Format(time.RFC3339), UpdatedBy: pubkey}
	if h.cfg == nil {
		return state
	}
	nostrCfg := h.cfg.Nostr
	state.BrowserRelays = cloneStrings(nostrCfg.BrowserRelayPolicyRelays())
	state.ContextVMRelays = cloneStrings(nostrCfg.ContextVMRelayPolicyRelays())
	state.ServiceRelays = cloneStrings(nostrCfg.ServiceRelayPolicyRelays())
	state.TrustedRelayMonitorPubkeys = cloneStrings(nostrCfg.TrustedRelayMonitorPubkeys)
	state.DMRelayLists = make([]RelayPolicyDMRelayList, 0, len(nostrCfg.DMRelayLists))
	for _, list := range nostrCfg.DMRelayLists {
		state.DMRelayLists = append(state.DMRelayLists, RelayPolicyDMRelayList{Enabled: list.Enabled, Feature: list.Feature, Identity: list.Identity, Relays: cloneStrings(list.Relays)})
	}
	state.RelayAdministration.Enabled = nostrCfg.RelayAdministration.Enabled
	for _, target := range nostrCfg.RelayAdministration.Targets {
		state.RelayAdministration.Targets = append(state.RelayAdministration.Targets, RelayPolicyAdminTarget{Ref: target.Ref, RelayURL: target.RelayURL, HTTPURL: target.HTTPURL, Authorization: target.Authorization, AdministratorPubkeys: cloneStrings(target.AdministratorPubkeys)})
	}
	return state
}

func (h *RelaySettingsHandlers) publishState(ctx context.Context, req ContextVMRequest, state RelayPolicyState, status string) error {
	if req.OuterEvent == nil && req.Event == nil {
		return fmt.Errorf("relay settings request event is missing")
	}
	publisher, signer, err := h.publisherForRequest(req)
	if err != nil {
		return err
	}
	content, err := json.Marshal(state)
	if err != nil {
		return err
	}
	ev := &nostr.Event{Kind: kinds.CASControlState, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"d", RelaySettingsDTag}, {"domain", RelaySettingsDomain}, {"entity", "relay-policy"}, {"schema", RelaySettingsSchema}, {"status", status}, {"p", req.Event.PubKey}}, Content: string(content)}
	if err := SignGoNostrEvent(ctx, signer, ev); err != nil {
		return fmt.Errorf("sign relay settings state: %w", err)
	}
	if published, err := publisher.Publish(ctx, *ev); err != nil {
		return fmt.Errorf("publish relay settings state: %w", err)
	} else if published == 0 {
		return fmt.Errorf("publish relay settings state: no relay accepted event")
	}
	return nil
}

func (h *RelaySettingsHandlers) publishRelayTopology(ctx context.Context, req ContextVMRequest, state RelayPolicyState) error {
	if err := h.publishRelaySet(ctx, req, "bahia-browser-v1", state.BrowserRelays); err != nil {
		return err
	}
	if err := h.publishRelaySet(ctx, req, "bahia-contextvm-v1", state.ContextVMRelays); err != nil {
		return err
	}
	if err := h.publishRelaySet(ctx, req, "bahia-service-v1", state.ServiceRelays); err != nil {
		return err
	}
	for _, list := range state.DMRelayLists {
		if !list.Enabled || list.Feature != config.DMRelayListFeatureNotifications || list.Identity != config.DMRelayListIdentityService || len(list.Relays) == 0 {
			continue
		}
		if err := h.publishDMRelayList(ctx, req, list); err != nil {
			return err
		}
	}
	return nil
}

func (h *RelaySettingsHandlers) publishRelaySet(ctx context.Context, req ContextVMRequest, dTag string, relays []string) error {
	publisher, signer, err := h.publisherForRequest(req)
	if err != nil {
		return err
	}
	tags := nostr.Tags{{"d", dTag}, {"title", dTag}}
	for _, relay := range relays {
		tags = append(tags, nostr.Tag{"relay", relay})
	}
	ev := &nostr.Event{Kind: kinds.RelaySetDiscovery, CreatedAt: nostr.Now(), Tags: tags, Content: ""}
	if err := SignGoNostrEvent(ctx, signer, ev); err != nil {
		return fmt.Errorf("sign relay set %s: %w", dTag, err)
	}
	if published, err := publisher.Publish(ctx, *ev); err != nil {
		return fmt.Errorf("publish relay set %s: %w", dTag, err)
	} else if published == 0 {
		return fmt.Errorf("publish relay set %s: no relay accepted event", dTag)
	}
	return nil
}

func (h *RelaySettingsHandlers) publishDMRelayList(ctx context.Context, req ContextVMRequest, list RelayPolicyDMRelayList) error {
	publisher, signer, err := h.publisherForRequest(req)
	if err != nil {
		return err
	}
	tags := nostr.Tags{{"title", "bahia-dm-relays"}, {"feature", list.Feature}}
	for _, relay := range list.Relays {
		tags = append(tags, nostr.Tag{"relay", relay})
	}
	ev := &nostr.Event{Kind: kinds.NIP51DMRelayList, CreatedAt: nostr.Now(), Tags: tags, Content: ""}
	if err := SignGoNostrEvent(ctx, signer, ev); err != nil {
		return fmt.Errorf("sign dm relay list: %w", err)
	}
	if published, err := publisher.Publish(ctx, *ev); err != nil {
		return fmt.Errorf("publish dm relay list: %w", err)
	} else if published == 0 {
		return fmt.Errorf("publish dm relay list: no relay accepted event")
	}
	return nil
}

func (h *RelaySettingsHandlers) publishAudit(ctx context.Context, req ContextVMRequest, state RelayPolicyState, eventType string) error {
	publisher, signer, err := h.publisherForRequest(req)
	if err != nil {
		return err
	}
	content, err := json.Marshal(map[string]any{"schema": RelaySettingsSchema, "type": eventType, "state": state})
	if err != nil {
		return err
	}
	ev := &nostr.Event{Kind: kinds.CASAudit, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"domain", RelaySettingsDomain}, {"type", eventType}, {"schema", "bahia.audit.v1"}, {"p", req.Event.PubKey}}, Content: string(content)}
	if err := SignGoNostrEvent(ctx, signer, ev); err != nil {
		return fmt.Errorf("sign relay settings audit: %w", err)
	}
	if published, err := publisher.Publish(ctx, *ev); err != nil {
		return fmt.Errorf("publish relay settings audit: %w", err)
	} else if published == 0 {
		return fmt.Errorf("publish relay settings audit: no relay accepted event")
	}
	return nil
}

func (h *RelaySettingsHandlers) publisherForRequest(req ContextVMRequest) (NostrEventPublisher, canonicalnostr.Signer, error) {
	_ = req
	if h.publisher == nil {
		return nil, nil, fmt.Errorf("relay settings publisher is not configured")
	}
	if h.signer == nil {
		return nil, nil, fmt.Errorf("relay settings signer is not configured")
	}
	return h.publisher, h.signer, nil
}

func normalizeAndValidateRelayPolicy(state *RelayPolicyState) error {
	state.BrowserRelays = normalizeRelayListForSettings(state.BrowserRelays)
	state.ContextVMRelays = normalizeRelayListForSettings(state.ContextVMRelays)
	state.ServiceRelays = normalizeRelayListForSettings(state.ServiceRelays)
	trustedRelayMonitorPubkeys, err := normalizePubkeysForSettings("trusted_relay_monitor_pubkeys", state.TrustedRelayMonitorPubkeys)
	if err != nil {
		return err
	}
	state.TrustedRelayMonitorPubkeys = trustedRelayMonitorPubkeys
	if len(state.BrowserRelays)+len(state.ContextVMRelays)+len(state.ServiceRelays) == 0 {
		return fmt.Errorf("at least one browser, contextvm, or service relay is required")
	}
	for _, relay := range append(append([]string{}, state.BrowserRelays...), append(state.ContextVMRelays, state.ServiceRelays...)...) {
		if err := validateWebsocketRelayURLForSettings(relay); err != nil {
			return err
		}
	}
	for i := range state.DMRelayLists {
		list := &state.DMRelayLists[i]
		list.Feature = strings.ToLower(strings.TrimSpace(list.Feature))
		list.Identity = strings.ToLower(strings.TrimSpace(list.Identity))
		list.Relays = normalizeRelayListForSettings(list.Relays)
		if !list.Enabled {
			continue
		}
		if list.Feature != config.DMRelayListFeatureNotifications || list.Identity != config.DMRelayListIdentityService {
			return fmt.Errorf("dm_relay_lists[%d] only supports feature=%q identity=%q", i, config.DMRelayListFeatureNotifications, config.DMRelayListIdentityService)
		}
		if len(list.Relays) == 0 {
			return fmt.Errorf("dm_relay_lists[%d] requires at least one relay when enabled", i)
		}
		for _, relay := range list.Relays {
			if err := validateWebsocketRelayURLForSettings(relay); err != nil {
				return fmt.Errorf("dm_relay_lists[%d]: %w", i, err)
			}
		}
	}
	for i := range state.RelayAdministration.Targets {
		target := &state.RelayAdministration.Targets[i]
		target.Ref = strings.TrimSpace(target.Ref)
		target.RelayURL = strings.TrimSpace(target.RelayURL)
		target.HTTPURL = strings.TrimSpace(target.HTTPURL)
		target.Authorization = strings.ToLower(strings.TrimSpace(target.Authorization))
		administratorPubkeys, err := normalizePubkeysForSettings(fmt.Sprintf("relay_administration.targets[%d].administrator_pubkeys", i), target.AdministratorPubkeys)
		if err != nil {
			return err
		}
		target.AdministratorPubkeys = administratorPubkeys
		if target.Ref == "" || target.RelayURL == "" {
			return fmt.Errorf("relay_administration.targets[%d] requires ref and relay_url", i)
		}
		if err := validateWebsocketRelayURLForSettings(target.RelayURL); err != nil {
			return fmt.Errorf("relay_administration target %q: %w", target.Ref, err)
		}
		if target.HTTPURL != "" {
			if err := validateHTTPURLForSettings(target.HTTPURL); err != nil {
				return fmt.Errorf("relay_administration target %q: %w", target.Ref, err)
			}
		}
		if target.Authorization != config.RelayAdministrationBahiaOwned && target.Authorization != config.RelayAdministrationBahiaAuthorized {
			return fmt.Errorf("relay_administration target %q authorization must be %q or %q", target.Ref, config.RelayAdministrationBahiaOwned, config.RelayAdministrationBahiaAuthorized)
		}
		if len(target.AdministratorPubkeys) == 0 {
			return fmt.Errorf("relay_administration target %q requires administrator_pubkeys", target.Ref)
		}
	}
	return nil
}

func applyRelayPolicyToRuntimeConfig(cfg *config.Config, state RelayPolicyState) {
	cfg.Nostr.BrowserRelays = cloneStrings(state.BrowserRelays)
	cfg.Nostr.ContextVMRelays = cloneStrings(state.ContextVMRelays)
	cfg.Nostr.ServiceRelays = cloneStrings(state.ServiceRelays)
	cfg.Nostr.Relays = cloneStrings(state.ServiceRelays)
	cfg.Nostr.TrustedRelayMonitorPubkeys = cloneStrings(state.TrustedRelayMonitorPubkeys)
	cfg.Nostr.DMRelayLists = make([]config.DMRelayListConfig, 0, len(state.DMRelayLists))
	for _, list := range state.DMRelayLists {
		cfg.Nostr.DMRelayLists = append(cfg.Nostr.DMRelayLists, config.DMRelayListConfig{Enabled: list.Enabled, Feature: list.Feature, Identity: list.Identity, Relays: cloneStrings(list.Relays)})
	}
	cfg.Nostr.RelayAdministration.Enabled = state.RelayAdministration.Enabled
	cfg.Nostr.RelayAdministration.Targets = make([]config.RelayAdministrationTarget, 0, len(state.RelayAdministration.Targets))
	for _, target := range state.RelayAdministration.Targets {
		cfg.Nostr.RelayAdministration.Targets = append(cfg.Nostr.RelayAdministration.Targets, config.RelayAdministrationTarget{Ref: target.Ref, RelayURL: target.RelayURL, HTTPURL: target.HTTPURL, Authorization: target.Authorization, AdministratorPubkeys: cloneStrings(target.AdministratorPubkeys)})
	}
}

func relayAdministrationTargetConfigured(cfg *config.Config, ref string) bool {
	if cfg == nil || !cfg.Nostr.RelayAdministration.Enabled {
		return false
	}
	for _, target := range cfg.Nostr.RelayAdministration.Targets {
		if target.Ref == ref && (target.Authorization == config.RelayAdministrationBahiaOwned || target.Authorization == config.RelayAdministrationBahiaAuthorized) {
			return true
		}
	}
	return false
}

func normalizeRelayListForSettings(values []string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, raw := range values {
		for _, part := range strings.Split(raw, ",") {
			value := strings.TrimSpace(part)
			if value == "" {
				continue
			}
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			out = append(out, value)
		}
	}
	return out
}

func normalizePubkeysForSettings(field string, values []string) ([]string, error) {
	seen := map[string]struct{}{}
	out := []string{}
	for i, raw := range values {
		value := strings.ToLower(strings.TrimSpace(raw))
		if value == "" {
			continue
		}
		if len(value) != 64 {
			return nil, fmt.Errorf("%s[%d] must be a 64-character hex pubkey", field, i)
		}
		if _, err := hex.DecodeString(value); err != nil {
			return nil, fmt.Errorf("%s[%d] must be hex: %w", field, i, err)
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out, nil
}

func validateWebsocketRelayURLForSettings(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("relay URL %q must be an absolute ws/wss URL", raw)
	}
	if parsed.Scheme != "wss" && parsed.Scheme != "ws" {
		return fmt.Errorf("relay URL %q must use ws or wss", raw)
	}
	return nil
}

func validateHTTPURLForSettings(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("HTTP URL %q must be absolute", raw)
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return fmt.Errorf("HTTP URL %q must use http or https", raw)
	}
	return nil
}

func cloneStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, len(values))
	copy(out, values)
	return out
}
