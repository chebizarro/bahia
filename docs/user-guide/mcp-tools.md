# MCP Tools Reference

Bahia exposes a Model Context Protocol (MCP) server for agents. It is the same control plane the CLI and web app use: reads come from the daemon's local copy of the canonical relay state, and most writes use kind-`30900` intents applied in-process. Backup-run requests are an exception: MCP requires the operator's actual signed event. `tools/list` on the running server is the authority for the exact set; this page documents the full registry.

## Connecting

The server is mounted at `POST /mcp` (JSON-RPC 2.0, protocol version `2024-11-05`) and supports `initialize`, `tools/list`, `tools/call`, and `resources/list`. The operator assistant uses the same server in-process; its tool catalog is the agent-safe subset of this registry filtered by `assistant.permissions`, optionally extended with external MCP servers (`assistant.mcp.external_servers`).

```json
{"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": {}}
{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": {"name": "bahia_list_services", "arguments": {}}}
```

## Authorization

Every `tools/call` is authorized independently; listing a tool is not permission to run it.

- Over HTTP the caller authenticates with NIP-98 (`Authorization: Nostr <base64 kind-27235 event>`), must hold the platform admin role, and its pubkey must be in `nostr.authorized_pubkeys`. An empty allowlist denies every external call.
- An internal system principal is accepted only when it explicitly carries the admin role.
- Organization-scoped operations additionally check the caller's role in the owning organization (for example `secrets:write` for secret mutations). Cross-organization access fails closed; explicit system admins bypass the tenant check.

Tool failures are returned as MCP content with `isError: true`; malformed JSON-RPC returns a protocol error. Check both.

## Writes

Most write tools build a `30900` intent on the caller's behalf, run it through the daemon's intent processor, and return a JSON result with `intent_id`, `event_id`, and `status`:

| `status` | Meaning |
|----------|---------|
| `accepted` | The canonical record (or deletion tombstone) is visible; it is returned as `state` |
| `pending` | Accepted for processing but canonical state is not yet visible — a success, not a failure |
| `rejected` / `conflict` | Structured tool error with a `reason` |

Pass `idempotency_key` in the arguments or `_meta.progressToken` in the `tools/call` params so a retry replays the same intent instead of applying twice; without either, each call mints a new UUIDv7 intent. Creates derive an omitted entity `id` from the intent ID. Supply `org_id` only when the owning organization cannot be derived from canonical state; a conflicting `org_id` on an existing entity is rejected. Request-style tools (ML commands, tool approvals, signature verification, SBOM import, notification channel tests, policy evaluation) return `accepted` once their signed `30315` status is visible.

## Tool registry

### Services, environments, and state

- `bahia_list_services`, `bahia_get_service`, `bahia_create_service`, `bahia_update_service`, `bahia_delete_service`
- `bahia_list_environments`, `bahia_get_environment`, `bahia_create_environment`, `bahia_update_environment`, `bahia_delete_environment`
- `bahia_list_states`, `bahia_list_drifted`, `bahia_get_observation`

### Deployments and runs

- Intents: `bahia_deploy`, `bahia_rollback`, `bahia_create_intent`, `bahia_list_intents`, `bahia_get_intent`, `bahia_approve_intent`, `bahia_reject_intent`, `bahia_approve_deployment`, `bahia_reject_deployment`, `bahia_get_deployment_status`
- Runs: `bahia_list_runs`, `bahia_get_run`, `bahia_create_run`, `bahia_complete_run`, `bahia_get_run_logs`
- Assistant receipts: `bahia_assistant_service_deploy`, `bahia_assistant_service_rollback`

### Builds, artifacts, SBOMs, and signatures

- Builds (read-only; Hive-CI evidence creates them): `bahia_list_builds`, `bahia_get_build`
- Artifacts: `bahia_list_artifacts`, `bahia_get_artifact`, `bahia_register_artifact` (`artifact/register` intent; `build_id`, `service_id`, `image_repo`, `image_tag`, `image_digest` required)
- SBOM: `bahia_get_sbom`, `bahia_get_sbom_packages`, `bahia_search_sbom_packages`, `bahia_ingest_sbom` (`sbom/import` with inline SPDX or CycloneDX JSON up to 360 KiB; acknowledges the request, follow the SBOM reference and availability events for completion)
- Signatures: `bahia_list_signatures`, `bahia_get_signature`, `bahia_list_verified_signatures`, `bahia_has_verified_signature`, `bahia_verify_signatures` (`artifact/signature-verify`; the bounded status carries counts, canonical signature records carry the durable result)

### Policies

`bahia_list_policies`, `bahia_get_policy`, `bahia_create_policy`, `bahia_update_policy`, `bahia_delete_policy`, `bahia_evaluate_policy`. Evaluation publishes a `policy/evaluate` intent and returns the coordinate of the bounded `30315` status that holds the decision; a rejected evaluation is never an allow.

### Secrets

`bahia_list_secrets`, `bahia_create_secret`, `bahia_update_secret`, `bahia_delete_secret`. Listing returns metadata only; values are never returned by MCP.

