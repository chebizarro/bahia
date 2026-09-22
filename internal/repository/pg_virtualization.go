package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/domain"
)

type PgVirtualizationRepository struct{ pool *pgxpool.Pool }

var _ VirtualizationRepository = (*PgVirtualizationRepository)(nil)

func NewPgVirtualizationRepository(pool *pgxpool.Pool) *PgVirtualizationRepository {
	return &PgVirtualizationRepository{pool: pool}
}
func (r *PgVirtualizationRepository) Hosts() VirtualizationHostRepository {
	return &pgVMResources[domain.VirtualizationHost]{r, domain.VirtualizationHostResource}
}
func (r *PgVirtualizationRepository) Images() VMImageRepository {
	return &pgVMResources[domain.VMImage]{r, domain.VMImageResource}
}
func (r *PgVirtualizationRepository) Deployments() PersistentVMDeploymentRepository {
	return &pgVMResources[domain.PersistentVMDeployment]{r, domain.PersistentVMResource}
}
func (r *PgVirtualizationRepository) ExecutionPlanes() ExecutionPlaneDeploymentRepository {
	return &pgVMResources[domain.ExecutionPlaneDeployment]{r, domain.ExecutionPlaneResource}
}
func (r *PgVirtualizationRepository) Checkpoints() VMCheckpointRepository {
	return &pgVMResources[domain.VMCheckpoint]{r, domain.VMCheckpointResource}
}
func (r *PgVirtualizationRepository) Exports() VMExportRepository {
	return &pgVMResources[domain.VMExport]{r, domain.VMExportResource}
}

func vmTable(kind domain.VirtualizationResourceKind) (string, error) {
	switch kind {
	case domain.VirtualizationHostResource:
		return "virtualization_hosts", nil
	case domain.VMImageResource:
		return "vm_images", nil
	case domain.PersistentVMResource:
		return "persistent_vm_deployments", nil
	case domain.ExecutionPlaneResource:
		return "execution_plane_deployments", nil
	case domain.VMCheckpointResource:
		return "vm_checkpoints", nil
	case domain.VMExportResource:
		return "vm_exports", nil
	case domain.VMOperationResource:
		return "vm_operations", nil
	default:
		return "", domain.ErrInvalidValue
	}
}

// vmReadDocument adds authoritative fencing columns to read/change snapshots,
// not the writable desired document. No observation is fabricated by rotation.
func vmReadDocument(kind domain.VirtualizationResourceKind) string {
	switch kind {
	case domain.VirtualizationHostResource, domain.PersistentVMResource, domain.ExecutionPlaneResource:
		return `(document || jsonb_build_object('observation_cursor', CASE WHEN observation_session IS NULL THEN NULL ELSE jsonb_build_object('session_id', observation_session, 'sequence', observation_sequence) END))`
	default:
		return "document"
	}
}

func vmMeta(value any) *domain.VirtualizationResourceMeta {
	switch v := value.(type) {
	case *domain.VirtualizationHost:
		return &v.VirtualizationResourceMeta
	case *domain.VMImage:
		return &v.VirtualizationResourceMeta
	case *domain.PersistentVMDeployment:
		return &v.VirtualizationResourceMeta
	case *domain.ExecutionPlaneDeployment:
		return &v.VirtualizationResourceMeta
	case *domain.VMCheckpoint:
		return &v.VirtualizationResourceMeta
	case *domain.VMExport:
		return &v.VirtualizationResourceMeta
	case *domain.VMOperation:
		return &v.VirtualizationResourceMeta
	default:
		return nil
	}
}
func vmValidate(value any) error {
	switch v := value.(type) {
	case *domain.VirtualizationHost:
		return domain.ValidateVirtualizationHost(v)
	case *domain.VMImage:
		return domain.ValidateVMImage(v)
	case *domain.PersistentVMDeployment:
		return domain.ValidatePersistentVMDeployment(v)
	case *domain.ExecutionPlaneDeployment:
		return domain.ValidateExecutionPlaneDeployment(v)
	case *domain.VMCheckpoint:
		return domain.ValidateVMCheckpoint(v)
	case *domain.VMExport:
		return domain.ValidateVMExport(v)
	case *domain.VMOperation:
		return domain.ValidateVMOperation(v)
	default:
		return domain.ErrInvalidValue
	}
}
func vmEmpty[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}
func vmNormalize(value any) {
	switch v := value.(type) {
	case *domain.VirtualizationHost:
		v.ObservationCursor = nil
	case *domain.VMImage:
		v.AllowedProfiles = vmEmpty(v.AllowedProfiles)
		v.Components = vmEmpty(v.Components)
	case *domain.PersistentVMDeployment:
		v.ObservationCursor = nil
		v.Bootstrap = vmEmpty(v.Bootstrap)
		v.Connections = vmEmpty(v.Connections)
		v.Access.Protocols = vmEmpty(v.Access.Protocols)
		v.Network.PassthroughDeviceRefs = vmEmpty(v.Network.PassthroughDeviceRefs)
		if v.Labels == nil {
			v.Labels = map[string]string{}
		}
	case *domain.ExecutionPlaneDeployment:
		v.ObservationCursor = nil
		v.Desired.ExpectedCapabilities = vmEmpty(v.Desired.ExpectedCapabilities)
		v.Desired.Configuration.SecretBindings = vmEmpty(v.Desired.Configuration.SecretBindings)
		v.Desired.Configuration.Network.PassthroughDeviceRefs = vmEmpty(v.Desired.Configuration.Network.PassthroughDeviceRefs)
	case *domain.VMCheckpoint:
		v.Components = vmEmpty(v.Components)
	case *domain.VMExport:
		v.Components = vmEmpty(v.Components)
	case *domain.VMOperation:
		v.PreparedStorageRefs = vmEmpty(v.PreparedStorageRefs)
	}
}
func vmHasObservation(value any) bool {
	switch v := value.(type) {
	case *domain.VirtualizationHost:
		return v.Observation != nil
	case *domain.PersistentVMDeployment:
		return v.Observation != nil
	case *domain.ExecutionPlaneDeployment:
		return v.Observation != nil
	}
	return false
}
func vmAttachObservation(value any, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	switch v := value.(type) {
	case *domain.VirtualizationHost:
		return domain.DecodeVirtualizationDocument(data, &v.Observation)
	case *domain.PersistentVMDeployment:
		return domain.DecodeVirtualizationDocument(data, &v.Observation)
	case *domain.ExecutionPlaneDeployment:
		return domain.DecodeVirtualizationDocument(data, &v.Observation)
	default:
		return domain.ErrInvalidValue
	}
}
func vmDBError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var e *pgconn.PgError
	if errors.As(err, &e) {
		switch e.Code {
		case "23505", "40001", "40P01":
			return fmt.Errorf("%w: %s", ErrConflict, e.ConstraintName)
		case "23503", "23514", "23502", "22P02":
			return fmt.Errorf("%w: %s", domain.ErrInvalidValue, e.ConstraintName)
		}
	}
	return err
}

