package loom

import (
	"context"
	"sync/atomic"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"go.uber.org/zap"
)

// remoteKeyer stands in for a NIP-46/NIP-55L service signer: no in-process
// key material, every operation counted.
type remoteKeyer struct {
	keyer.KeySigner
	signs, encryptions atomic.Int64
}

func (k *remoteKeyer) SignEvent(ctx context.Context, ev *nostr.Event) error {
	k.signs.Add(1)
	return k.KeySigner.SignEvent(ctx, ev)
}
func (k *remoteKeyer) Encrypt(ctx context.Context, plaintext string, to nostr.PubKey) (string, error) {
	k.encryptions.Add(1)
	return k.KeySigner.Encrypt(ctx, plaintext, to)
}

// TestSubmitJobSignsAndEncryptsThroughInjectedKeyer proves kind-5100 signing
// and NIP-44 job-secret encryption both go through the injected Keyer, and the
// worker can open the secrets with its own key.
func TestSubmitJobSignsAndEncryptsThroughInjectedKeyer(t *testing.T) {
	ctx := context.Background()
	remote := &remoteKeyer{KeySigner: keyer.NewPlainKeySigner(nostr.Generate())}
	worker := keyer.NewPlainKeySigner(nostr.Generate())
	workerPubkey, _ := worker.GetPublicKey(ctx)
	pool := &submitRelayPool{}
	client := &Client{pool: pool, signer: remote, submittedWorkers: make(map[string]string), logger: zap.NewNop()}

	if _, err := client.SubmitJob(ctx, JobRequest{Type: "build", WorkerPubkey: workerPubkey.Hex(), Secrets: map[string]string{"TOKEN": "s3cret"}}); err != nil {
		t.Fatalf("SubmitJob() error = %v", err)
	}
	if remote.signs.Load() != 1 || remote.encryptions.Load() != 1 {
		t.Fatalf("keyer signs=%d encryptions=%d, want 1 and 1", remote.signs.Load(), remote.encryptions.Load())
	}
	remotePubkey, _ := remote.GetPublicKey(ctx)
	ev := pool.published[0]
	if ev.PubKey != remotePubkey || !ev.VerifySignature() {
		t.Fatalf("kind-5100 not signed by the injected keyer")
	}
	for _, tag := range ev.Tags {
		if len(tag) == 3 && tag[0] == "secret" {
			plain, err := worker.Decrypt(ctx, tag[2], remotePubkey)
			if err != nil || plain != "s3cret" {
				t.Fatalf("worker cannot open secret: %q %v", plain, err)
			}
			return
		}
	}
	t.Fatalf("no secret tag in %v", ev.Tags)
}
