package hiveci

import (
	"context"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	cascadia "git.sharegap.net/cascadia/cascadia-go"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
)

type auditStoreFake struct {
	records []*repository.NostrEventRecord
}

func (s *auditStoreFake) Record(_ context.Context, record *repository.NostrEventRecord) (bool, error) {
	s.records = append(s.records, record)
	return true, nil
}

func TestRegistrationAuditStoresSignedCanonicalOutboxEvidence(t *testing.T) {
	fixture := newReleaseFixture(t)
	store := &auditStoreFake{}
	audit := NewRegistrationAudit(keyer.NewPlainKeySigner(nostr.Generate()), store)
	audit.now = func() time.Time { return fixture.now }
	if err := audit.AuditReleaseRegistration(context.Background(),
		domainReleaseCommit(fixture), nil, "accepted", nil); err != nil {
		t.Fatal(err)
	}
	if err := audit.AuditReleaseRejection(context.Background(), fixture.event, ErrInvalidRelease); err != nil {
		t.Fatal(err)
	}
	if len(store.records) != 2 {
		t.Fatalf("audit records=%d, want 2", len(store.records))
	}
	for _, record := range store.records {
		if record.Kind != cascadia.CAS_AUDIT || record.PublishState != repository.NostrPublishStatePending ||
			record.ID == "" || record.PubKey == "" || record.Sig == "" {
			t.Fatalf("invalid signed audit outbox record: %+v", record)
		}
	}
}

func domainReleaseCommit(f *releaseFixture) domain.HiveCIAcceptedRelease {
	commit, err := f.ingestor.Ingest(context.Background(), f.event)
	if err != nil {
		panic(err)
	}
	return commit.Release
}
