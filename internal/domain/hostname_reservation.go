package domain

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// RouteMutation makes hostname intent behavior explicit: preserve is read-only,
// set publishes or revises a claim, and remove withdraws providers then signs
// a tombstone. Omitting it never implies removal.
type RouteMutation string

const (
	RoutePreserve RouteMutation = "preserve"
	RouteSet      RouteMutation = "set"
	RouteRemove   RouteMutation = "remove"
)

// HostnameReservation is the service-signed owner of one public DNS name.
// A tombstone keeps its revision so a later claimant must observe the release.
type HostnameReservation struct {
	Hostname         string                  `json:"hostname"`
	OrgID            uuid.UUID               `json:"org_id"`
	ServiceID        uuid.UUID               `json:"service_id"`
	EnvironmentID    uuid.UUID               `json:"environment_id"`
	DeploymentUnitID uuid.UUID               `json:"deployment_unit_id"`
	Plan             *DesiredPublicRoutePlan `json:"plan,omitempty"`
	UpdatedAt        time.Time               `json:"updated_at"`
	Deleted          bool                    `json:"deleted"`
	IntentID         string                  `json:"intent_id"`
	IntentEventID    string                  `json:"intent_event_id"`
	RequestedBy      string                  `json:"requested_by"`
}

func (r HostnameReservation) Coordinate() string { return kinds.HostnameReservationDTag(r.Hostname) }

func (r HostnameReservation) Validate() error {
	host, err := NormalizePublicHostname(r.Hostname)
	if err != nil || host != r.Hostname {
		return fmt.Errorf("reservation hostname must be normalized: %w", err)
	}
	if r.OrgID == uuid.Nil || r.ServiceID == uuid.Nil || r.EnvironmentID == uuid.Nil || r.DeploymentUnitID == uuid.Nil || r.UpdatedAt.IsZero() || r.IntentID == "" || r.IntentEventID == "" || r.RequestedBy == "" {
		return fmt.Errorf("reservation identity, revision, and intent_id are required")
	}
	if r.Deleted && r.Plan != nil {
		return fmt.Errorf("reservation tombstone must not carry an active route plan")
	}
	if !r.Deleted {
		if r.Plan == nil {
			return fmt.Errorf("live reservation requires a route plan")
		}
		if err := ValidateDesiredPublicRoute(r.Plan); err != nil {
			return err
		}
		if r.Plan.Hostname != r.Hostname || r.Plan.ServiceID != r.ServiceID || r.Plan.EnvironmentID != r.EnvironmentID || r.Plan.DeploymentUnitID != r.DeploymentUnitID || !r.planOwnerMatches(r.Plan) {
			return fmt.Errorf("reservation owner and route plan differ")
		}
	}
	return nil
}

func (r HostnameReservation) Owns(plan *DesiredPublicRoutePlan) bool {
	if r.Deleted || plan == nil || r.Plan == nil || r.Hostname != plan.Hostname || r.ServiceID != plan.ServiceID || r.EnvironmentID != plan.EnvironmentID || r.DeploymentUnitID != plan.DeploymentUnitID || r.Plan.Provider != plan.Provider || !r.planOwnerMatches(plan) {
		return false
	}
	switch plan.Provider {
	case "cloudflare_tunnel":
		return r.Plan.DNS.SourceCoordinate == plan.DNS.SourceCoordinate
	case "caddy":
		return r.Plan.Caddy != nil && plan.Caddy != nil && r.Plan.Caddy.RouteID == plan.Caddy.RouteID && r.Plan.Caddy.ServerID == plan.Caddy.ServerID
	default:
		return false
	}
}

func (r HostnameReservation) planOwnerMatches(plan *DesiredPublicRoutePlan) bool {
	switch plan.Provider {
	case "cloudflare_tunnel":
		return plan.DNS.SourceCoordinate == PublicRouteCoordinate(r.ServiceID, r.EnvironmentID, r.DeploymentUnitID)
	case "caddy":
		return plan.Caddy != nil && plan.Caddy.RouteID == PublicRouteCaddyID(r.ServiceID, r.EnvironmentID, r.DeploymentUnitID)
	default:
		return false
	}
}
