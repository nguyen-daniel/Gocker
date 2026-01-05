# Gocker

A minimal Docker-clone implementation in Go using Linux namespaces, cgroups, and chroot. This educational project demonstrates how containerization works at a low level by creating isolated environments with separate hostnames, process IDs, filesystems, and resource limits.

## Project Structure

- **`main.go`** - Main implementation with namespace creation, cgroups setup, chroot jail, and command execution
- **`main_test.go`** - Integration tests for container functionality
- **`Makefile`** - Build automation, testing, and Alpine Linux rootfs management
- **`.github/workflows/main.yml`** - CI/CD pipeline with automated testing
- **`rootfs/`** - Alpine Linux mini rootfs directory (auto-downloaded on first run)

## Prerequisites

- Linux operating system (namespaces and cgroups are Linux-specific)
- Go 1.16 or later
- Docker (for setting up Alpine rootfs via `docker export`)
- Root/sudo access (required for namespace and cgroup operations)

## Installation

```bash
# Clone the repository
git clone <your-repo-url>
cd Gocker

# Ensure you have Go installed
go version
```

## Usage

### 1. Setup Rootfs

Set up the Alpine Linux rootfs (required for chroot-based filesystem isolation):

```bash
make setup
```

This will:
- Pull the Alpine Linux Docker image
- Create a temporary container
- Export and extract the filesystem to `rootfs/` using `docker export`

**Note:** The rootfs is necessary because Gocker uses chroot to create filesystem isolation. The rootfs provides a minimal Linux environment inside the container.

### 2. Build the Project

Build the Gocker binary:

```bash
make build
```

This will compile `main.go` and create the `gocker` executable.

### 3. Run Tests

Run the integration tests (requires sudo for namespace operations):

```bash
make test
```

**Note:** Tests require sudo because Linux namespaces (CLONE_NEWUTS, CLONE_NEWPID, CLONE_NEWNS) and cgroups operations require root privileges for container isolation.

### 4. Run a Container

Run a container with an interactive shell:

```bash
make run
```

This command will:
- Build the binary if it doesn't exist
- Automatically set up Alpine Linux rootfs to `rootfs/` if it doesn't exist
- Launch the container with `/bin/sh`

You can also run custom commands:

   ```bash
   # Standard commands (PATH is set in the container)
   sudo ./gocker run /bin/ls -la /
   sudo ./gocker run /bin/ps aux
   sudo ./gocker run /bin/hostname
   sudo ./gocker run /bin/echo "Hello from container!"
   
   # Or use busybox directly (always works)
   sudo ./gocker run /bin/busybox ls -la /
   sudo ./gocker run /bin/busybox ps aux
   sudo ./gocker run /bin/busybox hostname
   
   # Network testing (requires network namespace setup)
   sudo ./gocker run /bin/busybox ip addr show
   sudo ./gocker run /bin/busybox ip route show
   sudo ./gocker run /bin/busybox ping -c 3 10.0.0.1
   ```

### 5. Clean Up

Remove the built binary:

```bash
make clean
```

## How It Works

Gocker creates isolated containers using several Linux features:

### 1. Linux Namespaces

The container runs in isolated namespaces:
- **UTS Namespace**: Isolated hostname (set to `gocker-container`)
- **PID Namespace**: Separate process ID space (processes see PID 1 as the first process)
- **Mount Namespace**: Isolated filesystem mount points
- **Network Namespace**: Isolated network stack with its own network interfaces, IP addresses, and routing tables
- **User Namespace**: Isolated user and group ID space for enhanced security

### 2. User Namespace Isolation

- **UID/GID Mapping**: Container's root user (UID 0) is mapped to an unprivileged user on the host (typically UID 1000)
- **Security Enhancement**: Even if a process escapes the container, it runs as a non-root user on the host
- **Range Mapping**: Container UIDs 0-1000 are mapped to host UIDs 1000-2000 (when running as root)
- **File Permissions**: File permissions work correctly within the container namespace, with container root having full privileges inside the container

### 3. Network Isolation

- **Virtual Ethernet Pair (veth)**: Creates a veth pair to connect the container to the host network
- **IP Configuration**: Container receives IP address `10.0.0.2/24`, host end is `10.0.0.1/24`
- **NAT Masquerading**: Uses iptables NAT to enable internet connectivity from the container
- **Automatic Cleanup**: Network interfaces and iptables rules are cleaned up when the container exits

### 4. Filesystem Isolation

