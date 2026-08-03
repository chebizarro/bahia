# Browser-first Arcana deployment contract

**Fleet item:** `fp-bahia-arcana-01-contract`
**Parent epic:** `fp-bahia-arcana-web-first-deploy`
**Scope:** item 01 only; UI, build, wizard, routing, and safe rollback implementation remain in items 02–08.

## Objective and boundary

An authorized operator must eventually take Arcana from its private GitHub repository to a healthy public Bahia-managed deployment using only the Bahia web UI. The browser signs canonical mutation intent. It never receives database access, a shell, Compose-file access, or a general-purpose ContextVM console.

This is the one normative typed contract for that journey and the capability audit for item 01. This item adds only missing signer-first service mutation and deployment-decision adapters, tenant authorization, idempotency normalization, and secret-bearing Loom request rejection. It does not add UI.

## Security invariants

1. Requester identity is the verified pubkey of the signed inner ContextVM event. Payload `requested_by` fields cannot override it.
2. The browser default is signed ContextVM JSON-RPC inside a NIP-59 encrypted wrapper. Results are encrypted and correlated to the request.
3. The global pubkey allowlist is only transport admission. Each tenant mutation loads its organization and applies Bahia RBAC before policy evaluation or runtime access.
4. Repository/registry credentials, secret values, payment tokens, and secret-bearing `bunker://` or `nostrconnect://` URLs are forbidden in deployment Nostr events, logs, and argv. Contracts carry opaque references only; logs may contain identifiers, never resolved values.
5. Deployments select an immutable OCI digest, never only a mutable tag.
6. The desired-state builder and runtime lifecycle are the only path from intent to runtime apply/observe.
7. Direct-runtime Compose resolves an explicit Bahia-managed deployment unit (or an unambiguous configured default) and fails closed. It never falls back to Loom synthesis.

## Transport and idempotency

```ts
type UUID = string;
type ISO8601 = string;
type OCIImageDigest = string; // must be sha256:<hex>
type SecretRef = string;      // opaque server-resolved identifier
type ProgressToken = string;

interface ContextVMRequest<P> {
  jsonrpc: "2.0";
  id: string | number;
  method: string;
  params: P & {
    _meta?: { progressToken?: ProgressToken };
    idempotency_key?: ProgressToken; // compatibility alias
  };
}
```

`_meta.progressToken` is canonical. `idempotency_key` is accepted as an alias; if both occur they must match. Deduplication is scoped by verified signer, method, and token. The current bounded cache is process-local, so it protects immediate replay but is not durable across Bahia restarts.

## Typed workflow

### 1. Repository and service

```ts
interface RepositoryRef {
  source: "github";
  repo_coordinate: string;       // owner/repository
  clone_url?: string;            // non-secret HTTPS URL
  web_url?: string;
}

interface RepositorySelection extends RepositoryRef {
  default_branch: string;
  credential_ref: SecretRef;     // target build input; not persisted on Service
}

interface ServiceCreate {
  org_id: UUID;
  name: string;
  artifact_repo: string;
  repository: RepositoryRef;
  default_branch: string;
  runtime_type: "compose";
}
type ServiceCreateMethod =
  ContextVMRequest<ServiceCreate> & { method: "service/create" };

interface ServiceUpdate {
  id: UUID;
  name?: string;
  repo_url?: string;
  repository?: RepositoryRef;
  artifact_repo?: string;
  default_branch?: string;
  runtime_type?: "docker" | "compose";
}
type ServiceUpdateMethod =
  ContextVMRequest<ServiceUpdate> & { method: "service/update" };

interface ServiceDelete { id: UUID; force?: boolean }
type ServiceDeleteMethod =
  ContextVMRequest<ServiceDelete> & { method: "service/delete" };
```

Private GitHub credentials are selected by opaque reference and resolved only by an authorized builder. They never appear in `clone_url`, Nostr, logs, or argv. Bahia now accepts structured repository metadata and org-scoped service create/update/delete. It does not yet persist `credential_ref` or start a private-repository build. Service mutations require `services:write`.

### 2. Build request and compile-time inputs

```ts
interface ArcanaBuildRequest {
  service_id: UUID;
  git_ref: string;
  repository_credential_ref: SecretRef;
  artifact_repo: string;
  build_args: {
    VITE_ARCANA_READ_RELAYS?: string;
    VITE_ARCANA_WRITE_RELAYS?: string;
    VITE_ARCANA_SIGNER_MODE?: string;
    VITE_ARCANA_SEARCH_DVM_PUBKEY?: string;
    VITE_BLOSSOM_URL?: string;
    VITE_ARCANA_INFERENCE_URL?: string;
    VITE_ARCANA_WORKFLOW_API_URL?: string;
    VITE_SUPABASE_URL?: string;
    VITE_SUPABASE_PUBLISHABLE_KEY?: string;
  };
}
```

These `VITE_*` values are compiled into Arcana's static bundle. Changing one requires a new image. `VITE_SUPABASE_PUBLISHABLE_KEY` is public application configuration, not a server secret. This contract defines the build request; item 01 does not add a build handler.

### 3. Immutable artifact

