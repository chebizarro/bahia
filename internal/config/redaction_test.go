package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"gopkg.in/yaml.v3"
)

const configSecretSentinel = `BAHIA-SECRET-SENTINEL:/@?&%\"<>`

func TestConfigProtectedValuesAreRedactedFromFormattingAndSerialization(t *testing.T) {
	cfg := configWithSecretSentinels()
	values := []struct {
		name  string
		value any
	}{
		{name: "config", value: cfg},
		{name: "worker cleanup", value: cfg.WorkerCleanup},
		{name: "edge routing", value: cfg.EdgeRouting},
		{name: "internal routing", value: cfg.InternalRouting},
		{name: "dns", value: cfg.DNS},
		{name: "dns backend", value: cfg.DNS.Backends["primary"]},
		{name: "soul factory", value: cfg.SoulFactory},
		{name: "assistant", value: cfg.Assistant},
		{name: "assistant agentic", value: cfg.Assistant.Agentic},
		{name: "assistant mcp", value: cfg.Assistant.MCP},
		{name: "assistant external mcp", value: cfg.Assistant.MCP.ExternalServers[0]},
		{name: "package control plane", value: cfg.Packages},
		{name: "package backend", value: cfg.Packages.Backends["primary"]},
		{name: "llm control plane", value: cfg.LLM},
		{name: "llm gateway", value: cfg.LLM.Gateways["primary"]},
		{name: "registry", value: cfg.Registry},
		{name: "database", value: cfg.DB},
		{name: "harbor", value: cfg.Harbor},
		{name: "loom", value: cfg.Loom},
		{name: "loom projection", value: cfg.Loom.CanonicalProjection},
		{name: "nostr", value: cfg.Nostr},
		{name: "relay sidecar", value: cfg.Nostr.Sidecar},
		{name: "relay administration", value: cfg.Nostr.RelayAdministration},
		{name: "blossom", value: cfg.Blossom},
		{name: "oci", value: cfg.OCI},
		{name: "oci service account", value: cfg.OCI.ServiceAccounts[0]},
		{name: "hiveci", value: cfg.HiveCI},
		{name: "hiveci initiator", value: cfg.HiveCI.Initiator},
		{name: "qdrant", value: cfg.Qdrant},
		{name: "notifications", value: cfg.Notifications},
	}

	for _, test := range values {
		t.Run(test.name, func(t *testing.T) {
			jsonBytes, err := json.Marshal(test.value)
			if err != nil {
				t.Fatalf("marshal JSON: %v", err)
			}
			yamlBytes, err := yaml.Marshal(test.value)
			if err != nil {
				t.Fatalf("marshal YAML: %v", err)
			}
			outputs := map[string]string{
				"default format": fmt.Sprintf("%v", test.value),
				"field format":   fmt.Sprintf("%+v", test.value),
				"Go syntax":      fmt.Sprintf("%#v", test.value),
				"quoted format":  fmt.Sprintf("%q", test.value),
				"JSON":           string(jsonBytes),
				"YAML":           string(yamlBytes),
			}
			for outputName, output := range outputs {
				t.Run(outputName, func(t *testing.T) {
					assertSecretAbsent(t, outputName, output, configSecretSentinel)
					if !strings.Contains(output, redactedValue) && !strings.Contains(output, url.PathEscape(redactedValue)) {
						t.Fatalf("%s did not identify a redacted value: %s", outputName, output)
					}
				})
			}
		})
	}

	redactedJSON, err := json.Marshal(cfg.Redacted())
	if err != nil {
		t.Fatalf("marshal explicit redacted view: %v", err)
	}
	assertSecretAbsent(t, "explicit redacted view", string(redactedJSON), configSecretSentinel)
	for _, diagnostic := range []string{fmt.Sprint(cfg), string(redactedJSON)} {
		if !strings.Contains(diagnostic, "diagnostic-mode") || !strings.Contains(diagnostic, "db.internal") || !strings.Contains(diagnostic, "TOKEN=") {
			t.Fatalf("redaction removed useful non-secret diagnostics: %s", diagnostic)
		}
	}

}

func TestConfigLoggingUsesRedactedView(t *testing.T) {
	cfg := configWithSecretSentinels()
	core, observed := observer.New(zap.DebugLevel)
	zap.New(core).Info("configuration diagnostic", zap.Any("config", cfg), zap.Any("database", cfg.DB))
	entries := observed.All()
	if len(entries) != 1 {
		t.Fatalf("observed config log entries = %d, want 1", len(entries))
	}
	loggedFields := fmt.Sprint(entries[0].ContextMap())
	assertSecretAbsent(t, "Zap config log", loggedFields, configSecretSentinel)
	if !strings.Contains(loggedFields, redactedValue) || !strings.Contains(loggedFields, "db.internal") {
		t.Fatalf("Zap config log lost redaction marker or useful diagnostics: %s", loggedFields)
	}
}

