# Legacy agent Soul adoption report verification

The implementation is isolated from Bahia's existing adoption service, handlers, persistence, and migrations. It accepts only a caller-supplied sanitized JSON snapshot, emits JSON to standard output, and contains no network, database, Docker, Signet, Soul publication, ACL/grant, custody, or Bahia service mutation dependency.

Focused verification command:

```text
go test ./internal/soulfactory ./cmd/soulfactory-legacy-adoption-report
```

Result: PASS. The suite covers no-match, one strong match, multi-candidate ambiguity, name-only false positives, container-name mismatch, and the fixture-backed CLI report.