```ts
interface ArcanaArtifact {
  artifact_id: UUID;
  service_id: UUID;
  git_sha: string;
  image_repo: string;
  image_digest: OCIImageDigest;
  manifest_media_type?: string;
  sbom_url?: string;
  signature_ref?: string;
  scan_status?: string;
}
```

The builder must resolve and register the digest. Deploy references `artifact_id` and the canonical artifact supplies `image_repo@image_digest`. Bahia already persists builds/artifacts and projects canonical kind-11316 artifact announcements. Browser-first build submission and artifact registration adapters remain deferred.

### 4. Environment and deployment unit

Existing `environment/create`, `environment/update`, and `environment/delete` mutations carry this contract. `deployment_units` is a complete-set replacement, not an incremental patch. An update that supplies it must also supply `expected_updated_at`.

```ts
interface ArcanaEnvironment {
  org_id: UUID;
  name: string;
  protected: boolean;
  deploy_strategy: "replace" | "blue_green" | "canary";
  reconcile_mode: "observe_only" | "approval_required" | "auto_apply";
  targeting: {
    default_unit_key: string;
    secret_scope_mode: "unit" | "environment";
    default_reconcile_mode:
      "observe_only" | "approval_required" | "auto_apply";
    failure_domain_labels?: Record<string, string>;
  };
  deployment_units: DeploymentUnit[];
}

interface DeploymentUnit {
  key: string;
  display_name?: string;
  runtime_type: "compose";
  endpoint_ref: string;
  compose_dir: string;
  ownership_mode: "bahia_managed";
  reconcile_mode: "observe_only" | "approval_required" | "auto_apply";
  network_profile?: Record<string, string>;
  runtime_config?: Record<string, unknown>;
}
```

Environment mutations require `environments:write`. Bahia atomically reconciles the full unit set with optimistic revision checks. The prior claim that signer-first direct-runtime Compose lacks usable deployment-unit mutation is therefore **refuted**: dedicated unit CRUD is absent, but signed environment create/update already supplies atomic create/update/delete semantics. A parallel CRUD path would duplicate and weaken those semantics.

### 5. Runtime service

Arcana's audited image is a multi-stage Vite/nginx build. nginx runs as non-root, listens on 8080, and serves `GET /healthz`. The static SPA has no persistent database.

```ts
interface ArcanaRuntimeService {
  image: string; // image repository plus immutable digest
  container_port: 8080;
  healthcheck: {
    protocol: "http";
    method: "GET";
    path: "/healthz";
    port: 8080;
  };
  restart_policy: "unless-stopped";
  environment: Record<string, string>; // non-secret runtime literals
  secret_refs: Record<string, SecretRef>;
  volumes: [];                         // no persistent storage
}
```

`DesiredServiceSpec` and adapters can represent these fields. Today `DesiredStateBuilder` fills process configuration only for adopted services; a normal managed service has no typed persisted runtime definition. Item 01 intentionally does not add an untyped escape hatch. The schema and builder bridge remain deferred.

### 6. Public hostname and route

```ts
interface PublicRoute {
  service_id: UUID;
  environment_id: UUID;
  deployment_unit_id: UUID;
  hostname: string;
  upstream_scheme: "http";
  upstream_port: 8080;
  health_path: "/healthz";
  tls: "managed";
}
```

Policy must reject hostnames outside the org's allowed zones or already owned by another route. DNS, reverse proxy, certificates, and browser route orchestration do not yet form one canonical end-to-end operation and remain deferred.

### 7. Policy preview

```ts
interface DeploymentPreviewRequest {
  service_id: UUID;
  environment_id: UUID;
  deployment_unit_id: UUID;
  artifact_id: UUID;
  route?: PublicRoute;
}
interface DeploymentPreview {
  decision: "allow" | "deny" | "approval_required";
  blockers: Array<{ code: string; message: string }>;
  warnings: Array<{ code: string; message: string }>;
  desired_state_hash?: string;
}
```

Bahia has authoritative policy evaluation during deploy and policy persistence/reactor paths. A complete typed browser preview adapter is missing. Preview is advisory; deploy must always re-evaluate current policy.

### 8. Deploy and approval

```ts
interface ServiceDeploy {
  service_id: UUID;
  environment_id: UUID;
  deployment_unit_id?: UUID;
  artifact_id: UUID;
}
type ServiceDeployMethod =
  ContextVMRequest<ServiceDeploy> & { method: "service/deploy" };

interface DeploymentDecision {
  intent_id: UUID;
  decision: "approve" | "reject";
}
type ApprovalMethod =
  | (ContextVMRequest<DeploymentDecision> &
      { method: "approval/approve" })
  | (ContextVMRequest<DeploymentDecision> &
      { method: "approval/reject" });
```

Deploy requires `deployments:write`; decisions require `deployments:approve`. Service and environment must belong to the same org before policy runs. Method and `decision` must agree. Deploy builds a canonical desired-state snapshot and persists an intent. Auto-approved intents apply only through `RuntimeLifecycleService`, observe, and persist state. Protected or policy-gated intents stop pending approval; item 01 decisions update that intent, but automatic post-approval execution/resume is not yet wired.

