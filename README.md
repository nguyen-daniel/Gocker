# Gocker

A small Docker-like runtime in **Go**: isolate a process with Linux namespaces, cgroups v2, a chroot rootfs, and a veth/NAT network. Built to learn how containers actually work — not to replace Docker.

**Linux only.** Windows/macOS cannot run this (no `clone` namespaces). CI is Ubuntu + sudo.

## What I built

- **4 namespaces by default** (rootful): UTS, PID, mount, network. **User namespace is optional** (`--rootless` / `GOCKER_ALLOW_UNPRIVILEGED=1`) with uid/gid maps.
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
| `pids.max=20` | `TestPidsMaxEnforcement` (Linux) |
| IPAM 253-IP pool | `TestIPAM`, `TestMultipleContainers` |
| Startup vs Docker | `make bench` → `docs/BENCHMARKS.md` (not measured on Windows) |

## Honest limitations

- Requires **Linux + root** for the default path (namespaces, cgroups, chroot, iptables).
- User namespace is **not** on the rootful default path — do not claim “5 namespaces” unless you ran `--rootless`.
- Startup times vs Docker are **hardware-specific**. This repo does not ship a `<100ms` number; run `make bench` on Linux and quote the JSON.
- Not a production runtime (no image store, no seccomp profile beyond what Alpine + namespaces give you).

## Recruiter-facing one-liner

Go process isolator: 4 Linux namespaces, cgroups v2 (CPU / memory / 20 pids), veth+NAT+IPAM, Alpine chroot. Tests in CI. Optional user namespace for rootless experiments.

---

## Project structure

- `main.go` — namespaces, cgroups, network, CLI
- `main_test.go` — unit + integration tests
- `scripts/bench_startup.sh` — gocker vs `docker run --rm alpine true`
- `docs/BENCHMARKS.md` — how to reproduce benches
- `.github/workflows/main.yml` — Ubuntu CI

## Prerequisites

- Linux, Go 1.16+, Docker (rootfs setup only), sudo for the default path
