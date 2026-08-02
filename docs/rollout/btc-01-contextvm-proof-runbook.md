# btc-01 ContextVM Adoption Proof Runbook

> **Status (2026-08-01): Operator proof procedure, not rollout evidence.** The
> acceptance checklist remains an explicit manual gate; this document does not
> assert that btc-01 adoption has been executed.

Scope: operator-gated proof for btc-01 over canonical ContextVM kind `25910`. This runbook is procedural only; do not execute it from unattended agent sessions. Keep `bitcoind` and `lnd` out of scope.

## Preconditions

- Bahia is deployed with locked-down defaults explicitly enabled for the proof:
  - `nostr.private_key` supplied by deployment secret or environment, not committed config.
  - `nostr.publish_enabled=true`.
  - at least one `nostr.contextvm_relays` control-plane relay reachable by Bahia and the operator client.
  - `auth.enabled=true` with operator/agent allowlists for Biz, Stew, and the Bahia signer as applicable.
  - `adoption.enabled=true` and `direct_runtime_actions.enabled=true`.
- btc-01 Docker access is registered as a server-managed runtime endpoint alias, for example `runtime.endpoints.btc-01-docker`.
- Adoption requests use `endpoint_ref: "btc-01-docker"`; raw `docker_host` in Nostr payloads is forbidden for this proof.
- Operator signer can publish signed ContextVM `25910` requests and subscribe to correlated `30315`, `30900`, and `4903` observables.

## Target boundaries

Allowed initial import candidates:

- `rtl`
- `lnbits`

Excluded from this proof:

- `bitcoind`
- `lnd`
- any compose-wide takeover beyond the selected low-risk service

## Procedure

1. **Discover ContextVM surface**
   - Confirm Bahia publishes ContextVM discovery (`11316`-`11320`) on the configured relay set.
   - Confirm required methods are present: `adoption/scan`, `adoption/import`, `service/deploy`, and direct runtime restart/action support.

2. **Scan btc-01**
   - Publish a signed ContextVM `25910` request for `adoption/scan` with a target that references `endpoint_ref: "btc-01-docker"`.
   - Verify scan output lists btc-01 Docker containers and compose-origin warnings.
   - Verify environment values are redacted and no raw Docker credentials or sensitive env values appear in relay events, logs, or operator output.

3. **Select one low-risk workload**
   - Choose exactly one of `rtl` or `lnbits`.
   - Record container name, image, digest, compose labels, ports, volumes, and redaction evidence.

4. **Import selected workload**
   - Publish signed ContextVM `25910` `adoption/import` for the selected workload using `endpoint_ref: "btc-01-docker"`.
   - Confirm Bahia persists service, environment, artifact/build, adoption metadata, and canonical state projection.
   - Confirm audit (`4903`) and status (`30315`) events correlate to the request event ID.

5. **Restart through Bahia**
   - Publish the Bahia direct-runtime restart action for the imported service/environment.
   - Confirm the restart completes through the registered endpoint alias and does not require raw `docker_host` in the request.
   - Confirm observed state returns to healthy or starting during grace, then healthy.

6. **Deploy proof version**
   - Register an artifact for a new version of the imported service.
   - Publish signed ContextVM `25910` `service/deploy` for the imported service/environment.
   - Verify the runtime pulls/deploys by image digest, reconcile observes the new digest, and the service state reports `in_sync`.

7. **Evidence capture**
   - Save request event IDs, correlated status/result/audit/state event IDs, sanitized operator output, and Bahia logs.
   - Capture proof that `bitcoind` and `lnd` were not imported, restarted, deployed, or otherwise touched.

8. **Rollback / disable**
   - If any unexpected behavior occurs, set `adoption.enabled=false` and `direct_runtime_actions.enabled=false`, restart Bahia, and verify the mutation surface is closed while existing read-model state remains inspectable.

## Acceptance checklist

- [ ] `25910` methods are discoverable and no longer return `method not found` after deployment enablement.
- [ ] btc-01 scan uses `endpoint_ref` and shows redacted compose-origin data.
- [ ] Exactly one of `rtl` or `lnbits` is imported.
- [ ] Bahia restarts the imported service via direct runtime action.
- [ ] Bahia deploys a new artifact by digest and reconcile reports `in_sync`.
- [ ] `bitcoind` and `lnd` remain untouched.
- [ ] Evidence includes request IDs, correlated observables, and redaction proof.
