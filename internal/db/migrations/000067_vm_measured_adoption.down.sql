DO $$ BEGIN
 LOCK TABLE vm_adoption_storage, vm_operation_approvals, vm_operations,
 persistent_vm_deployments IN ACCESS EXCLUSIVE MODE;
 IF EXISTS (SELECT 1 FROM vm_adoption_storage)
 OR EXISTS (SELECT 1 FROM vm_operation_approvals WHERE adoption_digest <> '')
 OR EXISTS (SELECT 1 FROM vm_operations WHERE document->'adoption' IS NOT NULL AND document->'adoption' <> 'null'::jsonb)
 THEN RAISE EXCEPTION 'Measured VM adoption rollback refused: enrollment evidence remains';
 END IF;
 DROP TABLE vm_adoption_storage;
 ALTER TABLE vm_operation_approvals DROP COLUMN adoption_digest;
END $$;