### 9. Observation

```ts
interface ArcanaDeploymentObservation {
  intent_id: UUID;
  run_id?: UUID;
  desired_state_hash: string;
  runtime: "pending" | "running" | "healthy" | "degraded" | "failed";
  container_health: "healthy"; // GET /healthz:8080
  route_ready: boolean;
  tls_ready: boolean;
  observed_at: ISO8601;
}
```

Bahia persists runs, runtime observations, and environment/service state. Arcana's `/healthz` proves nginx serves; it does not prove relay, inference, or workflow API health. A browser aggregate including route and TLS readiness is still missing.

### 10. Rollback

```ts
interface RollbackRequest {
  service_id: UUID;
  environment_id: UUID;
  deployment_unit_id?: UUID;
  target_artifact_id: UUID;
}
```

Rollback must create a new canonical intent, rebuild desired state, re-evaluate current policy, respect protected-environment approval, and use the same lifecycle. Bahia has registry/reactor rollback mechanics, but item 01 deliberately exposes no ContextVM handler: current registry rollback selects history and creates an already-approved intent without those current-policy/protection guarantees. A thin handler would create a bypass.

## Capability matrix

Legend: **E2E** = signed browser-store operation through registered backend and persistence/runtime; **backend** = canonical backend exists but browser adapter is absent; **partial** = some layers exist; **added** = item 01 closed the backend signer-first gap; **missing** = deferred.

| Capability | Before item 01 | After item 01 | Evidence / remaining gap |
|---|---|---|---|
| Encrypted ContextVM request/result | E2E | E2E | Signed kind-25910 inner request, NIP-59 wrapper, correlated encrypted result. |
| Tenant RBAC | Partial | **Added** | Org permission precedes policy/runtime; org-less mutations fail. |
| Structured repository | Partial | **Added schema** | Service create/update supports repository metadata and org. Credential reference/build clone missing. |
| Service create | Partial | **Added/hardened** | Typed, relay-first, `services:write`. |
| Service update/delete | Missing handlers | **Added** | Typed, registered, relay-first, `services:write`. |
| Environment CRUD | E2E | E2E/hardened | Existing persistence; create requires org; update/delete authorize loaded org. |
| Deployment-unit mutation | E2E via environment full-set | E2E | Atomic full-set reconciliation plus revision guard; dedicated CRUD unnecessary. |
| Direct-runtime Compose resolution | Backend/E2E deploy | E2E deploy | Explicit/default unit; fails closed; no Loom fallback. |
| Private GitHub build | Missing | Missing | Contract defined; builder/credential handler and UI deferred. |
| Arcana `VITE_*` build args | Missing | Missing | Contract defined; build pipeline adapter deferred. |
| Immutable artifact | Backend | Backend | Build/artifact repositories and kind-11316 projection exist; browser build/result wiring missing. |
| Managed runtime definition | Partial | Partial | Desired spec supports fields; normal service persistence/builder bridge missing. |
| Deploy/apply/observe | Partial backend path | **Hardened partial** | Typed deploy and same-org `deployments:write` check before policy; auto-approved intents apply/observe. |
| Post-approval execution resume | Missing | Missing | Decision changes intent status; no coordinator resumes runtime apply after approval. |
| Policy during deploy | E2E backend path | E2E backend path | Authoritative evaluation exists; browser preview adapter missing. |
| Approval/rejection | Backend only | **Added handlers** | Typed approve/reject, `deployments:approve`, decision agreement. |
| Runtime observation | Backend/partial | Partial | State persists; combined runtime/route/TLS browser view missing. |
| Hostname/DNS/proxy/TLS | Partial subsystems | Missing workflow | No single browser-first route operation/readiness aggregate. |
| Safe rollback | Unsafe partial | Missing by design | Current path lacks current-policy/protected approval guarantees. |
| ContextVM idempotency | Progress token only | **Hardened** | Alias matching and signer+method+token scope; still process-local. |
| Loom container deploy | Unsafe for Arcana | Confirmed unsuitable | Only pull/stop/rm and bare `docker run -d --name … image`; no env-file, volumes, restart, healthcheck, or secret mounts. |
| Secret-safe Loom submission | Unsafe | **Hardened** | Raw secrets/payment tokens and bunker URLs in cmd/args/env/params rejected without echo. Reference resolution still missing. |
| Browser wizard | Missing | Missing | Explicitly outside item 01; no UI added. |

## Later-subtask acceptance boundary

The journey becomes browser-first only when later work:

1. selects the private GitHub repo and opaque credential reference;
2. builds with typed Arcana inputs and records Git SHA plus OCI digest;
3. persists the managed runtime definition and builds desired state from it;
4. creates/selects an explicit Bahia-managed unit;
5. previews policy, obtains approval when required, and deploys the digest;
6. provisions hostname, proxy, DNS, and TLS without shell or Compose edits;
7. presents runtime, `/healthz`, route, and TLS observation;
8. rolls back through a new policy-checked, approval-aware intent; and
9. provides durable idempotency across Bahia restarts.

Until these gaps close, this contract is normative but the complete Arcana browser-only journey is not available.
