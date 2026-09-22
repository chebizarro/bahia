package repository

import (
	"context"
	"encoding/json"
	"reflect"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

// VMAdoptionStorage retains content/source measurements and opaque file identity.
// Registered is set only with verified completion, not merely admission.
type VMAdoptionStorage struct {
	OperationID       uuid.UUID
	MeasurementDigest string
	Registered        bool
	Component         domain.VMAdoptionComponent
}

func vmAdoptionDigest(o *domain.VMOperation) string {
	if o.Adoption == nil {
		return ""
	}
	return o.Adoption.Digest
}

func vmEnrollAdoptionStorage(ctx context.Context, q pgQueryer, o *domain.VMOperation, v *domain.PersistentVMDeployment, registered bool) error {
	m := o.Adoption
	if domain.ValidateVMAdoptionMeasurement(m) != nil || !reflect.DeepEqual(m.Identity, v.Identity) || m.Generation != v.Generation || m.ConfigDigest != v.ConfigDigest || m.ImageID != v.ImageID || m.StoragePoolRef != v.StoragePoolRef {
		return domain.ErrInvalidValue
	}
	image, err := vmRead[domain.VMImage](ctx, q, domain.VMImageResource, o.OrgID, v.ImageID, false)
	if err != nil {
		return err
	}
	if image.ManifestDigest != m.ImageDigest {
		return ErrConflict
	}
	if registered && (v.Observation == nil || v.Observation.Marker == nil || v.Observation.Marker.ImageDigest != m.ImageDigest || v.Observation.Marker.ConfigDigest != m.ConfigDigest || v.Observation.AppliedImageDigest != m.ImageDigest || v.Observation.AppliedConfigDigest != m.ConfigDigest) {
		return ErrConflict
	}
	for _, component := range m.Components {
		data, err := json.Marshal(component)
		if err != nil {
			return err
		}
		if registered {
			tag, err := q.Exec(ctx, `UPDATE vm_adoption_storage SET registered=TRUE WHERE org_id=$1 AND deployment_id=$2 AND storage_ref=$3 AND operation_id=$4 AND measurement_digest=$5 AND component=$6`, o.OrgID, o.ResourceID, component.StorageRef, o.ID, m.Digest, data)
			if err != nil {
				return vmDBError(err)
			}
			if tag.RowsAffected() != 1 {
				return ErrConflict
			}
		} else {
			// Re-enrollment may update evidence only for the same exact storage
			// object and resource. It cannot acquire a peer's writable component.
			tag, err := q.Exec(ctx, `INSERT INTO vm_adoption_storage(org_id,deployment_id,host_id,operation_id,storage_ref,component_kind,storage_key,measurement_digest,component) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)
 ON CONFLICT (org_id,deployment_id,storage_ref) DO UPDATE SET operation_id=EXCLUDED.operation_id,measurement_digest=EXCLUDED.measurement_digest,component=EXCLUDED.component,registered=FALSE
 WHERE vm_adoption_storage.host_id=EXCLUDED.host_id AND vm_adoption_storage.storage_key=EXCLUDED.storage_key AND vm_adoption_storage.component_kind=EXCLUDED.component_kind`, o.OrgID, o.ResourceID, v.HostID, o.ID, component.StorageRef, component.Kind, component.StorageKey, m.Digest, data)
			if err != nil {
				return vmDBError(err)
			}
			if tag.RowsAffected() != 1 {
				return ErrConflict
			}
		}
	}
	return nil
}

func (r *PgVirtualizationRepository) ListAdoptionStorage(ctx context.Context, org, deployment uuid.UUID) ([]VMAdoptionStorage, error) {
	rows, err := r.pool.Query(ctx, `SELECT operation_id,measurement_digest,registered,component FROM vm_adoption_storage WHERE org_id=$1 AND deployment_id=$2 ORDER BY component_kind`, org, deployment)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VMAdoptionStorage
	for rows.Next() {
		var row VMAdoptionStorage
		var data []byte
		if err = rows.Scan(&row.OperationID, &row.MeasurementDigest, &row.Registered, &data); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(data, &row.Component); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
