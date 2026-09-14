# Legacy agent Soul adoption plan

This plan inventories running fleet agents that lack a trusted kind-`31951` Soul. It produces operator-review material only. It does not publish events, generate or alter keys, update access grants, change custody, or mutate Bahia services.

## Read-only evidence inputs

Prepare one sanitized `soulfactory-legacy-adoption-input/v1` JSON document from independently collected, read-only sources:

- `running_agents`: runtime inspection records with a stable `inventory_id`, `source_ref`, and explicit `running` state;
- `identity_records`: documented agent identity plus any existing managed pubkey, runtime binding, workspace, persona provenance, or public custody reference;
- `trusted_souls`: already validated kind-`31951` events, including their event ID, `d`-tag agent ID, agent `p`-tag pubkey, trust decision, and source reference.

Do not put private keys, `nsec` values, bunker URIs, client secrets, or other credentials in the input. `custody_ref` is for a stable, non-secret record identifier such as `signet-agent:donny`, not a connection URI.

The command performs no discovery itself. This keeps classification reproducible and prevents a report run from changing Docker, Bahia, Signet, relay, ACL, grant, or custody state.

## Deterministic matching policy

A candidate identity qualifies only when:

1. at least two explicit evidence fields match exactly after whitespace/case normalization;
2. at least one match is an identity anchor: `documented_agent_id`, `managed_pubkey`, or `custody_ref`; and
3. every field populated on both the runtime and identity record agrees.

The remaining eligible evidence fields are `runtime_binding`, `workspace`, and `persona_ref`. `display_name` and `container_name` are never identity evidence. A name-only or container-name-only similarity therefore produces `no_authoritative_match`. A container-name mismatch does not defeat otherwise consistent authoritative evidence.

Input records and output classifications are sorted by stable identifiers. Reports contain no clock-derived fields.

## Classification and refusal behavior

| Status | Reason code | Operator meaning |
| --- | --- | --- |
| `no_match` | `no_authoritative_match` | Gather better read-only evidence; do not adopt. |
| `operator_review_required` | `single_authoritative_match_requires_operator_approval` | Review the preserved identity fields and explicitly approve the named identity and exact kind-`31951` creation action in a separate execution task. |
| `already_trusted` | `trusted_soul_exists` | Do not re-adopt or replace the identity. |
| `ambiguous` | `multiple_authoritative_matches` | Stop; candidate ownership is ambiguous. |
| `ambiguous` | `trusted_soul_identity_conflict` | Stop; trusted Soul identity evidence conflicts or is duplicated. |

If any row is ambiguous, the CLI still emits the complete read-only report for review, prints a refusal message, and exits `3`. No ambiguous row contains an adoption plan.

## Operator-reviewed execution boundary

Run the report:

```bash
go run ./cmd/soulfactory-legacy-adoption-report -input <sanitized-inventory.json> > adoption-report.json
```

For each `operator_review_required` row, the operator must explicitly name the agent and authorize `authorize_create_one_canonical_kind_31951_soul`. That later, separately authorized workflow must preserve the reported managed pubkey, runtime binding, workspace/persona provenance, and custody reference, and must first recheck that no trusted Soul exists.

The report explicitly prohibits key generation, rotation, revocation, identity replacement, re-adoption, ACL/grant changes, custody changes, and Bahia service mutation. This task provides no execution command for any of those actions and no kind-`31951` publisher.
