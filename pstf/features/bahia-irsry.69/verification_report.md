# bahia-irsry.69 verification

- AC1: `TestDeploymentCreateIntentPublishesCanonicalOnce`, `TestDeploymentDecisionIntentPublishesCanonicalOnce`, and `TestDeploymentRollbackIntentFromPriorRunPublishesCanonicalOnce` exercise the registry-backed side effect, direct canonical publisher, bounded acceptance, replay, authorization, and stale-revision conflict paths.
- AC2–AC5: `TestRuntimeIntentOperations`, `TestRuntimeContextVMDualDispatch`, `TestLLMDeploymentIntentOperations`, and `TestBackupIntentHandler_RestoreApproval` exercise operation dispatch, status, replay, unauthorized principals, and supplied-revision conflicts. The LLM and backup tests use registry fakes for the operation boundary; existing registry/service tests cover the underlying transitions.
- AC6: Domain-enabled branches are guarded by registered handlers. Existing disabled-domain tests in `internal/controlplane` remain green.
- AC7: `TestDeploymentIntentContentFixtures` parses every checked-in operation shape in `web/tests/fixtures/deployment-intents.json`.
- Quality gates: Go full build/vet/test, archtest `TestNoNew`, web unit/lint/build, and Playwright all passed. Playwright used isolated port 51234 because another worktree owned the required 4173 port; 206 passed, 4 skipped.
- Beads tracker: `bd show bahia-irsry.69` could not open the worktree Dolt database (`beads_bahia` absent at the configured server). This session intentionally did not alter `.beads/`.