// Serializing journal allocation per tenant also makes cursor order commit order;
// BIGSERIAL alone can skip a late-committing lower sequence during crash recovery.
func (r *PgVirtualizationRepository) vmTx(ctx context.Context, org uuid.UUID, fn func(pgx.Tx) error) error {
	if org == uuid.Nil {
		return domain.ErrNilUUID
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,66))`, org.String()); err != nil {
		return err
	}
	if err = fn(tx); err != nil {
		return vmDBError(err)
	}
	return vmDBError(tx.Commit(ctx))
}
func vmJournal(ctx context.Context, q pgQueryer, kind domain.VirtualizationResourceKind, value any, change string) error {
	m := vmMeta(value)
	return vmJournalRef(ctx, q, VirtualizationResourceRef{m.OrgID, kind, m.ID}, change)
}

type pgVMResources[T any] struct {
	parent *PgVirtualizationRepository
	kind   domain.VirtualizationResourceKind
}

func vmRead[T any](ctx context.Context, q pgQueryer, kind domain.VirtualizationResourceKind, org, id uuid.UUID, lock bool) (*T, error) {
	table, err := vmTable(kind)
	if err != nil {
		return nil, err
	}
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	var data, obs []byte
	err = q.QueryRow(ctx, `SELECT `+vmReadDocument(kind)+`,observation FROM `+table+` WHERE org_id=$1 AND id=$2`+suffix, org, id).Scan(&data, &obs)
	if err != nil {
		return nil, vmDBError(err)
	}
	v := new(T)
	if err = domain.DecodeVirtualizationDocument(data, v); err != nil {
		return nil, err
	}
	if err = vmValidate(v); err != nil {
		return nil, err
	}
	if err = vmAttachObservation(v, obs); err != nil {
		return nil, err
	}
	return v, nil
}
func (r *pgVMResources[T]) Get(ctx context.Context, org, id uuid.UUID) (*T, error) {
	return vmRead[T](ctx, r.parent.pool, r.kind, org, id, false)
}
func vmPage(limit, offset int) error {
	if limit < 1 || limit > 1000 || offset < 0 {
		return domain.ErrInvalidValue
	}
	return nil
}
func (r *pgVMResources[T]) List(ctx context.Context, org uuid.UUID, limit, offset int) ([]T, error) {
	if org == uuid.Nil || vmPage(limit, offset) != nil {
		return nil, domain.ErrInvalidValue
	}
	table, err := vmTable(r.kind)
	if err != nil {
		return nil, err
	}
	rows, err := r.parent.pool.Query(ctx, `SELECT `+vmReadDocument(r.kind)+`,observation FROM `+table+` WHERE org_id=$1 ORDER BY id LIMIT $2 OFFSET $3`, org, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		var data, obs []byte
		if err = rows.Scan(&data, &obs); err != nil {
			return nil, err
		}
		var v T
		if err = domain.DecodeVirtualizationDocument(data, &v); err != nil {
			return nil, err
		}
		if err = vmValidate(&v); err != nil {
			return nil, err
		}
		if err = vmAttachObservation(&v, obs); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (r *pgVMResources[T]) Create(ctx context.Context, value *T) error {
	if value == nil {
		return domain.ErrInvalidValue
	}
	m := vmMeta(value)
	if m == nil {
		return domain.ErrInvalidValue
	}
	// Mutate caller state only after commit, not on an aborted admission.
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	v := new(T)
	if err = domain.DecodeVirtualizationDocument(data, v); err != nil {
		return err
	}
	err = r.parent.vmTx(ctx, m.OrgID, func(tx pgx.Tx) error { return vmCreate(ctx, tx, r.kind, v) })
	if err == nil {
		*value = *v
	}
	return err
}
func vmCreate(ctx context.Context, q pgQueryer, kind domain.VirtualizationResourceKind, value any) error {
	m := vmMeta(value)
	if m == nil || m.Generation != 1 || vmHasObservation(value) {
		return domain.ErrInvalidValue
	}
	now := time.Now().UTC()
	m.CreatedAt = now
	m.UpdatedAt = now
	vmNormalize(value)
	if err := vmValidate(value); err != nil {
		return err
	}
	if err := vmReferences(ctx, q, value); err != nil {
		return err
	}
	table, err := vmTable(kind)
	if err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	columns, values := "", ""
	if kind == domain.VMOperationResource {
		columns = ",deadline,completed_at"
		values = ",($7::jsonb->>'deadline')::timestamptz,($7::jsonb->>'completed_at')::timestamptz"
	}
	_, err = q.Exec(ctx, `INSERT INTO `+table+`(id,org_id,generation,created_by,created_at,updated_at,document`+columns+`) VALUES($1,$2,$3,$4,$5,$6,$7`+values+`)`, m.ID, m.OrgID, m.Generation, m.CreatedBy, m.CreatedAt, m.UpdatedAt, data)
	if err != nil {
		return err
	}
	return vmJournal(ctx, q, kind, value, "created")
}
func (r *pgVMResources[T]) Update(ctx context.Context, value *T, expected int64) error {
	if value == nil || vmMeta(value) == nil {
		return domain.ErrInvalidValue
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	v := new(T)
	if err = domain.DecodeVirtualizationDocument(data, v); err != nil {
		return err
	}
	err = r.parent.vmTx(ctx, vmMeta(v).OrgID, func(tx pgx.Tx) error { return vmUpdate(ctx, tx, r.kind, v, expected, false) })
	if err == nil {
		*value = *v
	}
	return err
}
func vmUpdate[T any](ctx context.Context, q pgQueryer, kind domain.VirtualizationResourceKind, v *T, expected int64, approvedRecreate bool) error {
	m := vmMeta(v)
	if m == nil || expected < 1 || m.Generation != expected+1 || vmHasObservation(v) {
		return domain.ErrInvalidValue
	}
	old, err := vmRead[T](ctx, q, kind, m.OrgID, m.ID, true)
	if err != nil {
		return err
	}
	before := vmMeta(old)
	if before.Generation != expected {
		return ErrConflict
	}
	if m.CreatedBy != before.CreatedBy || !m.CreatedAt.Equal(before.CreatedAt) {
		return fmt.Errorf("%w: creation identity is immutable", ErrConflict)
	}
	if err = vmImmutable(old, v, approvedRecreate); err != nil {
		return err
	}
	rebound := false
	if previous, ok := any(old).(*domain.PersistentVMDeployment); ok {
		next := any(v).(*domain.PersistentVMDeployment)
		if !reflect.DeepEqual(previous.Identity, next.Identity) {
			rebound = true
			o := previous.Observation
			if o == nil || o.Availability != domain.VMObservationAvailable || o.ObservedGeneration != previous.Generation || o.RuntimeState == nil || *o.RuntimeState != domain.VMRuntimeAbsent {
				return fmt.Errorf("%w: rebind requires confirmed old resource absence", ErrConflict)
			}
			var safe bool
			err = q.QueryRow(ctx, `SELECT observation_session=$3 AND NOT EXISTS(SELECT 1 FROM virtualization_capacity_reservations WHERE org_id=$1 AND resource_kind='persistent_vm' AND resource_id=$2) FROM persistent_vm_deployments WHERE org_id=$1 AND id=$2`, m.OrgID, m.ID, o.SessionID).Scan(&safe)
			if err != nil {
				return err
			}
			if !safe {
				return ErrConflict
			}
		}
	}
	m.UpdatedAt = time.Now().UTC()
	vmNormalize(v)
	if err = vmValidate(v); err != nil {
		return err
	}
	if err = vmReferences(ctx, q, v); err != nil {
		return err
	}
	if kind == domain.PersistentVMResource {
		var active bool
		if err = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM vm_operations WHERE org_id=$1 AND resource_id=$2 AND phase NOT IN ('succeeded','failed','cancelled'))`, m.OrgID, m.ID).Scan(&active); err != nil {
			return err
		}
		if active {
			return ErrConflict
		}
	}
	table, err := vmTable(kind)
	if err != nil {
		return err
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tag, err := q.Exec(ctx, `UPDATE `+table+` SET generation=$3,updated_at=$4,document=$5,observation=CASE WHEN $7 THEN NULL ELSE observation END,observation_session=CASE WHEN $7 THEN NULL ELSE observation_session END,observation_sequence=CASE WHEN $7 THEN 0 ELSE observation_sequence END WHERE org_id=$1 AND id=$2 AND generation=$6`, m.OrgID, m.ID, m.Generation, m.UpdatedAt, data, expected, rebound)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return vmJournal(ctx, q, kind, v, "updated")
}
func vmImmutable(old, next any, approvedRecreate bool) error {
	bad := false
	switch a := old.(type) {
	case *domain.VMImage:
		bad = true
	case *domain.VirtualizationHost:
		b := next.(*domain.VirtualizationHost)
		bad = a.InstallationID != b.InstallationID || a.Provider != b.Provider || a.ExecutionLocation != b.ExecutionLocation || a.Architecture != b.Architecture || !reflect.DeepEqual(a.LifecycleClasses, b.LifecycleClasses)
	case *domain.PersistentVMDeployment:
		b := next.(*domain.PersistentVMDeployment)
		bad = a.LifecycleClass != b.LifecycleClass || (!approvedRecreate && !reflect.DeepEqual(a.Identity, b.Identity))
	case *domain.ExecutionPlaneDeployment:
		b := next.(*domain.ExecutionPlaneDeployment)
		bad = a.HostID != b.HostID || a.WorkerPubKey != b.WorkerPubKey || !reflect.DeepEqual(a.Desired.LifecycleClasses, b.Desired.LifecycleClasses)
	case *domain.VMCheckpoint:
		b := next.(*domain.VMCheckpoint)
		bad = a.DeploymentID != b.DeploymentID || a.DeploymentGeneration != b.DeploymentGeneration || a.ImageID != b.ImageID || a.ImageDigest != b.ImageDigest || a.ConfigDigest != b.ConfigDigest || !reflect.DeepEqual(a.Identity, b.Identity) || a.Firmware != b.Firmware || a.TPMEnabled != b.TPMEnabled || a.Consistency != b.Consistency || !vmArtifactTransition(a.State, b.State)
		if a.State != domain.VMArtifactCreating {
			bad = bad || a.ManifestDigest != b.ManifestDigest || !reflect.DeepEqual(a.Components, b.Components)
		}
	case *domain.VMExport:
		b := next.(*domain.VMExport)
		bad = a.CheckpointID != b.CheckpointID || a.StorageRef != b.StorageRef || !vmArtifactTransition(a.State, b.State)
		if a.State != domain.VMArtifactCreating {
			bad = bad || a.ManifestDigest != b.ManifestDigest || !reflect.DeepEqual(a.Components, b.Components) || !reflect.DeepEqual(a.Provenance, b.Provenance)
		}
	}
	if bad {
		return fmt.Errorf("%w: immutable resource identity/manifest or invalid artifact transition", ErrConflict)
	}
	return nil
}
func vmArtifactTransition(a, b domain.VMArtifactState) bool {
	if a == b {
		return true
	}
	switch a {
	case domain.VMArtifactCreating:
		return b == domain.VMArtifactReady || b == domain.VMArtifactFailed
	case domain.VMArtifactReady, domain.VMArtifactFailed:
		return b == domain.VMArtifactDeleting
	case domain.VMArtifactDeleting:
		return b == domain.VMArtifactDeleted
	}
	return false
}
func vmReferences(ctx context.Context, q pgQueryer, value any) error {
	switch v := value.(type) {
	case *domain.VirtualizationHost:
		var c domain.VMCapacity
		err := q.QueryRow(ctx, `SELECT COALESCE(sum(vcpu),0),COALESCE(sum(memory_bytes),0),COALESCE(sum(disk_bytes),0) FROM virtualization_capacity_reservations WHERE org_id=$1 AND host_id=$2`, v.OrgID, v.ID).Scan(&c.VCPU, &c.MemoryBytes, &c.DiskBytes)
		if err != nil {
			return err
		}
		if !c.Fits(v.Quota) {
			return fmt.Errorf("%w: quota below reservations", ErrConflict)
		}
	case *domain.PersistentVMDeployment:
		h, err := vmRead[domain.VirtualizationHost](ctx, q, domain.VirtualizationHostResource, v.OrgID, v.HostID, true)
		if err != nil {
			return err
		}
		i, err := vmRead[domain.VMImage](ctx, q, domain.VMImageResource, v.OrgID, v.ImageID, false)
		if err != nil {
			return err
		}
		// Observations can be from an earlier generation and do not govern references.
		h.Observation = nil
		if err = domain.ValidateVMDeploymentReferences(v, h, i); err != nil {
			return err
		}
		if v.ServiceID != nil {
			var ok bool
			err = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM services s, environments e WHERE s.id=$1 AND e.id=$2 AND s.org_id=$3 AND e.org_id=$3)`, *v.ServiceID, *v.EnvironmentID, v.OrgID).Scan(&ok)
			if err != nil {
				return err
			}
			if !ok {
				return domain.ErrInvalidValue
			}
		}
		if v.DeploymentUnitID != nil {
			var ok bool
			if err = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM deployment_units WHERE id=$1 AND environment_id=$2)`, *v.DeploymentUnitID, *v.EnvironmentID).Scan(&ok); err != nil {
				return err
			}
			if !ok {
				return domain.ErrInvalidValue
			}
		}
	case *domain.ExecutionPlaneDeployment:
		h, err := vmRead[domain.VirtualizationHost](ctx, q, domain.VirtualizationHostResource, v.OrgID, v.HostID, true)
		if err != nil {
			return err
		}
		if !h.Enabled {
			return domain.ErrInvalidValue
		}
		for _, pin := range v.Desired.ImagePins {
			i, err := vmRead[domain.VMImage](ctx, q, domain.VMImageResource, v.OrgID, pin.ImageID, false)
			if err != nil {
				return err
			}
			if !slices.Contains(h.LifecycleClasses, pin.LifecycleClass) || !slices.Contains(i.LifecycleClasses, pin.LifecycleClass) || i.ManifestDigest != pin.ManifestDigest || i.Architecture != h.Architecture || i.OS != domain.VMOSLinux {
				return domain.ErrInvalidValue
			}
		}
	case *domain.VMCheckpoint:
		d, err := vmRead[domain.PersistentVMDeployment](ctx, q, domain.PersistentVMResource, v.OrgID, v.DeploymentID, false)
		if err != nil {
			return err
		}
		if v.DeploymentGeneration > d.Generation || (v.DeploymentGeneration == d.Generation && !reflect.DeepEqual(v.Identity, d.Identity)) {
			return domain.ErrInvalidValue
		}
		i, err := vmRead[domain.VMImage](ctx, q, domain.VMImageResource, v.OrgID, v.ImageID, false)
		if err != nil {
			return err
		}
		if i.ManifestDigest != v.ImageDigest {
			return domain.ErrInvalidValue
		}
	case *domain.VMExport:
		c, err := vmRead[domain.VMCheckpoint](ctx, q, domain.VMCheckpointResource, v.OrgID, v.CheckpointID, false)
		if err != nil {
			return err
		}
		if v.Generation == 1 && c.State != domain.VMArtifactReady {
			return domain.ErrInvalidValue
		}
		if v.State == domain.VMArtifactReady && (!vmSameComponentContent(v.Components, c.Components)) {
			return domain.ErrInvalidValue
		}
	}
	return nil
}

