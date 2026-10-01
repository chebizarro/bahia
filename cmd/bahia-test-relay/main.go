package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore/slicestore"
	"fiatjaf.com/nostr/khatru"
	"fiatjaf.com/nostr/nip44"

	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/strutil"
)

const serviceSecretHex = "1111111111111111111111111111111111111111111111111111111111111111"
const workerSecretHex = "2222222222222222222222222222222222222222222222222222222222222222"
const operatorSecretHex = "3333333333333333333333333333333333333333333333333333333333333333"

const (
	kindAudit                   = kinds.CASAudit
	kindNIP59GiftWrap           = kinds.ContextVMGiftWrap
	kindContextVMMessage        = kinds.ContextVMMessage
	kindNIP38Status             = kinds.NIP38Status
	kindControlplaneState       = kinds.CASControlState
	kindContextVMServer         = kinds.ContextVMServerAnnouncement
	kindContextVMTools          = kinds.ContextVMToolsList
	kindContextVMResources      = kinds.ContextVMResourcesList
	kindContextVMTemplates      = kinds.ContextVMResourceTemplatesList
	kindContextVMPrompts        = kinds.ContextVMPromptsList
	kindRelaySet                = kinds.RelaySetDiscovery
	kindNIP65RelayList          = kinds.NIP65RelayList
	kindNIP51DMRelayList        = kinds.NIP51DMRelayList
	kindSBOMAttestation         = kinds.SBOMAttestation
	kindLongFormContent         = kinds.LongFormContent
	kindLoomWorkerAdvertisement = kinds.LoomWorkerAdvertisement
	kindSoulAction              = kinds.SoulFactoryAction
	kindSoulTemplate            = kinds.SoulFactoryTemplate
	kindAgentSoul               = kinds.SoulFactoryAgentSoul
	kindSoulDraft               = kinds.SoulFactoryDraft
	kindRuntimeCapability       = kinds.SoulFactoryRuntimeCapability
)

type eventSpec struct {
	Kind    int
	Author  nostr.SecretKey
	Tags    nostr.Tags
	Content any
}

func main() {
	addr := flag.String("addr", strutil.Env("BAHIA_TEST_RELAY_ADDR", "127.0.0.1:0"), "HTTP/WebSocket listen address")
	flag.Parse()

	serviceKey := nostr.MustSecretKeyFromHex(serviceSecretHex)
	store := &slicestore.SliceStore{}
	if err := store.Init(); err != nil {
		log.Fatalf("init slicestore: %v", err)
	}
	defer store.Close()

	relay := khatru.NewRelay()
	relay.Info.Name = "Bahia Playwright relay"
	relay.Info.Description = "Deterministic local relay seeded with Bahia web read models for Playwright"
	servicePubKey := serviceKey.Public()
	relay.Info.PubKey = &servicePubKey
	relay.Info.SupportedNIPs = []any{1, 11, 42, 51, 65, 78}
	relay.UseEventstore(store, 10000)

	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("listen on %s: %v", *addr, err)
	}
	defer func() { _ = listener.Close() }()

	actualAddr := listener.Addr().String()
	wsURL := "ws://" + actualAddr
	seedEvents, err := seedCorpus(wsURL)
	if err != nil {
		log.Fatalf("build seed corpus: %v", err)
	}
	for _, evt := range seedEvents {
		if err := store.SaveEvent(evt); err != nil {
			log.Fatalf("seed event kind %d id %s: %v", evt.Kind, evt.ID, err)
		}
	}

	relay.OnEventSaved = func(ctx context.Context, event nostr.Event) {
		log.Printf("accepted EVENT kind=%d id=%s pubkey=%s", event.Kind, event.ID, event.PubKey)
		response, ok := contextVMResultForRequest(event, serviceKey)
		if !ok {
			return
		}
		if err := store.SaveEvent(response); err != nil {
			log.Printf("failed to store ContextVM result for %s: %v", event.ID, err)
			return
		}
		relay.BroadcastEvent(response)
	}

	router := relay.Router()
	router.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		if _, err := fmt.Fprintf(w, `{"ok":true,"relay":"%s","service_pubkey":"%s","events":%d}`+"\n", wsURL, serviceKey.Public().Hex(), len(seedEvents)); err != nil {
			log.Printf("write health response: %v", err)
		}
	})

	log.Printf("bahia test relay listening on %s service_pubkey=%s events=%d", actualAddr, serviceKey.Public().Hex(), len(seedEvents))
	if err := http.Serve(listener, relay); err != nil {
		log.Fatal(err)
	}
}

