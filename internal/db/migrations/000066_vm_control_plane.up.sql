-- Additive, provider-neutral virtualization resources. No legacy table is altered.
CREATE FUNCTION vm_control_plane_classes(value JSONB, ephemeral BOOLEAN) RETURNS BOOLEAN
LANGUAGE SQL IMMUTABLE STRICT AS $$
 SELECT jsonb_typeof(value) = 'array' AND jsonb_array_length(value) > 0
 AND NOT EXISTS (SELECT 1 FROM jsonb_array_elements_text(value) c
   WHERE c NOT IN ('persistent_vm','loom_firecracker_job_microvm','loom_qemu_job_domain')
      OR (ephemeral AND c = 'persistent_vm'))
 AND (SELECT count(*) = count(DISTINCT c) FROM jsonb_array_elements_text(value) c)
$$;

-- Shared envelope. Typed resource-specific columns below are generated from the
-- versioned document so column/document identity cannot diverge.
DO $$ DECLARE t TEXT; BEGIN
 FOREACH t IN ARRAY ARRAY['virtualization_hosts','vm_images','persistent_vm_deployments',
   'execution_plane_deployments','vm_checkpoints','vm_exports','vm_operations'] LOOP
  EXECUTE format('CREATE TABLE %I (
   id UUID PRIMARY KEY CHECK (id <> ''00000000-0000-0000-0000-000000000000''),
   org_id UUID NOT NULL REFERENCES organizations(id) ON DELETE RESTRICT,
   generation BIGINT NOT NULL CHECK (generation >= 1),
   created_by TEXT NOT NULL CHECK (length(created_by) BETWEEN 1 AND 128),
   created_at TIMESTAMPTZ NOT NULL,
   updated_at TIMESTAMPTZ NOT NULL CHECK (updated_at >= created_at),
   schema_version INTEGER NOT NULL DEFAULT 1 CHECK (schema_version = 1),
   document JSONB NOT NULL,
   observation JSONB,
   observation_session UUID,
   observation_sequence BIGINT NOT NULL DEFAULT 0 CHECK (observation_sequence >= 0),
   observed_generation BIGINT GENERATED ALWAYS AS ((observation->>''observed_generation'')::bigint) STORED CHECK (observed_generation>0),
   observation_availability TEXT GENERATED ALWAYS AS (observation->>''availability'') STORED CHECK (observation_availability IN (''available'',''unavailable'')),
   UNIQUE (org_id,id),
   CHECK (jsonb_typeof(document) = ''object'' AND
     document @> jsonb_build_object(''schema_version'',1,''id'',id::text,''org_id'',org_id::text,''generation'',generation,''created_by'',created_by)),
   CHECK (NOT (document ? ''observation'')),
   CHECK (observation IS NULL OR (jsonb_typeof(observation) = ''object'' AND observation @> ''{"schema_version":1}''::jsonb))
  )',t);
  EXECUTE format('CREATE INDEX ON %I (org_id,id)',t);
 END LOOP;
END $$;

