package loom

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type submitRelayPool struct {
	published []nostr.Event
	accepted  *int
}

type submitWorkerRepo struct {
	workers []domain.Worker
}

func (r *submitWorkerRepo) Upsert(context.Context, *domain.Worker) error { return nil }
func (r *submitWorkerRepo) GetByPubKey(context.Context, string) (*domain.Worker, error) {
	return nil, nil
}
func (r *submitWorkerRepo) List(context.Context, string, int) ([]domain.Worker, error) {
	return append([]domain.Worker(nil), r.workers...), nil
}
func (r *submitWorkerRepo) UpdateStatus(context.Context, string, domain.WorkerStatus) error {
	return nil
}

func (p *submitRelayPool) Publish(_ context.Context, event nostr.Event) (int, error) {
	p.published = append(p.published, event)
	if p.accepted != nil {
		return *p.accepted, nil
	}
	return 1, nil
}

func (p *submitRelayPool) SubscribeAllWithEOSE(context.Context, []nostr.Filter) (*nostrAdapter.MergedSubscription, error) {
	panic("not used")
}

func (p *submitRelayPool) AuthenticateRelay(context.Context, string) error {
	panic("not used")
}

func TestSubmitAndCancelRejectZeroRelayAcceptance(t *testing.T) {
	zero := 0
	pool := &submitRelayPool{accepted: &zero}
	client := &Client{
		pool:             pool,
		privateKey:       nostrutil.GeneratePrivateKeyHex(),
		submittedWorkers: make(map[string]string),
		logger:           zap.NewNop(),
	}

	eventID, err := client.SubmitJob(t.Context(), JobRequest{Service: "api"})
	if err == nil || !strings.Contains(err.Error(), "no relay accepted") {
		t.Fatalf("SubmitJob() = (%q, %v), want zero-relay error", eventID, err)
	}
	if eventID != "" || len(client.submittedWorkers) != 0 {
		t.Fatalf("unaccepted job was recorded: event=%q workers=%#v", eventID, client.submittedWorkers)
	}
	if err := client.CancelJob(t.Context(), strings.Repeat("a", 64), ""); err == nil || !strings.Contains(err.Error(), "no relay accepted") {
		t.Fatalf("CancelJob() error = %v, want zero-relay error", err)
	}
}

func TestSubmitJob_SecretsWithoutResolvedWorkerFailClosed(t *testing.T) {
	pool := &submitRelayPool{}
	client := &Client{
		pool:             pool,
		privateKey:       nostrutil.GeneratePrivateKeyHex(),
		submittedWorkers: make(map[string]string),
		logger:           zap.NewNop(),
	}

	eventID, err := client.SubmitJob(context.Background(), JobRequest{
		Service: "api",
		Secrets: map[string]string{"DATABASE_PASSWORD": "super-secret"},
	})
	if err == nil {
		t.Fatal("expected secret-bearing job without a worker pubkey to fail closed")
	}
	if !strings.Contains(err.Error(), "secrets") || !strings.Contains(err.Error(), "worker") {
		t.Fatalf("error = %q, want secret delivery/worker context", err)
	}
	if eventID != "" {
		t.Fatalf("event ID = %q, want empty", eventID)
	}
	if len(pool.published) != 0 {
		t.Fatalf("Publish called %d times, want 0", len(pool.published))
	}
}

func TestSubmitJob_RequirementsWithoutWorkerRepositoryFailClosed(t *testing.T) {
	pool := &submitRelayPool{}
	client := &Client{
		pool:             pool,
		privateKey:       nostrutil.GeneratePrivateKeyHex(),
		submittedWorkers: make(map[string]string),
		logger:           zap.NewNop(),
	}

	eventID, err := client.SubmitJob(t.Context(), JobRequest{
		RequiredWorkloads:    []string{"ci/workflow-run"},
		RequiredFeatures:     []string{"hive_ci_profile"},
		AllowedWorkerPubkeys: []string{strings.Repeat("a", 64)},
	})
	if err == nil || !strings.Contains(err.Error(), "worker repository") {
		t.Fatalf("SubmitJob() = (%q, %v), want missing worker repository error", eventID, err)
	}
	if eventID != "" || len(pool.published) != 0 {
		t.Fatalf("unsatisfied placement published a job: event=%q publishes=%d", eventID, len(pool.published))
	}
}

