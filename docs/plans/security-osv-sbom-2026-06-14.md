# OSV-backed SBOM Security Feature: Plan

## Goal
Create a Nostr-native Security feature that reacts to SBOM creation/import/update, rescans on a configurable schedule, uses OSV for SBOM-derived package, package-coordinate, PURL, and Git commit vulnerability lookup, evaluates findings against policy-scoped thresholds, and sends notifications only for policy breaches.

## Background
- User decisions from the up-front checkpoint: implement scanning as a separate event-driven Security service, not inline in the SBOM transaction; support both package-coordinate and Git commit OSV lookup beyond SBOMs; make settings policy-scoped; notify on policy breaches rather than every finding.
- SBOM generation/import is already centralized in `SBOMOrchestrator.run`, which publishes accepted/running/completed `30315` status events, stores payloads in Blossom, publishes `30078` SBOM reference events and `30004` availability lists, projects manifests/packages, and emits `4903` audits (`internal/service/sbom_orchestrator.go:195`, `internal/service/sbom_orchestrator.go:264`, `internal/service/sbom_orchestrator.go:278`, `internal/service/sbom_orchestrator.go:300`).
- SBOM projections already preserve scan-relevant data: `SBOMManifest` has package and vulnerability aggregate counts, while `SBOMManifestPackage` stores `name`, `version`, `ecosystem`, `purl`, and `cpe` (`internal/domain/sbom.go:54`, `internal/domain/sbom.go:82`).
- Canonical SBOM truth is Nostr + durable payload storage, not REST polling: `30078` reference events include subject, format, storage, location, and payload hash; `30004` availability lists enumerate subject SBOM references (`internal/adapters/sbom/index.go:105`, `internal/adapters/sbom/index.go:194`). Existing PSTF verification confirms Blossom storage, SHA-256 verification, OK-verified publication, and `30315`/`4903` orchestration (`pstf/features/SBOM_WORKFLOW_E2E/verification_report.md:7`).
- Existing policy gates evaluate global plus environment-scoped policies before deployments and block with `policy_blocked` on blocking violations (`internal/service/policy.go:71`, `internal/controlplane/reactor.go:857`). Vulnerability-count policy rules currently read aggregate SBOM counts (`internal/service/policy.go:139`, `internal/service/policy.go:202`).
- Policy scope today is global or environment-specific via `DeploymentPolicy.EnvironmentID`; policy rules carry params such as `max` for `max_critical_vulns` and `max_high_vulns` (`internal/domain/policy.go:52`, `internal/domain/policy.go:21`). The new Security feature should extend this policy surface rather than create a parallel configuration plane unless implementation discovery finds a blocking mismatch.
- A lightweight OSV client already exists for tool provisioning (`internal/adapters/security/osv.go:16`, `internal/adapters/security/osv.go:33`, `internal/adapters/security/osv.go:64`), but it only calls `/v1/query`, caches by ecosystem/name/version, and returns simplified vulnerability data. It is prior art, not sufficient for SBOM batch scans, PURL queries, commit queries, vulnerability hydration, durable scan results, or policy-breach evaluation.
- Notifications are dispatched through typed channels (`webhook`, `nostr_dm`) with persisted filters and delivery logs. The extension seams are event type constants, `Dispatcher.SetupSubscriptions`, `Dispatcher.Dispatch`, and channel `EventFilter` matching (`internal/notifications/dispatcher.go:15`, `internal/notifications/dispatcher.go:42`, `internal/notifications/dispatcher.go:86`, `internal/domain/notification.go:51`). Notification configuration is managed through encrypted ContextVM operations (`internal/controlplane/notification_encrypted_handlers.go:43`).
- Scheduled background processing currently follows a repository-backed due-record pattern: immediate startup execution plus ticker cycles, selecting records whose due time has passed and skipping disabled policy rows (`internal/reconcile/reconciler.go:82`, `internal/repository/pg_state.go:158`). The Security scanner should use this pattern only for rescan cadence, not event delivery or completion detection.
- OSV API facts from official docs: OSV does not accept SBOM files directly through `/v1/query` or `/v1/querybatch`; clients must normalize SBOM components to package coordinates or PURLs. `/v1/querybatch` returns results aligned to request order and may require follow-up `GET /v1/vulns/{id}` hydration. Commit queries are supported. Invalid versioned-PURL plus top-level version combinations return `400`. Official references: https://google.github.io/osv.dev/api/, https://google.github.io/osv.dev/post-v1-query/, https://google.github.io/osv.dev/post-v1-querybatch/, https://google.github.io/osv.dev/get-v1-vulns/, https://ossf.github.io/osv-schema/.
- Prior art to preserve: `pstf/features/SBOM_WORKFLOW_E2E/acceptance_criteria.json` defines canonical SBOM behavior; `pstf/features/ENCRYPTED_CONTROL_PLANE_NOTIFICATIONS/acceptance_criteria.json` defines encrypted notification control-plane requirements; `docs/investigations/rest-api-audit-2026-06-01.md:90` classifies OSV as a permanent outbound HTTP dependency; recent SBOM commits include `73a3f667` (Nostr-native SBOM generation) and notification commits include `e78df3b` (private Nostr transport).

