# Environment States

**Environment States** (`/environment-states`) compares desired and observed deployment state for every service–environment pair.

## What the page shows

Each row joins the `service-state` record with its service and environment definitions: service, environment, deployed artifact, drift status, drift details, and deployment information. Filter by **All**, **Drifted**, or **Unknown**; select the drift cell to open the complete state payload.

`drift_status` values:

| Value | Meaning |
|-------|---------|
| `in_sync` | the observed artifact matches the desired artifact |
| `drifted` | the observed artifact differs from the desired one |
| `deploying` | a deployment is converging the environment |
| `remediation_needed` | drift was detected and the environment's reconcile mode does not auto-apply |
| `unknown` | no accepted observation yet |

Missing or stale evidence is a reason to investigate, not a sign of health.

## Investigating drift

1. Filter to **Drifted** and open the state payload: service id, environment id, desired and observed artifact, author, timestamp.
2. Check the related deployment run and the runtime observation.
3. Fix the runtime difference, or redeploy / roll back through a reviewed deployment.
4. Wait for a new observation; the row updates when the next `service-state` record arrives.

`bahia state list` and `bahia state drifted` show the same records from the CLI.

## Related

- [Environments](environments.md)
- [Deployments](deployments.md)
- [Core Concepts](../core-concepts.md#runtime-observation-and-drift)
