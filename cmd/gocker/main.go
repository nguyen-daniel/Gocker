//go:build linux

package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"
)

func must(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	// Skip root check for "child" (runs inside the container namespaces),
	// "reap" (detached supervisor spawned by a privileged run), and for
	// explicit rootless/unprivileged mode used by tests.
	if os.Args[1] != "child" && os.Args[1] != "reap" && os.Geteuid() != 0 && !allowUnprivileged() {
		fmt.Println("Error: This program must be run with sudo/root permissions")
		fmt.Println("Hint: set GOCKER_ALLOW_UNPRIVILEGED=1 or pass --rootless to exercise user namespaces without root")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "run":
		run()
	case "child":
		child()
	case "reap":
		if len(os.Args) < 3 {
			fmt.Println("Error: container ID required")
			os.Exit(1)
		}
		reapContainer(os.Args[2])
	case "ps":
		listContainers()
	case "stop":
		if len(os.Args) < 3 {
			fmt.Println("Error: container ID required")
			fmt.Println("Usage: gocker stop <container-id>")
			os.Exit(1)
		}
		stopContainer(os.Args[2])
	case "rm":
		id, force, err := parseIDAndBoolFlag(os.Args[2:], "-f", "--force")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			fmt.Println("Usage: gocker rm [-f] <container-id>")
			os.Exit(1)
		}
		removeContainer(id, force)
	case "logs":
		id, follow, err := parseIDAndBoolFlag(os.Args[2:], "-f", "--follow")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			fmt.Println("Usage: gocker logs [-f] <container-id>")
			os.Exit(1)
		}
		showLogs(id, follow)
	default:
		fmt.Printf("Unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("Usage: gocker <command> [options]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  run              Run a new container")
	fmt.Println("  ps               List all containers")
	fmt.Println("  stop             Stop a running container")
	fmt.Println("  rm [-f]          Remove a container (-f / --force: SIGKILL if still running)")
	fmt.Println("  logs [-f]        Show container logs (-f / --follow: stream until exit)")
	fmt.Println()
	fmt.Println("Run options:")
	fmt.Println("  --cpu-limit <limit>         CPU limit (e.g. '1', '0.5', 'max')")
	fmt.Println("  --memory-limit <limit>      Memory limit (e.g. '512M', '1G', 'max')")
	fmt.Println("  --volume, -v <host:ctr>     Bind-mount a host path into the container")
	fmt.Println("  --detach, -d                Run container in background")
	fmt.Println("  --name <name>               Name for stop/rm/logs/ps (optional)")
	fmt.Println("  --quiet, -q                 Hide teaching logs; still prints the container ID when detached")
	fmt.Println("  --network <bridge|none>     Default bridge (veth+NAT). none: loopback only; skip host net setup")
	fmt.Println("  --rootfs <path>             Path to rootfs directory (default: ./rootfs)")
	fmt.Println("  --rootless                  Allow unprivileged run (user namespace; network/cgroups may fail)")
}

// allowUnprivileged reports whether the CLI may run without euid 0.
// Used to test the user-namespace path in CI without a full rootful stack.
func allowUnprivileged() bool {
	if os.Getenv("GOCKER_ALLOW_UNPRIVILEGED") == "1" {
		return true
	}
	for _, arg := range os.Args[2:] {
		if arg == "--rootless" {
			return true
		}
	}
	return false
}

func generateContainerID() string {
	randomBytes := make([]byte, 4)
	rand.Read(randomBytes)
	return hex.EncodeToString(randomBytes) + fmt.Sprintf("%d", time.Now().UnixNano())
}

func parseIDAndBoolFlag(args []string, short, long string) (id string, flag bool, err error) {
	for _, a := range args {
		if a == short || a == long {
			flag = true
			continue
		}
		if strings.HasPrefix(a, "-") {
			return "", false, fmt.Errorf("unknown flag: %s", a)
		}
		if id != "" {
			return "", false, fmt.Errorf("unexpected argument: %s", a)
		}
		id = a
	}
	if id == "" {
		return "", false, fmt.Errorf("container ID required")
	}
	return id, flag, nil
}
