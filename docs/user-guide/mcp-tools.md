# MCP Tools Reference

Bahia exposes Model Context Protocol (MCP) over `/mcp` and `/api/v1/mcp`. Use JSON-RPC discovery at runtime: `tools/list` is the authority for the exact tools enabled by the running server.

## Connect and discover

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "tools/list",
  "params": {}
}
```

Call a discovered tool with its advertised schema:

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "method": "tools/call",
  "params": {
    "name": "bahia_list_services",
    "arguments": {}
  }
}
```

## Authorization

Every `tools/call` request is authorized independently. There is no unauthenticated development bypass for tool execution.

For external callers:

1. the request context must contain an authenticated principal, normally established with a valid NIP-98 `Authorization: Nostr …` header;
2. the principal must have a normalized Nostr pubkey;
3. that pubkey must appear in the server's explicit MCP `AuthorizedPubkeys` set.

An empty external allowlist denies all external tool calls. The standard app constructor currently leaves the MCP external allowlist empty, so its HTTP MCP endpoint fails closed for external `tools/call` requests. Embeddings that intentionally expose external calls must populate `ServerDeps.AuthorizedPubkeys`; this is not inferred from a broad authentication toggle.

An internal system principal is allowed only when it carries the explicit admin role; merely labeling a caller “system” is insufficient. For authorized external callers, secret create, update, and delete calls additionally resolve the target service's organization and require `secrets:write` through organization RBAC. Cross-tenant service/secret access fails closed. Explicit system-admin callers bypass that tenant check.

```bash
curl -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -H "Authorization: Nostr <base64-signed-nip98-event>" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"bahia_list_services","arguments":{}}}'
```

Listing protocol metadata is not permission to execute a listed tool. Always handle authorization failures from `tools/call`.

## Registered tool inventory

The names below are verified against the current `internal/mcp` registries. Some registries are enabled only when their backing service or feature is configured, so a deployed instance can expose a subset.

### Services, environments, state, and observations

- Services: `bahia_list_services`, `bahia_get_service`, `bahia_create_service`, `bahia_update_service`, `bahia_delete_service`
- Environments: `bahia_list_environments`, `bahia_get_environment`, `bahia_create_environment`, `bahia_update_environment`, `bahia_delete_environment`
- State: `bahia_list_states`, `bahia_list_drifted`, `bahia_get_observation`

Signer-first mutations can return transport/correlation metadata rather than a completed domain object. Follow the canonical Nostr observables named in the result.

### Deployments and runs

- Intent workflow: `bahia_deploy`, `bahia_rollback`, `bahia_list_intents`, `bahia_get_intent`, `bahia_approve_intent`, `bahia_reject_intent`
- Compatibility names: `bahia_create_intent`, `bahia_approve_deployment`, `bahia_reject_deployment`, `bahia_get_deployment_status`
- Runs: `bahia_list_runs`, `bahia_get_run`, `bahia_create_run`, `bahia_complete_run`, `bahia_get_run_logs`

Use `bahia_assistant_service_deploy` and `bahia_assistant_service_rollback` for assistant-oriented asynchronous receipts.

### Builds, artifacts, SBOMs, and signatures

- Builds: `bahia_list_builds`, `bahia_get_build`, `bahia_register_build`, `bahia_update_build_status`
- Artifacts: `bahia_list_artifacts`, `bahia_get_artifact`, `bahia_register_artifact`
- SBOM: `bahia_get_sbom`, `bahia_get_sbom_packages`, `bahia_search_sbom_packages`, `bahia_ingest_sbom`
- Signatures: `bahia_list_signatures`, `bahia_get_signature`, `bahia_list_verified_signatures`, `bahia_has_verified_signature`, `bahia_verify_signatures`

### Policies, secrets, workers, and payments

- Policies: `bahia_list_policies`, `bahia_get_policy`, `bahia_create_policy`, `bahia_update_policy`, `bahia_delete_policy`, `bahia_evaluate_policy`
- Secrets: `bahia_list_secrets`, `bahia_create_secret`, `bahia_update_secret`, `bahia_delete_secret`
- Worker reads and cost: `bahia_list_workers`, `bahia_get_worker`, `bahia_get_worker_pricing`, `bahia_estimate_cost`, `bahia_get_run_cost`, `bahia_get_payment_history`
- Worker control: `bahia_worker_cordon`, `bahia_worker_uncordon`, `bahia_worker_drain`, `bahia_worker_undrain`, `bahia_worker_maintenance_enter`, `bahia_worker_maintenance_exit`, `bahia_worker_labels_update`, `bahia_worker_preview_eligibility`
- Worker assignment/drain reads: `bahia_worker_get_assignments`, `bahia_worker_list_assignments`, `bahia_worker_get_drain_status`, `bahia_worker_list_drain_status`

### Notification channels and logs

- Channels: `bahia_list_notification_channels`, `bahia_get_notification_channel`, `bahia_create_notification_channel`, `bahia_update_notification_channel`, `bahia_delete_notification_channel`, `bahia_test_notification_channel`
- Logs: `bahia_list_notifications`, `bahia_get_notification`, `bahia_mark_notification_read`, `bahia_dismiss_notification`

`bahia_get_notification` and `bahia_dismiss_notification` are registered compatibility tools but currently return unsupported. `bahia_mark_notification_read` searches recent logs and overwrites delivery status to `sent`; its read/unread behavior is a compatibility mapping, not a separate receipt model.

### LLM and ML

