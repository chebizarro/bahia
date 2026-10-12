#!/usr/bin/env python3
"""Opt-in live proof: Bahia's Signet secret store against a real signetd.

Inputs are a Signet (nostrc) checkout, its CMake build directory and the full
Signet commit under review. The runner builds signetd and signetctl from that
exact clean commit, starts them on a private loopback relay with a disposable
encrypted store and synthetic keys, and sets up what
docs/runbooks/signet-secret-store.md describes:

  - an owner agent (provision) that nobody connects to;
  - a requester key with no Signet rights but the owner's exact delivery
    grant: the single client of its credential.deliver policy;
  - one imported credential.

It then runs Bahia's opt-in TestLiveSignetCredentialStore (-tags
signetinterop) from this clean Bahia checkout: resolve the pinned version,
then resolve in one round trip (Signet reports the delivered version and
honours expected_version), refuse another tenant's credential held by the
same owner, refuse the old pin after a rotation and resolve the new one
(also as a paced batch), refuse when the delivery grant names another
client, refuse after revocation.

Never accepts an existing identity, relay URL or database. No secret or raw
test output is written to stdout or stderr; on failure only the failing test
line number is reported.
"""
import argparse
import json
import os
from pathlib import Path
import secrets
import subprocess
import sys
import tempfile
import uuid

sys.path.insert(0, str(Path(__file__).resolve().parent))
from signet_live_interop import (  # noqa: E402  (shared guards of the NIP-46 interop runner)
    free_loopback_port, management, private_file, pubkey, require_clean_checkout, require_commit,
    run, run_go_test, wait_management, wait_tcp,
)

