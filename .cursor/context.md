# Gocker Context & Design Decisions

## Project Purpose

Gocker is an **educational project** that demonstrates how containerization works at a low level. It's not intended for production use but serves as a learning tool to understand:
- Linux namespaces (UTS, PID, Mount, Network, User)
- Cgroups v2
- Chroot filesystem isolation
- Network namespace isolation
- User namespace security
- Container runtime internals

## Key Design Decisions

### 1. Why Go?

- Excellent system call support via `syscall` package
- Simple concurrency model for parent/child process management
- Cross-platform standard library with Linux-specific features
- Easy to understand and modify for educational purposes

### 2. Why Alpine Linux Rootfs?

- Minimal size (smaller rootfs)
- Uses busybox (single binary for many utilities)
- Musl libc (lightweight C library)
- Commonly used in production containers
- Easy to obtain via Docker export

### 3. Why Chroot Instead of OverlayFS?

- **Simplicity**: Chroot is the simplest form of filesystem isolation
- **Educational**: Easier to understand than overlay filesystems
- **No Mount Namespace Complexity**: Simpler implementation
- **Trade-off**: Less flexible than overlay filesystems (no copy-on-write)

### 4. Why Cgroups v2?

- Modern standard (cgroups v1 is legacy)
- Simpler API (unified hierarchy)
- Better resource management
- Future-proof approach

### 5. Network Architecture Decisions

- **Veth Pairs**: Standard Linux approach for container networking
- **10.0.0.0/24 Network**: Private network range, avoids conflicts
- **NAT Masquerading**: Enables internet access without complex routing
- **Parent-Child Setup**: Network configured in parent (before chroot) for tool access

### 6. User Namespace Implementation

- **Security Enhancement**: Container root (UID 0) mapped to unprivileged host user (UID 1000)
- **Defense in Depth**: Adds security layer even if other isolation fails
- **File Permissions**: Work correctly within container namespace
- **Mapping Approach**: Parent process (root) writes mappings to /proc/<pid>/uid_map and /proc/<pid>/gid_map

## Important Concepts

### Namespace Types Used

1. **UTS Namespace** (`CLONE_NEWUTS`)
   - Isolates hostname and domain name
   - Container sees "gocker-container" as hostname
   - Host hostname remains unchanged

2. **PID Namespace** (`CLONE_NEWPID`)
   - Isolates process IDs
   - Container's first process sees itself as PID 1
   - Parent process sees different PID for same process

3. **Mount Namespace** (`CLONE_NEWNS`)
   - Isolates filesystem mount points
   - Allows mounting `/proc` without affecting host
   - Mounts are private to container

4. **Network Namespace** (`CLONE_NEWNET`)
   - Isolated network stack
   - Own network interfaces, IP addresses, routing tables
   - Connected to host via veth pair

5. **User Namespace** (`CLONE_NEWUSER`)
   - Isolated user and group ID space
   - Container root (UID 0) mapped to unprivileged host user (UID 1000)
   - Enhanced security: escaped processes run as non-root on host
   - File permissions work correctly within container namespace

### Cgroups v2 Implementation

- **Location**: `/sys/fs/cgroup/gocker`
- **Process Limit**: `pids.max = 20` (default)
- **CPU Limit**: `cpu.max` controller (configurable via `--cpu-limit`)
  - Format: "quota period" in microseconds (e.g., "100000 100000" for 1 CPU)
  - Supports fractional CPUs (e.g., "0.5" for 50% of one CPU)
  - "max" for unlimited
- **Memory Limit**: `memory.max` controller (configurable via `--memory-limit`)
  - Format: bytes as string (e.g., "536870912" for 512M)
  - Supports K, M, G suffixes
  - "max" for unlimited
- **Assignment**: PID written to `cgroup.procs`
- **Purpose**: Resource limit enforcement (process, CPU, memory)

### Chroot Filesystem Isolation

- **Root Directory**: Changed to `./rootfs`
- **Effect**: Container cannot access files outside rootfs
- **Proc Mount**: Fresh `/proc` mounted after chroot
- **PATH**: Set to standard Linux paths for command discovery

### User Namespace Setup Sequence

1. **Parent Process** (after child starts):
   - Writes UID mapping to `/proc/<pid>/uid_map` (container 0 -> host 1000)
   - Writes GID mapping to `/proc/<pid>/gid_map` (container 0 -> host 1000)
   - Maps range of UIDs/GIDs (0-1000 in container -> 1000-2000 on host)