func TestSubmitJob_UsesInjectedControlPlaneSigner(t *testing.T) {
	pool := &submitRelayPool{}
	fallbackKey := nostrutil.GeneratePrivateKeyHex()
	controlPlaneKey := nostrutil.GeneratePrivateKeyHex()
	decoded, err := hex.DecodeString(controlPlaneKey)
	if err != nil {
		t.Fatalf("decode control-plane key: %v", err)
	}
	var secret [32]byte
	copy(secret[:], decoded)
	controlPlaneSigner := keyer.NewPlainKeySigner(secret)
	client := &Client{
		pool:             pool,
		privateKey:       fallbackKey,
		jobSigner:        controlPlaneSigner,
		submittedWorkers: make(map[string]string),
		logger:           zap.NewNop(),
	}

	if _, err := client.SubmitJob(t.Context(), JobRequest{Type: "build"}); err != nil {
		t.Fatalf("SubmitJob() error = %v", err)
	}
	wantPubkey, err := controlPlaneSigner.GetPublicKey(t.Context())
	if err != nil {
		t.Fatalf("control-plane public key: %v", err)
	}
	fallbackPubkey, err := nostrutil.PublicKeyHexFromPrivateKeyHex(fallbackKey)
	if err != nil {
		t.Fatalf("fallback public key: %v", err)
	}
	if len(pool.published) != 1 || pool.published[0].PubKey.Hex() != wantPubkey.Hex() {
		t.Fatalf("published signer = %v, want control-plane pubkey %s", pool.published, wantPubkey.Hex())
	}
	if pool.published[0].PubKey.Hex() == fallbackPubkey {
		t.Fatalf("kind-5100 was signed by fallback raw key %s", fallbackPubkey)
	}
}

func TestSubmitJob_InvalidWorkerPubkeyDoesNotPublish(t *testing.T) {
	pool := &submitRelayPool{}
	client := &Client{
		pool:             pool,
		privateKey:       nostrutil.GeneratePrivateKeyHex(),
		submittedWorkers: make(map[string]string),
		logger:           zap.NewNop(),
	}

	plaintext := "must-not-leak"
	_, err := client.SubmitJob(context.Background(), JobRequest{
		WorkerPubkey: "not-a-pubkey",
		Secrets:      map[string]string{"TOKEN": plaintext},
	})
	if err == nil || !strings.Contains(err.Error(), "invalid Loom worker pubkey") {
		t.Fatalf("error = %v, want invalid worker pubkey", err)
	}
	if len(pool.published) != 0 {
		t.Fatalf("Publish called %d times, want 0", len(pool.published))
	}
	if strings.Contains(err.Error(), plaintext) {
		t.Fatalf("plaintext secret leaked into error: %v", err)
	}
}

func TestSubmitJob_SecretsAreEncryptedForResolvedWorkerBeforePublish(t *testing.T) {
	pool := &submitRelayPool{}
	client := &Client{
		pool:             pool,
		privateKey:       nostrutil.GeneratePrivateKeyHex(),
		submittedWorkers: make(map[string]string),
		logger:           zap.NewNop(),
	}
	workerSecret := nostrutil.GeneratePrivateKeyHex()
	workerPubkey, err := nostrutil.PublicKeyHexFromPrivateKeyHex(workerSecret)
	if err != nil {
		t.Fatalf("derive worker pubkey: %v", err)
	}

	_, err = client.SubmitJob(context.Background(), JobRequest{
		WorkerPubkey: workerPubkey,
		Secrets:      map[string]string{"TOKEN": "super-secret"},
	})
	if err != nil {
		t.Fatalf("SubmitJob() error = %v", err)
	}
	if len(pool.published) != 1 {
		t.Fatalf("published events = %d, want 1", len(pool.published))
	}
	if got := getTagValue(pool.published[0].Tags, tagJobPubkey); got != workerPubkey {
		t.Fatalf("worker tag = %q, want %q", got, workerPubkey)
	}
	var encrypted string
	for _, tag := range pool.published[0].Tags {
		if len(tag) == 3 && tag[0] == "secret" && tag[1] == "TOKEN" {
			encrypted = tag[2]
			break
		}
	}
	if encrypted == "" || encrypted == "super-secret" {
		t.Fatalf("secret tag was not encrypted: %q", encrypted)
	}
}

func TestSubmitJob_ProjectsBoundedProfileParamsAsTags(t *testing.T) {
	pool := &submitRelayPool{}
	client := &Client{
		pool:             pool,
		privateKey:       nostrutil.GeneratePrivateKeyHex(),
		submittedWorkers: make(map[string]string),
		logger:           zap.NewNop(),
	}

	_, err := client.SubmitJob(context.Background(), JobRequest{
		Cmd: "ci/workflow-run",
		Params: map[string]string{
			"repo":     "https://git.sharegap.net/cascadia/loom-worker.git",
			"ref":      "main",
			"run":      "5401-event-id",
			"workflow": ".github/workflows/package-deb.yaml",
			"payment":  "forged-payment",
			"p":        strings.Repeat("f", 64),
			"secret":   "forged-secret",
		},
	})
	if err != nil {
		t.Fatalf("SubmitJob() error = %v", err)
	}
	if len(pool.published) != 1 {
		t.Fatalf("published events = %d, want 1", len(pool.published))
	}
	event := pool.published[0]
	for key, want := range map[string]string{
		"repo":     "https://git.sharegap.net/cascadia/loom-worker.git",
		"ref":      "main",
		"run":      "5401-event-id",
		"workflow": ".github/workflows/package-deb.yaml",
	} {
		if got := getTagValue(event.Tags, key); got != want {
			t.Fatalf("%s tag = %q, want %q", key, got, want)
		}
	}
	for _, forbidden := range []string{"payment", "p", "secret"} {
		if got := getTagValue(event.Tags, forbidden); got != "" {
			t.Fatalf("forbidden %s tag projected as %q", forbidden, got)
		}
	}
}

