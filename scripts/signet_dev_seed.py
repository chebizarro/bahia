#!/usr/bin/env python3
"""Seed a disposable Signet for Bahia: the credential owner, the requester's
delivery grant and the daemon's system credentials.

One code path for every disposable Signet Bahia runs against:

  - scripts/signet_secret_store_live.py (the opt-in live proof) imports the
    functions below;
  - the dev compose stack and the joined assistant e2e harness run the
    deploy/signet-dev image, whose entrypoint is `signet_dev_seed.py serve`.

Requires Signet >= 0.7.0: the requester holds only the owner's exact
credential.deliver grant, not provisioner rights. Never point it at a Signet
that holds real credentials: `serve` generates every key it uses.

serve: generate (once, under --state) the Signet store key, bunker,
provisioner and requester keys and two 32-byte system keys; run a private
relay (nak serve) and signetd; on first start provision the owner, grant the
requester delivery and import the system credentials as
bahia-system/<purpose>/<key-id>; then write, under --shared, what Bahia needs
(signet.pub, requester.key, credentials.json) and a ready marker, and keep
both processes running.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import pwd
import secrets
import signal
import struct
import subprocess
import sys
import time

sys.path.insert(0, str(Path(__file__).resolve().parent))
from signet_live_interop import management, private_file, pubkey, run, wait_tcp  # noqa: E402

OWNER = "bahia-vault"
SYSTEM_PURPOSES = ("assistant-transcript", "confidential-dedupe")
CREDENTIAL_TYPE = "credential"


def credential_id(owner, label, credential_type=CREDENTIAL_TYPE):
    """Signet's credential id: BLAKE2b-256 over a domain and the
    length-prefixed owner, type and label (signet/src/store_secrets.c)."""
    digest = hashlib.blake2b(b"signet-credential-id-v1", digest_size=32)
    for value in (owner, credential_type, label):
        encoded = value.encode()
        digest.update(struct.pack(">I", len(encoded)) + encoded)
    return "cred_" + digest.hexdigest()


def system_label(purpose, key_id):
    return f"bahia-system/{purpose}/{key_id}"


def signet_config(root, relay_url, bunker_pk, provisioner_pk):
    """signetd configuration for a disposable store under root."""
    return ('[server]\nlog_level = info\nhealth_port = 0\n'
            f'[store]\ndb_path = {root / "store.db"}\n'
            f'[nostr]\nrelays = {relay_url}\nidentity = bahia-disposable-signet\n'
            f'bunker_pubkey = {bunker_pk}\n'
            f'provisioner_pubkeys = {provisioner_pk}\n'
            f'[policy_defaults]\ndefault_decision = deny\npolicy_file = {root / "policies.toml"}\n'
            f'[audit]\npath = {root / "audit.log"}\nstdout = false\n'
            '[bootstrap]\nport = 0\n'
            '[dbus]\nunix_enabled = false\ntcp_enabled = false\n'
            '[nip5l]\nenabled = false\n[ssh_agent]\nenabled = false\n')


def signet_env(root):
    """signetd/signetctl environment: the store key and the key files."""
    env = os.environ.copy()
    env.update(SIGNET_DB_KEY=(root / "db.key").read_text().strip(),
               SIGNET_BUNKER_NSEC_FILE=str(root / "bunker.key"),
               SIGNET_PROVISIONER_NSEC_FILE=str(root / "provisioner.key"))
    return env


def wait_management(ctl, config, env, signetd):
    """Readiness is an authenticated management round trip, not a guess."""
    for _ in range(50):
        if signetd.poll() is not None:
            raise RuntimeError("signetd exited during startup; private log withheld")
        try:
            management(ctl, config, env, "status")
            return
        except RuntimeError:
            time.sleep(0.2)
    raise RuntimeError("signetd did not answer authenticated management status")


def seed(ctl, config, env, requester_pk, system_keys, key_id):
    """Provision the owner, grant the requester exactly credential.deliver and
    import each system key file as bahia-system/<purpose>/<key_id>. Returns
    {purpose: credential_id}; fails unless Signet reports the expected id at
    version 1."""
    run([str(ctl), "-c", str(config), "provision", OWNER], env=env)
    management(ctl, config, env, "set-policy", OWNER, json.dumps(
        {"default": "deny", "allow_clients": [requester_pk], "allow_methods": ["credential.deliver"]}))
    ids = {}
    for purpose, key_file in system_keys.items():
        label = system_label(purpose, key_id)
        imported = management(ctl, config, env, "import-credential", OWNER, "--type", CREDENTIAL_TYPE,
                              "--label", label, "--file", str(key_file))
        if imported.get("credential_id") != credential_id(OWNER, label) or imported.get("version") != 1:
            raise RuntimeError(f"import-credential did not report the expected version-1 id for {label}")
        ids[purpose] = imported["credential_id"]
    return ids


def ensure_key(path, value):
    if not path.exists():
        private_file(path, value)


def ensure_binary_key(path):
    if not path.exists():
        with open(os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "wb") as handle:
            handle.write(secrets.token_bytes(32))


def write_output(path, value, uid):
    """Write a file Bahia reads: owner-only for uid when given; otherwise
    world-readable inside the dev-only shared volume, for a consumer that
    copies it into an owner-only file of its own."""
    tmp = path.with_suffix(".tmp")
    tmp.unlink(missing_ok=True)
    with open(os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "w") as handle:
        handle.write(value)
    if uid is None:
        os.chmod(tmp, 0o444)
    else:
        os.chown(tmp, uid, -1)
        os.chmod(tmp, 0o400)
    os.replace(tmp, path)


def serve(args):
    state, shared = Path(args.state), Path(args.shared)
    state.mkdir(parents=True, exist_ok=True)
    shared.mkdir(parents=True, exist_ok=True)
    (shared / "ready").unlink(missing_ok=True)
    keys = state / "keys"
    keys.mkdir(mode=0o700, exist_ok=True)
    for name in ("db", "bunker", "provisioner", "requester"):
        ensure_key(keys / f"{name}.key", secrets.token_hex(32) + "\n")
    for purpose in SYSTEM_PURPOSES:
        ensure_binary_key(keys / f"{purpose}.key")
    bunker_pk, provisioner_pk, requester_pk = (pubkey((keys / f"{name}.key").read_text().strip())
                                               for name in ("bunker", "provisioner", "requester"))
    relay_url = f"ws://127.0.0.1:{args.relay_port}"
    config = state / "signet.conf"
    config.write_text(signet_config(state, relay_url, bunker_pk, provisioner_pk))
    (state / "policies.toml").touch()
    # signetd reads its keys from the key directory.
    for name in ("db", "bunker", "provisioner"):
        target = state / f"{name}.key"
        target.unlink(missing_ok=True)
        os.link(keys / f"{name}.key", target)
    # signetd runs as args.user and owns its store and the keys it reads;
    # signetctl runs as this process's user and reads only files it owns
    # (the system key files and the requester key stay with it).
    user = pwd.getpwnam(args.user)
    for path in [state, config, state / "policies.toml", *(state / f"{name}.key" for name in ("db", "bunker", "provisioner"))]:
        os.chown(path, user.pw_uid, user.pw_gid)
    env = signet_env(state)

    processes = []

    def stop(*_):
        for process in reversed(processes):
            process.terminate()
        sys.exit(0)

    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    relay = subprocess.Popen(["nak", "serve", "--hostname", "0.0.0.0", "--port", str(args.relay_port)], user=args.user,
                             stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    processes.append(relay)
    wait_tcp(args.relay_port, relay, "private relay")
    signetd = subprocess.Popen(["signetd", "-c", str(config)], env=env, user=args.user)
    processes.append(signetd)
    wait_management("signetctl", config, env, signetd)

    seeded = state / "seeded.json"
    if not seeded.exists():
        ids = seed("signetctl", config, env, requester_pk,
                   {purpose: keys / f"{purpose}.key" for purpose in SYSTEM_PURPOSES}, args.key_id)
        seeded.write_text(json.dumps(ids))
    ids = json.loads(seeded.read_text())
    uid = args.output_uid
    write_output(shared / "signet.pub", bunker_pk + "\n", uid)
    write_output(shared / "requester.key", (keys / "requester.key").read_text(), uid)
    write_output(shared / "credentials.json", json.dumps(
        {"owner": OWNER, "key_id": args.key_id, "relay_port": args.relay_port, "credentials": ids}) + "\n", uid)
    write_output(shared / "ready", "", uid)
    print(f"disposable Signet ready: pubkey {bunker_pk}, owner {OWNER}, relay port {args.relay_port}", flush=True)
    # Block until either child exits; the container then stops with it.
    pid, status = os.wait()
    for process in processes:
        process.terminate()
    raise RuntimeError(f"child process {pid} exited (status {status})")


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    commands = parser.add_subparsers(dest="command", required=True)
    serve_args = commands.add_parser("serve", help="run a seeded disposable Signet")
    serve_args.add_argument("--state", required=True, help="persistent directory for keys and the Signet store")
    serve_args.add_argument("--shared", required=True, help="directory Bahia reads signet.pub, requester.key and credentials.json from")
    serve_args.add_argument("--relay-port", type=int, default=7777)
    serve_args.add_argument("--key-id", default="dev", help="key id of the system credentials")
    serve_args.add_argument("--user", default="signet", help="user signetd and the relay run as")
    serve_args.add_argument("--output-uid", type=int, help="owner of the shared files (mode 0400); unset: mode 0444")
    args = parser.parse_args(argv)
    serve(args)


if __name__ == "__main__":
    try:
        main()
    except (OSError, RuntimeError, ValueError, json.JSONDecodeError, KeyError) as exc:
        print(f"signet dev seed: {exc}", file=sys.stderr)
        sys.exit(1)
