package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore/slicestore"
	"fiatjaf.com/nostr/khatru"
	"fiatjaf.com/nostr/nip44"
)

const serviceSecretHex = "1111111111111111111111111111111111111111111111111111111111111111"
const workerSecretHex = "2222222222222222222222222222222222222222222222222222222222222222"
const operatorSecretHex = "3333333333333333333333333333333333333333333333333333333333333333"

const (
	kindAudit                   = 4903
	kindNIP59GiftWrap           = 1059
	kindContextVMMessage        = 25910
	kindNIP38Status             = 30315
	kindControlplaneState       = 30900
	kindContextVMServer         = 11316
	kindContextVMTools          = 11317
	kindContextVMResources      = 11318
	kindContextVMTemplates      = 11319
	kindContextVMPrompts        = 11320
	kindRelaySet                = 30002
	kindNIP65RelayList          = 10002
	kindNIP51DMRelayList        = 10050
	kindSBOMAttestation         = 30078
	kindLongFormContent         = 30023
	kindLoomWorkerAdvertisement = 10100
	kindSoulAction              = 1950
	kindSoulTemplate            = 31950
	kindAgentSoul               = 31951
	kindSoulDraft               = 31952
	kindRuntimeCapability       = 30317
)

type eventSpec struct {
	Kind    int
	Author  nostr.SecretKey
	Tags    nostr.Tags
	Content any
}

