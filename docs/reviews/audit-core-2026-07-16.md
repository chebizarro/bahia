# Bahia Core Production-Readiness Audit — 2026-07-16

## Context / Scope

Read-only production-readiness audit of the `bahia` Go service, scoped to the
core control-plane and platform packages:

`internal/api`, `internal/auth`, `internal/app`, `internal/service`,
`internal/controlplane`, `internal/db` (incl. migrations), `internal/config`,
`internal/events`, `internal/pipeline`, `internal/reconcile`,
`internal/workflow`, `internal/notifications`, `internal/repository`,
`internal/domain`, `internal/rollout`, `internal/mcp`,
`internal/nostrmigration`, `internal/soulfactory`, `internal/fipsbridge`,
`internal/relaysidecar`.

Explicitly **out of scope** (covered by other reviewers): `internal/adapters`,
`internal/backends`, `cmd/`, `web/`.

This is an inherited codebase assembled over many LLM sessions, so the audit
specifically hunted for *fake completion* (handlers that ack but don't
persist/publish), missing auth/tenant checks, swallowed errors, hardcoded
config, partial migrations, and idempotency/rollback gaps. Every finding below
was confirmed by reading the cited code directly (line numbers verified against
the working tree at audit time). Findings are ordered by severity.

### Method
Subsystems were swept in parallel, then the load-bearing findings were
re-read and verified by hand. Where a claim depends on runtime wiring or
deployment exposure I have flagged it explicitly rather than overstating it.

### Severity key
- **P0** — exploitable now / guaranteed data-loss or cross-tenant compromise; blocks production.
- **P1** — serious security or correctness defect; must fix before relying on the feature in production.
- **P2** — real reliability/security gap that will bite under failure or concurrency.
- **P3** — debt / hardening / observability weakness.

---

## Summary table

| # | Severity | Category | Title |
|---|----------|----------|-------|
| 1 | P0 | security / missing-implementation | MCP tool dispatch performs no authorization on any tool (deploy/secrets/delete) |
| 2 | P0 | security | MCP secret update/delete mutate by UUID with no ownership/tenant check |
| 3 | P1 | security | `/state*` endpoints expose registry-wide state with no org/tenant scoping |
| 4 | P1 | security | Deployment log endpoints leak cross-tenant logs (no org resolver) |
| 5 | P1 | security | Notification channel CRUD + logs are global, unauthenticated beyond tier gate |
| 6 | P1 | security | SBOM read/ingest endpoints omit the artifact org resolver |
| 7 | P1 | unsafe-hardcoding | Default DB password `bahia` + `sslmode=disable`, never rejected by `validate()` |
| 8 | P1 | incomplete-migration | Tenant `org_id` backfill (mig 21/22) never runs → services/environments orphaned |
| 9 | P1 | fake-completion | Rollout traffic-shift and blue/green switch are logging no-ops reported as success |
| 10 | P1 | fake-completion | Soulfactory provisioning marks steps "complete" despite failed sub-actions |
| 11 | P1 | reliability | Rollout auto-rollback swallows every error and never restores prior artifact |
| 12 | P2 | security | NIP-98: replay protection is process-local; body not bound (no payload check) |
| 13 | P2 | security | Continuity/heartbeat *definition* events accepted with no operator authorization |
| 14 | P2 | fake-completion | Control-plane responders return `nil` (success) without publishing anything |
| 15 | P2 | reliability | Destructive backup restore/snapshot can re-run after a checkpoint persistence failure |
| 16 | P2 | reliability | Notification dispatch swallows all send/persist errors; zero-relay DM = "sent" |
| 17 | P2 | incomplete-migration | nostrmigration: single fixed batch, no pagination, content not translated to canonical schema |
| 18 | P2 | reliability | Repository stale/optimistic-concurrency writes report success on no-op |
| 19 | P2 | reliability | Cross-tenant / non-atomic writes in repository layer |
| 20 | P2 | reliability | LLM promotion has a check-then-act race with no route lock |
| 21 | P2 | fake-completion | Coordinators return `nil` when mandatory deps are missing (silent disable) |
| 22 | P3 | unsafe-hardcoding | Hardcoded OCI service-account hash, anonymous-pull CIDR, and provisioning relay |
| 23 | P3 | observability | Event bus handlers cannot return errors; rollout health-gate ignores observer errors |
| 24 | P3 | debt | Payment metadata marshal error silently coerced to `{}` |

---

## P0 findings

