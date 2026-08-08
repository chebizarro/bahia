-- 000054_vm_runtime_types: Revert the runtime_type closed set to the pre-VM values.
-- Fails if any deployment unit still uses vm-firecracker or vm-qemu; such units
-- must be removed before downgrading.

ALTER TABLE deployment_units
  DROP CONSTRAINT IF EXISTS deployment_units_runtime_type_check;

ALTER TABLE deployment_units
  ADD CONSTRAINT deployment_units_runtime_type_check
  CHECK (runtime_type IN ('docker', 'compose', 'kubernetes', 'podman'));
