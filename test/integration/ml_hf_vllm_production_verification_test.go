//go:build integration

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"go.uber.org/zap"
)

const hfVLLMProductionVerifyEnv = "BAHIA_HF_VLLM_PROD_VERIFY"

type productionPrerequisite struct {
	Env         string
	Description string
}

var hfVLLMProductionPrerequisites = []productionPrerequisite{
	{"BAHIA_HF_ARTIFACT_URL", "HTTPS URL for the exact pinned Hugging Face artifact or snapshot manifest to verify"},
	{"BAHIA_HF_ARTIFACT_SHA256", "expected 64-character sha256 digest for the downloaded pinned Hugging Face artifact bytes"},
	{"BAHIA_VLLM_BASE_URL", "reachable production vLLM/OpenAI-compatible backend URL"},
	{"BAHIA_ML_EXPECTED_MODEL_ID", "model id that must be advertised by both vLLM and the gateway"},
	{"BAHIA_ML_GATEWAY_MODELS_URL", "reachable production gateway /v1/models-compatible URL for the routed model"},
	{"BAHIA_ML_OCI_MANIFEST_URL", "HTTPS OCI manifest/referrers URL for the mirrored model artifact/provenance object"},
	{"BAHIA_ML_OCI_ARTIFACT_SHA256", "expected 64-character sha256 digest for the downloaded OCI mirror/provenance bytes"},
	{"BAHIA_ML_BLOSSOM_ARTIFACT_URL", "HTTPS Blossom artifact URL for the mirrored model artifact"},
	{"BAHIA_ML_BLOSSOM_ARTIFACT_SHA256", "expected 64-character sha256 digest for the downloaded Blossom mirror bytes"},
	{"BAHIA_ML_RELAY_URLS", "comma-separated production relay URLs used by the AI fabric read-model path"},
	{"BAHIA_ML_RELAY_PRIVATE_KEY", "Nostr private key authorized to publish/subscribe verification events, including NIP-42 AUTH when required"},
}

func TestAIHFVLLMProductionIntegrations(t *testing.T) {
	if os.Getenv(hfVLLMProductionVerifyEnv) != "1" {
		t.Skipf("production HF/vLLM verification is opt-in; set %s=1 and provide prerequisites: %s", hfVLLMProductionVerifyEnv, prerequisitesText())
	}
	missing := missingHFVLLMProductionPrerequisites()
	if len(missing) > 0 {
		t.Fatalf("production HF/vLLM verification enabled but blocked by missing prerequisites: %s", strings.Join(missing, "; "))
	}

	ctx, cancel := context.WithTimeout(context.Background(), envDuration("BAHIA_HF_VLLM_PROD_VERIFY_TIMEOUT", 45*time.Second))
	defer cancel()
	client := &http.Client{Timeout: envDuration("BAHIA_HF_VLLM_HTTP_TIMEOUT", 15*time.Second)}

	t.Run("huggingface artifact is pinned and digest-addressed", func(t *testing.T) {
		verifyArtifactBytes(ctx, t, client, httpArtifactCheck{
			URL:            os.Getenv("BAHIA_HF_ARTIFACT_URL"),
			Authorization:  bearerFromEnv("BAHIA_HF_TOKEN"),
			Accept:         "application/octet-stream",
			ExpectedSHA256: os.Getenv("BAHIA_HF_ARTIFACT_SHA256"),
			Name:           "Hugging Face artifact",
		})
	})

	t.Run("artifact mirrors and provenance endpoints are reachable", func(t *testing.T) {
		verifyArtifactBytes(ctx, t, client, httpArtifactCheck{
			URL:            os.Getenv("BAHIA_ML_OCI_MANIFEST_URL"),
			Authorization:  bearerFromEnv("BAHIA_ML_OCI_TOKEN"),
			Accept:         "application/vnd.oci.image.manifest.v1+json, application/vnd.oci.artifact.manifest.v1+json, application/json",
			ExpectedSHA256: os.Getenv("BAHIA_ML_OCI_ARTIFACT_SHA256"),
			Name:           "OCI model artifact/provenance manifest",
		})
		verifyArtifactBytes(ctx, t, client, httpArtifactCheck{
			URL:            os.Getenv("BAHIA_ML_BLOSSOM_ARTIFACT_URL"),
			Authorization:  bearerFromEnv("BAHIA_ML_BLOSSOM_TOKEN"),
			Accept:         "application/octet-stream, application/json",
			ExpectedSHA256: os.Getenv("BAHIA_ML_BLOSSOM_ARTIFACT_SHA256"),
			Name:           "Blossom model artifact mirror",
		})
	})

	t.Run("vllm backend and gateway expose real OpenAI-compatible model routes", func(t *testing.T) {
		expectedModelID := os.Getenv("BAHIA_ML_EXPECTED_MODEL_ID")
		verifyOpenAIModelsEndpoint(ctx, t, client, strings.TrimRight(os.Getenv("BAHIA_VLLM_BASE_URL"), "/")+"/v1/models", bearerFromEnv("BAHIA_VLLM_API_KEY"), "vLLM backend", expectedModelID)
		verifyOpenAIModelsEndpoint(ctx, t, client, os.Getenv("BAHIA_ML_GATEWAY_MODELS_URL"), bearerFromEnv("BAHIA_ML_GATEWAY_TOKEN"), "ML gateway", expectedModelID)
	})

	t.Run("production relay verifies OK EOSE CLOSED AUTH signatures and scoped filters", func(t *testing.T) {
		verifyProductionRelayPath(ctx, t)
	})
}