func main() {
	addr := flag.String("addr", envOr("BAHIA_TEST_RELAY_ADDR", "127.0.0.1:48629"), "HTTP/WebSocket listen address")
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

	wsURL := "ws://" + *addr
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
		fmt.Fprintf(w, `{"ok":true,"relay":"%s","service_pubkey":"%s","events":%d}`+"\n", wsURL, serviceKey.Public().Hex(), len(seedEvents))
	})

	log.Printf("bahia test relay listening on %s service_pubkey=%s events=%d", *addr, serviceKey.Public().Hex(), len(seedEvents))
	if err := http.ListenAndServe(*addr, relay); err != nil {
		log.Fatal(err)
	}
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
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
	state := func(schema, d string, content map[string]any, tags ...nostr.Tag) error {
		body := map[string]any{"schema": schema, "id": d, "deleted": false}
		for k, v := range content {
			body[k] = v
		}
		baseTags := nostr.Tags{{"domain", domainForSchema(schema)}, {"schema", schema}, {"d", d}, {"deleted", "false"}}
		baseTags = append(baseTags, tags...)
		return add(eventSpec{Kind: kindControlplaneState, Author: serviceKey, Tags: baseTags, Content: body})
	}

	if err := add(eventSpec{Kind: kindContextVMServer, Author: serviceKey, Tags: nostr.Tags{{"d", "bahia-system-v1"}}, Content: map[string]any{
		"schema":   "bahia.system-discovery.v1",
		"features": map[string]any{"relay_sidecar": true, "relay_read_models": true, "legacy_sse": false, "publish_enabled": true},
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
		{"domain", "assistant"},
		{"schema", "bahia.assistant-status.v1"},
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

	readModels := []struct {
		schema  string
		d       string
		content map[string]any
		tags    []nostr.Tag
	}{
		{"bahia.registry.service.v1", "svc-1", map[string]any{"name": "Checkout API", "slug": "checkout-api", "artifact_repo": "registry.example/checkout", "runtime_type": "docker", "status": "running"}, nil},
		{"bahia.registry.environment.v1", "env-1", map[string]any{"name": "Production", "slug": "production", "protected": true, "status": "active"}, nil},
		{"bahia.state.service.v1", "svc-1:env-1", map[string]any{"service_id": "svc-1", "environment_id": "env-1", "drift_status": "drifted", "status": "running"}, []nostr.Tag{{"service", "svc-1"}, {"environment", "env-1"}}},
		{"bahia.registry.artifact.v1", "art-1", map[string]any{"service_id": "svc-1", "digest": "sha256:abc", "status": "available", "created_at": "2026-06-12T12:00:00Z"}, []nostr.Tag{{"service", "svc-1"}}},
		{"bahia.registry.build.v1", "build-1", map[string]any{"service_id": "svc-1", "status": "succeeded", "created_at": "2026-06-12T11:00:00Z"}, []nostr.Tag{{"service", "svc-1"}}},
		{"bahia.registry.deployment-intent.v1", "intent-1", map[string]any{"service_id": "svc-1", "environment_id": "env-1", "status": "pending", "created_at": "2026-06-12T12:05:00Z"}, []nostr.Tag{{"service", "svc-1"}, {"environment", "env-1"}}},
		{"bahia.registry.deployment-run.v1", "run-1", map[string]any{"service_id": "svc-1", "environment_id": "env-1", "status": "running", "created_at": "2026-06-12T12:10:00Z"}, []nostr.Tag{{"service", "svc-1"}, {"environment", "env-1"}}},
		{"bahia.registry.policy.v1", "policy-1", map[string]any{"name": "Default policy", "status": "active"}, nil},
		{"bahia.registry.llm-route.v1", "llm-route-1", map[string]any{"route_id": "llm-route-1", "name": "chat-default", "model": "llama-3.1"}, []nostr.Tag{{"route", "llm-route-1"}}},
		{"bahia.state.llm-route.v1", "llm-route-1:env-1", map[string]any{"route_id": "llm-route-1", "environment_id": "env-1", "status": "live"}, []nostr.Tag{{"route", "llm-route-1"}, {"environment", "env-1"}}},
		{"bahia.registry.package-repository.v1", "pkg-repo-1", map[string]any{"name": "packages", "url": "oci://registry.example/packages"}, nil},
		{"bahia.registry.package-artifact.v1", "pkg-1", map[string]any{"name": "pkg-one", "version": "1.0.0", "status": "published", "created_at": "2026-06-12T10:00:00Z"}, nil},
		{"bahia.registry.package-promotion.v1", "promotion-1", map[string]any{"package_id": "pkg-1", "environment_id": "env-1", "status": "promoted", "promoted_at": "2026-06-12T10:30:00Z"}, nil},
		{"bahia.registry.backup-repository.v1", "backup-repo-1", map[string]any{"name": "primary backups", "status": "ready"}, nil},
		{"bahia.registry.backup-policy.v1", "backup-policy-1", map[string]any{"name": "daily", "status": "active"}, nil},
		{"bahia.registry.backup-recipe.v1", "backup-recipe-1", map[string]any{"name": "postgres", "status": "ready"}, nil},
		{"bahia.registry.backup-definition.v1", "backup-def-1", map[string]any{"name": "checkout db", "status": "enabled"}, nil},
		{"bahia.state.backup-run.v1", "backup-run-1", map[string]any{"definition_id": "backup-def-1", "status": "succeeded", "created_at": "2026-06-12T09:00:00Z"}, nil},
		{"bahia.state.backup-verification.v1", "backup-verify-1", map[string]any{"run_id": "backup-run-1", "status": "passed"}, nil},
		{"bahia.state.backup-restore.v1", "backup-restore-1", map[string]any{"run_id": "backup-run-1", "status": "available"}, nil},
		{"bahia.state.backup-runtime-observation.v1", "backup-obs-1", map[string]any{"repository_id": "backup-repo-1", "health": "healthy"}, nil},
		{"bahia.registry.ml-model.v1", "model-1", map[string]any{"name": "fraud-detector", "status": "ready"}, nil},
		{"bahia.registry.ml-model-version.v1", "model-version-1", map[string]any{"model_id": "model-1", "version": "v1", "status": "ready"}, nil},
		{"bahia.registry.ml-inference-endpoint.v1", "ml-endpoint-1", map[string]any{"name": "fraud-endpoint", "model_id": "model-1", "status": "ready"}, nil},
		{"bahia.state.ml-inference-endpoint.v1", "ml-endpoint-state-1", map[string]any{"endpoint_id": "ml-endpoint-1", "status": "live"}, nil},
		{"bahia.state.worker.v1", workerPubkey, map[string]any{"pubkey": workerPubkey, "name": "worker-one", "status": "online", "fips_overlay_addr": "fd00::1", "capabilities": map[string]any{"docker": true}, "mesh_health": map[string]any{"rtt": "15ms", "loss": 0.01, "jitter": "2ms", "goodput": "1Gbps"}}, []nostr.Tag{{"worker", workerPubkey}}},
		{"bahia.state.worker-assignment.v1", "assignment-1", map[string]any{"worker_pubkey": workerPubkey, "service_id": "svc-1", "environment_id": "env-1", "status": "assigned"}, []nostr.Tag{{"worker", workerPubkey}, {"service", "svc-1"}, {"environment", "env-1"}}},
		{"bahia.state.worker-drain-status.v1", workerPubkey, map[string]any{"worker_pubkey": workerPubkey, "status": "schedulable"}, []nostr.Tag{{"worker", workerPubkey}}},
		{"bahia.state.worker-eligibility-preview.v1", "eligibility-1", map[string]any{"worker_pubkey": workerPubkey, "service_id": "svc-1", "eligible": true}, []nostr.Tag{{"worker", workerPubkey}, {"service", "svc-1"}}},
		{"bahia.state.worker-cleanup-execution.v1", "cleanup-1", map[string]any{"worker_pubkey": workerPubkey, "status": "completed"}, []nostr.Tag{{"worker", workerPubkey}}},
		{"bahia.state.dns-zone.v1", "zone-1", map[string]any{"zone": "prod.example.com", "name": "prod.example.com", "status": "active"}, []nostr.Tag{{"domain", "dns"}, {"zone", "prod.example.com"}}},
		{"bahia.state.dns-endpoint.v1", "endpoint-1", map[string]any{"fqdn": "checkout.prod.example.com", "zone": "prod.example.com", "service": "svc-1", "environment": "env-1", "family": "mesh", "address": "fd00::1", "protocol": "https", "port": 443, "health": "healthy", "worker_pubkey": workerPubkey, "metadata": map[string]any{"mesh": "fips", "projection_status": "projected"}}, []nostr.Tag{{"domain", "dns"}, {"family", "mesh"}, {"mesh", "fips"}, {"dns", "checkout.prod.example.com"}, {"worker", workerPubkey}, {"zone", "prod.example.com"}}},
		{"bahia.state.dns-policy.v1", "dns-policy-1", map[string]any{"name": "public policy", "zone": "prod.example.com", "status": "active"}, []nostr.Tag{{"domain", "dns"}, {"zone", "prod.example.com"}}},
		{"bahia.state.dns-backend.v1", "dns-backend-1", map[string]any{"name": "route53", "status": "ready"}, []nostr.Tag{{"domain", "dns"}}},
	}
	for _, model := range readModels {
		if err := state(model.schema, model.d, model.content, model.tags...); err != nil {
			return nil, err
		}
	}
	if err := add(eventSpec{Kind: kindLoomWorkerAdvertisement, Author: workerKey, Tags: nostr.Tags{{"t", "worker"}}, Content: map[string]any{"name": "worker-one", "description": "relay worker", "pubkey": workerPubkey}}); err != nil {
		return nil, err
	}
	if err := add(eventSpec{Kind: kindAudit, Author: serviceKey, Tags: nostr.Tags{{"domain", "controlplane"}, {"schema", "bahia.audit.v1"}, {"type", "service.created"}, {"event_type", "service.created"}, {"d", "svc-1"}, {"service", "svc-1"}}, Content: map[string]any{"schema": "bahia.audit.v1", "type": "service.created", "event_type": "service.created", "entity_id": "svc-1", "data": map[string]any{"name": "Checkout API"}}}); err != nil {
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

func domainForSchema(schema string) string {
	switch {
	case strings.Contains(schema, ".dns-"):
		return "dns"
	case strings.Contains(schema, ".worker"):
		return "worker"
	case strings.Contains(schema, ".backup"):
		return "backup"
	case strings.Contains(schema, ".ml-"):
		return "ml"
	default:
		return "controlplane"
	}
}
