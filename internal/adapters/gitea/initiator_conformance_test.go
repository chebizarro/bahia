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
	cascadia "git.sharegap.net/cascadia/cascadia-go"
	"github.com/google/uuid"
	loomAdapter "github.com/openagentsinc/bahia/internal/adapters/loom"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

const (
	testRepositoryCredential = "secret_private_repo_credential_1234567890"
	testCommitSHA            = "0123456789abcdef0123456789abcdef01234567"
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

type capturingLoomSubmitter struct {
	jobs []loomAdapter.JobRequest
}

func (s *capturingLoomSubmitter) SubmitJob(_ context.Context, job loomAdapter.JobRequest) (string, error) {
	s.jobs = append(s.jobs, job)
	return strings.Repeat("ef", 32), nil
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
	mu                 sync.Mutex
	mirrored           bool
	migrateCalls       int
	syncCalls          int
	sourceCloneURL     string
	sourceService      string
	sourceAuthUsername string
	migrationRequest   map[string]any
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
			expectedCloneURL := g.sourceCloneURL
			if expectedCloneURL == "" {
				expectedCloneURL = "https://github.com/chebizarro/living-library-forge.git"
			}
			expectedService := g.sourceService
			if expectedService == "" {
				expectedService = MigrationServiceGitHub
			}
			authOK := req["auth_token"] == testRepositoryCredential
			if expectedService == MigrationServiceGit {
				authOK = req["auth_username"] == g.sourceAuthUsername && req["auth_password"] == testRepositoryCredential
				_, tokenPresent := req["auth_token"]
				authOK = authOK && !tokenPresent
			}
			if req["clone_addr"] != expectedCloneURL || req["service"] != expectedService || !authOK {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte(`{"message":"unsupported source provider or mirror authentication"}`))
				return
			}
			if req["private"] != true || req["mirror"] != true {
				t.Errorf("migration must create a private mirror, got %v", req)
			}
			g.migrationRequest = req
			g.mirrored = true
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/fleet/living-library-forge":
			if !g.mirrored {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			originalURL := g.sourceCloneURL
			if originalURL == "" {
				originalURL = "https://github.com/chebizarro/living-library-forge.git"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name": "living-library-forge", "private": true, "mirror": true, "original_url": originalURL,
				"clone_url": "https://git.fleet.internal/fleet/living-library-forge.git",
			})
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
	resolver := &fakeSecretResolver{known: map[string]string{credentialRef.String(): testRepositoryCredential}}
	publisher := &capturingPublisher{}
	loomSubmitter := &capturingLoomSubmitter{}
	core, logs := observer.New(zap.DebugLevel)
	signer := newTestSigner(t)
	pubkey, err := signer.GetPublicKey(context.Background())
	if err != nil {
		t.Fatalf("get signer pubkey: %v", err)
	}
	initiator := NewInitiator(
		NewAPIClient(server.URL, "gitea-admin-token", server.Client()),
		resolver,
		publisher,
		signer,
		NewMemoryInitiationStore(),
		InitiatorConfig{
			MirrorOwner:              "fleet",
			WorkflowPath:             ".hive/workflows/arcana-build.yml",
			SourceProvider:           SourceProviderGitHub,
			RepoAnnouncementAddr:     "30617:" + pubkey.Hex() + ":living-library-forge",
			TrustedCIPubkeys:         []string{pubkey.Hex()},
			TrustedLoomWorkerPubkeys: []string{strings.Repeat("ab", 32)},
			RelayHint:                "wss://relay.fleet.internal",
			RefResolveAttempts:       2,
			RefResolveDelay:          1,
		},
		zap.New(core),
		WithLoomJobSubmitter(loomSubmitter),
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

	// Evidence: one fleet-local Hive-CI workflow run and one addressed
	// queued-state projection, both signed and verifiable.
	if len(publisher.events) != 2 {
		t.Fatalf("expected 2 published events, got %d", len(publisher.events))
	}
	runEvent, evidence := publisher.events[0], publisher.events[1]
	if runEvent.ID.Hex() != result.CIRunID {
		t.Fatalf("run request event ID mismatch")
	}
	if int(runEvent.Kind) != 5401 {
		t.Fatalf("workflow run kind = %d, want wire kind 5401", runEvent.Kind)
	}
	if int(runEvent.Kind) != kinds.HiveCIWorkflowRun {
		t.Fatalf("workflow run kind = %d, want Bahia constant %d", runEvent.Kind, kinds.HiveCIWorkflowRun)
	}
	contextVMKind := cascadia.ContextVMMethods["ci/workflow-run"].Kind
	if contextVMKind != 25910 {
		t.Fatalf("ci/workflow-run ContextVM binding kind = %d, want 25910", contextVMKind)
	}
	if int(runEvent.Kind) == contextVMKind {
		t.Fatalf("workflow run must not use ephemeral ContextVM kind %d", contextVMKind)
	}
	if !runEvent.VerifySignature() || !evidence.VerifySignature() {
		t.Fatalf("published evidence must be verifiably signed")
	}
	if runEvent.Content != "" {
		t.Fatalf("workflow run must use the grasp-compatible tag-only payload, got %q", runEvent.Content)
	}
	tags := make(map[string]string, len(runEvent.Tags))
	for _, tag := range runEvent.Tags {
		if len(tag) >= 2 {
			tags[tag[0]] = tag[1]
		}
	}
	pubkey, err := initiator.signer.GetPublicKey(context.Background())
	if err != nil {
		t.Fatalf("get initiator pubkey: %v", err)
	}
	wantTags := map[string]string{
		"a":            "30617:" + pubkey.Hex() + ":living-library-forge",
		"commit":       testCommitSHA,
		"branch":       "main",
		"trigger":      "push",
		"triggered-by": req.RequesterPubkey,
		"workflow":     ".hive/workflows/arcana-build.yml",
		"publisher":    pubkey.Hex(),
		"t":            "hive-ci",
	}
	for key, want := range wantTags {
		if got := tags[key]; got != want {
			t.Fatalf("workflow run tag %q = %q, want %q", key, got, want)
		}
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
	loomSubmitter := initiator.loom.(*capturingLoomSubmitter)
	if len(loomSubmitter.jobs) != 1 {
		t.Fatalf("Loom submissions = %d, want 1", len(loomSubmitter.jobs))
	}
	job := loomSubmitter.jobs[0]
	if job.ReferencedEventID != result.CIRunID || job.Params["run"] != result.CIRunID {
		t.Fatalf("Loom correlation = e:%q run:%q, want 5401 %q", job.ReferencedEventID, job.Params["run"], result.CIRunID)
	}
	if job.Params["method"] != "ci/workflow-run" || job.Params["repo"] != "https://git.fleet.internal/fleet/living-library-forge.git" ||
		job.Params["ref"] != "main" || job.Params["workflow"] != ".hive/workflows/arcana-build.yml" {
		t.Fatalf("unexpected Hive-CI Loom params: %#v", job.Params)
	}
	if len(job.RequiredWorkloads) != 1 || job.RequiredWorkloads[0] != "ci/workflow-run" ||
		len(job.RequiredFeatures) != 1 || job.RequiredFeatures[0] != "hive_ci_profile" {
		t.Fatalf("Hive-CI capability requirements = workloads:%v features:%v", job.RequiredWorkloads, job.RequiredFeatures)
	}
	if job.PaymentToken != "" {
		t.Fatalf("fleet-internal Hive-CI job carried payment token")
	}

	// Secret hygiene: the credential must never appear in any published
	// Nostr event or any log entry.
	for _, ev := range publisher.events {
		blob, _ := json.Marshal(ev)
		if strings.Contains(string(blob), testRepositoryCredential) {
			t.Fatalf("credential leaked into published Nostr event")
		}
	}
	for _, entry := range logs.All() {
		line, _ := json.Marshal(entry.ContextMap())
		if strings.Contains(entry.Message, testRepositoryCredential) || strings.Contains(string(line), testRepositoryCredential) {
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
	if len(loomSubmitter.jobs) != 1 {
		t.Fatalf("replay duplicated Loom dispatch, got %d submissions", len(loomSubmitter.jobs))
	}
	if gitea.migrateCalls != 1 || gitea.syncCalls != 0 {
		t.Fatalf("replay must not touch the mirror (migrate=%d sync=%d)", gitea.migrateCalls, gitea.syncCalls)
	}
	if len(publisher.events) != 2 {
		t.Fatalf("replay must not publish new events, got %d", len(publisher.events))
	}
}

func TestSelfDispatchRejectsUnsupportedBuildArgsBeforeSideEffects(t *testing.T) {
	gitea := &fakeGitea{}
	server := httptest.NewServer(gitea.handler(t))
	defer server.Close()

	initiator, publisher, resolver, credentialRef, _ := newConformanceInitiator(t, server)
	req := arcanaStartRequest(credentialRef)
	req.BuildArgs = map[string]string{"VITE_ARCANA_SIGNER_MODE": "nip07"}
	if _, err := initiator.StartHiveCIBuild(context.Background(), req); err == nil || !strings.Contains(err.Error(), "does not support build arguments") {
		t.Fatalf("unsupported build args error = %v", err)
	}
	if len(resolver.calls) != 0 || gitea.migrateCalls != 0 || gitea.syncCalls != 0 || len(publisher.events) != 0 {
		t.Fatalf("unsupported build args reached side effects: secret=%d migrate=%d sync=%d events=%d", len(resolver.calls), gitea.migrateCalls, gitea.syncCalls, len(publisher.events))
	}
}

func TestSelfDispatchWarnsWhenServicePubkeyIsNotTrusted(t *testing.T) {
	gitea := &fakeGitea{}
	server := httptest.NewServer(gitea.handler(t))
	defer server.Close()

	initiator, _, _, credentialRef, logs := newConformanceInitiator(t, server)
	initiator.cfg.TrustedCIPubkeys = nil
	if _, err := initiator.StartHiveCIBuild(context.Background(), arcanaStartRequest(credentialRef)); err != nil {
		t.Fatalf("StartHiveCIBuild: %v", err)
	}
	for _, entry := range logs.All() {
		if entry.ContextMap()["reason"] == "self_issued_run_untrusted" {
			return
		}
	}
	t.Fatal("missing self_issued_run_untrusted warning")
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
	if strings.Contains(err.Error(), testRepositoryCredential) {
		t.Fatalf("credential leaked into error: %v", err)
	}
}

// TestConformancePrivateGiteaMirrorBuildInitiation is the live HTTP 422
// regression: the fake returns 422 unless Bahia selects provider-neutral git
// migration and supplies the resolved credential as auth_password alongside
// the configured username. The stored original_url remains the exact clean
// clone_addr, so post-create validation still succeeds without a credential.
func TestConformancePrivateGiteaMirrorBuildInitiation(t *testing.T) {
	const (
		cloneURL = "https://git.sharegap.net/chebizar-coinos.io-336e0b4c237a0c000c1e/astillero.git"
		username = "bahia-mirror"
	)
	gitea := &fakeGitea{
		sourceCloneURL: cloneURL, sourceService: MigrationServiceGit, sourceAuthUsername: username,
	}
	server := httptest.NewServer(gitea.handler(t))
	defer server.Close()

	initiator, publisher, _, credentialRef, logs := newConformanceInitiator(t, server)
	initiator.cfg.SourceProvider = SourceProviderGitea
	initiator.cfg.SourceCloneURL = cloneURL
	initiator.cfg.SourceAuthUsername = username

	result, err := initiator.StartHiveCIBuild(context.Background(), arcanaStartRequest(credentialRef))
	if err != nil {
		t.Fatalf("StartHiveCIBuild private Gitea 422 regression: %v", err)
	}
	if result.GitSHA != testCommitSHA || gitea.migrateCalls != 1 {
		t.Fatalf("private Gitea initiation result=%+v migrateCalls=%d", result, gitea.migrateCalls)
	}
	if got := gitea.migrationRequest["clone_addr"]; got != cloneURL {
		t.Fatalf("credential-free clone_addr = %q, want %q", got, cloneURL)
	}
	for _, ev := range publisher.events {
		blob, _ := json.Marshal(ev)
		if strings.Contains(string(blob), testRepositoryCredential) {
			t.Fatal("private Gitea credential leaked into Nostr event")
		}
	}
	for _, entry := range logs.All() {
		contextJSON, _ := json.Marshal(entry.ContextMap())
		if strings.Contains(entry.Message, testRepositoryCredential) || strings.Contains(string(contextJSON), testRepositoryCredential) {
			t.Fatal("private Gitea credential leaked into logs")
		}
	}
}

func TestConformanceSourceProviderConfigFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name        string
		provider    string
		cloneURL    string
		username    string
		wantMessage string
	}{
		{name: "missing provider", wantMessage: "source provider"},
		{name: "unsupported provider", provider: "auto", wantMessage: "source provider"},
		{name: "Gitea missing clone URL", provider: SourceProviderGitea, username: "mirror-user", wantMessage: "source clone URL"},
		{name: "Gitea missing username", provider: SourceProviderGitea, cloneURL: "https://git.example/private/repo.git", wantMessage: "source auth username"},
		{name: "Gitea non-HTTPS clone URL", provider: SourceProviderGitea, cloneURL: "http://git.example/private/repo.git", username: "mirror-user", wantMessage: "absolute https URL"},
		{name: "GitHub foreign host", provider: SourceProviderGitHub, cloneURL: "https://attacker.example/private/repo.git", wantMessage: "must use github.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gitea := &fakeGitea{}
			server := httptest.NewServer(gitea.handler(t))
			defer server.Close()
			initiator, publisher, resolver, credentialRef, _ := newConformanceInitiator(t, server)
			initiator.cfg.SourceProvider = tc.provider
			initiator.cfg.SourceCloneURL = tc.cloneURL
			initiator.cfg.SourceAuthUsername = tc.username

			_, err := initiator.StartHiveCIBuild(context.Background(), arcanaStartRequest(credentialRef))
			if err == nil || !strings.Contains(err.Error(), tc.wantMessage) {
				t.Fatalf("expected fail-closed %q error, got %v", tc.wantMessage, err)
			}
			if len(resolver.calls) != 0 || gitea.migrateCalls != 0 || len(publisher.events) != 0 {
				t.Fatalf("invalid provider config caused side effects: resolves=%d migrations=%d events=%d", len(resolver.calls), gitea.migrateCalls, len(publisher.events))
			}
		})
	}
}

func TestValidateMirrorPreservesSourcePathCase(t *testing.T) {
	initiator := &Initiator{}
	err := initiator.validateMirror(&RepoInfo{
		Private:     true,
		Mirror:      true,
		OriginalURL: "https://git.sharegap.net/Fleet/Private.git",
	}, "https://GIT.SHAREGAP.NET/fleet/Private.git")
	if err == nil || !strings.Contains(err.Error(), "unexpected upstream") {
		t.Fatalf("case-sensitive source path mismatch must fail closed, got %v", err)
	}
}

func TestConformanceUnexpectedExistingMirrorFailsClosed(t *testing.T) {
	for _, originalURL := range []string{"", "https://git.sharegap.net/other/repository.git"} {
		t.Run(originalURL, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/fleet/living-library-forge" {
					_ = json.NewEncoder(w).Encode(map[string]any{
						"private": true, "mirror": true, "original_url": originalURL,
					})
					return
				}
				w.WriteHeader(http.StatusNotFound)
			}))
			defer server.Close()

			initiator, publisher, _, credentialRef, _ := newConformanceInitiator(t, server)
			_, err := initiator.StartHiveCIBuild(context.Background(), arcanaStartRequest(credentialRef))
			if err == nil || !strings.Contains(err.Error(), "unexpected upstream") {
				t.Fatalf("expected fail-closed upstream validation error, got %v", err)
			}
			if len(publisher.events) != 0 {
				t.Fatal("mismatched mirror must not publish build events")
			}
		})
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
