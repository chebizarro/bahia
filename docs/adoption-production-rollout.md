# Adoption and Direct Runtime Production Rollout Runbook

Scope: production/staging rollout of signer-first adoption/import and direct-runtime operator workflows.
Normative gate: [`adoption-live-network-verification.md`](adoption-live-network-verification.md)
Execution checklist: [`adoption-signer-first-operator-checklist.md`](adoption-signer-first-operator-checklist.md)

This runbook assumes signer-first operator execution over Nostr control-plane requests.
The legacy privileged REST adoption/import and direct-runtime mutation routes are no longer mounted (`internal/api/router/router.go`); signer-first ContextVM `25910` is the only working execution path.

## Safety defaults

- Adoption and direct runtime actions are disabled unless explicitly enabled.
- Signer-first operator execution is authorized by operator pubkeys and signed event verification.
- Prefer server-managed `runtime.endpoints.<ref>` aliases. Raw `docker_host` request payloads are compatibility/break-glass only.
- CLI defaults to signer-first operator transport for `bahia adopt ...` and `bahia services actions ...`.
- HTTP compatibility fallback is opt-in only (`--http-fallback` or `BAHIA_OPERATOR_HTTP_FALLBACK=true`) and is consulted only before any relay accepts the signed request. For `adopt` and `services actions` the fallback no longer reaches a server: the client returns `REST ... is removed; publish a signed Nostr ... event instead`.
- Scan and import responses redact sensitive environment variables and labels. Sensitive environment values are imported through Bahia secrets when secret storage/encryption is configured.
- Compose-origin containers are direct-Docker takeover candidates; enable takeover only after operators accept that Bahia, not Compose, will drive restart/deploy/stop actions.
- Signed imports must resolve one organization. Pass `--org <organization-uuid>` when target environments/the organization catalog do not make the choice unambiguous; Bahia rejects cross-org reuse.
- Each imported service is bound to a deployment unit, and its initial state plus observation carry that unit identity.
- Importing a same-name legacy service with no adopted-runtime identity is an explicit takeover. An already-adopted same-name service on a different target remains a hard conflict.

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
   - `nostr.authorized_pubkeys` is an optional outer pre-filter: when non-empty, the ContextVM transport rejects any requester not in it before dispatch. When empty, the pre-filter is disabled (so tenant-scoped browser signers can reach RBAC-checked methods) and the server logs a startup warning.
   - `adoption.allowed_pubkeys` and `direct_runtime_actions.allowed_pubkeys` are then checked per method (`adoption/*` and `service/action` respectively). These checks **fail closed**: an empty list authorizes no signer.
   - Config load rejects `adoption.enabled=true` or `direct_runtime_actions.enabled=true` unless that surface has a non-empty `allowed_pubkeys` list of 64-character hex pubkeys. Entries are lowercased and deduplicated. A subject- or email-only allowlist is rejected with an error, because those entries cannot authorize signer-first requests.
   - Subject/email operator allowlists are compatibility-only. They still apply to HTTP/NIP-98 routes, but they never authorize signer-first public events.

   > **NOTE (2026-09-11, updated):** Earlier builds treated an empty `adoption.allowed_pubkeys` / `direct_runtime_actions.allowed_pubkeys` as "allow any signer" and accepted subject/email-only allowlists at load. Fixed under `bahia-9sav5`. Existing configs that relied on subject/email-only allowlists for these surfaces now fail to load until `allowed_pubkeys` is set. Other operator-only ContextVM methods (worker, backup, relay settings, DNS, and similar) still rely on the `nostr.authorized_pubkeys` pre-filter alone; see `bahia-4yhej`. Set a non-empty `nostr.authorized_pubkeys` for operator-only deployments.
   - `direct_runtime_actions.enabled=true` also requires `auth.enabled=true` and `nostr.private_key`.

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
   - ContextVM discovery (`11316`-`11320`) plus NIP-51 relay sets (`30002`) is captured for the release candidate
   - relay URLs are available either via explicit `--relay`, `BAHIA_NOSTR_RELAYS`, or trusted ContextVM/NIP-51 discovery (`bahia-contextvm-v1` preferred, `bahia-browser-v1` fallback)
   - if encrypted request/response web validation is in scope, verify ContextVM discovery plus NIP-51 relay sets advertise `nostr.browser_relays` / `nostr.contextvm_relays` and `features.encrypted_nostr_requests`
   - if sidecar/web validation is in scope, verify `/relay` pathing and reachability

