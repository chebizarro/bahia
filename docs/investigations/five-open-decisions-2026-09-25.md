# Investigation: Five open architecture/product decisions in the Bahia backlog

## Summary
Five backlog items surfaced by the 2026-09-25 reconciliation sweep are blocked on a
decision rather than on engineering. This report establishes the evidence needed to
make each call, and recommends a direction for each.

## The five decisions

| Issue | Decision to make |
|---|---|
| `bahia-gb14q` | Must migration filename prefixes be globally unique (external-runner compatibility), or should full-stem version semantics be formally documented and test-enforced? |
| `bahia-185t0` | Control Plane fold breaks same-version ties by inner rumor id without resolving CORD-04 authority. Accept and spec-record the bounded staff-griefer divergence, or implement owner-rooted Roles/Grants roster folding? |
| `bahia-02f15` | Keep DNS zone CRUD fleet-owned with organization-scoped route claims, or unify with the public-route model and make canonical DNS zones tenant-owned? |
| `bahia-5teth` | Which second managed public-route provider, for what concrete use case, and with what DNS/ingress/TLS contract? |
| `bahia-1qkfk` | Is batch plan review/editing still a supported product feature? If not, retire the legacy planner; if yes, rebuild it over a single turn-engine interface. |

## Symptoms
- Each item has sat open without progress because the engineering question is downstream of an unanswered product/architecture question.
- Two (`185t0`, `02f15`) risk being "fixed" in a direction that contradicts an existing model elsewhere in the codebase.
- One (`gb14q`) looks like a defect but may be a documentation gap, since Bahia's own runner is unaffected.

## Background / Prior Research

Four explore agents gathered facts outside the Bahia Go source. Summary of each, with the
evidence that bears on the decision.

### B1. `gb14q` — external migration runners DO key on the numeric prefix

All three mainstream Go runners identify a migration by the **leading numeric version**, not
the full filename stem that Bahia's own runner uses:

- **`golang-migrate/migrate`** parses `numericVersion_identifier.direction.extension`; a second
  file with the same numeric version *and* direction is rejected with `ErrDuplicateMigration`.
  It errors hard — it does **not** silently pick one.
- **`pressly/goose`** takes the text before the first underscore as an `int64` version and
  **panics** on duplicates (`goose: duplicate version ... detected`).
- **`amacneil/dbmate`** captures leading digits as the version and stores it as the
  `schema_migrations.version` primary key, so duplicates collide on insert.

Important qualifier: **goose and dbmate are independently incompatible** with Bahia's separate
`.up.sql`/`.down.sql` convention (goose treats each as its own migration; dbmate expects combined
`-- migrate:up`/`-- migrate:down` files). So `golang-migrate` is the *only* realistic external
runner — and it is precisely the one that hard-errors on the seven duplicate prefix groups.

**Bearing on the decision:** the hazard is real but bounded and loud. It does not corrupt
anything today; it means *adopting `golang-migrate` is impossible without renumbering first*.

### B2. `185t0` — CORD-04 is normatively specified, and it prescribes the answer

CORD-04 exists upstream at `concord-protocol/concord/04.md`, and Cascadia names the CORD
documents the **"sole normative authority"** (`cascadia-nips/nips/NIP-CAS-0008.md:7-16`,
classifying CORD-04 as upstream-normative at `:25-36`).

CORD-04 §1 prescribes, for equal versions: **"authority first, then the lower rumor id"**
(`04.md:72-73`). Critically, "authority first" is a **filter, not a ranking**:

- resolve each signer against the owner-rooted roster (owner's authority derives from
  `community_id`; Roles/Grants resolve outward from that root, `04.md:91-93`);
- **drop** editions whose signer lacks authorization;
- **if both surviving editions are authorized, the lowest rumor id wins.**

The spec does *not* say the higher-ranked of two authorized actors wins. Rank determines whether
an action qualifies at all (`04.md:112-113,134-138`).

It also requires **fail-closed deferral** when authority cannot be resolved: an action whose
`vac`-cited Grant edition is absent, mismatched, forged or forked stays parked and must not be
enforced; a client may render what it can read but may not act on unresolved authority
(`04.md:49-50,139-141`, chain gaps at `:74-76`).

**Bearing on the decision:** Bahia's lowest-rumor-id tie-break is *correct* — it is the second
half of the rule. What is missing is the first half: authorization filtering before the tie-break.
So this is not "accept divergence vs. build roster folding"; it is "comply with a normative spec,
or knowingly diverge from one Cascadia declares binding."

### B3. `02f15` — no product requirement for per-tenant DNS zones exists

Extensive cross-repo search found **no** evidence that per-tenant/per-org DNS zones are required.
The documented model is the opposite:

- The originating audit classifies DNS as a **"fleet/global-scoped resource where tenant RBAC is
  the wrong model"** and recommends **operator-level authorization**
  (`fleet-planning/docs/audits/2026-09-08-bahia-encrypted-handler-tenant-rbac-audit.md:22-27,152-160`).
  It states zone ownership is *"only worth doing if per-tenant zones are actually a product
  requirement"* (`:207-216`) — the same conditional that became this issue.
- The DNS design treats zones as a **derived projection of infrastructure**, scoped by network
  (`internal`/`external`/`edge`), with no organization owner
  (`bahia/docs/designs/dns-orchestration-layer.md:14-25,190-203,631-639`).
- The org boundary already exists one layer up: `allowed_org_ids` **"limits which organizations
  may claim the zone"** — claim authorization *within* an operator-configured zone, not ownership
  of it (`docs/user-guide/guides/managed-dns-and-https-routes.md:43-58`).
- `docs/user-guide/features/organizations.md:110-146` enumerates org-scoped resources — services,
  environments, artifacts, deployments, policies, notifications. **DNS zones are absent.**
- Customer-supplied domains are explicitly *future* product surface, not current requirement
  (`fleet-planning/docs/investigations/ownauth-vs-workos-2026-08-29.md:87-96,136-153`).

**Bearing on the decision:** the precondition the issue sets for itself is unmet.

### B4a. `5teth` — filed speculatively; no driver, no named provider

- Created by commit `2ce6bf5d` (2026-08-02) **in the same commit that implemented the first
  Cloudflare provider**, marked `discovered-from: bahia-75pt0`. Original wording: *"Extend the
  provider-agnostic public route Backend/Resolver with an nginx or alternate ingress-controller
  API adapter."* That is the only recorded motivation — exercise the new abstraction.
- An independent review 43 minutes later treated it as optional: *"Fine. Cloudflare is the only
  production provider and the abstraction is in place."* (`2951f8d4`).
- `650657d4` later added nginx, but as the **internal** HTTPS stage composed after Cloudflare
  (`internal/adapters/routing/composite.go:13-26`), not a second public provider.
- **No** recorded Cloudflare rate-limit, outage, cost, account or customer constraint. The one
  real outage (`git.sharegap.net` 502) was caused by **nginx caching a stale Docker upstream IP**,
  not Cloudflare. Other Cloudflare-path defects were fixed in place (`9911ddb8`, `bahia-coj73`).

