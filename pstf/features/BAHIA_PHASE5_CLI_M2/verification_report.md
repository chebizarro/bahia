# Verification — Bahia Phase 5 CLI M2 (`bahia-irsry.13.7`)

## Intended behavior

The CLI publishes operator-signed kind `30900` deployment and runtime intents matching D69's checked-in wire fixtures. It persists each event in the CLI outbox before publication and follows kind `30315` status. Legacy ContextVM CRUD methods for these operations are removed; deployment preview and route-attach remain ContextVM calls with replayable idempotency keys.

## Acceptance evidence

- `cmd/cli/deployment_intents_test.go` compares all seven CLI deployment/runtime operations against `web/tests/fixtures/deployment-intents.json`, including `d`, domain, schema, op, org, intent ID, exact JSON content, and valid signatures.
- The fixture test simulates relay acceptance without a status and verifies exit code 2 plus seven pending CLI outbox entries.
- `TestCLIRuntimeIntentThroughD69ProcessorAcceptsAndRejects` sends signed CLI events through `IntentProcessor.ProcessInProcess` and the D69 runtime handler. Accepted status yields exit 0 and the lifecycle action; rejected status yields exit 1 without another action.
- `TestCLIDeploymentRejectThroughD69ProcessorPublishesCanonicalState` sends a CLI decision through the D69 deployment handler with in-memory repositories. Accepted status yields exit 0, persisted decision, and a signed canonical kind `30900` record. Unauthorized replay yields exit 1 without another canonical record.
- `TestDeploymentAndRuntimeDirectCommandAliases` verifies `bahia deploy`, `bahia rollback`, and `bahia services deploy|restart|stop` are executable aliases of the existing grouped commands.
- Existing D69 handler tests cover deployment creation/rollback and approval decisions with canonical publication, authorization, revision conflicts, and idempotency.

## Gate

`CGO_ENABLED=0 go build ./... && go vet ./... && go test ./...` passed. `git diff --check` and gofmt passed. No web production code changed, so the web gate is not applicable.

## Scope notes

The CLI has no LLM deployment/approval or backup restore-approval commands in this branch. Their daemon intent fixtures exist, but no CLI command was migrated or invented for them. Beads status could not be updated: `bd prime` failed because the worktree's configured Dolt server has no `beads_bahia` database; `.beads/` was not modified.
