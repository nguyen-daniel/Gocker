# Gocker Infrastructure & Architecture

## Project Overview

Gocker is a minimal Docker-clone implementation in Go that demonstrates containerization using Linux primitives: namespaces, cgroups v2, and chroot. It's an educational project that shows how container runtimes work at a low level.

## Project Structure

```
Gocker/
├── main.go              # Main implementation (namespace creation, cgroups, chroot, network setup, lifecycle management)
├── gui.go               # Graphical user interface (Fyne-based GUI)
├── main_test.go         # Integration tests (requires sudo)
├── go.mod               # Go module definition (Go 1.21+)
├── go.sum               # Go module checksums
├── Makefile             # Build automation, rootfs setup, testing
├── README.md            # Comprehensive documentation
├── GUI_FRAMEWORKS.md    # GUI framework comparison and documentation
├── rootfs/              # Alpine Linux mini rootfs (auto-downloaded via Docker)
│   ├── bin/             # Busybox binaries and utilities
│   ├── etc/             # System configuration files
│   ├── lib/             # Shared libraries (musl libc)
│   ├── usr/             # User binaries and libraries
│   └── ...              # Standard Linux filesystem structure
└── .cursor/             # IDE context files (this directory)
```

## Architecture

### Process Flow

```
Parent Process (run)
    │
    ├─ Validates root permissions
    ├─ Creates child with namespaces:
    │   ├─ CLONE_NEWUTS  (hostname isolation)
    │   ├─ CLONE_NEWPID  (process ID isolation)
    │   ├─ CLONE_NEWNS   (mount namespace)
    │   ├─ CLONE_NEWNET  (network namespace)
    │   └─ CLONE_NEWUSER (user namespace)
    │
    ├─ User Namespace Setup:
    │   ├─ Writes UID mapping (container 0 -> host 1000)
    │   └─ Writes GID mapping (container 0 -> host 1000)
    │
    ├─ Network Setup (before child starts):
    │   ├─ Creates veth pair (veth<pid> <-> vethc<pid>)
    │   ├─ Moves container veth into child's network namespace
    │   ├─ Configures host IP (10.0.0.1/24)
    │   ├─ Enables IP forwarding
    │   └─ Sets up NAT masquerading via iptables
    │
    └─ Waits for child, then cleans up network on exit

Child Process (child)
    │
    ├─ Verifies user namespace (sees UID 0 in container)
    │
    ├─ Sets up cgroups v2 (/sys/fs/cgroup/gocker)
    │   ├─ Limits to 20 max processes (pids.max)
    │   ├─ CPU limits (cpu.max) if specified
    │   └─ Memory limits (memory.max) if specified
    │
    ├─ Configures container network:
    │   ├─ Brings up veth interface
    │   ├─ Assigns IP (10.0.0.2/24)
    │   └─ Sets default route via 10.0.0.1
    │
    ├─ Sets hostname to "gocker-container"
    │
    ├─ Mounts volumes (bind mounts from host to container paths)
    │
    ├─ Chroot to ./rootfs (filesystem isolation)
    │
    ├─ Mounts /proc filesystem
    │
    └─ Executes user command with PATH set
    │
    └─ Parent saves container state to /var/lib/gocker/containers/<id>.json
```

## Key Components

### 1. Namespace Isolation

- **UTS Namespace**: Isolated hostname (`gocker-container`)
- **PID Namespace**: Separate process ID space (PID 1 in container)
- **Mount Namespace**: Isolated filesystem mount points
- **Network Namespace**: Isolated network stack with veth pair
- **User Namespace**: Isolated user/group ID space (container root → host UID 1000)

### 2. Network Architecture

- **Virtual Ethernet Pair (veth)**: Connects container to host
  - Host end: `veth<pid>` with IP `10.0.0.1/24`
  - Container end: `vethc<pid>` with IP `10.0.0.2/24`
- **NAT Masquerading**: Enables internet connectivity via iptables
- **IP Forwarding**: Enabled on host for routing
- **Automatic Cleanup**: Network interfaces and iptables rules removed on exit

### 3. Filesystem Isolation

- **Chroot**: Root filesystem changed to `./rootfs` directory
- **Volume Mounting**: Bind mount host directories into container
  - Format: `--volume /host/path:/container/path` or `-v /host/path:/container/path`
  - Mounted before chroot using `MS_BIND | MS_REC` flags
  - Mount propagation set to `MS_PRIVATE | MS_REC` for isolation
  - Supports both directory and file mounts
- **Proc Mount**: Fresh `/proc` mounted inside container
- **Alpine Linux Rootfs**: Minimal Linux environment with busybox

### 4. Resource Limits (Cgroups v2)

- **Location**: `/sys/fs/cgroup/gocker`
- **Process Limit**: Maximum 20 processes (default, `pids.max`)
- **CPU Limit**: Configurable via `--cpu-limit` flag (`cpu.max` controller)
  - Format: number (e.g., "1" for 1 CPU, "0.5" for 50% of one CPU) or "max"
  - Converted to "quota period" format in microseconds
- **Memory Limit**: Configurable via `--memory-limit` flag (`memory.max` controller)
  - Format: size with unit (e.g., "512M", "1G") or "max"
  - Converted to bytes
- **Automatic Assignment**: Container PID added to cgroup

### 5. User Namespace Security

- **UID Mapping**: Container UID 0 (root) → Host UID 1000 (unprivileged)
- **GID Mapping**: Container GID 0 (root) → Host GID 1000 (unprivileged)
- **Range Mapping**: Container UIDs 0-1000 → Host UIDs 1000-2000
- **Security Benefit**: Even if process escapes, it runs as non-root on host
- **File Permissions**: Work correctly within container namespace

## Technology Stack

