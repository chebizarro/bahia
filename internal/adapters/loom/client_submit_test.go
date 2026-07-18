package loom

import (
	"context"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"go.uber.org/zap"
)

type submitRelayPool struct {
	published []nostr.Event
	accepted  *int
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

func TestSubmitJob_InvalidWorkerPubkeyDoesNotPublish(t *testing.T) {
	pool := &submitRelayPool{}
	client := &Client{
		pool:             pool,
		privateKey:       nostrutil.GeneratePrivateKeyHex(),
		submittedWorkers: make(map[string]string),
		logger:           zap.NewNop(),
	}

	_, err := client.SubmitJob(context.Background(), JobRequest{
		WorkerPubkey: "not-a-pubkey",
		Secrets:      map[string]string{"TOKEN": "secret"},
	})
	if err == nil || !strings.Contains(err.Error(), "invalid Loom worker pubkey") {
		t.Fatalf("error = %v, want invalid worker pubkey", err)
	}
	if len(pool.published) != 0 {
		t.Fatalf("Publish called %d times, want 0", len(pool.published))
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
