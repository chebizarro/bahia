# HITL Decisions — SECURITY_OSV_SBOM

## Decisions recorded

- 2026-06-14: Epic 1 freezes the Security Nostr contract on existing canonical mechanisms (`25910`, `30315`, `30900`, `30078`, `4903`, and existing SBOM `30004` availability lists). No new Bahia-specific Security event kind is allocated.

## Decisions required before or during implementation

- `SECURITY-HITL-001`: Confirm raw hydrated OSV vulnerability cache retention. The plan recommends a bounded cache such as 30 days, while normalized findings and scan summaries remain durable until a product retention policy says otherwise.
- `SECURITY-HITL-002`: Confirm expected steady-state scan volume before high-volume deployments rely on default table/index/cache sizing; add partitioning or retention design if operator volume exceeds the moderate-volume assumption in the plan.

## Blocker status

No Epic 1 blocker requires a human decision before implementation epics can begin. The decisions above are recorded so subsequent Beads epics do not silently guess retention or scale policy.
