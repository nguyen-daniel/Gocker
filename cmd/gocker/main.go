//go:build linux

package main

import (
	"fmt"
	"os"
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

	if topic, ok := helpRequest(os.Args[1:]); ok {
		printHelp(topic)
		os.Exit(0)
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
	case "exec":
		id, command, err := parseExecArgs(os.Args[2:])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			fmt.Print(execUsage)
			os.Exit(1)
		}
		execContainer(id, command)
	default:
		fmt.Printf("Unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
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
