package identityrecovery

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDurableContainmentEvidenceAndOperatorException(t *testing.T) {
	dir := t.TempDir()
	ledger, err := OpenLedger(filepath.Join(dir, "ledger.bolt"), filepath.Join(dir, "daemon.bolt"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	id := "019a0000-0000-7000-8000-000000000001"
	if err := ledger.Save(State{IncidentID: id}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "outbox.identity-containment")
	a := DurableContainmentAdapter{Ledger: ledger, MarkerPath: path}
	if _, err := a.Contain(context.Background()); err == nil {
		t.Fatal("missing marker accepted")
	}
	a.ConfirmDaemonUnreachable = true
	if proof, err := a.Contain(context.Background()); err != nil || len(proof) != 1 || proof[0] != "containment:daemon-unreachable:operator-confirmed:"+id {
		t.Fatalf("unreachable proof: %v %v", proof, err)
	}
	if err := os.WriteFile(path, []byte(`{"incident_id":"`+id+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if proof, err := a.Contain(context.Background()); err != nil || len(proof) != 1 || proof[0] != "containment:runtime-kill-switch:"+id {
		t.Fatalf("marker proof: %v %v", proof, err)
	}
	if err := os.WriteFile(path, []byte(`{"incident_id":"different"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Contain(context.Background()); err == nil {
		t.Fatal("wrong marker accepted with operator flag")
	}
}