## Approach
Implement Security as an additive, event-driven subsystem. SBOM generation/import remains responsible for producing canonical SBOM references and projections; Security observes those canonical facts, performs scans, publishes its own durable observables, and feeds policy/notification surfaces.

### Nostr contract
Use existing canonical kind families unless the implementation phase proves a new kind is required by `docs/nostr-event-implementation-guide.md`:

- ContextVM kind `25910` for explicit scan/rescan mutation intent acknowledgments only.
- Kind `30315` for Security scan status with a Security schema tag and `d=security:scan:<run_id>`.
- Kind `30900` for scan summaries and latest target summaries.
- Kind `30078` for detailed normalized finding app-data with `domain=security`.
- Kind `4903` for Security audit facts, including scan lifecycle and policy breaches.

Manual ContextVM responses must not be treated as terminal truth. Clients and tests should subscribe to Security observables with narrow filters, process historical events until EOSE, keep subscriptions open when realtime updates are needed, handle CLOSED/AUTH, and verify publish OK responses for every Security event. Status events should expose meaningful phase/step tags, but exact step names belong to the implementation slice after event schemas are finalized.

### Security service boundary and wiring
Add a separate Security scan service. It should be initialized in app wiring alongside the SBOM, policy, notification, and relay components, with explicit dependencies for repository, OSV adapter, storage resolver, verified Nostr publisher, policy service, event publisher/notification dispatcher, logger, and config. Its lifecycle should mirror existing long-running service patterns: start subscriptions on application start, backfill with EOSE, keep realtime subscriptions open, re-issue subscriptions after reconnect, handle CLOSED/AUTH, and close subscriptions on shutdown.

The service should:

1. Subscribe to accepted SBOM references and availability lists with narrow filters.
2. Backfill missed SBOM references using EOSE as historical catch-up completion.
3. Resolve Blossom SBOM payloads and verify payload hashes before scanning.
4. Parse packages using existing SBOM parser/projection shapes.
5. Normalize package/PURL/commit targets into OSV requests.
6. Batch OSV lookups, hydrate vulnerability details when needed, and cache safely.
7. Persist scan runs, findings, target summaries, schedules, and policy-breach fingerprints.
8. Publish status, summary, finding, and audit observables with OK verification.
9. Evaluate policy-scoped thresholds and dispatch notifications only for new or changed policy breaches.

Do not add scan execution into `SBOMOrchestrator.run`; the SBOM lifecycle should remain non-blocking for Security work. Any compatibility update to SBOM vulnerability aggregate counts should happen after Security scans complete and must not mutate canonical SBOM events.