- LLM: `bahia_llm_list_routes`, `bahia_llm_create_route`, `bahia_llm_update_route`, `bahia_llm_register_release`, `bahia_llm_list_releases`, `bahia_llm_deploy`, `bahia_llm_approve_deployment`, `bahia_llm_reject_deployment`, `bahia_llm_rollback`
- Assistant LLM: `bahia_assistant_llm_deploy`, `bahia_assistant_llm_approve_deployment`, `bahia_assistant_llm_rollback`
- ML: `bahia_ml_import_model`, `bahia_ml_run_recipe`, `bahia_ml_deploy`, `bahia_ml_rollback`, `bahia_ml_list_state`, `bahia_ml_get_state`, `bahia_ml_get_provenance`
- Assistant ML: `bahia_assistant_ml_deploy`, `bahia_assistant_ml_approve_deployment`, `bahia_assistant_ml_rollback`

Use the names above exactly; the older model-import and recipe-run spellings are not registered.

### Package management

`bahia_package_repository_apply`, `bahia_package_repository_delete`, `bahia_package_upload`, `bahia_package_promote`, `bahia_package_yank`, `bahia_package_list`, `bahia_package_get`, `bahia_package_status`, and `bahia_package_drift_detect`.

### DNS and FIPS

- DNS reads: `bahia_dns_list_endpoints`, `bahia_dns_list_drift`
- DNS assistant operations: `bahia_assistant_dns_zone_create`, `bahia_assistant_dns_policy_apply`, `bahia_assistant_dns_record_override`, `bahia_assistant_dns_drift_remediate`, `bahia_assistant_dns_list_endpoints`, `bahia_assistant_dns_list_drift`
- FIPS: `bahia_fips_list_mesh_nodes`, `bahia_fips_mesh_status`

### Tool provisioning and policy

`bahia_tool_provision_request`, `bahia_tool_provision_status`, `bahia_tool_provision_approve`, `bahia_tool_provision_reject`, `bahia_tool_profile_get`, `bahia_tool_denylist_list`, `bahia_tool_denylist_add`, and `bahia_tool_denylist_remove`.

### Backup

When the backup registry is configured, the server registers both the base name and a `bahia_`-prefixed alias for each operation:

- apply: `apply_backup_repository`, `apply_backup_policy`, `apply_backup_recipe`, `apply_backup_definition`
- request/approval: `request_backup_run`, `request_backup_verification`, `request_backup_restore`, `approve_backup_restore`, `reject_backup_restore`, `request_backup_retention`
- probe: `probe_backup_repository`
- list: `list_backup_repositories`, `list_backup_policies`, `list_backup_recipes`, `list_backup_definitions`, `list_backup_runs`, `list_backup_restores`, `list_backup_retention_runs`
- inspect: `inspect_backup_repository`, `inspect_backup_policy`, `inspect_backup_recipe`, `inspect_backup_definition`, `inspect_backup_run`, `inspect_backup_restore`, `inspect_backup_retention_run`

The verified prefixed aliases are: `bahia_apply_backup_repository`, `bahia_apply_backup_policy`, `bahia_apply_backup_recipe`, `bahia_apply_backup_definition`, `bahia_probe_backup_repository`, `bahia_request_backup_run`, `bahia_request_backup_verification`, `bahia_request_backup_restore`, `bahia_approve_backup_restore`, `bahia_reject_backup_restore`, `bahia_request_backup_retention`, `bahia_list_backup_repositories`, `bahia_list_backup_policies`, `bahia_list_backup_recipes`, `bahia_list_backup_definitions`, `bahia_list_backup_runs`, `bahia_list_backup_restores`, `bahia_list_backup_retention_runs`, `bahia_inspect_backup_repository`, `bahia_inspect_backup_policy`, `bahia_inspect_backup_recipe`, `bahia_inspect_backup_definition`, `bahia_inspect_backup_run`, `bahia_inspect_backup_restore`, `bahia_inspect_backup_retention_run`.

### Documentation

- `bahia_docs_list` lists the generated user-guide catalog.
- `bahia_docs_read` reads a topic by its catalog slug.

The catalog scans `docs/user-guide/**/*.md`; new guide files become MCP documentation resources and `/docs/<topic>` pages from the same source.

## Resources

Use `resources/list` and `resources/read` rather than assuming a static resource set. Current registries can expose:

- `bahia://service/<id>`
- `bahia://environment/<id>`
- `bahia://worker/<pubkey>`
- `bahia://artifact/<id>`
- `bahia://policy/<id>`
- `bahia://docs/<topic>`
- DNS endpoint resources
- FIPS mesh-node resources

```json
{
  "jsonrpc": "2.0",
  "id": 5,
  "method": "resources/read",
  "params": {
    "uri": "bahia://docs/features-deployments"
  }
}
```

## Asynchronous completion

A successful tool response may mean that Bahia validated and published a request, not that the workflow completed. Follow returned request IDs and canonical observables:

- `30900` for current state;
- `30315` for operational status;
- `4903` for audit facts;
- domain-specific NIP-51/NIP-78 or standard-NIP events where the result identifies them.

Relay `OK` confirms transport acceptance only.

## Errors

Tool failures normally return MCP content with `isError: true`; malformed JSON-RPC can return a protocol error. Check both.

```json
{
  "jsonrpc": "2.0",
  "id": 3,
  "result": {
    "content": [{"type": "text", "text": "{\"error\":\"service not found\"}"}],
    "isError": true
  }
}
```

## Best practices

1. Call `tools/list` and use its input schemas.
2. Authenticate every tool call and handle fail-closed authorization.
3. Use `bahia_docs_list` before assuming a documentation topic.
4. Correlate asynchronous requests with durable Nostr observables.
5. Check `isError` even when JSON-RPC itself succeeded.

## Related

- [Nostr Integration](nostr-integration.md) — Event model and durable completion
- [CLI Reference](cli-reference.md) — Operator commands
- [Notifications](features/notifications.md) — Tenant-scoped channel behavior