### Workers and cost

- Reads: `bahia_list_workers`, `bahia_get_worker`, `bahia_get_worker_pricing`, `bahia_estimate_cost`, `bahia_get_run_cost`, `bahia_get_payment_history`
- Control: `bahia_worker_cordon`, `bahia_worker_uncordon`, `bahia_worker_drain`, `bahia_worker_undrain`, `bahia_worker_maintenance_enter`, `bahia_worker_maintenance_exit`, `bahia_worker_labels_update`, `bahia_worker_preview_eligibility`
- Assignments and drains: `bahia_worker_get_assignments`, `bahia_worker_list_assignments`, `bahia_worker_get_drain_status`, `bahia_worker_list_drain_status`

### Notifications

- Channels: `bahia_list_notification_channels`, `bahia_get_notification_channel`, `bahia_create_notification_channel`, `bahia_update_notification_channel`, `bahia_delete_notification_channel`, `bahia_test_notification_channel`
- Delivery log: `bahia_list_notifications`, `bahia_get_notification`, `bahia_mark_notification_read`, `bahia_dismiss_notification`

Channel reads and write results redact credentials and webhook URLs. The delivery log is the canonical latest-50-per-channel window; `bahia_list_notifications` returns at most 50 entries sorted across channels, `bahia_get_notification` resolves IDs inside that window, `bahia_mark_notification_read` rewrites a recent entry's delivery status to `sent`, and `bahia_dismiss_notification` always reports unsupported because delivery logs are immutable.

### LLM routes

`bahia_llm_list_routes`, `bahia_llm_create_route`, `bahia_llm_update_route`, `bahia_llm_register_release`, `bahia_llm_list_releases`, `bahia_llm_deploy`, `bahia_llm_approve_deployment`, `bahia_llm_reject_deployment`, `bahia_llm_rollback`; assistant receipts `bahia_assistant_llm_deploy`, `bahia_assistant_llm_approve_deployment`, `bahia_assistant_llm_rollback`.

The eight LLM release/lifecycle tools in that list (register release, deploy,
rollback, approve, reject, and the three assistant variants) currently refuse
before dispatch. MCP transport authentication cannot sign an operator's Nostr
event on their behalf. Submit a genuine operator-signed `30900` LLM intent
through a relay;
the daemon validates its local observation and returns a paused rejection
until canonical encrypted release publication and provisioning recovery are
available. No assistant receipt or service-signed outcome is issued for an
MCP-generated unsigned request.
Historical assistant processed markers do not restore an LLM receipt while
canonical provisioning is paused.

### ML models and inference

- Commands: `bahia_ml_import_model`, `bahia_ml_run_recipe`, `bahia_ml_deploy`, `bahia_ml_rollback`
- Registry: `bahia_ml_model_create`, `bahia_ml_model_update`, `bahia_ml_model_delete`, `bahia_ml_version_create`, `bahia_ml_version_update`, `bahia_ml_version_delete`, `bahia_ml_endpoint_create`, `bahia_ml_endpoint_update`, `bahia_ml_endpoint_delete`
- Reads: `bahia_ml_list_state`, `bahia_ml_get_state`, `bahia_ml_get_provenance`
- Assistant receipts: `bahia_assistant_ml_deploy`, `bahia_assistant_ml_approve_deployment` (`intent_id` and `decision` required), `bahia_assistant_ml_rollback`

ML is fleet-scoped: import, recipe run, and approval take an optional `org_id`; deploy and rollback derive the organization from the endpoint's environment. Registry tools take the handler's full desired-state JSON in `content`.

### Packages

`bahia_package_repository_apply`, `bahia_package_repository_delete`, `bahia_package_upload`, `bahia_package_promote`, `bahia_package_yank`, `bahia_package_drift_detect`, `bahia_package_list`, `bahia_package_get`, `bahia_package_status`.

### DNS

- Reads: `bahia_dns_list_endpoints`, `bahia_dns_list_drift`, `bahia_assistant_dns_list_endpoints`, `bahia_assistant_dns_list_drift`
- Intents: `bahia_assistant_dns_zone_create`, `bahia_assistant_dns_zone_update`, `bahia_assistant_dns_zone_delete`, `bahia_assistant_dns_endpoint_create`, `bahia_assistant_dns_endpoint_update`, `bahia_assistant_dns_endpoint_delete`, `bahia_assistant_dns_backend_create`, `bahia_assistant_dns_backend_update`, `bahia_assistant_dns_backend_delete`, `bahia_assistant_dns_policy_create`, `bahia_assistant_dns_policy_apply`, `bahia_assistant_dns_policy_update`, `bahia_assistant_dns_policy_delete`, `bahia_assistant_dns_record_set`, `bahia_assistant_dns_record_override`, `bahia_assistant_dns_override_retire`

Record overrides may return `pending` until the endpoint projection is visible. Drift remediation is CLI-only (`bahia dns drift-remediate`).

### FIPS mesh

`bahia_fips_list_mesh_nodes`, `bahia_fips_mesh_status`.

### Tool provisioning

