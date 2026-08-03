# HITL decisions

No architecture exception was accepted. The existing coordinator remains the sole execution owner, rollback uses a new canonical intent, public projections contain only allowlisted non-secret fields, and routing/DNS/tunnel implementation remains in sibling item 06.
