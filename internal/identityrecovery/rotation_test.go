package identityrecovery

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
)

type rotationSource struct {
	refs []SecretRef
	err  error
}

func (s rotationSource) CredentialRefs(context.Context) ([]SecretRef, error) { return s.refs, s.err }

type secretViewFixture struct {
	secrets []domain.SecretRef
	org     string
}

func (v secretViewFixture) StoreBackedSecrets(context.Context) ([]domain.SecretRef, error) {
	return v.secrets, nil
}
func (v secretViewFixture) ServiceOrgID(context.Context, uuid.UUID) (string, error) {
	return v.org, nil
}

func TestRotationInventoryAndOperatorEvidence(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	ledger, err := OpenLedger(filepath.Join(dir, "ledger.bolt"), filepath.Join(dir, "daemon.bolt"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := ledger.Close(); err != nil {
			t.Error(err)
		}
	}()
	org, service := uuid.New(), uuid.New()
	old := domain.SecretCredentialRef{Store: "signet", CredentialID: "cred_" + strings.Repeat("a", 64), Version: 1}
	newer := domain.SecretCredentialRef{Store: "signet", CredentialID: "cred_" + strings.Repeat("b", 64), Version: 2}
	source := SecretRegistrySource{View: secretViewFixture{secrets: []domain.SecretRef{{ServiceID: service, Name: "TOKEN", CredentialRef: &old}}, org: org.String()}}
	canonical, err := source.CredentialRefs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(canonical) != 1 || canonical[0].Label != org.String()+"/"+service.String()+"/TOKEN" {
		t.Fatalf("canonical refs: %+v", canonical)
	}
	path := filepath.Join(dir, "updates.json")
	tracker := &LedgerRotationTracker{Ledger: ledger, Source: source, SystemKeys: config.SystemKeysConfig{AssistantTranscript: []config.SystemKeyConfig{{KeyID: "k1", CredentialID: "cred_" + strings.Repeat("c", 64), Version: 1}}}, UpdatesPath: path}
	refs, err := tracker.Inventory(ctx)
	if err != nil || len(refs) != 2 {
		t.Fatalf("inventory: %+v %v", refs, err)
	}
	if _, err = tracker.Rotate(ctx, refs); err == nil {
		t.Fatal("missing updates accepted")
	}
	updates := []RotationUpdate{{RefID: refs[0].ID, NewRef: &newer, SourceRotationEvidenceID: "source-receipt-1"}, {RefID: refs[1].ID, WaiverEvidenceID: "operator-waiver-1"}}
	raw, _ := json.Marshal(updates)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	receipts, err := tracker.Rotate(ctx, refs)
	if err != nil || len(receipts) != 2 {
		t.Fatalf("rotation: %v %+v", err, receipts)
	}
	items, err := ledger.RotationItems()
	if err != nil || len(items) != 2 {
		t.Fatalf("ledger: %v %+v", err, items)
	}
	if items[refs[0].ID].Replacement == nil || items[refs[0].ID].SourceRotationEvidenceID != "source-receipt-1" {
		t.Fatal("new reference not recorded")
	}
	if !strings.HasPrefix(receipts[refs[1].ID], "waiver:") {
		t.Fatal("waiver not distinguishable in signed receipt")
	}
	tracker.Source = rotationSource{refs: []SecretRef{canonical[0]}}
	tracker.SystemKeys = config.SystemKeysConfig{}
	retained, err := tracker.Inventory(ctx)
	if err != nil || len(retained) != 2 {
		t.Fatalf("old work items not retained after canonical repoint: %v %+v", err, retained)
	}
	tracker.Source = rotationSource{refs: []SecretRef{{Provider: "signet", Label: canonical[0].Label, Credential: newer}}}
	retained, err = tracker.Inventory(ctx)
	if err != nil || len(retained) != 2 {
		t.Fatalf("replacement incorrectly inventoried: %v %+v", err, retained)
	}
}

func TestRotationFailsClosedOnSourceAndUnchangedReference(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	ledger, err := OpenLedger(filepath.Join(dir, "ledger.bolt"), filepath.Join(dir, "daemon.bolt"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := ledger.Close(); err != nil {
			t.Error(err)
		}
	}()
	tracker := &LedgerRotationTracker{Ledger: ledger, Source: rotationSource{err: errors.New("canonical unavailable")}}
	if _, err := tracker.Inventory(ctx); err == nil {
		t.Fatal("source error ignored")
	}
	old := domain.SecretCredentialRef{Store: "signet", CredentialID: "cred_" + strings.Repeat("a", 64), Version: 1}
	tracker.Source = rotationSource{refs: []SecretRef{{Provider: "signet", Label: "bahia-system/test/k1", Credential: old}}}
	refs, err := tracker.Inventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tracker.UpdatesPath = filepath.Join(dir, "updates.json")
	raw, _ := json.Marshal([]RotationUpdate{{RefID: refs[0].ID, NewRef: &old, SourceRotationEvidenceID: "proof"}})
	if err := os.WriteFile(tracker.UpdatesPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := tracker.Rotate(ctx, refs); err == nil {
		t.Fatal("unchanged credential accepted")
	}
}
