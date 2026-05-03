# Adoption and Direct Runtime Production Rollout Runbook

Scope: production/staging rollout of signer-first adoption/import and direct-runtime operator workflows.
Normative gate: [`adoption-live-network-verification.md`](adoption-live-network-verification.md)
Execution checklist: [`adoption-signer-first-operator-checklist.md`](adoption-signer-first-operator-checklist.md)

This runbook now assumes signer-first operator execution over Nostr control-plane requests.
Legacy privileged HTTP/NIP-98 paths remain compatibility-only and secondary.

## Safety defaults

- Adoption and direct runtime actions are disabled unless explicitly enabled.
- Signer-first operator execution is authorized by operator pubkeys and signed event verification.
- Prefer server-managed `runtime.endpoints.<ref>` aliases. Raw `docker_host` request payloads are compatibility/break-glass only.
- CLI defaults to signer-first operator transport for `bahia adopt ...` and `bahia services actions ...`.
- HTTP fallback is explicit only (`--http-fallback` or `BAHIA_OPERATOR_HTTP_FALLBACK=true`) and is safe only before any relay accepts the signed request.
- Scan and import responses redact sensitive environment variables and labels. Sensitive environment values are imported through Bahia secrets when secret storage/encryption is configured.
- Compose-origin containers are direct-Docker takeover candidates; enable takeover only after operators accept that Bahia, not Compose, will drive restart/deploy/stop actions.

## Enablement checklist

1. Enable signer-first operator features and allowlists:

   ```yaml
   adoption:
     enabled: true
     allow_raw_docker_hosts: false
     allow_compose_takeover: false
     allowed_pubkeys: ["<operator-hex-pubkey>"]

   direct_runtime_actions:
     enabled: true
     allowed_pubkeys: ["<operator-hex-pubkey>"]

   nostr:
     authorized_pubkeys: ["<global-operator-hex-pubkey>"]
   ```

   Notes:
   - `adoption.allowed_pubkeys` and `direct_runtime_actions.allowed_pubkeys` scope signer-first operator execution.
   - `nostr.authorized_pubkeys` remains the global fallback for public operator request authorization.
   - Subject/email operator allowlists are compatibility-only and do not authorize signer-first public events.

2. Configure endpoint aliases; do not expose Docker credentials to clients:

   ```yaml
   runtime:
     endpoints:
       prod-docker:
         docker_host: tcp://docker-prod.example.com:2376
         ca_cert_file: /etc/bahia/docker/prod/ca.pem
         client_cert_file: /etc/bahia/docker/prod/cert.pem
         client_key_file: /etc/bahia/docker/prod/key.pem
   ```

3. Confirm signer-first discovery and topology evidence:
   - `/api/v1/system/info` is captured for the release candidate
   - relay URLs are available either via explicit `--relay`, `BAHIA_NOSTR_RELAYS`, or `/api/v1/system/info` discovery (`nostr.browser_relays`, `nostr.sidecar_url`)
   - if encrypted request/result web validation is in scope, verify `/api/v1/system/info` advertises `nostr.browser_encrypted_request_relays` and `features.encrypted_nostr_requests`
   - if sidecar/web validation is in scope, verify `/relay` pathing and reachability

4. Prepare signer/operator execution inputs:
   - signer key material is available via `--nsec`, `--privkey`, `BAHIA_NOSTR_NSEC`, or `BAHIA_NOSTR_PRIVATE_KEY`
   - operators know whether compatibility HTTP fallback is approved for this rollout
   - evidence capture includes request event IDs and correlated status/result event IDs

## Dry-run scan

Run a signer-first scan before importing anything:

```bash
bahia --relay wss://relay.example/relay adopt scan --target prod-docker
```

Validate:

- candidate count matches the expected running workloads;
- every candidate has an image digest;
- `redacted_environment_keys` / `redacted_label_keys` contain only key names, never values;
- compose-origin warnings are understood before enabling `allow_compose_takeover`;
- logs show the operator actor pubkey and endpoint alias, not raw secrets or certificate material;
- CLI status appears on `stderr` only, while final result output remains clean on `stdout`.

## Import rollout

1. Start with one non-critical Docker-origin workload.
2. Import by explicit selection before using `--all`:

   ```bash
   bahia --relay wss://relay.example/relay adopt import --target prod-docker --select prod-docker/<container-id>=<name>
   ```

3. Confirm:

   - service, environment, build, artifact, state, and runtime observation rows exist;
   - request, status, and terminal result event IDs are captured;
   - metrics advanced: `bahia_adoption_imports_total`, success/failure counters, redaction counters;
   - no raw sensitive env values are present in results or logs.

4. Only then import additional workloads or use `--all` for a bounded target.

## Direct runtime actions

Direct runtime actions are intended only for imported direct-runtime workloads. Failed guardrails must fail closed and should not be bypassed.

Use signer-first CLI actions after import validation:

```bash
bahia --relay wss://relay.example/relay services actions restart --service <service-id> --environment <env-id>
bahia --relay wss://relay.example/relay services actions stop --service <service-id> --environment <env-id>
bahia --relay wss://relay.example/relay services actions deploy --service <service-id> --environment <env-id> --artifact <artifact-id>
```

Monitor:

- correlated `6963` action status and `7962` terminal result events;
- `bahia_runtime_actions_total` and duration metrics;
- logs with `service_id`, `environment_id`, optional `artifact_id`, `target_name`, `endpoint_ref`, `result`, `request_id`, and request event id.

## Compatibility-only fallback mode

Fallback is not the primary operator path.
Use it only when explicitly approved.

- Enable with `--http-fallback` or `BAHIA_OPERATOR_HTTP_FALLBACK=true`.
- Fallback is allowed only before any relay accepts the signer-first request.
- `--raw-target` is compatibility-only and requires explicit fallback approval.
- Do not use fallback to bypass signer-first terminal failures, authorization failures after acceptance, or runtime guardrails.

Example compatibility-only raw-target invocation:

```bash
bahia --http-fallback adopt scan --raw-target breakglass=tcp://127.0.0.1:2375
```

## Rate limits and telemetry

Dedicated operational rate limits remain separate from the generic write limiter:

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

If adoption or direct-runtime execution causes unexpected behavior:

1. Disable the execution surface and restart Bahia:

   ```yaml
   adoption:
     enabled: false
   direct_runtime_actions:
     enabled: false
   ```

2. Retry signer-first operator requests and verify they fail closed.
3. Stop issuing direct runtime actions. For compose-origin workloads, return to the Compose project and run the normal Compose deployment/restart flow from the original project directory.
4. If a workload should no longer be Bahia-managed, remove or quarantine the imported service/environment state through the normal registry/admin path after exporting audit records.
5. Keep endpoint aliases configured until rollback verification is complete so observations can still be inspected if needed.

## Compatibility notes

- HTTP privileged adoption/import/direct-runtime endpoints are no longer the primary rollout gate.
- Bearer rejection (`401`) and any legacy NIP-98 execution checks are compatibility evidence only.
- Canonical encrypted request/result terminology: `nostr.encrypted_request_relays`, `nostr.browser_encrypted_request_relays`, `features.encrypted_nostr_requests`.
- Deprecated aliases retained for mixed-version operator rollouts: `private_relays`, `private_browser_relays`, `private_nostr_transport`.
- Wire marker `bahia-private-v1` remains unchanged as a legacy v1 routing marker.
- If a release requirement still depends on the legacy HTTP operator path, record that dependency explicitly in the signoff evidence.
