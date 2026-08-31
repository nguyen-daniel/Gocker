# Gocker benchmarks

**Linux-only.** Native Windows cannot create Linux namespaces or cgroups. WSL was not installed when this was written, so startup vs Docker is **not measured here**.

## Reproduce (Linux / GitHub Actions `ubuntu-latest`)

```bash
make setup
make bench          # N=20
N=50 make bench
```

`scripts/bench_startup.sh` times `gocker run /bin/busybox true` vs `docker run --rm alpine true` (if Docker works). First timed run is **cold**; the rest are **warm**. Writes `docs/startup_bench.json`.

## Claims vs evidence

| Topic | What is true | What is not claimed |
|-------|----------------|---------------------|
| Namespaces | Rootful default: UTS + PID + mount + net. User ns is optional (`--rootless`). | “5 namespaces” on the default path |
| cgroups v2 | CPU 0.5–2, memory, `pids.max=20`. `TestPidsMaxEnforcement` on Linux | — |
| Network | veth + NAT + IPAM `10.0.0.2`–`.254` | — |
| Startup vs Docker | Run `make bench` and quote the JSON | No `<100ms` / `~200ms` figure in-repo |

Host: record kernel and CPU when you run the bench.
