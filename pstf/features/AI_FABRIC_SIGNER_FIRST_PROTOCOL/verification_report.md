# Verification Report — AI_FABRIC_SIGNER_FIRST_PROTOCOL

## Summary

PSTF artifacts exist for `AI_FABRIC_SIGNER_FIRST_PROTOCOL` with acceptance criteria mapped to `docs/plans/bahia-ai-ml-inference-fabric-2026-05-16.md`. No product code or executable protocol verification is recorded in this PSTF slice.

This report is a **planning/contract artifact only**. It must not be read as evidence that the AI/ML signer-first protocol is production-ready.

Executable signer-first protocol verification is tracked by Bead `bahia-vn8o`.

## Commands Run

Historical artifact-creation commands recorded by the original verification slice:

- `bd show bahia-66ax.1.3`
- `bd update bahia-66ax.1.3 --claim` and child claim updates

Item 6 stale-verification cleanup commands:

- `bd prime`
- `bd update bahia-dg3t --claim`
- `bd search "signer-first AI ML"`, `bd search "AI_FABRIC_SIGNER_FIRST_PROTOCOL"`
- `bd create ...` for `bahia-vn8o`

## Acceptance Criteria Status

Draft criteria are mapped in `acceptance_criteria.json`. They remain unverified because the corresponding tests in `test_matrix.json` are placeholders with no executable evidence.

## Test Matrix Status

Placeholder tests are mapped one-to-one with criteria in `test_matrix.json`; executable tests are not implemented. No deterministic event-driven tests currently prove:

- AI/ML command/result events use the `38390-38399` namespace and avoid NIP-90 reserved ranges;
- AI/ML read models use `31980-31989` addressable/replaceable events with stable `d` tags;
- long-running AI/ML flows complete through correlated status/result events instead of HTTP polling;
- command/result events include required `e`, `p`, `status`, and scoped tags;
- existing LLM `597x`/`697x`/`797x`/`3196x` compatibility semantics stay isolated from generic AI semantics.

## Defects / Remaining Gaps

- `D-SFP-EXEC-001` / `bahia-vn8o`: executable signer-first AI/ML protocol verification is missing.

## Ambiguities / Human Decisions Needed

None for correcting the evidence classification. ML browser ingress was later moved to signer-first browser Nostr publishing by `bahia-gkg7` and is tracked in `BAHIA_NOSTR_AUDIT_PARITY` as `ml-nostr-controlplane`.

## Confidence Assessment

Medium for the completeness of the draft criteria mapping. Not established for implementation correctness or production readiness.

## Recommendation

Use these artifacts as PSTF gate inputs before protocol implementation, and require `bahia-vn8o` evidence before marking signer-first AI/ML protocol behavior verified.