### Target identity
Target identity is load-bearing for dedupe, scheduling, latest-state lookup, and breach fingerprinting. The implementation should define stable canonical target keys before building scan orchestration:

- SBOM target: `sbom:<subject_type>:<subject_key_or_digest>:<format>:<payload_sha256>:<reference_d_tag>`.
- Package target: normalized lowercase `package:<ecosystem>:<name>:<version>` with empty version represented explicitly.
- PURL target: canonicalized `purl:<normalized_purl>`; version remains only in the PURL if present.
- Commit target: `commit:<repo_url_hash_or_unknown>:<commit_hash>`; if repo is unknown, the key must still be deterministic and collision-resistant for the commit hash.

Persist the canonical target key and a hash form. Use the key for duplicate suppression and operator-readable diagnostics; use the hash for indexes, `d` tags, and fingerprints where length matters.

### OSV behavior
Extend the existing OSV adapter rather than creating a parallel client. The expanded adapter should support `/v1/querybatch`, PURL queries, commit queries, `/v1/vulns/{id}` hydration, response-order preservation, chunking, retry/backoff for transient failures, and explicit non-retryable handling for invalid requests.

Normalization rules:

- Prefer PURL when present.
- If a PURL includes a version, do not also send top-level `version`.
- Fall back to `{ecosystem, name, version}` when PURL is absent.
- Skip CPE-only packages with a persisted `unsupported_coordinate` reason.
- Deduplicate identical coordinates before batch submission.
- Preserve query-result index mapping from `/v1/querybatch`.
- Hydrate unique vulnerability IDs only when policy evaluation, notification payloads, or durable finding details require it.

### Policy-scoped configuration and deployment gates
Use the existing deployment policy scope as the first-class configuration plane: global policies and environment-scoped policies already exist, and vulnerability thresholds already live as policy rules. Add an OSV Security scan rule/config surface to that model rather than creating a second policy system.

The plan does **not** make deployment policy evaluation synchronously trigger and wait for an OSV scan. Deployment gates should evaluate the latest completed Security scan for the target and its freshness/status. If no suitable Security scan exists, policy behavior should be explicit through policy settings: require a completed/fresh scan to block, warn, or pass. This preserves event-driven scan completion and avoids timeout-based deployment blocking.

The `security_osv_scan` policy surface should configure at least enablement, schedule/freshness, source types, provider selection, hydration/detail level, scan-error behavior, and notification/breach behavior. Exact JSON field names and defaults should be finalized with the Nostr/policy docs in the implementation slice.

Thresholds should continue to use existing policy rules where possible (`max_critical_vulns`, `max_high_vulns`, and `require_scan_status`). Policy evaluation should prefer latest Security scan counts when available, then fall back to existing SBOM aggregate counts only for compatibility with artifacts that have not yet been scanned by the Security service.

### Breach lifecycle and notifications
Add a breach event type such as `security.policy_breached` and wire it through the existing event publisher/dispatcher subscription list. Dispatch only when a policy breach fingerprint is new or materially changed.

Persist each breach comparison record scoped by `policy_id + target_key_hash`. Store the current fingerprint, prior fingerprint when replaced, enforcement level, violated rules, severity counts, sorted OSV IDs, first-seen time, last-seen time, resolved time, and notification status. A policy rule or enforcement change should be treated as a new/changed breach because operator actionability changed. A scan that no longer violates the policy should mark the breach resolved without sending a breach notification unless the implementation adds a separate resolved notification type and documents it.

Suggested fingerprint inputs:

```text
policy_id + target_key_hash + policy_revision_or_updated_at + enforcement + violated_rules + severity_counts + sorted_osv_ids
```

Do not notify for every individual finding, every clean scan, or unchanged recurring breaches. Existing channel filters should allow users to subscribe to `security.policy_breached` without new channel types.

### Persistence and retention
Add durable Security persistence for:

- scan runs and status transitions;
- scan targets and latest target state;
- normalized findings;
- policy breaches and breach fingerprints;
- policy-derived schedules;
- optional hydrated OSV vulnerability cache.