- **Chroot**: Changes the root filesystem to `./rootfs` directory
- **Proc Mount**: Mounts a fresh `/proc` filesystem inside the container for process visibility

### 5. Resource Limits (Cgroups v2)

- Creates a cgroup at `/sys/fs/cgroup/gocker`
- Limits the container to a maximum of 20 processes
- Automatically assigns the container's PID to the cgroup

### 6. Execution Flow

1. Parent process (`run`) creates a child process with new namespaces (including network and user namespaces)
2. Parent process sets up user namespace:
   - Writes UID/GID mappings to `/proc/<pid>/uid_map` and `/proc/<pid>/gid_map`
   - Maps container root (UID 0) to unprivileged host user (UID 1000)
3. Parent process sets up network:
   - Creates veth pair (host and container ends)
   - Moves container veth into child's network namespace
   - Configures host IP and NAT rules
4. Child process (`child`) sets up:
   - Verifies user namespace mapping (sees itself as UID 0 in container)
   - Cgroups for resource limits
   - Network interface configuration (IP address, routing)
   - Hostname isolation
   - Chroot filesystem jail
   - Proc filesystem mount
5. User's command is executed inside the isolated environment
6. On exit, parent process cleans up network interfaces and iptables rules

## Key Features

- **Namespace Isolation**: UTS, PID, Mount, Network, and User namespaces for complete isolation
- **User Namespace Security**: Container root is mapped to unprivileged host user, enhancing security
- **Network Isolation**: Each container has its own network namespace with veth pair connectivity
- **Internet Connectivity**: NAT masquerading enables containers to access the internet
- **Cgroups v2 Integration**: Process limit enforcement (20 max processes)
- **Filesystem Jail**: Chroot-based filesystem isolation with Alpine Linux rootfs
- **Proc Filesystem**: Isolated `/proc` mount for container-specific process information
- **Automatic Cleanup**: Network interfaces and rules are automatically cleaned up on container exit
- **Clean Error Handling**: Graceful error handling with helpful messages
- **Automated Testing**: Integration tests with CI/CD pipeline
- **Docker-based Setup**: Uses `docker export` for reliable rootfs creation

## Architecture Overview

```
Parent Process (run)
    │
    ├─ Creates child with namespaces (CLONE_NEWUTS | CLONE_NEWPID | CLONE_NEWNS | CLONE_NEWNET | CLONE_NEWUSER)
    │
    ├─ Setup user namespace:
    │   ├─ Write UID mapping (container 0 -> host 1000)
    │   └─ Write GID mapping (container 0 -> host 1000)
    │
    ├─ Setup network:
    │   ├─ Create veth pair (veth<pid> <-> vethc<pid>)
    │   ├─ Move container veth into child namespace
    │   ├─ Configure host IP (10.0.0.1/24)
    │   └─ Setup NAT and forwarding rules
    │
    └─ Child Process (child)
         │
         ├─ Verify user namespace (sees UID 0 in container)
         ├─ Setup cgroups v2 (/sys/fs/cgroup/gocker)
         ├─ Configure network (IP: 10.0.0.2/24, default route)
         ├─ Set hostname (gocker-container)
         ├─ Chroot to ./rootfs
         ├─ Mount /proc
         └─ Execute user command
```

## Testing

Gocker includes integration tests that verify container functionality:

```bash
make test
```

The test suite includes:
- **Container Execution Test**: Verifies that commands can be executed inside the container
- **Hostname Isolation Test**: Verifies UTS namespace isolation

**Important:** Tests must run with sudo because:
1. Linux namespaces (CLONE_NEWUTS, CLONE_NEWPID, CLONE_NEWNS, CLONE_NEWNET, CLONE_NEWUSER) require root privileges
2. User namespace UID/GID mapping requires root to write to /proc/<pid>/uid_map and /proc/<pid>/gid_map
3. Network interface creation and configuration require root privileges
4. iptables rules for NAT require root privileges
5. Cgroups v2 operations require root to create directories and write limits
6. Chroot operations require root to change the root filesystem
7. Mounting /proc requires root privileges

### CI/CD

The project includes a GitHub Actions workflow (`.github/workflows/main.yml`) that:
- Runs on every push to main/master/develop branches
- Sets up Go and Docker
- Runs `make setup` to prepare the rootfs
- Runs `make test` with sudo privileges
- Ensures all tests pass before merging

## Limitations

This is an educational implementation and has several limitations compared to production container runtimes:

