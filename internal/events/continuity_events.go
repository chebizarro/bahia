package events

import (
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

const (
	EventContinuityProfileObserved EventType = "continuity.profile.observed"
	EventFailoverPolicyObserved    EventType = "continuity.failover_policy.observed"
	EventReplicationPolicyObserved EventType = "continuity.replication_policy.observed"
	EventRecoveryWorkflowObserved  EventType = "continuity.recovery_workflow.observed"
	EventHeartbeatObserved         EventType = "heartbeat.observed"
	EventFailoverRequested         EventType = "failover.requested"
	EventRecoveryRequested         EventType = "recovery.requested"
)

// NostrSource identifies the event that produced an internal continuity event.
type NostrSource struct {
	EventID   string    `json:"event_id"`
	Kind      int       `json:"kind"`
	PubKey    string    `json:"pubkey"`
	CreatedAt time.Time `json:"created_at"`
}

// ContinuityProfileObserved carries a decoded service continuity profile.
type ContinuityProfileObserved struct {
	Source  NostrSource                     `json:"source"`
	Profile domain.ServiceContinuityProfile `json:"profile"`
}

// ContinuityRecipeObserved carries a decoded failover or recovery recipe.
type ContinuityRecipeObserved struct {
	Source NostrSource             `json:"source"`
	Recipe domain.ContinuityRecipe `json:"recipe"`
}

// ReplicationPolicyObserved carries a decoded replication policy.
type ReplicationPolicyObserved struct {
	Source NostrSource              `json:"source"`
	Policy domain.ReplicationPolicy `json:"policy"`
}

// HeartbeatObserved carries a decoded heartbeat observation.
type HeartbeatObserved struct {
	Source      NostrSource                 `json:"source"`
	Observation domain.HeartbeatObservation `json:"observation"`
}

// ContinuityCommandRequested carries a failover or recovery command request.
type ContinuityCommandRequested struct {
	Source             NostrSource           `json:"source"`
	ServiceKey         string                `json:"service_key"`
	TargetWorkerPubKey string                `json:"target_worker_pubkey"`
	TargetProfile      domain.ContinuityMode `json:"target_profile,omitempty"`
	RecipeName         string                `json:"recipe_name,omitempty"`
	IdempotencyKey     string                `json:"idempotency_key"`
	Reason             string                `json:"reason,omitempty"`
	Metadata           map[string]any        `json:"metadata,omitempty"`
}
