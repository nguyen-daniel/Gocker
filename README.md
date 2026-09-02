# Gocker

[![CI](https://github.com/nguyen-daniel/Gocker/actions/workflows/main.yml/badge.svg)](https://github.com/nguyen-daniel/Gocker/actions/workflows/main.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

A small Docker-like runtime in **Go**: isolate a process with Linux namespaces, cgroups v2, OverlayFS + `pivot_root`, and a veth/NAT network. Built to learn how containers actually work — not to replace Docker.

Released under the MIT License (see [LICENSE](LICENSE)).

**Linux only.** Windows/macOS cannot run this (no `clone` namespaces). CI is Ubuntu + sudo.

## What I built

- **4 namespaces by default** (rootful): UTS, PID, mount, network. **User namespace is optional** (`--rootless` / `GOCKER_ALLOW_UNPRIVILEGED=1`) with uid/gid maps.
- **OverlayFS + `pivot_root`**: shared Alpine lower dir, per-container `upper`/`work` under `/var/lib/gocker/containers/<id>/`
- **cgroups v2**: CPU (`cpu.max`), memory (`memory.max`), **`pids.max=20`**
- **Network**: `gocker0` bridge, veth pair, NAT masquerade, IPAM on `10.0.0.2`–`10.0.0.254` (253 usable). Bridge/NAT failure **fails the run** unless `--network=none`.
- **Bind mounts**: `-v host:container` jailed under the overlay merged dir
- **CLI**: `run`, `ps`, `stop`, `rm`, `logs` with JSON state under `/var/lib/gocker`
- **Teaching cap drop**: after `pivot_root`, drop a short list of extra capabilities (not a production profile; no seccomp)

## How to run

```bash
git clone https://github.com/nguyen-daniel/Gocker.git
cd Gocker
make setup    # Alpine rootfs via docker export
make build
sudo ./gocker run /bin/busybox echo hello
sudo ./gocker run -v "$PWD/data:/data" /bin/busybox ls /data
sudo ./gocker run -d --name web --cpu-limit 0.5 --memory-limit 64M /bin/busybox sleep 30
sudo ./gocker logs -f web
sudo ./gocker rm -f web
make test              # sudo integration tests
make test-unprivileged # user-namespace unit tests, no sudo
make bench             # startup vs docker (Linux); writes docs/startup_bench.json
```

## CLI

| Command | What it does |
|---------|----------------|
| `gocker run [options] <cmd>` | Create namespaces, overlay jail, cgroup, optional veth, exec `cmd` |
| `gocker ps` | List saved containers |
| `gocker stop <id\|name>` | SIGTERM (then SIGKILL), status `stopped` |
| `gocker rm [-f] <id\|name>` | Delete overlay + state; refuses a live container unless `-f` |
| `gocker logs [-f] <id\|name>` | Print the log file; `-f` follows until the container exits |

### `run` flags

| Flag | Default | What it does |
|------|---------|----------------|
| `--cpu-limit <n>` | unlimited (still `pids.max=20`) | cgroup v2 `cpu.max` (`0.5` = half a CPU period) |
| `--memory-limit <size>` | unlimited | cgroup v2 `memory.max` (`512M`, `1G`, …) |
| `--detach`, `-d` | off | Parent exits; a reaper waits for the child then removes cgroup/veth |
| `--name <name>` | none | Used by `stop` / `rm` / `logs` / `ps` |
| `--quiet`, `-q` | off | Hide teaching logs. Detached runs still print `Container started with ID:` |
| `-v`, `--volume <host:container>` | none | Bind-mount. Container path is jailed under the overlay (no `Join` escape) |
| `--network <mode>` | `bridge` | `bridge`: veth + NAT. `none`: loopback only, skip host net. **Unknown modes fail.** If bridge/NAT setup fails, the run fails unless `none`. |
| `--rootfs <path>` | `./rootfs` | OverlayFS lower dir |
| `--rootless` | off | User namespace; network/cgroups may fail |

`--network=none` and `--network none` are both accepted.

## Proof

| Behavior | Test / artifact |
|----------|-----------------|
| 4-ns default, optional user ns | `TestNamespaceConfig`, `TestCloneUserNamespace` |
| Hostname isolation | `TestGockerRunWithHostname` → `gocker-container` |
| OverlayFS write isolation | `TestOverlayWriteIsolation` |
| Bind-mount `-v` | `TestMountVolumesBind`, `TestVolumeBindMount` |
| Detached survives parent exit | `TestDetachedSurvivesParentExit` |
| Reaper cleans cgroup/veth/status | `TestDetachedReaperCleansUp` |
| `stop` / `rm` / `logs` | `TestStopSetsStopped`, `TestRmRefusesRunningThenDeletes`, `TestLogsContents` |
| `echo hello` + hostname | [`docs/demo_run.txt`](docs/demo_run.txt) |
| `pids.max=20` | `TestPidsMaxEnforcement` (Linux) |
| CPU / memory cgroup files + load | `TestCPUAndMemoryLimitsApplied`, `TestCPULimitEnforcement`, `TestMemoryLimitEnforcement` |
| Teaching cap drop | `TestDropTeachingCaps`, `TestTeachingCapsDroppedInContainer` |
| IPAM 253-IP pool | `TestIPAM`, `TestFindFreeIP`, `TestIPAMReuse`, `TestMultipleContainers` |
| Startup vs Docker | `make bench` → `docs/BENCHMARKS.md`; CI uploads `docs/startup_bench.json` as artifact `startup-bench` (not measured on Windows) |

## Honest limitations

- Requires **Linux + root** for the default path (namespaces, cgroups, overlay mounts, iptables).
- User namespace is **not** on the rootful default path — do not claim “5 namespaces” unless you ran `--rootless`.
- Startup times vs Docker are **hardware-specific**. This repo does not ship a `<100ms` number; run `make bench` on Linux and quote the JSON (CI artifact `startup-bench`).
- Not a production runtime: no image store, no seccomp, no port publish. The capability drop is a **teaching demo** (a few extra caps after `pivot_root`; `CAP_SYS_ADMIN` is kept). It is not Docker’s default profile.
- CPU throttle proof uses `cpu.max` plus `cpu.stat` (`nr_throttled` / usage). On a noisy or idle host the counter can lag; the test always checks the cgroup file and only fails if usage looks like an unthrottled core.
- `--network=none` is the opt-out if you cannot create `gocker0` / NAT. Default `bridge` no longer leaves a container `running` with a broken network.

---

## Project structure

- `cmd/gocker` — CLI (`run`, `ps`, `stop`, `rm`, `logs`) and integration tests
- `internal/ns` — clone flags (UTS/PID/mount/net; optional user ns) and teaching cap drop
- `internal/cgroup` — cgroups v2 CPU/memory/`pids.max`
- `internal/net` — bridge, veth, NAT, IPAM
- `internal/overlay` — OverlayFS, `pivot_root`, volume path jail + bind-mount
- `internal/state` — JSON container state under `/var/lib/gocker`
- `scripts/bench_startup.sh` — gocker vs `docker run --rm alpine true`
- `docs/BENCHMARKS.md` — how to reproduce benches
- `docs/demo_run.txt` — `echo hello` + hostname transcript
- `.github/workflows/main.yml` — Ubuntu CI (`gofmt`, `go vet`, tests)

## Prerequisites

- Linux, Go 1.21, Docker (rootfs setup only), sudo for the default path
