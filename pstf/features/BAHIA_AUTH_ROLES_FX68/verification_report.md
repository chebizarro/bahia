# FX68 membership-derived role verification

Issue: `bahia-irsry.68`.

The browser derives its role map from service-authored encrypted member records only when the event coordinate and decrypted `org_id`/`pubkey` identify the signed-in session. Latest `created_at` wins, including encrypted removals. The org detail uses a separately named full member-list state for display, while role-gated actions use `roleForOrg`.

The mock store tests use deterministic OCK-encrypted records; the browser test delivers signed relay fixtures with a viewer record for the session and an owner record for another member. It verifies the own viewer role grants viewer-required route access, owner-required access is denied, and org member-management controls are hidden.

Final gate (2026-10-03):

- `pnpm run test:unit`: 1,057 passed, 1 skipped.
- `pnpm run lint`: 0 errors, 0 warnings.
- `pnpm run build`: passed.
- `CGO_ENABLED=0 CI=1 npx playwright test`: 207 passed, 4 skipped, no failures or retries in the final run; port 4173 was checked free before launch.
- The new browser case passed 5 consecutive runs with `--retries=0` before the final suite.
- `CGO_ENABLED=0 go build ./...`: passed.
- `git diff --check`: passed.

Beads status was not changed: this worktree's Dolt server reported `beads_bahia` missing, and the task explicitly forbids writes to `.beads/`. No push was performed per task instructions.
