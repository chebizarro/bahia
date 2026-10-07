# Environments

An environment defines where and how a service runs. Its deployment units identify concrete runtime targets, while strategy, reconciliation, protection, and secret-scope settings control deployment behavior.

## Deployment units

Each unit can define a durable key, runtime type, endpoint reference, Compose directory or namespace, ownership mode, reconcile mode, failure-domain label, runtime configuration, and worker selector.

`deployment_units` is a complete set:

- omitting it on update leaves the current set unchanged;
- sending `[]` returns to the implicit default unit;
- sending a non-empty list replaces the explicit set.

The daemon rejects duplicate keys, ambiguous defaults, invalid endpoint references, and stale `expected_updated_at` revisions.

## Web and CLI

Open **Environments** (`/environments`) to create an environment, review its units, and inspect associated state.

```bash
bahia environments list
bahia environments get <environment-id>
bahia environments create --org "$ORG" --name production --units-file units.json
bahia environments update <environment-id> --units-file units.json --expected-updated-at <rfc3339>
bahia environments units list <environment-id>
bahia environments units create <environment-id> --file unit.json
bahia environments units update <environment-id> <unit-key> --file unit.json
```

Inline unit flags are also available; see `bahia environments create --help`. MCP provides list, get, create, update, and delete tools.

## Deployment behavior

- `strategy`: `replace`, `blue_green`, or `canary`.
- `reconcile_mode`: `observe_only`, `auto_apply`, `approval_required`, or `disabled`.
- `protected`: requires deployment approval.
- `secret_scope_mode`: `service`, `environment`, or `unit`.

A deployment preview resolves the selected unit and produces a desired-state hash. If an environment has multiple units, the operator must choose one explicitly.

## State and drift

The service-state record joins desired artifact, observed artifact, deployment status, and drift for each service/environment target. Use **Environment States**, `bahia state list`, or `bahia state drifted` to investigate.

## Virtualization

Persistent VM resources use the virtualization query and approval surface. An environment may reference a VM-backed execution plane, but VM lifecycle governance is separate from the environment record. See [Virtual Machines](virtual-machines.md).

## Related

- [Services](services.md)
- [Deployments](deployments.md)
- [Environment States](environment-states.md)
- [Policies](policies.md)
