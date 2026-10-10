#!/usr/bin/env python3
"""Opt-in live interop: Bahia's NIP-46 service signer against a real signetd.

Bahia opens its service identity with servicesigner.Open and
nostr.signer.method=nip46; Signet is only the bunker under test. Inputs are a
Signet (nostrc) checkout, its CMake build directory and the full Signet commit
the operator pinned for review. The runner builds signetd and signetctl from
that exact clean commit, starts them on a private loopback relay with a
disposable encrypted store and synthetic keys, adopts a synthetic existing
service key (`adopt-existing`, so the pubkey is preserved) and runs Bahia's
opt-in `signetinterop` tests from this (clean) Bahia checkout:

  1. TestLiveNIP46AssignedWriterSigns: the first dedicated client key,
     assigned with `signetctl writer-acquire`, opens the signer and signs.
  2. The writer is reassigned to a second, single-use client key.
  3. TestLiveNIP46ServiceSigner: a wrong nostr.public_key is fatal at open;
     the new writer signs, NIP-44 encrypts/decrypts (text and binary) and
     signs an SBOM attestation event; the displaced writer still opens but
     every key operation is refused remotely.
  4. TestLiveNIP46Reconnect, in a new process (a restart): the writer
     reconnects with the connect secret it already spent, then again in the
     same process (a reload, which omits the spent secret) and with the
     secret removed from the URI, and signs each time; a secret spent by
     another client and an unpaired client without a secret are refused
     promptly with an explained error rather than a timeout.
  5. TestLiveAssistantWrappedStartupHistoricalReads: wrapped read-only
     assistant startup through the NIP-46 service signer.

Never accepts an existing identity, relay URL or database. No secret, pairing
URI or raw test output is written to stdout or stderr; on failure only the
failing test line number is reported.
"""
import argparse
import json
import os
from pathlib import Path
import re
import secrets
import socket
import subprocess
import sys
import tempfile
import time
from urllib.parse import parse_qsl, urlencode, urlsplit, urlunsplit

ASSIGNED_TEST = "TestLiveNIP46AssignedWriterSigns"
SIGNER_TEST = "TestLiveNIP46ServiceSigner"
RECONNECT_TEST = "TestLiveNIP46Reconnect"
APP_TEST = "TestLiveAssistantWrappedStartupHistoricalReads"
SIGNER_PACKAGE = "./internal/servicesigner"
SIGNER_SOURCE = "nip46_live_integration_test.go"
APP_PACKAGE = "./internal/app"
AGENT_ID = "bahia-interop"
# Standard NIP-46 methods Bahia's service signer uses, plus the binary-safe
# NIP-44 pair (nip44_*_b64) behind its optional BinaryCipher capability.
ALLOWED_METHODS = ("connect, get_public_key, sign_event, nip44_encrypt, nip44_decrypt, "
                   "nip44_encrypt_b64, nip44_decrypt_b64")
COMMIT_PATTERN = re.compile(r"^[0-9a-f]{40}$")
HEX_KEY_PATTERN = re.compile(r"^[0-9a-f]{64}$")


def run(argv, *, env=None, cwd=None, input_text=None):
    result = subprocess.run(argv, env=env, cwd=cwd, input=input_text, text=True, capture_output=True)
    if result.returncode:
        raise RuntimeError(f"{Path(argv[0]).name} failed (exit {result.returncode}); private output withheld")
    return result.stdout


def private_file(path, value):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w") as stream:
        stream.write(value)
        stream.flush()
        os.fsync(stream.fileno())


def pubkey(secret):
    """Derive a public key without putting the secret in an argument vector."""
    result = subprocess.run(["nak", "key", "public"], input=secret + "\n", text=True, capture_output=True)
    if result.returncode:
        raise RuntimeError("nak failed to derive synthetic public key; private output withheld")
    value = result.stdout.strip()
    if not HEX_KEY_PATTERN.match(value):
        raise RuntimeError("nak did not return a hex public key")
    return value


