#!/usr/bin/env bash
# Compare gocker run of a trivial command vs docker run --rm alpine true.
# Linux + root (or passwordless sudo) required. Not runnable on native Windows.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

N="${N:-20}"
OUT_DIR="${OUT_DIR:-docs}"
RESULTS_JSON="${OUT_DIR}/startup_bench.json"
RESULTS_MD="${OUT_DIR}/BENCHMARKS.md"

if [[ "$(uname -s)" != "Linux" ]]; then
  echo "This benchmark requires Linux (namespaces/cgroups). Skipping execution."
  echo "On CI (ubuntu-latest + sudo) run: make bench"
  exit 0
fi

if [[ ! -x ./gocker ]]; then
  echo "Building gocker..."
  go build -o gocker .
fi

if [[ ! -d ./rootfs ]]; then
  echo "rootfs missing; run make setup first"
  exit 1
fi

GOCKER_CMD=(./gocker run /bin/busybox true)
if [[ "$(id -u)" -ne 0 ]]; then
  GOCKER_CMD=(sudo ./gocker run /bin/busybox true)
fi

DOCKER_AVAILABLE=0
if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
  DOCKER_AVAILABLE=1
fi

mean_ms() {
  python3 - "$@" <<'PY'
import sys
vals = [float(x) for x in sys.argv[1:]]
print(sum(vals) / len(vals) if vals else 0.0)
PY
}

time_runs() {
  local label="$1"
  shift
  local times=()
  local i
  # Cold: first run after a brief pause
  local t0 t1
  t0=$(date +%s%N)
  "$@" >/tmp/gocker_bench_out.txt 2>&1 || true
  t1=$(date +%s%N)
  local cold_ms
  cold_ms=$(python3 -c "print((${t1}-${t0})/1e6)")
  times+=("$cold_ms")

  for ((i = 2; i <= N; i++)); do
    t0=$(date +%s%N)
    "$@" >/tmp/gocker_bench_out.txt 2>&1 || true
    t1=$(date +%s%N)
    times+=("$(python3 -c "print((${t1}-${t0})/1e6)")")
  done
  echo "${times[*]}"
}

echo "Warming gocker (1 discarded run)..."
"${GOCKER_CMD[@]}" >/dev/null 2>&1 || true

echo "Timing gocker ($N runs, first is cold)..."
GOCKER_TIMES=($(time_runs gocker "${GOCKER_CMD[@]}"))
GOCKER_COLD="${GOCKER_TIMES[0]}"
GOCKER_WARM_MEAN=$(mean_ms "${GOCKER_TIMES[@]:1}")
GOCKER_ALL_MEAN=$(mean_ms "${GOCKER_TIMES[@]}")

DOCKER_COLD=""
DOCKER_WARM_MEAN=""
DOCKER_ALL_MEAN=""
if [[ "$DOCKER_AVAILABLE" -eq 1 ]]; then
  echo "Warming docker..."
  docker run --rm alpine true >/dev/null 2>&1 || true
  echo "Timing docker run --rm alpine true ($N runs)..."
  DOCKER_TIMES=($(time_runs docker docker run --rm alpine true))
  DOCKER_COLD="${DOCKER_TIMES[0]}"
  DOCKER_WARM_MEAN=$(mean_ms "${DOCKER_TIMES[@]:1}")
  DOCKER_ALL_MEAN=$(mean_ms "${DOCKER_TIMES[@]}")
fi

HOST="$(uname -a)"
CPU="$(grep -m1 'model name' /proc/cpuinfo | cut -d: -f2 | xargs || true)"
mkdir -p "$OUT_DIR"

python3 - "$RESULTS_JSON" <<PY
import json, sys
path = sys.argv[1]
data = {
  "os": """$HOST""",
  "cpu": """$CPU""",
  "n": $N,
  "note": "cold = first timed run; warm = mean of remaining N-1 runs. Hardware-dependent.",
  "gocker": {
    "command": "gocker run /bin/busybox true",
    "cold_ms": float("$GOCKER_COLD" or 0),
    "warm_mean_ms": float("$GOCKER_WARM_MEAN" or 0),
    "all_mean_ms": float("$GOCKER_ALL_MEAN" or 0),
  },
  "docker": {
    "command": "docker run --rm alpine true",
    "available": bool($DOCKER_AVAILABLE),
    "cold_ms": float("${DOCKER_COLD:-0}" or 0),
    "warm_mean_ms": float("${DOCKER_WARM_MEAN:-0}" or 0),
    "all_mean_ms": float("${DOCKER_ALL_MEAN:-0}" or 0),
  },
  "resume_claim": "startup <100ms vs Docker ~200ms",
}
data["claim_met"] = data["gocker"]["warm_mean_ms"] < 100 if data["gocker"]["warm_mean_ms"] else False
with open(path, "w") as f:
    json.dump(data, f, indent=2)
print(json.dumps(data, indent=2))
PY

cat > "$RESULTS_MD" <<EOF
# Gocker startup benchmark

**Linux-only.** Native Windows cannot run Linux namespaces. CI: \`ubuntu-latest\` + sudo.

## How to reproduce

\`\`\`bash
make setup
make bench          # N=20 by default
N=50 make bench
\`\`\`

Measures wall time of \`gocker run /bin/busybox true\` vs \`docker run --rm alpine true\`.
First timed run is **cold**; the rest are **warm**.

## Latest run

See \`docs/startup_bench.json\` (written by \`scripts/bench_startup.sh\`).

Resume claim: gocker startup <100ms vs Docker ~200ms. Publish the JSON numbers, not this sentence, if they disagree.

| Runtime | Cold (ms) | Warm mean (ms) |
|---------|-----------|----------------|
| gocker  | ${GOCKER_COLD} | ${GOCKER_WARM_MEAN} |
| docker  | ${DOCKER_COLD:-n/a} | ${DOCKER_WARM_MEAN:-n/a} |

Host: ${HOST}
EOF

echo "Wrote $RESULTS_JSON and $RESULTS_MD"
