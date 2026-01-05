# Gocker Quick Reference

## Commands

```bash
# Build the binary
make build

# Setup Alpine rootfs (first time only)
make setup

# Run tests (requires sudo)
make test

# Run container with /bin/sh
make run

# Run custom command
sudo ./gocker run /bin/ls -la /
sudo ./gocker run /bin/busybox ps aux
sudo ./gocker run /bin/hostname

# Run with resource limits
sudo ./gocker run --cpu-limit 0.5 --memory-limit 512M /bin/sh

# Run with volume mount
sudo ./gocker run --volume /tmp:/mnt/tmp /bin/busybox ls -la /mnt/tmp

# Run in detached mode
sudo ./gocker run --detach /bin/busybox sleep 60

# Container lifecycle management
sudo ./gocker ps                    # List all containers
sudo ./gocker stop <container-id>   # Stop a container
sudo ./gocker rm <container-id>     # Remove a container
sudo ./gocker logs <container-id>   # View logs

# Launch GUI
sudo ./gocker gui

# Clean up binary
make clean
```

## Key Files

- `main.go` - Main implementation (CLI, lifecycle management)
- `gui.go` - Graphical user interface (Fyne-based)
- `main_test.go` - Integration tests
- `Makefile` - Build automation
- `README.md` - Comprehensive documentation
- `GUI_FRAMEWORKS.md` - GUI framework comparison
- `rootfs/` - Alpine Linux filesystem
- `go.mod` - Go module definition

## Key Functions

### main.go

- `main()` - Entry point, command routing
- `run()` - Parent process, namespace creation, user/network setup
- `child()` - Container setup, chroot, command execution
- `setupUserNamespace()` - Writes UID/GID mappings for user namespace
- `setupNetwork()` - Creates veth pair, configures host networking
- `configureContainerNetwork()` - Configures network inside container
- `cleanupNetwork()` - Removes network interfaces and iptables rules
- `limitResources()` - Sets up cgroups v2
- `getDefaultInterface()` - Finds default network interface

## Network Configuration

- **Container IP**: `10.0.0.2/24`
- **Host IP**: `10.0.0.1/24`
- **Network**: `10.0.0.0/24`
- **Interface Names**: `veth<pid>` (host), `vethc<pid>` (container)
- **Hostname**: `gocker-container`

## Cgroups Configuration

- **Path**: `/sys/fs/cgroup/gocker`
- **Process Limit**: 20 max processes (default, `pids.max`)
- **CPU Limit**: Configurable via `--cpu-limit` (`cpu.max`)
  - Examples: "1" (1 CPU), "0.5" (50% of one CPU), "max" (unlimited)
- **Memory Limit**: Configurable via `--memory-limit` (`memory.max`)
  - Examples: "512M", "1G", "max" (unlimited)
- **Version**: Cgroups v2

## Namespaces Used

- `CLONE_NEWUTS` - Hostname isolation
- `CLONE_NEWPID` - Process ID isolation
- `CLONE_NEWNS` - Mount namespace
- `CLONE_NEWNET` - Network namespace
- `CLONE_NEWUSER` - User namespace (security)

## Requirements

- Linux OS
- Go 1.21+
- Root/sudo access
- Docker (for rootfs setup)
- `ip` command
- `iptables` command
- Cgroups v2 support
- User namespaces enabled (`/proc/sys/user/max_user_namespaces > 0`)

## Troubleshooting

### Permission Denied
```bash
sudo ./gocker run /bin/sh
```

### Check Cgroups Version
```bash
mount | grep cgroup
```

### Check IP Forwarding
```bash
sysctl net.ipv4.ip_forward
```

### Clean Up Network Interfaces
```bash
sudo ip link delete veth<pid>
```

### Check Network Tools
```bash
which ip
which iptables
```

## Testing

```bash
# Run all tests
make test

# Individual test
sudo go test -v -run TestGockerRun
sudo go test -v -run TestGockerRunWithHostname
```

## Project Structure

```
Gocker/
├── main.go           # CLI implementation & lifecycle management
├── gui.go            # GUI implementation (Fyne)
├── main_test.go      # Integration tests
├── Makefile          # Build automation
├── go.mod            # Go module
├── go.sum            # Go module checksums
├── README.md         # Comprehensive documentation
├── GUI_FRAMEWORKS.md # GUI framework comparison
└── rootfs/           # Alpine Linux rootfs
```

## Architecture Summary

1. Parent creates child with namespaces (including user namespace)
2. Parent sets up user namespace (UID/GID mappings)
3. Parent sets up network (veth pair, NAT)
4. Parent saves container state to `/var/lib/gocker/containers/<id>.json`
5. Child verifies user namespace, sets up cgroups (process, CPU, memory), network, hostname
6. Child mounts volumes (bind mounts from host to container paths)
7. Child chroots to rootfs
8. Child mounts /proc
9. Child executes user command
10. Parent updates container status and cleans up network on exit

## State Management

- **State Directory**: `/var/lib/gocker/containers/`
- **Log Directory**: `/var/lib/gocker/logs/`
- **Container Metadata**: JSON files with ID, PID, status, command, creation time
- **Container Status**: running, stopped, exited