func missingHFVLLMProductionPrerequisites() []string {
	missing := make([]string, 0, len(hfVLLMProductionPrerequisites))
	for _, prereq := range hfVLLMProductionPrerequisites {
		if strings.TrimSpace(os.Getenv(prereq.Env)) == "" {
			missing = append(missing, prereq.Env+" ("+prereq.Description+")")
			continue
		}
		if strings.HasSuffix(prereq.Env, "SHA256") {
			if _, ok := normalizeSHA256(os.Getenv(prereq.Env)); !ok {
				missing = append(missing, prereq.Env+" (must be a 64-character hex sha256, optionally prefixed by sha256:)")
			}
		}
	}
	return missing
}

func prerequisitesText() string {
	parts := make([]string, 0, len(hfVLLMProductionPrerequisites))
	for _, prereq := range hfVLLMProductionPrerequisites {
		parts = append(parts, prereq.Env+": "+prereq.Description)
	}
	return strings.Join(parts, "; ")
}

type httpArtifactCheck struct {
	URL            string
	Authorization  string
	Accept         string
	ExpectedSHA256 string
	Name           string
}

func verifyArtifactBytes(ctx context.Context, t *testing.T, client *http.Client, check httpArtifactCheck) {
	t.Helper()
	expectedSHA256, ok := normalizeSHA256(check.ExpectedSHA256)
	if !ok {
		t.Fatalf("%s expected sha256 is not a 64-character hex digest: %q", check.Name, check.ExpectedSHA256)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, check.URL, nil)
	if err != nil {
		t.Fatalf("%s request: %v", check.Name, err)
	}
	if check.Accept != "" {
		req.Header.Set("Accept", check.Accept)
	}
	if check.Authorization != "" {
		req.Header.Set("Authorization", check.Authorization)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s GET failed: %v", check.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		t.Fatalf("%s GET status = %s", check.Name, resp.Status)
	}
	h := sha256.New()
	bytesRead, err := io.Copy(h, resp.Body)
	if err != nil {
		t.Fatalf("%s byte stream hash failed after %d bytes: %v", check.Name, bytesRead, err)
	}
	actualSHA256 := hex.EncodeToString(h.Sum(nil))
	if actualSHA256 != expectedSHA256 {
		t.Fatalf("%s byte sha256 mismatch: got %s want %s bytes=%d", check.Name, actualSHA256, expectedSHA256, bytesRead)
	}
	if advertised := responseAdvertisedDigests(resp); len(advertised) > 0 && !containsString(advertised, expectedSHA256) {
		t.Fatalf("%s byte sha256 matched %s but response advertised conflicting digest headers: %v", check.Name, expectedSHA256, advertised)
	}
}