def require_commit(value, label):
    if not COMMIT_PATTERN.match(value or ""):
        raise RuntimeError(f"{label} must be a full lowercase 40-character commit SHA")
    return value


def require_clean_checkout(repo, label, expected_commit=None):
    head = run(["git", "-C", str(repo), "rev-parse", "HEAD"]).strip()
    if expected_commit is not None and head != expected_commit:
        raise RuntimeError(f"{label} checkout is not at the pinned commit")
    if run(["git", "-C", str(repo), "status", "--porcelain"]).strip():
        raise RuntimeError(f"{label} checkout has uncommitted changes; commit before interop so the result identifies it")
    return require_commit(head, f"{label} HEAD")


def require_live_test_pass(output, named_test):
    """Require exactly one Run and one Pass for named_test in go test -json output.

    Rejects an empty, skipped, failed, repeated or substituted test even when
    the process exited zero.
    """
    state = "absent"
    for line in output.splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError as exc:
            raise RuntimeError("go test did not emit complete JSON events") from exc
        if not isinstance(event, dict) or event.get("Test") != named_test:
            continue
        action = event.get("Action")
        if action == "run":
            if state != "absent":
                raise RuntimeError(f"{named_test} ran more than once")
            state = "running"
        elif action == "pass":
            if state != "running":
                raise RuntimeError(f"{named_test} passed without one Run event")
            state = "passed"
        elif action in ("skip", "fail"):
            raise RuntimeError(f"{named_test} was skipped or failed")
    if state != "passed":
        raise RuntimeError(f"{named_test} did not emit Run and Pass events")


def failure_location(text, source):
    locations = re.findall(re.escape(source) + r":(\d+)", text)
    return f" at {source}:{locations[0]}" if locations else ""


def with_connect_secret(bunker_uri, secret):
    """Return bunker_uri with its connect secret replaced by secret."""
    parts = urlsplit(bunker_uri)
    query = [(key, value) for key, value in parse_qsl(parts.query, keep_blank_values=True) if key != "secret"]
    query.append(("secret", secret))
    return urlunsplit((parts.scheme, parts.netloc, parts.path, urlencode(query), parts.fragment))


def require_loopback_bunker(bunker_uri, bunker_pk, relay_url):
    parts = urlsplit(bunker_uri)
    relays = [value for key, value in parse_qsl(parts.query) if key == "relay"]
    if parts.scheme != "bunker" or parts.hostname != bunker_pk or relays != [relay_url]:
        raise RuntimeError("pairing URI does not pin the bunker pubkey and only the private loopback relay")


def free_loopback_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def wait_tcp(port, process, label):
    for _ in range(100):
        if process.poll() is not None:
            raise RuntimeError(f"{label} exited during startup; private log withheld")
        try:
            with socket.create_connection(("127.0.0.1", port), timeout=0.2):
                return
        except OSError:
            time.sleep(0.1)
    raise RuntimeError(f"{label} did not listen on loopback")


def management(ctl, config, env, *args, input_text=None):
    output = run([str(ctl), "-c", str(config), *args], env=env, input_text=input_text)
    marker = "Reply received:\n"
    if marker not in output:
        raise RuntimeError("management acknowledgement missing")
    reply = json.loads(output.split(marker, 1)[1])
    if not isinstance(reply, dict) or not isinstance(reply.get("result"), dict):
        raise RuntimeError("management acknowledgement has no result")
    result = reply["result"]
    # Signet wraps a command result under a second result object.
    if isinstance(result.get("result"), dict):
        result = result["result"]
    return result


def run_go_test(bahia, env, package, named_test, source):
    result = subprocess.run(["go", "test", "-tags", "signetinterop", package, "-run", f"^{named_test}$",
                             "-count=1", "-json"], cwd=bahia, env=env, text=True, capture_output=True)
    # The Go test may print errors containing its fixture URI. Never echo raw output.
    if result.returncode:
        location = failure_location(result.stdout + result.stderr, source)
        raise RuntimeError(f"Bahia {named_test} failed{location}; private output withheld")
    require_live_test_pass(result.stdout, named_test)