Default retention recommendation for the implementation slice: retain normalized findings and scan summaries indefinitely until a product retention policy is explicitly defined; cache raw hydrated OSV records for a bounded period such as 30 days; preserve withdrawn advisories by marking them withdrawn rather than deleting historical findings.

### File impact
New implementation files are expected around:

- `internal/domain/security.go` for Security run, target, finding, breach, schedule, and enum types.
- `internal/repository/security.go` and `internal/repository/pg_security.go` for persistence and due-record queries.
- `internal/db/migrations/*_security_osv*.sql` for additive tables and indexes.
- `internal/service/security_scanner.go` for scan orchestration and observable ingestion.
- `internal/service/security_scheduler.go` for due-record rescan cadence.
- `internal/controlplane/security_handlers.go` for explicit scan/rescan and read operations.
- `pstf/features/SECURITY_OSV_SBOM/*` for acceptance, tests, and verification evidence.

Existing files likely to change:

- `internal/adapters/security/osv.go` for OSV batch/PURL/commit/hydration support.
- `internal/domain/policy.go`, `internal/service/policy.go`, `internal/controlplane/policy_command_publisher.go`, and relevant control-plane registration for policy-scoped Security config.
- `internal/notifications/dispatcher.go`, `internal/events/events.go`, and notification docs/tests for `security.policy_breached`.
- `internal/repository/pg_sbom.go` and `internal/domain/sbom.go` only for compatibility aggregate updates, not canonical SBOM mutation.
- Nostr/user docs listed in the References section.

### Risks and mitigations
- OSV has no documented rate limits today, but the service must still chunk large batches, cache, and handle 429/5xx with retry/backoff. Rate-limit or transient OSV failure should mark the scan failed or degraded according to policy, but must not block SBOM import.
- Large SBOMs can exceed practical response sizes. Deduplicate, chunk, prefer HTTP/2 where available, and store raw hydrated vulnerability data in the database rather than public Nostr content.
- Relay publish failure can leave database state ahead of public observables. Track publish state per scan/finding summary and provide a retry path rather than silently marking complete.
- Duplicate notifications are likely without explicit fingerprinting. Persist fingerprints before dispatch and update notification status after dispatcher completion.
- The additive database schema should be rollback-safe: old code ignores Security rows/events; new code can fall back to SBOM aggregate counts when Security state is absent.

## Work Items

### Item 1 — Protocol, PSTF, and Beads foundation
**Goal:** Freeze the Security contract and establish traceability before implementation begins.

**Done when:** `pstf/features/SECURITY_OSV_SBOM/` contains feature spec, acceptance criteria, test matrix, and verification report placeholders; Beads issues exist for implementation slices; docs specify Security ContextVM methods, scoped subscription filters, EOSE/CLOSED/AUTH expectations, OK verification, schema tags, status/audit conventions, and examples using existing canonical kinds unless the event guide justifies another kind.

**Key files:** `pstf/features/SECURITY_OSV_SBOM/*`, `docs/nostr-event-implementation-guide.md`, `docs/control-planes.md`, `docs/event-spec.md`, `docs/nostr-commands.md`, `docs/user-guide/nostr-integration.md`.

**Dependencies:** None.

**Size:** M.

### Item 2 — Security domain model and persistence
**Goal:** Persist scan runs, targets, findings, schedules, latest target state, breach fingerprints, and publication state.

**Done when:** Domain and repository tests cover create/update/list, terminal state transitions, target-key uniqueness for SBOM/package/PURL/commit, due-schedule selection, finding uniqueness, breach fingerprint lifecycle, raw OSV cache retention, and observable publish-state retry queries.

**Key files:** `internal/domain/security.go`, `internal/repository/security.go`, `internal/repository/pg_security.go`, `internal/db/migrations/*_security_osv*.sql`, `internal/repository/interfaces.go`.

