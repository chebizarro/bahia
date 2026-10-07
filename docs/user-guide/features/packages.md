# Packages

**Packages** (`/packages`) manages repositories, uploads, promotions, yanks, policy, and drift across package backends.

## Repositories

A repository has a stable name, package format, backend type/reference, and optional policy and backend configuration.

```bash
bahia package repo apply --name libs --format npm --backend-type nexus --backend-ref nexus-main
bahia package repo delete --name libs --reason "repository retired"
```

Supported formats are `npm`, `pypi`, `conan`, `deb`, `rpm`, `pub`, `go_modules`, and `gradle`. Registered backend types include `nexus`, `pulp`, and `filesystem_mock`. Use production backends for durable package storage.

MCP exposes `bahia_package_repository_apply` and `bahia_package_repository_delete`.

## Upload, promote, and yank

```bash
bahia package upload --repository libs --package widgets --version 1.0.0 --file ./widgets-1.0.0.tgz
bahia package promote --source-repository libs --target-repository production \
  --package widgets --version 1.0.0 --filename widgets-1.0.0.tgz
bahia package yank --repository production --package widgets --version 1.0.0 \
  --filename widgets-1.0.0.tgz --reason "security issue"
```

The matching MCP tools are `bahia_package_upload`, `bahia_package_promote`, and `bahia_package_yank`. Promotion and deletion may require approval according to repository policy.

A normal yank removes backend bytes and publishes the governed result. `deprecated: true` publishes advisory deprecation metadata without deleting bytes. Use a reason in either case.

## Observation and drift

Open a package detail page to inspect repository state, versions, operation status, and drift. MCP provides `bahia_package_list`, `bahia_package_get`, `bahia_package_status`, and `bahia_package_drift_detect`. The CLI command is `bahia package drift`.

A successful request acknowledgement is not proof that the backend mutation completed. Follow the canonical status and compare backend observation with desired state. Partial backend effects are reported; retries must use the same idempotency key.

## Safety

- Use immutable package versions.
- Promote between repositories instead of uploading directly to every stage.
- Review SBOM and policy evidence before promotion.
- Keep backend credentials in protected configuration.
- Investigate drift before forcing reconciliation.

## Related

- [Artifacts](artifacts.md)
- [Policies](policies.md)
- [Services](services.md)