### 1. MCP tool dispatch performs no authorization on any tool
- **Severity:** P0 · **Category:** security / missing-implementation
- **Files:** `internal/mcp/server.go:1757-1789` (`InvokeTool` → `CallTool`), dispatch switch continues for hundreds of lines.
- **Evidence:**
  ```go
  func (s *Server) InvokeTool(ctx context.Context, name string, arguments map[string]interface{}) (*ToolResult, error) {
      return s.CallTool(ctx, name, arguments)
  }

  func (s *Server) CallTool(ctx context.Context, name string, arguments map[string]interface{}) (*ToolResult, error) {
      s.logger.Info("tool call", zap.String("tool", name))
      if isBackupToolName(name) {
          return s.handleBackupTool(ctx, name, arguments)
      }
      switch name {
      case "bahia_create_service":
          return s.handleCreateService(ctx, arguments)
      ...
  ```
  `CallTool` extracts **no** authenticated principal and performs **no**
  authorization decision before dispatching. It immediately routes to handlers
  including secret management, service/environment mutation, and deletion.
  (Note: some *registry-mutating* tools were deliberately redirected to signed
  ContextVM commands via `signerFirstMCPMutationUnavailable`, but read tools,
  secret tools, and backup tools still execute directly here.)
- **Why not production-ready:** The reactor/HTTP layers gate every command on
  `isAuthorized(...)` / `coreRBAC(...)`, but this parallel MCP entrypoint has no
  equivalent gate. Any caller that can reach the MCP server can enumerate and
  mutate platform state and secrets. Whether this is remotely reachable depends
  on how the server is bound, but the code offers no defense in depth.
- **Recommended fix:** Require an authenticated principal in the MCP transport,
  thread it into `CallTool`, and enforce per-tool RBAC (mirror the reactor's
  `isAuthorized`/operator-scope model). Deny by default for mutating tools.

### 2. MCP secret update/delete mutate by UUID with no ownership check
- **Severity:** P0 · **Category:** security
- **Files:** `internal/mcp/server.go:3461-3499` (`handleUpdateSecret`), `internal/mcp/server.go:3516-3535` (`handleDeleteSecret`).
- **Evidence:**
  ```go
  secretID, err := uuid.Parse(secretIDStr)
  ...
  existing, err := s.secretsRepo.GetByID(ctx, secretID)
  ...
  existing.EncryptedValue = encryptedValue
  existing.Version++
  if err := s.secretsRepo.Update(ctx, existing); err != nil { ... }
  ```
  ```go
  secretID, err := uuid.Parse(secretIDStr)
  if err != nil { return errorResult(...) }
  if err := s.secretsRepo.Delete(ctx, secretID); err != nil { ... }
  ```
  Neither handler verifies that the caller owns (or is a member of the org that
  owns) the secret's service. Possession of a secret UUID is sufficient to
  overwrite or delete it.
- **Why not production-ready:** Combined with finding #1 (no auth on the
  transport), this is arbitrary cross-tenant secret replacement/destruction. Even
  with transport auth added, the handler still needs a service/org ownership
  check.
- **Recommended fix:** Load the secret, resolve its owning service/org, and
  authorize the principal against that org with a secrets-write permission before
  mutating. `handleDeleteSecret` must load-then-authorize rather than deleting
  directly by ID.

---

## P1 findings

### 3. `/state*` endpoints expose registry-wide state with no org/tenant scoping
- **Severity:** P1 · **Category:** security (tenant isolation)
- **Files:** `internal/api/router/router.go:243-246`; `internal/api/handlers/state.go:17-72`.
- **Evidence:** Router registers state reads with only a tier gate:
  ```go
  r.With(tier2Gate).Get("/state", stateH.ListAll)
  r.With(tier2Gate).Get("/state/drifted", stateH.ListDrifted)
  r.With(tier2Gate).Get("/environments/{envId}/state", stateH.ListByEnvironment)
  r.With(tier2Gate).Get("/services/{serviceId}/environments/{envId}/state", stateH.GetState)
  ```
  The handler queries the registry directly with no membership/org filter:
  ```go
  func (h *StateHandler) ListAll(w http.ResponseWriter, r *http.Request) {
      states, err := h.registry.ListAllStates(r.Context())
      ...
      writeData(w, http.StatusOK, states)
  }
  func (h *StateHandler) GetState(...) {
      state, err := h.registry.GetEnvironmentServiceState(r.Context(), serviceID, envID)
      ...
  }
  ```
  Contrast: nearly every other resource route wraps `coreRBAC(deps,
  authMiddleware, <resource>OrgResolver(...), true)` (see `router.go:212-232,
  313-340`). State routes omit it.
- **Why not production-ready:** Any authenticated tier-2 principal can read all
  environment/service desired-and-observed state across every tenant, and address
  arbitrary `serviceId/envId`. This defeats tenant isolation for the platform's
  most sensitive read model.
- **Recommended fix:** Apply `coreRBAC` with `serviceEnvOrgResolver` on the
  scoped routes, and for `ListAll`/`ListDrifted` filter by the caller's org (add
  an org-scoped registry query) instead of returning global state.

