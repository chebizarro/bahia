package domain

import (
	"fmt"
	"strings"
	"time"
)

// ContinuityMode identifies a service's operating profile during normal and degraded operation.
type ContinuityMode string

const (
	ContinuityModeFull      ContinuityMode = "full"
	ContinuityModeDegraded  ContinuityMode = "degraded"
	ContinuityModeEmergency ContinuityMode = "emergency"
	ContinuityModeOffline   ContinuityMode = "offline"
)

// ServiceContinuityProfile is the authoritative continuity profile definition for a service.
type ServiceContinuityProfile struct {
	ServiceKey          string                                   `json:"service_key"`
	PrimaryWorkerPubKey string                                   `json:"primary_worker_pubkey,omitempty"`
	Profiles            map[ContinuityMode]ContinuityProfileSpec `json:"profiles"`
	UpdatedAt           time.Time                                `json:"updated_at"`
	SourceEventID       string                                   `json:"source_event_id,omitempty"`
}

// ContinuityProfileSpec describes requirements and behavior for one continuity mode.
type ContinuityProfileSpec struct {
	Requires   []string          `json:"requires,omitempty"`
	Disables   []string          `json:"disables,omitempty"`
	Limits     map[string]string `json:"limits,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

// IsValid reports whether the mode is a supported continuity profile value.
func (m ContinuityMode) IsValid() bool {
	switch m {
	case ContinuityModeFull, ContinuityModeDegraded, ContinuityModeEmergency, ContinuityModeOffline:
		return true
	default:
		return false
	}
}

// Validate checks that a service continuity profile has the required identity and profile data.
func (p *ServiceContinuityProfile) Validate() error {
	if p == nil {
		return fmt.Errorf("%w: service continuity profile must not be nil", ErrInvalidValue)
	}
	p.ServiceKey = strings.TrimSpace(p.ServiceKey)
	p.PrimaryWorkerPubKey = strings.TrimSpace(p.PrimaryWorkerPubKey)
	p.SourceEventID = strings.TrimSpace(p.SourceEventID)
	if err := ValidateRequiredString(p.ServiceKey, "service_key"); err != nil {
		return err
	}
	if len(p.Profiles) == 0 {
		return fmt.Errorf("%w: at least one continuity profile must be defined", ErrInvalidValue)
	}
	for mode := range p.Profiles {
		if !mode.IsValid() {
			return fmt.Errorf("%w: continuity mode %q is not valid", ErrInvalidValue, mode)
		}
	}
	return nil
}

// ValidateServiceContinuityProfile checks that a service continuity profile is usable.
func ValidateServiceContinuityProfile(profile *ServiceContinuityProfile) error {
	return profile.Validate()
}
