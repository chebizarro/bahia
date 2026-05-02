# Adoption and Direct Runtime Production Rollout Runbook

This runbook covers the operator-only adoption/import and direct-runtime feature set. The live-network verification matrix and final production gate live in [`adoption-live-network-verification.md`](adoption-live-network-verification.md); this document is the operational procedure, not final production signoff.

## Safety defaults

- Adoption and direct runtime actions are disabled unless explicitly enabled.
- Both features require API auth plus an operator allowlist.
- Prefer server-managed `runtime.endpoints.<ref>` aliases. Raw `docker_host` request payloads are legacy/dev compatibility only.
- Scan and import responses redact sensitive environment variables and labels. Sensitive environment values are imported through Bahia secrets when secret storage/encryption is configured.
- Compose-origin containers are direct-Docker takeover candidates; enable takeover only after operators accept that Bahia, not Compose, will drive restart/deploy/stop actions.

## Enablement checklist

1. Enable auth with either JWT or NIP-98:

   ```yaml
   auth:
     enabled: true
     jwt_secret: "<from-secret-manager>"
     # or: nip98_enabled: true
   ```

2. Configure operator allowlists for adoption and direct runtime actions:

   ```yaml
   adoption:
     enabled: true
     allow_raw_docker_hosts: false
     allow_compose_takeover: false
     allowed_subjects: ["ops@example.com"]
     allowed_pubkeys: []
     allowed_emails: []

   direct_runtime_actions:
     enabled: true
     allowed_subjects: ["ops@example.com"]
   ```

3. Configure endpoint aliases; do not expose Docker credentials to clients:

   ```yaml
   runtime:
     endpoints:
       prod-docker:
         docker_host: tcp://docker-prod.example.com:2376
         ca_cert_file: /etc/bahia/docker/prod/ca.pem
         client_cert_file: /etc/bahia/docker/prod/cert.pem
         client_key_file: /etc/bahia/docker/prod/key.pem
   ```

4. Ensure `/metrics` is reachable by monitoring with the same API auth method when auth is enabled, and logs include `request_id`, `actor_subject`/`actor_pubkey`, `target_name`, `endpoint_ref`, result counts, and duration fields.

## Dry-run scan

Run a scan before importing anything:

```bash
bahia adopt scan --target prod-docker
```

Validate:

- candidate count matches the expected running workloads;
- every candidate has an image digest;
- `redacted_environment_keys` / `redacted_label_keys` contain only key names, never values;
- compose-origin warnings are understood before enabling `allow_compose_takeover`;
- logs show the operator actor and endpoint alias, not raw secrets or certificate material.

## Import rollout

1. Start with one non-critical Docker-origin workload.
2. Import by explicit selection before using `--all`:

   ```bash
   bahia adopt import --target prod-docker --select prod-docker/<container-id>=<name>
   ```

3. Confirm:

   - service, environment, build, artifact, state, and runtime observation rows exist;
   - `adoption.imported` events were emitted after persistence;
   - metrics advanced: `bahia_adoption_imports_total`, success/failure counters, redaction counters;
   - no raw sensitive env values are present in API responses or logs.

4. Only then import additional workloads or enable `import_all` for a bounded target.

## Direct runtime actions

Direct runtime actions are intended only for imported direct-runtime workloads. Failed guardrails return conflict responses and should not be bypassed.

Use dedicated action endpoints only after import validation:

```http
POST /api/v1/services/{serviceId}/environments/{envId}/restart
POST /api/v1/services/{serviceId}/environments/{envId}/stop
POST /api/v1/services/{serviceId}/environments/{envId}/deploy
```

Monitor `bahia_runtime_actions_total` and runtime action duration metrics. Logs should include `service_id`, `environment_id`, optional `artifact_id`, `target_name`, `endpoint_ref`, `result`, and `duration_ms`.

## Rate limits and telemetry

Dedicated operational rate limits are applied separately from the generic write limiter:

- adoption scan: 5 requests/minute/IP;
- adoption import: 10 requests/minute/IP;
- direct runtime actions: 20 requests/minute/IP.

Prometheus-style metrics include:

- `bahia_adoption_scans_total{status=...}`;
- `bahia_adoption_targets_scanned_total`;
- `bahia_adoption_candidates_total`;
- `bahia_adoption_redacted_keys_total`;
- `bahia_adoption_imports_total{status=...}`;
- `bahia_adoption_import_success_total` and `bahia_adoption_import_failure_total`;
- `bahia_runtime_actions_total{key="action:status"}`;
- scan/import/runtime action duration summaries.

## Rollback / disable

If adoption causes unexpected behavior:

1. Disable privileged routes and restart Bahia:

   ```yaml
   adoption:
     enabled: false
   direct_runtime_actions:
     enabled: false
   ```

2. Stop issuing direct runtime actions. For compose-origin workloads, return to the Compose project and run the normal Compose deployment/restart flow from the original project directory.
3. If a workload should no longer be Bahia-managed, remove or quarantine the imported service/environment state through the normal registry/admin path after exporting audit records.
4. Keep endpoint aliases configured until rollback verification is complete so observations can still be inspected if needed.

## Caveats

- Raw-host mode (`allow_raw_docker_hosts: true`) is a temporary compatibility mode for trusted development or break-glass use only.
- Compose takeover changes the operational owner of the container. Do not enable it globally without staging validation.
- Final live-network verification and production signoff remain in `bahia-ejj8`.