### 4. Deployment log endpoints leak cross-tenant logs
- **Severity:** P1 · **Category:** security (tenant isolation)
- **Files:** `internal/api/router/router.go:231-240` (`.../runs/{id}/logs`, `.../environments/{envId}/logs`); `internal/api/handlers/logs.go:66-121, 124-222`.
- **Evidence:** Routes use `tier2Gate` only; `GetRunLogs` loads the run purely by
  ID and never resolves/checks org:
  ```go
  runID, err := uuid.Parse(chi.URLParam(r, "id"))
  run, err := h.runs.GetByID(r.Context(), runID)
  ...
  logs, err := h.logService.FetchRunLogs(r.Context(), run)
  writeData(w, http.StatusOK, logs)
  ```
  Sibling routes such as `GET /deployments/runs/{id}` use
  `runOrgResolver(registry, deps.Services, "id")` (`router.go:231`). The log
  routes do not.
- **Why not production-ready:** Any tier-2 user can read another org's run logs
  (which routinely contain secrets, tokens, and internal hostnames) or stream a
  live environment's logs by guessing/enumerating UUIDs.
- **Recommended fix:** Wrap both log routes in `coreRBAC` with `runOrgResolver`
  and `serviceEnvOrgResolver` respectively.

### 5. Notification channel CRUD + logs are global with no org/RBAC
- **Severity:** P1 · **Category:** security (tenant isolation)
- **Files:** `internal/api/router/router.go:351-355, 407-412`; `internal/api/handlers/notifications.go:31-197`.
- **Evidence:** Handlers such as `DeleteChannel`, `CreateChannel`, `ListChannels`,
  `ListLogs`, `TestChannel` operate on globally-stored channels keyed only by ID:
  ```go
  func (h *NotificationHandler) DeleteChannel(w http.ResponseWriter, r *http.Request) {
      id, err := uuid.Parse(chi.URLParam(r, "id"))
      if err := h.repo.DeleteChannel(r.Context(), id); err != nil { ... }
      writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
  }
  ```
  No membership/permission/ownership check anywhere in the file, and the routes
  carry only `tier2Gate`.
- **Why not production-ready:** Any tier-2 user can enumerate, modify, delete, or
  test any tenant's notification channels (which include webhook URLs and DM
  targets), and read the global notification log.
- **Recommended fix:** Scope notification channels to an org, add an org resolver
  + permission check on all channel routes, and filter `ListLogs`/`ListChannels`
  by the caller's org.

### 6. SBOM endpoints omit the artifact org resolver
- **Severity:** P1 · **Category:** security (tenant isolation)
- **Files:** `internal/api/router/router.go:324-326, 392`; `internal/api/handlers/sbom.go:42-182`.
- **Evidence:** `GET/POST /artifacts/{id}/sbom` are registered with only a tier
  gate and validate artifact existence but never call membership/tenant checks,
  whereas the adjacent signature routes (`router.go:317, 410`) use
  `signatureOrgResolver`/`artifactOrgResolver`.
- **Why not production-ready:** Any tier-3 user can read or ingest an SBOM for
  another org's artifact, leaking dependency inventories and allowing SBOM
  poisoning.
- **Recommended fix:** Wrap both SBOM routes in `coreRBAC` with
  `artifactOrgResolver(deps.Artifacts, deps.Services, "id")`.

### 7. Insecure default DB credentials + disabled TLS, not rejected by validation
- **Severity:** P1 · **Category:** unsafe-hardcoding / security
- **Files:** `internal/config/config.go:722-731` (defaults), `:1040-1080` (`validate()`), `:715-716` + `:845-846` (host / auth default).
- **Evidence:**
  ```go
  DB: DBConfig{
      Host: "localhost", Port: 5432,
      User: "bahia", Password: "bahia", Name: "bahia",
      SSLMode: "disable",
      ...
  },
  Server: ServerConfig{ Host: "0.0.0.0", Port: 8080, ... },
  Auth: AuthConfig{ Enabled: false },
  ```
  `validate()` only requires `auth.enabled=true` when *adoption*, *direct runtime
  actions*, or *LLM operational REST* are enabled (`config.go:1050, 1075, 1373`).
  There is no check that rejects the default `bahia`/`bahia` credential, requires
  TLS, or forces auth for the base API server.
- **Why not production-ready:** A deployment that leans on defaults binds an
  unauthenticated API on `0.0.0.0` and talks to Postgres with a guessable
  password over an unencrypted connection. Nothing in startup prevents shipping
  this.
- **Recommended fix:** In `validate()`, reject `Password == "bahia"` and
  `SSLMode == "disable"` (and ideally `Auth.Enabled == false`) unless an explicit
  `allow_insecure_dev=true` flag is set. Fail closed by default.