LIVE_TEST = "TestLiveSignetCredentialStore"
PACKAGE = "./internal/adapters/signet"
SOURCE = "credential_store_live_integration_test.go"
OWNER = "bahia-vault"


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--signet-repo", type=Path, required=True, help="nostrc checkout at the reviewed commit")
    parser.add_argument("--build-dir", type=Path, required=True, help="CMake build directory configured from --signet-repo")
    parser.add_argument("--signet-commit", required=True, help="reviewed full Signet (nostrc) commit SHA")
    args = parser.parse_args(argv)
    bahia = Path(__file__).resolve().parents[1]
    signet_repo = args.signet_repo.resolve()
    build = args.build_dir.resolve()
    signet_commit = require_commit(args.signet_commit, "--signet-commit")

    for executable in ("nak", "go", "cmake", "git"):
        if subprocess.run(["which", executable], capture_output=True).returncode:
            raise RuntimeError(f"{executable} is required")
    if not (bahia / PACKAGE / SOURCE).is_file():
        raise RuntimeError("Bahia checkout lacks the opt-in secret store live test")
    bahia_commit = require_clean_checkout(bahia, "Bahia")
    require_clean_checkout(signet_repo, "Signet", signet_commit)
    cache = build / "CMakeCache.txt"
    if not cache.is_file() or f"CMAKE_HOME_DIRECTORY:INTERNAL={signet_repo}" not in cache.read_text():
        raise RuntimeError("build directory must be configured from the pinned Signet checkout")
    run(["cmake", "--build", str(build), "--target", "signetd", "signetctl"])
    daemon = build / "signet/signetd"
    ctl = build / "signet/signetctl"
    if not daemon.is_file() or not ctl.is_file():
        raise RuntimeError("Signet daemon or management CLI was not built")
    require_clean_checkout(signet_repo, "Signet", signet_commit)

    with tempfile.TemporaryDirectory(prefix="bahia-signet-secret-store-") as temp:
        root = Path(temp)
        os.chmod(root, 0o700)
        relay_port = free_loopback_port()
        relay_url = f"ws://127.0.0.1:{relay_port}"
        bunker_sk, provisioner_sk, requester_sk, other_sk = (secrets.token_hex(32) for _ in range(4))
        bunker_pk, provisioner_pk, requester_pk, other_pk = map(pubkey, (bunker_sk, provisioner_sk, requester_sk, other_sk))
        if len({bunker_pk, provisioner_pk, requester_pk, other_pk}) != 4:
            raise RuntimeError("test roles unexpectedly share a key")
        private_file(root / "bunker.key", bunker_sk + "\n")
        private_file(root / "provisioner.key", provisioner_sk + "\n")
        value_v1, value_v2 = root / "value-v1", root / "value-v2"
        private_file(value_v1, "postgres://live:" + secrets.token_hex(16) + "@db/one")
        private_file(value_v2, "postgres://live:" + secrets.token_hex(16) + "@db/two")
        policy = root / "policies.toml"
        private_file(policy, "")
        config = root / "signet.conf"
        private_file(config,
                     '[server]\nlog_level = info\nhealth_port = 0\n'
                     f'[store]\ndb_path = {root / "store.db"}\n'
                     f'[nostr]\nrelays = {relay_url}\nidentity = bahia-secret-store-live\n'
                     f'bunker_pubkey = {bunker_pk}\n'
                     f'provisioner_pubkeys = {provisioner_pk}\n'
                     f'[policy_defaults]\ndefault_decision = deny\npolicy_file = {policy}\n'
                     f'[audit]\npath = {root / "audit.log"}\nstdout = false\n'
                     '[bootstrap]\nport = 0\n'
                     '[dbus]\nunix_enabled = false\ntcp_enabled = false\n'
                     '[nip5l]\nenabled = false\n[ssh_agent]\nenabled = false\n')
        env = os.environ.copy()
        env.update(SIGNET_DB_KEY=secrets.token_hex(32),
                   SIGNET_BUNKER_NSEC_FILE=str(root / "bunker.key"),
                   SIGNET_PROVISIONER_NSEC_FILE=str(root / "provisioner.key"))
        processes = []
        try:
            with open(root / "relay.log", "w") as relay_log, open(root / "daemon.log", "w") as daemon_log:
                relay = subprocess.Popen(["nak", "serve", "--hostname", "127.0.0.1", "--port", str(relay_port)],
                                         stdout=relay_log, stderr=subprocess.STDOUT, env=env)
                processes.append(relay)
                wait_tcp(relay_port, relay, "private relay")
                signetd = subprocess.Popen([str(daemon), "-c", str(config)], stdout=daemon_log, stderr=subprocess.STDOUT, env=env)
                processes.append(signetd)
                wait_management(ctl, config, env, signetd)

                run([str(ctl), "-c", str(config), "provision", OWNER], env=env)
                # The requester holds only the owner's exact delivery grant
                # (Signet >= 0.7.0); it is never a provisioner.
                management(ctl, config, env, "set-policy", OWNER, json.dumps(
                    {"default": "deny", "allow_clients": [requester_pk], "allow_methods": ["credential.deliver"]}))
                # Bahia binds a credential to its tenant by label:
                # <org-id>/<service-id>/<name>.
                org_id, service_id = str(uuid.uuid4()), str(uuid.uuid4())
                imported = management(ctl, config, env, "import-credential", OWNER, "--type", "credential",
                                      "--label", f"{org_id}/{service_id}/DATABASE_URL", "--file", str(value_v1))
                credential_id = imported.get("credential_id", "")
                if not credential_id.startswith("cred_") or imported.get("version") != 1:
                    raise RuntimeError("import-credential did not report a version-1 credential id")
                foreign = management(ctl, config, env, "import-credential", OWNER, "--type", "credential",
                                     "--label", f"{uuid.uuid4()}/{uuid.uuid4()}/DATABASE_URL", "--file", str(value_v1))
                foreign_id = foreign.get("credential_id", "")
                if not foreign_id.startswith("cred_"):
                    raise RuntimeError("import-credential did not report the other tenant's credential id")

                fixture = root / "fixture.json"
                private_file(fixture, json.dumps(dict(
                    disposable=True, signet_commit=signet_commit, relay_url=relay_url, signet_pubkey=bunker_pk,
                    requester_secret_key_hex=requester_sk, owner=OWNER, credential_id=credential_id,
                    org_id=org_id, service_id=service_id, foreign_credential_id=foreign_id,
                    value_v1_file=str(value_v1), value_v2_file=str(value_v2), signetctl=str(ctl),
                    signet_config=str(config), other_client_pubkey=other_pk)) + "\n")
                test_env = env.copy()
                test_env["BAHIA_SIGNET_SECRET_STORE_CONFIG"] = str(fixture)
                run_go_test(bahia, test_env, PACKAGE, LIVE_TEST, SOURCE)
                print(f"PASS: Bahia {bahia_commit} Signet secret store against Signet {signet_commit} "
                      f"({LIVE_TEST}) on a disposable loopback signetd")
        finally:
            for process in reversed(processes):
                process.terminate()
            for process in reversed(processes):
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()


if __name__ == "__main__":
    try:
        main()
    except (OSError, RuntimeError, ValueError, json.JSONDecodeError) as exc:
        print(f"signet secret store live: {exc}", file=sys.stderr)
        sys.exit(1)
