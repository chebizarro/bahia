-- Refuse destructive rollback: operator must export/retire the managed inventory
-- through the lifecycle service, then explicitly archive these authoritative rows.
DO $$ BEGIN
 -- Fence admission and journal writes until the transaction finishes dropping
 -- the tables; an emptiness snapshot alone cannot authorize destructive DDL.
 LOCK TABLE virtualization_hosts, vm_images, persistent_vm_deployments,
 execution_plane_deployments, vm_checkpoints, vm_exports,
 vm_operation_approvals, vm_operations, virtualization_capacity_reservations,
 virtualization_resource_changes IN ACCESS EXCLUSIVE MODE;
 IF EXISTS (SELECT 1 FROM persistent_vm_deployments)
 OR EXISTS (SELECT 1 FROM execution_plane_deployments)
 OR EXISTS (SELECT 1 FROM vm_checkpoints)
 OR EXISTS (SELECT 1 FROM vm_exports)
 OR EXISTS (SELECT 1 FROM vm_operations)
 OR EXISTS (SELECT 1 FROM virtualization_capacity_reservations)
 OR EXISTS (SELECT 1 FROM vm_operation_approvals)
 OR EXISTS (SELECT 1 FROM virtualization_hosts)
 OR EXISTS (SELECT 1 FROM vm_images)
 OR EXISTS (SELECT 1 FROM virtualization_resource_changes)
 THEN RAISE EXCEPTION 'VM control-plane rollback refused: authoritative inventory or history remains';
 END IF;
DROP TABLE virtualization_resource_changes;
DROP TABLE virtualization_capacity_reservations;
DROP TABLE vm_operations;
DROP TABLE vm_operation_approvals;
DROP TABLE vm_exports;
DROP TABLE vm_checkpoints;
DROP TABLE execution_plane_deployments;
DROP TABLE persistent_vm_deployments;
DROP TABLE vm_images;
DROP TABLE virtualization_hosts;
DROP FUNCTION vm_control_plane_classes(JSONB,BOOLEAN);
END $$;
