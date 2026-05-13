# HITL Decisions

No human decision was required for this test-only slice. The task description mentioned `createRule` dispatch and an in-component JSON toggle, but the repository implementation exposes bindable `rules` and keeps the JSON toggle in the policies route. Tests were written against the observed component contract.
