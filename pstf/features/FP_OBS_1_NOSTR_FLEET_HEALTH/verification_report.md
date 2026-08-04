# Verification Report — fp-obs-1

## Scope

Nostr-native fleet health projection for signed canonical observables, including explicit relay ingestion health, bounded Prometheus labels, and documentation of the direct-scrape boundary.

## Acceptance mapping

- AC1: the subscriber includes kinds 30315, 30316, 30317, 30900, and 4903 and invokes the projector only after validation and durable event persistence.
- AC2: projector tests prove an older replaceable event cannot regress newer subject state.
- AC3: lifecycle tests distinguish subscription/EOSE/CLOSED state from retained subject state.
- AC4: metrics tests reject attacker-controlled domain/status labels and prove raw event content is absent.
- AC5: the operator guide declares relay events as the semantic source and restricts direct scraping to node/process/GPU exporters.

## Verification

- `go test ./internal/adapters/telemetry ./internal/adapters/nostr ./internal/app` — passed.
- `git diff --check` — passed.