ALTER TABLE virtualization_hosts
 ADD COLUMN installation_id UUID GENERATED ALWAYS AS ((document->>'installation_id')::uuid) STORED NOT NULL,
 ADD COLUMN provider TEXT GENERATED ALWAYS AS (document->>'provider') STORED NOT NULL CHECK (provider IN ('libvirt','firecracker')),
 ADD COLUMN execution_location TEXT GENERATED ALWAYS AS (document->>'execution_location') STORED NOT NULL CHECK (execution_location IN ('local','remote')),
 ADD COLUMN lifecycle_classes JSONB GENERATED ALWAYS AS (document->'lifecycle_classes') STORED NOT NULL CHECK (vm_control_plane_classes(lifecycle_classes,false)),
 ADD COLUMN vcpu BIGINT GENERATED ALWAYS AS ((document#>>'{capacity,vcpu}')::bigint) STORED NOT NULL CHECK (vcpu>0),
 ADD COLUMN memory_bytes BIGINT GENERATED ALWAYS AS ((document#>>'{capacity,memory_bytes}')::bigint) STORED NOT NULL CHECK (memory_bytes>0),
 ADD COLUMN disk_bytes BIGINT GENERATED ALWAYS AS ((document#>>'{capacity,disk_bytes}')::bigint) STORED NOT NULL CHECK (disk_bytes>0),
 ADD COLUMN quota_vcpu BIGINT GENERATED ALWAYS AS ((document#>>'{quota,vcpu}')::bigint) STORED NOT NULL CHECK (quota_vcpu>0 AND quota_vcpu<=vcpu),
 ADD COLUMN quota_memory_bytes BIGINT GENERATED ALWAYS AS ((document#>>'{quota,memory_bytes}')::bigint) STORED NOT NULL CHECK (quota_memory_bytes>0 AND quota_memory_bytes<=memory_bytes),
 ADD COLUMN quota_disk_bytes BIGINT GENERATED ALWAYS AS ((document#>>'{quota,disk_bytes}')::bigint) STORED NOT NULL CHECK (quota_disk_bytes>0 AND quota_disk_bytes<=disk_bytes),
 ADD CONSTRAINT vm_host_remote_endpoint CHECK (execution_location <> 'remote' OR
   COALESCE((document->>'management_endpoint_ref')::uuid <> '00000000-0000-0000-0000-000000000000',false));

ALTER TABLE vm_images
 ADD COLUMN manifest_digest TEXT GENERATED ALWAYS AS (document->>'manifest_digest') STORED NOT NULL CHECK (manifest_digest ~ '^sha256:[a-f0-9]{64}$'),
 ADD COLUMN format TEXT GENERATED ALWAYS AS (document->>'format') STORED NOT NULL CHECK (format IN ('qcow2','firecracker-rootfs')),
 ADD COLUMN architecture TEXT GENERATED ALWAYS AS (document->>'architecture') STORED NOT NULL CHECK (architecture IN ('amd64','arm64')),
 ADD COLUMN os TEXT GENERATED ALWAYS AS (document->>'os') STORED NOT NULL CHECK (os IN ('linux','windows')),
 ADD COLUMN lifecycle_classes JSONB GENERATED ALWAYS AS (document->'lifecycle_classes') STORED NOT NULL CHECK (vm_control_plane_classes(lifecycle_classes,false)),
 ADD CONSTRAINT vm_images_windows_persistent CHECK (os <> 'windows' OR lifecycle_classes = '["persistent_vm"]'::jsonb),
 ADD CONSTRAINT vm_images_manifest_shape CHECK (jsonb_typeof(document->'components')='array' AND jsonb_array_length(document->'components')>0);

ALTER TABLE persistent_vm_deployments
 ADD COLUMN lifecycle_class TEXT GENERATED ALWAYS AS (document->>'lifecycle_class') STORED NOT NULL CHECK (lifecycle_class='persistent_vm'),
 ADD COLUMN host_id UUID GENERATED ALWAYS AS ((document->>'host_id')::uuid) STORED NOT NULL,
 ADD COLUMN image_id UUID GENERATED ALWAYS AS ((document->>'image_id')::uuid) STORED NOT NULL,
 ADD COLUMN service_id UUID GENERATED ALWAYS AS ((document->>'service_id')::uuid) STORED REFERENCES services(id) ON DELETE RESTRICT,
 ADD COLUMN environment_id UUID GENERATED ALWAYS AS ((document->>'environment_id')::uuid) STORED REFERENCES environments(id) ON DELETE RESTRICT,
 ADD COLUMN deployment_unit_id UUID GENERATED ALWAYS AS ((document->>'deployment_unit_id')::uuid) STORED REFERENCES deployment_units(id) ON DELETE RESTRICT,
 ADD COLUMN provider TEXT GENERATED ALWAYS AS (document->>'provider') STORED NOT NULL CHECK (provider IN ('libvirt','firecracker')),
 ADD COLUMN provider_resource_id UUID GENERATED ALWAYS AS ((document#>>'{identity,provider_resource_id}')::uuid) STORED NOT NULL,
 ADD COLUMN purpose TEXT GENERATED ALWAYS AS (document->>'purpose') STORED NOT NULL CHECK (purpose IN ('desktop','service')),
 ADD COLUMN desired_power TEXT GENERATED ALWAYS AS (document->>'desired_power') STORED NOT NULL CHECK (desired_power IN ('stopped','running')),
 ADD COLUMN runtime_state TEXT GENERATED ALWAYS AS (observation->>'runtime_state') STORED CHECK (runtime_state IN ('absent','stopped','running','paused','failed')),
 ADD COLUMN drift TEXT GENERATED ALWAYS AS (observation->>'drift') STORED CHECK (drift IN ('in_sync','drifted','unknown')),
 ADD COLUMN guest_health TEXT GENERATED ALWAYS AS (observation->>'guest_health') STORED CHECK (guest_health IN ('not_configured','starting','healthy','unhealthy','unknown')),
 ADD COLUMN ownership TEXT GENERATED ALWAYS AS (observation->>'ownership') STORED CHECK (ownership IN ('owned','orphan','foreign','unknown')),
 ADD COLUMN vcpu BIGINT GENERATED ALWAYS AS ((document#>>'{allocation,vcpu}')::bigint) STORED NOT NULL CHECK (vcpu>0),
 ADD COLUMN memory_bytes BIGINT GENERATED ALWAYS AS ((document#>>'{allocation,memory_bytes}')::bigint) STORED NOT NULL CHECK (memory_bytes>0),
 ADD COLUMN disk_bytes BIGINT GENERATED ALWAYS AS ((document#>>'{allocation,disk_bytes}')::bigint) STORED NOT NULL CHECK (disk_bytes>0),
 ADD FOREIGN KEY (org_id,host_id) REFERENCES virtualization_hosts(org_id,id) ON DELETE RESTRICT,
 ADD FOREIGN KEY (org_id,image_id) REFERENCES vm_images(org_id,id) ON DELETE RESTRICT,
 ADD UNIQUE (host_id,provider_resource_id),
 ADD CONSTRAINT vm_deployment_targeting CHECK ((service_id IS NULL)=(environment_id IS NULL) AND (deployment_unit_id IS NULL OR environment_id IS NOT NULL)),
 ADD CONSTRAINT vm_deployment_marker_identity CHECK (document->'identity' @> jsonb_build_object('org_id',org_id::text,'host_id',host_id::text,'deployment_id',id::text,'lifecycle_class','persistent_vm','provider',provider));

ALTER TABLE execution_plane_deployments
 ADD COLUMN host_id UUID GENERATED ALWAYS AS ((document->>'host_id')::uuid) STORED NOT NULL,
 ADD COLUMN lifecycle_classes JSONB GENERATED ALWAYS AS (document#>'{desired,lifecycle_classes}') STORED NOT NULL CHECK (vm_control_plane_classes(lifecycle_classes,true)),
 ADD COLUMN state TEXT GENERATED ALWAYS AS (document#>>'{desired,state}') STORED NOT NULL CHECK (state IN ('enabled','disabled')),
 ADD COLUMN package_digest TEXT GENERATED ALWAYS AS (document#>>'{desired,package,digest}') STORED NOT NULL CHECK (package_digest ~ '^sha256:[a-f0-9]{64}$'),
 ADD COLUMN config_revision TEXT GENERATED ALWAYS AS (document#>>'{desired,configuration,revision}') STORED NOT NULL CHECK (config_revision ~ '^sha256:[a-f0-9]{64}$'),
 ADD COLUMN vcpu BIGINT GENERATED ALWAYS AS ((document#>>'{desired,reserved_capacity,vcpu}')::bigint) STORED NOT NULL CHECK (vcpu>0),
 ADD COLUMN memory_bytes BIGINT GENERATED ALWAYS AS ((document#>>'{desired,reserved_capacity,memory_bytes}')::bigint) STORED NOT NULL CHECK (memory_bytes>0),
 ADD COLUMN disk_bytes BIGINT GENERATED ALWAYS AS ((document#>>'{desired,reserved_capacity,disk_bytes}')::bigint) STORED NOT NULL CHECK (disk_bytes>0),
 ADD COLUMN concurrency INTEGER GENERATED ALWAYS AS ((document#>>'{desired,concurrency}')::integer) STORED NOT NULL CHECK (concurrency>0),
 ADD FOREIGN KEY (org_id,host_id) REFERENCES virtualization_hosts(org_id,id) ON DELETE RESTRICT,
 ADD CONSTRAINT execution_plane_no_windows CHECK (NOT jsonb_path_exists(document, '$.desired.expected_capabilities[*] ? (@.os != "linux")'));

ALTER TABLE vm_checkpoints
 ADD COLUMN lifecycle_class TEXT GENERATED ALWAYS AS (document->>'lifecycle_class') STORED NOT NULL CHECK (lifecycle_class='persistent_vm'),
 ADD COLUMN deployment_id UUID GENERATED ALWAYS AS ((document->>'deployment_id')::uuid) STORED NOT NULL,
 ADD COLUMN deployment_generation BIGINT GENERATED ALWAYS AS ((document->>'deployment_generation')::bigint) STORED NOT NULL CHECK (deployment_generation>0),
 ADD COLUMN image_id UUID GENERATED ALWAYS AS ((document->>'image_id')::uuid) STORED NOT NULL,
 ADD COLUMN state TEXT GENERATED ALWAYS AS (document->>'state') STORED NOT NULL CHECK (state IN ('creating','ready','failed','deleting','deleted')),
 ADD COLUMN consistency TEXT GENERATED ALWAYS AS (document->>'consistency') STORED NOT NULL CHECK (consistency='cold'),
 ADD FOREIGN KEY (org_id,deployment_id) REFERENCES persistent_vm_deployments(org_id,id) ON DELETE RESTRICT,
 ADD FOREIGN KEY (org_id,image_id) REFERENCES vm_images(org_id,id) ON DELETE RESTRICT,
 ADD CONSTRAINT vm_checkpoint_ready CHECK (state<>'ready' OR (COALESCE(document->>'manifest_digest','') ~ '^sha256:[a-f0-9]{64}$' AND jsonb_array_length(document->'components')>0));
ALTER TABLE vm_exports
 ADD COLUMN lifecycle_class TEXT GENERATED ALWAYS AS (document->>'lifecycle_class') STORED NOT NULL CHECK (lifecycle_class='persistent_vm'),
 ADD COLUMN checkpoint_id UUID GENERATED ALWAYS AS ((document->>'checkpoint_id')::uuid) STORED NOT NULL,
 ADD COLUMN state TEXT GENERATED ALWAYS AS (document->>'state') STORED NOT NULL CHECK (state IN ('creating','ready','failed','deleting','deleted')),
 ADD FOREIGN KEY (org_id,checkpoint_id) REFERENCES vm_checkpoints(org_id,id) ON DELETE RESTRICT,
 ADD CONSTRAINT vm_export_ready CHECK (state<>'ready' OR (COALESCE(document->>'manifest_digest','') ~ '^sha256:[a-f0-9]{64}$' AND jsonb_array_length(document->'components')>0));

CREATE TABLE vm_operation_approvals (
 id UUID PRIMARY KEY,
 org_id UUID NOT NULL REFERENCES organizations(id) ON DELETE RESTRICT,
 resource_id UUID NOT NULL,
 lifecycle_class TEXT NOT NULL CHECK (lifecycle_class='persistent_vm'),
 generation BIGINT NOT NULL CHECK (generation>0),
 request_hash TEXT NOT NULL CHECK (request_hash ~ '^sha256:[a-f0-9]{64}$'),
 provider_fingerprint TEXT NOT NULL CHECK (provider_fingerprint ~ '^sha256:[a-f0-9]{64}$'),
 tier INTEGER NOT NULL CHECK (tier=2),
 requester TEXT NOT NULL CHECK (length(requester)>0),
 approver TEXT NOT NULL CHECK (length(approver)>0 AND approver<>requester),
 reason TEXT NOT NULL CHECK (length(reason) BETWEEN 1 AND 512),
 schema_version INTEGER NOT NULL DEFAULT 1 CHECK (schema_version=1),
 created_at TIMESTAMPTZ NOT NULL,
 expires_at TIMESTAMPTZ NOT NULL CHECK (expires_at>created_at AND expires_at<=created_at+interval '30 minutes'),
 consumed_at TIMESTAMPTZ,
 UNIQUE (org_id,id),
 FOREIGN KEY (org_id,resource_id) REFERENCES persistent_vm_deployments(org_id,id) ON DELETE RESTRICT
);
ALTER TABLE vm_operations
 ADD COLUMN lifecycle_class TEXT GENERATED ALWAYS AS (document->>'lifecycle_class') STORED NOT NULL CHECK (lifecycle_class='persistent_vm'),
 ADD COLUMN resource_id UUID GENERATED ALWAYS AS ((document->>'resource_id')::uuid) STORED NOT NULL,
 ADD COLUMN resource_generation BIGINT GENERATED ALWAYS AS ((document->>'resource_generation')::bigint) STORED NOT NULL CHECK (resource_generation>0),
 ADD COLUMN expected_generation BIGINT GENERATED ALWAYS AS ((document->>'expected_generation')::bigint) STORED NOT NULL CHECK (expected_generation>=0 AND expected_generation<=resource_generation),
 ADD COLUMN actor TEXT GENERATED ALWAYS AS (document->>'actor') STORED NOT NULL CHECK (length(actor) BETWEEN 1 AND 128),
 ADD COLUMN deadline TIMESTAMPTZ NOT NULL CHECK (deadline=(document->>'deadline')::timestamptz),
 ADD COLUMN completed_at TIMESTAMPTZ CHECK (completed_at=(document->>'completed_at')::timestamptz),
 ADD COLUMN idempotency_key TEXT GENERATED ALWAYS AS (document->>'idempotency_key') STORED NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 256),
 ADD COLUMN request_hash TEXT GENERATED ALWAYS AS (document->>'request_hash') STORED NOT NULL CHECK (request_hash ~ '^sha256:[a-f0-9]{64}$'),
 ADD COLUMN kind TEXT GENERATED ALWAYS AS (document->>'kind') STORED NOT NULL CHECK (kind IN ('define','adopt','start','graceful_stop','reboot','checkpoint','export','clone','restore','delete')),
 ADD COLUMN phase TEXT GENERATED ALWAYS AS (document->>'phase') STORED NOT NULL CHECK (phase IN ('accepted','awaiting_approval','executing','verifying','succeeded','failed','cancelled','unconfirmed')),
 ADD COLUMN required_tier INTEGER GENERATED ALWAYS AS ((document->>'required_tier')::integer) STORED NOT NULL CHECK (required_tier IN (1,2)),
 ADD COLUMN approval_id UUID GENERATED ALWAYS AS ((document->>'approval_id')::uuid) STORED,
 ADD FOREIGN KEY (org_id,resource_id) REFERENCES persistent_vm_deployments(org_id,id) ON DELETE RESTRICT,
 ADD FOREIGN KEY (org_id,approval_id) REFERENCES vm_operation_approvals(org_id,id) ON DELETE RESTRICT,
 ADD UNIQUE (org_id,idempotency_key),
 ADD CONSTRAINT vm_operation_minimum_tier CHECK (kind NOT IN ('adopt','export','restore','delete') OR required_tier=2),
 ADD CONSTRAINT vm_operation_completion CHECK ((phase IN ('succeeded','failed','cancelled'))=(completed_at IS NOT NULL)),
 ADD CONSTRAINT vm_operation_delete_target CHECK (kind<>'delete' OR COALESCE(document->>'delete_target','') IN ('deployment','checkpoint','export')),
 ADD CONSTRAINT vm_operation_approved CHECK (required_tier<>2 OR phase IN ('awaiting_approval','cancelled') OR approval_id IS NOT NULL);
CREATE UNIQUE INDEX vm_operations_exclusive_resource ON vm_operations(org_id,resource_id)
 WHERE phase NOT IN ('succeeded','failed','cancelled');

CREATE TABLE virtualization_capacity_reservations (
 id UUID PRIMARY KEY,
 org_id UUID NOT NULL REFERENCES organizations(id) ON DELETE RESTRICT,
 host_id UUID NOT NULL,
 resource_id UUID NOT NULL,
 resource_kind TEXT NOT NULL CHECK (resource_kind IN ('persistent_vm','execution_plane','checkpoint','export')),
 lifecycle_class TEXT NOT NULL CHECK (lifecycle_class IN ('persistent_vm','loom_firecracker_job_microvm','loom_qemu_job_domain')),
 vcpu BIGINT NOT NULL CHECK (vcpu>=0),
 memory_bytes BIGINT NOT NULL CHECK (memory_bytes>=0),
 disk_bytes BIGINT NOT NULL CHECK (disk_bytes>0),
 created_at TIMESTAMPTZ NOT NULL,
 CHECK ((resource_kind='execution_plane')=(lifecycle_class<>'persistent_vm')),
 CHECK (resource_kind NOT IN ('checkpoint','export') OR (vcpu=0 AND memory_bytes=0)),
 UNIQUE (org_id,resource_kind,resource_id),
 FOREIGN KEY (org_id,host_id) REFERENCES virtualization_hosts(org_id,id) ON DELETE RESTRICT
);
CREATE INDEX ON virtualization_capacity_reservations(org_id,host_id);

CREATE TABLE virtualization_resource_changes (
 sequence BIGSERIAL PRIMARY KEY,
 schema_version INTEGER NOT NULL DEFAULT 1 CHECK (schema_version=1),
 org_id UUID NOT NULL REFERENCES organizations(id) ON DELETE RESTRICT,
 resource_kind TEXT NOT NULL CHECK (resource_kind IN ('host','image','persistent_vm','execution_plane','checkpoint','export','operation')),
 resource_id UUID NOT NULL,
 generation BIGINT NOT NULL CHECK (generation>0),
 lifecycle_classes JSONB NOT NULL CHECK (vm_control_plane_classes(lifecycle_classes,false)),
 change_type TEXT NOT NULL CHECK (change_type IN ('created','updated','observed','session_changed','reserved','released','approval_created','approval_consumed')),
 document JSONB NOT NULL CHECK (jsonb_typeof(document)='object' AND document @> jsonb_build_object('schema_version',1,'id',resource_id::text,'org_id',org_id::text,'generation',generation)),
 observation JSONB CHECK (observation IS NULL OR (jsonb_typeof(observation)='object' AND observation @> '{"schema_version":1}'::jsonb)),
 approval_id UUID REFERENCES vm_operation_approvals(id) ON DELETE RESTRICT,
 occurred_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX ON virtualization_resource_changes(org_id,sequence);