### 8. Tenant `org_id` backfill never runs (migration 21/22 ordering)
- **Severity:** P1 · **Category:** incomplete-migration
- **Files:** `internal/db/migrations/000021_core_resource_org_ownership.up.sql:10-40`; `internal/db/migrations/000022_tenant_schema.up.sql:1-8, 35-47`.
- **Evidence:** Migration 21 adds `org_id` and attempts a single-org backfill, but
  guards it on the `organizations` table already existing:
  ```sql
  DO $$
  BEGIN
      IF to_regclass('public.organizations') IS NOT NULL THEN
          ... UPDATE services SET org_id = (SELECT id FROM single_org)
              WHERE org_id IS NULL AND (SELECT n FROM org_count) = 1 ...
      END IF;
  END $$;
  ```
  The `organizations` table is not created until migration **22**
  (`000022_tenant_schema.up.sql:1`). On a normal sequential apply, `to_regclass`
  is NULL at migration 21, so the backfill is skipped entirely. Migration 22 then
  only *nulls out* dangling `org_id`s — it never repeats the backfill:
  ```sql
  UPDATE services s SET org_id = NULL
  WHERE org_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM organizations o WHERE o.id = s.org_id);
  ```
- **Why not production-ready:** After upgrade, every pre-existing service and
  environment has `org_id IS NULL`. Because tenant RBAC resolves access via
  `org_id`, those resources become invisible/inaccessible through the org-scoped
  API until manually repaired in SQL. The migration's own comment claims a
  single-org backfill that the ordering makes impossible.
- **Recommended fix:** Move the backfill into migration 22 (after
  `organizations` is created), or split: create `organizations` first, then
  backfill. Add a data check/repair for existing deployments.

### 9. Rollout traffic-shift and blue/green switch are no-ops reported as success
- **Severity:** P1 · **Category:** fake-completion
- **Files:** `internal/rollout/executor.go:208-228`.
- **Evidence:**
  ```go
  case domain.StepActionShiftTraffic:
      weight := 50
      if w, ok := step.Config["weight"].(float64); ok { weight = int(w) }
      e.logger.Info("traffic shift requested", zap.String("service", serviceName), zap.Int("weight_percent", weight))
      // In a production system, this would configure a load balancer or service mesh.
      return nil
  case domain.StepActionSwitch:
      e.logger.Info("blue-green switch", ...)
      return nil
  ```
  Both return `nil` (step passes) after only logging. The step is then marked
  passed and the plan can emit `rollout.completed`.
- **Why not production-ready:** Canary and blue/green rollouts advance and report
  "completed" while **no traffic is ever shifted**. Operators believe a canary is
  live at N% or that traffic cut over to green when it did not — a textbook fake
  completion in a core deploy flow.
- **Recommended fix:** Implement the traffic operation against the actual
  runtime/LB/mesh, verify the applied weight, and return an error (failing the
  step) when the capability is unavailable rather than silently succeeding.

### 10. Soulfactory provisioning marks steps "complete" despite failed sub-actions
- **Severity:** P1 · **Category:** fake-completion
- **Files:** `internal/soulfactory/provisioner_full.go:288-295` (memory), `:333-337` (NIP-05 relay), `:335-421` (deploy step).
- **Evidence:**
  ```go
  if err := p.agentMemory.SeedMemory(...); err != nil {
      logger.Warn("memory seeding failed", ...)
  }
  p.recordStep(run, domain.StepMemory, domain.StepStatusComplete, ...)
  ```
  The final deploy step similarly downgrades NIP-05 registration, snapshot
  upload, Bahia service registration, and initial deployment failures to
  `logger.Warn(...)`, then unconditionally records the aggregate step as
  `StepStatusComplete` and activates the soul.
- **Why not production-ready:** A provisioning run reports success and the agent
  is marked active even when memory seeding, identity registration, and the
  initial deployment all failed. Downstream operators/consumers get a "ready"
  signal for a half-provisioned agent.
- **Recommended fix:** Treat these as step failures (or a partial/degraded
  status), propagate the error, and only mark `Complete` when the sub-actions
  succeeded.

### 11. Rollout auto-rollback swallows every error and never restores prior artifact
- **Severity:** P1 · **Category:** reliability
- **Files:** `internal/rollout/executor.go:247-265` (`autoRollback`), `:242-246` (`StepActionRollback`); related `internal/reconcile/remediation.go:338-348`.
- **Evidence:**
  ```go
  func (e *Executor) autoRollback(ctx context.Context, plan *domain.RolloutPlan, serviceName string) {
      _ = e.rt.Undeploy(ctx, serviceName+"-canary")
      _ = e.rt.Undeploy(ctx, serviceName+"-green")
      plan.Status = domain.RolloutStatusRolledBack
      plan.CompletedAt = &completed
      _ = e.repo.UpdatePlan(ctx, plan)
      e.publisher.Publish(ctx, events.Event{Type: "rollout.rolled_back", ...})
  }
  ```
  Every operational and persistence error is discarded via `_ =`, and the routine
  only removes canary/green slots — it never redeploys the last-known-good primary
  artifact. `remediation.go:338-348` has the same "undeploy and let the original
  run" comment/behavior.
- **Why not production-ready:** `rollout.rolled_back` is emitted even if the
  undeploys failed (stale canary/green left running) or the status write failed
  (DB says still in-progress). If the primary was already replaced, there is no
  restoration path — the "rollback" leaves the service in the broken new state.
