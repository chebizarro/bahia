#!/usr/bin/env python3
"""
scripts/seed_hiveci_pipeline_policy.py

Seeds (or inspects) the hiveci_pipeline_policies row that maps the Bahia
GitHub repo to its Bahia service/environment deployment target.

Background
----------
Bahia's pipeline bridge (internal/pipeline/bridge.go) calls
GetPolicyByRepoAndWorkflow(repo_coordinate, workflow_path) for every
ingested kind-5402 result event.  If no matching row exists the bridge
exits silently with "no pipeline policy match; skipping" and no artifact
is ever registered.  This script inserts that row idempotently.

The repo_coordinate value is the NIP-34 "a" tag published by grasp-gitea
in kind-5401 events (format: "30617:<grasp-gitea-pubkey>:<repo-slug>").
Use --list-seen-coordinates to discover what value grasp-gitea is actually
emitting if 5401 events have already been ingested.

Usage examples
--------------
# Dry-run: print the SQL without touching the database
python3 scripts/seed_hiveci_pipeline_policy.py \\
    --db-url "postgres://bahia:bahia@localhost:5432/bahia" \\
    --repo-coordinate "30617:abc123:chebizarro/bahia" \\
    --service-name bahia \\
    --environment-name edge-01 \\
    --dry-run

# Apply
python3 scripts/seed_hiveci_pipeline_policy.py \\
    --db-url "postgres://bahia:bahia@localhost:5432/bahia" \\
    --repo-coordinate "30617:abc123:chebizarro/bahia" \\
    --service-name bahia \\
    --environment-name edge-01

# Show all current policies and any ingested repo coordinates
python3 scripts/seed_hiveci_pipeline_policy.py \\
    --db-url "postgres://bahia:bahia@localhost:5432/bahia" \\
    --list

# Enable auto-deploy-staging after artifact registration is verified (step 8)
python3 scripts/seed_hiveci_pipeline_policy.py \\
    --db-url "postgres://bahia:bahia@localhost:5432/bahia" \\
    --repo-coordinate "30617:abc123:chebizarro/bahia" \\
    --service-name bahia \\
    --environment-name edge-01 \\
    --auto-deploy-staging \\
    --staging-environment edge-01

Database connectivity
---------------------
--db-url accepts a libpq connection string or DSN.  If omitted the script
reads BAHIA_DATABASE_URL from the environment.  On edge-01 the live value
is available in the running bahia container's environment.

Requires: psycopg2 (pip install psycopg2-binary) or psycopg2 system package.
If psycopg2 is unavailable, use --dry-run to print SQL and pipe to psql:

    python3 scripts/seed_hiveci_pipeline_policy.py --dry-run ... | psql "$DB_URL"
"""

import argparse
import json
import os
import sys

WORKFLOW_PATH_DEFAULT = ".github/workflows/hive-ci-build.yml"

# ---------------------------------------------------------------------------
# SQL fragments
# ---------------------------------------------------------------------------

SQL_LOOKUP_SERVICE = """
SELECT id::text, name, artifact_repo
FROM services
WHERE name = %(service_name)s
LIMIT 1;
"""

SQL_LOOKUP_ENVIRONMENT = """
SELECT id::text, name, protected
FROM environments
WHERE name = %(environment_name)s
LIMIT 1;
"""

SQL_INSERT_POLICY = """
INSERT INTO hiveci_pipeline_policies
    (repo_coordinate, workflow_path, branch_pattern,
     service_id, environment_id, enabled, metadata)
VALUES
    (%(repo_coordinate)s, %(workflow_path)s, %(branch_pattern)s,
     %(service_id)s::uuid, %(environment_id)s::uuid,
     TRUE, %(metadata)s::jsonb)
ON CONFLICT DO NOTHING
RETURNING id::text, repo_coordinate, workflow_path, branch_pattern,
          service_id::text, environment_id::text, enabled, metadata;
"""

SQL_SELECT_EXISTING = """
SELECT p.id::text, p.repo_coordinate, p.workflow_path,
       p.branch_pattern, p.enabled, p.metadata,
       s.name AS service_name, e.name AS environment_name
FROM hiveci_pipeline_policies p
JOIN services s ON s.id = p.service_id
JOIN environments e ON e.id = p.environment_id
WHERE p.repo_coordinate = %(repo_coordinate)s
  AND p.workflow_path = %(workflow_path)s
ORDER BY p.created_at;
"""

SQL_LIST_POLICIES = """
SELECT p.id::text, p.repo_coordinate, p.workflow_path,
       p.branch_pattern, p.enabled, p.metadata,
       s.name AS service_name, e.name AS environment_name,
       p.created_at
FROM hiveci_pipeline_policies p
JOIN services s ON s.id = p.service_id
JOIN environments e ON e.id = p.environment_id
ORDER BY p.created_at;
"""

SQL_LIST_SEEN_COORDINATES = """
SELECT DISTINCT repo_coordinate, workflow_path,
       MAX(event_created_at) AS last_seen,
       COUNT(*) AS run_count
FROM hiveci_workflow_runs
GROUP BY repo_coordinate, workflow_path
ORDER BY last_seen DESC;
"""

