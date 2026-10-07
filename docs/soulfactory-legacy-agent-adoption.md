# Soul identity adoption report

`soulfactory-legacy-adoption-report` is a read-only classification tool for
matching running agent containers to authoritative Soul identities before an
operator links them. It never mutates Bahia, Signet, relay state, container
state or access-control lists.

## Input

Provide one sanitized `soulfactory-legacy-adoption-input/v1` JSON document
built from independently collected evidence:

- running agent inventory with stable runtime/container identifiers;
- trusted Soul `31951` records and their signed identity references;
- optional operator labels used only as display context.

Do not include private keys, bunker connection secrets, environment values,
TLS material or tokens. Container and display names are not identity evidence.

```bash
go run ./cmd/soulfactory-legacy-adoption-report \
  -input sanitized-inventory.json > adoption-report.json
```

Use `-input -` for stdin.

## Matching policy

The report classifies a running agent only when its independently trusted
identity evidence selects exactly one authoritative Soul. Multiple matches,
conflicting identity pubkeys or incomplete evidence are refusals. The command
exits `3` when any running agent has multiple authoritative matches or
conflicting trusted identity evidence.

An operator reviews the report before any separate linking operation. Preserve
the input digest, report digest, source event IDs and reviewer identity; do not
turn a name-only suggestion into an approved match.

The report explicitly performs no key generation, rotation, revocation,
identity replacement, ACL/grant change, custody change or service mutation.
