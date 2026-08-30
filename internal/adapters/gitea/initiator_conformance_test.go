package gitea

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

const (
	testGitHubToken = "ghp_secret_private_repo_token_1234567890"
	testCommitSHA   = "0123456789abcdef0123456789abcdef01234567"
)

type fakeSecretResolver struct {
	known map[string]string
	calls []string
}

func (f *fakeSecretResolver) ResolveSecretWithAudit(_ context.Context, ref string, _ domain.SecretResolveOptions) (string, domain.SecretAccessManifest, error) {
	f.calls = append(f.calls, ref)
	value, ok := f.known[ref]
	if !ok {
		return "", domain.SecretAccessManifest{}, fmt.Errorf("secret %s not found", ref)
	}
	return value, domain.SecretAccessManifest{}, nil
}

type capturingPublisher struct {
	mu     sync.Mutex
	events []nostr.Event
	fail   bool
}

func (p *capturingPublisher) Publish(_ context.Context, ev nostr.Event) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fail {
		return 0, fmt.Errorf("relay unavailable")
	}
	p.events = append(p.events, ev)
	return 1, nil
}

// fakeGitea simulates the fleet Gitea API guarding a private mirror of
// chebizarro/living-library-forge. Migration succeeds only when the request
// presents the correct upstream credential.
type fakeGitea struct {
	mu           sync.Mutex
	mirrored     bool
	migrateCalls int
	syncCalls    int
}

