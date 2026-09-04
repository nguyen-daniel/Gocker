package main

import (
	"fmt"
	"strings"
	"unicode"
)

type runOptions struct {
	cpuLimit    string
	memoryLimit string
	rootfsPath  string
	network     string
	name        string
	volumes     []string
	detached    bool
	quiet       bool
	help        bool
	command     []string
}

func parseRunFlags(args []string) (runOptions, error) {
	var opt runOptions
	opt.network = "bridge"
	opt.quiet = true // demo-quiet default; --teach enables teaching dumps
	var rest []string

	for i := 0; i < len(args); i++ {
		arg := args[i]
		needVal := func(flag string) (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s requires a value", flag)
			}
			i++
			return args[i], nil
		}
		switch {
		case arg == "--help" || arg == "-h":
			opt.help = true
		case arg == "--cpu-limit":
			v, err := needVal(arg)
			if err != nil {
				return opt, err
			}
			opt.cpuLimit = v
		case arg == "--memory-limit":
			v, err := needVal(arg)
			if err != nil {
				return opt, err
			}
			opt.memoryLimit = v
		case arg == "--volume" || arg == "-v":
			v, err := needVal(arg)
			if err != nil {
				return opt, err
			}
			opt.volumes = append(opt.volumes, v)
		case arg == "--detach" || arg == "-d":
			opt.detached = true
		case arg == "--quiet" || arg == "-q":
			opt.quiet = true
		case arg == "--teach":
			opt.quiet = false
		case arg == "--rootless":
			// Consumed so it is not treated as the container command.
			// Root check is handled in main() via allowUnprivileged().
		case arg == "--rootfs":
			v, err := needVal(arg)
			if err != nil {
				return opt, err
			}
			opt.rootfsPath = v
		case arg == "--name":
			v, err := needVal(arg)
			if err != nil {
				return opt, err
			}
			opt.name = v
		case arg == "--network":
			v, err := needVal(arg)
			if err != nil {
				return opt, err
			}
			opt.network = v
		case strings.HasPrefix(arg, "--network="):
			opt.network = strings.TrimPrefix(arg, "--network=")
		case arg == "--":
			rest = append(rest, args[i+1:]...)
			i = len(args)
		default:
			if strings.HasPrefix(arg, "-") {
				return opt, fmt.Errorf("unknown flag: %s", arg)
			}
			// First non-flag is the jail command; later dashed args are argv
			// (so `gocker run /bin/busybox --help` is not gocker help).
			rest = append(rest, args[i:]...)
			i = len(args)
		}
	}

	opt.command = rest
	if opt.network == "" {
		opt.network = "bridge"
	}
	if opt.network != "bridge" && opt.network != "none" {
		return opt, fmt.Errorf("unknown --network mode %q (supported: bridge, none)", opt.network)
	}
	if opt.name != "" && !validContainerName(opt.name) {
		return opt, fmt.Errorf("invalid container name %q", opt.name)
	}
	return opt, nil
}

func validContainerName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for i, r := range name {
		ok := unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '.'
		if !ok {
			return false
		}
		if i == 0 && (r == '.' || r == '-') {
			return false
		}
	}
	return true
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
