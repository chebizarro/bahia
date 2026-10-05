package domain

import (
	"sort"
	"time"

	"github.com/google/uuid"
)

// Adoption binding statuses. A binding is published in progress before the
// first resource of an adoption and complete after the last one, so an
// adoption interrupted between resources stays visible until it is resumed.
const (
	AdoptionBindingInProgress = "in_progress"
	AdoptionBindingComplete   = "complete"
)

// AdoptionBinding is the canonical record of one adopted workload: the stable
// fingerprints that identify the runtime workload, the resources the adoption
// published for it, and how far that adoption got. It is addressed by service
// and environment, so re-adopting a workload replaces its binding.
type AdoptionBinding struct {
	OrgID            uuid.UUID  `json:"org_id"`
	ServiceID        uuid.UUID  `json:"service_id"`
	EnvironmentID    uuid.UUID  `json:"environment_id"`
	DeploymentUnitID *uuid.UUID `json:"deployment_unit_id,omitempty"`
	// BuildID and ArtifactID are nil only on a binding backfilled from the
	// SQL-era identity table, which never recorded them.
	BuildID     *uuid.UUID       `json:"build_id,omitempty"`
	ArtifactID  *uuid.UUID       `json:"artifact_id,omitempty"`
	HostAlias   string           `json:"host_alias,omitempty"`
	EndpointRef string           `json:"endpoint_ref,omitempty"`
	TargetName  string           `json:"target_name,omitempty"`
	ContainerID string           `json:"container_id,omitempty"`
	ImageDigest string           `json:"image_digest,omitempty"`
	Compose     *ComposeMetadata `json:"compose,omitempty"`
	// Fingerprints maps a fingerprint kind to the fingerprint itself.
	Fingerprints map[string]string `json:"fingerprints"`
	Status       string            `json:"status"`
	UpdatedAt    time.Time         `json:"updated_at"`
}

// Identities expands the binding into one adopted runtime identity per
// fingerprint, ordered by fingerprint kind. It is the shape the SQL index
// stores.
func (b AdoptionBinding) Identities() []AdoptedRuntimeIdentity {
	kinds := make([]string, 0, len(b.Fingerprints))
	for kind := range b.Fingerprints {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	out := make([]AdoptedRuntimeIdentity, 0, len(kinds))
	for _, kind := range kinds {
		out = append(out, AdoptedRuntimeIdentity{
			OrgID:           b.OrgID,
			ServiceID:       b.ServiceID,
			EnvironmentID:   b.EnvironmentID,
			FingerprintKind: kind,
			Fingerprint:     b.Fingerprints[kind],
			ContainerID:     b.ContainerID,
			ImageDigest:     b.ImageDigest,
			EndpointRef:     b.EndpointRef,
			HostAlias:       b.HostAlias,
			TargetName:      b.TargetName,
			Compose:         b.Compose,
		})
	}
	return out
}