- **Recommended fix:** Check each `Undeploy`/`UpdatePlan` error, redeploy the
  previous primary artifact, verify health, and only publish `rolled_back` after
  the prior state is restored; surface failures as a `rollback_failed` terminal
  state.

---

## P2 findings

### 12. NIP-98 replay protection is process-local; request body is unbound
- **Severity:** P2 · **Category:** security
- **Files:** `internal/auth/nip98.go:30-51, 92-131`.
- **Evidence:** Replay state is an in-memory map with a background purge:
  ```go
  // In-memory replay protection. Keyed by event ID → expiry time.
  mu   sync.Mutex
  seen map[string]time.Time
  ```
  Validation checks kind, signature, `created_at` skew, `u`, and `method`, but
  never verifies NIP-98's optional `payload` tag against a hash of the HTTP body.
- **Why not production-ready:** (a) With multiple replicas or after a restart, the
  same signed token is accepted once per replica and again post-restart within the
  skew window — replay protection is not global. (b) Because the body is not bound
  to the event, a captured token for a body-bearing request can be replayed with a
  modified body (subject to reaching the validator before the original consumes
  the ID).
- **Recommended fix:** Back replay state with a shared store (DB/Redis) keyed by
  event ID with TTL; verify the `payload` tag equals the SHA-256 of the request
  body for methods that carry one.

### 13. Continuity/heartbeat *definition* events accepted with no operator authorization
- **Severity:** P2 · **Category:** security
- **Files:** `internal/controlplane/continuity_definition_handlers.go:11-122`; dispatch in `internal/controlplane/reactor.go:585-614`.
- **Evidence:** Every reactor *command* handler gates on
  `if !r.isAuthorized(event.PubKey.Hex()) { ... }` (confirmed across
  `dns_handlers.go`, `ml_handlers.go`, `package_handlers.go:288`,
  `backup_handlers.go`, `continuity_command_handlers.go:12,29`,
  `operator_actions.go`, and `reactor.go` ×13). But
  `continuity_definition_handlers.go` handlers (profile, failover policy, standby
  node, replication policy, recovery workflow, heartbeat) call **no**
  authorization:
  ```go
  func (r *Reactor) handleContinuityProfileDefinition(ctx context.Context, event *gonostr.Event) {
      profile, err := nostradapter.DecodeContinuityProfileEvent(event)
      if err != nil { ... return }
      r.eventBus.Publish(ctx, events.Event{ Type: events.EventContinuityProfileObserved, ... })
  }
  ```
- **Why not production-ready:** Signature validation proves event integrity, not
  that the author is an authorized operator. Any validly-signed event that passes
  routing can inject continuity/failover/standby/heartbeat observations into the
  internal event bus, which feed failover and reconciliation decisions.
- **Recommended fix:** Add `isAuthorized`/operator-scope checks to the definition
  and heartbeat handlers (or an explicit allowlist of worker pubkeys for
  heartbeats), consistent with the command handlers.

### 14. Control-plane responders return `nil` (success) without publishing
- **Severity:** P2 · **Category:** fake-completion
- **Files:** `internal/controlplane/ml_responder.go:37-42, 70-75`; `llm_responder.go:52-59`; `tool_responder.go:50-53, 78-81`; `backup_restore_responder.go:38-40, 64-66, 98-100`; `backup_run_responder.go:44-46, 67-69`; `backup_retention_responder.go:38-40, 63-65`.
- **Evidence:** ML status is an explicit no-op, and terminal publishers early-return
  `nil` when correlation metadata is missing:
  ```go
  func (r *MLResponder) PublishStatus(context.Context, *domain.MLDeploymentIntent, *domain.MLDeploymentRun, string, string) error {
      return nil
  }
  ...
  func (r *MLResponder) publishRecipe(...) error {
      if r == nil || r.pool == nil || run == nil { return nil }
      requestEventID, requestPubkey, _ := mlRecipeNostrCorrelation(run)
      if requestEventID == "" || requestPubkey == "" { return nil }
      ...
  ```
- **Why not production-ready:** Callers treat `nil` as "response delivered." When
  the relay pool is unconfigured or correlation data is absent, the terminal
  Nostr reply is silently dropped while the operation reports success — requesters
  wait forever or never learn the outcome. The ML `PublishStatus` no-op is
  acceptable *only if* the 3198x read-model path is guaranteed; that coupling is
  undocumented and fragile.
- **Recommended fix:** Distinguish "not applicable" from "could not publish."
  Return a sentinel/error when required correlation or pool is missing so callers
  can log/alert, and assert the read-model alternative exists for the ML no-op.

### 15. Destructive backup restore/snapshot can re-run after a checkpoint failure
- **Severity:** P2 · **Category:** reliability (idempotency)
- **Files:** `internal/service/backup_restore_coordinator.go:194-210`; `internal/service/backup_run_coordinator.go:238-258`.
- **Evidence:**
  ```go
  result, restoreErr = restoreBackend.Restore(ctx, BackupRestoreRequest{...})
  ...
  completed, err := c.registry.CompleteBackupRestore(ctx, restore.ID, result, nil)
  if err != nil {
      return err   // restore already executed; run left non-terminal
  }
  ```
  The destructive restore (and, in the run coordinator, `CreateSnapshot`) executes
  *before* its terminal state is durably recorded. If the completion write fails,
  the run stays non-terminal with no operation token/checkpoint.