func verifyOpenAIModelsEndpoint(ctx context.Context, t *testing.T, client *http.Client, url string, authorization string, name string, expectedModelID string) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("%s models request: %v", name, err)
	}
	req.Header.Set("Accept", "application/json")
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s /v1/models request failed: %v", name, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		t.Fatalf("%s /v1/models status = %s body=%s", name, resp.Status, strings.TrimSpace(string(body)))
	}
	var decoded struct {
		Object string `json:"object"`
		Data   []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("%s /v1/models returned non-JSON body: %v", name, err)
	}
	if len(decoded.Data) == 0 {
		t.Fatalf("%s /v1/models returned no models", name)
	}
	for _, model := range decoded.Data {
		if model.ID == expectedModelID {
			return
		}
	}
	t.Fatalf("%s /v1/models did not include expected model id %q", name, expectedModelID)
}

func verifyProductionRelayPath(ctx context.Context, t *testing.T) {
	t.Helper()
	privateKey := os.Getenv("BAHIA_ML_RELAY_PRIVATE_KEY")
	secret, err := gonostr.SecretKeyFromHex(privateKey)
	if err != nil {
		t.Fatalf("parse relay verification private key: %v", err)
	}
	pubkey := secret.Public()
	pool := nostradapter.NewRelayPool(splitCSV(os.Getenv("BAHIA_ML_RELAY_URLS")), zap.NewNop(), nostradapter.WithPrivateKey(privateKey))
	defer pool.Close()

	for _, relayURL := range pool.URLs() {
		info, err := pool.FetchRelayInfo(ctx, relayURL, true)
		if err != nil {
			t.Fatalf("fetch NIP-11 metadata for %s: %v", relayURL, err)
		}
		if info == nil {
			t.Fatalf("relay %s returned no NIP-11 metadata", relayURL)
		}
	}
	pool.Connect(ctx)
	if pool.ConnectedCount() == 0 {
		t.Fatal("no production relay connections established")
	}

	dTag := "bahia-jicv:" + strconv.FormatInt(time.Now().UTC().UnixNano(), 10)
	event := gonostr.Event{
		Kind:      gonostr.Kind(envInt("BAHIA_ML_RELAY_VERIFY_KIND", 30078)),
		CreatedAt: gonostr.Now(),
		Tags: gonostr.Tags{
			{"d", dTag},
			{"t", "bahia-jicv-production-verification"},
			{"runtime", "vllm"},
			{"feature", "AI_FABRIC_HF_VLLM_DEPLOYMENT"},
			{"expiration", strconv.FormatInt(time.Now().UTC().Add(envDuration("BAHIA_ML_RELAY_VERIFY_EXPIRATION", 24*time.Hour)).Unix(), 10)},
		},
		Content: `{"feature_id":"AI_FABRIC_HF_VLLM_DEPLOYMENT","bead":"bahia-jicv","verification":"relay-ok-eose-closed-auth-signature-scoped-filter"}`,
	}
	if err := event.Sign(secret); err != nil {
		t.Fatalf("sign relay verification event: %v", err)
	}
	if err := nostradapter.ValidateInboundEvent(&event, time.Now().UTC(), nostradapter.InboundEventMaxFutureSkew); err != nil {
		t.Fatalf("local NIP-01 signature/id validation failed before publish: %v", err)
	}

	results, err := pool.PublishWithResults(ctx, event)
	if err != nil {
		t.Fatalf("publish relay verification event failed: %v results=%s", err, publishResultsSummary(results))
	}
	if len(results) == 0 {
		t.Fatal("publish relay verification event produced no OK results")
	}
	accepted := false
	for _, result := range results {
		if result.Accepted || result.IsDuplicate() {
			accepted = true
			continue
		}
		if result.IsAuthRequired() {
			t.Fatalf("relay %s requires AUTH but verification key was not accepted: %s", result.RelayURL, result.Reason)
		}
		if result.Reason != "" || result.Error != nil {
			t.Fatalf("relay %s did not accept verification event: reason=%q error=%v", result.RelayURL, result.Reason, result.Error)
		}
	}
	if !accepted {
		t.Fatalf("no relay accepted verification event: %s", publishResultsSummary(results))
	}

	filter := gonostr.Filter{
		Kinds:   []gonostr.Kind{event.Kind},
		Authors: []gonostr.PubKey{pubkey},
		Tags:    gonostr.TagMap{"d": []string{dTag}, "t": []string{"bahia-jicv-production-verification"}},
		Limit:   1,
	}
	for _, result := range results {
		if result.Accepted || result.IsDuplicate() {
			verifySingleRelayReadback(ctx, t, result.RelayURL, privateKey, event, filter)
		}
	}
}

