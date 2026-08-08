-- 000054_vm_runtime_types: Extend the deployment unit runtime_type closed set
-- with the VM runtime types vm-firecracker and vm-qemu.

ALTER TABLE deployment_units
  DROP CONSTRAINT IF EXISTS deployment_units_runtime_type_check;

ALTER TABLE deployment_units
  ADD CONSTRAINT deployment_units_runtime_type_check
  CHECK (runtime_type IN ('docker', 'compose', 'kubernetes', 'podman', 'vm-firecracker', 'vm-qemu'));
