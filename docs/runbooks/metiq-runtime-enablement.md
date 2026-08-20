# Metiq runtime enablement and validation

Task: **bahia-agent-runtimes-infrastructure-20260810**

This is a Track A artifact and a Track B operator procedure. Running the commands that contact Signet, relays, Bahia, or a runtime is Track B work. Do not migrate incumbents. Marjam and SNR identities, signer custody, runtime state, bindings, and storage remain untouched.

## Inputs and boundaries

- Render `deploy/soulfactory/metiq-enablement.yaml.template` with the exact OpenClaw runtime pubkey, dedicated Metiq runtime pubkey, SoulFactory controller/operator pubkeys, and canonical service relay set from the approved signed `30002`/`10002` topology capture.
- Keep the existing SoulFactory Signet and LLM secret injection unchanged. The enablement overlay contains no secret.
- Render `deploy/soulfactory/metiq-signet-enrollment.json.template` to a mode-`0600` host file. It contains public keys and protected file paths only.
- Store the Signet provisioner credential in the configured host file, owned by the executing UID and mode `0600`. Never export it in a host environment, pass it in argv, print it, or copy it into evidence.
- Pin the Bahia, Metiq bridge, OpenClaw bridge, Signet, and relay images by `repository@sha256:<64-hex>` before Track B starts.

## Capture the prior configuration

On the target host, stop before any service mutation and create a protected change directory:

```sh
umask 077
change_dir=/var/lib/bahia/changes/metiq-runtime-<CHANGE_ID>
install -d -m 0700 "$change_dir"
install -m 0600 "$BAHIA_CONFIG" "$change_dir/prior.yaml"
shasum -a 256 "$change_dir/prior.yaml" >"$change_dir/prior.sha256"
```

Render the candidate by changing only the runtime list, exact runtime-pubkey pins, and approved relay/controller values. Preserve all other prior keys byte-for-byte where the renderer permits. Store it as `candidate.yaml` mode `0600`; record its digest, not its contents, in evidence. Review a redacted structural diff. A runtime pubkey not listed under `runtime_pubkeys.metiq` must not be eligible even if it publishes a valid signed capability naming the controller.

## Rehearse rollback without deployment

Run the repository rehearsal against protected copies:

```sh
scripts/rehearse_metiq_config_rollback.sh \
  "$change_dir/prior.yaml" "$change_dir/candidate.yaml" \
  >"$change_dir/rollback-rehearsal.json"
chmod 0444 "$change_dir/rollback-rehearsal.json"
```

The command applies candidate and prior copies in an isolated temporary directory, proves byte-for-byte restoration, and emits only SHA-256 digests. This does not restart Bahia or contact infrastructure. Track B repeats the restoration against the real config boundary during the controlled rollback gate.

## Provision the dedicated Metiq Signet identity

Build the tool from the reviewed commit. The `enroll` action invokes containerized `signetctl provision`, creates a persistent file-backed NIP-46 client key, installs a deny-by-default policy for the exact client pubkey, verifies the managed identity and signing path, persists a secret-free contract, and deletes the one-time bunker handoff only after proof succeeds:

```sh
metiq-signet-enrollment -config /etc/bahia/metiq-signet-enrollment.json enroll \
  >"$change_dir/metiq-enrollment-public.json"
```

The host process reads the provisioner credential at each `signetctl` execution and sends it only on stdin to the fixed container shell. It is absent from host argv, logs, configuration, and output. The Metiq profile permits only `connect`, `get_public_key`, `get_relays`, `ping`, and `sign_event`, with signing limited to the Cascadia capability kind and Bahia runtime-control result kind. Wildcard clients and methods are rejected.

Operations:

```sh
# Secret-free local contract inspection; no Signet mutation.
metiq-signet-enrollment -config /etc/bahia/metiq-signet-enrollment.json inspect

# Reapply exact policy and prove durable NIP-46 signing after restart.
metiq-signet-enrollment -config /etc/bahia/metiq-signet-enrollment.json reconcile

# Revoke the exact client and remove only its Bahia-owned client/state files.
metiq-signet-enrollment -config /etc/bahia/metiq-signet-enrollment.json revoke

# Same safe cleanup for a failed enrollment attempt.
metiq-signet-enrollment -config /etc/bahia/metiq-signet-enrollment.json compensate
```

Revoke/compensate never deletes a valid Signet-custodied identity and never touches OpenClaw or incumbent material. A failed connectivity proof revokes the new exact client but retains the protected one-time handoff and retry-stable client key for inspected recovery.