- **Why not production-ready:** Lease recovery or a retry re-enters the coordinator
  and can execute the restore/snapshot a second time. For a restore this can
  overwrite data twice; there is no rollback or idempotency key protecting the
  side effect.
- **Recommended fix:** Persist an "executing/executed" checkpoint (with an
  idempotency token) *before* invoking the backend, and on recovery detect the
  token and skip re-execution, reconciling only the terminal state.

### 16. Notification dispatch swallows send/persist errors; zero-relay DM = "sent"
- **Severity:** P2 · **Category:** reliability
- **Files:** `internal/notifications/dispatcher.go:71-90, 119-183`; `internal/notifications/nostr_dm.go:74-86`; `internal/api/handlers/notifications.go:166-187` (`TestChannel`).
- **Evidence:**
  ```go
  // dispatcher
  _ = d.sendToChannel(ctx, &ch, eventType, payload)   // error dropped; Dispatch() has no error return
  _ = d.repo.UpdateLog(ctx, logEntry)                 // audit/retry state write dropped
  ```
  ```go
  // nostr_dm
  published, err := s.relayPool.Publish(ctx, ev)
  if err != nil { ... }
  // no check for published == 0
  return nil
  ```
  `TestChannel` calls `Dispatch(...)` (which matches on event filters, not the
  target channel) and unconditionally returns `{"status":"test sent"}`.
- **Why not production-ready:** Failed notifications are lost with no signal;
  audit/retry log writes can fail silently (causing lost failures or duplicate
  resends); a DM accepted by zero relays is recorded as sent; and the test
  endpoint reports success even when the selected channel receives nothing.
- **Recommended fix:** Give `Dispatch` an error return, check `sendToChannel` and
  `UpdateLog` results, treat `published == 0` as failure, and make `TestChannel`
  use a targeted `DispatchToChannel(...) error` and surface the result.

### 17. nostrmigration: single fixed batch, no pagination, content not translated
- **Severity:** P2 · **Category:** incomplete-migration
- **Files:** `internal/nostrmigration/runner.go:69-77, 111-121, 124-129, 177-244`.
- **Evidence:** Local migration reads one fixed batch and never loops:
  ```go
  records, err := r.repo.ListByKinds(ctx, LegacyKinds(), r.config.LocalBatchLimit) // default 1000
  for i := range records { ... }   // no pagination/cursor
  ```
  Relay backfill is likewise capped at one subscription window (`BackfillLimit`,
  default 500). And `BuildCanonicalEvent` wraps the legacy payload rather than
  translating it to the canonical schema named by `Disposition.Schema`:
  ```go
  payload := map[string]any{
      "migration": "bahia-nostr-native-v1",
      "legacy_event": map[string]any{ ..., "content": rec.Content, ... },
  }
  // method path: params.content = rec.Content (raw legacy content, untranslated)
  ```
- **Why not production-ready:** A store with >1000 legacy local events (or >500
  relay events) migrates only one batch per process start, and repeatedly-returned
  already-migrated records can starve later ones. The "canonical" output embeds
  raw legacy content, so new consumers expecting canonical state/status/ContextVM
  payloads may reject/misread migrated events while legacy consumers still read the
  original — a concrete old/new path disagreement. Idempotency
  (`FindByTag("migrated-from", ...)` at `:181`) also can't detect malformed prior
  output, so schema fixes won't regenerate.
- **Recommended fix:** Paginate with a durable cursor until drained; translate
  content into the disposition's canonical schema; include a migration/schema
  version in the idempotency check so corrected runs can supersede bad output.

### 18. Repository stale / optimistic-concurrency writes report success on no-op
- **Severity:** P2 · **Category:** reliability
- **Files:** `internal/repository/pg_worker.go:100-145`; `internal/repository/pg_secret.go:138-154`.
- **Evidence:** Worker advertisement upsert guards on a timestamp but discards the
  command tag:
  ```sql
  ... ON CONFLICT (...) DO UPDATE SET ...
  WHERE EXCLUDED.last_advertisement_at >= workers.last_advertisement_at
  ```
  ```go
  _, err = r.pool.Exec(ctx, ...)   // RowsAffected() ignored
  return nil
  ```
  Secret update increments version with no expected-version predicate and ignores
  the command tag:
  ```go
  // UPDATE ... SET ..., version = version + 1 WHERE id = $3
  _, err := r.pool.Exec(ctx, ...)  // no version check; RowsAffected ignored
  ```
- **Why not production-ready:** A stale worker advertisement that loses the
  timestamp guard affects zero rows but returns success — the caller can't tell an
  applied update from a rejected one. Concurrent secret editors silently clobber
  each other (lost update), and updating a nonexistent secret returns `nil`
  without writing history.