2. **Child Process**:
   - Initially mapped to "nobody" (UID 65534) until parent writes mapping
   - After mapping, sees itself as UID 0 (root) in container namespace
   - Has full privileges within container, but runs as UID 1000 on host

### Network Setup Sequence

1. **Parent Process** (before child starts):
   - Creates veth pair
   - Moves container end into child's network namespace
   - Configures host IP and NAT rules

2. **Child Process** (before chroot):
   - Finds veth interface in namespace
   - Brings up interface
   - Assigns container IP
   - Sets default route

3. **After Container Exit**:
   - Parent cleans up veth interfaces
   - Removes iptables rules

## Code Patterns

### Error Handling

- `must()` helper function: Exits on error with message
- Network errors are warnings (container continues)
- Other errors are fatal (container cannot function)

### Process Management

- Parent creates child with `exec.Command("/proc/self/exe", ...)`
- Uses `CLONE_*` flags for namespace creation
- Child PID captured before `CLONE_NEWPID` takes effect
- Parent waits for child completion

### Network Interface Naming

- Host: `veth<pid>` (e.g., `veth12345`)
- Container: `vethc<pid>` (e.g., `vethc12345`)
- PID-based naming ensures uniqueness

### Command Execution

- Default command: `/bin/sh` (if none provided)
- PATH environment variable set for command discovery
- Standard I/O forwarded to parent (stdin, stdout, stderr)

## Testing Strategy

### Integration Tests

- **Requires sudo**: All namespace operations need root
- **Requires rootfs**: Alpine rootfs must exist
- **Requires binary**: `gocker` binary must be built
- **Tests**:
  - Basic command execution (`/bin/busybox true`)
  - Hostname isolation (UTS namespace)

### Test Limitations

- Cannot test network in CI easily (requires network setup)
- Cannot test cgroups limits easily (requires process spawning)
- Focus on core functionality (execution and hostname)

## Common Issues & Solutions

### Issue: Permission Denied

**Solution**: Run with `sudo` - all operations require root

### Issue: Cgroup Errors

**Solution**: Ensure system uses cgroups v2:
```bash
mount | grep cgroup  # Should show cgroup2
```

### Issue: Network Not Working

**Solutions**:
- Check `ip` command availability
- Check `iptables` availability
- Verify IP forwarding enabled: `sysctl net.ipv4.ip_forward`
- Check for leftover veth interfaces

### Issue: User Namespace Errors

**Solutions**:
- Verify user namespaces enabled: `cat /proc/sys/user/max_user_namespaces` (should be > 0)
- Check UID/GID mapping: `cat /proc/<pid>/uid_map` and `cat /proc/<pid>/gid_map`
- Ensure running as root (mapping requires root privileges)
- Check file permissions in rootfs (should be accessible by mapped user)

### Issue: Rootfs Not Found

**Solution**: Run `make setup` to download Alpine rootfs via Docker

## Development Workflow

1. **Make Changes**: Edit `main.go`
2. **Build**: `make build`
3. **Test**: `make test` (requires sudo)
4. **Run**: `make run` (interactive testing)
5. **Clean**: `make clean` (remove binary)

## Future Enhancement Ideas

- [x] User namespace support (better security) - **COMPLETED**
- [x] CPU and memory cgroup limits - **COMPLETED**
- [x] Volume mounting support - **COMPLETED**
- [x] Container lifecycle management (start/stop/list) - **COMPLETED**
- [x] Detached mode support - **COMPLETED**
- [x] Container logging - **COMPLETED**
- [x] Graphical user interface (GUI) - **COMPLETED**
- Multiple container instances
- Port mapping (Docker `-p` equivalent)
- Custom network bridge configuration
- Configurable user namespace mapping (allow specifying host UID/GID)
- Image management system
- Container registry support

## References & Learning Resources

- Linux Namespaces: `man 7 namespaces`
- Cgroups v2: Kernel documentation
- chroot: `man 2 chroot`
- Docker Internals: Docker documentation
- Network Namespaces: Linux networking documentation

## Code Style & Conventions

- **Error Messages**: Descriptive, include context
- **Logging**: Uses `os.Stderr` for informational messages
- **Function Names**: Clear, descriptive (e.g., `setupNetwork`, `limitResources`)
- **Comments**: Explain why, not just what
- **Structure**: Logical grouping of related functionality

