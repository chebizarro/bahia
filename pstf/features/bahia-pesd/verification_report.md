# Verification Report

## Evidence

- `GOCACHE=/tmp/bahia-go-cache go test ./internal/domain`
- `GOCACHE=/tmp/bahia-go-cache go test ./internal/service`
- `GOCACHE=/tmp/bahia-go-cache go test ./internal/adapters/runtime`

## Result

Both targeted packages pass. Golden hashes were intentionally updated after bumping desired-state schema version to 2 for deployment-unit hash inputs.