**Dependencies:** Item 1.

**Size:** L.

### Item 3 — OSV adapter expansion
**Goal:** Support production SBOM and non-SBOM OSV lookup paths.

**Done when:** Unit tests cover batch query ordering, PURL with and without version, invalid version/PURL combinations, commit lookup, vulnerability hydration, severity normalization, 400/404/429/5xx behavior, cache keys, chunking, and transient retry/backoff behavior.

**Key files:** `internal/adapters/security/osv.go`, new tests under `internal/adapters/security/`.

**Dependencies:** Item 1.

**Size:** L.

### Item 4 — Security scanner and target normalization
**Goal:** Implement the scan lifecycle from normalized target through durable findings and published observables.

**Done when:** Deterministic tests prove target normalization, status progression, idempotency locks, per-target dedupe, payload hash verification, OSV calls, finding persistence, audit publication, OK handling, publication retry state, failure states, cancellation behavior, and no timeout-based completion logic.

**Key files:** `internal/service/security_scanner.go`, `internal/domain/security.go`, `internal/adapters/sbom/parser.go`, `internal/domain/sbom.go`, `internal/controlplane/backup_run_responder.go` as a long-running responder pattern, `internal/nostrutil/nostrutil.go`.

**Dependencies:** Items 2 and 3.

**Size:** XL.

### Item 5 — SBOM observable ingestion and app wiring
**Goal:** Trigger Security scans from canonical SBOM create/import/update facts without blocking SBOM production.

**Done when:** The scanner is wired into application startup/shutdown; backfills historical `30078`/`30004` facts until EOSE; keeps realtime subscriptions open when enabled; re-subscribes after reconnect; handles CLOSED/AUTH; dedupes SBOM references by target key; verifies Blossom payload hash before scanning; and never polls for event delivery.

**Key files:** `internal/service/security_scanner.go`, application wiring in `internal/app/app.go` or the current composition root, relay subscription code near existing long-running services, `internal/adapters/sbom/index.go`, `internal/adapters/sbom/storage.go`.

**Dependencies:** Item 4.

**Size:** L.

### Item 6 — ContextVM Security handlers and read surfaces
**Goal:** Expose explicit SBOM, package, PURL, commit, and rescan requests through Bahia’s Nostr-native control plane, plus read surfaces for schedules/findings.

**Done when:** Authorized encrypted ContextVM requests acknowledge accepted scan intent; invalid payloads produce correlated errors; terminal progress is visible only through Security observables; handlers validate idempotency keys and target shapes; read paths expose persisted findings/schedules without implying command completion; implementation explicitly documents whether REST compatibility is absent or added.

**Key files:** `internal/controlplane/security_handlers.go`, `internal/controlplane/sbom_handlers.go`, `internal/controlplane/reactor.go`, `docs/nostr-commands.md`, `docs/user-guide/mcp-tools.md`, `docs/user-guide/cli-reference.md` if applicable.

**Dependencies:** Item 4.

**Size:** M.

### Item 7 — Policy-scoped Security configuration and gates
**Goal:** Attach OSV scan settings and thresholds to existing policy scope without synchronous scan-and-wait behavior.

**Done when:** Policy create/update/evaluate paths persist and validate the Security scan policy surface; schedule derivation reads global and environment-scoped policies; deployment gates evaluate latest completed/fresh Security results according to policy settings; existing vulnerability gates prefer Security counts and fall back to SBOM counts; docs/tests cover block/warn/pass behavior when no scan exists, scan is stale, scan failed, and scan breaches thresholds.

**Key files:** `internal/domain/policy.go`, `internal/service/policy.go`, `internal/controlplane/policy_command_publisher.go`, `internal/controlplane/reactor.go`, `docs/user-guide/features/policies.md`.

**Dependencies:** Items 2 and 4.

**Size:** L.

### Item 8 — Scheduled rescans
**Goal:** Add repository-backed periodic rescans for enabled policy-scoped Security settings.