- **Language**: Go 1.21+
- **OS Requirements**: Linux (namespaces and cgroups are Linux-specific)
- **Base Image**: Alpine Linux (via Docker export)
- **System Tools**: `ip`, `iptables`, `sysctl` (for network setup)
- **Build Tool**: Make
- **Testing**: Go testing framework (requires sudo)

## Dependencies

- **External Tools** (required at runtime):
  - `ip` command (for network interface management)
  - `iptables` (for NAT masquerading)
  - Docker (for rootfs setup via `make setup`)

- **Go Dependencies**:
  - `fyne.io/fyne/v2` - GUI framework (for `gocker gui` command)

## Build & Execution

### Build Process
1. `make build` - Compiles `main.go` to `gocker` binary
2. `make setup` - Downloads Alpine rootfs via Docker export
3. `make test` - Runs integration tests (requires sudo)
4. `make run` - Builds and runs container with `/bin/sh`

### Execution Requirements
- **Root/sudo access**: Required for all operations (namespaces, cgroups, chroot, network)
- **Cgroups v2**: System must use cgroups v2 (not v1)
- **Network tools**: `ip` and `iptables` must be available
- **Docker**: Required for initial rootfs setup

## File Responsibilities

- **main.go**:
  - `main()`: Entry point, command routing, root permission check
  - `run()`: Parent process, creates child with namespaces, sets up user/network namespaces, container state management
  - `child()`: Container setup (user namespace verification, cgroups, hostname, network, volumes, chroot, proc mount, command execution)
  - `setupUserNamespace()`: Writes UID/GID mappings to /proc/<pid>/uid_map and /proc/<pid>/gid_map
  - `setupNetwork()`: Creates veth pair, configures host networking, NAT setup
  - `configureContainerNetwork()`: Configures network inside container namespace
  - `cleanupNetwork()`: Removes veth interfaces and iptables rules
  - `limitResources()`: Sets up cgroups v2 with process, CPU, and memory limits
  - `parseCPULimit()`: Parses CPU limit string and converts to cgroup v2 format
  - `parseMemoryLimit()`: Parses memory limit string and converts to bytes
  - `mountVolumes()`: Binds mount host directories into container rootfs
  - `getDefaultInterface()`: Finds default network interface for NAT
  - `generateContainerID()`: Generates unique container ID
  - `saveContainerState()`: Saves container metadata to JSON file
  - `loadContainerState()`: Loads container metadata from JSON file
  - `updateContainerStatus()`: Updates container status in state file
  - `listContainers()`: Lists all containers with their status
  - `stopContainer()`: Stops a running container (SIGTERM, then SIGKILL)
  - `removeContainer()`: Removes a stopped container and its state
  - `showLogs()`: Displays container logs
  - `must()`: Error handling helper

- **gui.go**:
  - `NewGockerGUI()`: Creates new GUI application instance
  - `Run()`: Starts the GUI application
  - `setupUI()`: Creates the main UI layout
  - `createLeftPanel()`: Creates container list and details panel
  - `createRightPanel()`: Creates log viewer panel
  - `createBottomPanel()`: Creates container creation form
  - `refreshContainers()`: Refreshes the container list
  - `loadAllContainers()`: Loads all containers from state directory
  - `showContainerDetails()`: Displays container details and logs
  - `createContainer()`: Creates a new container via GUI
  - `stopSelectedContainer()`: Stops the selected container
  - `removeSelectedContainer()`: Removes the selected container

- **main_test.go**:
  - `TestGockerRun()`: Integration test for basic container execution
  - `TestGockerRunWithHostname()`: Tests UTS namespace isolation

- **Makefile**:
  - `build`: Compiles Go binary
  - `setup`: Downloads Alpine rootfs via Docker
  - `test`: Runs tests with sudo
  - `run`: Builds and runs container
  - `clean`: Removes binary

## Network Configuration Details

- **Container IP**: `10.0.0.2/24`
- **Host IP**: `10.0.0.1/24`
- **Network**: `10.0.0.0/24`
- **Default Route**: Via `10.0.0.1` (host end of veth pair)
- **NAT**: Masquerading on default interface for internet access

## Important Implementation Notes

1. **User Namespace Setup**: UID/GID mappings must be written by parent (as root) before child performs privileged operations. Child blocks on privileged operations until mapping is written.
2. **Network Setup Timing**: Network is configured in parent process before child starts, then container network is configured in child process before chroot
3. **Chroot Order**: Network must be configured before chroot (needs access to `/proc` and network tools)
4. **PID Handling**: Child PID is captured before CLONE_NEWPID takes effect (for network and user namespace setup)
5. **Error Handling**: Network and user namespace setup failures are warnings (container continues)
6. **Cleanup**: Network interfaces and iptables rules are cleaned up on container exit via defer
7. **Security**: Container root (UID 0) is mapped to unprivileged host user (UID 1000) for enhanced security

## Container Lifecycle Management

- **State Directory**: `/var/lib/gocker/containers/` (JSON files)
- **Log Directory**: `/var/lib/gocker/logs/` (log files)
- **Container ID**: Generated using nanosecond timestamp
- **Status Tracking**: running, stopped, exited
- **Commands**: `ps`, `stop`, `rm`, `logs`, `gui`
- **Detached Mode**: Containers can run in background with `--detach` or `-d` flag

## GUI Implementation

- **Framework**: Fyne v2 (cross-platform GUI toolkit)
- **Command**: `gocker gui` launches graphical interface
- **Features**: Container list, creation form, details panel, log viewer, stop/remove actions
- **Auto-refresh**: Container list refreshes every 2 seconds

## Limitations

- No image management system
- No container registry support
- Single container instance (no multi-container support)
- User namespace mapping is fixed (maps to UID 1000 when running as root)
- Network port mapping not yet implemented