func TestRedactedJSONPreservesAnonymousFieldShape(t *testing.T) {
	encoded, err := json.Marshal(LLMControlplaneConfig{
		Enabled: true,
		OperatorAccessConfig: OperatorAccessConfig{
			AllowedSubjects: []string{"operator-1"},
		},
	})
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal JSON: %v", err)
	}
	if _, nested := decoded["OperatorAccessConfig"]; nested {
		t.Fatalf("anonymous field unexpectedly nested: %s", encoded)
	}
	if got, ok := decoded["AllowedSubjects"].([]any); !ok || len(got) != 1 || got[0] != "operator-1" {
		t.Fatalf("anonymous field was not preserved: %s", encoded)
	}
}

func configWithSecretSentinels() Config {
	s := configSecretSentinel
	return Config{
		Mode:            "diagnostic-mode",
		WorkerCleanup:   WorkerCleanupConfig{PaymentToken: s},
		EdgeRouting:     EdgeRoutingConfig{Provider: "cloudflare", APITokenRef: s},
		InternalRouting: InternalRoutingConfig{Provider: "nginx", CommandEnv: []string{"TOKEN=" + s}},
		DNS: DNSConfig{Backends: map[string]DNSBackendConfig{
			"primary": {Type: "powerdns", PowerDNSAPIURL: "https://dns.internal", PowerDNSAPIKey: s},
		}},
		SoulFactory: SoulFactoryConfig{
			SignetBunkerURI: s, SignetClientSecretKey: s, LLMAPIKey: s,
			WorkspacePrivateKeyRef: s, WorkspaceAgentMemoryMCPURLRef: s,
		},
		Assistant: AssistantConfig{
			LLMBaseURL: "https://llm.internal", LLMAPIKey: s, SignetBunkerURI: s,
			Agentic: AssistantAgenticConfig{Provider: "openai", APIKey: s},
			MCP: AssistantMCPConfig{ExternalServers: []AssistantExternalMCPServerConfig{{
				Name: "private", URL: "https://mcp.internal", AuthHeaders: map[string]string{"Authorization": "Bearer " + s},
			}}},
		},
		Packages: PackageControlplaneConfig{Backends: map[string]PackageBackendConfig{
			"primary": {Type: "oci", BaseURL: "https://packages.internal", AuthSecretRef: s, TLSSecretRef: s, SecretRefs: map[string]string{"signing": s}},
		}},
		LLM: LLMControlplaneConfig{Gateways: map[string]LLMGatewayEndpointConfig{
			"primary": {Type: "litellm", BaseURL: "https://gateway.internal", AuthToken: s},
		}},
		Registry: RegistryAdapterConfig{Type: "harbor", URL: "https://registry.internal", Username: "diagnostic-user", Password: s},
		DB:       DBConfig{Host: "db.internal", Port: 5432, User: "bahia", Password: s, Name: "bahia", SSLMode: "require"},
		Harbor:   HarborConfig{URL: "https://harbor.internal", Username: "diagnostic-user", Password: s},
		Loom: LoomConfig{CanonicalProjection: LoomCanonicalProjectionConfig{
			SignetBunkerURI: s, SignetClientSecretKey: s, RawPrivateKey: s,
		}},
		Nostr: NostrConfig{
			PrivateKey:          s,
			Sidecar:             RelaySidecarConfig{PublicURL: "wss://relay.internal", AuthPrivateKey: s},
			RelayAdministration: RelayAdministrationConfig{AdministratorPrivateKeyRef: s},
		},
		Blossom: BlossomConfig{URL: "https://blossom.internal", PrivateKey: s},
		OCI: OCIServerConfig{PublicHost: "oci.internal", ServiceAccounts: []OCIServiceAccountConfig{{
			Username: "diagnostic-user", PasswordHash: s,
		}}},
		HiveCI:        HiveCIConfig{Initiator: HiveCIInitiatorConfig{GiteaBaseURL: "https://git.internal", GiteaToken: s}},
		Qdrant:        QdrantConfig{URL: "https://qdrant.internal", APIKey: s},
		Notifications: NotificationsConfig{Enabled: true, WebhookURL: "https://hooks.internal/" + s},
	}
}

func assertSecretAbsent(t *testing.T, outputName, output, secret string) {
	t.Helper()
	for _, variant := range secretRepresentations(secret) {
		if variant != "" && strings.Contains(output, variant) {
			t.Fatalf("%s leaked protected representation %q", outputName, variant)
		}
	}
}