## Track B enablement gates

1. Capture Marjam and SNR public event IDs and one-way state fingerprints before change.
2. Confirm Bahia, Signet, relay, OpenClaw, and Metiq health/readiness; confirm all promoted image/source digests.
3. Install `candidate.yaml` atomically, restart only Bahia, and verify `agent_runtimes=[openclaw,metiq]` plus exact pubkey pins. Do not publish a deployment intent from this Track A session.
4. Start the Metiq bridge on its dedicated host/container boundary with persistent idempotency/binding state, health/readiness, limits, restart policy, logs, and backup/restore configured by Track B.
5. Require a fresh signed Metiq `30317` from the pinned runtime pubkey, addressed to the trusted controller and advertising `soulfactory.provision` plus exactly one lifecycle method selected for the disposable test.
6. Provision only a disposable soul. Capture event IDs for `31952 → 5950 → 38384 → 38386 → 31951/7950`.
7. Inspect Metiq local state after first provision, exact replay, conflicting replay, supported lifecycle request, unsupported direct request, and restart. Record counts plus one-way binding and process-instance fingerprints only.
8. Restart the Metiq bridge: require a distinct process-instance fingerprint, a newer `30317`, unchanged binding/effect count, and recovered state. Restart Bahia and record a distinct Bahia process-instance fingerprint while withholding or delaying the runtime result, then prove either EOSE backfill or late-result reconciliation to the correlated `7950`/`31951`.
9. Confirm Marjam and SNR before/after event IDs and fingerprints are identical.
10. Restore the prior config in the rollback gate, restart Bahia, prove its digest equals the captured prior digest, and reconfirm OpenClaw/Marjam/SNR. Do not delete the valid Metiq identity or public event lineage.

## Run the validation harness

Render `deploy/soulfactory/metiq-runtime-validation.json.template` with public event IDs and sanitized local counters/fingerprints. Query only the canonical relay set; the harness uses one ID-scoped subscription and EOSE, validates IDs/signatures/authors/kinds/correlation, and emits no event content:

```sh
soulfactory-runtime-validate \
  -scenario "$change_dir/metiq-runtime-validation.json" \
  -relays "$CANONICAL_SOULFACTORY_RELAYS" \
  >"$change_dir/metiq-runtime-validation-report.json"
chmod 0444 "$change_dir/metiq-runtime-validation-report.json"
```

CI runs the same validator with an in-memory event source:

```sh
go test ./internal/soulfactory -run 'TestValidateRuntimeScenario'
```

## Immutable sanitized evidence

Evidence may contain only reviewed commit/image/config digests, timestamps, check outcomes, public pubkeys, one-way local-state fingerprints, and Nostr event IDs. Never include event content, bunker URIs, NIP-46 client keys, provisioner credentials, nsecs, decrypted messages, environment dumps, or unredacted logs.

1. Make the scenario, validation report, rollback rehearsal, and public enrollment contract read-only.
2. Hash each file and store the files plus hashes in the approved append-only/WORM evidence location.
3. Record the object version/retention identifier separately; do not rewrite a failed report. Create a new evidence version for every rerun.
4. An independent reviewer resolves every listed event ID from the canonical relays and repeats the checklist below.

## Independent validation checklist

- [ ] Dedicated Metiq managed/runtime/client pubkeys differ from OpenClaw, Marjam, SNR, controller, and provisioner pubkeys.
- [ ] Signet policy is deny-by-default, exact-client, signing-only, kind-limited, and contains no wildcard.
- [ ] Credential and one-time handoff are absent from argv, logs, config, public events, and evidence; handoff is removed after durable proof.
- [ ] Metiq idempotency/binding state survives bridge restart; provision count remains exactly one across replay/conflict/restart.
- [ ] Every event ID has a valid NIP-01 ID/signature and exact author/kind/address/correlation lineage.
- [ ] `31951` and `7950` contain no private or one-time secret material.
- [ ] Exactly one advertised lifecycle method succeeds; an unadvertised method returns non-retryable `unsupported_method` without a local effect.
- [ ] Conflicting idempotency reuse returns non-retryable `duplicate_conflict`; exact replay is a no-op with the cached logical result.
- [ ] Newer `30317`, recovered binding/state, and Bahia backfill or late-result reconciliation are proven after restart.
- [ ] Marjam, SNR, and OpenClaw identity/state/bindings remain unchanged.
- [ ] Candidate rollback restores the captured prior config digest and OpenClaw remains operational.