func TestSubmitJob_HiveCIShapeSelectsCapableWorkerEncryptsSecretsAndOmitsPayment(t *testing.T) {
	pool := &submitRelayPool{}
	logCore, logs := observer.New(zap.DebugLevel)
	senderPrivateKey := nostrutil.GeneratePrivateKeyHex()
	uncapablePrivateKey := nostrutil.GeneratePrivateKeyHex()
	uncapablePubkey, err := nostrutil.PublicKeyHexFromPrivateKeyHex(uncapablePrivateKey)
	if err != nil {
		t.Fatalf("derive uncapable pubkey: %v", err)
	}
	capablePrivateKey := nostrutil.GeneratePrivateKeyHex()
	capablePubkey, err := nostrutil.PublicKeyHexFromPrivateKeyHex(capablePrivateKey)
	if err != nil {
		t.Fatalf("derive capable pubkey: %v", err)
	}
	software := []domain.WorkerSoftware{{Name: "git"}, {Name: "act"}, {Name: "docker"}}
	workers := &submitWorkerRepo{workers: []domain.Worker{
		{
			PubKey: uncapablePubkey, Status: domain.WorkerStatusOnline, SchedulingState: domain.WorkerSchedulingActive,
			MaxConcurrentJobs: 1, Software: software,
			Capabilities: domain.WorkerCapabilities{WorkloadKinds: []string{"ci/workflow-run"}},
		},
		{
			PubKey: capablePubkey, Status: domain.WorkerStatusOnline, SchedulingState: domain.WorkerSchedulingActive,
			MaxConcurrentJobs: 1, Software: software,
			Capabilities: domain.WorkerCapabilities{
				WorkloadKinds: []string{"ci/workflow-run"}, Features: []string{"hive_ci_profile"},
			},
		},
	}}
	client := &Client{
		pool: pool, workerRepo: workers, privateKey: senderPrivateKey,
		submittedWorkers: make(map[string]string), logger: zap.New(logCore),
	}
	runEventID := strings.Repeat("12", 32)
	plaintext := "private-clone-credential"
	_, err = client.SubmitJob(context.Background(), JobRequest{
		ReferencedEventID:    runEventID,
		Secrets:              map[string]string{"GIT_CREDENTIAL": plaintext},
		RequiredSoftware:     []string{"git", "act", "docker"},
		RequiredWorkloads:    []string{"ci/workflow-run"},
		RequiredFeatures:     []string{"hive_ci_profile"},
		AllowedWorkerPubkeys: []string{uncapablePubkey, capablePubkey},
		Params: map[string]string{
			"method": "ci/workflow-run", "run": runEventID,
			"repo": "https://git.fleet.internal/fleet/repository.git",
			"ref":  "main", "workflow": ".github/workflows/build.yml",
		},
	})
	if err != nil {
		t.Fatalf("SubmitJob() error = %v", err)
	}
	if len(pool.published) != 1 {
		t.Fatalf("published events = %d, want 1", len(pool.published))
	}
	event := pool.published[0]
	if int(event.Kind) != KindJobRequest || getTagValue(event.Tags, tagJobPubkey) != capablePubkey ||
		getTagValue(event.Tags, "method") != "ci/workflow-run" || getTagValue(event.Tags, tagJobEvent) != runEventID {
		t.Fatalf("unexpected Hive-CI 5100 shape: kind=%d tags=%v", event.Kind, event.Tags)
	}
	if getTagValue(event.Tags, "payment") != "" {
		t.Fatalf("fleet-internal Hive-CI request carried a payment tag: %v", event.Tags)
	}
	encoded := event.Content + fmt.Sprint(event.Tags)
	if strings.Contains(encoded, plaintext) {
		t.Fatalf("plaintext secret leaked into kind-5100 event")
	}
	for _, entry := range logs.All() {
		fields, _ := json.Marshal(entry.ContextMap())
		if strings.Contains(entry.Message, plaintext) || strings.Contains(string(fields), plaintext) {
			t.Fatalf("plaintext secret leaked into Loom logs")
		}
	}
	secretFound := false
	for _, tag := range event.Tags {
		if len(tag) == 3 && tag[0] == "secret" && tag[1] == "GIT_CREDENTIAL" && tag[2] != "" && tag[2] != plaintext {
			secretFound = true
		}
	}
	if !secretFound {
		t.Fatalf("encrypted secret tag missing: %v", event.Tags)
	}
}
