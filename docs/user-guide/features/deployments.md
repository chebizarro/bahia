# Deployments

A deployment declares which immutable artifact should run for a service, environment, and optional deployment unit. Bahia separates the reviewed intent, the execution run, and the runtime observation.

## Workflow

1. **Preview** resolves the service, environment, unit, secrets, policies, and managed runtime configuration. It returns a bounded plan and desired-state hash.
2. **Submit** publishes a signed deployment intent containing the reviewed hash.
3. **Evaluate** applies policy and protected-environment rules.
4. **Approve or reject** when review is required.
5. **Run** executes through the selected runtime or worker.
6. **Observe** compares the running digest with desired state and publishes drift.

Relay acceptance is not deployment completion. Follow the correlated `30315` status, the canonical deployment-intent and deployment-run records, and the service-state observation.

## Web

Use **Deployments** (`/deployments`) for intents and runs, **Pending Approvals** (`/deployments/pending`) for review, and the run detail page for status and logs. The deployment wizard requires an explicit unit when an environment has multiple units.

Managed Compose deploys render only into a Bahia-owned Compose directory, validate the staged project, and apply the complete project. Bahia refuses an unowned directory. Secret values may exist in generated runtime files but are excluded from events, summaries, logs, and observations.

## CLI

```bash
bahia deployments preview --service <service-id> --environment <env-id> --artifact <artifact-id>
bahia deploy --org "$ORG" --service <service-id> --environment <env-id> \
  --artifact <artifact-id> --expected-desired-state-hash <hash>
bahia deployments approve --org "$ORG" --intent <intent-id> --expected-updated-at <rfc3339>
bahia deployments reject  --org "$ORG" --intent <intent-id> --expected-updated-at <rfc3339>
bahia state list
bahia state drifted
bahia logs run <run-id> --tail 100
```

`bahia rollback` creates a new desired-state intent for an explicit successful artifact and identifies the intent it supersedes. `bahia deployments route-attach` attaches managed HTTPS to a current deployment.

## MCP

Deployment tools include `bahia_deploy`, `bahia_rollback`, intent list/get/approve/reject tools, run list/get/create/complete tools, status, and run-log retrieval. A `pending` tool result is successful admission, not final convergence.

## Approval and policy

An environment marked `protected`, a reconcile mode of `approval_required`, or a blocking policy can hold a deployment. Approval is bound to the current intent revision. Re-read after a conflict and approve the current `updated_at`; do not reuse stale review evidence.

## Logs and HTTP

The CLI and `bahia_get_run_logs` use the governed run-log surface. The daemon also mounts authenticated HTTP reads for stored run logs and live service/environment logs when their dependencies are configured.

## Troubleshooting

- **Pending:** inspect policy evaluation and the protected/reconcile settings.
- **Failed:** inspect run logs, artifact pull access, endpoint resolution, and worker eligibility.
- **Drifted:** compare desired and observed digests, then redeploy or use the configured reconciliation mode.
- **No status after relay OK:** use the intent and event IDs to inspect the outbox and status subscription.
- **`deployment_run_health` warns:** stale-run health publication is suspended because kind-30100 Loom status history has no independent EOSE catch-up barrier. Read the canonical deployment-run record and Loom status stream directly; absence of a stale-health event is not evidence that the run is healthy.

## Related

- [Services](services.md)
- [Environments](environments.md)
- [Artifacts](artifacts.md)
- [Policies](policies.md)
- [Environment States](environment-states.md)
