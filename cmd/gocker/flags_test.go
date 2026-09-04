package main

import (
	"strings"
	"testing"
)

func TestParseRunFlags(t *testing.T) {
	opt, err := parseRunFlags([]string{
		"-d", "-q", "--name", "web", "--network=none",
		"-v", "/tmp/data:/data", "--cpu-limit", "0.5", "--memory-limit", "32M",
		"/bin/busybox", "echo", "hi",
	})
	if err != nil {
		t.Fatalf("parseRunFlags: %v", err)
	}
	if !opt.detached || !opt.quiet || opt.name != "web" || opt.network != "none" {
		t.Errorf("flags: %+v", opt)
	}
	if opt.cpuLimit != "0.5" || opt.memoryLimit != "32M" {
		t.Errorf("limits: cpu=%q mem=%q", opt.cpuLimit, opt.memoryLimit)
	}
	if len(opt.volumes) != 1 || opt.volumes[0] != "/tmp/data:/data" {
		t.Errorf("volumes: %v", opt.volumes)
	}
	if strings.Join(opt.command, " ") != "/bin/busybox echo hi" {
		t.Errorf("command: %v", opt.command)
	}

	_, err = parseRunFlags([]string{"--network", "host", "true"})
	if err == nil {
		t.Fatal("expected error for --network=host")
	}
}

func TestParseRunFlagsDefaultQuietAndTeach(t *testing.T) {
	opt, err := parseRunFlags([]string{"/bin/true"})
	if err != nil {
		t.Fatalf("default: %v", err)
	}
	if !opt.quiet {
		t.Fatal("default should be demo-quiet")
	}

	opt, err = parseRunFlags([]string{"--teach", "/bin/true"})
	if err != nil {
		t.Fatalf("teach: %v", err)
	}
	if opt.quiet {
		t.Fatal("--teach should enable teaching logs")
	}

	opt, err = parseRunFlags([]string{"--teach", "-q", "/bin/true"})
	if err != nil {
		t.Fatalf("teach+q: %v", err)
	}
	if !opt.quiet {
		t.Fatal("-q after --teach should win")
	}
}

func TestParseRunFlagsHelpAndJailArgv(t *testing.T) {
	opt, err := parseRunFlags([]string{"--help"})
	if err != nil {
		t.Fatalf("help: %v", err)
	}
	if !opt.help {
		t.Fatal("expected help on gocker run --help")
	}

	opt, err = parseRunFlags([]string{"/bin/busybox", "--help"})
	if err != nil {
		t.Fatalf("jail --help: %v", err)
	}
	if opt.help {
		t.Fatal("busybox --help must be jail argv, not gocker help")
	}
	if strings.Join(opt.command, " ") != "/bin/busybox --help" {
		t.Errorf("command: %v", opt.command)
	}
}

func TestParseRunFlagsUnknown(t *testing.T) {
	_, err := parseRunFlags([]string{"--bogus", "/bin/true"})
	if err == nil {
		t.Fatal("expected unknown flag error")
	}
}

func TestParseIDAndBoolFlag(t *testing.T) {
	id, force, err := parseIDAndBoolFlag([]string{"-f", "abc123def456"}, "-f", "--force")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if id != "abc123def456" || !force {
		t.Errorf("id=%q force=%v", id, force)
	}

	_, _, err = parseIDAndBoolFlag([]string{"--help"}, "-f", "--force")
	if err == nil {
		t.Fatal("expected unknown flag for --help at this layer")
	}
}

func TestParseExecArgs(t *testing.T) {
	id, cmd, err := parseExecArgs([]string{"web", "/bin/busybox", "echo", "hi"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if id != "web" || strings.Join(cmd, " ") != "/bin/busybox echo hi" {
		t.Errorf("id=%q cmd=%v", id, cmd)
	}
	if _, _, err := parseExecArgs([]string{"web"}); err == nil {
		t.Fatal("expected command required")
	}
	if _, _, err := parseExecArgs([]string{"--foo", "web", "true"}); err == nil {
		t.Fatal("expected unknown flag")
	}
}
