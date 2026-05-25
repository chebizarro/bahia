# Verification Report: bahia-pr5i

## Evidence

- Added `artifact_ref` to Kind 31402 standby node definition serialization and decoding.
- Registered a continuity projection applier that updates `Worker.StandbyAssignments` from decoded standby definitions, replacing entries by service key and preserving multiple assignments per worker.
- Added `standby_assignments` worker JSONB persistence and migration so projected assignments survive repository read/write.
- Added deterministic unit coverage for hot/warm artifact refs, cold standby empty refs, and standby serialization.

## Tests Run

```text
go test ./internal/... -run "Standby" -v -count=1
```

Result: passed.
