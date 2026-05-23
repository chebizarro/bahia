package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	gonostr "github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/domain"
)

type relayFirstExtendedTestPublisher struct {
	accepted int
	err      error
	events   []gonostr.Event
}

func (p *relayFirstExtendedTestPublisher) Publish(_ context.Context, ev gonostr.Event) (int, error) {
	p.events = append(p.events, ev)
	if p.err != nil {
		return 0, p.err
	}
	return p.accepted, nil
}

type relayFirstExtendedTestSigner struct{}

func (relayFirstExtendedTestSigner) Sign(_ context.Context, ev *gonostr.Event) error {
	ev.ID = "signed-event"
	ev.PubKey = "pubkey"
	return nil
}

type fakeLLMDelegate struct{ calls int }

func (d *fakeLLMDelegate) CreateRoute(context.Context, *domain.LLMRoute) error {
	d.calls++
	return nil
}
func (d *fakeLLMDelegate) CreateDeploymentIntent(context.Context, *domain.LLMDeploymentIntent) error {
	d.calls++
	return nil
}
func (d *fakeLLMDelegate) RollbackWithMetadata(context.Context, uuid.UUID, uuid.UUID, string, map[string]any) (*domain.LLMDeploymentIntent, error) {
	d.calls++
	return &domain.LLMDeploymentIntent{ID: uuid.New()}, nil
}

func TestRelayFirstLLMPublishFailureBlocksMutation(t *testing.T) {
	publisher := &relayFirstExtendedTestPublisher{err: errors.New("relay rejected")}
	delegate := &fakeLLMDelegate{}
	wrapper := NewRelayFirstLLM(delegate, publisher, relayFirstExtendedTestSigner{}, nil)

	err := wrapper.CreateDeploymentIntent(context.Background(), &domain.LLMDeploymentIntent{ID: uuid.New(), RouteID: uuid.New(), EnvironmentID: uuid.New(), ReleaseID: uuid.New(), RequestedBy: "operator"})
	if err == nil {
		t.Fatal("expected publish failure")
	}
	if delegate.calls != 0 {
		t.Fatalf("delegate called after publish failure: %d", delegate.calls)
	}
}

func TestRelayFirstLLMSuccessfulMutationDelegates(t *testing.T) {
	publisher := &relayFirstExtendedTestPublisher{accepted: 1}
	delegate := &fakeLLMDelegate{}
	wrapper := NewRelayFirstLLM(delegate, publisher, relayFirstExtendedTestSigner{}, nil)

	_, err := wrapper.Rollback(context.Background(), uuid.New(), uuid.New(), "operator")
	if err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	if delegate.calls != 1 {
		t.Fatalf("delegate calls = %d, want 1", delegate.calls)
	}
	if len(publisher.events) != 1 || publisher.events[0].Kind != relayFirstKindLLMRollbackRequest {
		t.Fatalf("published events = %#v", publisher.events)
	}
}

type fakeBackupDelegate struct{ calls int }

func (d *fakeBackupDelegate) CreateOrUpdateRecipe(context.Context, *domain.BackupRecipe) error {
	d.calls++
	return nil
}

func TestRelayFirstBackupPublishFailureBlocksMutation(t *testing.T) {
	publisher := &relayFirstExtendedTestPublisher{accepted: 0}
	delegate := &fakeBackupDelegate{}
	wrapper := NewRelayFirstBackup(delegate, publisher, relayFirstExtendedTestSigner{}, nil)

	err := wrapper.CreateOrUpdateRecipe(context.Background(), &domain.BackupRecipe{ID: uuid.New(), Name: "daily", Version: "v1"})
	if err == nil {
		t.Fatal("expected no relay accepted error")
	}
	if delegate.calls != 0 {
		t.Fatalf("delegate called after publish failure: %d", delegate.calls)
	}
}

func TestRelayFirstBackupSuccessfulMutationDelegates(t *testing.T) {
	publisher := &relayFirstExtendedTestPublisher{accepted: 1}
	delegate := &fakeBackupDelegate{}
	wrapper := NewRelayFirstBackup(delegate, publisher, relayFirstExtendedTestSigner{}, nil)

	if err := wrapper.CreateOrUpdateRecipe(context.Background(), &domain.BackupRecipe{ID: uuid.New(), Name: "daily", Version: "v1"}); err != nil {
		t.Fatalf("CreateOrUpdateRecipe() error = %v", err)
	}
	if delegate.calls != 1 {
		t.Fatalf("delegate calls = %d, want 1", delegate.calls)
	}
}

