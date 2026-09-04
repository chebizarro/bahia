# Verification — deployed-artifact-dns-routes

Branch `task/bahia-deployed-artifact-dns-routes` (base master 3d1d8400).

- `go build ./...`, `go vet ./...`, `go test ./...` all pass on the combined change.
- Live root cause reproduced from code: `PublicRouteCoordinate` is a fixed 123-character string sent verbatim as the Cloudflare comment (limit 100) — every prior apply failed, so no legacy raw-coordinate records can exist in Cloudflare; the derived marker therefore needs no legacy matching.
- Review cycle fixed two defects pre-commit: transient deployment-unit load errors silently downgrading reconcile mode/runtime resolution, and unreachable filesystem-backend wiring left behind after config-validation rejection.
- Live retest (route attach for astillero.sharegap.net, public DNS + HTTPS 200, internal DNS resolution, Astillero service metadata cleanup via signed service/update --runtime-type) is operator/Track B scope per the task acceptance.
