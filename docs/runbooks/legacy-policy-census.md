# Legacy deployment-policy relay census

`bahia-policy-census` is an operator-only, read-only diagnostic. It takes a
repeatable-read snapshot of up to `--max-rows` legacy `deployment_policies`
identifiers and reads each addressable `30900` policy coordinate from every
relay in the effective signed control-plane policy. It never signs,
enqueues, publishes, writes a cursor, or changes PostgreSQL. A relay-held
policy wins over the SQL row regardless of which content is newer or more
desirable.

```sh
make build-bahia-policy-census
bin/bahia-policy-census --config config.yaml --relays wss://relay-a.example,wss://relay-b.example
```

The command reports `relay-present-sql-skipped` with signed event IDs when any
selected relay holds the coordinate. It never certifies absence: the underlying
Nostr transport can discard invalid EVENT frames before the census observes
them, so EOSE without a valid coordinate fails closed with no JSON report.
The command reads the signed canonical `RelayPolicyState` from every
daemon hydration candidate: configured sidecar backend/public, ContextVM,
browser, service, generic, and NIP-34 relays, plus relays advertised by the
validated durable PostgreSQL relay-policy projection and the signed state.
The projection has no retained signature, so its URLs are discovery hints only;
its event ID and payload hash must match the signed head returned by relays. It requires the same event head on every candidate and every
derived effective control-plane relay. The derived set follows daemon sidecar-backend
precedence, then canonical ContextVM relays, then canonical service relays.
`--relays` must exactly match that set; a different or missing signed policy
head, incomplete EOSE, or absent effective topology aborts without a report.
The output includes `relay_policy_event_id` and marks the set
`signed-canonical-policy-verified`. The command rechecks the head across the
entire discovery set before reporting, including relays outside the effective
control-plane publish set. This is a bounded read observation, not a lease against later
changes. It does not fence old SQL publishers or stage SQL rows in the outbox.
Do not treat its output as a cutover or import receipt.

An unavailable relay, terminal `CLOSED`, truncated stored result, no valid
signed policy head, invalid projection hint, SQL read failure, global deadline,
or a row count beyond `--max-rows` aborts without a partial JSON report. The
Nostr transport may filter invalid EVENT frames before the census sees them;
when an older valid head is present, the command cannot identify every such
filtered frame. Stop or fence
SQL writers before using a full census operationally; the SQL snapshot alone
cannot prevent a writer from publishing to a relay during the read.

## Bounded paged dry-run

For a large SQL table, run an initial page and record its signed
`relay_policy_event_id`. Pin that ID on every resumed page and retain each
JSON report as an operator audit artifact:

```sh
bin/bahia-policy-census --config config.yaml \
  --relays wss://relay-a.example,wss://relay-b.example \
  --page-size 100
# If page.has_more is true, repeat with both:
# --expected-policy-head <relay_policy_event_id> --after-id <page.next_after_id>.
```

Each page reads at most `--page-size + 1` ordered SQL identifiers in a
repeatable-read, read-only transaction, then checks each selected coordinate
through complete EOSE on every effective relay. A successful page reports
`page.last_id`, `page.has_more` and an exclusive `page.next_after_id` only when
another row was visible in that page's SQL snapshot. The same cursor can be
replayed without a write-side checkpoint. The pinned signed relay-policy head
is required on resume, checked at the start and across discovery relays before a page is emitted;
a changed head aborts. A row without a valid relay event, a failed relay, or a
deadline aborts the entire page with no JSON. Previously completed pages
remain observations, not a commit log.

Page runs do **not** share one SQL snapshot. A concurrent SQL insert or delete
can change what a resumed page sees, including adding a UUID behind a prior
cursor. Fence SQL writers and preserve source-row change evidence independently
before interpreting a full sequence of pages. Even with that fence, a
relay-empty coordinate is ambiguous because invalid EVENT frames may be
filtered before observation. There is no `--publish` or import mode, and no
page constitutes an admission or cutover receipt. Importing an SQL-only
policy requires a separately reviewed absence/coordinate-ownership proof and
a fenced signer/old-writer cutover; this diagnostic intentionally refuses to
manufacture either.