- **Recommended fix:** Inspect `CommandTag.RowsAffected()`; return a
  stale/conflict error when zero. For secrets, add optimistic concurrency
  (`WHERE id=$ AND version=$expected`) and fail on mismatch.

### 19. Cross-tenant / non-atomic writes in the repository layer
- **Severity:** P2 · **Category:** reliability / security
- **Files:** `internal/repository/pg_adopted_runtime_identity.go:30-80`; `internal/repository/pg_hiveci.go:32-92`; `internal/repository/pg_sbom.go:128-149`; `internal/repository/pg_tenant.go:312-316`.
- **Evidence:**
  - Adopted runtime identity upserts on `ON CONFLICT (fingerprint)` and copies
    `org_id = EXCLUDED.org_id`, so one org can overwrite another's row for a
    matching fingerprint; `FindByFingerprints` has no `org_id` predicate. The
    batch upsert loops per-row (`for i := range identities { r.pool.Exec(...) }`)
    with no transaction, so a mid-batch failure leaves a partial apply.
  - `pg_hiveci.go` writes a run/result projection as two separate `Exec` calls
    (INSERT then UPDATE) with no transaction — a failure between them leaves an
    orphaned/`pending_run` projection.
  - `pg_sbom.go` updates the compatibility `artifact_sboms` row and the canonical
    `sbom_manifests` row in two non-atomic statements, so vulnerability counts can
    permanently diverge.
  - `pg_tenant.go GetByID` returns invite membership by invite UUID with no org
    scoping.
  - *Nuance:* Many `pg_service.go`/`pg_environment.go` single-row reads
    (`GetByID`, `GetByName`) are intentionally unscoped because org enforcement is
    layered in the router via `coreRBAC`/`*OrgResolver`. Those are acceptable
    **provided** every caller applies the resolver — which findings #3–#6 show is
    not universally true. The repository defects above are ones where no
    middleware compensates.
- **Why not production-ready:** Fingerprint-keyed upsert enables cross-tenant
  ownership takeover; non-atomic projection writes produce observable
  inconsistent state that downstream logic acts on.
- **Recommended fix:** Include `org_id` in conflict targets/predicates for
  tenant-owned upserts; wrap multi-statement logical writes in a transaction (the
  repo already has `tx.go`); scope invite lookups by org.

### 20. LLM promotion has a check-then-act race with no route lock
- **Severity:** P2 · **Category:** reliability (concurrency)
- **Files:** `internal/service/llm_provisioning_coordinator.go:145-184, 211-225`.
- **Evidence:**
  ```go
  state, err := c.registry.GetRouteState(...)
  if state == nil || state.DesiredIntentID != intent.ID { ... }   // check
  gatewayObs, err := c.gateway.UpsertRoute(...)                   // act (no lock/CAS)
  ```
  The desired-state check and the gateway update are not guarded by a route/env
  lock or compare-and-swap; rollback/cancel paths also `_ = provisioner.Deprovision(...)`
  and `_ = c.runs.Update(...)`, swallowing cleanup/persistence errors.
- **Why not production-ready:** A newer intent can become desired between the
  check and `UpsertRoute`, so an obsolete deployment overwrites the live gateway
  route. Swallowed rollback errors can leave a backend live while the run is marked
  cancelled/failed.
- **Recommended fix:** Hold a per-route/environment lock (the codebase has
  `runtime_apply_lock.go`) or use a CAS on desired-intent around gateway mutation;
  check and surface deprovision/update errors.

### 21. Coordinators return `nil` when mandatory deps are missing (silent disable)
- **Severity:** P2 · **Category:** fake-completion
- **Files:** `internal/service/backup_run_coordinator.go:149-175`; `backup_restore_coordinator.go:103-129`; `ml_recipe_coordinator.go:192-218`; `ml_inference_provisioning_coordinator.go:191-217`; `llm_provisioning_coordinator.go:70-72`.
- **Evidence:**
  ```go
  if c == nil || c.registry == nil || c.queue == nil { return nil }
  ```
- **Why not production-ready:** A coordinator wired without a mandatory dependency
  silently becomes a no-op while supervisors observe clean success; queued
  production work is never processed and readiness still passes.
- **Recommended fix:** Fail fast at construction/startup (return an error or panic
  during wiring) when required dependencies are nil, rather than degrading to a
  silent success at runtime.

---

## P3 findings

### 22. Hardcoded OCI service-account hash, anonymous-pull CIDR, and provisioning relay
- **Severity:** P3 · **Category:** unsafe-hardcoding
- **Files:** `internal/config/config.go:869-880`; `internal/soulfactory/provisioner_full.go:333-337`.
- **Evidence:**
  ```go
  AllowAnonymousPullCIDRs: []string{"192.168.40.0/24"},
  ServiceAccounts: []OCIServiceAccountConfig{{
      Username: "hive-ci",
      PasswordHash: "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy",
      ...
  }},
  ```
  ```go
  p.nip05Manager.Register(..., []string{"wss://relay.sharegap.net"})
  ```
