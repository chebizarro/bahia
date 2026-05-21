# HITL decisions — BACKUP_CONTROL_PLANE_ORCHESTRATION

## Required decisions

1. **Backup event namespace**
   - Observed: the proposed backup kinds include `31200-31207`, `30330-30331`, and `38395-38396`.
   - Repository evidence: `31200` is already used by `internal/adapters/signing/nostr_sign.go` for Nostr artifact attestations, and `38395-38396` are documented in `docs/control-planes.md` as AI/ML result kinds.
   - Decision needed: allocate non-conflicting backup definition/policy/run/restore/verification/retention/repository/status/observation/request kinds before implementation.

2. **First vertical implementation slice**
   - Options: Kopia filesystem backup, Velero Kubernetes restore orchestration, or a native snapshot adapter such as ZFS/CSI.
   - Recommendation: start with Kopia filesystem backup plus signed verification provenance because it proves the generic adapter boundary without turning Bahia into a storage engine.

3. **Minimum verified-backup policy**
   - Decision needed: define which verification evidence is mandatory before Bahia can mark a backup as verified and restore-eligible.

## Escalation rationale

These choices affect public protocol semantics, operator safety, and restore reliability. They should not be silently resolved in code.
