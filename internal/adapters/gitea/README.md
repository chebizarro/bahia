# Durable HiveCI initiation

The signed `build/request` intent event is the identity of a build: its id is
the unique replay key, and the canonical build ID is derived from it
(`controlplane.BuildIDForSourceEvent`, a namespaced UUID of the source event
id). Two initiators, or a restart, name the same build from the same signed
request without claiming anything; a request that names another build ID for
the same source event is refused.

The initiation journal (`CanonicalInitiationStore`) is a confidential
`30900` cp-state record per source event (`legacy_kind=32024`,
`d=hiveci:initiation:<source-event-id>`, `t=hiveci-initiation`), published
through the local outbox before any index is written and read back from the
daemon's own retained records in the local event store. The document (stage,
request fingerprint, prepared signed events, pinned job arguments, mirror
read-credential *reference*) is fleet-OCK encrypted; the per-run publisher
key is in the envelope's `service_inner` layer, NIP-44 encrypted to the
service pubkey, so it never reaches the relay in plaintext or under the fleet
key. Repository and mirror-read passwords are not journaled at all: Loom
dispatch re-resolves the recorded service-scoped credential reference.

The record advances with a stage compare-and-publish on the retained record:

`claimed → request_ready → request_unconfirmed → request_published →
job_unconfirmed → job_published → evidence_ready → evidence_unconfirmed →
evidence_published`

Adopted trusted runs skip request publication and Loom dispatch. Only the
initiator that advanced to an unconfirmed stage may send the ready event or
submit the Loom job; the unconfirmed stage is journaled **before** the
external operation and accepted publication is journaled separately. A crash
between stages resumes at the next stage from the journal, in a new process,
with a new connection pool, or with no database at all; completed replay
performs no secret resolution or publication.

An unconfirmed operation is not success. Recovery inspects signed relay
evidence using exact event IDs (run/evidence) or the original signer and run
correlation (Loom). It checks event hashes/signatures and completes historical
inspection through EOSE; CLOSED, cancellation, invalid or ambiguous evidence
fail closed. Confirmed stages are never republished. Missing evidence leaves
the operation unconfirmed: it does **not** authorize a new send, even if the
process crashed before the original send. This is an at-most-once dispatch
guarantee with explicit uncertainty, not a distributed transaction or a
guarantee of eventual completion. Do not mint a different request to bypass an
unknown outcome; restore/inspect the original relay evidence first.

## Credential boundary (bahia-xjdo9)

Initiation has exactly one database dependency: resolving the upstream
repository credential (`CredentialRef`) and the fleet mirror-read password
(`MirrorReadCredentialRef`). Both are secret **values**, not metadata. The
canonical secret registry (`30900` secret-registry records) carries
references only; the values exist solely in the PostgreSQL secret store,
encrypted with the service key, and the daemon has no canonical path that
could yield them. The initiator is therefore wired whenever the journal
exists, and `app.go` substitutes `SecretStoreUnavailable` for the resolver
when the store is absent (WARN `hiveci_credential_store_unavailable`; health
check `hiveci.initiator_credential_store=false`). A new initiation then fails
closed at credential resolution with `ErrSecretStoreUnavailable`, before any
mirror call or publication, and its journal record stays at `claimed`, so the
same signed request prepares once the store is reachable
(`initiator_secret_store_test.go`). Journaling, build identity, replay of a
completed initiation and resume of a prepared one never touch the store.

`hiveci_initiations` (`PgInitiationStore`) is an optional, encrypted SQL
index of the journal. It is written after each journal publish (failures are
logged), rebuilt from the journal after the local store's warm start, and
in-flight SQL-era initiations are journaled once at upgrade
(`BackfillFromIndex`). Nothing reads it on the initiation path.

Proof runs over a real local event store, outbox, projector and fleet-OCK
encryptor in a temp dir (`initiation_canonical_test.go`): every durable
boundary resumes after a daemon restart, 32 concurrent initiations of one
signed request dispatch once, the prepared operation and per-run key are
recovered without plaintext secrets, and the SQL index may fail or be lost.
The PostgreSQL index conformance remains runnable with a disposable database:

```sh
BAHIA_HIVECI_TEST_DATABASE_URL=postgres://... go test -tags=integration -p 1 ./internal/adapters/gitea
```