- **Why not production-ready:** Every install ships the same OCI credential hash
  and pre-authorizes anonymous pulls from a site-specific RFC1918 subnet (only
  relevant if OCI is enabled, which is off by default). Provisioned identities are
  permanently advertised against a hardcoded third-party relay regardless of relay
  policy.
- **Recommended fix:** Remove bundled credential hashes and default CIDRs (require
  explicit operator config, reject the bundled hash in `validate()`); source the
  NIP-05 relay from configuration.

### 23. Event bus can't report handler errors; health-gate ignores observer errors
- **Severity:** P3 · **Category:** observability
- **Files:** `internal/events/events.go:86-90, 124-141`; `internal/rollout/health_gate.go:67-78`.
- **Evidence:** `type Handler func(ctx, Event)` and `Publisher.Publish(ctx, Event)`
  return nothing; only panics are logged. In the health gate, observation *errors*
  increment counters but `continue` without checking
  `consecutiveUnhealthy >= cfg.FailureThreshold`, so a broken observer never trips
  the fast-fail path (waits for full timeout).
- **Why not production-ready:** Handler failures are structurally invisible (no
  retries/acks possible), and a persistently-erroring health observer delays
  rollout failure detection.
- **Recommended fix:** Let handlers return errors so the publisher can log/retry;
  count observer errors toward the failure threshold.

### 24. Payment metadata marshal error silently coerced to `{}`
- **Severity:** P3 · **Category:** debt
- **Files:** `internal/repository/pg_payment.go:42-45`.
- **Evidence:**
  ```go
  metadataJSON, err := json.Marshal(p.Metadata)
  if err != nil { metadataJSON = []byte("{}") }
  ```
- **Why not production-ready:** A serialization defect silently destroys
  caller-provided payment metadata and is indistinguishable from intentionally
  empty metadata.
- **Recommended fix:** Return the marshal error instead of substituting `{}`.

---

## Flow traces

### A. HTTP request → auth → RBAC → handler → repo (tenant isolation)
Well-formed resource routes chain `tier<N>Gate` → `coreRBAC(deps,
authMiddleware, <resource>OrgResolver(...), true)` → handler (e.g.
`GET /deployments/runs/{id}` at `router.go:231`). The resolver loads the resource,
derives its `org_id`, and the RBAC middleware checks the authenticated principal's
membership. Repository reads (`pg_service.GetByID`, etc.) are deliberately
org-agnostic because the middleware enforces the boundary. **The break:** the
`/state*`, `/deployments/runs/{id}/logs`, `/services/{id}/environments/{envId}/logs`,
`/notifications/*`, and `/artifacts/{id}/sbom` routes skip the `coreRBAC`/resolver
link (findings #3–#6), so the request reaches an org-agnostic handler+repo with no
tenant boundary — authenticated cross-tenant read/write.

### B. Rollout execution (canary/blue-green)
`Executor.executeStep` handles `Deploy`/`HealthCheck`/`Promote` against the real
runtime, but `ShiftTraffic` and `Switch` only log and `return nil`
(`executor.go:208-228`, finding #9). A canary plan therefore: deploys the canary
slot (real) → "shifts" 10/50/90% traffic (no-op) → health-checks (against the
canary observe name) → promotes → emits `rollout.completed`. On failure,
`autoRollback` (finding #11) removes slots with all errors swallowed and never
restores the prior primary. Net: the traffic-management heart of the rollout is
fake, and rollback is best-effort with no guarantee.

### C. Backup restore command (Nostr → reactor → coordinator → responder)
A restore command event is routed by the reactor and authorized via
`authorizeBackupCommandRequest`/`isAuthorized` (`backup_handlers.go:121-126`) —
this part is correctly gated. The coordinator then executes
`restoreBackend.Restore(...)` **before** persisting the terminal state; if
`CompleteBackupRestore` fails the run is left non-terminal with no idempotency
token (finding #15), so recovery can re-run the destructive restore. Finally the
terminal reply goes through `BackupRestoreResponder`, which `return nil`s without
publishing when correlation metadata or the relay pool is missing (finding #14),
so the requester may never receive the outcome even though the coordinator
reported success.

---

## Notes on scope and confidence
- All P0/P1 findings and the load-bearing P2s were verified by directly reading the
  cited lines. Line numbers reflect the working tree at audit time and may shift.
- Findings #13 (definition-event auth) was corroborated by confirming that every
  sibling *command* handler calls `isAuthorized` while
  `continuity_definition_handlers.go` calls none; whether a given event type is
  externally reachable depends on the reactor's subscription filters, which should
  be double-checked before prioritizing the fix.
- The repository "unscoped read" pattern (#19 nuance) is acceptable by design where
  middleware enforces org; it becomes a vulnerability only on the routes in #3–#6.
  Fixing the routing gaps closes most of the exposure.
- No code was modified during this audit.
