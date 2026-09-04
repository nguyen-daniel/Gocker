#!/usr/bin/env bash
# Recruiter-facing walkthrough: UTS, OverlayFS, pids.max, two detached IPs.
# Linux + root. Payloads stay visible (-q). Bridge failure is a fallback, not a lie.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

section() {
  printf '\n======== %s ========\n\n' "$1"
}

die() {
  echo "Error: $*" >&2
  exit 1
}

if [[ "$(uname -s)" != "Linux" ]]; then
  echo "Error: this demo requires Linux (clone namespaces, cgroups v2, OverlayFS)."
  echo "On Windows use WSL2 Ubuntu or GitHub Codespaces, then: sudo make demo"
  exit 1
fi

if [[ "$(id -u)" -ne 0 ]]; then
  if command -v sudo >/dev/null 2>&1; then
    echo "Re-running with sudo (namespaces / cgroups / overlay / bridge need root)..."
    exec sudo -E bash "$0" "$@"
  fi
  die "must run as root (sudo make demo)"
fi

if [[ ! -x ./gocker ]]; then
  die "./gocker not found or not executable. Run: make build"
fi

if [[ ! -d ./rootfs || ! -e ./rootfs/bin/busybox ]]; then
  die "./rootfs missing (need Alpine lower dir). Run: make setup
  make setup uses docker export alpine — Docker is only for that export, not for running gocker."
fi

cleanup() {
  ./gocker rm -f demo-net >/dev/null 2>&1 || true
  ./gocker rm -f demo-a >/dev/null 2>&1 || true
  ./gocker rm -f demo-b >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "Gocker demo"
echo "Namespaces + OverlayFS + cgroups v2 + optional veth/NAT"
echo "Binary: $ROOT/gocker   rootfs: $ROOT/rootfs"

NET_ARGS=()
NET_MODE=bridge

section "Network probe (gocker0 bridge + NAT)"
set +e
probe_out="$(./gocker run -d -q --name demo-net /bin/busybox sleep 8 2>&1)"
probe_ec=$?
set -e
if [[ "$probe_ec" -ne 0 ]]; then
  echo "Bridge/NAT setup failed. Honest fallback: continuing OverlayFS and cgroup"
  echo "sections with --network=none (loopback only; no container IPs)."
  echo
  echo "$probe_out"
  NET_ARGS=(--network=none)
  NET_MODE=none
else
  echo "Bridge/NAT is up. Detached containers will get different 10.0.0.x addresses."
  ./gocker rm -f demo-net >/dev/null 2>&1 || true
fi

section "1. Hostname / UTS namespace"
echo "Expect: gocker-container  (host hostname is isolated)"
./gocker run -q --network=none /bin/hostname

section "2. OverlayFS isolation"
MARKER="gocker-demo-mark-$$"
echo "Container A writes /$MARKER ; container B must not see it (per-container upper dir)."
echo "--- A ---"
./gocker run -q --network=none /bin/busybox sh -c "echo from-A > /$MARKER; /bin/busybox cat /$MARKER"
echo "--- B ---"
./gocker run -q --network=none /bin/busybox sh -c "if [ -f /$MARKER ]; then echo LEAK; else echo isolated: B cannot see A's /$MARKER; fi"

section "3. cgroup pids.max=20 (fork failure)"
echo "Spawn 25 background sleeps inside one jail. The 21st process should fail to fork."
set +e
pids_out="$(./gocker run -q --network=none /bin/busybox sh -c \
  'i=0; while [ $i -lt 25 ]; do /bin/busybox sleep 30 & i=$((i+1)); done; echo SPAWNED; wait' 2>&1)"
pids_ec=$?
set -e
printf '%s\n' "$pids_out"
if echo "$pids_out" | grep -qiE "can't fork|Resource temporarily unavailable|nproc"; then
  echo
  echo "OK: pids.max=20 blocked extra forks (exit $pids_ec)."
else
  echo
  echo "Note: did not see a fork-failure string. Check cgroup v2 pids controller on this host."
fi

section "4. Two detached containers + ps"
echo "Network mode: $NET_MODE"
./gocker run -d -q --name demo-a "${NET_ARGS[@]}" /bin/busybox sleep 60
./gocker run -d -q --name demo-b "${NET_ARGS[@]}" /bin/busybox sleep 60
echo
echo "--- gocker ps ---"
./gocker ps
if [[ "$NET_MODE" == "none" ]]; then
  echo
  echo "IP column is '-' because this run used --network=none (bridge/NAT failed above)."
fi
echo
echo "--- gocker rm -f ---"
./gocker rm -f demo-a
./gocker rm -f demo-b

echo
echo "======== Done ========"
echo "Teaching dumps: gocker run --teach ..."
echo "Record 45s on Linux: asciinema rec docs/demo.cast   (around sudo make demo)"
