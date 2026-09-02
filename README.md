# Gocker

[![CI](https://github.com/nguyen-daniel/Gocker/actions/workflows/main.yml/badge.svg)](https://github.com/nguyen-daniel/Gocker/actions/workflows/main.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

A small Docker-like runtime in **Go**: isolate a process with Linux namespaces, cgroups v2, OverlayFS + `pivot_root`, and a veth/NAT network. Built to learn how containers actually work — not to replace Docker.

Released under the MIT License (see [LICENSE](LICENSE)).

**Linux only.** Windows/macOS cannot run this (no `clone` namespaces). CI is Ubuntu + sudo.

## What I built

- **4 namespaces by default** (rootful): UTS, PID, mount, network. **User namespace is optional** (`--rootless` / `GOCKER_ALLOW_UNPRIVILEGED=1`) with uid/gid maps.
- **OverlayFS + `pivot_root`**: shared Alpine lower dir, per-container `upper`/`work` under `/var/lib/gocker/containers/<id>/`
- **cgroups v2**: CPU (`0.5`–`2` via `cpu.max`), memory limits, **`pids.max=20`**
- **Network**: `gocker0` bridge, veth pair, NAT masquerade, IPAM on `10.0.0.2`–`10.0.0.254` (253 usable)
- **CLI**: `run`, `ps`, `stop`, `rm`, `logs` with JSON state under `/var/lib/gocker`

## How to run

```bash
git clone https://github.com/nguyen-daniel/Gocker.git
cd Gocker
make setup    # Alpine rootfs via docker export
make build
sudo ./gocker run /bin/busybox echo hello
make test              # sudo integration tests
make test-unprivileged # user-namespace unit tests, no sudo
make bench             # startup vs docker (Linux); writes docs/startup_bench.json
```

## Proof

| Behavior | Test / artifact |
|----------|-----------------|
| 4-ns default, optional user ns | `TestNamespaceConfig`, `TestCloneUserNamespace` |
| Hostname isolation | `TestGockerRunWithHostname` → `gocker-container` |
| OverlayFS write isolation | `TestOverlayWriteIsolation` |
| Detached survives parent exit | `TestDetachedSurvivesParentExit` |
| `echo hello` + hostname | [`docs/demo_run.txt`](docs/demo_run.txt) |
| `pids.max=20` | `TestPidsMaxEnforcement` (Linux) |
| IPAM 253-IP pool | `TestIPAM`, `TestFindFreeIP`, `TestIPAMReuse`, `TestMultipleContainers` |
| Startup vs Docker | `make bench` → `docs/BENCHMARKS.md`; CI uploads `docs/startup_bench.json` as artifact `startup-bench` (not measured on Windows) |

## Honest limitations

- Requires **Linux + root** for the default path (namespaces, cgroups, overlay mounts, iptables).
- User namespace is **not** on the rootful default path — do not claim “5 namespaces” unless you ran `--rootless`.
- Startup times vs Docker are **hardware-specific**. This repo does not ship a `<100ms` number; run `make bench` on Linux and quote the JSON (CI artifact `startup-bench`).
- Not a production runtime (no image store, no seccomp profile beyond what Alpine + namespaces give you).

---

## Project structure

- `cmd/gocker` — CLI (`run`, `ps`, `stop`, `rm`, `logs`) and integration tests
- `internal/ns` — clone flags (UTS/PID/mount/net; optional user ns)
- `internal/cgroup` — cgroups v2 CPU/memory/`pids.max`
- `internal/net` — bridge, veth, NAT, IPAM
- `internal/overlay` — OverlayFS, `pivot_root`, volume path jail
- `internal/state` — JSON container state under `/var/lib/gocker`
- `scripts/bench_startup.sh` — gocker vs `docker run --rm alpine true`
- `docs/BENCHMARKS.md` — how to reproduce benches
- `docs/demo_run.txt` — `echo hello` + hostname transcript
- `.github/workflows/main.yml` — Ubuntu CI (`gofmt`, `go vet`, tests)

## Prerequisites

- Linux, Go 1.21, Docker (rootfs setup only), sudo for the default path
