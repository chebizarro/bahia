# Policies

Policies evaluate artifacts and deployment requests before execution. Open **Policies** (`/policies`) to create, inspect, and evaluate fleet policy.

## Policy model

A policy has a name, optional environment scope, rules, and `enforcement`:

- `warn` records failures but allows the deployment to continue.
- `block` prevents admission until required evidence passes or an authorized approval satisfies the rule.

Rules can require SBOM presence, acceptable security findings, verified signatures, approval, or other registered evidence. Policy evaluation produces an explicit decision and reasons. A missing or rejected evaluation is never an allow.

## Create and read

```bash
bahia policies list
bahia policies get <policy-id>
bahia policies create --name require-sbom --rules '[{"type":"require_sbom"}]' --enforcement block
```

MCP provides `bahia_list_policies`, `bahia_get_policy`, `bahia_create_policy`, `bahia_update_policy`, `bahia_delete_policy`, and `bahia_evaluate_policy`. Updates use the current record revision; re-read after a conflict.

## Evaluation

Deployment admission evaluates the policies that match the service, environment, and artifact. The decision records which rules passed or failed and the evidence used. Manual evaluation is useful before submitting a deployment, but a deployment uses a current evaluation of its own reviewed artifact and target.

Approval rules bind an approval to the reviewed request and revision. An approval for different content cannot be reused.

## Config Fabric

Signed service configuration policy is managed separately under **Config Fabric**. It tracks desired, applied, rejected, and withdrawn configuration versions. See [Config Fabric](config-fabric.md).

## Troubleshooting

- **Blocked for missing SBOM:** wait for the artifact's SBOM availability record, then re-evaluate.
- **Signature failure:** verify the artifact digest and trusted signer.
- **Conflict:** read the current policy and submit the intended revision.
- **Unexpected allow:** inspect the selected scope, enforcement mode, and evaluation reasons.

## Related

- [Artifacts](artifacts.md)
- [Deployments](deployments.md)
- [Security](security.md)
- [Config Fabric](config-fabric.md)