func verifySingleRelayReadback(ctx context.Context, t *testing.T, relayURL string, privateKey string, event gonostr.Event, filter gonostr.Filter) {
	t.Helper()
	pool := nostradapter.NewRelayPool([]string{relayURL}, zap.NewNop(), nostradapter.WithPrivateKey(privateKey))
	defer pool.Close()
	pool.Connect(ctx)
	if pool.ConnectedCount() != 1 {
		t.Fatalf("relay %s was accepted during publish but did not reconnect for scoped readback", relayURL)
	}
	sub, err := pool.SubscribeAllWithEOSE(ctx, []gonostr.Filter{filter})
	if err != nil {
		t.Fatalf("scoped relay subscription failed for %s: %v", relayURL, err)
	}
	defer sub.Close()

	var observed bool
	var eose bool
	for !eose {
		select {
		case <-ctx.Done():
			t.Fatalf("relay %s verification context ended before EOSE: observed_event=%v err=%v", relayURL, observed, ctx.Err())
		case closed, ok := <-sub.Closed:
			if !ok {
				sub.Closed = nil
				continue
			}
			if nostradapter.IsAuthRequiredReason(closed.Reason) {
				t.Fatalf("relay %s CLOSED scoped subscription with auth-required reason after AUTH attempt: %s", closed.RelayURL, closed.Reason)
			}
			t.Fatalf("relay %s CLOSED scoped subscription: %s", closed.RelayURL, closed.Reason)
		case ev, ok := <-sub.Events:
			if !ok {
				sub.Events = nil
				continue
			}
			if ev.ID != event.ID {
				t.Fatalf("relay %s scoped filter returned unexpected event id %s want %s", relayURL, ev.ID, event.ID)
			}
			if err := nostradapter.ValidateInboundEvent(ev, time.Now().UTC(), nostradapter.InboundEventMaxFutureSkew); err != nil {
				t.Fatalf("relay %s event failed NIP-01 inbound validation: %v", relayURL, err)
			}
			observed = true
		case <-sub.EndOfStoredEvents:
			eose = true
		}
	}
	if !observed {
		t.Fatalf("relay %s scoped subscription reached EOSE without returning the accepted verification event", relayURL)
	}
}

func responseAdvertisedDigests(resp *http.Response) []string {
	if resp == nil {
		return nil
	}
	seen := map[string]struct{}{}
	out := []string{}
	for _, header := range []string{"Digest", "X-Checksum-Sha256", "X-Content-Sha256"} {
		for _, token := range digestHeaderTokens(resp.Header.Get(header)) {
			if _, ok := seen[token]; ok {
				continue
			}
			seen[token] = struct{}{}
			out = append(out, token)
		}
	}
	return out
}

func digestHeaderTokens(value string) []string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return nil
	}
	replacer := strings.NewReplacer("\"", " ", "'", " ", ",", " ", ";", " ", "=", " ", "sha256:", " ", "sha-256:", " ", "sha256", " ", "sha-256", " ", "w/", " ")
	fields := strings.Fields(replacer.Replace(value))
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if normalized, ok := normalizeSHA256(field); ok {
			out = append(out, normalized)
		}
	}
	return out
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func normalizeSHA256(value string) (string, bool) {
	normalized := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), "sha256:")
	if len(normalized) != 64 {
		return "", false
	}
	if _, err := hex.DecodeString(normalized); err != nil {
		return "", false
	}
	return normalized, true
}

func bearerFromEnv(name string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(value), "bearer ") || strings.HasPrefix(strings.ToLower(value), "basic ") {
		return value
	}
	return "Bearer " + value
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func envDuration(name string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envInt(name string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func publishResultsSummary(results []nostradapter.PublishResult) string {
	parts := make([]string, 0, len(results))
	for _, result := range results {
		parts = append(parts, fmt.Sprintf("%s accepted=%v reason=%q error=%v", result.RelayURL, result.Accepted, result.Reason, result.Error))
	}
	return strings.Join(parts, "; ")
}