func seedCorpus(relayURL string) ([]nostr.Event, error) {
	serviceKey := nostr.MustSecretKeyFromHex(serviceSecretHex)
	workerKey := nostr.MustSecretKeyFromHex(workerSecretHex)
	operatorKey := nostr.MustSecretKeyFromHex(operatorSecretHex)
	servicePubkey := serviceKey.Public().Hex()
	workerPubkey := workerKey.Public().Hex()
	operatorPubkey := operatorKey.Public().Hex()
	now := nostr.Now()
	events := []nostr.Event{}
	add := func(spec eventSpec) error {
		content := ""
		switch value := spec.Content.(type) {
		case string:
			content = value
		case nil:
			content = ""
		default:
			encoded, err := json.Marshal(value)
			if err != nil {
				return err
			}
			content = string(encoded)
		}
		evt := nostr.Event{Kind: nostr.Kind(spec.Kind), CreatedAt: now, Tags: spec.Tags, Content: content}
		if err := evt.Sign(spec.Author); err != nil {
			return err
		}
		events = append(events, evt)
		return nil
	}
	if err := add(eventSpec{Kind: kindContextVMServer, Author: serviceKey, Tags: nostr.Tags{{"d", "bahia-system-v1"}}, Content: map[string]any{
		"schema":   "bahia.system-discovery.v1",
		"features": map[string]any{"relay_sidecar": true, "relay_read_models": true, "encrypted_nostr_requests": true, "direct_nostr_http_auth": true, "legacy_sse": false, "publish_enabled": true},
		"nostr":    map[string]any{"trusted_relay_monitor_pubkeys": []string{}},
	}}); err != nil {
		return nil, err
	}
	for _, d := range []string{"bahia-browser-v1", "bahia-contextvm-v1", "bahia-service-v1"} {
		if err := add(eventSpec{Kind: kindRelaySet, Author: serviceKey, Tags: nostr.Tags{{"d", d}, {"relay", relayURL}}, Content: ""}); err != nil {
			return nil, err
		}
	}
	if err := add(eventSpec{Kind: kindNIP65RelayList, Author: serviceKey, Tags: nostr.Tags{{"r", relayURL, "read"}, {"r", relayURL, "write"}}, Content: ""}); err != nil {
		return nil, err
	}
	if err := add(eventSpec{Kind: kindNIP51DMRelayList, Author: serviceKey, Tags: nostr.Tags{{"relay", relayURL}}, Content: ""}); err != nil {
		return nil, err
	}

	if err := add(eventSpec{Kind: kindControlplaneState, Author: serviceKey, Tags: nostr.Tags{
		{"domain", "assistant"},
		{"schema", "bahia.assistant-session.v1"},
		{"d", "assistant-session-1"},
		{"session", "assistant-session-1"},
		{"status", "completed"},
		{"p", operatorPubkey, "", "operator"},
		{"agent", "bahia-assistant"},
	}, Content: map[string]any{
		"schema":             "bahia.assistant-session.v1",
		"session_id":         "assistant-session-1",
		"state":              "completed",
		"operator_pubkey":    operatorPubkey,
		"participants":       []string{operatorPubkey},
		"assistant_id":       "bahia-assistant",
		"assistant_pubkey":   servicePubkey,
		"transcript_summary": "Relay-backed assistant session",
		"current_turn_id":    "turn-1",
	}}); err != nil {
		return nil, err
	}
	if err := add(eventSpec{Kind: kindNIP38Status, Author: serviceKey, Tags: nostr.Tags{
		{"d", "bahia.assistant-status.v1:assistant-session-1:completed:1"},
		{"domain", "assistant"},
		{"schema", "bahia.assistant-status.v1"},
		{"t", kinds.AssistantStatusTopic},
		{"session", "assistant-session-1"},
		{"status", "completed"},
		{"agent", "bahia-assistant"},
	}, Content: map[string]any{
		"schema":     "bahia.assistant-status.v1",
		"session_id": "assistant-session-1",
		"status":     "completed",
		"message":    "Relay-backed assistant is ready.",
		"summary":    "Assistant session hydrated from the local relay.",
	}}); err != nil {
		return nil, err
	}

	if err := add(eventSpec{Kind: kindSoulTemplate, Author: serviceKey, Tags: nostr.Tags{
		{"d", "scout-template"},
		{"name", "Scout Template"},
		{"description", "Relay-backed Soul Factory template"},
		{"tier", "standard"},
		{"t", "bahia"},
		{"default-kind", fmt.Sprintf("%d", kindSoulAction)},
	}, Content: map[string]any{
		"name":        "Scout Template",
		"description": "Relay-backed Soul Factory template",
		"tier":        "standard",
		"brief":       "Create a relay-backed research assistant soul.",
		"customization": map[string]any{
			"tone": "direct",
		},
	}}); err != nil {
		return nil, err
	}
	if err := add(eventSpec{Kind: kindAgentSoul, Author: serviceKey, Tags: nostr.Tags{
		{"d", "scout"},
		{"name", "Scout"},
		{"purpose", "Relay-backed research assistant"},
		{"tier", "standard"},
		{"status", "active"},
		{"deploy-status", "healthy"},
		{"runtime", "local-runtime"},
		{"runtime-state", "ready"},
		{"capability", "local-runtime"},
		{"p", servicePubkey, "", "agent"},
	}, Content: map[string]any{
		"name":          "Scout",
		"purpose":       "Relay-backed research assistant",
		"tier":          "standard",
		"status":        "active",
		"deploy_status": "healthy",
		"runtime": map[string]any{
			"target": "local-runtime",
			"state":  "ready",
		},
		"permissions": map[string]any{
			"allowed_kinds": []int{kindSoulAction, kindContextVMMessage},
		},
		"workspace": map[string]any{
			"service_id": "svc-1",
		},
		"spec_hash": "relay-backed-scout-v1",
	}}); err != nil {
		return nil, err
	}
	if err := add(eventSpec{Kind: kindSoulDraft, Author: serviceKey, Tags: nostr.Tags{
		{"d", "scout"},
		{"name", "Scout"},
		{"tier", "standard"},
		{"template", "scout-template"},
		{"spec-hash", "relay-backed-scout-v1"},
	}, Content: map[string]any{
		"schema":   "soulfactory-draft/v2",
		"agent_id": "scout",
		"identity": map[string]any{
			"name":    "Scout",
			"purpose": "Relay-backed research assistant",
		},
		"runtime": map[string]any{
			"target": "local-runtime",
		},
		"persona": map[string]any{
			"instructions": "Use relay-backed event state.",
		},
	}}); err != nil {
		return nil, err
	}
	if err := add(eventSpec{Kind: kindRuntimeCapability, Author: serviceKey, Tags: nostr.Tags{
		{"d", "local-runtime"},
		{"runtime", "local-runtime"},
		{"schema", "soulfactory-runtime-capability/v1"},
		{"control-schema", "soulfactory-runtime-control/v1"},
		{"method", "soulfactory.provision"},
		{"method", "soulfactory.config.reload"},
		{"controller", servicePubkey},
		{"relay", relayURL},
	}, Content: map[string]any{
		"schema":         "soulfactory-runtime-capability/v1",
		"control_schema": "soulfactory-runtime-control/v1",
		"runtime":        "local-runtime",
		"methods":        []string{"soulfactory.provision", "soulfactory.config.reload"},
		"controllers":    []string{servicePubkey},
		"relays":         []string{relayURL},
		"status":         "ready",
	}}); err != nil {
		return nil, err
	}

	// Projected read models carry the producer contract: kind 30900 in the
	// projector's envelope (see seed_state.go), and worker state and worker
	// cleanup execution exactly as the control plane's publishers emit them.
	seededAt := now.Time()
	stateSeeds, err := controlStateSeeds(workerPubkey, seededAt)
	if err != nil {
		return nil, err
	}
	for _, seed := range stateSeeds {
		evt, err := controlStateEvent(seed, now, serviceKey)
		if err != nil {
			return nil, err
		}
		events = append(events, evt)
	}
	workerState, err := workerStateEvent(context.Background(), seedWorker(workerPubkey, seededAt), serviceKey)
	if err != nil {
		return nil, err
	}
	events = append(events, workerState)
	workerCleanup, err := workerCleanupStateEvent(context.Background(), workerPubkey, seededAt, serviceKey)
	if err != nil {
		return nil, err
	}
	events = append(events, workerCleanup)
	if err := add(eventSpec{Kind: kindLoomWorkerAdvertisement, Author: workerKey, Tags: nostr.Tags{{"t", "worker"}}, Content: map[string]any{"name": "worker-one", "description": "relay worker", "pubkey": workerPubkey}}); err != nil {
		return nil, err
	}
	if err := add(eventSpec{Kind: kindAudit, Author: serviceKey, Tags: nostr.Tags{{"domain", "controlplane"}, {"schema", "bahia.audit.v1"}, {"type", "service.created"}, {"event_type", "service.created"}, {"t", kinds.CPAuditTopic}, {kinds.CPAuditTagState, "svc-1"}, {"service", "svc-1"}}, Content: map[string]any{"schema": "bahia.audit.v1", "type": "service.created", "event_type": "service.created", "entity_id": "svc-1", "data": map[string]any{"name": "Checkout API"}}}); err != nil {
		return nil, err
	}
	if err := add(eventSpec{Kind: kindNIP38Status, Author: serviceKey, Tags: nostr.Tags{{"domain", "controlplane"}, {"status", "running"}, {"service", "svc-1"}}, Content: map[string]any{"status": "running", "message": "Checkout API running"}}); err != nil {
		return nil, err
	}
	if err := add(eventSpec{Kind: kindSBOMAttestation, Author: serviceKey, Tags: nostr.Tags{{"d", "art-1"}, {"service", "svc-1"}}, Content: map[string]any{"artifact_id": "art-1", "digest": "sha256:abc", "packages": []string{"pkg-one"}}}); err != nil {
		return nil, err
	}
	if err := add(eventSpec{Kind: kindLongFormContent, Author: serviceKey, Tags: nostr.Tags{{"d", "features-services"}, {"t", "bahia-docs"}, {"title", "Services"}}, Content: "# Services\nRelay-backed service documentation."}); err != nil {
		return nil, err
	}
	for _, kind := range []int{kindContextVMTools, kindContextVMResources, kindContextVMTemplates, kindContextVMPrompts} {
		if err := add(eventSpec{Kind: kind, Author: serviceKey, Tags: nostr.Tags{{"d", fmt.Sprintf("announcement-%d", kind)}}, Content: map[string]any{"items": []any{}, "service_pubkey": servicePubkey}}); err != nil {
			return nil, err
		}
	}
	if err := add(eventSpec{Kind: kindContextVMMessage, Author: serviceKey, Tags: nostr.Tags{{"p", servicePubkey}, {"status", "success"}}, Content: map[string]any{"jsonrpc": "2.0", "result": map[string]any{"status": "success"}}}); err != nil {
		return nil, err
	}
	return events, nil
}

