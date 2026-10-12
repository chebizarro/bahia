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
bahia secrets set <service-id> DATABASE_URL --credential-id cred_<64 hex> --credential-version 1
bahia secrets delete <service-id> <secret-id>
```

List returns metadata only. Secret values live only in the secret store (Signet): the operator imports a value with `signetctl import-credential`, labelled `<org-id>/<service-id>/<name>`, and the secret's record holds only the credential reference. In the web app, import or rotate the value in Signet first with that exact label, then enter the Signet credential id and version when adding or updating the secret; the signed web intent carries only `credential_ref`, and the list shows its id and pinned version. Bahia fetches the value when an authorized operation needs it (a deploy, VM bootstrap, a build, a reveal, run-log redaction) and never stores it; without a secret store those operations fail closed ([Secret store](../../architecture/secret-store.md), [runbook](../../runbooks/signet-secret-store.md)). A secret whose value Bahia once sealed itself is refused until it is rotated, imported and re-pointed. The web app can reveal a value through an interactive confidential request when the signer has read permission. Rejected changes show the daemon's `30315` reason in the secret status notice.

## Routing and health

A service deployment can attach a managed hostname. The route plan binds the service, environment, unit, port, and health path. Route canaries verify the resulting public or internal path.

Runtime supervision publishes managed instance health, recovery attempts, and maintenance state. See [Instance Health](instance-health.md).

Reconciliation retains a runtime observation when the service is first observed or its runtime identity, placement, image, health, or normalized configuration changes. A retained transition publishes a state-change record even if health remains the same. Repeated samples with only a new timestamp or diagnostic metadata update the reconciliation bookkeeping without adding observation history or publishing another state-change event. The current observation link continues to identify the last retained sample.

## Related

- [Builds](builds.md)
- [Artifacts](artifacts.md)
- [Environments](environments.md)
- [Deployments](deployments.md)
- [Managed DNS and HTTPS Routes](../guides/managed-dns-and-https-routes.md)