`bahia_tool_provision_request`, `bahia_tool_provision_status`, `bahia_tool_provision_approve`, `bahia_tool_provision_reject`, `bahia_tool_profile_get`, `bahia_tool_denylist_list`, `bahia_tool_denylist_add`, `bahia_tool_denylist_remove`. Tool provisioning and approval execution are suspended. Approve and reject do not turn SQL provisioning rows into authority: the daemon refuses an unsigned MCP-generated event, and even an operator-signed `tool/approval-response` intent receives a rejected `30315` status. No tool image build or deploy follows that status.

### Backup

Each backup operation is registered under a base name and a `bahia_`-prefixed alias:

- apply: `apply_backup_repository`, `apply_backup_policy`, `apply_backup_recipe`, `apply_backup_definition`
- probe and requests: `probe_backup_repository`, `request_backup_run`, `request_backup_verification`, `request_backup_restore`, `approve_backup_restore`, `reject_backup_restore`, `request_backup_retention`
- list: `list_backup_repositories`, `list_backup_policies`, `list_backup_recipes`, `list_backup_definitions`, `list_backup_runs`, `list_backup_restores`, `list_backup_retention_runs`
- inspect: `inspect_backup_repository`, `inspect_backup_policy`, `inspect_backup_recipe`, `inspect_backup_definition`, `inspect_backup_run`, `inspect_backup_restore`, `inspect_backup_retention_run`

Aliases: `bahia_apply_backup_repository`, `bahia_apply_backup_policy`, `bahia_apply_backup_recipe`, `bahia_apply_backup_definition`, `bahia_probe_backup_repository`, `bahia_request_backup_run`, `bahia_request_backup_verification`, `bahia_request_backup_restore`, `bahia_approve_backup_restore`, `bahia_reject_backup_restore`, `bahia_request_backup_retention`, `bahia_list_backup_repositories`, `bahia_list_backup_policies`, `bahia_list_backup_recipes`, `bahia_list_backup_definitions`, `bahia_list_backup_runs`, `bahia_list_backup_restores`, `bahia_list_backup_retention_runs`, `bahia_inspect_backup_repository`, `bahia_inspect_backup_policy`, `bahia_inspect_backup_recipe`, `bahia_inspect_backup_definition`, `bahia_inspect_backup_run`, `bahia_inspect_backup_restore`, `bahia_inspect_backup_retention_run`.

`request_backup_run` / `bahia_request_backup_run` requires one argument, `signed_intent_event`: the complete NIP-01 event object signed by the same operator pubkey used for MCP NIP-98 authentication. The event must be kind `30900`, `domain=backup`, `op=run`, with a UUIDv7 run `id`, `d=backup-run:<id>`, a stable `intent_id`, and the complete resolved run inputs: recipe, repository, policy (if any), backend, target, verification mode, and `execution_snapshot`. The snapshot must contain the service-signed recipe/repository/policy event IDs and exact definitions; a repository credential profile requires a `credential_version_id` UUID. Do not put secret values in the request. Add exactly one signed NIP-40 `expiration` tag after `created_at` and no more than 15 minutes later. Requests older than 15 minutes or past expiration are rejected; the normal inbound future-clock-skew limit applies.

Publish the signed event to a relay first, then use `signed_intent_event` if an MCP handoff is needed. MCP requires the exact event to be visible in the daemon's local relay-synced store. **Local visibility is not a relay `OK`, delivery quorum, or backup-run acceptance receipt.** A valid request with ACKed configuration versions returns `pending` with the staged service-signed run-state event ID. Replay the same signed request after that state reaches the control-plane relay quorum to receive `accepted`. Neither result starts backup execution; credential recovery and step checkpoints remain unavailable. The old `recipe_id`/`recipe`-only MCP request and MCP `idempotency_key` are not accepted for this tool; reuse the same signed event and `intent_id` for retries.

### Outbox

- `bahia_outbox_status` — pending and failed publish counts; `include_details: true` lists failed entries for authorized callers.
- `bahia_outbox_retry` — re-queue one failed entry (`event_id`) or `all: true`; a delivered entry clears its undelivered marker on the readiness endpoint.

### Documentation

- `bahia_docs_list` — the user-guide catalog.
- `bahia_docs_read` — one topic by slug (`topic`; `index` is the overview).

The catalog is `docs/user-guide/**/*.md`: a file `features/services.md` is topic `features-services`, resource `bahia://docs/features-services`, and page `/docs/features-services`.

## Resources

`resources/list` returns `bahia://docs/<topic>` for every documentation topic, `bahia://dns/…` endpoint resources, and `bahia://fips/…` mesh-node resources.

## Asynchronous completion

A successful write result means the daemon validated and applied or queued the intent. Durable truth is the canonical `30900` record, `30315` statuses, and `4903` audit facts on the relays; follow `intent_id` and `event_id` there when a result is `pending`.

## Related

- [Nostr Integration](nostr-integration.md) — the event contract behind every tool
- [CLI Reference](cli-reference.md) — the same operations from a terminal
- [Operator Assistant](features/operator-assistant.md) — the in-product agent that uses this registry