func contextVMResultForRequest(event nostr.Event, serviceKey nostr.SecretKey) (nostr.Event, bool) {
	requestEvent := event
	if event.Kind == kindNIP59GiftWrap {
		conversationKey, err := nip44.GenerateConversationKey(event.PubKey, serviceKey)
		if err != nil {
			return nostr.Event{}, false
		}
		plaintext, err := nip44.Decrypt(event.Content, conversationKey)
		if err != nil {
			return nostr.Event{}, false
		}
		if err := json.Unmarshal([]byte(plaintext), &requestEvent); err != nil {
			return nostr.Event{}, false
		}
		if requestEvent.Kind != kindContextVMMessage || !requestEvent.CheckID() || !requestEvent.VerifySignature() {
			return nostr.Event{}, false
		}
	}

	if requestEvent.Kind != kindContextVMMessage || requestEvent.PubKey == serviceKey.Public() {
		return nostr.Event{}, false
	}
	servicePubkey := serviceKey.Public().Hex()
	if !requestEvent.Tags.ContainsAny("p", []string{servicePubkey}) {
		return nostr.Event{}, false
	}

	var request struct {
		JSONRPC string         `json:"jsonrpc"`
		ID      any            `json:"id"`
		Method  string         `json:"method"`
		Params  map[string]any `json:"params"`
	}
	if err := json.Unmarshal([]byte(requestEvent.Content), &request); err != nil || request.JSONRPC != "2.0" || request.ID == nil {
		return nostr.Event{}, false
	}

	payload := map[string]any{}
	switch request.Method {
	case "services/secrets-list":
		payload["secrets"] = []map[string]any{{"id": "relay-secret-1", "service_id": request.Params["service_id"], "name": "RELAY_BACKED_SECRET", "version": 1}}
	case "services/secrets-create":
		payload["secret"] = map[string]any{"id": "relay-secret-created", "service_id": request.Params["service_id"], "name": request.Params["name"], "version": 1}
	default:
		payload["acknowledged"] = true
		payload["method"] = request.Method
	}

	content, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      request.ID,
		"result": map[string]any{
			"status":  "success",
			"payload": payload,
		},
	})
	if err != nil {
		return nostr.Event{}, false
	}
	response := nostr.Event{
		Kind:      kindContextVMMessage,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"e", event.ID.Hex()},
			{"p", requestEvent.PubKey.Hex()},
			{"status", "success"},
			{"method", request.Method},
		},
		Content: string(content),
	}
	if err := response.Sign(serviceKey); err != nil {
		return nostr.Event{}, false
	}
	if event.Kind == kindNIP59GiftWrap {
		wrapperContent, err := json.Marshal(response)
		if err != nil {
			return nostr.Event{}, false
		}
		conversationKey, err := nip44.GenerateConversationKey(requestEvent.PubKey, serviceKey)
		if err != nil {
			return nostr.Event{}, false
		}
		ciphertext, err := nip44.Encrypt(string(wrapperContent), conversationKey)
		if err != nil {
			return nostr.Event{}, false
		}
		wrapper := nostr.Event{
			Kind:      kindNIP59GiftWrap,
			CreatedAt: nostr.Now(),
			Tags: nostr.Tags{
				{"e", event.ID.Hex()},
				{"p", requestEvent.PubKey.Hex()},
				{"status", "success"},
				{"method", request.Method},
			},
			Content: ciphertext,
		}
		if err := wrapper.Sign(serviceKey); err != nil {
			return nostr.Event{}, false
		}
		return wrapper, true
	}
	return response, true
}
