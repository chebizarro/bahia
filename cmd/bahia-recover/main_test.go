package main

import (
	"bytes"
	"context"
	"fmt"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"os"
	"path/filepath"
	"testing"
)

func TestPlanAndStatusDoNotCreateLedger(t *testing.T) {
	dir := t.TempDir()
	ledger := filepath.Join(dir, "recovery.bolt")
	cfgPath := filepath.Join(dir, "config.yaml")
	key := func() string {
		k, err := nostrutil.NewLocalKeyer(nostrutil.GeneratePrivateKeyHex())
		if err != nil {
			t.Fatal(err)
		}
		pub, err := k.GetPublicKey(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return pub.Hex()
	}
	old, root, next := key(), key(), key()
	yaml := fmt.Sprintf(`nostr:
  service_id: bahia-service-v1
  operator_root_pubkey: %s
  public_key: %s
  local_store:
    path: %s
identity_recovery:
  ledger_path: %s
  old_pubkey: %s
  reason_code: compromised
  controlled_relays: ["wss://controlled.example"]
  consumer_roster: [daemon]
  accepted_event_ids: []
  operator:
    public_key: %s
    signer:
      method: nip55l
  new_identity:
    public_key: %s
    signer:
      method: nip55l
`, root, old, filepath.Join(dir, "daemon.bolt"), ledger, old, root, next)
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"plan", "--config", cfgPath}, {"status", "--config", cfgPath}, {"apply", "--config", cfgPath}} {
		var out, errbuf bytes.Buffer
		code := run(context.Background(), args, &out, &errbuf)
		if args[0] == "apply" {
			if code != 2 {
				t.Fatalf("missing confirmation code=%d: %s", code, errbuf.String())
			}
		} else if code != 0 {
			t.Fatalf("%s code=%d: %s", args[0], code, errbuf.String())
		}
		if _, err := os.Stat(ledger); !os.IsNotExist(err) {
			t.Fatalf("%s mutated ledger: %v", args[0], err)
		}
	}
}
func TestMissingConfigFailsClosed(t *testing.T) {
	var out, errbuf bytes.Buffer
	if code := run(context.Background(), []string{"plan"}, &out, &errbuf); code == 0 {
		t.Fatal("accepted missing config")
	}
}
