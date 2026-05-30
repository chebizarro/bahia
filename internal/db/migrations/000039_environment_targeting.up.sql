-- 000039_environment_targeting: Add typed environment and deployment-unit targeting fields.

ALTER TABLE environments
  ADD COLUMN targeting JSONB NOT NULL DEFAULT '{}'::jsonb;

UPDATE environments
SET targeting = jsonb_strip_nulls(jsonb_build_object(
  'default_unit_key', COALESCE(NULLIF(runtime_config->>'default_unit_key', ''), 'default'),
  'failure_domain_labels', runtime_config->'failure_domain_labels',
  'secret_scope_mode', runtime_config->>'secret_scope_mode',
  'default_reconcile_mode', COALESCE(NULLIF(runtime_config->>'default_reconcile_mode', ''), NULLIF(runtime_config->>'reconcile_mode', ''), 'observe_only')
));

ALTER TABLE deployment_units
  ADD COLUMN endpoint_ref TEXT NOT NULL DEFAULT '',
  ADD COLUMN compose_dir TEXT NOT NULL DEFAULT '',
  ADD COLUMN namespace TEXT NOT NULL DEFAULT '',
  ADD COLUMN network_profile JSONB NOT NULL DEFAULT '{}'::jsonb;

UPDATE deployment_units
SET endpoint_ref = COALESCE(NULLIF(runtime_config->>'endpoint_ref', ''), ''),
    compose_dir = COALESCE(NULLIF(runtime_config->>'compose_dir', ''), ''),
    namespace = COALESCE(NULLIF(runtime_config->>'namespace', ''), NULLIF(runtime_config->>'kube_namespace', ''), ''),
    network_profile = COALESCE(runtime_config->'network_profile', '{}'::jsonb);