func vmSameComponentContent(a, b []domain.VMComponent) bool {
	if len(a) != len(b) {
		return false
	}
	for _, source := range a {
		found := false
		for _, target := range b {
			if source.Kind == target.Kind && source.Digest == target.Digest && source.SizeBytes == target.SizeBytes {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func (r *PgVirtualizationRepository) ReserveCapacity(ctx context.Context, res domain.VMCapacityReservation) error {
	return r.vmTx(ctx, res.OrgID, func(tx pgx.Tx) error { return vmReserve(ctx, tx, res) })
}
func vmReservationResource(ctx context.Context, q pgQueryer, res domain.VMCapacityReservation) (uuid.UUID, *domain.VMCapacity, error) {
	switch res.ResourceKind {
	case domain.PersistentVMResource:
		v, e := vmRead[domain.PersistentVMDeployment](ctx, q, res.ResourceKind, res.OrgID, res.ResourceID, false)
		if e != nil {
			return uuid.Nil, nil, e
		}
		return v.HostID, &v.Allocation, nil
	case domain.ExecutionPlaneResource:
		v, e := vmRead[domain.ExecutionPlaneDeployment](ctx, q, res.ResourceKind, res.OrgID, res.ResourceID, false)
		if e != nil {
			return uuid.Nil, nil, e
		}
		if !slices.Contains(v.Desired.LifecycleClasses, res.LifecycleClass) {
			return uuid.Nil, nil, domain.ErrInvalidValue
		}
		return v.HostID, &v.Desired.ReservedCapacity, nil
	case domain.VMCheckpointResource:
		v, e := vmRead[domain.VMCheckpoint](ctx, q, res.ResourceKind, res.OrgID, res.ResourceID, false)
		if e != nil {
			return uuid.Nil, nil, e
		}
		return v.Identity.HostID, nil, nil
	case domain.VMExportResource:
		v, e := vmRead[domain.VMExport](ctx, q, res.ResourceKind, res.OrgID, res.ResourceID, false)
		if e != nil {
			return uuid.Nil, nil, e
		}
		c, e := vmRead[domain.VMCheckpoint](ctx, q, domain.VMCheckpointResource, res.OrgID, v.CheckpointID, false)
		if e != nil {
			return uuid.Nil, nil, e
		}
		return c.Identity.HostID, nil, nil
	default:
		return uuid.Nil, nil, domain.ErrInvalidValue
	}
}
func vmReserve(ctx context.Context, q pgQueryer, res domain.VMCapacityReservation) error {
	if res.ID == uuid.Nil || res.OrgID == uuid.Nil || res.HostID == uuid.Nil || res.ResourceID == uuid.Nil || domain.ValidateVMCapacity(res.Capacity, false) != nil || res.Capacity.DiskBytes == 0 || domain.ValidateVMLifecycleClass(res.LifecycleClass) != nil {
		return domain.ErrInvalidValue
	}
	if (res.ResourceKind == domain.ExecutionPlaneResource) != (res.LifecycleClass != domain.VMLifecyclePersistent) {
		return domain.ErrInvalidValue
	}
	if (res.ResourceKind == domain.VMCheckpointResource || res.ResourceKind == domain.VMExportResource) && (res.Capacity.VCPU != 0 || res.Capacity.MemoryBytes != 0) {
		return domain.ErrInvalidValue
	}
	hostID, allocation, err := vmReservationResource(ctx, q, res)
	if err != nil {
		return err
	}
	if hostID != res.HostID || (allocation != nil && *allocation != res.Capacity) {
		return domain.ErrInvalidValue
	}
	h, err := vmRead[domain.VirtualizationHost](ctx, q, domain.VirtualizationHostResource, res.OrgID, res.HostID, true)
	if err != nil {
		return err
	}
	if !h.Enabled || !slices.Contains(h.LifecycleClasses, res.LifecycleClass) {
		return domain.ErrInvalidValue
	}
	var used, old domain.VMCapacity
	var oldID, oldHost uuid.UUID
	err = q.QueryRow(ctx, `SELECT id,host_id,vcpu,memory_bytes,disk_bytes FROM virtualization_capacity_reservations WHERE org_id=$1 AND resource_kind=$2 AND resource_id=$3 FOR UPDATE`, res.OrgID, res.ResourceKind, res.ResourceID).Scan(&oldID, &oldHost, &old.VCPU, &old.MemoryBytes, &old.DiskBytes)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err == nil && (oldID != res.ID || oldHost != res.HostID) {
		return ErrConflict
	}
	if err == nil && old == res.Capacity {
		return nil
	}
	if !old.Fits(res.Capacity) {
		return fmt.Errorf("%w: reservations cannot shrink before verified release", ErrConflict)
	}
	err = q.QueryRow(ctx, `SELECT COALESCE(sum(vcpu),0),COALESCE(sum(memory_bytes),0),COALESCE(sum(disk_bytes),0) FROM virtualization_capacity_reservations WHERE org_id=$1 AND host_id=$2`, res.OrgID, res.HostID).Scan(&used.VCPU, &used.MemoryBytes, &used.DiskBytes)
	if err != nil {
		return err
	}
	delta := domain.VMCapacity{VCPU: res.Capacity.VCPU - old.VCPU, MemoryBytes: res.Capacity.MemoryBytes - old.MemoryBytes, DiskBytes: res.Capacity.DiskBytes - old.DiskBytes}
	// Subtract before comparing to avoid signed overflow on hostile quantities.
	headroom := domain.VMCapacity{VCPU: h.Quota.VCPU - used.VCPU, MemoryBytes: h.Quota.MemoryBytes - used.MemoryBytes, DiskBytes: h.Quota.DiskBytes - used.DiskBytes}
	if !delta.Fits(headroom) {
		return fmt.Errorf("%w: %s", ErrConflict, domain.VMErrorQuota)
	}
	var session uuid.UUID
	if err = q.QueryRow(ctx, `SELECT COALESCE(observation_session,'00000000-0000-0000-0000-000000000000') FROM virtualization_hosts WHERE org_id=$1 AND id=$2`, res.OrgID, res.HostID).Scan(&session); err != nil {
		return err
	}
	o := h.Observation
	now := time.Now().UTC()
	if o == nil || o.Availability != domain.VMObservationAvailable || o.ObservedGeneration != h.Generation || o.SessionID != session || o.ObservedAt.After(now) || now.Sub(o.ObservedAt) > time.Duration(h.CapacityObservationMaxAgeSeconds)*time.Second {
		return fmt.Errorf("%w: fresh provider capacity observation required", ErrConflict)
	}
	// Free capacity must cover all reservations. This is deliberately conservative:
	// a provider may report free excluding allocated disks, but thin provisioning
	// cannot spend the same observed free bytes concurrently.
	total := domain.VMCapacity{VCPU: used.VCPU + delta.VCPU, MemoryBytes: used.MemoryBytes + delta.MemoryBytes, DiskBytes: used.DiskBytes + delta.DiskBytes}
	if !total.Fits(o.Free) {
		return fmt.Errorf("%w: provider free capacity", ErrConflict)
	}
	_, err = q.Exec(ctx, `INSERT INTO virtualization_capacity_reservations(id,org_id,host_id,resource_id,resource_kind,lifecycle_class,vcpu,memory_bytes,disk_bytes,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,now()) ON CONFLICT(org_id,resource_kind,resource_id) DO UPDATE SET vcpu=excluded.vcpu,memory_bytes=excluded.memory_bytes,disk_bytes=excluded.disk_bytes`, res.ID, res.OrgID, res.HostID, res.ResourceID, res.ResourceKind, res.LifecycleClass, res.Capacity.VCPU, res.Capacity.MemoryBytes, res.Capacity.DiskBytes)
	if err != nil {
		return err
	}
	return vmJournalRef(ctx, q, VirtualizationResourceRef{res.OrgID, res.ResourceKind, res.ResourceID}, "reserved")
}
func (r *PgVirtualizationRepository) ListReservations(ctx context.Context, org, host uuid.UUID) ([]domain.VMCapacityReservation, error) {
	rows, err := r.pool.Query(ctx, `SELECT id,org_id,host_id,resource_id,resource_kind,lifecycle_class,vcpu,memory_bytes,disk_bytes,created_at FROM virtualization_capacity_reservations WHERE org_id=$1 AND host_id=$2 ORDER BY id`, org, host)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.VMCapacityReservation{}
	for rows.Next() {
		var v domain.VMCapacityReservation
		if err = rows.Scan(&v.ID, &v.OrgID, &v.HostID, &v.ResourceID, &v.ResourceKind, &v.LifecycleClass, &v.Capacity.VCPU, &v.Capacity.MemoryBytes, &v.Capacity.DiskBytes, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (r *PgVirtualizationRepository) ReleaseCapacity(ctx context.Context, org, id uuid.UUID) error {
	return r.vmTx(ctx, org, func(tx pgx.Tx) error {
		var resource uuid.UUID
		var kind domain.VirtualizationResourceKind
		if err := tx.QueryRow(ctx, `SELECT resource_id,resource_kind FROM virtualization_capacity_reservations WHERE org_id=$1 AND id=$2 FOR UPDATE`, org, id).Scan(&resource, &kind); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil // Already released; never delete another tenant's reservation.
			}
			return err
		}
		safe := false
		switch kind {
		case domain.PersistentVMResource:
			v, err := vmRead[domain.PersistentVMDeployment](ctx, tx, kind, org, resource, true)
			if err != nil {
				return err
			}
			o := v.Observation
			if o != nil && o.Availability == domain.VMObservationAvailable && o.ObservedGeneration == v.Generation && o.RuntimeState != nil && *o.RuntimeState == domain.VMRuntimeAbsent {
				var session uuid.UUID
				if err = tx.QueryRow(ctx, `SELECT observation_session FROM persistent_vm_deployments WHERE org_id=$1 AND id=$2`, org, resource).Scan(&session); err != nil {
					return err
				}
				safe = o.SessionID == session
			}
		case domain.ExecutionPlaneResource:
			v, err := vmRead[domain.ExecutionPlaneDeployment](ctx, tx, kind, org, resource, true)
			if err != nil {
				return err
			}
			o := v.Observation
			if v.Desired.State == domain.ExecutionPlaneDisabled && o != nil && o.ObservedGeneration == v.Generation && o.Availability == domain.VMObservationAvailable && o.State == domain.ExecutionPlaneDisabled && !o.Draining {
				var session uuid.UUID
				if err = tx.QueryRow(ctx, `SELECT observation_session FROM execution_plane_deployments WHERE org_id=$1 AND id=$2`, org, resource).Scan(&session); err != nil {
					return err
				}
				safe = o.SessionID == session
			}
		case domain.VMCheckpointResource:
			v, err := vmRead[domain.VMCheckpoint](ctx, tx, kind, org, resource, true)
			if err != nil {
				return err
			}
			safe = v.State == domain.VMArtifactDeleted
		case domain.VMExportResource:
			v, err := vmRead[domain.VMExport](ctx, tx, kind, org, resource, true)
			if err != nil {
				return err
			}
			safe = v.State == domain.VMArtifactDeleted
		}
		var active bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM vm_operations WHERE org_id=$1 AND phase NOT IN ('succeeded','failed','cancelled') AND (resource_id=$2 OR document->>'checkpoint_id'=$2::text OR document->>'export_id'=$2::text OR document->>'clone_target_id'=$2::text))`, org, resource).Scan(&active); err != nil {
			return err
		}
		if !safe || active {
			return fmt.Errorf("%w: capacity retained until verified absence/drain/deletion and terminal operations", ErrConflict)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM virtualization_capacity_reservations WHERE org_id=$1 AND id=$2`, org, id); err != nil {
			return err
		}
		return vmJournalRef(ctx, tx, VirtualizationResourceRef{org, kind, resource}, "released")
	})
}

func (r *PgVirtualizationRepository) RotateObservationSession(ctx context.Context, ref VirtualizationResourceRef, generation int64, expected, next uuid.UUID) error {
	if next == uuid.Nil || next == expected || !slices.Contains([]domain.VirtualizationResourceKind{domain.VirtualizationHostResource, domain.PersistentVMResource, domain.ExecutionPlaneResource}, ref.Kind) {
		return domain.ErrInvalidValue
	}
	table, err := vmTable(ref.Kind)
	if err != nil {
		return err
	}
	return r.vmTx(ctx, ref.OrgID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE `+table+` SET observation_session=$4,observation_sequence=0 WHERE org_id=$1 AND id=$2 AND generation=$3 AND COALESCE(observation_session,'00000000-0000-0000-0000-000000000000')=$5`, ref.OrgID, ref.ID, generation, next, expected)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		return vmJournalRef(ctx, tx, ref, "session_changed")
	})
}
func vmJournalRef(ctx context.Context, q pgQueryer, ref VirtualizationResourceRef, change string) error {
	return vmJournalWithApproval(ctx, q, ref, change, nil)
}
func vmJournalWithApproval(ctx context.Context, q pgQueryer, ref VirtualizationResourceRef, change string, approval *uuid.UUID) error {
	table, err := vmTable(ref.Kind)
	if err != nil {
		return err
	}
	classes := `jsonb_build_array('persistent_vm')`
	if ref.Kind == domain.VirtualizationHostResource || ref.Kind == domain.VMImageResource {
		classes = `document->'lifecycle_classes'`
	}
	if ref.Kind == domain.ExecutionPlaneResource {
		classes = `document#>'{desired,lifecycle_classes}'`
	}
	_, err = q.Exec(ctx, `INSERT INTO virtualization_resource_changes(org_id,resource_kind,resource_id,generation,lifecycle_classes,change_type,document,observation,approval_id) SELECT org_id,$3,id,generation,`+classes+`,$4,`+vmReadDocument(ref.Kind)+`,observation,$5 FROM `+table+` WHERE org_id=$1 AND id=$2`, ref.OrgID, ref.ID, ref.Kind, change, approval)
	return err
}
func (r *PgVirtualizationRepository) acceptObservation(ctx context.Context, ref VirtualizationResourceRef, stamp domain.VMObservationStamp, observation any, validate func(pgx.Tx) error) error {
	if err := domain.ValidateVMObservationStamp(stamp); err != nil {
		return err
	}
	table, err := vmTable(ref.Kind)
	if err != nil {
		return err
	}
	return r.vmTx(ctx, ref.OrgID, func(tx pgx.Tx) error {
		if err := validate(tx); err != nil {
			return err
		}
		data, err := json.Marshal(observation)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE `+table+` SET observation=$6,observation_sequence=$5 WHERE org_id=$1 AND id=$2 AND generation=$3 AND observation_session=$4 AND observation_sequence<$5 AND (observation IS NULL OR (observation->>'session_id')::uuid<>$4 OR (observation->>'observed_at')::timestamptz<=$7)`, ref.OrgID, ref.ID, stamp.ObservedGeneration, stamp.SessionID, stamp.Sequence, data, stamp.ObservedAt)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		return vmJournalRef(ctx, tx, ref, "observed")
	})
}
func (r *PgVirtualizationRepository) AcceptHostObservation(ctx context.Context, org, id uuid.UUID, o domain.VirtualizationHostObservation) error {
	o.StoragePools = vmEmpty(o.StoragePools)
	o.Networks = vmEmpty(o.Networks)
	return r.acceptObservation(ctx, VirtualizationResourceRef{org, domain.VirtualizationHostResource, id}, o.VMObservationStamp, &o, func(tx pgx.Tx) error {
		h, err := vmRead[domain.VirtualizationHost](ctx, tx, domain.VirtualizationHostResource, org, id, true)
		if err != nil {
			return err
		}
		return domain.ValidateVirtualizationHostObservation(h, &o)
	})
}
func (r *PgVirtualizationRepository) AcceptVMObservation(ctx context.Context, org, id uuid.UUID, o domain.VMObservation) error {
	o.Connections = vmEmpty(o.Connections)
	return r.acceptObservation(ctx, VirtualizationResourceRef{org, domain.PersistentVMResource, id}, o.VMObservationStamp, &o, func(tx pgx.Tx) error {
		v, err := vmRead[domain.PersistentVMDeployment](ctx, tx, domain.PersistentVMResource, org, id, true)
		if err != nil {
			return err
		}
		if o.Availability == domain.VMObservationUnavailable {
			if v.Observation != nil {
				o.RuntimeState = v.Observation.RuntimeState
				o.RuntimeObservedAt = v.Observation.RuntimeObservedAt
			} else {
				o.RuntimeState = nil
				o.RuntimeObservedAt = nil
			}
		}
		return domain.ValidateVMObservation(v, &o)
	})
}
func (r *PgVirtualizationRepository) AcceptPlaneObservation(ctx context.Context, org, id uuid.UUID, o domain.ExecutionPlaneObservation) error {
	o.ImagePins = vmEmpty(o.ImagePins)
	if o.Probe != nil {
		p := *o.Probe
		p.ImagePins = vmEmpty(p.ImagePins)
		p.Capabilities = vmEmpty(p.Capabilities)
		o.Probe = &p
	}
	return r.acceptObservation(ctx, VirtualizationResourceRef{org, domain.ExecutionPlaneResource, id}, o.VMObservationStamp, &o, func(tx pgx.Tx) error {
		p, err := vmRead[domain.ExecutionPlaneDeployment](ctx, tx, domain.ExecutionPlaneResource, org, id, true)
		if err != nil {
			return err
		}
		if p.Observation != nil && p.Observation.Probe != nil {
			previous := p.Observation.Probe
			if previous.SessionID == o.SessionID && previous.ObservedGeneration == o.ObservedGeneration {
				if o.Probe == nil {
					o.Probe = previous
				} else if o.Probe.Sequence < previous.Sequence || (o.Probe.Sequence == previous.Sequence && !reflect.DeepEqual(o.Probe, previous)) {
					return ErrConflict
				}
			}
		}
		return domain.ValidateExecutionPlaneObservation(p, &o)
	})
}