**Done when:** Scheduler runs once on startup and then on ticker cycles; selects due schedules from repository state; skips disabled policies and active duplicate scans; records next due time from policy config; uses timers only for cadence, not event delivery or completion; tests cover due selection and duplicate suppression.

**Key files:** `internal/service/security_scheduler.go`, `internal/repository/pg_security.go`, `internal/reconcile/reconciler.go`, `internal/repository/pg_state.go`.

**Dependencies:** Item 7.

**Size:** M.

### Item 9 — Policy-breach evaluation, notifications, and compatibility aggregates
**Goal:** Notify only for actionable policy breaches while preserving existing vulnerability count consumers.

**Done when:** Breach evaluation produces deterministic fingerprints; new or changed breach fingerprints publish `4903` audit facts and dispatch `security.policy_breached`; notification logs are created through the existing dispatcher; unchanged recurring breaches do not dispatch again; latest successful Security scan counts update compatibility aggregate projections for `SBOMManifest`/`ArtifactSBOM` without mutating canonical SBOM events; channel filters can target the new event type.

**Key files:** `internal/service/security_scanner.go`, `internal/notifications/dispatcher.go`, `internal/events/events.go`, `internal/domain/notification.go`, `internal/domain/sbom.go`, `internal/repository/pg_sbom.go`, `internal/repository/pg_security.go`, `docs/user-guide/features/notifications.md`, `docs/user-guide/features/artifacts.md`.

**Dependencies:** Item 7.

**Size:** L.

### Item 10 — Verification, documentation, and release evidence
**Goal:** Prove the intended behavior and leave the implementation handoff-ready.

**Done when:** PSTF verification records passing Go, web, and event-driven integration tests; docs explain SBOM-triggered scans, explicit package/PURL/commit scans, schedules, policy thresholds, breach notifications, Nostr observables, failure handling, and troubleshooting; Beads defects exist for any remaining gaps; migration and rollback notes are documented; no touched production path contains fake, stubbed, hardcoded, or placeholder behavior.

**Key files:** `pstf/features/SECURITY_OSV_SBOM/*`, `pstf/features/SBOM_WORKFLOW_E2E/*`, `pstf/features/ENCRYPTED_CONTROL_PLANE_NOTIFICATIONS/*`, `docs/user-guide/features/artifacts.md`, `docs/user-guide/features/policies.md`, `docs/user-guide/features/notifications.md`, `docs/user-guide/nostr-integration.md`, relevant test packages.

**Dependencies:** All prior items.

**Size:** L.

## Open Questions
- Final Security schema details and exact tags should be settled in Item 1 against `docs/nostr-event-implementation-guide.md` before implementation writes event builders.
- Retention defaults above are a recommended starting point; if product/legal requirements require shorter or longer raw OSV retention, record that HITL decision in PSTF and Beads before implementation.
- Expected steady-state scan volume is not known. The plan assumes moderate volume with chunking, indexes, and bounded caches; if operators expect high-frequency scans across many environments, Item 2 should add partitioning/retention design before implementation.

## References
- `docs/nostr-event-implementation-guide.md`
- `docs/control-planes.md`
- `docs/nostr-commands.md`
- `docs/event-spec.md`
- `docs/user-guide/features/policies.md`
- `docs/user-guide/features/notifications.md`
- `docs/user-guide/features/artifacts.md`
- `docs/user-guide/nostr-integration.md`
- `pstf/features/SBOM_WORKFLOW_E2E/`
- `pstf/features/ENCRYPTED_CONTROL_PLANE_NOTIFICATIONS/`
- OSV API overview: https://google.github.io/osv.dev/api/
- OSV query API: https://google.github.io/osv.dev/post-v1-query/
- OSV batch API: https://google.github.io/osv.dev/post-v1-querybatch/
- OSV vulnerability hydration: https://google.github.io/osv.dev/get-v1-vulns/
- OSV schema: https://ossf.github.io/osv-schema/
