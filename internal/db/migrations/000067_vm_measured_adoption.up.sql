ALTER TABLE vm_operation_approvals ADD COLUMN adoption_digest TEXT NOT NULL DEFAULT ''
 CHECK (adoption_digest = '' OR adoption_digest ~ '^sha256:[a-f0-9]{64}$');

-- Reservations are not ownership. Only verified operation completion registers
-- the measured baseline. Failed/interrupted evidence remains for explicit review.
CREATE TABLE vm_adoption_storage (
 org_id UUID NOT NULL,
 deployment_id UUID NOT NULL,
 host_id UUID NOT NULL,
 operation_id UUID NOT NULL,
 storage_ref UUID NOT NULL,
 component_kind TEXT NOT NULL CHECK (component_kind IN ('disk','rootfs','kernel','nvram','swtpm')),
 storage_key TEXT NOT NULL CHECK (storage_key ~ '^sha256:[a-f0-9]{64}$'),
 measurement_digest TEXT NOT NULL CHECK (measurement_digest ~ '^sha256:[a-f0-9]{64}$'),
 component JSONB NOT NULL,
 registered BOOLEAN NOT NULL DEFAULT FALSE,
 PRIMARY KEY (org_id,deployment_id,storage_ref),
 FOREIGN KEY (org_id,deployment_id) REFERENCES persistent_vm_deployments(org_id,id) ON DELETE RESTRICT,
 FOREIGN KEY (org_id,host_id) REFERENCES virtualization_hosts(org_id,id) ON DELETE RESTRICT,
 FOREIGN KEY (org_id,operation_id) REFERENCES vm_operations(org_id,id) ON DELETE RESTRICT,
 CHECK (component @> jsonb_build_object('storage_ref',storage_ref::text,'kind',component_kind,'storage_key',storage_key))
);
CREATE UNIQUE INDEX vm_adoption_writable_owner ON vm_adoption_storage(host_id,storage_key)
 WHERE component_kind <> 'kernel';
