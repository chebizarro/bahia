# Bahia SQL migrations

The embedded Go runner identifies each migration by its **complete filename
stem**, not its numeric prefix. It applies `.up.sql` files in lexicographic
filename order and records that entire stem in `schema_migrations.version`.
Several historical numeric prefixes are shared by distinct migrations; do not
renumber or collapse them. External runners such as golang-migrate, goose, and
dbmate are unsupported for this directory: their version or file conventions
would not preserve Bahia's live version keys and transactional behavior.

The server migrates up on startup. Operators can use `bahia-migrate status|up|down`
without starting the server; see `docs/user-guide/cli-reference.md`. Down
scripts are supported, but destructive and sometimes guarded by live-data
checks. They should be applied newest first against a backed-up database.