type fakePackageDelegate struct{ calls int }

func (d *fakePackageDelegate) EnsureRepository(_ context.Context, repo *domain.PackageRepository, _ *domain.PackageRepository) (*domain.PackageRepository, error) {
	d.calls++
	return repo, nil
}

func TestRelayFirstPackagePublishFailureBlocksMutation(t *testing.T) {
	publisher := &relayFirstExtendedTestPublisher{err: errors.New("relay unavailable")}
	delegate := &fakePackageDelegate{}
	wrapper := NewRelayFirstPackage(delegate, publisher, relayFirstExtendedTestSigner{}, nil)

	_, err := wrapper.EnsureRepository(context.Background(), &domain.PackageRepository{ID: uuid.New(), Name: "charts"}, nil)
	if err == nil {
		t.Fatal("expected publish failure")
	}
	if delegate.calls != 0 {
		t.Fatalf("delegate called after publish failure: %d", delegate.calls)
	}
}

func TestRelayFirstPackageSuccessfulMutationDelegates(t *testing.T) {
	publisher := &relayFirstExtendedTestPublisher{accepted: 1}
	delegate := &fakePackageDelegate{}
	wrapper := NewRelayFirstPackage(delegate, publisher, relayFirstExtendedTestSigner{}, nil)

	_, err := wrapper.EnsureRepository(context.Background(), &domain.PackageRepository{ID: uuid.New(), Name: "charts"}, nil)
	if err != nil {
		t.Fatalf("EnsureRepository() error = %v", err)
	}
	if delegate.calls != 1 {
		t.Fatalf("delegate calls = %d, want 1", delegate.calls)
	}
}

type fakeDNSDelegate struct{ calls int }

func (d *fakeDNSDelegate) CreateZone(context.Context, domain.DNSZone) error {
	d.calls++
	return nil
}
func (d *fakeDNSDelegate) CreateOverride(context.Context, domain.DNSRecordOverride) error {
	d.calls++
	return nil
}

func TestRelayFirstDNSPublishFailureBlocksMutation(t *testing.T) {
	publisher := &relayFirstExtendedTestPublisher{accepted: 0}
	delegate := &fakeDNSDelegate{}
	wrapper := NewRelayFirstDNS(delegate, publisher, relayFirstExtendedTestSigner{}, nil)

	err := wrapper.CreateZone(context.Background(), domain.DNSZone{Name: "prod.example", BackendRef: "edge"})
	if err == nil {
		t.Fatal("expected no relay accepted error")
	}
	if delegate.calls != 0 {
		t.Fatalf("delegate called after publish failure: %d", delegate.calls)
	}
}

func TestRelayFirstDNSSuccessfulMutationDelegates(t *testing.T) {
	publisher := &relayFirstExtendedTestPublisher{accepted: 1}
	delegate := &fakeDNSDelegate{}
	wrapper := NewRelayFirstDNS(delegate, publisher, relayFirstExtendedTestSigner{}, nil)

	err := wrapper.CreateOverride(context.Background(), domain.DNSRecordOverride{ID: uuid.New(), ZoneName: "prod.example", RecordName: "api", RecordType: domain.DNSRecordTypeA, Value: "192.0.2.10", TTL: 60, Reason: "incident pin", OperatorPubkey: "operator"})
	if err != nil {
		t.Fatalf("CreateOverride() error = %v", err)
	}
	if delegate.calls != 1 {
		t.Fatalf("delegate calls = %d, want 1", delegate.calls)
	}
}

type fakeMLDelegate struct{ calls int }

func (d *fakeMLDelegate) CreateOrUpdateModel(context.Context, *domain.MLModel) error {
	d.calls++
	return nil
}

func TestRelayFirstMLPassThroughDelegates(t *testing.T) {
	delegate := &fakeMLDelegate{}
	wrapper := NewRelayFirstML(delegate, &relayFirstExtendedTestPublisher{err: errors.New("should not publish")}, nil, nil)

	if err := wrapper.CreateOrUpdateModel(context.Background(), &domain.MLModel{}); err != nil {
		t.Fatalf("CreateOrUpdateModel() error = %v", err)
	}
	if delegate.calls != 1 {
		t.Fatalf("delegate calls = %d, want 1", delegate.calls)
	}
}
