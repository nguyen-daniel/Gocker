package main

import (
	"testing"
)

func TestHelpRequest(t *testing.T) {
	cases := []struct {
		args  []string
		topic string
		ok    bool
	}{
		{[]string{"--help"}, "", true},
		{[]string{"-h"}, "", true},
		{[]string{"help"}, "", true},
		{[]string{"help", "run"}, "run", true},
		{[]string{"run", "--help"}, "run", true},
		{[]string{"run", "-h"}, "run", true},
		{[]string{"run", "-q", "--help"}, "run", true},
		{[]string{"run", "--name", "web", "--help"}, "run", true},
		{[]string{"run", "/bin/busybox", "--help"}, "", false},
		{[]string{"run", "--", "--help"}, "", false},
		{[]string{"ps", "--help"}, "ps", true},
		{[]string{"stop", "-h"}, "stop", true},
		{[]string{"rm", "--force", "--help"}, "rm", true},
		{[]string{"logs", "-f", "--help"}, "logs", true},
		{[]string{"exec", "--help"}, "exec", true},
		{[]string{"exec", "-h"}, "exec", true},
		{[]string{"help", "exec"}, "exec", true},
		{[]string{"exec", "web", "--help"}, "", false},
		{[]string{"exec", "web", "/bin/busybox", "echo", "hi"}, "", false},
		{[]string{"run", "-p", "8080:80", "--help"}, "run", true},
		{[]string{"run", "-p", "8080:80", "/bin/busybox", "--help"}, "", false},
		{[]string{"run", "/bin/busybox", "echo", "hi"}, "", false},
		{[]string{"stop", "abc123"}, "", false},
	}
	for _, tc := range cases {
		topic, ok := helpRequest(tc.args)
		if ok != tc.ok || topic != tc.topic {
			t.Errorf("helpRequest(%v)=(%q,%v), want (%q,%v)", tc.args, topic, ok, tc.topic, tc.ok)
		}
	}
}

func TestIsHelpArg(t *testing.T) {
	if !isHelpArg("--help") || !isHelpArg("-h") {
		t.Fatal("expected --help and -h to be help")
	}
	if isHelpArg("help") || isHelpArg("--teach") {
		t.Fatal("help command name and --teach are not help flags")
	}
}
