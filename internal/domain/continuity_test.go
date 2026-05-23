package domain

import (
	"errors"
	"testing"
)

func TestContinuityModeIsValid(t *testing.T) {
	valid := []ContinuityMode{
		ContinuityModeFull,
		ContinuityModeDegraded,
		ContinuityModeEmergency,
		ContinuityModeOffline,
	}
	for _, mode := range valid {
		if !mode.IsValid() {
			t.Fatalf("expected continuity mode %q to be valid", mode)
		}
	}
	if ContinuityMode("maintenance").IsValid() {
		t.Fatalf("unexpected valid continuity mode")
	}
}

func TestServiceContinuityProfileValidateRequiresServiceKey(t *testing.T) {
	profile := &ServiceContinuityProfile{
		ServiceKey: " ",
		Profiles: map[ContinuityMode]ContinuityProfileSpec{
			ContinuityModeFull: {},
		},
	}

	err := profile.Validate()
	if err == nil || !errors.Is(err, ErrEmptyField) {
		t.Fatalf("expected empty service_key error, got %v", err)
	}
}

func TestServiceContinuityProfileValidateRequiresAtLeastOneProfile(t *testing.T) {
	profile := &ServiceContinuityProfile{ServiceKey: "svc.api"}

	err := profile.Validate()
	if err == nil || !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("expected missing profiles error, got %v", err)
	}
}

func TestServiceContinuityProfileValidateRejectsUnknownMode(t *testing.T) {
	profile := &ServiceContinuityProfile{
		ServiceKey: "svc.api",
		Profiles: map[ContinuityMode]ContinuityProfileSpec{
			ContinuityMode("maintenance"): {},
		},
	}

	err := ValidateServiceContinuityProfile(profile)
	if err == nil || !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("expected invalid mode error, got %v", err)
	}
}

func TestServiceContinuityProfileValidateTrimsIdentityFields(t *testing.T) {
	profile := &ServiceContinuityProfile{
		ServiceKey:          " svc.api ",
		PrimaryWorkerPubKey: " primary-pubkey ",
		SourceEventID:       " event-id ",
		Profiles: map[ContinuityMode]ContinuityProfileSpec{
			ContinuityModeFull: {
				Requires:   []string{"cpu:x86_64", "data:primary"},
				Disables:   []string{"batch_jobs"},
				Limits:     map[string]string{"cpu": "4"},
				Attributes: map[string]string{"endpoint": "primary"},
			},
		},
	}

	if err := profile.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if profile.ServiceKey != "svc.api" {
		t.Fatalf("ServiceKey = %q", profile.ServiceKey)
	}
	if profile.PrimaryWorkerPubKey != "primary-pubkey" {
		t.Fatalf("PrimaryWorkerPubKey = %q", profile.PrimaryWorkerPubKey)
	}
	if profile.SourceEventID != "event-id" {
		t.Fatalf("SourceEventID = %q", profile.SourceEventID)
	}
}
