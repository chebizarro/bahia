# POST_FLOOD_IMAGE_ADMISSION verification

## Source verification

- Unit tests cover revision floors, OCI safety metadata, exact-release binding, legacy digest admission, and sanitized rollback generation.
- The existing digest-only Compose updater suite remains green.
- The real canonical `d34b8e25...` revision passes both ancestry floors.
- The pre-fix `09edf5ef...` revision is rejected.

## Live artifact and rollback verification

- Repository Docker build from the accepted `781e26f0...` lineage produced runtime revision `d84fda3be7f79f527f3cf4793d53496643c50f38` and edge image ID `sha256:f52bc3d746d4ec2c5349f92c3c2e0cd85ec846d02e7513cbf7631e3139212895`.
- The guarded rollout replaced only `bahia`. The dedicated relay remained on `sha256:84bded311a1333bcc29728038178a79d08c17ca46d9ecc3624f8d2524867aa6c`; web, configuration, identities, policy, and data were preserved.
- Bahia and relay are healthy with zero restarts; Bahia origin health/readiness and public health passed.
- All 264 historical Compose fallbacks were rewritten so only `services.bahia.image` uses admitted rollback `sha256:824e4ffa0954d2135b87169233f1a9edd04fc5148125904ba0c7b95b61e6b16a`. Original bytes are retained as mode-000 checksummed evidence. Manifest SHA-256: `6c3ca531ac3f3425565487e49dda80438cbb9ca990b920088f688480a7f0bf77`.
- The initial Docker audit found 215 Bahia backend images: two admitted and 213 rejected. One exited container and 212 rejected image objects were removed. One shared object was detached from its protected Bahia tags and retained only as `bahia-relay:rollback-pre-durable-relay-v8`.
- Post-purge audit reports exactly two admitted Bahia images and zero rejected images. Audit SHA-256: `84d97a1b0197687c315c771e294f9845db1b8bb2374c9dcb488889f7f52a8bad`.
- The formerly live unsafe image `sha256:0860217692710fe58b4e0060ee0b1be16dc1f2dd3465346e2cba2dc914098638` is absent from the edge image store.
- Canonical `master` contains the fail-closed deployment, rollback, backup-quarantine, and image-store admission tooling. Automatic push deployment was removed; the workflow now requires an explicit full revision and exact ancestry proof.

## Telemetry finding still requiring acceptance

- The first ten minutes included a startup catch-up burst at 17:07 UTC involving historical SBOM projections.
- A later five-minute window contained three rejected DNS-agent list request events at 17:17 UTC (six matching log lines because each relay rejection is also wrapped by the caller). There were no process-fatal signatures; the prior broad `oom` matcher was a false positive on `loom-*` environment names.
- This is not the former continuous projection storm, but it does not satisfy the task's zero-rejection acceptance gate. Artifact admission is complete; the task remains open for independent review and publisher/relay telemetry follow-up.

## Tracking

Authoritative task: `bahia-post-flood-image-admission-20260919`.

Repository Beads creation was attempted but the configured Dolt backend was unavailable on this host; no fake or parallel Beads record was created.