- No image management system
- No container lifecycle management (start/stop/restart)
- Limited cgroup controls (only process limits, no CPU/memory limits)
- No volume mounting
- No container registry support
- Network setup requires `ip` command and `iptables` (may not work in all environments)
- User namespace mapping is fixed (maps to UID 1000 when running as root, current user otherwise)

## Troubleshooting

### Permission Denied Errors

Ensure you're running with sudo:
```bash
sudo ./gocker run /bin/sh
```

### Cgroup Errors

Make sure your system uses cgroups v2:
```bash
mount | grep cgroup
```

If you see `cgroup2`, you're using v2. If you see `cgroup`, you may need to enable cgroups v2 in your kernel.

### Rootfs Not Found

The Makefile will automatically set up the rootfs on first run using `make setup`. If you encounter issues:

1. Ensure Docker is installed and running:
```bash
docker --version
docker ps
```

2. Manually run the setup:
```bash
make setup
```

3. If Docker is not available, you can manually download the rootfs (alternative method):
```bash
mkdir -p rootfs
curl -L https://dl-cdn.alpinelinux.org/alpine/v3.19/releases/x86_64/alpine-minirootfs-3.19.0-x86_64.tar.gz | tar -xz -C rootfs --strip-components=1
```

### Network Issues

If network connectivity doesn't work in containers:

1. **Check if `ip` command is available:**
   ```bash
   which ip
   # Should show /usr/bin/ip or /sbin/ip
   ```

2. **Check if `iptables` is available (for NAT):**
   ```bash
   which iptables
   # Should show /usr/bin/iptables or /sbin/iptables
   ```

3. **Verify IP forwarding is enabled:**
   ```bash
   sysctl net.ipv4.ip_forward
   # Should show: net.ipv4.ip_forward = 1
   ```

4. **Check for existing network interfaces:**
   ```bash
   ip link show
   # Look for veth interfaces that might not have been cleaned up
   ```

5. **Manually clean up if needed:**
   ```bash
   # Remove leftover veth interfaces
   sudo ip link delete veth<pid>
   
   # Remove iptables rules (if needed)
   sudo iptables -t nat -L -n -v | grep 10.0.0.0/24
   ```

### User Namespace Issues

If you encounter issues with user namespace:

1. **Check if user namespaces are enabled:**
   ```bash
   cat /proc/sys/user/max_user_namespaces
   # Should show a number > 0 (e.g., 15000)
   ```

2. **Verify UID/GID mapping:**
   ```bash
   # While container is running, check the mapping
   cat /proc/<container_pid>/uid_map
   cat /proc/<container_pid>/gid_map
   ```

3. **File permission issues:**
   - Files in the rootfs should be accessible by the mapped user
   - If you see permission denied errors, check file ownership in rootfs
   - Container root (UID 0) has full privileges within the container namespace

4. **Mapping conflicts:**
   - If UID 1000 is already in use, the mapping might conflict
   - The implementation maps container UID 0 to host UID 1000 when running as root
   - When running as a non-root user, it maps to your current UID

### Test Failures

If tests fail, ensure:
- You're running tests with `make test` (which uses sudo automatically)
- Docker is installed and the daemon is running
- The rootfs directory exists (run `make setup` first)
- Your system supports cgroups v2
- Network tools (`ip`, `iptables`) are available on the system
- User namespaces are enabled in the kernel (`/proc/sys/user/max_user_namespaces > 0`)

## Future Improvements

- [x] Add network namespace isolation
- [x] Implement user namespace for better security
- [ ] Add more cgroup controls (CPU, memory limits)
- [ ] Support for volume mounting
- [ ] Container image management
- [ ] Container lifecycle commands (start, stop, list, remove)
- [ ] Support for multiple container instances
- [ ] Logging and debugging features
- [ ] Support for different base images (not just Alpine)
- [ ] Network port mapping (similar to Docker's -p flag)
- [ ] Custom network bridge configuration
- [ ] Configurable user namespace mapping (allow specifying host UID/GID)

## References

- [Linux Namespaces](https://man7.org/linux/man-pages/man7/namespaces.7.html) - Linux namespace documentation
- [Cgroups v2](https://www.kernel.org/doc/html/latest/admin-guide/cgroup-v2.html) - Control groups v2 documentation
- [chroot(2)](https://man7.org/linux/man-pages/man2/chroot.2.html) - Change root directory system call
- [Docker Internals](https://docs.docker.com/get-started/overview/) - Docker architecture overview

## License

This project is for educational purposes.
