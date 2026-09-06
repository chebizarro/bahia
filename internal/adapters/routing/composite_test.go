package routing

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
)

type compositePublicStub struct {
	events          *[]string
	applyErr        error
	compensationErr error
}

func (s *compositePublicStub) Check(context.Context, *domain.DesiredPublicRoutePlan) error {
	*s.events = append(*s.events, "public-check")
	return nil
}
func (s *compositePublicStub) Apply(context.Context, *domain.DesiredPublicRoutePlan) error {
	*s.events = append(*s.events, "public-apply-direct")
	return s.applyErr
}
func (s *compositePublicStub) ApplyWithCompensation(context.Context, *domain.DesiredPublicRoutePlan) (Compensation, error) {
	*s.events = append(*s.events, "public-apply")
	if s.applyErr != nil {
		return nil, s.applyErr
	}
	return func(context.Context) error {
		*s.events = append(*s.events, "public-rollback")
		return s.compensationErr
	}, nil
}

type compositeInternalStub struct {
	events   *[]string
	applyErr error
}

func (s *compositeInternalStub) Check(context.Context, *domain.DesiredPublicRoutePlan) error {
	*s.events = append(*s.events, "internal-check")
	return nil
}
func (s *compositeInternalStub) Apply(context.Context, *domain.DesiredPublicRoutePlan) error {
	*s.events = append(*s.events, "internal-apply")
	if s.applyErr != nil {
		// Backend implementations restore their own partial mutation before
		// returning an error; make that inverse visible in this ordering test.
		*s.events = append(*s.events, "internal-rollback")
	}
	return s.applyErr
}

func TestCompositeCheckAndApplyOrdering(t *testing.T) {
	var events []string
	backend, err := NewCompositeBackend(&compositePublicStub{events: &events}, &compositeInternalStub{events: &events})
	if err != nil {
		t.Fatal(err)
	}
	plan := cloudflareTestPlan()
	if err := backend.Check(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(events, []string{"public-check", "internal-check"}) {
		t.Fatalf("check order = %#v", events)
	}
	events = nil
	if err := backend.Apply(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(events, []string{"internal-check", "public-apply", "internal-apply"}) {
		t.Fatalf("apply order = %#v", events)
	}
}

func TestCompositeInternalFailureCompensatesInReverseOrder(t *testing.T) {
	var events []string
	backend, err := NewCompositeBackend(
		&compositePublicStub{events: &events},
		&compositeInternalStub{events: &events, applyErr: errors.New("nginx reload failed; previous internal route restored")},
	)
	if err != nil {
		t.Fatal(err)
	}
	err = backend.Apply(context.Background(), cloudflareTestPlan())
	if err == nil || !strings.Contains(err.Error(), "previous public route restored") {
		t.Fatalf("Apply error = %v", err)
	}
	want := []string{"internal-check", "public-apply", "internal-apply", "internal-rollback", "public-rollback"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("compensation order = %#v, want %#v", events, want)
	}
}

func TestCompositeReportsCrossCompensationFailure(t *testing.T) {
	var events []string
	backend, err := NewCompositeBackend(
		&compositePublicStub{events: &events, compensationErr: errors.New("cloudflare restore failed")},
		&compositeInternalStub{events: &events, applyErr: errors.New("nginx failed")},
	)
	if err != nil {
		t.Fatal(err)
	}
	err = backend.Apply(context.Background(), cloudflareTestPlan())
	if err == nil || !strings.Contains(err.Error(), "nginx failed") || !strings.Contains(err.Error(), "cloudflare restore failed") {
		t.Fatalf("Apply error = %v", err)
	}
}