func (g *fakeGitea) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/repos/migrate":
			g.migrateCalls++
			body, _ := io.ReadAll(r.Body)
			var req map[string]any
			_ = json.Unmarshal(body, &req)
			if req["auth_token"] != testGitHubToken {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if req["private"] != true || req["mirror"] != true {
				t.Errorf("migration must create a private mirror, got %v", req)
			}
			g.mirrored = true
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/fleet/living-library-forge":
			if !g.mirrored {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(`{"name":"living-library-forge","private":true,"mirror":true,"original_url":"https://github.com/chebizarro/living-library-forge.git"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/repos/fleet/living-library-forge/mirror-sync":
			g.syncCalls++
			if !g.mirrored {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/fleet/living-library-forge/branches/main":
			if !g.mirrored {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(`{"commit":{"id":"` + testCommitSHA + `"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func newTestSigner(t *testing.T) nostr.Signer {
	t.Helper()
	seed := strings.Repeat("7a", 32)
	signer, err := controlplane.NewPrivateKeySigner(seed)
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	return signer
}

func newConformanceInitiator(t *testing.T, server *httptest.Server) (*Initiator, *capturingPublisher, *fakeSecretResolver, uuid.UUID, *observer.ObservedLogs) {
	t.Helper()
	credentialRef := uuid.New()
	resolver := &fakeSecretResolver{known: map[string]string{credentialRef.String(): testGitHubToken}}
	publisher := &capturingPublisher{}
	core, logs := observer.New(zap.DebugLevel)
	initiator := NewInitiator(
		NewAPIClient(server.URL, "gitea-admin-token", server.Client()),
		resolver,
		publisher,
		newTestSigner(t),
		NewMemoryInitiationStore(),
		InitiatorConfig{
			MirrorOwner:        "fleet",
			WorkflowPath:       ".hive/workflows/arcana-build.yml",
			RelayHint:          "wss://relay.fleet.internal",
			RefResolveAttempts: 2,
			RefResolveDelay:    1,
		},
		zap.New(core),
	)
	return initiator, publisher, resolver, credentialRef, logs
}

func arcanaStartRequest(credentialRef uuid.UUID) controlplane.HiveCIBuildStartRequest {
	return controlplane.HiveCIBuildStartRequest{
		BuildID:              uuid.New(),
		ServiceID:            uuid.New(),
		RepositoryCoordinate: controlplane.ArcanaRepositoryCoordinate,
		GitRef:               "main",
		CredentialRef:        credentialRef,
		ArtifactRepo:         "registry.fleet.internal/arcana/web",
		BuildArgs:            map[string]string{"VITE_ARCANA_SIGNER_MODE": "nip07"},
		RequesterPubkey:      strings.Repeat("ab", 32),
		SourceEventID:        strings.Repeat("cd", 32),
	}
}

// TestConformancePrivateMirrorBuildInitiation proves the acceptance criteria
// that are provable at the initiator boundary: private source resolution from
// an opaque credential reference, immutable commit resolution, canonical CI
// run request and addressed queued evidence publication, exact-replay
// idempotency, and zero credential leakage into Nostr events or logs.
func TestConformancePrivateMirrorBuildInitiation(t *testing.T) {
	gitea := &fakeGitea{}
	server := httptest.NewServer(gitea.handler(t))
	defer server.Close()

	initiator, publisher, _, credentialRef, logs := newConformanceInitiator(t, server)
	req := arcanaStartRequest(credentialRef)

	result, err := initiator.StartHiveCIBuild(context.Background(), req)
	if err != nil {
		t.Fatalf("StartHiveCIBuild: %v", err)
	}
	if result.GitSHA != testCommitSHA {
		t.Fatalf("expected immutable commit %s, got %q", testCommitSHA, result.GitSHA)
	}
	if result.GitRef != "main" {
		t.Fatalf("unexpected git ref %q", result.GitRef)
	}
	if len(result.CIRunID) != 64 {
		t.Fatalf("CIRunID must be the published run-request event ID, got %q", result.CIRunID)
	}
	if _, err := hex.DecodeString(result.CIRunID); err != nil {
		t.Fatalf("CIRunID is not hex: %v", err)
	}
	if !gitea.mirrored || gitea.migrateCalls != 1 {
		t.Fatalf("expected exactly one private mirror migration, got %d", gitea.migrateCalls)
	}

	// Evidence: one canonical ci/workflow-run request and one addressed
	// queued-state projection, both signed and verifiable.
	if len(publisher.events) != 2 {
		t.Fatalf("expected 2 published events, got %d", len(publisher.events))
	}
	runEvent, evidence := publisher.events[0], publisher.events[1]
	if runEvent.ID.Hex() != result.CIRunID {
		t.Fatalf("run request event ID mismatch")
	}
	if !runEvent.VerifySignature() || !evidence.VerifySignature() {
		t.Fatalf("published evidence must be verifiably signed")
	}
	var rpc struct {
		Method string `json:"method"`
		Params struct {
			Workflow string `json:"workflow"`
			Commit   string `json:"commit"`
		} `json:"params"`
	}
	if err := json.Unmarshal([]byte(runEvent.Content), &rpc); err != nil {
		t.Fatalf("run request content: %v", err)
	}
	if rpc.Method != "ci/workflow-run" || rpc.Params.Commit != testCommitSHA || rpc.Params.Workflow != ".hive/workflows/arcana-build.yml" {
		t.Fatalf("unexpected canonical run request: %+v", rpc)
	}
	if int(evidence.Kind) != kinds.CASControlState {
		t.Fatalf("evidence must be addressed kind %d, got %d", kinds.CASControlState, evidence.Kind)
	}
	foundD := false
	for _, tag := range evidence.Tags {
		if len(tag) >= 2 && tag[0] == "d" && tag[1] == "hiveci-build:"+req.BuildID.String() {
			foundD = true
		}
	}
	if !foundD {
		t.Fatalf("evidence must be addressed by build ID d-tag: %v", evidence.Tags)
	}
	var state map[string]any
	if err := json.Unmarshal([]byte(evidence.Content), &state); err != nil {
		t.Fatalf("evidence content: %v", err)
	}
	if state["status"] != string(domain.BuildStatusQueued) || state["git_sha"] != testCommitSHA {
		t.Fatalf("unexpected queued evidence: %v", state)
	}

	// Secret hygiene: the credential must never appear in any published
	// Nostr event or any log entry.
	for _, ev := range publisher.events {
		blob, _ := json.Marshal(ev)
		if strings.Contains(string(blob), testGitHubToken) {
			t.Fatalf("credential leaked into published Nostr event")
		}
	}
	for _, entry := range logs.All() {
		line, _ := json.Marshal(entry.ContextMap())
		if strings.Contains(entry.Message, testGitHubToken) || strings.Contains(string(line), testGitHubToken) {
			t.Fatalf("credential leaked into logs")
		}
	}

	// Exact request replay is idempotent: same source event returns the
	// recorded result without new mirror, sync, or publish side effects.
	replayed, err := initiator.StartHiveCIBuild(context.Background(), req)
	if err != nil {
		t.Fatalf("replay StartHiveCIBuild: %v", err)
	}
	if *replayed != *result {
		t.Fatalf("replay must return the original result: %+v vs %+v", replayed, result)
	}
	if gitea.migrateCalls != 1 || gitea.syncCalls != 0 {
		t.Fatalf("replay must not touch the mirror (migrate=%d sync=%d)", gitea.migrateCalls, gitea.syncCalls)
	}
	if len(publisher.events) != 2 {
		t.Fatalf("replay must not publish new events, got %d", len(publisher.events))
	}
}

// TestConformanceUnknownCredentialFailsClosed proves that a bad opaque
// credential reference aborts initiation before any Gitea or Nostr side
// effects.
func TestConformanceUnknownCredentialFailsClosed(t *testing.T) {
	gitea := &fakeGitea{}
	server := httptest.NewServer(gitea.handler(t))
	defer server.Close()

	initiator, publisher, _, _, _ := newConformanceInitiator(t, server)
	req := arcanaStartRequest(uuid.New()) // unknown credential ref

	if _, err := initiator.StartHiveCIBuild(context.Background(), req); err == nil {
		t.Fatalf("expected failure for unknown credential reference")
	}
	if gitea.migrateCalls != 0 || len(publisher.events) != 0 {
		t.Fatalf("failed credential resolution must have no side effects")
	}
}

// TestConformanceErrorsNeverCarryCredential proves that upstream failures
// after credential resolution never leak the credential through errors.
func TestConformanceErrorsNeverCarryCredential(t *testing.T) {
	gitea := &fakeGitea{}
	server := httptest.NewServer(gitea.handler(t))
	defer server.Close()

	initiator, publisher, _, credentialRef, _ := newConformanceInitiator(t, server)
	publisher.fail = true
	req := arcanaStartRequest(credentialRef)

	_, err := initiator.StartHiveCIBuild(context.Background(), req)
	if err == nil {
		t.Fatalf("expected publish failure")
	}
	if strings.Contains(err.Error(), testGitHubToken) {
		t.Fatalf("credential leaked into error: %v", err)
	}
}

// TestConformanceUntrustedExistingRepoFailsClosed proves that a pre-existing
// same-name repository that is not a private mirror of the expected upstream
// is never built from.
func TestConformanceUntrustedExistingRepoFailsClosed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/fleet/living-library-forge" {
			_, _ = w.Write([]byte(`{"name":"living-library-forge","private":false,"mirror":false}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	initiator, publisher, _, credentialRef, _ := newConformanceInitiator(t, server)
	_, err := initiator.StartHiveCIBuild(context.Background(), arcanaStartRequest(credentialRef))
	if err == nil || !strings.Contains(err.Error(), "not a private mirror") {
		t.Fatalf("expected fail-closed mirror validation error, got %v", err)
	}
	if len(publisher.events) != 0 {
		t.Fatalf("untrusted repository must not produce run requests")
	}
}

// TestConformanceSecondRequestSyncsExistingMirror proves that a distinct
// request (new source event) against an existing mirror performs a
// mirror-sync rather than a re-migration.
func TestConformanceSecondRequestSyncsExistingMirror(t *testing.T) {
	gitea := &fakeGitea{}
	server := httptest.NewServer(gitea.handler(t))
	defer server.Close()

	initiator, _, _, credentialRef, _ := newConformanceInitiator(t, server)
	first := arcanaStartRequest(credentialRef)
	if _, err := initiator.StartHiveCIBuild(context.Background(), first); err != nil {
		t.Fatalf("first StartHiveCIBuild: %v", err)
	}
	second := arcanaStartRequest(credentialRef)
	second.SourceEventID = strings.Repeat("ef", 32)
	result, err := initiator.StartHiveCIBuild(context.Background(), second)
	if err != nil {
		t.Fatalf("second StartHiveCIBuild: %v", err)
	}
	if result.GitSHA != testCommitSHA {
		t.Fatalf("unexpected sha %q", result.GitSHA)
	}
	if gitea.migrateCalls != 1 || gitea.syncCalls != 1 {
		t.Fatalf("expected one migrate and one sync, got migrate=%d sync=%d", gitea.migrateCalls, gitea.syncCalls)
	}
}
