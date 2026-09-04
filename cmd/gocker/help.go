package main

import (
	"fmt"
	"strings"
)

func isHelpArg(s string) bool {
	return s == "--help" || s == "-h"
}

func isUserCommand(s string) bool {
	switch s {
	case "run", "ps", "stop", "rm", "logs":
		return true
	}
	return false
}

// helpRequest reports whether argv (os.Args[1:]) asked for help.
// topic is "" for global help, or a command name.
// --help after the jail command (gocker run /bin/busybox --help) is not help.
func helpRequest(args []string) (topic string, ok bool) {
	if len(args) == 0 {
		return "", false
	}
	if isHelpArg(args[0]) {
		return "", true
	}
	if args[0] == "help" {
		if len(args) > 1 && isUserCommand(args[1]) {
			return args[1], true
		}
		return "", true
	}
	cmd := args[0]
	rest := args[1:]
	switch cmd {
	case "run":
		if runHelpRequested(rest) {
			return "run", true
		}
	case "ps", "stop", "rm", "logs":
		for _, a := range rest {
			if isHelpArg(a) {
				return cmd, true
			}
		}
	}
	return "", false
}

func runHelpRequested(args []string) bool {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return false
		}
		if isHelpArg(arg) {
			return true
		}
		switch arg {
		case "--cpu-limit", "--memory-limit", "--volume", "-v", "--rootfs", "--name", "--network":
			i++
		case "--detach", "-d", "--quiet", "-q", "--rootless", "--teach":
		default:
			if strings.HasPrefix(arg, "--network=") {
				continue
			}
			if strings.HasPrefix(arg, "-") {
				continue
			}
			return false
		}
	}
	return false
}

func printHelp(topic string) {
	switch topic {
	case "run":
		printRunUsage()
	case "ps":
		fmt.Print(psUsage)
	case "stop":
		fmt.Print(stopUsage)
	case "rm":
		fmt.Print(rmUsage)
	case "logs":
		fmt.Print(logsUsage)
	default:
		printUsage()
	}
}

func printUsage() {
	fmt.Print(globalUsage)
}

func printRunUsage() {
	fmt.Print(runUsage)
}

const globalUsage = `Usage: gocker <command> [options]

Commands:
  run              Run a new container
  ps               List containers
  stop             Stop a running container
  rm [-f]          Remove a container
  logs [-f]        Show container logs

Run 'gocker <command> --help' for flags.
Linux + root required (except --help). Try: sudo make demo
`

const runUsage = `Usage: gocker run [options] <command> [args...]

  --cpu-limit <n>          CPU limit (e.g. '1', '0.5', 'max')
  --memory-limit <size>    Memory limit (e.g. '512M', '1G', 'max')
  --volume, -v <host:ctr>  Bind-mount a host path into the container
  --detach, -d             Run container in background
  --name <name>            Name for stop/rm/logs/ps (optional)
  --quiet, -q              Hide teaching logs (default)
  --teach                  Verbose teaching logs (namespaces, overlay, cgroup)
  --network <bridge|none>  Default bridge (veth+NAT). none: loopback only
  --rootfs <path>          Path to rootfs directory (default: ./rootfs)
  --rootless               Allow unprivileged run (user namespace)

Prints a 12-char id on every run (stderr when foreground; stdout when -d).
`

const psUsage = `Usage: gocker ps

List saved containers (laptop-width columns: ID, NAME, STATUS, PID, IP, COMMAND).
IDs are 12-char hex prefixes; names and unique prefixes resolve the same way.
`

const stopUsage = `Usage: gocker stop <container-id|name>

Send SIGTERM, then SIGKILL if needed. Status becomes stopped.
`

const rmUsage = `Usage: gocker rm [-f|--force] <container-id|name>

Delete overlay + state. Refuses a live container unless -f / --force (SIGKILL).
`

const logsUsage = `Usage: gocker logs [-f|--follow] <container-id|name>

Print the container log file. -f / --follow streams until the container exits.
`
