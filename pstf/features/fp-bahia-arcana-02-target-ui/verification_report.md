# Verification report: fp-bahia-arcana-02-target-ui

## Result

The browser UI now creates, lists, and edits explicit Bahia-managed Compose deployment units through signed ContextVM environment mutations. Unit edits use the complete explicit set plus the canonical `expected_updated_at` revision. Multi-unit deploys require explicit selection and carry `deployment_unit_id` through policy preview and `service/deploy`.

Endpoint presentation is limited to `endpoint_ref` aliases. The UI does not load or display Docker host URLs, certificate paths, keys, or credentials.

## Evidence

- `npm run test:unit`: 77 files, 607 tests passed.
- `npm run test:e2e -- tests/e2e/environments-crud-smoke.spec.js tests/e2e/deployment-unit-targeting.spec.js`: 14 passed.
- `npm run lint`: 0 errors and 0 warnings.
- `npm run build`: production static build succeeded.

## Acceptance mapping

- AC1–AC3: `web/tests/e2e/environments-crud-smoke.spec.js`
- AC4–AC6: `web/tests/e2e/deployment-unit-targeting.spec.js`
- Serialization and validation: `web/tests/unit/deployment-units.test.js`
- ContextVM request shape and structured errors: `web/tests/unit/public-controlplane.test.js`

## Scope confirmation

No direct REST mutation shortcut, deployment-unit CRUD operation, build UI, route/TLS workflow, runtime-definition bridge, or rollback behavior was added.
