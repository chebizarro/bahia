# Independent Review

An independent read-only review found blockers in the first implementation: stage-wide rollback inspection, generic terminal evidence, missing requested-spec enforcement, mutable lineage, incorrect compensation ordering, unsafe public text, non-inspecting abort dry-runs, and thin restart/concurrency/failure evidence.

The reviewed findings were addressed by:

- per-resource inspection during compensation and dry-run;
- explicit correlated `provisioning_result_7950` and `agent_soul_31951` terminal lineage for running, rolled-back, and failed-terminal states;
- engine-side spec/correlation validation before create-or-adopt advances;
- append-only identity, resource, transition, and compensation validation in the CAS store;
- the required Signet policy → credentials → runtime → container → projection compensation order;
- allowlisted public errors, omission of adapter detail, and one-way external resource references;
- retention for recoverable failures as well as terminal failures and rolled-back runs;
- restart, concurrent isolation, multi-resource compensation, and every-forward-stage failure injection tests.

A final high-severity pass additionally led to non-expiring recoverable lineage, checkpoint-and-compensate handling for invalid post-apply reality, sanitized returned errors, and expanded before/after mutation failure classes.