def run_app_test(bahia, env, root, service_sk):
    """Run the app test binary with the synthetic service key on stdin only."""
    binary = root / "bahia-app.test"
    run(["go", "test", "-c", "-tags", "signetinterop", "-o", str(binary), APP_PACKAGE], cwd=bahia, env=env)
    app = subprocess.run([str(binary), "-test.run", f"^{APP_TEST}$", "-test.count=1", "-test.v=test2json"],
                         cwd=bahia / APP_PACKAGE, env=env, input=service_sk + "\n", text=True, capture_output=True)
    if app.returncode:
        location = failure_location(app.stdout + app.stderr, "assistant_wrapped_live_integration_test.go")
        raise RuntimeError(f"Bahia {APP_TEST} failed{location}; private output withheld")
    events = run(["go", "tool", "test2json", "-p", "app"], cwd=bahia, env=env, input_text=app.stdout)
    require_live_test_pass(events, APP_TEST)


def write_fixture(path, **fields):
    private_file(path, json.dumps(dict(disposable=True, **fields)) + "\n")


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--signet-repo", type=Path, required=True,
                        help="nostrc checkout containing signet/ at the pinned commit")
    parser.add_argument("--build-dir", type=Path, required=True,
                        help="CMake build directory configured from --signet-repo")
    parser.add_argument("--signet-commit", required=True,
                        help="reviewed full Signet (nostrc) commit SHA expected at --signet-repo")
    args = parser.parse_args(argv)
    bahia = Path(__file__).resolve().parents[1]
    signet_repo = args.signet_repo.resolve()
    build = args.build_dir.resolve()
    signet_commit = require_commit(args.signet_commit, "--signet-commit")

    for executable in ("nak", "go", "cmake", "git"):
        if subprocess.run(["which", executable], capture_output=True).returncode:
            raise RuntimeError(f"{executable} is required")
    if not (bahia / SIGNER_PACKAGE / SIGNER_SOURCE).is_file() or \
            not (bahia / APP_PACKAGE / "assistant_wrapped_live_integration_test.go").is_file():
        raise RuntimeError("Bahia checkout lacks the opt-in NIP-46 interop tests")
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
    # Rebuilding must not have changed the pinned source.
    require_clean_checkout(signet_repo, "Signet", signet_commit)

    # No operator-provided keys or endpoints: this script cannot target production.
    with tempfile.TemporaryDirectory(prefix="bahia-signet-interop-") as temp:
        root = Path(temp)
        os.chmod(root, 0o700)
        relay_port = free_loopback_port()
        relay_url = f"ws://127.0.0.1:{relay_port}"
        bunker_sk, provisioner_sk, service_sk, displaced_sk, writer_sk = (secrets.token_hex(32) for _ in range(5))
        bunker_pk, provisioner_pk, service_pk, displaced_pk, writer_pk = map(
            pubkey, (bunker_sk, provisioner_sk, service_sk, displaced_sk, writer_sk))
        if len({bunker_pk, provisioner_pk, service_pk, displaced_pk, writer_pk}) != 5:
            raise RuntimeError("test roles unexpectedly share a key")
        private_file(root / "bunker.key", bunker_sk + "\n")
        private_file(root / "provisioner.key", provisioner_sk + "\n")
        policy = root / "policies.toml"
        private_file(policy,
                     f'[identity.{AGENT_ID}]\n'
                     'allow_clients = "*"\n'
                     f'allow_methods = "{ALLOWED_METHODS}"\n'
                     'allow_kinds = "*"\n'
                     'default = "allow"\n')
        config = root / "signet.conf"
        private_file(config,
                     '[server]\nlog_level = info\nhealth_port = 0\n'
                     f'[store]\ndb_path = {root / "store.db"}\n'
                     f'[nostr]\nrelays = {relay_url}\nidentity = {AGENT_ID}\n'
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
                signetd = subprocess.Popen([str(daemon), "-c", str(config)],
                                           stdout=daemon_log, stderr=subprocess.STDOUT, env=env)
                processes.append(signetd)
                # Health is an authenticated management roundtrip, not a sleep-based guess.
                for _ in range(30):
                    if signetd.poll() is not None:
                        raise RuntimeError("signetd exited during startup; private log withheld")
                    try:
                        management(ctl, config, env, "status")
                        break
                    except RuntimeError:
                        time.sleep(0.2)
                else:
                    raise RuntimeError("signetd did not answer authenticated management status")

                adopted = run([str(ctl), "-c", str(config), "adopt-existing", AGENT_ID,
                               "--sec", "-", "--expected-pubkey", service_pk],
                              env=env, input_text=service_sk + "\n")
                fields = dict(line.split(": ", 1) for line in adopted.splitlines()
                              if ": " in line and line.split(": ", 1)[0] in ("pubkey", "bunker_uri"))
                if fields.get("pubkey") != service_pk or not fields.get("bunker_uri"):
                    raise RuntimeError("adoption reply lacked the synthetic public identity or pairing URI")
                first_uri = fields["bunker_uri"]
                require_loopback_bunker(first_uri, bunker_pk, relay_url)
                common = dict(signet_commit=signet_commit, expected_bunker_pubkey=bunker_pk,
                              expected_service_pubkey=service_pk)
                test_env = env.copy()

                # Phase 1: the first assigned writer pairs and signs.
                management(ctl, config, env, "writer-acquire", AGENT_ID, displaced_pk)
                first_fixture = root / "fixture-assigned.json"
                write_fixture(first_fixture, writer_bunker_uri=first_uri, writer_secret_key_hex=displaced_sk, **common)
                test_env["BAHIA_SIGNET_INTEROP_CONFIG"] = str(first_fixture)
                run_go_test(bahia, test_env, SIGNER_PACKAGE, ASSIGNED_TEST, SIGNER_SOURCE)

                # Phase 2: pair a second client and reassign the writer to it.
                # Connect secrets are one-time; reissue writes the new one 0600.
                secret_file = root / "writer.connect-secret"
                run([str(ctl), "-c", str(config), "reissue-connect", AGENT_ID, "--out", str(secret_file)], env=env)
                writer_uri = with_connect_secret(first_uri, secret_file.read_text().strip())
                require_loopback_bunker(writer_uri, bunker_pk, relay_url)
                management(ctl, config, env, "writer-acquire", AGENT_ID, writer_pk)
                fixture = root / "fixture-reassigned.json"
                write_fixture(fixture, writer_bunker_uri=writer_uri, writer_secret_key_hex=writer_sk,
                              displaced_bunker_uri=first_uri, displaced_writer_secret_key_hex=displaced_sk, **common)
                test_env["BAHIA_SIGNET_INTEROP_CONFIG"] = str(fixture)
                run_go_test(bahia, test_env, SIGNER_PACKAGE, SIGNER_TEST, SIGNER_SOURCE)
                # Phase 3: the same client keys connect again from a new process.
                run_go_test(bahia, test_env, SIGNER_PACKAGE, RECONNECT_TEST, SIGNER_SOURCE)

                # Phase 4: wrapped read-only assistant startup through the NIP-46 signer.
                run_app_test(bahia, test_env, root, service_sk)
                print(f"PASS: Bahia {bahia_commit} NIP-46 service signer interop against Signet {signet_commit} "
                      f"({ASSIGNED_TEST}, {SIGNER_TEST}, {RECONNECT_TEST}, {APP_TEST}) on a disposable loopback signetd")
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
        print(f"signet interop: {exc}", file=sys.stderr)
        sys.exit(1)
