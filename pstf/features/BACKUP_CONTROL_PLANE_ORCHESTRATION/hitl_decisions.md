# HITL decisions — BACKUP_CONTROL_PLANE_ORCHESTRATION

## Resolved decisions

1. **Backup event namespace** — resolved 2026-05-21 for bucket U4-1 / Beads `bahia-u4b0`.
   - Observed collision risk: the proposed backup kinds included `31200-31207`, `30330-30331`, and `38395-38396`.
   - Repository evidence: `31200` is already used by `internal/adapters/signing/nostr_sign.go` for Nostr artifact attestations, and `38395-38396` are documented in `docs/control-planes.md` as AI/ML result kinds.
   - Decision: allocate a backup-specific namespace: commands `38400-38409`, terminal results `38410-38419`, status/observations `6981-6984`, replaceable read models `31991-31999`, and attestations `31310-31311`.
   - Tag contract: commands require `d` and narrow scoped tags such as `p`, `t`/`task`, `target`, `backend`, `repository`, `policy`, `recipe`, `run`, `worker`, `site`, `environment`, and `verification` where applicable. Results require `d=result:<request_event_id>`, `e=<request_event_id>`, `p=<requester_pubkey>`, `status`, echoed scoped tags, and `run=<backup-run-id>` when durable state exists.
   - Collision review: repository search found no production-path use of the selected backup kinds before this docs/PSTF update. The selected namespace therefore stays aligned with the Oracle plan and no alternative allocation was needed.

## Required decisions

1. **First vertical implementation slice**
   - Options: Kopia filesystem backup, Velero Kubernetes restore orchestration, or a native snapshot adapter such as ZFS/CSI.
   - Recommendation: start with Kopia filesystem backup plus signed verification provenance because it proves the generic adapter boundary without turning Bahia into a storage engine.

2. **Minimum verified-backup policy**
   - Decision needed: define which verification evidence is mandatory before Bahia can mark a backup as verified and restore-eligible.

## Escalation rationale

The remaining choices affect operator safety and restore reliability. They should not be silently resolved in code.
