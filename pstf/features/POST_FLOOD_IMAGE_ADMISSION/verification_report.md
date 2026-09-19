# POST_FLOOD_IMAGE_ADMISSION verification

## Source verification

- Unit tests cover revision floors, OCI safety metadata, exact-release binding, legacy digest admission, and sanitized rollback generation.
- The existing digest-only Compose updater suite remains green.
- The real canonical `d34b8e25...` revision passes both ancestry floors.
- The pre-fix `09edf5ef...` revision is rejected.

## Remaining live verification

- Build the guarded backport from the accepted `781e26f0...` lineage.
- Prove the candidate image passes admission and `086021...` fails.
- Deploy only the Bahia service while preserving relay/web/config/data.
- Prove readiness/public health and sustained event/rejection telemetry.
- Quarantine unsafe rollback snapshots and remove unused pre-floor Bahia image IDs.

## Tracking

Authoritative task: `bahia-post-flood-image-admission-20260919`.

Repository Beads creation was attempted but the configured Dolt backend was unavailable on this host; no fake or parallel Beads record was created.
