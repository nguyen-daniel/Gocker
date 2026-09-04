# Gocker

[![CI](https://github.com/nguyen-daniel/Gocker/actions/workflows/main.yml/badge.svg)](https://github.com/nguyen-daniel/Gocker/actions/workflows/main.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

A small Docker-like runtime in **Go**: isolate a process with Linux namespaces, cgroups v2, OverlayFS + `pivot_root`, and a veth/NAT network. Built to learn how containers actually work — not to replace Docker.

Released under the MIT License (see [LICENSE](LICENSE)).

## 60 seconds

**Linux + root.** Native Windows/macOS cannot run this (no `clone` namespaces). On Windows use **WSL2 Ubuntu** or [GitHub Codespaces](https://github.com/codespaces) (optional [`.devcontainer`](.devcontainer/devcontainer.json): Ubuntu, Go, Docker-in-Docker for the Alpine export). Docker is **only** used for `docker export` of Alpine — gocker does not drive Docker at runtime.

```bash
git clone https://github.com/nguyen-daniel/Gocker.git
cd Gocker
make setup          # docker export alpine → ./rootfs
make build
sudo make demo      # hostname, OverlayFS isolation, pids.max=20, two IPs + ps
```

`make demo` re-execs sudo if needed and uses `-q` so the payloads stay readable. If bridge/NAT cannot be created, the script says so and continues the overlay/cgroup sections with `--network=none`. Example transcript: [`docs/demo_run.txt`](docs/demo_run.txt) (labeled example — not a live Windows capture).

Record 45s with asciinema on Linux: `asciinema rec docs/demo.cast` around `make demo`. Do not check in a fabricated `.cast`.

By hand (same wow path):

```bash
sudo ./gocker run -q --network=none /bin/hostname          # gocker-container
sudo ./gocker run -d --name a /bin/busybox sleep 60
sudo ./gocker exec a /bin/hostname
sudo ./gocker run -d --name b /bin/busybox sleep 60
sudo ./gocker ps
sudo ./gocker rm -f a
sudo ./gocker rm -f b
```

`gocker --help` and `gocker run --help` / `gocker exec --help` work without root. Teaching dumps are off by default; pass `--teach` to see namespace/overlay/cgroup narration.

## What I built

- **5 namespaces by default** (rootful): UTS, PID, mount, network, IPC. **User namespace is optional** (`--rootless` / `GOCKER_ALLOW_UNPRIVILEGED=1`) with uid/gid maps.
- **OverlayFS + `pivot_root`**: shared Alpine lower dir, per-container `upper`/`work` under `/var/lib/gocker/containers/<id>/`
- **Host `/etc/resolv.conf`** is copied into the overlay (before `pivot_root`) so bridge containers can resolve names. No DNS server.
- **cgroups v2**: CPU (`cpu.max`), memory (`memory.max`), **`pids.max=20`**
- **Network**: `gocker0` bridge, veth pair, NAT masquerade, IPAM on `10.0.0.2`–`10.0.0.254` (253 usable). Link/addr/route/veth use netlink (`golang.org/x/sys/unix`); iptables stays for MASQUERADE/FORWARD and `-p` DNAT. Bridge/NAT failure **fails the run** unless `--network=none`.
- **Port publish**: `run -p HOST:CONTAINER` (repeatable, TCP only). iptables DNAT on `PREROUTING` and `OUTPUT` (localhost). Removed on `stop` / `rm` / reaper.
- **`gocker exec`**: join a running container's namespaces via `setns` (and its cgroup). Non-TTY only; PID-ns join forks after `setns`.
- **Bind mounts**: `-v host:container` jailed under the overlay merged dir
- **CLI**: `run`, `ps`, `stop`, `rm`, `logs`, `exec` with JSON state under `/var/lib/gocker`. 12-char hex ids; `ps` is laptop-width.
- **Teaching cap drop + seccomp**: after `pivot_root`, drop a short list of extra capabilities, set `PR_SET_NO_NEW_PRIVS`, and install a short seccomp filter (mount/reboot/unshare/bpf/…). **Not** Docker's default profile; `CAP_SYS_ADMIN` is kept.

## How to run

```bash
make setup    # Alpine rootfs via docker export
make build
sudo ./gocker run /bin/busybox echo hello
sudo ./gocker run -v "$PWD/data:/data" /bin/busybox ls /data
sudo ./gocker run -d --name web --cpu-limit 0.5 --memory-limit 64M /bin/busybox sleep 30
sudo ./gocker exec web /bin/busybox echo from-exec
sudo ./gocker logs -f web
sudo ./gocker rm -f web
sudo ./gocker run -d -p 8080:80 --name httpd /bin/busybox httpd -f -p 80
sudo ./gocker rm -f httpd
make test              # sudo integration tests
make test-unprivileged # user-namespace + parse tests, no sudo
make bench             # startup vs docker (Linux); writes docs/startup_bench.json
```

## CLI

| Command | What it does |
|---------|----------------|
| `gocker run [options] <cmd>` | Create namespaces, overlay jail, cgroup, optional veth, exec `cmd` |
| `gocker ps` | List saved containers (ID, NAME, STATUS, PID, IP, COMMAND) |
| `gocker stop <id\|name>` | SIGTERM (then SIGKILL), status `stopped` |
| `gocker rm [-f] <id\|name>` | Delete overlay + state; refuses a live container unless `-f` |
| `gocker logs [-f] <id\|name>` | Print the log file; `-f` follows until the container exits |
| `gocker exec <id\|name> <cmd>` | `setns` into a running container and run `cmd` (non-TTY) |

### `run` flags

| Flag | Default | What it does |
|------|---------|----------------|
| `--cpu-limit <n>` | unlimited (still `pids.max=20`) | cgroup v2 `cpu.max` (`0.5` = half a CPU period) |
| `--memory-limit <size>` | unlimited | cgroup v2 `memory.max` (`512M`, `1G`, …) |
| `--detach`, `-d` | off | Parent exits; a reaper waits for the child then removes cgroup/veth/DNAT |
| `--name <name>` | none | Used by `stop` / `rm` / `logs` / `exec` / `ps` |
| `--quiet`, `-q` | **on** (same as default) | Hide teaching logs. Foreground still prints `id <12-hex>` on stderr; detached still prints `Container started with ID:` |
| `--teach` | off | Verbose teaching logs (namespaces, overlay, cgroup, veth) |
| `-v`, `--volume <host:container>` | none | Bind-mount. Container path is jailed under the overlay (no `Join` escape) |
| `-p`, `--publish <host:container>` | none | TCP DNAT. Repeatable. Fails with `--network=none`. No UDP, no ranges, no docker-proxy. |
| `--network <mode>` | `bridge` | `bridge`: veth + NAT. `none`: loopback only, skip host net. **Unknown modes fail.** If bridge/NAT setup fails, the run fails unless `none`. |
| `--rootfs <path>` | `./rootfs` | OverlayFS lower dir |
| `--rootless` | off | User namespace; network/cgroups may fail |

`--network=none` and `--network none` are both accepted. `--help` / `-h` on any command exits 0 without treating the flag as a jail argv (`gocker exec web --help` runs `--help` inside the container).

## Proof

| Behavior | Test / artifact |
|----------|-----------------|
| 5-ns default, optional user ns | `TestNamespaceConfig`, `TestCloneUserNamespace` |
| Hostname isolation | `TestGockerRunWithHostname` → `gocker-container` |
| OverlayFS write isolation | `TestOverlayWriteIsolation` |
| Bind-mount `-v` | `TestMountVolumesBind`, `TestVolumeBindMount` |
| Host resolv.conf in jail | `TestCopyResolvConf`, `TestResolvConfInJail` |
| Detached survives parent exit | `TestDetachedSurvivesParentExit` |
| Reaper cleans cgroup/veth/status | `TestDetachedReaperCleansUp` |
| `stop` / `rm` / `logs` / `exec` | `TestStopSetsStopped`, `TestRmRefusesRunningThenDeletes`, `TestLogsContents`, `TestExecEchoAndHostname` |
| Port publish `-p` | `TestParsePublish`, `TestDNATRuleArgs`, `TestPortPublishLocalhost` |
| Labeled demo transcript | [`docs/demo_run.txt`](docs/demo_run.txt) |
| `pids.max=20` | `TestPidsMaxEnforcement` (Linux) |
| CPU / memory cgroup files + load | `TestCPUAndMemoryLimitsApplied`, `TestCPULimitEnforcement`, `TestMemoryLimitEnforcement` |
| Teaching cap drop + seccomp | `TestDropTeachingCaps`, `TestTeachingCapsDroppedInContainer`, `TestTeachingSeccompInContainer` |
| IPAM 253-IP pool | `TestIPAM`, `TestFindFreeIP`, `TestIPAMReuse`, `TestMultipleContainers` |
| Startup vs Docker | `make bench` → `docs/BENCHMARKS.md`; CI uploads `docs/startup_bench.json` as artifact `startup-bench` (not measured on Windows) |

## Honest limitations

- Requires **Linux + root** for the default path (namespaces, cgroups, overlay mounts, iptables).
- User namespace is **not** on the rootful default path — the default is 5 namespaces (UTS, PID, mount, net, IPC); `--rootless` adds user ns.
- Startup times vs Docker are **hardware-specific**. This repo does not ship a `<100ms` number; run `make bench` on Linux and quote the JSON (CI artifact `startup-bench`).
- Not a production runtime: no image store, no registry, no Dockerfile. The capability drop + seccomp filter is a **teaching demo** (a short deny list after `pivot_root`; `CAP_SYS_ADMIN` is kept). It is not Docker's default profile and is not argument-aware (e.g. `clone` with `NEW*` flags is not blocked).
- `exec` is non-TTY (no PTY). PID-namespace join uses clone+setns+fork because a multithreaded Go process cannot `setns(CLONE_NEWPID)` directly.
- `-p` is TCP only (no UDP, no port ranges, no docker-proxy). DNAT cleanup depends on stored `published_ports`; leftover rules are possible if state is deleted by hand.
- CPU throttle proof uses `cpu.max` plus `cpu.stat` (`nr_throttled` / usage). On a noisy or idle host the counter can lag; the test always checks the cgroup file and only fails if usage looks like an unthrottled core.
- `--network=none` is the opt-out if you cannot create `gocker0` / NAT. Default `bridge` no longer leaves a container `running` with a broken network.

---

## Project structure

- `cmd/gocker` — CLI (`run`, `ps`, `stop`, `rm`, `logs`, `exec`) and integration tests
- `internal/ns` — clone flags (UTS/PID/mount/net/IPC; optional user ns), teaching cap drop, seccomp, `exec` setns
- `internal/cgroup` — cgroups v2 CPU/memory/`pids.max`
- `internal/net` — netlink bridge/veth/addr/route, iptables NAT + `-p` DNAT, IPAM
- `internal/overlay` — OverlayFS, `pivot_root`, resolv.conf copy, volume path jail + bind-mount
- `internal/state` — JSON container state under `/var/lib/gocker`
- `scripts/demo.sh` — `make demo` walkthrough
- `scripts/bench_startup.sh` — gocker vs `docker run --rm alpine true`
- `docs/BENCHMARKS.md` — how to reproduce benches
- `docs/demo_run.txt` — labeled example `make demo` transcript
- `.github/workflows/main.yml` — Ubuntu CI (`gofmt`, `go vet`, tests)

## Prerequisites

- Linux, Go 1.21, Docker (rootfs setup only), sudo for the default path
