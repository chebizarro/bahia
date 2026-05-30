# Verification report: bahia-hxpe

## Evidence

- Added machine-readable JSON Schema draft 2020-12 contracts for desired state, deployment units, command receipts, and reconcile policy/state payloads under `schemas/`.
- Schema fields were derived from `internal/domain/runtime_desired_state.go`, `internal/domain/deployment_unit.go`, `internal/api/dto/command_receipt.go`, `internal/api/dto/requests.go`, and reconcile state fields in `internal/domain/models.go`.
- Added PSTF maturity-slice evidence bundles under `pstf/features/bahia-hxpe/maturity_slices/` for Tasks 2, 6, 7, 8, 9, 12, 13, and 14. Each bundle contains acceptance criteria, rollout gates, metrics, rollback criteria, and evidence references.
- No production code paths were changed.

## Tests run

```bash
python3 -m json.tool schemas/desired_state.json >/tmp/desired_state.json
python3 -m json.tool schemas/deployment_unit.json >/tmp/deployment_unit.json
python3 -m json.tool schemas/command_receipt.json >/tmp/command_receipt.json
python3 -m json.tool schemas/reconcile_policy.json >/tmp/reconcile_policy.json
for f in pstf/features/bahia-hxpe/*.json pstf/features/bahia-hxpe/maturity_slices/*.json; do python3 -m json.tool "$f" >/tmp/$(basename "$f"); done
```

Result: passed.
