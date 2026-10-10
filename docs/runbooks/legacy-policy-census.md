# Legacy deployment-policy relay census

`bahia-policy-census` is an operator-only, read-only diagnostic. It takes a
repeatable-read snapshot of up to `--max-rows` legacy `deployment_policies`
identifiers and reads each addressable `30900` policy coordinate from every
operator-supplied relay. It never signs, enqueues, publishes, writes a cursor,
or changes PostgreSQL. A relay-held policy wins over the SQL row regardless of
which content is newer or more desirable.

```sh
make build-bahia-policy-census
bin/bahia-policy-census --config config.yaml --relays wss://relay-a.example,wss://relay-b.example
```

The command reports `relay-present-sql-skipped` with signed event IDs when any
selected relay holds the coordinate. `absent-on-selected-relays` means only
that every **selected** relay completed a non-truncated EOSE without returning
that coordinate. It is not a safe import decision: this command does not
hydrate the effective canonical `RelayPolicyState`, prove its relay set equals
the supplied set, fence old SQL publishers, or stage SQL rows in the outbox.
Do not treat its output as a cutover or import receipt. The `relay_set_type`
field is always `operator-supplied-unverified`.

An unavailable relay, terminal `CLOSED`, truncated stored result, invalid
signed policy event, SQL read failure, or a row count beyond `--max-rows`
aborts without a partial JSON report. Stop or fence SQL writers before using a
full census operationally; the SQL snapshot alone cannot prevent a writer
from publishing to a relay during the read.
