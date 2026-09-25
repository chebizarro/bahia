# Durable HiveCI initiation

`source_event_id` is the unique replay key. The first PostgreSQL insert claims
the canonical build ID and immutable request fingerprint. Conflicting requests
cannot reuse that key. ContextVM derives the build ID from the signed source
event; the initiator also returns the ID recorded by the winning claim.

The local-authority record advances with single-statement stage CAS:

`claimed → request_ready → request_unconfirmed → request_published →
job_unconfirmed → job_published → evidence_ready → evidence_unconfirmed →
evidence_published`

Adopted trusted runs skip request publication and Loom dispatch. Prepared signed
events, pinned job arguments, and the per-run signing key are encrypted at rest
using Bahia's existing secret encryptor. Repository passwords are not persisted
in the initiation record. Loom dispatch re-resolves the recorded service-scoped
mirror credential; completed replay performs no secret resolution or publication.

Only the CAS winner may send a ready event or submit a Loom job. The unconfirmed
stage is committed **before** the external operation; accepted publication is
committed separately. A crash between confirmed stages resumes at the next
stage, even with a new connection pool and initiator process.

An unconfirmed operation is not success. Recovery inspects signed relay evidence
using exact event IDs (run/evidence) or the original signer and run correlation
(Loom). It checks event hashes/signatures and completes historical inspection
through EOSE; CLOSED, cancellation, invalid or ambiguous evidence fail closed.
Confirmed stages are never republished. Missing evidence leaves the operation
unconfirmed: it does **not** authorize a new send, even if the process crashed
before the original send. This is an at-most-once dispatch guarantee with explicit
uncertainty, not a distributed transaction or a guarantee of eventual completion.
Do not delete claims or mint a different request to bypass an unknown outcome;
restore/inspect the original relay evidence first. The down migration locks the
table and refuses to discard any replay authority.

Integration proof uses a disposable PostgreSQL database with one schema per test:

```sh
BAHIA_HIVECI_TEST_DATABASE_URL=postgres://... go test -tags=integration -p 1 ./internal/adapters/gitea
BAHIA_HIVECI_TEST_DATABASE_URL=postgres://... go test -tags=integration -race -p 1 ./internal/adapters/gitea -run 'TestPostgres.*(AtomicClaim|Concurrent)' -count=25
```
