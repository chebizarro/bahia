#!/usr/bin/env bash
# Live interop test: Bahia's NIP-55L keyer (internal/adapters/nip55l) against
# the nostrc reference nostr-signer-daemon, fully isolated.
#
#   * a private dbus-daemon (no service directories, so nothing is
#     D-Bus-activated; the user's session bus is never used)
#   * the daemon under `env -i` with a throwaway HOME / XDG_* tree
#   * a libsecret-backed daemon whose Secret Service is the Go test itself
#     (an in-memory store), and keys generated fresh by the test
#   * on macOS, a daemon that links the Keychain (SecItem*) is refused:
#     its Keychain is the user's real login keychain
#
# Usage:
#   NOSTRC_SRC=/path/to/nostrc scripts/nip55l_live_test.sh
#   NOSTR_SIGNER_DAEMON=/path/to/libsecret-build/nostr-signer-daemon scripts/nip55l_live_test.sh
#
# Environment:
#   NOSTR_SIGNER_DAEMON   daemon to test; built from NOSTRC_SRC when unset
#   NOSTRC_SRC            nostrc checkout, built out of tree (read-only use)
#   NIP55L_LIVE_BUILD_DIR reuse this build directory (default: inside the
#                         run's temp dir, removed afterwards)
#   LEGACY_SIGNER_DAEMON  pre-0.4.0 daemon to check that New refuses it
#                         (default /opt/homebrew/bin/nostr-signer-daemon or
#                         /usr/bin/nostr-signer-daemon if present; "none" skips)
#   KEEP_TMP=1            keep the temp dir (logs) after the run
set -euo pipefail

repo=$(cd "$(dirname "$0")/.." && pwd)
tmp=$(mktemp -d /tmp/nip55l-live.XXXXXX) # short: unix socket paths are length-limited
pids=()

