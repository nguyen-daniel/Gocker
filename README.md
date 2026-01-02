# Gocker

A minimal Docker-clone implementation in Go using Linux namespaces, cgroups, and chroot. This educational project demonstrates how containerization works at a low level by creating isolated environments with separate hostnames, process IDs, filesystems, and resource limits.

## Project Structure

- **`main.go`** - Main implementation with namespace creation, cgroups setup, chroot jail, and command execution
- **`Makefile`** - Build automation and Alpine Linux rootfs management
- **`rootfs/`** - Alpine Linux mini rootfs directory (auto-downloaded on first run)

## Prerequisites

- Linux operating system (namespaces and cgroups are Linux-specific)
- Go 1.16 or later
- Root/sudo access (required for namespace and cgroup operations)
- `curl` and `tar` (for downloading Alpine rootfs)

## Installation

```bash
# Clone the repository
git clone <your-repo-url>
cd Gocker

# Ensure you have Go installed
go version
```

## Usage

### 1. Build the Project

Build the Gocker binary:

```bash
make build
```

This will compile `main.go` and create the `gocker` executable.

### 2. Run a Container

Run a container with an interactive shell:

```bash
make run
```

This command will:
- Build the binary if it doesn't exist
- Automatically download Alpine Linux mini rootfs to `rootfs/` if it doesn't exist
- Launch the container with `/bin/sh`

You can also run custom commands:

```bash
sudo ./gocker run /bin/ls -la
sudo ./gocker run /bin/ps aux
sudo ./gocker run /bin/hostname
```

### 3. Clean Up

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

### 2. Filesystem Isolation

- **Chroot**: Changes the root filesystem to `./rootfs` directory
- **Proc Mount**: Mounts a fresh `/proc` filesystem inside the container for process visibility

### 3. Resource Limits (Cgroups v2)

- Creates a cgroup at `/sys/fs/cgroup/gocker`
- Limits the container to a maximum of 20 processes
- Automatically assigns the container's PID to the cgroup

### 4. Execution Flow

1. Parent process (`run`) creates a child process with new namespaces
2. Child process (`child`) sets up:
   - Cgroups for resource limits
   - Hostname isolation
   - Chroot filesystem jail
   - Proc filesystem mount
3. User's command is executed inside the isolated environment

## Key Features

- **Namespace Isolation**: UTS, PID, and Mount namespaces for process and filesystem isolation
- **Cgroups v2 Integration**: Process limit enforcement (20 max processes)
- **Filesystem Jail**: Chroot-based filesystem isolation with Alpine Linux rootfs
- **Proc Filesystem**: Isolated `/proc` mount for container-specific process information
- **Clean Error Handling**: Graceful error handling with helpful messages
- **Automatic Rootfs Setup**: Makefile automatically downloads Alpine Linux rootfs

## Architecture Overview

```
Parent Process (run)
    │
    ├─ Creates child with namespaces (CLONE_NEWUTS | CLONE_NEWPID | CLONE_NEWNS)
    │
    └─ Child Process (child)
         │
         ├─ Setup cgroups v2 (/sys/fs/cgroup/gocker)
         ├─ Set hostname (gocker-container)
         ├─ Chroot to ./rootfs
         ├─ Mount /proc
         └─ Execute user command
```

## Limitations

This is an educational implementation and has several limitations compared to production container runtimes:

- No network namespace isolation
- No user namespace (runs as root)
- No image management system
- No container lifecycle management (start/stop/restart)
- Limited cgroup controls (only process limits)
- No volume mounting
- No container registry support

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

The Makefile will automatically download the rootfs on first run. If you encounter issues, manually download:

```bash
mkdir -p rootfs
curl -L https://dl-cdn.alpinelinux.org/alpine/v3.19/releases/x86_64/alpine-minirootfs-3.19.0-x86_64.tar.gz | tar -xz -C rootfs --strip-components=1
```

## Future Improvements

- [ ] Add network namespace isolation
- [ ] Implement user namespace for better security
- [ ] Add more cgroup controls (CPU, memory limits)
- [ ] Support for volume mounting
- [ ] Container image management
- [ ] Container lifecycle commands (start, stop, list, remove)
- [ ] Support for multiple container instances
- [ ] Logging and debugging features
- [ ] Support for different base images (not just Alpine)

## References

- [Linux Namespaces](https://man7.org/linux/man-pages/man7/namespaces.7.html) - Linux namespace documentation
- [Cgroups v2](https://www.kernel.org/doc/html/latest/admin-guide/cgroup-v2.html) - Control groups v2 documentation
- [chroot(2)](https://man7.org/linux/man-pages/man2/chroot.2.html) - Change root directory system call
- [Docker Internals](https://docs.docker.com/get-started/overview/) - Docker architecture overview

## License

This project is for educational purposes.
