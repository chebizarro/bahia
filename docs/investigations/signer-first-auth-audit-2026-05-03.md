# Signer-first auth follow-up audit (2026-05-03)

Issue: `bahia-4tw4.5`

## Scope
- lowercase tenant pubkeys in storage
- remaining routes that still require REST compatibility

## 1) Tenant pubkey lowercase invariant audit

### What was checked
- `internal/api/handlers/tenants.go`
- `internal/repository/pg_tenant.go`
- `internal/domain/tenant.go`

### Findings
- `CreateOrg` normalizes principal pubkey to lowercase before write.
- Other tenant write paths currently accept/request pubkeys without explicit lowercase normalization in handler/repository write boundaries:
  - `AddMember` uses `req.Pubkey` directly.
  - `CreateInvite` uses `req.Pubkey` and `p.PubKey` directly.
  - `AcceptInvite` uses `p.PubKey` directly.
  - `pg_tenant.go` writes whatever pubkey string is passed.
- Domain comments do not currently state canonical lowercase invariant.

### Conclusion
- Lowercase invariant is **not fully enforced at write boundaries**.
- A follow-up implementation issue is required to normalize tenant pubkeys before persistence and to document the invariant.

### Data migration status
- Read-only audit executed against local compose Postgres (`docker compose exec -T postgres psql -U bahia -d bahia ...`) on 2026-05-03.

Exact query set:
```sql
SELECT COUNT(*) AS mixed_owner_pubkeys
FROM organizations
WHERE owner_pubkey <> lower(owner_pubkey);

SELECT COUNT(*) AS mixed_member_pubkeys
FROM org_members
WHERE pubkey <> lower(pubkey);

SELECT COUNT(*) AS mixed_invite_pubkeys
FROM org_invites
WHERE pubkey <> lower(pubkey)
   OR invited_by <> lower(invited_by);
```

Result:
```text
mixed_owner_pubkeys: 0
mixed_member_pubkeys: 0
mixed_invite_pubkeys: 0
```

Conclusion:
- No mixed-case rows found in audited tables.
- No follow-up data-normalization migration bead is required from this audit run.

## 2) Remaining REST compatibility route audit

### Superseded status
This section is retained as historical investigation evidence only. The route-level REST compatibility conclusion from this 2026-05-03 audit was superseded by `TRANSPORT_POLICY_GOVERNANCE` decision `HITL-TRANSPORT_POLICY_GOVERNANCE-001`.

Current approved browser policy:
- `web/src/lib/auth/route-access.js` intentionally keeps `ROUTE_COMPATIBILITY_REQUIREMENTS` empty by default.
- Protected browser routes may still require authentication or role checks, but they do not require blanket route-prefix REST compatibility gating.
- Any future route-level REST compatibility exception must be an explicit product decision, not an inferred consequence of protected-route prefix or direct REST usage discovered in this historical audit.

### Historical findings from 2026-05-03
- Protected route prefixes with direct REST client usage (`$lib/api/client.js`) observed during the audit were:
  - `/artifacts`
  - `/deployments`
  - `/environments`
  - `/notifications`
  - `/orgs`
  - `/payments`
  - `/policies`
  - `/services`
  - `/settings`
  - `/workers`
- Protected prefixes observed **without** direct REST client usage in route pages were:
  - `/events`
  - `/souls`

### Current conclusion
- The historical recommendation to classify direct-REST protected prefixes through route-level compatibility gating is no longer the intended policy.
- The approved `TRANSPORT_POLICY_GOVERNANCE` policy treats the empty route-level REST compatibility map as intentional browser behavior.
- Remaining browser REST transport drift should be tracked as feature-specific migration or compatibility work, not by reviving blanket route-prefix gating.
