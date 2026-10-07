# Services

A service is a deployable application owned by one organization. It records repository and CI metadata, the artifact repository, runtime type, managed runtime configuration, secret references, tags, and desired public routing.

## Create and inspect

Open **Services** (`/services`) or use:

```bash
bahia services list
bahia services get <service-id>
bahia services create --org "$ORG" --name payment-api \
  --artifact-repo ghcr.io/acme/payment-api \
  --repo-source gitea --repo-coordinate acme/payment-api \
  --clone-url https://git.example/acme/payment-api.git \
  --ci-provider hiveci --ci-workflow .hive/ci.yaml --runtime-type compose
bahia services update --org "$ORG" --service <service-id> --name payment-api-v2
```

MCP provides `bahia_list_services`, `bahia_get_service`, `bahia_create_service`, `bahia_update_service`, and `bahia_delete_service`. Creates can use a client-minted UUID so an exact retry is idempotent. Updates carry the current `updated_at` revision.

The web app, CLI, and intent-backed MCP tools publish signed service intents. Follow the `30315` status and canonical service record; relay `OK` alone is not admission.

## Repository and builds

`repo_source`, `repo_coordinate`, `clone_url`, `ci_provider`, and `ci_workflow` identify the governed source and build path. Credentials are secret references. A build result from a trusted Hive-CI publisher can register an immutable artifact for the service.

## Runtime actions

Direct actions are available when `direct_runtime_actions.enabled`:

```bash
bahia services deploy  --org "$ORG" --service <service-id> --environment <env-id> --artifact <artifact-id>
bahia services restart --org "$ORG" --service <service-id> --environment <env-id>
bahia services stop    --org "$ORG" --service <service-id> --environment <env-id>
```

These are governed runtime intents. Deployment previews and reviewed deployment intents remain the normal way to change desired artifact state.

Managed Compose configuration is rendered into a Bahia-owned directory. The daemon validates ownership before writing and records only redacted desired and observed metadata.

## Secrets

```bash
bahia secrets list <service-id>
bahia secrets set <service-id> DATABASE_URL --value-file /run/secrets/database-url
bahia secrets delete <service-id> <secret-id>
```

List returns metadata only. Secret values travel in gift-wrapped intents and are resolved only at the authorized operation boundary. The web app can reveal a value through a confidential request when the signer has read permission.

## Routing and health

A service deployment can attach a managed hostname. The route plan binds the service, environment, unit, port, and health path. Route canaries verify the resulting public or internal path.

Runtime supervision publishes managed instance health, recovery attempts, and maintenance state. See [Instance Health](instance-health.md).

## Related

- [Builds](builds.md)
- [Artifacts](artifacts.md)
- [Environments](environments.md)
- [Deployments](deployments.md)
- [Managed DNS and HTTPS Routes](../guides/managed-dns-and-https-routes.md)