SQL_LIST_SERVICES = """
SELECT id::text, name, artifact_repo FROM services ORDER BY name;
"""

SQL_LIST_ENVIRONMENTS = """
SELECT id::text, name, protected FROM environments ORDER BY name;
"""

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def die(msg):
    print(f"ERROR: {msg}", file=sys.stderr)
    sys.exit(1)


def connect(db_url):
    try:
        import psycopg2
        import psycopg2.extras
    except ImportError:
        die(
            "psycopg2 is not installed.  Install it with:\n"
            "  pip install psycopg2-binary\n"
            "Or use --dry-run to print SQL and pipe to psql instead."
        )
    try:
        conn = psycopg2.connect(db_url)
        conn.autocommit = False
        return conn, psycopg2.extras.RealDictCursor
    except Exception as exc:
        die(f"Could not connect to database: {exc}")


def print_table(rows, columns):
    if not rows:
        print("  (no rows)")
        return
    col_widths = {c: len(c) for c in columns}
    for row in rows:
        for c in columns:
            col_widths[c] = max(col_widths[c], len(str(row.get(c, "") or "")))
    header = "  " + "  ".join(c.ljust(col_widths[c]) for c in columns)
    sep    = "  " + "  ".join("-" * col_widths[c] for c in columns)
    print(header)
    print(sep)
    for row in rows:
        print("  " + "  ".join(str(row.get(c, "") or "").ljust(col_widths[c]) for c in columns))


# ---------------------------------------------------------------------------
# Commands
# ---------------------------------------------------------------------------

def cmd_list(conn, CursorClass):
    with conn.cursor(cursor_factory=CursorClass) as cur:
        print("\n=== Current hiveci_pipeline_policies ===")
        cur.execute(SQL_LIST_POLICIES)
        rows = cur.fetchall()
        print_table(rows, ["id", "repo_coordinate", "workflow_path",
                            "service_name", "environment_name", "enabled", "metadata"])

        print("\n=== Repo coordinates seen in ingested 5401 events ===")
        cur.execute(SQL_LIST_SEEN_COORDINATES)
        rows = cur.fetchall()
        print_table(rows, ["repo_coordinate", "workflow_path", "run_count", "last_seen"])

        print("\n=== Available services ===")
        cur.execute(SQL_LIST_SERVICES)
        rows = cur.fetchall()
        print_table(rows, ["id", "name", "artifact_repo"])

        print("\n=== Available environments ===")
        cur.execute(SQL_LIST_ENVIRONMENTS)
        rows = cur.fetchall()
        print_table(rows, ["id", "name", "protected"])
    print()


def cmd_seed(args, conn, CursorClass):
    with conn.cursor(cursor_factory=CursorClass) as cur:
        # Resolve service
        if args.service_id:
            service_id = args.service_id
            service_name = args.service_id
        else:
            cur.execute(SQL_LOOKUP_SERVICE, {"service_name": args.service_name})
            row = cur.fetchone()
            if not row:
                # Show available services to help the operator
                cur.execute(SQL_LIST_SERVICES)
                services = cur.fetchall()
                names = [r["name"] for r in services]
                die(
                    f"Service '{args.service_name}' not found.\n"
                    f"  Available services: {names}\n"
                    f"  Pass --service-name with one of those, or --service-id with the UUID."
                )
            service_id = row["id"]
            service_name = row["name"]

        # Resolve environment
        if args.environment_id:
            environment_id = args.environment_id
            environment_name = args.environment_id
        else:
            cur.execute(SQL_LOOKUP_ENVIRONMENT, {"environment_name": args.environment_name})
            row = cur.fetchone()
            if not row:
                cur.execute(SQL_LIST_ENVIRONMENTS)
                envs = cur.fetchall()
                names = [r["name"] for r in envs]
                die(
                    f"Environment '{args.environment_name}' not found.\n"
                    f"  Available environments: {names}\n"
                    f"  Pass --environment-name with one of those, or --environment-id with the UUID."
                )
            environment_id = row["id"]
            environment_name = row["name"]

        metadata = {}
        if args.auto_deploy_staging:
            metadata["auto_deploy_staging"] = True
            if args.staging_environment:
                metadata["staging_environment"] = args.staging_environment
        metadata_json = json.dumps(metadata)

        params = {
            "repo_coordinate": args.repo_coordinate,
            "workflow_path":   args.workflow_path,
            "branch_pattern":  args.branch_pattern or None,
            "service_id":      service_id,
            "environment_id":  environment_id,
            "metadata":        metadata_json,
        }

        if args.dry_run:
            # Render interpolated SQL for the operator to review/run manually
            print("\n=== DRY RUN — SQL to execute ===\n")
            sql = f"""\
INSERT INTO hiveci_pipeline_policies
    (repo_coordinate, workflow_path, branch_pattern,
     service_id, environment_id, enabled, metadata)
VALUES
    ('{args.repo_coordinate}',
     '{args.workflow_path}',
     {("'" + args.branch_pattern + "'") if args.branch_pattern else "NULL"},
     '{service_id}'::uuid,
     '{environment_id}'::uuid,
     TRUE,
     '{metadata_json}'::jsonb)
ON CONFLICT DO NOTHING
RETURNING id, repo_coordinate, workflow_path, branch_pattern,
          service_id, environment_id, enabled, metadata;
"""
            print(sql)
            print(f"-- service resolved: {service_name} ({service_id})")
            print(f"-- environment resolved: {environment_name} ({environment_id})")
            print("\nRun without --dry-run to apply, or pipe the SQL above to psql.")
            return

        # Apply
        cur.execute(SQL_INSERT_POLICY, params)
        inserted = cur.fetchone()

        if inserted:
            print(f"\n✓ Policy inserted:")
            print(f"  id:              {inserted['id']}")
            print(f"  repo_coordinate: {inserted['repo_coordinate']}")
            print(f"  workflow_path:   {inserted['workflow_path']}")
            print(f"  branch_pattern:  {inserted['branch_pattern'] or '(any)'}")
            print(f"  service:         {service_name} ({inserted['service_id']})")
            print(f"  environment:     {environment_name} ({inserted['environment_id']})")
            print(f"  enabled:         {inserted['enabled']}")
            print(f"  metadata:        {inserted['metadata']}")
        else:
            # ON CONFLICT DO NOTHING — row already existed, show it
            cur.execute(SQL_SELECT_EXISTING, {
                "repo_coordinate": args.repo_coordinate,
                "workflow_path":   args.workflow_path,
            })
            existing = cur.fetchall()
            print("\n~ Policy already exists (no change made):")
            print_table(existing, ["id", "repo_coordinate", "workflow_path",
                                   "service_name", "environment_name", "enabled", "metadata"])

        conn.commit()


# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------

def build_parser():
    p = argparse.ArgumentParser(
        description=__doc__,
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )

    p.add_argument(
        "--db-url",
        default=os.environ.get("BAHIA_DATABASE_URL", ""),
        help=(
            "PostgreSQL connection string "
            "(default: $BAHIA_DATABASE_URL). "
            "Example: postgres://bahia:bahia@localhost:5432/bahia"
        ),
    )
    p.add_argument(
        "--list",
        action="store_true",
        help="Show current policies, seen repo coordinates, services, and environments then exit.",
    )
    p.add_argument(
        "--dry-run",
        action="store_true",
        help="Print the INSERT SQL without executing it.",
    )
    p.add_argument(
        "--repo-coordinate",
        help=(
            "NIP-34 repository coordinate from grasp-gitea's kind-5401 'a' tag. "
            "Format: '30617:<grasp-gitea-pubkey>:<repo-slug>'. "
            "Use --list to see coordinates already ingested from 5401 events."
        ),
    )
    p.add_argument(
        "--workflow-path",
        default=WORKFLOW_PATH_DEFAULT,
        help=f"Workflow file path to match (default: {WORKFLOW_PATH_DEFAULT}).",
    )
    p.add_argument(
        "--branch-pattern",
        default=None,
        help="Optional branch glob to restrict the policy (default: matches any branch).",
    )

    svc = p.add_mutually_exclusive_group()
    svc.add_argument("--service-name", help="Bahia service name to look up (e.g. 'bahia').")
    svc.add_argument("--service-id",   help="Bahia service UUID (skips name lookup).")

    env = p.add_mutually_exclusive_group()
    env.add_argument("--environment-name", help="Bahia environment name to look up (e.g. 'edge-01').")
    env.add_argument("--environment-id",   help="Bahia environment UUID (skips name lookup).")

    p.add_argument(
        "--auto-deploy-staging",
        action="store_true",
        default=False,
        help=(
            "Set metadata.auto_deploy_staging=true. "
            "Only enable after artifact registration is verified stable (runbook step 8)."
        ),
    )
    p.add_argument(
        "--staging-environment",
        default=None,
        help="Set metadata.staging_environment to this name when --auto-deploy-staging is set.",
    )
    return p


def main():
    parser = build_parser()
    args = parser.parse_args()

    db_url = args.db_url
    if not db_url:
        die(
            "No database URL provided.  Set BAHIA_DATABASE_URL or pass --db-url.\n"
            "  Example: --db-url 'postgres://bahia:bahia@localhost:5432/bahia'"
        )

    conn, CursorClass = connect(db_url)

    try:
        if args.list:
            cmd_list(conn, CursorClass)
            return

        # Seed mode — validate required args
        if not args.repo_coordinate:
            parser.error(
                "--repo-coordinate is required for seeding.\n"
                "  Use --list to see coordinates already ingested from 5401 events,\n"
                "  or check what grasp-gitea is publishing."
            )
        if not args.service_name and not args.service_id:
            parser.error("One of --service-name or --service-id is required.")
        if not args.environment_name and not args.environment_id:
            parser.error("One of --environment-name or --environment-id is required.")

        cmd_seed(args, conn, CursorClass)

    finally:
        conn.close()


if __name__ == "__main__":
    main()
