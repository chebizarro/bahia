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

### What was checked
- Route gating config: `web/src/lib/auth/route-access.js`
- Route usage search for direct REST client calls (`api.*`) under `web/src/routes/**`

### Findings
- Protected route prefixes with direct REST client usage (`$lib/api/client.js`) are:
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
- Protected prefixes currently **without** direct REST client usage in route pages:
  - `/events`
  - `/souls`
- Route gating has now been updated so the direct-REST prefixes above are marked `requiresRestCompatibility` in `web/src/lib/auth/route-access.js`.

### Conclusion
- Compatibility gating now matches the currently known direct-REST protected surface.
- `/events` and `/souls` remain signer-only gated for now.
- Follow-up migration issues are required to move the REST-dependent route groups to Nostr/store-first flows so compatibility gating can be reduced over time.
