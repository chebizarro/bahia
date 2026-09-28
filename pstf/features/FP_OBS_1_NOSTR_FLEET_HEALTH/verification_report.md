# Verification Report — fp-obs-1

## Scope

Nostr-native fleet health projection for signed canonical observables, including explicit relay ingestion health, bounded Prometheus labels, and documentation of the direct-scrape boundary.

## Acceptance mapping

- AC1: the subscriber includes kinds 30315, 30316, 30317, 30900, and 4903 and invokes the projector only after validation and durable event persistence.
- AC2: projector tests prove an older replaceable event cannot regress newer subject state.
- AC3: lifecycle tests distinguish subscription/EOSE/CLOSED state from retained subject state.
- AC4: metrics tests reject attacker-controlled domain/status labels and prove raw event content is absent.
- AC5: the operator guide declares relay events as the semantic source and restricts direct scraping to node/process/GPU exporters.

## Verification

- `go test ./internal/adapters/telemetry ./internal/adapters/nostr ./internal/app` — passed.
- `git diff --check` — passed.

## 2026-09-27 independent-review remediation

- Finding 1: semantic kinds now require a recognized schema for the wire kind, required kind tags, a size-bounded nonempty JSON-object envelope, matching content schema when present, and the assistant-transcript encrypted envelope shape. Generated canonical validators are applied to `bahia.service-state.v2`, `bahia.worker-state.v1`, `bahia.audit.build.v1`, and `bahia.audit.deployment.v1`. Known schemas without canonical validators are accepted only from trusted publishers and counted as schema-unvalidated in content-free event and entity metrics, per the owner's explicit decision. Admission runs after NIP-01 verification but before event persistence, handler dispatch, or fleet-health observation. Rejections increment a bounded, event-ID-deduplicated content-free counter.
- Finding 2: publisher trust is resolved from Bahia's service key, current worker records, active SoulFactory agent read models, and existing managed controller/runtime/assistant identities. There is no observability-specific publisher list. A metrics scrape drops entities whose publishers have been deregistered. Registration lookup errors fail closed for new events without reclassifying already-observed subjects as failed.
- Oracle reviewed the complete committed patch and identified one non-blocking counter undercount for accepted out-of-order schema-unvalidated events; the counter now records distinct trusted, valid-envelope events before projection-only early returns, and the canonical/unvalidated test covers newest-first delivery.
- Tests: `TestNostrFleetHealthRejectsInvalidSchemaContentFree`, `TestNostrFleetHealthRejectsKindSchemaMismatchAndMalformedPayload`, `TestNostrFleetHealthCanonicalValidatorAndUnvalidatedMarker`, `TestNostrFleetHealthRejectsOversizedEnvelopeBeforeTrustLookup`, and `TestSubscriberAdmissionRejectsSemanticEventBeforePersistenceOrProjection` cover finding 1. `TestNostrFleetHealthCurrentWorkerAndAgentRegistrations`, `TestNostrFleetHealthUntrustedSignersCannotExhaustEntityBudget`, and `TestIsRegisteredAgentTracksCurrentSoulRegistration` cover finding 2. Existing route-canary integration fixtures now configure their existing service signer as trusted.
- Local gates in the `obs` worktree: `go build ./...`, `go vet ./...`, `go test ./...`, `go test -race ./internal/adapters/telemetry ./internal/adapters/nostr ./internal/app ./internal/soulfactory ./internal/service`, and `git diff --check` passed. This is local source/test verification, not live relay or deployment acceptance.

Residual risk: all known allowlisted schema families except the four listed above lack a generated Go payload validator. They are marked schema-unvalidated, and admission cannot prove domain-specific payload fields or cross-field invariants. These schema families need canonical validators as follow-up; do not treat the marker as full validation. The current SoulFactory registration lookup performs an EOSE-bounded, 1,000-event relay query per unknown publisher and can delay semantic admission or metrics collection while that relay is impaired; it fails closed for new events. The fixed 1,000-event window can omit a registered agent, causing a false rejection. Registration checks and entity insertion are not one atomic transaction with external registration changes, so a concurrent revocation can leave a stale entity until the next metrics scrape. Configured controller/runtime identities remain trusted until Bahia configuration changes. These are follow-up hardening items, not live acceptance evidence.

### Known schema families lacking canonical payload validators

These are admitted only from trusted publishers after strict envelope validation and are counted as schema-unvalidated. The generated validators cover the four schemas listed above.

- **30315 status** (9): `bahia.status.continuity-heartbeat.v1`, `bahia.status.dns.v1`, `bahia.status.managed-instance-health.v1`, `bahia.status.package.v1`, `bahia.status.route-canary.v1`, `bahia.status.security-scan.v1`, `bahia.status.service.v1`, `bahia.status.worker.v1`, `bahia.agent-runtime-release.v1`.
- **30900 state/control** (41): `bahia.cp-state.v1`, `bahia.assistant-session.v2`, `bahia.relay-settings.v1`, `cascadia.config.status.v1`, `cascadia.config.status.v2`, `bahia.dnsagent.state.v1`, `bahia.security.scan-summary.v1`, `bahia.security.target-summary.v1`, `bahia.state.assistant-session.v1`, `bahia.state.backup-observation.v1`, `bahia.state.backup-restore.v1`, `bahia.state.backup-run.v1`, `bahia.state.backup-verification.v1`, `bahia.state.continuity-profile.v1`, `bahia.state.dns-backend.v1`, `bahia.state.dns-endpoint.v1`, `bahia.state.dns-policy.v1`, `bahia.state.dns-zone.v1`, `bahia.state.failover-policy.v1`, `bahia.state.llm-route.v1`, `bahia.state.managed-instance-health.v1`, `bahia.state.ml-evaluation.v1`, `bahia.state.ml-inference-endpoint.v1`, `bahia.state.ml-provenance.v1`, `bahia.state.ml-recipe-run.v1`, `bahia.state.ml-runtime-capability.v1`, `bahia.state.package-artifact.v1`, `bahia.state.package-promotion.v1`, `bahia.state.package-repository.v1`, `bahia.state.recovery-workflow.v1`, `bahia.state.replication-policy.v1`, `bahia.state.route-canary.v1`, `bahia.state.service.v1`, `bahia.state.soul-factory-provisioning.v1`, `bahia.state.standby-node.v1`, `bahia.state.virtualization.v1`, `bahia.state.worker-assignment.v1`, `bahia.state.worker-cleanup.v1`, `bahia.state.worker-drain.v1`, `bahia.state.worker-eligibility.v1`, `bahia.state.worker.v1`.
- **4903 audit** (11): `bahia.audit.v1`, `bahia.audit.artifact-registration.v1`, `bahia.audit.assistant-execution-checkpoint.v1`, `bahia.audit.backup-run-attestation.v1`, `bahia.audit.backup-verification-attestation.v1`, `bahia.audit.managed-instance-health.v1`, `bahia.audit.release-promotion.v1`, `bahia.audit.release.v1`, `bahia.audit.route-canary.v1`, `bahia.audit.security.v1`, `bahia.audit.virtualization.v1`.
- **30316 assistant transcript**: `bahia.assistant-transcript.v1` (envelope shape is checked, but no generated canonical payload validator).
- **30317 runtime capability**: `soulfactory-runtime-capability/v1` (no generated canonical payload validator).
