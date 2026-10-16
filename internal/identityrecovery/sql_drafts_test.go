package identityrecovery

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	adapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

type fakeReviewRows struct {
	rows []SQLReviewRow
	err  error
}

func (f *fakeReviewRows) ListReviewRows(context.Context) ([]SQLReviewRow, error) {
	return f.rows, f.err
}

func sqlDraftFixture(t *testing.T) (*SQLReviewDraftAdapter, *registryTestRelay, *fakeReviewRows) {
	t.Helper()
	oldKey := signer(t)
	svc := &domain.Service{ID: uuid.New(), OrgID: uuid.New(), Name: "api"}
	kind, tags, content := adapter.ServiceRegistryRecoveryRecord(svc, false)
	old := signedRegistryHead(t, oldKey, kind, tags, content, 100)
	base, relay, local, _ := registryFixture(t, adapter.KindServiceRegistry, []nostr.Event{old})
	rows := &fakeReviewRows{rows: []SQLReviewRow{
		{Family: "service-registry", Table: "services", RowID: svc.ID.String(), Kind: kinds.CASControlState, Coordinate: svc.ID.String(), LegacyKind: strconv.Itoa(adapter.KindServiceRegistry)},
		{Family: "service-registry", Table: "services", RowID: uuid.NewString(), Kind: kinds.CASControlState, Coordinate: uuid.NewString(), LegacyKind: strconv.Itoa(adapter.KindServiceRegistry)},
	}}
	a := &SQLReviewDraftAdapter{OldPubkey: old.PubKey, Source: relay, Local: local, Ledger: base.Ledger, Rows: rows, ArtifactPath: filepath.Join(t.TempDir(), "drafts.json")}
	return a, relay, rows
}

func TestSQLReviewDraftReportOnlyIdempotentAndVerified(t *testing.T) {
	a, relay, rows := sqlDraftFixture(t)
	ctx := context.Background()
	ids, err := a.Inventory(ctx)
	if err != nil || len(ids) != 1 {
		t.Fatalf("inventory %v: %v", ids, err)
	}
	if _, err := a.Reassert(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if relay.publishes != 0 {
		t.Fatal("SQL draft reporter published an event")
	}
	data, err := os.ReadFile(a.ArtifactPath)
	if err != nil {
		t.Fatal(err)
	}
	var report SQLDraftReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	if report.ReviewedRows != 2 || len(report.Drafts) != 1 || report.Drafts[0].RequiredAction != "operator-signed intent after manual review" {
		t.Fatalf("report = %+v", report)
	}
	info, err := os.Stat(a.ArtifactPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("artifact permissions %v: %v", info, err)
	}
	if _, err := a.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Reassert(ctx, nil); err != nil {
		t.Fatal(err)
	}
	again, _ := os.ReadFile(a.ArtifactPath)
	if !reflect.DeepEqual(data, again) {
		t.Fatal("idempotent draft report changed")
	}
	rows.rows = append(rows.rows, SQLReviewRow{Family: "backup-recipe", Table: "backup_recipes", RowID: uuid.NewString(), Kind: kinds.CASControlState, Coordinate: "backup-recipe:" + uuid.NewString(), LegacyKind: strconv.Itoa(adapter.KindBackupRecipeRegistry)})
	if _, err := a.Verify(ctx); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("changed SQL snapshot verified: %v", err)
	}
}

func TestSQLReviewDraftRequiresEOSEAndSQL(t *testing.T) {
	a, relay, rows := sqlDraftFixture(t)
	relay.readErr = errors.New("EOSE incomplete")
	if _, err := a.Reassert(context.Background(), nil); err == nil {
		t.Fatal("EOSE failure accepted")
	}
	if _, err := os.Stat(a.ArtifactPath); !os.IsNotExist(err) {
		t.Fatalf("artifact after failed EOSE: %v", err)
	}
	relay.readErr = nil
	rows.err = errors.New("SQL unavailable")
	if _, err := a.Reassert(context.Background(), nil); err == nil {
		t.Fatal("SQL failure accepted as no candidates")
	}
}