cleanup() {
  local status=$?
  for pid in "${pids[@]+"${pids[@]}"}"; do
    kill "$pid" 2>/dev/null || true
  done
  for pid in "${pids[@]+"${pids[@]}"}"; do
    wait "$pid" 2>/dev/null || true
  done
  if [[ $status -ne 0 ]]; then
    for log in "$tmp"/*.log; do
      [[ -f $log ]] || continue
      echo "---- $log (tail)" >&2
      tail -n 40 "$log" >&2
    done
  fi
  if [[ ${KEEP_TMP:-} == 1 ]]; then
    echo "kept $tmp" >&2
  else
    rm -rf "$tmp"
  fi
  exit "$status"
}
trap cleanup EXIT INT TERM

die() { echo "nip55l_live_test: $*" >&2; exit 1; }

find_tool() {
  command -v "$1" 2>/dev/null || { [[ -x /opt/homebrew/bin/$1 ]] && echo "/opt/homebrew/bin/$1"; } || die "$1 not found"
}
dbus_daemon=$(find_tool dbus-daemon)
dbus_send=$(find_tool dbus-send)

# links_keychain BINARY: true when the binary imports the macOS Keychain API.
links_keychain() {
  [[ $(uname -s) == Darwin ]] && nm -u "$1" 2>/dev/null | grep -q '_SecItem'
}

# --- the daemon under test ---------------------------------------------------
daemon=${NOSTR_SIGNER_DAEMON:-}
if [[ -z $daemon ]]; then
  [[ -n ${NOSTRC_SRC:-} ]] || die "set NOSTR_SIGNER_DAEMON or NOSTRC_SRC"
  build=${NIP55L_LIVE_BUILD_DIR:-$tmp/nostrc-build}
  if [[ ! -x $build/nips/nip55l/nostr-signer-daemon ]]; then
    echo "building nostr-signer-daemon (libsecret backend, test build) in $build"
    cmake -S "$NOSTRC_SRC" -B "$build" -G Ninja -DCMAKE_BUILD_TYPE=RelWithDebInfo \
      -DBUILD_TESTING=ON -DNIP55L_SECRET_BACKEND=libsecret \
      -DBUILD_APPS=OFF -DENABLE_APPS=OFF -DBUILD_GNOSTR_APP=OFF -DBUILD_GROUNDHOG=OFF \
      -DBUILD_LIBHANAMI=OFF -DBUILD_LIBMARMOT=OFF -DBUILD_MARMOT_GOBJECT=OFF \
      -DBUILD_NOSTR_GTK=OFF -DBUILD_RELAYD=OFF -DBUILD_NATIVE_HOST=OFF >"$tmp/cmake.log" 2>&1
    ninja -C "$build" nostr-signer-daemon >"$tmp/ninja.log" 2>&1
  fi
  daemon=$build/nips/nip55l/nostr-signer-daemon
fi
[[ -x $daemon ]] || die "no daemon at $daemon"
if links_keychain "$daemon"; then
  die "$daemon uses the macOS Keychain (the user's login keychain); build it with -DNIP55L_SECRET_BACKEND=libsecret"
fi

# --- isolation helpers -------------------------------------------------------
cat >"$tmp/bus.conf" <<CONF
<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN"
 "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <type>session</type>
  <listen>unix:dir=$tmp</listen>
  <auth>EXTERNAL</auth>
  <policy context="default">
    <allow send_destination="*" eavesdrop="true"/>
    <allow eavesdrop="true"/>
    <allow own="*"/>
  </policy>
</busconfig>
CONF

# isolated_env NAME: the environment for a daemon or test run named NAME.
isolated_env() {
  local root=$tmp/$1
  mkdir -p "$root/home" "$root/config" "$root/data" "$root/cache" "$root/runtime"
  chmod 700 "$root/runtime"
  echo PATH=/usr/bin:/bin:/usr/sbin:/sbin:/opt/homebrew/bin HOME="$root/home" \
    XDG_CONFIG_HOME="$root/config" XDG_DATA_HOME="$root/data" XDG_CACHE_HOME="$root/cache" \
    XDG_RUNTIME_DIR="$root/runtime" XDG_DATA_DIRS="$root/data" XDG_CONFIG_DIRS="$root/config" \
    TMPDIR="$tmp" SECRET_BACKEND=service
}

bus_address=""
# start_bus NAME: a private dbus-daemon; sets bus_address.
start_bus() {
  local addr_file=$tmp/$1.bus-address
  # shellcheck disable=SC2046
  env -i $(isolated_env "$1") "$dbus_daemon" --config-file="$tmp/bus.conf" --nofork \
    --print-address=3 3>"$addr_file" 2>"$tmp/$1-dbus.log" &
  pids+=("$!")
  for _ in $(seq 100); do
    [[ -s $addr_file ]] && break
    sleep 0.05
  done
  bus_address=$(head -n1 "$addr_file")
  [[ -n $bus_address ]] || die "dbus-daemon printed no address"
}

# start_signer NAME BINARY [VAR=VALUE...]: the daemon on the private bus,
# waiting until it owns org.nostr.Signer.
start_signer() {
  local name=$1 bin=$2
  shift 2
  # shellcheck disable=SC2046
  env -i $(isolated_env "$name") DBUS_SESSION_BUS_ADDRESS="$bus_address" "$@" \
    "$bin" >"$tmp/$name-daemon.log" 2>&1 &
  local pid=$!
  pids+=("$pid")
  for _ in $(seq 100); do
    "$dbus_send" --bus="$bus_address" --print-reply=literal --dest=org.freedesktop.DBus \
      /org/freedesktop/DBus org.freedesktop.DBus.NameHasOwner string:org.nostr.Signer 2>/dev/null |
      grep -q true && return 0
    kill -0 "$pid" 2>/dev/null || die "$bin exited at startup"
    sleep 0.05
  done
  die "$bin did not acquire org.nostr.Signer"
}

stop_all() {
  for pid in "${pids[@]+"${pids[@]}"}"; do kill "$pid" 2>/dev/null || true; done
  for pid in "${pids[@]+"${pids[@]}"}"; do wait "$pid" 2>/dev/null || true; done
  pids=()
}

echo "compiling the live test binary"
(cd "$repo" && go test -c -tags nip55llive -o "$tmp/nip55l.test" ./internal/adapters/nip55l)

run_tests() {
  local name=$1 pattern=$2
  shift 2
  # shellcheck disable=SC2046
  env -i $(isolated_env "$name-test") NIP55L_LIVE_BUS_ADDRESS="$bus_address" "$@" \
    "$tmp/nip55l.test" -test.run "$pattern" -test.count=1 -test.v -test.timeout=5m
}

# --- phase 1: a legacy daemon is refused --------------------------------------
legacy=${LEGACY_SIGNER_DAEMON:-}
if [[ -z $legacy ]]; then
  for cand in /opt/homebrew/bin/nostr-signer-daemon /usr/bin/nostr-signer-daemon; do
    [[ -x $cand ]] && { legacy=$cand; break; }
  done
fi
if [[ -n $legacy && $legacy != none ]]; then
  if strings "$legacy" | grep -q '^ListIdentities$'; then
    echo "skipping legacy check: $legacy is not a pre-0.4.0 daemon"
  else
    # Only unknown methods are called (New's probes), so no handler runs; the
    # env key keeps even GetPublicKey off any keystore.
    echo "== legacy daemon: $legacy"
    start_bus legacy
    start_signer legacy "$legacy" NOSTR_SIGNER_SECKEY_HEX="$(openssl rand -hex 32)"
    run_tests legacy '^TestLiveLegacyDaemonRejected$' NIP55L_LIVE_LEGACY=1
    stop_all
  fi
fi

# --- phase 2: the reference daemon, provisioned --------------------------------
echo "== reference daemon: $daemon"
start_bus current
start_signer current "$daemon" NOSTR_SIGNER_ALLOW_KEY_MUTATIONS=1 NOSTR_SIGNER_TEST_NO_MIGRATION=1
run_tests current '^TestLive(Selectors|Keyer|ServiceSignerOpen)$' \
  NIP55L_LIVE_PROVISION=1 NIP55L_LIVE_GRANTS_FILE="$tmp/current/config/gnostr/signer-grants.ini"
stop_all

# --- phase 3: an env-only key (NOSTR_SIGNER_SECKEY_HEX, nothing stored) --------
echo "== reference daemon, env-only key"
env_sk=$(openssl rand -hex 32)
start_bus envkey
start_signer envkey "$daemon" NOSTR_SIGNER_SECKEY_HEX="$env_sk" NOSTR_SIGNER_TEST_NO_MIGRATION=1
run_tests envkey '^TestLiveEnvKey$' \
  NIP55L_LIVE_ENV_SECKEY="$env_sk" NIP55L_LIVE_GRANTS_FILE="$tmp/envkey/config/gnostr/signer-grants.ini"
stop_all
echo "nip55l live test: PASS"