4. Prepare signer/operator execution inputs:
   - local signer key material is available via `--nostr-key-file`, `BAHIA_NOSTR_KEY_FILE`, `BAHIA_NOSTR_NSEC`, or `BAHIA_NOSTR_PRIVATE_KEY`; the CLI intentionally has no `--nsec` or `--privkey` flags
   - or NIP-46 is configured with `--nostr-bunker-file` and `--nostr-client-key-file` (plus repeatable `--nostr-bunker-relay` when needed)
   - operators know whether HTTP compatibility fallback is approved for this rollout
   - evidence capture includes request event IDs and correlated progress/terminal ContextVM `25910` response IDs, plus canonical observable IDs where emitted

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
   bahia --relay wss://relay.example/relay adopt import --org <organization-uuid> --target prod-docker --select prod-docker/<container-id>=<name>
   ```

3. Confirm:

   - service, environment, build, artifact, deployment unit, state, and runtime observation rows exist;
   - service/environment organization IDs match `--org`, and state/observation rows reference the imported deployment unit;
   - the request event ID and correlated terminal ContextVM `25910` response event ID are captured, along with any progress or canonical observable event IDs;
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

- the correlated ContextVM `25910` response plus canonical `30315` status, `4903` audit, and `30900` state events; legacy `6963`/`7962` are migration inventory only;
- `bahia_runtime_actions_total` and duration metrics;
- logs with `service_id`, `environment_id`, optional `artifact_id`, `target_name`, `endpoint_ref`, `result`, `request_id`, and request event id.

## Compatibility-only fallback mode

Fallback is not an operator path for adoption or direct runtime actions.

- `--http-fallback` / `BAHIA_OPERATOR_HTTP_FALLBACK=true` is consulted only before any relay accepts the signer-first request.
- For `adopt scan`, `adopt import`, and `services actions {deploy,restart,stop}`, the fallback calls client methods that now return a `REST ... is removed` error. No HTTP request is made and no runtime action runs.
- `--raw-target` is rejected without `--http-fallback`. With `--http-fallback` it takes the same removed-REST path and fails. Raw Docker hosts cannot be scanned or imported from the CLI; register a `runtime.endpoints.<ref>` alias instead.
- Do not use fallback to bypass signer-first terminal failures, authorization failures after acceptance, or runtime guardrails.

The raw-target invocation below is kept only as the SF-04 negative check. It must fail without contacting the Docker host:

```bash
bahia --http-fallback adopt scan --raw-target breakglass=tcp://127.0.0.1:2375
```

## Rate limits and telemetry

Signer-first requests arrive over relays, not REST, so the per-IP REST limiters (100/minute reads, 30/minute writes) do not apply to them. The former dedicated adoption/runtime-action per-IP limits went away with the REST routes.

> **NOTE (2026-09-11):** No relay-side rate limit specific to adoption or
> direct runtime actions was found in this checkout. Rely on relay policy and
> the operator allowlists above for abuse control.

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

- HTTP privileged adoption/import/direct-runtime endpoints are not mounted in current Bahia and cannot serve as a rollout gate or fallback.
- Legacy bearer/NIP-98 checks against those routes will see unmounted-route responses, not auth rejections. Record them only as proof the routes are absent.
- Canonical encrypted request/result terminology: `nostr.relays`, `nostr.browser_relays`, `features.encrypted_nostr_requests`.
- Encrypted request/result wire marker is `encrypted=bahia-encrypted-v1`.
- If a release requirement still depends on the legacy HTTP operator path, record that dependency explicitly in the signoff evidence.