### B4b. `1qkfk` — legacy planner retained for rollout compatibility only; no decision ever made

- `fb1e7109` (2026-05-16) introduced the whole-plan "plan-and-approve" workflow; `98cdd99b`
  deliberately added `ModifiedPlan` editing (*"operators can remove, reorder, and edit tool args
  before approving"*), implementing a planned Phase 1.5 follow-up.
- `e83cde6a` + `a48c88cd` (2026-07-02) added the agent loop and the production branch between the
  two engines. Acceptance explicitly required *"legacy plan path still works with flag off"* —
  i.e. preserved for **rollout compatibility**.
- `3729c55e` (2026-07-03) made agentic the default but **did not remove** the flag-off planner.
- Since then the legacy planner has had exactly **one** behavioural change (`ad1f929d`, streaming
  toggle) and two mechanical ones (`c1cceb70`, `eaba411a`).
- The UI still ships batch plan editing (`web/src/lib/components/assistant/AssistantPlanApproval.svelte:52-94,104-141`)
  with a unit test asserting plan-review rendering.
- **No** commit, design doc or test ever declared `ModifiedPlan` either permanently supported or
  scheduled for retirement. The only retirement language is the Sept 19 slop-audit that created
  this very issue.

## Investigator Findings: CORD-04 authority feasibility (`bahia-185t0`)

### Scope and bottom line

Investigated 2026-09-25 at Bahia `master`, commit
`48e95424c6c37f850939a34e6b1461022312a240`, intentionally 17 commits ahead of
`origin/master`. B2's normative interpretation is taken as established, not
re-derived. All source references below are relative to Bahia. Assistant source
and UI were excluded. This is source/test evidence, not an inspection of a live
community's relay history or proof that deployed communities have complete rosters.

**Feasible, but not a tie-break patch.** Bahia already has the decryption material,
signed payload bytes, and validated owner identity needed to implement a resolver.
It lacks typed roster interpretation, exact-citation resolution, and authorization
of candidates before selecting heads. Estimate **10–20 engineer-days** for a tested,
reviewed implementation on complete input, versus **1–3 days** for a conservative
refusal-first interim. These are engineering estimates, not measured delivery times;
recovering already-missing historical authority evidence is an additional availability
problem, not something a parser can solve.

**Do not accept the current behavior as a bounded display/tie divergence.** The
unfiltered heads are used to authorize rotations by citation and to seed the next
epoch through compaction. This is durable propagation when that API is invoked,
although no in-tree caller of the exported rotation entry point was found in the
searched, non-assistant production scope.

### Verification of the four claims

**1. `vac` is not extracted — verified; “the evidence is discarded” needs correction.**

- `parseConcordControlEdition` extracts `eid`, `vsk`, `ev`, and `ep`, checks version/link
  shape, and hashes `rumor.Content`; it never reads or validates incoming `vac`
  (`internal/soulfactory/concord_control.go:320-363`). The edition struct has actor,
  identity, version/link/hash and seal fields, but no parsed citation
  (`concord_control.go:85-99`). `concordAuthorityCitation` and its `tag()` method exist
  for **outgoing** citations (`concord_control.go:56-75`); their existence is not an
  incoming authorization check.
- The evidence is **not irretrievably lost**: the embedded rumor is decoded from
  `seal.Content`, and the complete parsed seal is retained in the edition
  (`concord_control.go:307-310,353-363`). The original rumor JSON string, including its
  tags and content, therefore remains accessible through `edition.seal.Content` for
  retained heads. Precision: Bahia retains the signature-covered content string,
  not the original outer seal JSON serialization; compaction calls `seal.String()`
  (`internal/soulfactory/concord_compaction.go:130-141`).
- Cost implication: parse and retain a strictly validated citation alongside the
  original signed bytes; no new encryption scheme or wire field is needed. However,
  the returned fold retains **only heads**, not the full candidate/history index
  (`concord_control.go:108-115,188-189,225-241`), so exact older cited editions need
  an explicit representation during authority resolution.

**2. Role/Grant bodies are opaque — verified; unavailable/decryption-blocked — refuted.**

- The outer gift wrap is ID/signature-checked and decrypted using the Control read
  key (`concord_control.go:192-210`). The inner kind-20014 seal is required to be
  plaintext, its signature is checked, and its rumor author must match the seal
  author (`concord_control.go:292-318`). `rumor.Content` is already available as a
  plaintext string when hashed (`concord_control.go:349`). There is no additional
  Role/Grant ciphertext layer in this path.
- Neither `vsk` dispatch nor typed Role/Grant body parsing occurs: `vsk` is accepted
  as a number and stored, while content is only hashed (`concord_control.go:324-363`).
  The struct has no rank, permission, member, or role-reference representation
  (`concord_control.go:85-99`). Indeed, this parser does not even validate that the
  body string is JSON, although the test builders generally supply JSON.
- The parallel repository search found no Concord roster interpreter outside this
  code. Spot-checks found `role_ids` only in the Control test payloads
  (`internal/soulfactory/concord_control_test.go:22,546`), not in production Go.
  Searches of the two relevant active dependencies (`go.mod:7-8`: `fiatjaf.com/nostr`
  and `cascadia-go`) likewise found no Concord/`role_ids` implementation. This is a
  bounded source-search result, not a claim about unrelated external clients.
- Cost implication: implement typed Role/Grant decoding and authority semantics;
  do not budget a new key-distribution or decryption subsystem. Preserve raw content
  for hashes/signatures rather than hashing reserialized typed objects.

**3. Rotation finds a Grant-coordinate head without proving its authority or permission — verified.**

- `resolveConcordRotationAuthority` recognizes the owner locally; otherwise it
  fetches the fold, derives the rotator's Grant coordinate, checks `fold.head(eid)`,
  and returns that head's coordinate/version/hash
  (`concord_control.go:446-483`). `head()` checks suspension/existence only
  (`concord_control.go:118-127`).
- It does **not** require that head to have the Grant `vsk`, bind the payload's member
  to the rotator, resolve its Roles, authorize the Grant's signer, or check the
  permission/rank needed for the requested operation. Its arguments do not include
  the requested channel scopes; the boolean only controls whether a fold is needed.
- Consequently, the comment that an “unauthorized Rotator never reaches custody”
  overstates the implementation (`internal/soulfactory/concord_rotation.go:171-179`).
  A structurally valid edition at the coordinate suffices, not an authorized Grant.
  This still requires accepted Control-wrap provenance and a valid inner signature
  (`concord_control.go:192-210,304-318`): it is not permission for arbitrary relay
  users to forge the Control address or the owner's signature.

**4. Validated owner/`community_id` exists but does not reach the fold — verified.**

- Invite validation matches the configured community ID, validates owner/salt, and
  recomputes their self-certifying community ID
  (`internal/soulfactory/concord_invite.go:330-340,402-412`). The typed bundle already
  contains all three fields (`concord_invite.go:110-116`). The validated result
  retains the complete raw bundle, even though it has no separate typed owner field
  (`concord_invite.go:102-108,394-399`). The trust anchor is the configured community
  identity plus this validation, not a relay-provided owner assertion.
- Custody stores the invite bundle, and sealed custody decrypts it per operation
  (`internal/soulfactory/concord_custody.go:24-38,113-130`). `source.resolve` validates
  loaded material; rotation then decodes `current.bundle`
  (`concord_invite.go:73-99`; `concord_rotation.go:158-165`). Rotation preserves bundle
  fields and revalidates before storing the new material
  (`concord_rotation.go:285-294,466-477,193-204`).
- The fetcher has `community_id`, root, and decoded bundle in hand but calls
  `foldConcordControlPlane(events, address, read.ConversationKey)`
  (`concord_control.go:398-429`). The fold signature receives neither owner nor
  community identity (`concord_control.go:184`). Owner comparison already works
  immediately outside it (`concord_control.go:453-466`).
- Cost implication: local context plumbing from an already-validated bundle, not
  owner discovery, a new persisted secret, or a protocol change.

### Consequence A: authorization must precede head selection generally

**Yes — every candidate must be eligible, not merely same-version twins.** The
current implementation sorts by increasing version, discards all but the lowest
rumor ID at each version, checks adjacent chain links, and returns the highest
version (`concord_control.go:261-289`). No actor/permission decision occurs anywhere
in that selection.

A concrete source-derived counterexample is an authorized v1 followed by a validly
signed but unauthorized v2 with `ep = hash(v1)`: v2 passes the existing chain check
and wins. An unauthorized higher version across a gap can also win because gaps
are deliberately permitted (`concord_control.go:275-289`). These are deductions
from the code, not newly executed adversarial tests. An unauthorized non-linking
adjacent edition can instead suspend the entity and block compaction.

The correct ordering is authorization eligibility first, then the existing
version/chain/tie rules over eligible candidates. Two **authorized** equal-version
editions still use the lower rumor ID; do not invent a rank-based winner. Unknown
or unresolved authority must remain distinguishable from proven unauthorized
input: parking it is not silently treating it as absent or a usable head.

### Consequence B: the fold directly feeds durable compaction

The production path is explicit:

1. `foldConcordEntity` returns the selected head; `foldConcordControlPlane` installs
   it in `fold.heads` (`concord_control.go:233-240,289`).
2. `Rotate` obtains that fold and checks compactability before planning
   (`concord_rotation.go:179-189`). The compactability guard rejects only nil folds
   and suspended chains, not unresolved authority (`concord_rotation.go:81-88`).
3. Rotation stores new custody and publishes rekeys, then passes the same fold into
   `republishConcordCompaction` (`concord_rotation.go:203-230`).
4. Compaction enumerates **every selected head**, rewraps its retained signed seal
   under the new Control address/read key, and publishes it
   (`concord_compaction.go:89-125,130-157`). No authorization filter intervenes.
5. A subsequent fresh fold accepts a head without its old ancestors
   (`concord_control.go:246-252,275-289`). The existing round-trip compaction test
   explicitly proves that only heads cross epochs and that original author/hash
   survive (`internal/soulfactory/concord_compaction_test.go:66-106`).

Thus an unauthorized selected edition **can be carried across epochs**, while the
legitimate losing edition is not copied into the new plane. Compaction does not
make its signature authoritative—a compliant receiver can still reject/park it—but
it propagates the bad selection and may deprive fresh readers of the competing
valid state. No deletion of old relay events is shown here; the loss is from the
new epoch's published head set. Channel-only rotation does not compact, but can
still mint/persist keys and emit a citation based on an ineligible Grant.

**Reachability qualification:** outside `concord_*.go`, the observed production
rotation surface is `FullProvisioner.RotateConcordCommunity`
(`internal/soulfactory/provisioner_full.go:885-914`). Exact symbol searches found no
in-tree caller in the non-assistant production scope. Fold heads themselves are
private, not an API/UI projection. Ordinary governed provisioning calls `Assign`
(`internal/soulfactory/governed_provisioning_production.go:845-850`), whose path
resolves bundles and sends invites without folding
(`concord_invite.go:195-241`). Therefore this investigation demonstrates an unsafe
implemented rotation path, **not an observed ongoing production corruption event**.
It also makes a refusal-first gate comparatively contained.

### Data availability: enough to implement, not enough to promise every operation can proceed

A successfully decrypted current-epoch edition supplies its signer, payload, chain
identity, and any original `vac`; trusted owner context is available locally. That
is sufficient to attempt owner-rooted resolution without new wire data. However:

- Fetching reads only the current held Control address/read key
  (`concord_control.go:403-429`); the fold returns heads, not an exact-edition archive
  (`concord_control.go:108-115,233-241`).
- Compaction preserves head seals, including their unchanged original citations,
  while omitting non-head ancestors (`concord_compaction.go:107-141`). A surviving
  edition may cite a Grant version that is not itself among the republished heads.
  That is a concrete possible missing-dependency case; no deployed instance of it
  was checked in this investigation.
- The custody document contains bundle/control-root material, not authority history
  (`concord_custody.go:24-38`). Nothing in the examined fold path retrieves an exact
  missing Grant edition from a prior epoch or persists a verified roster archive.

A resolver therefore needs an exact-citation index over available candidates,
explicit authorized/unauthorized/unresolved outcomes, and dependency/cycle handling.
A current Grant head alone cannot substitute for an absent exact citation, nor does
possessing a cited edition alone prove present permission. Cross-epoch evidence
availability must be validated against real fixtures and the compaction contract;
if the required evidence cannot be obtained, the safe result is refusal/deferral,
not inferred authority. An always-available migration of already-pruned communities
cannot be promised from the evidence Bahia has today.

### What existing tests assume, and what real fixtures need

- The base fold fixture derives keys from a constant community ID, with no
  owner-root binding or roster (`concord_control_test.go:280-296`). Its builder
  stamps every rumor `vsk=3`, accepts arbitrary content, and adds no `vac`
  (`concord_control_test.go:306-329`). The chained-head test uses `{"v":1}` etc.
  under a fresh arbitrary signer (`concord_control_test.go:122-149`). These test
  mechanics/provenance, not Grant semantics or authorization.
- `concordTestGrantEdition` names a repeated `a1` Role ID without constructing its
  definition (`concord_control_test.go:536-547`). The successful non-owner rotation
  test supplies only two owner-signed Grant editions, no Role
  (`concord_control_test.go:392-413`). The compaction acceptance test likewise seeds
  those Grants plus arbitrary metadata (`concord_compaction_test.go:17-38`).
- The rotation bundle fixture **does** correctly bind owner/salt/community ID and
  can be reused (`internal/soulfactory/concord_rotation_test.go:478-503`). Existing
  no-Grant and fork-abort tests already establish useful no-mutation assertions
  (`concord_control_test.go:433-466`; `concord_compaction_test.go:115-140`).

Real authorization fixtures need a self-certified owner community; typed Role
editions with actual rank/permissions; member-bound Grants at derived coordinates;
owner-signed bootstrap and delegated chains; and exact `vac` edition pins for
non-owner actions. Add cases for unauthorized equal- and higher-version editions;
two authorized ties with different ranks; wrong Grant member/type/coordinate;
missing, mismatched, forged, forked and cyclic dependencies; insufficient permission
and demotion/revocation; out-of-order arrival/replay and deterministic convergence;
and missing old citations after compaction. Round-trip tests must prove that only
eligible heads survive refounding and that unresolved authority causes **no custody
write, epoch/key change, or publication**. Keep hash/provenance tests at their own
layer rather than pretending arbitrary `vsk=3` JSON is an authorized Grant.

### Implementation size and staged compliance judgement

Estimated work for one engineer familiar with this code, including focused tests
and review; rows are a sizing breakdown, not new tracked tasks:

| Work | Estimated engineer-days | Main reason |
|---|---:|---|
| Typed payload/citation parsing, trusted context plumbing, candidate indices | 1–2 | Wire bytes and owner binding already exist; preserve exact signed bytes. |
| Deterministic owner-rooted Role/Grant resolver and per-action eligibility | 4–7 | Transitive dependencies, current roster/rank semantics, revocation, cycles, forks and exact citations; not a sort comparator. |
| Fold/rotation/compaction integration and explicit unresolved-state propagation | 1–3 | Filter all head candidates, check the requested operation/scopes, abort before mutation; do not compact a silently partial authorized projection. |
| Real fixtures, negative/permutation tests, cross-epoch and no-side-effect proofs | 3–5 | Existing tests intentionally bypass roster semantics. |
| Security/spec review and compatibility/availability checks | 1–3 | Especially historical citations and already-compacted input. |
| **Total on complete, supported input** | **10–20** | Roughly 2–4 engineer-weeks; historical recovery/backend work is not included. |

The main changes are localized to `internal/soulfactory/concord_control.go`,
rotation/compaction boundaries, a new internal authority implementation, and their
fixtures. No assistant integration, public UI, or new cryptography is intrinsically
required. Once-only trust plumbing/parsing is small; correct roster semantics and
proofs dominate. Input is already capped at 20,000 editions
(`concord_control.go:49-54,221-224`); reuse that boundary with indexed, bounded
dependency resolution rather than repeated relay polling or unbounded rescans.

**Staged compliance is viable as explicit feature refusal, not as acceptance of the
divergence.** A conservative **1–3 engineer-day** interim would reject all non-owner
rotations and all refoundings until their authority/compaction prerequisites can be
proved. Keep the independently justified owner channel-only rekey path
(`concord_control.go:453-458`); owner status alone must not bypass validation of
other authors' heads during refounding. Put the refusal before planning/custody/
publish at the existing preflight boundary (`concord_rotation.go:179-205`), with
clear unsupported/unresolved errors and tests. This deliberately sacrifices those
operations' availability, rather than trusting a coordinate lookup or copying a
partial plane. It does not newly certify the separate invitation path's CORD
compliance, which is outside this question.

Later stages can re-enable operations only when the resolver proves their full
required authority; unresolved dependencies stay parked, and proven unauthorized
editions are excluded. Simply changing tie handling, emitting a warning, or
filtering immediately before publication **after custody has changed** is not a
safe interim.

### Verification performed

- Three narrow read-only explore probes covered typed payload parsing/dependencies,
  trust-root flow, and external consumers. Two initial probe launches failed before
  becoming ready and were retried; all final results were collected. Load-bearing
  findings were spot-checked against source and exact-symbol searches.
- Focused existing suite **passed**: cached Go 1.26.3 with
  `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off`, running
  `go test -mod=readonly ./internal/soulfactory -run '^TestConcord' -count=1`
  (`ok`, 0.912s). The default local Go 1.26.2 was too old; an auto-toolchain attempt
  with the checksum database disabled also failed, so the already-installed 1.26.3
  binary was invoked directly. No dependency fetch was needed. These passes do not
  constitute authorization coverage, for the fixture reasons above.
- Investigation tooling: Jev `find-lines`, `filter-search`, and `rank-files` narrowed
  reads; `verify-claim` supported the trust-root check but returned inconclusive
  results for two compound claims, which were verified directly from source.
  Parent-session totals: 22 requests, 117,282 input tokens, $0.004926 reported cost
  (probe usage not aggregated). Its relevance scores were navigation aids, not
  evidence of correctness.
- No source, tests, tracker state, commits, or remote refs were changed. Only this
  investigator section was appended; no assistant-area investigation was performed.


## Investigator Findings: legacy planner blast radius (`bahia-1qkfk`)
<!-- Pair B appends here -->

### Scope and bottom line

Inspected local `master` at `48e95424`, intentionally 17 commits ahead of
`origin/master`, on 2026-09-25. This is a source-grounded reachability investigation,
not evidence of live deployment configuration or operator usage. B4b's established
history is accepted rather than re-derived. No source, tracker, deployment, Git ref,
or index was changed; `internal/soulfactory/concord_*` was excluded.

**Batch plan review/editing is reachable shipped functionality, but not the default
assistant workflow.** New legacy plans require deliberate selection of a documented
configuration escape hatch. There is no browser/per-request engine switch. However,
existing legacy plan approvals are not gated by that switch: turning agentic mode
back on does not retire outstanding plans. The editor is mounted by the normal app,
not an orphan component. This proves an available compatibility capability, not a
permanent product commitment or current usage.

**Retirement is a bounded cross-layer compatibility change, not a database-schema
migration.** It is materially cheaper than maintaining two engines or genuinely
unifying them, but not equivalent to deleting the editor or its containing files.
Cancellation, old executable state, and shared runtime helpers are the main traps.

### Four initial claims: verdicts and evidence

1. **VERIFIED, with a persistence qualification — no assistant-specific SQL migration
   is required by retirement.** `AssistantSession` is JSON content for schema
   `bahia.assistant-session.v1` on kind 30900, containing plan fields and generic
   metadata (`internal/domain/assistant.go:78-97`). `publishSession` marshals the
   whole session and signs/publishes that projection
   (`internal/service/assistant_orchestrator.go:1002-1023`). Production supplies an
   `auditedNostrPublisher` backed by the generic event repository
   (`internal/app/app.go:1338-1339,1368-1378`); after successful relay publication it
   records the event's unchanged content (`internal/app/app.go:3458-3473`). The
   PostgreSQL repository inserts into `nostr_events`
   (`internal/repository/pg_nostr_event.go:109-121`), whose schema has generic
   `kind`, `content TEXT`, and `tags JSONB`, not assistant columns
   (`internal/db/migrations/000001_init.up.sql:146-158`). Searching all 160 SQL files
   under `internal/db/migrations` found no `assistant` declaration/reference.
   Startup reads generic kind records and JSON-decodes `AssistantSession`
   (`internal/app/app.go:3941-3977`; ordering at
   `internal/repository/pg_nostr_event.go:209-218`).

   Qualification: this is not *only* local SQL state. Relay projections are the
   published contract; SQL auditing is conditional on successful publication, with
   recording failures logged. Startup also queries relay history through EOSE
   (`internal/service/assistant_session_recovery.go:87-137`). Retirement must handle
   old JSON on relays, in the event log, and in browser caches. No DDL is necessary;
   an explicit execution-state transition/read-compatibility policy still is. The
   startup loader's 500-event limit is not a census of all historical sessions
   (`internal/app/app.go:3951-3975`).

2. **VERIFIED — `internal/mcp/agent_async_tools.go` is shared, not deletable legacy
   infrastructure.** The legacy dispatcher invokes `InvokeAssistantAsyncTool`
   (`internal/service/assistant_orchestrator.go:434-454`). The agentic runtime
   invokes the same method (`internal/service/assistant_tool_runtime.go:346-377`),
   through the production adapter delegating to the same MCP server
   (`internal/app/app.go:1438-1444,3746-3750`). Its tool definitions and command
   publishers remain live (`internal/mcp/agent_async_tools.go:13-34,39-60`).
   `AsyncToolReceipt` is likewise shared
   (`internal/domain/assistant.go:175-188`).

   There is a second easily missed shared dependency: the agentic observer adapter
   delegates to `AssistantOrchestrator.observeDownstreamResult`
   (`internal/service/assistant_tool_runtime.go:654-658`). Preserve the observer,
   receipt/result correlation, terminal parsing, and subscription infrastructure
   (`internal/service/assistant_orchestrator.go:668-779`), not just the MCP file.

3. **VERIFIED — composer cancellation depends on a legacy hash; agentic cancellation
   needs a defined contract.** The button is shown for `executing`/`blocked`, but
   `cancelSession` silently returns without `lastPlanHash` or
   `currentPlan.plan_hash`, otherwise sends plan-hash `decision: cancel`
   (`web/src/lib/components/assistant/AssistantComposer.svelte:23-25,63-80`). A fresh
   agentic turn writes loop metadata, not a new plan/hash
   (`internal/service/assistant_agent_loop.go:224-254`). Thus the visible button is
   not proof of working cancellation for fresh agentic sessions.

   Both the transport handler and orchestrator require a plan hash or action ID
   (`internal/controlplane/assistant_handlers.go:44-57`;
   `internal/service/assistant_orchestrator.go:318-330`). The legacy cancel branch
   cancels the local observer, marks the session failed, clears pending steps, and
   explicitly does no downstream rollback
   (`internal/service/assistant_orchestrator.go:361-372`). Agentic action cancellation
   is not equivalent: the backend treats `cancel` like rejecting one deferred action
   and continues the model loop (`internal/service/assistant_agent_loop.go:315-375`);
   the browser action publisher only allows approve/reject
   (`web/src/lib/stores/assistant.svelte.js:735-740,780-783`). `CancelScope` fields do
   not supply an implemented run-stop operation: the orchestrator action-decision
   call forwards session/action/decision/reason, not the request's cancel scope
   (`internal/service/assistant_orchestrator.go:539-549`). Define run/session stop
   versus action rejection, persisted cancellation, outstanding-receipt handling,
   and the no-rollback boundary before removing the hash-dependent path.

4. **VERIFIED — recovery consumes receipts but cannot drain undispatched later
   steps.** Legacy recovery only enters for `executing`/`blocked` sessions with
   pending steps. It takes the first pending step; if no receipt exists, it writes
   `blocked` and returns (`internal/service/assistant_session_recovery.go:150-191`).
   Otherwise it backfills/observes the receipt, removes the completed step, and
   repeats (`:196-224`). It never dispatches a missing-receipt step. The normal
   approval loop dispatches sequentially, waiting before moving to the next step
   (`internal/service/assistant_orchestrator.go:434-493`), so a restart during step 1
   of a multi-step plan naturally leaves later steps without receipts. Recovery
   also does not execute `awaiting_approval` plans (`assistant_session_recovery.go:160-162`).
   "Let recovery drain" is therefore incomplete even if existing receipt observation
   is retained. Agentic recovery is a different branch: waiting-async metadata
   resumes through the loop (`assistant_session_recovery.go:227-261`).

### Exact reachability: config, persisted state, and shipped UI

**Configuration selection**

- The subsystem itself defaults **off**; when enabled, its agentic flag defaults
  **true** (`internal/config/config.go:1288-1299`). The real key is
  `assistant.agentic.enabled`, not a per-request `agentic_enabled` field.
- YAML can set the flag false. Loading starts with defaults, loads YAML, then loads
  `BAHIA_` environment values, so env overrides YAML
  (`internal/config/config.go:1417-1438`).
  `BAHIA_ASSISTANT_AGENTIC_ENABLED=false` maps explicitly to the nested key
  (`:1457-1459`); double-underscore nesting is also accepted (`:1442-1444`).
  The false setting is deliberately documented as selecting the legacy planner
  (`docs/user-guide/getting-started.md:74-76,115-139`).
- Flag-off remains a validated production configuration: an enabled assistant
  requires `assistant.llm_model` in that mode and the usual valid provider URL and
  service key (`internal/config/config.go:2311-2325`). Production copies the flag
  into the orchestrator (`internal/app/app.go:1368-1380`), whose prompt entry branches
  directly between the agent loop and legacy planning
  (`internal/service/assistant_orchestrator.go:229-252`). A missing agent loop fails
  rather than automatically falling back (`:503-508`).
- Changes can take effect via process restart or config reload/application
  reconstruction: SIGHUP reloads configuration and replaces the application
  (`cmd/server/main.go:105-133`); assistant changes are not handled by the limited
  in-place reloader (`internal/app/app.go:1892-1908`). Editing mounted YAML is a real
  reload path. Changing a deployment environment variable normally requires a new
  process; a shell's new environment does not alter an already running process.
- No assistant runtime-config API or per-request engine override was found. Prompt
  params have session/turn/prompt/context/refs/metadata only
  (`internal/domain/assistant.go:121-130`); the handler forwards them without changing
  mode (`internal/controlplane/assistant_handlers.go:30-41`), and the selector reads
  the constructor's boolean, not request metadata
  (`internal/service/assistant_orchestrator.go:152-164,238-252`). This is an operator
  deployment-config escape hatch, not a normal user-facing mode toggle. Actual live
  env/config and usage were not inspected.

**Shapes still produced/accepted today**

- A flag-off prompt still produces `current_plan`, `last_plan_hash`, `pending_steps`,
  and `awaiting_approval`, returning a `planned` result with the plan/hash
  (`internal/service/assistant_orchestrator.go:279-300`). Approval with an edited
  plan validates and hashes the new content, replaces the plan/pending steps, then
  dispatches it (`:378-399,423-454`). These are current producers, not merely
  historical deserializers.
- Existing local event-log sessions load regardless of the flag
  (`internal/app/app.go:1368-1379,3945-3977`). Approval routing selects agentic only
  when `action_id` is supplied; otherwise it enters legacy plan handling without an
  `agenticEnabled` guard (`internal/service/assistant_orchestrator.go:347-403`). The
  flag check is inside the action-ID branch (`:532-537`). Consequently a loaded
  legacy `awaiting_approval` session remains executable with agentic **enabled**.
- Do not assume a mode flip cleans old fields. `StartTurn` updates loop metadata and
  session state but does not clear the old plan/hash/pending steps
  (`internal/service/assistant_agent_loop.go:224-254`); persistence marshals the
  entire session (`internal/service/assistant_orchestrator.go:1002-1023`). Reusing an
  old session can therefore republish mixed old-plan/new-loop state. This is a
  source-derived consequence, not an observed deployed incident. Retirement needs
  an explicit discriminator/transition policy, not inference from today's default.

**The editor is on the normal app route**

1. Root layout bootstraps assistant state for authenticated users and mounts
   `AssistantChat` outside its protected-route branch
   (`web/src/routes/+layout.svelte:68-94`). Chat mounts the panel/bubble
   (`web/src/lib/components/assistant/AssistantChat.svelte:9-14`); the bubble opens
   the panel (`AssistantBubble.svelte:10-30`).
2. The active panel maps transcript rows to `AssistantTurn`
   (`web/src/lib/components/assistant/AssistantPanel.svelte:15-19,79-89`). Each turn
   derives a plan from its result or the awaiting session, and a hash from the item
   or session. It mounts `AssistantPlanApproval` when plan and hash exist and either
   the item is `planned` or the session is `awaiting_approval`
   (`AssistantTurn.svelte:13-16,238-244`). There is **no engine-config gate** there.
3. The flag-off server's `planned` response follows the live browser prompt path:
   ContextVM request, result normalization, insertion into local transcript state
   (`web/src/lib/stores/assistant.svelte.js:638-665,712-724`). The component offers
   removal, reordering, JSON argument editing and edited-plan approval
   (`web/src/lib/components/assistant/AssistantPlanApproval.svelte:56-101,114-137`).
   The publisher recomputes the hash, sends `modified_plan`, and calls the shared
   `assistant/approval` method (`web/src/lib/stores/assistant.svelte.js:735-777`).
4. Existing data can also feed those gates: session parsing/hydration retains old
   plan fields (`web/src/lib/nostr/assistant.js:99-128`;
   `web/src/lib/stores/assistant.svelte.js:300-329`), and browser cache restoration
   restores both sessions and transcript items (`:100-154`). Qualification: a
   session projection **alone**, with no transcript row, does not render a plan
   card—the panel displays its empty state (`AssistantPanel.svelte:79-89`). Cached
   `planned` rows or current prompt results do render it. A historical `planned`
   row can continue showing a card even after session state changes; backend state
   validation still determines whether an approval executes.

### Retirement inventory: remove branches, preserve shared infrastructure

Paths are relative to Bahia. "Legacy-only" below means a removal candidate **after**
old-session/request policy is defined, not permission to discard historical state.

| Surface | Legacy-only / retirement work | Shared or still-live surface that must remain |
|---|---|---|
| Domain/session contract | `AssistantPlan`, hash envelope/`ComputePlanHash`, session `CurrentPlan`, `LastPlanHash`, `PendingSteps`, approval `ModifiedPlan` (`internal/domain/assistant.go:91-119,132-143,190-214`). Keep a reader/transition if old projections remain supported. | `AssistantSession`, lifecycle states (including `awaiting_approval`), prompt/approval envelope, participant/turn identity, metadata, observable schemas/kinds and `AsyncToolReceipt` (`:16-26,55-97,121-143,175-188`). Agentic approvals also use `awaiting_approval` (`internal/service/assistant_tool_runtime.go:422-436`). |
| Plan-step type | `AssistantPlanStep` is a legacy plan representation. | It is **not currently reference-isolated**: the agentic observer adapter constructs one (`internal/service/assistant_tool_runtime.go:656-658`). Remove that unused step-shaped adapter parameter before deleting the type; preserve the observer. |
| Orchestrator | Flag-off prompt body, planner interfaces, `planFromPrompt`, `validatePlan`, plan prompts; modified-plan/hash/reject/cancel/sequential-dispatch branches; `submittedPlans`, `pending_receipts`, and their helpers (`internal/service/assistant_orchestrator.go:26-33,241-300,361-500,781-831,949-1000,1130-1170`). | The containing file/class is **mixed**, not deletable: prompt/action routing, deduplication, locks, participants, session/status publication, signing, result envelopes, agent-loop DI/controller and async observer remain (`:35-109,503-658,668-779,835-948,1002-1128`). `activeObservers` is used by the shared observer. |
| Provider adapter | `ChatClient`/`ChatClientConfig`, `PlanFromPrompt*`, plan-specific HTTP/SSE implementation, `parseAssistantPlan`, `assistantPlanResponseFormat` (`internal/adapters/llm/chat_client.go:19-27,48-385,396-438`). | Do **not** delete the file wholesale without moving `ContextTooLargeError`/`IsContextTooLarge` and `isContextLimitResponse` (`:29-46,388-394`): OpenAI, Anthropic and prompted agent clients use the error/matcher (`openai_agent_client.go:102-105`, `anthropic_agent_client.go:101-104`, `prompted_agent_client.go:107-110`). |
| Config/DI | Retire false-mode selection/legacy-only streaming behavior; remove planner construction/dependency and the legacy prefix allowlist helper (`internal/app/app.go:1363-1379,3825-3835`; `internal/config/config.go:2314-2315`; guide `:74-76`). Decide whether an old false flag produces a clear configuration error rather than silently changing execution semantics. | Preserve assistant enablement, agentic provider/runtime configuration and identity. The apparently legacy `llm_base_url`, `llm_model`, `llm_api_key` are current agentic fallbacks (`internal/config/config.go:2243-2259`); deleting them is a separate config migration, not necessary planner retirement. |
| ContextVM/control-plane methods | Remove or explicitly reject plan-hash/`modified_plan` operations; replace hash-based cancellation validation. | `assistant/prompt`, `assistant/approval`, `RegisterAssistantContextVMHandlers`, participant checks, fleet-operator gate and decoding are shared (`internal/controlplane/assistant_handlers.go:13-62`). Do not unregister approval: current action-ID approvals use it. |
| MCP/tools/receipts | No wholesale retirement of the assistant-safe async tool surface. | Keep `agent_async_tools.go`, tool registration/descriptors, command publishers, `InvokeAssistantAsyncTool`, `AsyncToolReceipt`, agentic tool runtime/permissions/hooks (`internal/mcp/agent_async_tools.go:13-60`; `internal/app/app.go:1438-1459,3746-3750`). |
| Recovery | Legacy pending-step recovery, `receiptForStep`, `findTerminalResult`, `removePendingStep`, and `pending_receipts` metadata need removal or bounded compatibility handling (`internal/service/assistant_session_recovery.go:150-224,281-358`). | Keep startup subscription/query infrastructure, JSON decoding, `recoverAgentLoop`/blocking path and its registration (`:52-147,227-279`; `internal/app/app.go:1461`). Generic event persistence/loaders are shared. |
| Web components | `AssistantPlanApproval.svelte`; `AssistantTurn` plan/hash derivation and plan-card branch (`:13-14,242-244`); composer hash-based cancel implementation (`AssistantComposer.svelte:63-75`). | Chat, bubble, panel, tabs, composer, most turn rendering and `AssistantActionApproval` remain (`AssistantChat.svelte:9-14`; `AssistantPanel.svelte:79-89`; `AssistantTurn.svelte:238-240`). |
| Web store/parsers | Plan/hash normalization/hashing (`web/src/lib/nostr/assistant.js:6-40`), old plan/pending-step fields and plan-hash/modified-plan branch of the approval publisher (`web/src/lib/stores/assistant.svelte.js:300-329,638-665,735-750`). Account for cached old rows. | Keep the store, subscriptions, cache/session/transcript machinery and general session/status parsers. `publishAssistantApproval` is shared with `publishAssistantActionDecision` (`:780-783`); parser/status envelopes also carry action/tool/observation data (`web/src/lib/nostr/assistant.js:136-177`). |
| Other agentic plan-hash fields | Audit the optional `PlanHash`/`CancelScope` compatibility fields separately; they are not proof of a second batch scheduler. | They are present in `AssistantDeferredAction` and passed by the current runtime/loop (`internal/domain/assistant_agent.go:148-162`; `internal/service/assistant_tool_runtime.go:392-408`; `internal/service/assistant_agent_loop.go:337,515`). Preserve or deliberately change those contracts; do not mechanically delete every `plan_hash` match. |
| Tests and docs | Retire/rewrite legacy planner generation/streaming/hash lifecycle tests, adapter plan tests, plan-card rendering assertions, flag-off config assertions and the escape-hatch documentation. Update acceptance artifacts explicitly rather than erasing evidence. | Keep agentic routing/action, shared observer/receipt, persistence/recovery, context-builder, tool runtime and provider-error coverage. Most assistant test files contain mixed responsibilities (details below). |

### What the current tests actually establish

- **No joined flag-off end-to-end test was found** covering real plan generation,
  `ModifiedPlan`, execution, receipt observation and completion. Searching current
  Go/web test/spec files found no `ModifiedPlan` or `modified_plan` reference.
- `TestAssistantOrchestratorPromptPublishesPlanWithoutSideEffects` uses a supplied
  fake plan and explicitly asserts zero dispatch; streaming tests explicitly choose
  flag-off (`internal/service/assistant_orchestrator_test.go:21-87`).
- The closest approval/receipt exercise seeds an `awaiting_approval` session and
  plan, uses a fake invoker/subscriber, and injects the terminal result; it never
  generates or edits the plan (`assistant_orchestrator_test.go:121-172,628-681`).
  This is useful real orchestrator logic coverage, not a joined production path.
- The actual legacy LLM adapter is tested against an `httptest.Server`, stopping at
  plan parsing (`internal/adapters/llm/chat_client_test.go:64-105`). The web plan test
  checks rendered text/buttons, not remove/reorder/edit/hash/submit behavior
  (`web/tests/unit/assistant/assistant-components.test.js:151-189`).
- The assistant Playwright suite is agentic-labelled and scripts backend replies in
  browser code (`web/tests/e2e/assistant-agentic-enablement.spec.js:264-323,343-413`);
  it is not a live Go-backend legacy acceptance test.
- Concrete legacy test-removal candidates are orchestrator planning/streaming and
  plan-hash lifecycle/error cases (`assistant_orchestrator_test.go:21-297,373-449`),
  legacy `PlanFromPrompt*` adapter tests (`chat_client_test.go:64-263`), domain
  plan-hash approval compatibility (`internal/domain/assistant_test.go:9-29`), and
  the plan-card rendering test above. Preserve agentic cases in the same files,
  including orchestrator action routing (`assistant_orchestrator_test.go:299-371`).
  Recovery tests must be split by the two recovery branches, not deleted together.
- The old PSTF report still calls flag-off the default
  (`pstf/features/bahia-6hic.8/verification_report.md:7`), whereas current defaults
  are agentic-on. Its AC2 mapping points to planning-only coverage
  (`pstf/features/bahia-6hic.8/test_matrix.json:5`). The newer escape-hatch evidence
  also cites unit/build gates (`pstf/features/bahia-yu6g/test_matrix.json:5-20`).
  These demonstrate intentional compatibility testing, not a later product decision
  to keep batch editing permanently.

Tests were inspected, not executed in this read-only investigation. No browser,
provider, live relay, production session inventory, or deployment acceptance was run.
Three narrow explore probes completed; one failed startup was retried. Jev navigation
used `find-lines`, `filter-search`, and `verify-claim` (27 requests, 134,473 input tokens,
about $0.00565). Ranked spans guided reads; one uncertain test-coverage judgment was
resolved by reading the fixture directly. Findings above are grounded in source, not
relevance scores. RepoPrompt reads hit a structure-provider error, so numbered shell
excerpts were used; report mutations used targeted RepoPrompt edits.

### If keeping batch editing: what real unification would require

A `TurnEngine` with `StartTurn`/`Resume` methods delegating to today's two engines
would retain the actual duplication. The existing `AssistantAgentLoopController`
already abstracts start/async-resume/action-resume
(`internal/service/assistant_orchestrator.go:86-93`), while the legacy approval
handler independently dispatches tools (`:434-493`). The useful design boundary is
**two ways to propose work, one durable execution lifecycle**, not two dispatchers
behind an interface.

| Concern | Actual convergence required |
|---|---|
| Execution and approval | Convert a reviewed/edited batch into immutable approved work items consumed by the same executor as agentic tool calls. Share permission checks, argument validation, hooks, tool descriptor lookup and sync/async dispatch. Legacy currently applies a name allowlist then calls the invoker directly (`assistant_orchestrator.go:979-997,434-454`), while agentic dispatch enforces command scope/hooks/runtime permissions (`assistant_agent_loop.go:514-549`; `assistant_tool_runtime.go:172-214`). Decide explicitly how batch approval authorizes individual steps and whether later hooks may change approved arguments; a wrapper cannot reconcile those semantics. |
| Durable work and receipts | Introduce one resumable queue/cursor and receipt ownership model. Legacy stores `pending_steps` plus `metadata.pending_receipts` (`assistant_orchestrator.go:801-829`); agentic stores one `metadata.agent_loop.waiting_receipt` and pending tool/action IDs (`internal/domain/assistant_agent.go:31-50`; `assistant_tool_runtime.go:365-377`). Preserve existing idempotency keys during conversion: legacy uses `assistant:<session>:<plan-hash>:<step>` (`assistant_orchestrator.go:436`), agentic uses `assistant-agent:<session>:<run>:<call>` (`assistant_tool_runtime.go:797-799`). Re-keying already dispatched work risks replay. |
| Batch continuation | Persist which approved steps remain and resume them without asking the model to recreate the batch. The current agent loop suspends on the first deferred/async observation (`assistant_agent_loop.go:455-465`); its resume methods feed one observation back to the model (`:279-309,367-375`). It is not already a durable multi-step approved-plan scheduler. This continuation/cursor work is real new implementation. |
| Observation and history | Reuse the already-shared receipt observer (`assistant_tool_runtime.go:654-658`) and normalize all results to one tool-observation contract. Unify legacy recovery backfill and normal resume ownership rather than adding another subscription layer. Feed both modes into a common persisted turn/transcript/audit history: legacy currently updates a summary (`assistant_orchestrator.go:289`), whereas agentic appends model/tool messages (`assistant_agent_loop.go:431-435,468-472`). |
| Cancellation | Supply one explicit run/session cancellation operation separate from denying an action or rejecting a plan. Persist stop state, stop further dispatch, coordinate active observers/model work, and specify how already submitted downstream work is still accounted for without promising rollback. The two current cancellation paths do different things (claim 3); neither an interface name nor `CancelScope` metadata resolves this. |
| Recovery and compatibility | One recovery entry must resume the shared state machine, consume known receipts, advance the approved cursor safely, and explicitly park/reject incompatible historical states. Today's legacy recovery blocks at unreceipted steps while agentic recovery resumes model reasoning (`assistant_session_recovery.go:183-224,227-261`). Convert/version v1 JSON and cached views deliberately; test old awaiting, executing, blocked, terminal and mixed-mode sessions. No SQL schema change is inherently required here either. |
| Verification | Add a joined fake-provider/real-service generation -> edited approval -> common executor -> receipt completion/restart test, plus browser edit/hash/submit and cancellation coverage. Preserve default agentic behavior and permission boundaries. Existing separated tests cannot certify the unified lifecycle. |

### Decision implications

The evidence supports **retiring a documented rollout fallback**, if product owners
confirm batch editing is no longer required; it does not support calling the path
unreachable, the UI orphaned, or the feature already retired. Fresh default agentic
sessions do not generate batch plans, but a configured flag-off operator can do so
today, and retained plans can still be approved after the flag is turned back on.
There is no repository evidence of the number of such operators/sessions.

A retirement should therefore explicitly (a) stop new legacy plan production and
handle old flag-off configuration clearly, (b) inventory/resolve outstanding plan
states—including steps without receipts—rather than assuming recovery drains them,
(c) retain observation/accounting for already dispatched effects, (d) define genuine
agentic run cancellation, and (e) remove the legacy branches/editor while preserving
the shared surfaces above. Historical JSON can remain readable without keeping its
execution path indefinitely; rejecting old approval contracts must be deliberate
and visible, not accidental decoder breakage.

That is substantially smaller than building a common executor with durable batch
continuation, authorization semantics, unified receipt checkpoints and recovery.
If batch editing remains a supported product requirement, that larger consolidation
is justified; a thin two-engine wrapper is not. Neither reachability nor a rendering
unit test settles the missing product decision in B4b.


## Investigation Log

### Phase 1.5 — External fact-gathering (4 explore agents)
**Hypothesis:** several of these decisions turn on facts outside the Go source.
**Findings:** all four returned decisive evidence — see Background B1-B4b.
**Conclusion:** three of the five decisions (`gb14q`, `02f15`, `5teth`) were effectively
settled by external evidence alone; two (`185t0`, `1qkfk`) needed in-workspace investigation.

### Phase 3 — Pair investigations (2 disjoint paths)
**Hypothesis:** the cost of CORD-04 compliance, and the blast radius of planner retirement,
are both unknown and determine their decisions.
**Findings:** all eight numbered claims verified, with two material corrections — CORD-04
evidence is recoverable from retained seals rather than discarded, and legacy plan approvals
are not gated by the agentic flag.
**Conclusion:** both decisions are now cost-bounded rather than open-ended.

### Orchestrator spot-checks
Independently verified: seven duplicate migration prefix groups; `RegisterDNSContextVMHandlers`
takes a `*FleetOperatorGate`; the Cloudflare-only public provider check at `config.go:2808`;
`concord_control.go:104` self-documenting that folding "does not resolve the CORD-04 Roster";
`concord_compaction.go:87,107-108` consuming `fold.heads`/`fold.editions`; and
`assistant_orchestrator.go:329,374-383` sitting outside the `agenticEnabled` branch at `:238`.

## Root Cause

These five items were not blocked by engineering difficulty. Each was blocked by an
**unanswered prior question** that no ticket owned:

- `gb14q` — is external-runner portability a goal? (Nobody had asked.)
- `185t0` — what does CORD-04 actually prescribe? (The spec existed upstream and was never consulted.)
- `02f15` — are per-tenant DNS zones a product requirement? (The issue set this as its own
  precondition and the precondition was never tested.)
- `5teth` — is there a concrete second-provider use case? (Filed speculatively to exercise a
  new abstraction.)
- `1qkfk` — is batch plan review/editing a supported product feature? (Retained for rollout
  compatibility; the decision was deferred and then forgotten.)

In three cases the answer was already recorded somewhere — an upstream spec, an originating
audit recommendation, or the issue's own filing commit — and simply had not been retrieved.

## Recommendations

1. **`185t0` — comply, starting with containment.** Install pre-mutation refusal in
   `concord_rotation.go` *before* rotation planning, key minting, custody writes or publication
   (1-3 days), then implement full authority filtering across all head candidates (10-20 days),
   retaining the lowest-rumor-id rule only between authorized equal-version candidates. Record
   the deliberate availability loss: non-owner rotation and all refounding become unavailable
   until authority is provable, which means a compromised member cannot be rotated out during
   the interim. Resolve the fresh-joiner evidence question early — parsing cannot recreate
   evidence that earlier compactions omitted.
2. **`1qkfk` — decide the product question, then retire in stages.** The agentic flag governs
   new prompt routing only, so "enable agentic, wait, delete" is unsafe. Sequence: stop creating
   new legacy obligations → apply an explicit, server-enforced policy to outstanding approvals
   and undispatched steps → remove execution compatibility → remove the editor. Keep
   `agent_async_tools.go` and the shared tool runtime. Separate retiring *batch editing* from
   retiring *human review* — do not let the former silently widen autonomous execution.
3. **`02f15` — close.** The precondition it sets for itself is unmet, and the originating
   audit's recommended fix (operator gating) is implemented.
4. **`5teth` — close as speculative.** Reopen when a concrete provider and use case exist.
5. **`gb14q` — decide portability, then document or renumber.** If `golang-migrate` adoption is
   a real objective, prefix uniqueness plus a version transition are prerequisites *of that
   adoption*. Otherwise document and test the full-stem semantics and state the incompatibility.
   Do not renumber deployed history for a hypothetical.

## Preventive Measures

- **Record the precondition's answer, not just the precondition.** `02f15` carried
  "only if per-tenant zones are a product requirement" for seventeen days without anyone
  testing it. A conditional issue should either cite evidence for its condition or be a question.
- **Consult normative specs before filing protocol issues.** `185t0` described Bahia's tie-break
  as wrong when it was half-right; CORD-04 was findable upstream the whole time.
- **Don't file speculative issues from inside the commit that creates the abstraction.**
  `5teth` was born of "we could add another one", not a need.
- **Deferred-compatibility decisions need an owner and a date.** The legacy planner survived on
  "keep it working with the flag off" for nearly three months with no review trigger.
- **Re-count claims in aging issues.** This sweep found stale counts in three separate tickets.
