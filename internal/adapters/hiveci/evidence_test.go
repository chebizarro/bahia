package hiveci

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
)

type admissionEventRepo struct {
	record  *repository.NostrEventRecord
	records map[string]repository.NostrEventRecord
}

func (r *admissionEventRepo) Record(_ context.Context, record *repository.NostrEventRecord) (bool, error) {
	if r.records == nil {
		r.records = map[string]repository.NostrEventRecord{}
	}
	if _, exists := r.records[record.ID]; exists {
		return false, nil
	}
	r.records[record.ID] = *record
	return true, nil
}
func (r *admissionEventRepo) GetByID(_ context.Context, id string) (*repository.NostrEventRecord, error) {
	if r.record != nil && r.record.ID == id {
		return r.record, nil
	}
	if record, exists := r.records[id]; exists {
		copy := record
		return &copy, nil
	}
	return nil, nil
}
func (*admissionEventRepo) ListByKind(context.Context, int, int) ([]repository.NostrEventRecord, error) {
	return nil, nil
}
func (*admissionEventRepo) ListByKinds(context.Context, []int, int) ([]repository.NostrEventRecord, error) {
	return nil, nil
}
func (r *admissionEventRepo) FindByTag(_ context.Context, name, value string, kinds []int, limit int) ([]repository.NostrEventRecord, error) {
	allowed := map[int]bool{}
	for _, kind := range kinds {
		allowed[kind] = true
	}
	var found []repository.NostrEventRecord
	for _, record := range r.records {
		if !allowed[record.Kind] {
			continue
		}
		var tags nostr.Tags
		if json.Unmarshal(record.Tags, &tags) != nil {
			continue
		}
		for _, tag := range tags {
			if len(tag) >= 2 && tag[0] == name && tag[1] == value {
				found = append(found, record)
				break
			}
		}
		if limit > 0 && len(found) >= limit {
			break
		}
	}
	return found, nil
}
func (*admissionEventRepo) ListByEntity(context.Context, string, uuid.UUID, int) ([]repository.NostrEventRecord, error) {
	return nil, nil
}
func (*admissionEventRepo) LatestCreatedAtForKinds(context.Context, []int) (*time.Time, error) {
	return nil, nil
}
func (*admissionEventRepo) LatestCreatedAtForKindsAndAuthors(context.Context, []int, []string) (*time.Time, error) {
	return nil, nil
}

type admissionWorkerRepo struct {
	worker *domain.Worker
}

func (*admissionWorkerRepo) Upsert(context.Context, *domain.Worker) error { return nil }
func (r *admissionWorkerRepo) GetByPubKey(context.Context, string) (*domain.Worker, error) {
	return r.worker, nil
}
func (*admissionWorkerRepo) List(context.Context, string, int) ([]domain.Worker, error) {
	return nil, nil
}
func (*admissionWorkerRepo) UpdateStatus(context.Context, string, domain.WorkerStatus) error {
	return nil
}

func TestRepositoryReleaseEvidenceBindsCurrentSignedWorkerAdvertisementAndCapability(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	key := nostr.Generate()
	ad := &nostr.Event{
		Kind: kinds.LoomWorkerAdvertisement, CreatedAt: nostr.Timestamp(now.Add(-time.Minute).Unix()),
		Tags:    nostr.Tags{{"S", "docker", "1"}, {"A", "linux-amd64"}},
		Content: `{"name":"release-worker"}`,
	}
	if err := ad.Sign(key); err != nil {
		t.Fatal(err)
	}
	tags, err := json.Marshal(ad.Tags)
	if err != nil {
		t.Fatal(err)
	}
	events := &admissionEventRepo{record: &repository.NostrEventRecord{
		ID: ad.ID.Hex(), Kind: int(ad.Kind), PubKey: ad.PubKey.Hex(), Content: ad.Content,
		Tags: tags, Sig: hex.EncodeToString(ad.Sig[:]), CreatedAt: ad.CreatedAt.Time(), ReceivedAt: now,
	}}
	worker := &domain.Worker{
		PubKey: ad.PubKey.Hex(), LastAdvertisementAt: ad.CreatedAt.Time(),
		Status: domain.WorkerStatusOnline, SchedulingState: domain.WorkerSchedulingActive,
		Pressure: &domain.WorkerPressureAssessment{
			CapacityClass: domain.WorkerCapacityOpen, OverallLevel: domain.WorkerPressureNominal,
		},
	}
	evidence := NewRepositoryReleaseEvidence(events, nil, &admissionWorkerRepo{worker: worker}, nil)
	evidence.now = func() time.Time { return now }
	capabilityBytes, _ := json.Marshal(ad.Tags)
	admission, admitted, err := evidence.AdmitWorker(
		context.Background(), ad.PubKey.Hex(), string(capabilityBytes), ad.ID.Hex(),
	)
	if err != nil || !admitted {
		t.Fatalf("admission=%+v admitted=%v err=%v", admission, admitted, err)
	}
	if admission.WorkerAdEventID != ad.ID.Hex() || admission.DecisionCode != "eligible" {
		t.Fatalf("unexpected admission evidence: %+v", admission)
	}

	t.Run("capability mismatch", func(t *testing.T) {
		_, _, err := evidence.AdmitWorker(context.Background(), ad.PubKey.Hex(), `[["S","other"]]`, ad.ID.Hex())
		if err == nil {
			t.Fatal("unsigned capability was admitted")
		}
	})
	t.Run("superseded advertisement", func(t *testing.T) {
		worker.LastAdvertisementAt = ad.CreatedAt.Time().Add(time.Second)
		_, _, err := evidence.AdmitWorker(context.Background(), ad.PubKey.Hex(), string(capabilityBytes), ad.ID.Hex())
		if err == nil {
			t.Fatal("superseded worker advertisement was admitted")
		}
	})
	t.Run("cordoned worker", func(t *testing.T) {
		worker.LastAdvertisementAt = ad.CreatedAt.Time()
		worker.SchedulingState = domain.WorkerSchedulingCordoned
		_, admitted, err := evidence.AdmitWorker(context.Background(), ad.PubKey.Hex(), string(capabilityBytes), ad.ID.Hex())
		if err != nil {
			t.Fatal(err)
		}
		if admitted {
			t.Fatal("cordoned worker was admitted")
		}
	})
}
