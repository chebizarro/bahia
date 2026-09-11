# BAHIA_ROUTE_CANARY_POLICY_OVERRIDES: Human-in-the-Loop Decisions

## Resolved during implementation

**Overrides are keyed by hostname in `route_canaries.overrides`, not added to the route plan.**
Canary expectations are control-plane policy, not signed desired state (see `BAHIA_ROUTE_CANARIES`). Putting overrides on `DesiredPublicRoutePlan` would invalidate deployed plan hashes. A hostname-keyed map mirrors `internal_dial_addresses`. Koanf keeps dotted map keys intact through decoding, which was verified before committing to the shape. Keys are canonicalized (case, trailing dot), and keys that collide once canonicalized are rejected.

**Each override field replaces exactly that fleet-wide field, and pointer fields distinguish unset from explicit zero.**
Field-level inheritance is one rule that covers every field. Pointers are what let a route clear a fleet-wide marker (`expected_body_contains: ""`) or disable the expiry warning (`tls_min_days_remaining: 0`). Without them, an explicit zero would be indistinguishable from "inherit".

**The regex must match the whole bounded body.**
The issue asked for an "anchored" regex, so anchoring is fixed rather than left to the pattern author: the pattern is evaluated as `\A(?:pattern)\z`, the same whole-value contract Prometheus relabel regexes use. A pattern therefore never passes by matching an incidental fragment. `\A`/`\z` are used rather than `^`/`$` so `(?m)` cannot weaken them. The cost is that partial matches need explicit wildcards, e.g. `(?s).*"status"\s*:\s*"ok".*`; the user guide documents this.

**Regex bounds: 256-byte source, 2048 compiled instructions, empty-matching patterns rejected.**
RE2 is linear-time, so there is no catastrophic backtracking. The bounds fix each assertion's worst-case cost at body length times program size, over a body already bounded to 512 bytes. They also keep patterns reviewable. A pattern matching an empty body, such as `(?s).*`, asserts nothing, and it would silence the catch-all warning, so it is rejected.

**A regex failure reuses `body_mismatch`.**
Both body forms mean "the intended application did not answer". Reusing the classification keeps the DB CHECK constraint and its conformance test unchanged, with no migration. The reason string names which assertion was configured.

**Per-route interval uses a next-due schedule and a timer that wakes at the earliest due route.**
A fixed ticker at the smallest interval would drift routes whose interval is not a multiple of it. The timer only schedules health checks. The wait is capped at the fleet-wide interval so new routes are discovered on the usual cadence. A sweep that overran a route's interval is followed by the next sweep at once, as the previous ticker behaved. Overdue entries are ignored only after a failed enumeration, so a failing plan source backs off instead of being retried in a loop. A backwards wall-clock step makes a route due immediately. `EvaluateOnce` keeps its original "probe every route once" contract; the new `EvaluateDue` drives the schedule.

**Minimum per-route interval is 5s.**
Every probe is a real HTTPS request against a production route. An override tuned for one route must not turn the canary into load.

## Requires human decision

**Per-route thresholds, catch-all detection and gate timing.**
Failure/success thresholds, `detect_catch_all`, `require_discriminating_health_path`, and gate timeout/retry stay fleet-wide. They were outside the acceptance criteria. Making them per-route is mechanically the same change, but it widens what a single route can silently relax, so it is left for an explicit decision.

**Warning for overrides that match no managed route.**
An override for a hostname not currently in desired state is valid: the route may be deployed later. It is neither rejected nor warned about. A startup or periodic warning for unmatched overrides would catch typos in hostnames, and is a candidate follow-up.
